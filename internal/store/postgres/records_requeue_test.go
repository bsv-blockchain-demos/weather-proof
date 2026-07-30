package postgres_test

import (
	"context"
	"fmt"
	"sort"
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

// TestReapExpiredNonPositiveLimitNeverTouchesTheDatabase is the regression
// gate on interfaces.go's clamp rule, routed through the package's single
// clampLimit enforcement point (see its doc comment in records.go): a
// non-positive limit returns zero rows and a NIL error, with no exception for
// a negative limit, and it does so WITHOUT ever building or sending a query.
//
// This is the second time this exact class of bug has shipped in this
// package — first ClaimPending, now ReapExpired and Requeue, both measured
// to leak SQLSTATE 2201W through classify for a negative limit when this
// task's own prescribed six tests never exercised the case at all. The
// already-canceled context is what proves the guard fires BEFORE any query
// reaches the database, not merely that it produces the right answer by
// coincidence: if ReapExpired instead reached the database for a
// non-positive limit, a canceled context would surface here as a non-nil
// error (context.Canceled for a query that never got past connection
// acquisition, or the SQLSTATE 2201W this package classifies to
// ErrOperation if the raw LIMIT still executed for the negative case). A nil
// error is only possible if clampLimit returned before ctx was ever
// consulted.
func TestReapExpiredNonPositiveLimitNeverTouchesTheDatabase(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	// A genuinely stranded row: if the clamp were skipped and the query ran
	// anyway with limit=0, LIMIT 0 would harmlessly return zero rows anyway
	// and this test would not discriminate a missing guard from a present
	// one. The canceled-context assertions below are what actually prove the
	// guard, independent of what a real query would have returned.
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
		VALUES ('stranded', 1, now(), now(), $1, 'processing', now() - interval '9 minutes', $2)`,
		fullWeatherData(), uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("seeding the stranded row: %v", err)
	}

	for _, limit := range []int{0, -1} {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		reaped, err := rs.ReapExpired(cctx, 5*time.Minute, limit)
		if err != nil {
			t.Fatalf("ReapExpired(limit=%d) with an already-canceled context: err = %v, want nil", limit, err)
		}
		if reaped == nil {
			t.Fatalf("ReapExpired(limit=%d) returned a nil slice, want a non-nil empty slice", limit)
		}
		if len(reaped) != 0 {
			t.Fatalf("ReapExpired(limit=%d) reaped %d rows, want 0", limit, len(reaped))
		}
	}

	// The stranded row must be entirely untouched by either call.
	var stillProcessing int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM weather_records WHERE status = 'processing'").Scan(&stillProcessing); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if stillProcessing != 1 {
		t.Fatalf("processing rows = %d after two non-positive-limit reaps, want 1 (untouched)", stillProcessing)
	}
}

// TestRequeueNonPositiveLimitNeverTouchesTheDatabase is Requeue's half of the
// same regression gate, covering all four combinations of {0, -1} x
// {write, DryRun}: the clamp must behave identically whether or not the call
// would have written anything, since both paths route through the same
// clampLimit call in Requeue. See
// TestReapExpiredNonPositiveLimitNeverTouchesTheDatabase for why an
// already-canceled context is what proves the guard fires before any query is
// built or sent, not merely that it produces the right answer by coincidence.
func TestRequeueNonPositiveLimitNeverTouchesTheDatabase(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, attempts,
		   processed_at, created_at)
		VALUES ('f1', 1000, now(), now(), $1, 'failed', 5, now() - interval '2 hours',
		        now() - interval '2 hours')`, fullWeatherData()); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	for _, limit := range []int{0, -1} {
		for _, dryRun := range []bool{false, true} {
			cctx, cancel := context.WithCancel(context.Background())
			cancel()
			n, err := rs.Requeue(cctx, store.RequeueFilter{
				Status: store.StatusFailed, Since: time.Hour, Limit: limit, DryRun: dryRun,
			})
			if err != nil {
				t.Fatalf("Requeue(limit=%d, dryRun=%v) with an already-canceled context: err = %v, want nil",
					limit, dryRun, err)
			}
			if n != 0 {
				t.Fatalf("Requeue(limit=%d, dryRun=%v) = %d, want 0", limit, dryRun, n)
			}
		}
	}

	var stillFailed int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM weather_records WHERE status = 'failed'").Scan(&stillFailed); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if stillFailed != 1 {
		t.Fatalf("failed rows = %d after four non-positive-limit requeues, want 1 (untouched)", stillFailed)
	}
}

