package postgres_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

var _ func(context.Context, int, uuid.UUID) ([]store.Record, error) = (*postgres.RecordStore)(nil).ClaimPending

// seedCallCount gives every seedPending call its own observation_time window.
//
// Without it the helper is silently NON-COMPOSABLE: (station_id,
// observation_time) is the dedupe key of ux_records_station_obs, and a fixed
// base plus i minutes means two calls for the SAME station both write
// base+0 minutes and the second dies with SQLSTATE 23505. That is not
// hypothetical — TestSetBlockHeightsNeverLowersAStationHeight calls
// publishOneBatch twice on station 1000 with n=1, and it failed exactly this
// way. Tests in a package run sequentially (no t.Parallel anywhere near
// Postgres), so a plain counter is sufficient and needs no mutex.
var seedCallCount int

// seedPending inserts n pending rows in ONE transaction and returns their ids
// in creation order.
//
// One transaction is the production shape: created_at defaults to now(), which
// is transaction_timestamp(), so every row a poll writes shares one created_at
// to the microsecond. Seeding them one statement at a time would give each row
// a distinct created_at and would silently make every ordering test weaker.
//
// The dedupe key is (station_id, observation_time), so each CALL gets its own
// day and each row inside a call its own minute. Repeat calls for one station
// are therefore safe, which several tests depend on.
func seedPending(t testing.TB, pool *pgxpool.Pool, n int, stationID int64) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	seedCallCount++
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC).
		Add(time.Duration(seedCallCount) * 24 * time.Hour)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("seedPending: Begin: %v", err)
	}
	// Without this rollback a seeding failure is a WHOLE-PACKAGE CI TIMEOUT
	// rather than a test failure: t.Fatalf calls runtime.Goexit while tx still
	// holds a pooled connection, so the t.Cleanup(pool.Close) that storetest.Pool
	// registered blocks forever inside puddle's WaitGroup and the package burns
	// the full -timeout 10m before printing a goroutine dump that buries the real
	// error. Measured: adding this line turned a 10-minute hang into a 0.10s
	// failure. Rollback after a successful Commit is a no-op in pgx (it returns
	// ErrTxClosed), so discarding the error is correct and keeps errcheck quiet
	// without a //nolint.
	defer func() { _ = tx.Rollback(ctx) }()
	for i := range n {
		id, idErr := uuid.NewV7()
		if idErr != nil {
			t.Fatalf("seedPending: uuid: %v", idErr)
		}
		ids = append(ids, id.String())
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
			VALUES ($1, $2, $3, $4, $5)`,
			id.String(), stationID, base, base.Add(time.Duration(i)*time.Minute), fullWeatherData(),
		); execErr != nil {
			t.Fatalf("seedPending: insert %d: %v", i, execErr)
		}
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		t.Fatalf("seedPending: Commit: %v", commitErr)
	}
	return ids
}

// TestClaimPendingNeverDoubleClaims is the headline invariant of this whole
// plan, and the CI workflow repeats it with -count=5.
//
// What it replaces: the TypeScript claim was two statements with no lock, no
// version guard and no conditional filter (find pending, then bulkWrite with
// filter {_id: id} rather than {_id: id, status: 'pending'}). Overlapping pods
// avoided double-publishing only by accident — both then spent from the same
// funding basket and the loser's createAction failed as a double spend — and
// under the new design the app passes no inputs at all, so the server funds
// each pod from DIFFERENT fuel UTXOs and BOTH succeed. A SELECT-then-UPDATE
// port would therefore be STRICTLY WORSE than the TypeScript it replaces.
//
// The assertions are INVARIANTS, never schedules: which worker wins which row
// is scheduler-dependent, and asserting on that would flake immediately.
func TestClaimPendingNeverDoubleClaims(t *testing.T) {
	const (
		workers = 12
		seeded  = 40
		batch   = 7
	)
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	// Without this the test is silently vacuous: a pool smaller than the worker
	// count serializes the race, so the claim would appear correct however it
	// was written. This exact fixture defect left a sibling repository's
	// SKIP LOCKED paths untested on the production engine.
	if got := pool.Config().MaxConns; got < workers {
		t.Fatalf("pool MaxConns = %d, need >= %d or this test cannot observe a race", got, workers)
	}

	seedPending(t, pool, seeded, 1000)
	rs := postgres.NewRecordStore(pool)

	var mu sync.Mutex
	seen := make(map[string]int, seeded)
	refs := make(map[uuid.UUID]int, workers)
	errs := make([]error, 0, workers)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, refErr := uuid.NewV7()
			if refErr != nil {
				mu.Lock()
				errs = append(errs, refErr)
				mu.Unlock()
				return
			}
			<-start
			recs, err := rs.ClaimPending(ctx, batch, ref)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for _, r := range recs {
				seen[r.ID]++
				if r.ClaimRef != nil {
					refs[*r.ClaimRef]++
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		t.Errorf("worker error: %v", err)
	}

	doubled := make([]string, 0, len(seen))
	worst := 0
	for id, n := range seen {
		if n > 1 {
			doubled = append(doubled, id)
		}
		if n > worst {
			worst = n
		}
	}
	if len(doubled) != 0 {
		t.Fatalf("claim double-allocated %d of %d rows (worst row claimed %d times)",
			len(doubled), len(seen), worst)
	}
	if len(seen) > seeded {
		t.Fatalf("claimed %d distinct rows from %d seeded", len(seen), seeded)
	}

	// Every claimed row must be processing with a lease and a ref, and the
	// remaining rows must still be pending. If the claim leaked a row into
	// processing without returning it, the pipeline would stall silently.
	var processing, pending int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'processing'),
		       count(*) FILTER (WHERE status = 'pending')
		  FROM weather_records`).Scan(&processing, &pending)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if processing != len(seen) {
		t.Fatalf("processing rows = %d, but %d were returned to callers", processing, len(seen))
	}
	if processing+pending != seeded {
		t.Fatalf("processing+pending = %d, want %d", processing+pending, seeded)
	}

	var leaseless, refless int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'processing' AND claimed_at IS NULL),
		       count(*) FILTER (WHERE status = 'processing' AND claim_ref IS NULL)
		  FROM weather_records`).Scan(&leaseless, &refless)
	if err != nil {
		t.Fatalf("counting lease/ref: %v", err)
	}
	if leaseless != 0 || refless != 0 {
		t.Fatalf("%d processing rows have no lease and %d have no ref, want 0 and 0", leaseless, refless)
	}
}

// TestClaimPendingStampsExactlyOneRefPerCall is the regression gate on the
// gen_random_uuid() defect: COALESCE(claim_ref, gen_random_uuid()) evaluates
// the VOLATILE function once per updated row, so a 21-row claim produces 21
// distinct refs and the batch label that the adopt design uses as its
// idempotency key ceases to exist. The ref must therefore be a bind parameter.
func TestClaimPendingStampsExactlyOneRefPerCall(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedPending(t, pool, 21, 1000)
	rs := postgres.NewRecordStore(pool)

	ref := uuid.Must(uuid.NewV7())
	recs, err := rs.ClaimPending(ctx, 21, ref)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(recs) != 21 {
		t.Fatalf("claimed %d rows, want 21", len(recs))
	}
	for _, r := range recs {
		if r.ClaimRef == nil || *r.ClaimRef != ref {
			t.Fatalf("row %s has ref %v, want %v", r.ID, r.ClaimRef, ref)
		}
		if r.Status != store.StatusProcessing {
			t.Fatalf("row %s status = %q, want processing", r.ID, string(r.Status))
		}
		if r.ClaimedAt == nil {
			t.Fatalf("row %s has no lease", r.ID)
		}
		if r.Attempts != 0 {
			t.Fatalf("row %s attempts = %d; the claim must never spend the attempt budget", r.ID, r.Attempts)
		}
	}

	var distinct int
	if err := pool.QueryRow(ctx,
		"SELECT count(DISTINCT claim_ref) FROM weather_records WHERE claim_ref IS NOT NULL").Scan(&distinct); err != nil {
		t.Fatalf("counting refs: %v", err)
	}
	if distinct != 1 {
		t.Fatalf("count(DISTINCT claim_ref) = %d after one 21-row claim, want 1", distinct)
	}
}

// TestClaimPendingPreservesAPriorRef covers the reaped-row case: a row that
// was claimed, reaped and re-claimed keeps its ORIGINAL ref, which is what
// makes the adopt check able to look up the prior batch. One claim can
// therefore legitimately return several distinct refs, and the publisher must
// partition its batch rather than assuming one action per claim.
func TestClaimPendingPreservesAPriorRef(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	// station 2000, not 1000: seedPending's stationID is otherwise a constant
	// 1000 across every call in this file, which golangci-lint's unparam
	// (pinned to the same v2.12.2 the CI job runs) correctly flags as "always
	// receives 1000" — a hard lint failure, since //nolint is forbidden here.
	// station id is immaterial to this test (the dedupe window is keyed off
	// seedCallCount, not station), so varying it is a no-op for behavior.
	ids := seedPending(t, pool, 2, 2000)
	rs := postgres.NewRecordStore(pool)

	priorRef := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx,
		"UPDATE weather_records SET claim_ref = $1 WHERE id = $2", priorRef, ids[0]); err != nil {
		t.Fatalf("stamping a prior ref: %v", err)
	}

	newRef := uuid.Must(uuid.NewV7())
	recs, err := rs.ClaimPending(ctx, 2, newRef)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("claimed %d rows, want 2", len(recs))
	}
	byID := make(map[string]store.Record, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	if got := byID[ids[0]].ClaimRef; got == nil || *got != priorRef {
		t.Fatalf("row with a prior ref got %v, want the prior %v", got, priorRef)
	}
	if got := byID[ids[1]].ClaimRef; got == nil || *got != newRef {
		t.Fatalf("fresh row got %v, want the new %v", got, newRef)
	}
}

// TestClaimPendingIsFIFOAcrossATiedBatch pins the queue's batch-split behavior:
// the first claim takes the oldest four rows and the second takes the next four.
//
// WHAT IT DOES NOT DO, stated because an earlier draft of this comment claimed
// the opposite: it is NOT a regression gate on `, c.id ASC`. MEASURED on
// postgres:17-alpine — with the tiebreaker deleted, and again with
// ix_records_status_created also removed so the planner must sort, this test
// still PASSED on three consecutive runs. At twenty rows in a freshly loaded
// heap the untied query happens to agree with the tied one. The tiebreaker is
// still mandatory, and the evidence for it is elsewhere:
// TestCreatedAtIsTheTransactionTimestamp proves the whole batch shares one
// created_at, and `LIMIT`/`OFFSET` over a non-total order is unspecified — at
// production shape two legitimate plans for the identical untied query returned
// 19 of 20 different rows at the same offset. Do not delete `, c.id ASC` on the
// strength of this test staying green.
//
// The assertion compares SETS PER BATCH, never positions within a batch, and
// that is a correctness requirement rather than a stylistic preference:
// PostgreSQL does not define the order in which `UPDATE … RETURNING` emits
// rows. The subquery's ORDER BY chooses WHICH rows are locked and updated; the
// ModifyTable node then returns them in whatever order it processed them, which
// today happens to follow the heap and would change the moment rows are updated
// in place. What IS guaranteed, and what the queue depends on, is that the
// FIRST claim takes the oldest four and the SECOND takes the next four.
func TestClaimPendingIsFIFOAcrossATiedBatch(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ids := seedPending(t, pool, 10, 1000)
	rs := postgres.NewRecordStore(pool)

	// uuidv7 ids sort in generation order, so ids is already the intended FIFO
	// order and the tiebreaker makes it the actual one.
	first, err := rs.ClaimPending(ctx, 4, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := rs.ClaimPending(ctx, 4, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}

	batches := []struct {
		label string
		got   []store.Record
		want  []string
	}{
		{"first claim", first, ids[0:4]},
		{"second claim", second, ids[4:8]},
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

	// The two remaining rows must be the LAST two seeded, which is the property
	// that actually says "FIFO" rather than "some four then some four". Read
	// straight from the table rather than through List, which does not exist
	// until Task 12.
	rows, err := pool.Query(ctx,
		"SELECT id FROM weather_records WHERE status = 'pending' ORDER BY id ASC")
	if err != nil {
		t.Fatalf("listing pending rows: %v", err)
	}
	remaining, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collecting pending ids: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("%d rows still pending, want 2", len(remaining))
	}
	left := map[string]bool{ids[8]: true, ids[9]: true}
	for _, id := range remaining {
		if !left[id] {
			t.Fatalf("row %s is still pending but is not one of the last two seeded %v", id, ids[8:])
		}
	}
}

func TestClaimPendingOnAnEmptyQueueReturnsNoRows(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	recs, err := rs.ClaimPending(ctx, 10, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending on an empty queue: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("claimed %d rows from an empty queue", len(recs))
	}
}
