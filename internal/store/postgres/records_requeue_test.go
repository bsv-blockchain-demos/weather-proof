package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

var (
	_ func(context.Context, time.Duration, int) ([]store.Record, error) = (*postgres.RecordStore)(nil).ReapExpired
	_ func(context.Context, store.RequeueFilter) (int64, error)         = (*postgres.RecordStore)(nil).Requeue
)

// TestReapExpiredReclaimsOnlyStrandedRows uses a BACKDATED claimed_at and no
// sleeps at all.
//
// Never sleep for the lease. The production lease is five minutes, so sleeping
// is a non-starter, and shortening the lease to something sleepable is the
// classic timing flake. Writing claimed_at directly is deterministic, instant,
// and needs no timer.
func TestReapExpiredReclaimsOnlyStrandedRows(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	priorRef := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
		VALUES ('stranded', 1, now(), now(), $1, 'processing', now() - interval '9 minutes', $2)`,
		fullWeatherData(), priorRef); err != nil {
		t.Fatalf("seeding the stranded row: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
		VALUES ('inflight', 2, now(), now(), $1, 'processing', now() - interval '30 seconds', $2)`,
		fullWeatherData(), uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("seeding the in-flight row: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
		VALUES ('fresh', 3, now(), now(), $1)`, fullWeatherData()); err != nil {
		t.Fatalf("seeding the never-claimed row: %v", err)
	}

	reaped, err := rs.ReapExpired(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("ReapExpired: %v", err)
	}
	if len(reaped) != 1 {
		got := make([]string, 0, len(reaped))
		for _, r := range reaped {
			got = append(got, r.ID)
		}
		t.Fatalf("reaped %v, want exactly [stranded]", got)
	}
	r := reaped[0]
	if r.ID != "stranded" {
		t.Fatalf("reaped %q, want stranded", r.ID)
	}
	if r.Status != store.StatusPending {
		t.Errorf("reaped status = %q, want pending", string(r.Status))
	}
	if !r.AdoptRequired {
		t.Error("reaped row must have adopt_required true: it is the only signal that " +
			"tells a reaped row apart from a never-claimed one, and without it a row " +
			"whose prior batch did publish is broadcast a second time")
	}
	if r.ClaimRef == nil || *r.ClaimRef != priorRef {
		t.Errorf("reaped claim ref = %v, want the preserved %v", r.ClaimRef, priorRef)
	}
	if r.Attempts != 0 {
		t.Errorf("reaped attempts = %d, want 0: only a permanent error spends the budget", r.Attempts)
	}
	if r.ClaimedAt == nil {
		t.Error("claimed_at must be LEFT SET: it is the ops timeline evidence, and " +
			"adopt_required is the distinguishing signal, not claimed_at")
	}

	// The in-flight and never-claimed rows are untouched.
	var inflightStatus, freshStatus string
	var freshAdopt bool
	if err := pool.QueryRow(ctx,
		"SELECT status FROM weather_records WHERE id = 'inflight'").Scan(&inflightStatus); err != nil {
		t.Fatalf("reading inflight: %v", err)
	}
	if inflightStatus != string(store.StatusProcessing) {
		t.Errorf("inflight status = %q, want processing", inflightStatus)
	}
	if err := pool.QueryRow(ctx,
		"SELECT status, adopt_required FROM weather_records WHERE id = 'fresh'").
		Scan(&freshStatus, &freshAdopt); err != nil {
		t.Fatalf("reading fresh: %v", err)
	}
	if freshStatus != string(store.StatusPending) || freshAdopt {
		t.Errorf("fresh row = (%q, adopt %v), want (pending, false)", freshStatus, freshAdopt)
	}
}

// TestReapExpiredHonorsItsLimit uses the US spelling of "Honors" because
// misspell runs with locale US over test files and this repository's
// ignore-rules do not cover the -s inflection.
//
// The fixture seeds all four stale rows in ONE transaction, which is the
// PRODUCTION shape: claimSQL stamps `claimed_at = now()` on a whole batch inside
// one statement, so every row of a real stranded batch shares one claimed_at to
// the microsecond. Five separate Execs — which an earlier draft used — would give
// five distinct values and quietly test a situation that cannot occur. The
// count(DISTINCT claimed_at) assertion is what keeps the fixture honest.
//
// The assertion is "two successive limited reaps return DISJOINT sets that
// together cover the batch" rather than "len == 2", because that is the property
// a caller depends on. Note honestly what it is NOT: like the claim and list
// ordering tests, it was MEASURED to pass with `, c.id ASC` deleted at this row
// count. The tiebreaker is still required — `LIMIT` over a non-total order is
// unspecified, and the failure it prevents (overlapping reaps leaving part of a
// batch stranded for another whole lease) is silent when it happens.
func TestReapExpiredHonorsItsLimit(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i := range 4 {
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO weather_records
			  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
			VALUES ($1, $2, now(), $3, $4, 'processing', now() - interval '9 minutes', $5)`,
			"s"+string(rune('a'+i)), int64(i), time.Now().Add(time.Duration(i)*time.Minute),
			fullWeatherData(), uuid.Must(uuid.NewV7())); execErr != nil {
			t.Fatalf("seeding %d: %v", i, execErr)
		}
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	var distinct int
	if countErr := pool.QueryRow(ctx,
		"SELECT count(DISTINCT claimed_at) FROM weather_records").Scan(&distinct); countErr != nil {
		t.Fatalf("counting distinct claimed_at: %v", countErr)
	}
	if distinct != 1 {
		t.Fatalf("fixture is not the production shape: count(DISTINCT claimed_at) = %d, want 1", distinct)
	}

	firstReap, err := rs.ReapExpired(ctx, 5*time.Minute, 2)
	if err != nil {
		t.Fatalf("first ReapExpired: %v", err)
	}
	if len(firstReap) != 2 {
		t.Fatalf("first reap returned %d rows with limit 2", len(firstReap))
	}

	var stillProcessing int
	if countErr := pool.QueryRow(ctx,
		"SELECT count(*) FROM weather_records WHERE status = 'processing'").Scan(&stillProcessing); countErr != nil {
		t.Fatalf("counting: %v", countErr)
	}
	if stillProcessing != 2 {
		t.Fatalf("processing rows = %d after a limited reap, want 2", stillProcessing)
	}

	// Return the first two to processing is NOT what happens next: they are
	// pending now, so a second reap must pick up the OTHER two and nothing else.
	secondReap, err := rs.ReapExpired(ctx, 5*time.Minute, 2)
	if err != nil {
		t.Fatalf("second ReapExpired: %v", err)
	}
	seen := make(map[string]int, 4)
	for _, r := range firstReap {
		seen[r.ID]++
	}
	for _, r := range secondReap {
		seen[r.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s was reaped %d times across two limited reaps, want 1", id, n)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("two limited reaps covered %d of 4 rows: %v", len(seen), seen)
	}
}

func TestReapExpiredOnAnIdleQueueReturnsNothing(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	seedPending(t, pool, 3, 1000)

	reaped, err := rs.ReapExpired(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("ReapExpired: %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped %d rows from a queue with nothing in processing", len(reaped))
	}
}

func TestRequeueDryRunCountsWithoutWriting(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	for i := range 4 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO weather_records
			  (id, station_id, timestamp, observation_time, data, status, attempts,
			   processed_at, created_at)
			VALUES ($1, $2, now(), $3, $4, 'failed', 5, now() - interval '2 hours',
			        now() - interval '2 hours')`,
			"f"+string(rune('a'+i)), int64(1000+i%2),
			time.Now().Add(time.Duration(i)*time.Minute), fullWeatherData()); err != nil {
			t.Fatalf("seeding %d: %v", i, err)
		}
	}

	n, err := rs.Requeue(ctx, store.RequeueFilter{
		Status: store.StatusFailed, Since: time.Hour, Limit: 100, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Requeue dry run: %v", err)
	}
	if n != 4 {
		t.Fatalf("dry run counted %d, want 4", n)
	}

	var stillFailed int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM weather_records WHERE status = 'failed'").Scan(&stillFailed); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if stillFailed != 4 {
		t.Fatalf("a dry run wrote to %d rows", 4-stillFailed)
	}
}

func TestRequeueWritesAndFiltersByStation(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	for i := range 4 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO weather_records
			  (id, station_id, timestamp, observation_time, data, status, attempts,
			   processed_at, created_at)
			VALUES ($1, $2, now(), $3, $4, 'failed', 5, now() - interval '2 hours',
			        now() - interval '2 hours')`,
			"f"+string(rune('a'+i)), int64(1000+i%2),
			time.Now().Add(time.Duration(i)*time.Minute), fullWeatherData()); err != nil {
			t.Fatalf("seeding %d: %v", i, err)
		}
	}

	station := int64(1000)
	n, err := rs.Requeue(ctx, store.RequeueFilter{
		Status: store.StatusFailed, Since: time.Hour, StationID: &station, Limit: 100,
	})
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if n != 2 {
		t.Fatalf("requeued %d rows for station 1000, want 2", n)
	}

	rows, err := pool.Query(ctx,
		"SELECT station_id, status, adopt_required, attempts FROM weather_records ORDER BY id")
	if err != nil {
		t.Fatalf("reading rows: %v", err)
	}
	defer rows.Close()
	requeued, untouched := 0, 0
	for rows.Next() {
		var stationID int64
		var status string
		var adopt bool
		var attempts int32
		if err := rows.Scan(&stationID, &status, &adopt, &attempts); err != nil {
			t.Fatalf("scan: %v", err)
		}
		switch stationID {
		case 1000:
			if status != string(store.StatusPending) || !adopt {
				t.Errorf("station 1000 row = (%q, adopt %v), want (pending, true)", status, adopt)
			}
			if attempts != 5 {
				t.Errorf("requeue must not reset attempts, got %d, want 5", attempts)
			}
			requeued++
		default:
			if status != string(store.StatusFailed) {
				t.Errorf("station %d row = %q, want an untouched failed", stationID, status)
			}
			untouched++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if requeued != 2 || untouched != 2 {
		t.Fatalf("requeued %d untouched %d, want 2 and 2", requeued, untouched)
	}
}

func TestRequeueRespectsTheSinceWindow(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, processed_at, created_at)
		VALUES ('old', 1, now(), now(), $1, 'failed', now() - interval '2 hours',
		        now() - interval '2 hours')`, fullWeatherData()); err != nil {
		t.Fatalf("seeding old: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, processed_at)
		VALUES ('recent', 2, now(), now(), $1, 'failed', now())`, fullWeatherData()); err != nil {
		t.Fatalf("seeding recent: %v", err)
	}

	n, err := rs.Requeue(ctx, store.RequeueFilter{
		Status: store.StatusFailed, Since: time.Hour, Limit: 100,
	})
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if n != 1 {
		t.Fatalf("requeued %d rows, want 1 (only the row older than the window)", n)
	}
	var recentStatus string
	if err := pool.QueryRow(ctx,
		"SELECT status FROM weather_records WHERE id = 'recent'").Scan(&recentStatus); err != nil {
		t.Fatalf("reading recent: %v", err)
	}
	if recentStatus != string(store.StatusFailed) {
		t.Fatalf("recent row status = %q, want an untouched failed", recentStatus)
	}
}