// excludedRowSnapshot is a plain-value capture of everything Requeue could
// plausibly touch on a row it must NOT touch: status, attempts, and whether
// claimed_at/claim_ref are set. No pointer field appears anywhere in it —
// deliberately, so that comparing a "before" and "after" snapshot can never
// fall into the trap of comparing two pointers that happen to alias the same
// backing value (or a *store.Record some other code path could still mutate
// after the fact), which is the exact shape of bug that occurred in Task 2.
// Each snapshot below is an INDEPENDENT round trip into an independent value.
type excludedRowSnapshot struct {
	status   string
	attempts int32
	hasClaim bool
	hasRef   bool
}

// TestRequeueLeavesADifferentStatusRowUntouched is the Task-10-shaped gap
// closed here: every fixture in this file's OTHER Requeue tests seeds rows
// that are already 'failed', so none of them can tell a correct
// `status = $1` predicate apart from one that is missing entirely — a
// mutation dropping it was measured (in a scratch copy, full suite) to leave
// every other test in this package green.
//
// This fixture seeds a 'processing' row that is otherwise identical in every
// way a dropped status filter could let through: same age (old enough to
// clear the Since window) and no station filter to hide behind, so the
// status predicate is the ONLY thing standing between it and a wrongful
// requeue. The "before" and "after" values are two independently captured
// excludedRowSnapshot values (plain scalars, no pointers) rather than one
// struct held across the call and compared to itself.
func TestRequeueLeavesADifferentStatusRowUntouched(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	for i := range 2 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO weather_records
			  (id, station_id, timestamp, observation_time, data, status, attempts,
			   processed_at, created_at)
			VALUES ($1, 1000, now(), $2, $3, 'failed', 5, now() - interval '2 hours',
			        now() - interval '2 hours')`,
			"g"+string(rune('a'+i)), time.Now().Add(time.Duration(i)*time.Minute),
			fullWeatherData()); err != nil {
			t.Fatalf("seeding failed row %d: %v", i, err)
		}
	}

	// A processing row, just as old as the failed ones (so the Since window
	// alone would let it through) and on the same station (so a station
	// filter isn't what's excluding it either).
	priorRef := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, attempts,
		   claimed_at, claim_ref, created_at)
		VALUES ('excluded', 1000, now(), now(), $1, 'processing', 2,
		        now() - interval '1 minute', $2, now() - interval '2 hours')`,
		fullWeatherData(), priorRef); err != nil {
		t.Fatalf("seeding the excluded processing row: %v", err)
	}

	readExcluded := func() excludedRowSnapshot {
		var s excludedRowSnapshot
		var claimedAt *time.Time
		var claimRef *uuid.UUID
		if err := pool.QueryRow(ctx,
			"SELECT status, attempts, claimed_at, claim_ref FROM weather_records WHERE id = 'excluded'").
			Scan(&s.status, &s.attempts, &claimedAt, &claimRef); err != nil {
			t.Fatalf("reading excluded row: %v", err)
		}
		s.hasClaim = claimedAt != nil
		s.hasRef = claimRef != nil
		return s
	}
	before := readExcluded()

	n, err := rs.Requeue(ctx, store.RequeueFilter{
		Status: store.StatusFailed, Since: time.Hour, Limit: 100,
	})
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if n != 2 {
		t.Fatalf("requeued %d rows, want 2 (the two failed rows only)", n)
	}

	after := readExcluded()
	if after != before {
		t.Fatalf("excluded processing row changed: before %+v, after %+v", before, after)
	}
}

// reapChurnCount is the row count TestReapExpiredTiebreaksByIDUnderHeapChurn
// seeds. It is 40, not 8, for a measured reason documented on the test.
const reapChurnCount = 40

// reapChurnOrder is a fixed, non-identity permutation of index positions
// 0..39, used below to shuffle the physical heap order of
// TestReapExpiredTiebreaksByIDUnderHeapChurn's forty equal-claimed_at fixture
// rows before reaping. It is a literal permutation rather than a call into
// math/rand (forbidden here by gosec G404) or crypto/rand (which would make
// the test's discriminating behavior nondeterministic for no benefit): the
// property this needs is simply SOME order other than the rows' insertion
// order, not genuine randomness. (Sorting it recovers 0..39, confirming it is
// a full permutation — captured once from a seeded math/rand/v2 shuffle and
// pasted here as a constant, never generated at test time.) This mirrors
// records_claim_test.go's churnOrder, which pins the identical tiebreaker
// shape for claimSQL — a separate constant, at a different size, because the
// two queries were MEASURED to need different row counts to discriminate
// (see the test's own doc comment) and the two names must not collide within
// one package.
var reapChurnOrder = [reapChurnCount]int{
	9, 39, 26, 36, 6, 5, 38, 13, 18, 32, 34, 33, 12, 7, 11, 27, 19, 4, 21, 2,
	10, 17, 29, 25, 3, 31, 14, 0, 8, 24, 30, 16, 15, 1, 23, 37, 35, 22, 20, 28,
}

// TestReapExpiredTiebreaksByIDUnderHeapChurn pins `, c.id ASC` in
// reapExpiredSQL's ORDER BY.
//
// A freshly loaded heap cannot discriminate this at all, matching
// records_claim_test.go's identical finding for claimSQL — which is exactly
// why TestReapExpiredHonorsItsLimit's four-row, no-churn fixture (this file,
// above) was measured to pass unchanged with `, c.id ASC` deleted. But
// churn alone, at claimSQL's own N=8/10 scale, was ALSO measured to be
// insufficient here, and that is a genuine difference from claimSQL worth
// stating rather than assuming away: claimSQL's subquery sorts by
// created_at, which IS ix_records_status_created's own second column, so
// Postgres answers it with a single Index Scan and no separate Sort node —
// and for a duplicate-key group, THAT plan shape was measured to be
// churn-sensitive at N=8. reapExpiredSQL's subquery sorts by claimed_at,
// which is NOT in that index, so Postgres instead does an Index Scan on
// status alone feeding an explicit Sort on claimed_at — and at N=8, that
// extra Sort node was measured to emit a full tie group in stable insertion
// order regardless of churn (confirmed directly: EXPLAIN showed the Sort
// node, and the returned set was unchanged pre- and post-churn). Scaling to
// N=40 with the full permutation below WAS measured to perturb the Sort's
// tie handling — the returned set differs from strict id order — so this is
// the row count that actually exercises the branch the tiebreaker guards for
// THIS query's plan shape, not an arbitrarily large number for its own sake.
//
// The assertion compares SETS per reap, never positions: Postgres does not
// define the order `UPDATE ... RETURNING` emits rows in. What the tiebreaker
// guarantees, and what a caller depends on, is that the FIRST limited reap
// takes the twenty LOWEST ids and the SECOND takes the other twenty —
// covering the whole batch with no overlap, exactly like the id-ASC claim
// queue.
func TestReapExpiredTiebreaksByIDUnderHeapChurn(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	ids := make([]string, reapChurnCount)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i := range reapChurnCount {
		ids[i] = fmt.Sprintf("t%03d", i)
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO weather_records
			  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
			VALUES ($1, $2, now(), $3, $4, 'processing', now() - interval '9 minutes', $5)`,
			ids[i], int64(i), time.Now().Add(time.Duration(i)*time.Minute),
			fullWeatherData(), uuid.Must(uuid.NewV7())); execErr != nil {
			t.Fatalf("seeding %d: %v", i, execErr)
		}
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// Churn the heap before reaping: see reapChurnOrder's doc comment for why
	// a freshly loaded heap, and even a churned N=8 heap, cannot discriminate
	// the tiebreaker for THIS query's plan shape. Setting attempts to its own
	// current value is a no-op for every assertion below — it must change
	// nothing this test checks, only each row's physical heap position.
	for _, idx := range reapChurnOrder {
		if _, execErr := pool.Exec(ctx,
			"UPDATE weather_records SET attempts = attempts WHERE id = $1", ids[idx]); execErr != nil {
			t.Fatalf("churning row %d: %v", idx, execErr)
		}
	}

	first, err := rs.ReapExpired(ctx, 5*time.Minute, reapChurnCount/2)
	if err != nil {
		t.Fatalf("first ReapExpired: %v", err)
	}
	second, err := rs.ReapExpired(ctx, 5*time.Minute, reapChurnCount/2)
	if err != nil {
		t.Fatalf("second ReapExpired: %v", err)
	}

	batches := []struct {
		label string
		got   []store.Record
		want  []string
	}{
		{"first reap", first, ids[0 : reapChurnCount/2]},
		{"second reap", second, ids[reapChurnCount/2:]},
	}
	for _, b := range batches {
		got := make([]string, 0, len(b.got))
		for _, r := range b.got {
			got = append(got, r.ID)
		}
		sort.Strings(got)
		want := make([]string, 0, len(b.want))
		want = append(want, b.want...)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("%s returned %d rows, want %d", b.label, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s = %v, want the batch %v (seeded order %v)", b.label, got, want, ids)
			}
		}
	}
}
