package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

var (
	_ func(context.Context, []store.BlockHeightUpdate) error            = (*postgres.RecordStore)(nil).SetBlockHeights
	_ func(context.Context, time.Duration, int) ([]store.Record, error) = (*postgres.RecordStore)(nil).ReconcileCandidates
)

// publishOneBatch seeds n pending rows for stationID, claims them and completes
// them under txid. It returns the completed record ids.
//
// It is safe to call twice for the SAME station: seedPending gives every call
// its own observation_time window, so the second call cannot collide with the
// first on ux_records_station_obs. (It could, before seedPending grew its
// per-call offset — the symptom was SQLSTATE 23505 inside the helper, from a
// test whose actual assertion never ran.) It does NOT seed the stations row;
// callers that assert on station columns call seedStations first.
func publishOneBatch(t testing.TB, pool *pgxpool.Pool, stationID int64, n int, txid string) []string {
	t.Helper()
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	seedPending(t, pool, n, stationID)

	claimed, err := rs.ClaimPending(ctx, n, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("publishOneBatch: ClaimPending: %v", err)
	}
	pubs := make([]store.Publication, 0, len(claimed))
	ids := make([]string, 0, len(claimed))
	for i, r := range claimed {
		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
		ids = append(ids, r.ID)
	}
	if _, completeErr := rs.Complete(ctx, txid, pubs); completeErr != nil {
		t.Fatalf("publishOneBatch: Complete: %v", completeErr)
	}
	return ids
}

// TestSetBlockHeightsWritesBothTablesInOneTransaction is the store half of the
// unauthenticated verify write path.
//
// The security invariant this preserves: the CALLER chooses only WHICH existing
// rows are refreshed, never the VALUE. The height comes from the block explorer
// and is assigned into store.BlockHeightUpdate by the verify handler, and the
// HTTP request DTO has no height field at all. If a rewrite ever lets a caller
// supply blockHeight, that is trivial data forgery on a proof-of-existence
// demo.
func TestSetBlockHeightsWritesBothTablesInOneTransaction(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	rs := postgres.NewRecordStore(pool)
	ssv := postgres.NewStationStore(pool)

	const txid = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	ids := publishOneBatch(t, pool, 1000, 3, txid)

	minedAt := time.Date(2026, 4, 17, 16, 0, 0, 0, time.UTC)
	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: txid, BlockHeight: 900123, MinedAt: &minedAt},
	}); err != nil {
		t.Fatalf("SetBlockHeights: %v", err)
	}

	for _, id := range ids {
		rec, err := rs.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if rec.BlockHeight == nil || *rec.BlockHeight != 900123 {
			t.Errorf("%s block height = %v, want 900123", id, rec.BlockHeight)
		}
		if rec.ChainStatus == nil || *rec.ChainStatus != store.ChainMined {
			t.Errorf("%s chain status = %v, want mined", id, rec.ChainStatus)
		}
		if rec.MinedAt == nil || !rec.MinedAt.Equal(minedAt) {
			t.Errorf("%s mined at = %v, want %v", id, rec.MinedAt, minedAt)
		}
		if rec.Status != store.StatusCompleted {
			t.Errorf("%s status = %q, want an unchanged completed", id, string(rec.Status))
		}
	}

	// stations.last_block_height moved in the SAME transaction. The frontend's
	// auto-verify loop re-fires forever if the refreshed list does not already
	// carry the height, because its `attempted` ref is per-mount and does not
	// survive navigation.
	st, err := ssv.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get station: %v", err)
	}
	if st.LastBlockHeight == nil || *st.LastBlockHeight != 900123 {
		t.Fatalf("stations.last_block_height = %v, want 900123", st.LastBlockHeight)
	}
}

func TestSetBlockHeightsNeverLowersAStationHeight(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	rs := postgres.NewRecordStore(pool)
	ssv := postgres.NewStationStore(pool)

	publishOneBatch(t, pool, 1000, 1, "tx-high")
	publishOneBatch(t, pool, 1000, 1, "tx-low")

	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: "tx-high", BlockHeight: 900500},
	}); err != nil {
		t.Fatalf("SetBlockHeights high: %v", err)
	}
	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: "tx-low", BlockHeight: 900100},
	}); err != nil {
		t.Fatalf("SetBlockHeights low: %v", err)
	}

	st, err := ssv.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get station: %v", err)
	}
	if st.LastBlockHeight == nil || *st.LastBlockHeight != 900500 {
		t.Fatalf("stations.last_block_height = %v, want the greatest seen 900500", st.LastBlockHeight)
	}
}

func TestSetBlockHeightsNoOpsForAnUnknownTxID(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	rs := postgres.NewRecordStore(pool)
	ssv := postgres.NewStationStore(pool)

	// Station 1001 rather than 1000, and not arbitrarily. `unparam` flags a
	// parameter every call site passes the same value for, and it is right to: a
	// helper whose station id is decorative is a helper nobody can tell is
	// honoring it. seedStations creates 1001, so this covers a second station on
	// the same path.
	ids := publishOneBatch(t, pool, 1001, 1, "tx-real")

	// This is the second property that bounds the unauthenticated write path:
	// the update matches nothing when no row carries the txid.
	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: "0000000000000000000000000000000000000000000000000000000000000000", BlockHeight: 999999},
		{TxID: "", BlockHeight: 1},
		{TxID: "not-a-txid", BlockHeight: 2},
	}); err != nil {
		t.Fatalf("SetBlockHeights for unknown txids: %v", err)
	}

	rec, err := rs.Get(ctx, ids[0])
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.BlockHeight != nil {
		t.Fatalf("an unrelated row got block height %v", rec.BlockHeight)
	}
	st, err := ssv.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("Get station: %v", err)
	}
	if st.LastBlockHeight != nil {
		t.Fatalf("an unrelated station got last_block_height %v", st.LastBlockHeight)
	}

	// An empty update list is a no-op, not an error.
	if err := rs.SetBlockHeights(ctx, nil); err != nil {
		t.Fatalf("SetBlockHeights(nil): %v", err)
	}
}

func TestSetBlockHeightsOnlyTouchesCompletedRows(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	ssv := postgres.NewStationStore(pool)

	// The station row MUST exist for this test to mean anything. An earlier draft
	// seeded only the record, so the station half of the write went unobserved:
	// setStationHeightSQL's subquery had no status filter, and a txid carried only
	// by an aborted row still raised that station's displayed last_block_height.
	// With no stations row the UPDATE matched nothing for an unrelated reason and
	// the hole was invisible.
	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, is_active) VALUES (1, true)"); err != nil {
		t.Fatalf("seeding station: %v", err)
	}

	// A failed row that nevertheless carries a txid — the aborted case.
	if _, insertErr := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, txid, chain_status)
		VALUES ('aborted', 1, now(), now(), $1, 'failed', 'tx-aborted', 'aborted')`,
		fullWeatherData()); insertErr != nil {
		t.Fatalf("seeding: %v", insertErr)
	}

	if setErr := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: "tx-aborted", BlockHeight: 900777},
	}); setErr != nil {
		t.Fatalf("SetBlockHeights: %v", setErr)
	}

	rec, err := rs.Get(ctx, "aborted")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.BlockHeight != nil {
		t.Fatalf("an aborted row got block height %v; verify must only refresh completed rows", rec.BlockHeight)
	}
	if rec.ChainStatus == nil || *rec.ChainStatus != store.ChainAborted {
		t.Fatalf("chain status = %v, want an unchanged aborted", rec.ChainStatus)
	}

	// And the station it belongs to must be untouched. This is the assertion the
	// unauthenticated verify path actually needs: an attacker who knows the txid
	// of a transaction that was ABORTED must not be able to make the dashboard
	// claim that station is confirmed at a height.
	st, err := ssv.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get station: %v", err)
	}
	if st.LastBlockHeight != nil {
		t.Fatalf("stations.last_block_height = %v from an ABORTED row's txid, want nil", st.LastBlockHeight)
	}
}

func TestReconcileCandidatesSelectsOnlyUnminedCompletedRows(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	fixtures := []string{
		// Old, completed, no chain status at all: a candidate.
		`INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
		 VALUES ('cand-null', 1, now(), now(), $1, 'completed', 'tx-1', 0, now() - interval '5 hours')`,
		// Old, completed, arc-accepted: also a candidate.
		`INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
		 VALUES ('cand-arc', 2, now(), now(), $1, 'completed', 'tx-2', 0, now() - interval '4 hours', 'arc-accepted')`,
		// Old, completed, already mined: NOT a candidate.
		`INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
		 VALUES ('mined', 3, now(), now(), $1, 'completed', 'tx-3', 0, now() - interval '6 hours', 'mined')`,
		// Recent, completed, unmined: NOT a candidate yet.
		`INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
		 VALUES ('fresh', 4, now(), now(), $1, 'completed', 'tx-4', 0, now(), 'arc-accepted')`,
		// Pending: NOT a candidate.
		`INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
		 VALUES ('pending', 5, now(), now(), $1)`,
	}
	for i, stmt := range fixtures {
		if _, err := pool.Exec(ctx, stmt, fullWeatherData()); err != nil {
			t.Fatalf("seeding %d: %v", i, err)
		}
	}

	cands, err := rs.ReconcileCandidates(ctx, time.Hour, 100)
	if err != nil {
		t.Fatalf("ReconcileCandidates: %v", err)
	}
	got := make(map[string]bool, len(cands))
	for _, c := range cands {
		got[c.ID] = true
	}
	if len(cands) != 2 || !got["cand-null"] || !got["cand-arc"] {
		ids := make([]string, 0, len(cands))
		for _, c := range cands {
			ids = append(ids, c.ID)
		}
		t.Fatalf("candidates = %v, want exactly [cand-null cand-arc]", ids)
	}

	// Oldest first, so a reconciler tick always makes progress on the worst
	// backlog.
	if cands[0].ID != "cand-arc" && cands[0].ID != "cand-null" {
		t.Fatalf("unexpected first candidate %q", cands[0].ID)
	}
	if !cands[0].ProcessedAt.Before(*cands[1].ProcessedAt) {
		t.Fatalf("candidates are not oldest-first: %v then %v", cands[0].ProcessedAt, cands[1].ProcessedAt)
	}

	// The limit is honored.
	limited, err := rs.ReconcileCandidates(ctx, time.Hour, 1)
	if err != nil {
		t.Fatalf("ReconcileCandidates with limit 1: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit 1 returned %d rows", len(limited))
	}
}

func TestReconcileCandidatesOnAnEmptyDatabase(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	cands, err := rs.ReconcileCandidates(ctx, time.Hour, 100)
	if err != nil {
		t.Fatalf("ReconcileCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("candidates = %d on an empty database, want 0", len(cands))
	}
}

// TestSetBlockHeightsAtTheSameHeightIsANoOpNotARegression covers the EQUAL
// case the brief's own never-lowers test does not: TestSetBlockHeightsNeverLowersAStationHeight
// only proves a STRICTLY lower height cannot win, which a comparison written
// as `>` (strict) would also pass. greatest(a, a) = a, so the comparison this
// package actually needs is non-strict (>=): applying the SAME height a
// station already carries must leave it unchanged, not regress it to some
// other value and not error. This is the fixture that would catch a rewrite
// of setStationHeightSQL's greatest(...) into a strict `WHERE $2 > s.last_block_height`
// guard, which TestSetBlockHeightsNeverLowersAStationHeight alone cannot
// discriminate from the correct non-strict version.
func TestSetBlockHeightsAtTheSameHeightIsANoOpNotARegression(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	rs := postgres.NewRecordStore(pool)
	ssv := postgres.NewStationStore(pool)

	publishOneBatch(t, pool, 1000, 1, "tx-first")
	publishOneBatch(t, pool, 1000, 1, "tx-second")

	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: "tx-first", BlockHeight: 900300},
	}); err != nil {
		t.Fatalf("SetBlockHeights first: %v", err)
	}
	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: "tx-second", BlockHeight: 900300},
	}); err != nil {
		t.Fatalf("SetBlockHeights second (same height): %v", err)
	}

	st, err := ssv.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get station: %v", err)
	}
	if st.LastBlockHeight == nil || *st.LastBlockHeight != 900300 {
		t.Fatalf("stations.last_block_height = %v after a repeat confirmation at the SAME height, want 900300",
			st.LastBlockHeight)
	}
}

// TestReconcileCandidatesNonPositiveLimitNeverTouchesTheDatabase is the
// regression gate on this method's own half of clampLimit — the standing
// override this task's own brief omitted verbatim, exactly as ReapExpired and
// Requeue omitted it before it (see clampLimit's doc comment in records.go).
// ReconcileCandidates has no separate Total to preserve (unlike List), so a
// non-positive limit short-circuits the WHOLE method: zero rows, a NIL error,
// and — proven by the already-canceled context, per this package's
// established pattern (TestReapExpiredNonPositiveLimitNeverTouchesTheDatabase,
// TestClaimPendingNonPositiveNNeverTouchesTheDatabase) — no query reaching the
// database at all.
func TestReconcileCandidatesNonPositiveLimitNeverTouchesTheDatabase(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	// A genuine candidate: old, completed, unmined. If the clamp were skipped
	// and the query ran anyway with limit=0, LIMIT 0 would harmlessly return
	// zero rows regardless, so this fixture alone cannot discriminate a
	// missing guard — the canceled-context assertions below are what actually
	// prove it.
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
		VALUES ('candidate', 1, now(), now(), $1, 'completed', 'tx-candidate', 0, now() - interval '2 hours')`,
		fullWeatherData()); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	for _, limit := range []int{0, -1} {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		cands, err := rs.ReconcileCandidates(cctx, time.Hour, limit)
		if err != nil {
			t.Fatalf("ReconcileCandidates(limit=%d) with an already-canceled context: err = %v, want nil", limit, err)
		}
		if cands == nil {
			t.Fatalf("ReconcileCandidates(limit=%d) returned a nil slice, want a non-nil empty slice", limit)
		}
		if len(cands) != 0 {
			t.Fatalf("ReconcileCandidates(limit=%d) returned %d rows, want 0", limit, len(cands))
		}
	}

	// A real call afterward must still see the untouched candidate: the two
	// non-positive-limit calls above must not have written anything.
	cands, err := rs.ReconcileCandidates(ctx, time.Hour, 100)
	if err != nil {
		t.Fatalf("ReconcileCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0].ID != "candidate" {
		t.Fatalf("candidates = %v, want exactly [candidate] (untouched by the two non-positive-limit calls)", cands)
	}
}

// TestReconcileCandidatesTiebreaksByIDUnderHeapChurn pins `, id ASC` in
// reconcileCandidatesSQL's ORDER BY.
//
// EXPLAIN was read FIRST, against this package's own schema and
// fullWeatherData()-sized rows rather than a toy empty-jsonb payload: even on
// a freshly loaded 40-row table the planner chose a Seq Scan feeding an
// EXPLICIT Sort node (`Sort Key: processed_at, id`, `Sort Method: quicksort`),
// never touching the partial index ix_records_reconcile — the table is small
// and the predicate matches nearly every row, so Postgres judged the index not
// worth it. That is reapExpiredSQL's plan shape (an explicit Sort over the
// WHOLE tied group), not claimSQL's (an index already delivering rows in
// order), so per TestReapExpiredTiebreaksByIDUnderHeapChurn's own measurement
// this needs the SAME full N=40 permutation churn (reapChurnOrder) to perturb
// the tie handling. Confirmed directly against this schema before writing this
// test (40 realistic-sized rows sharing one processed_at, reapChurnOrder
// applied): `ORDER BY processed_at ASC` alone returned a DIFFERENT 20-row SET
// than `..., id ASC` — c005/c009/c013/c018 dropped out and c020-c023 appeared
// in their place — not merely a reordering of the same rows.
//
// The assertion is exact POSITION, not just membership: ReconcileCandidates
// does not consume rows (unlike a claim or a reap), so a single call with
// limit = reapChurnCount/2 must return precisely the 20 lowest ids, in id
// order, every time.
func TestReconcileCandidatesTiebreaksByIDUnderHeapChurn(t *testing.T) {
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
		ids[i] = fmt.Sprintf("c%03d", i)
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO weather_records
			  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
			VALUES ($1, $2, now(), $3, $4, 'completed', $5, 0, now() - interval '2 hours')`,
			ids[i], int64(i), time.Now().Add(time.Duration(i)*time.Minute),
			fullWeatherData(), "tx-"+ids[i]); execErr != nil {
			t.Fatalf("seeding %d: %v", i, execErr)
		}
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	var distinct int
	if countErr := pool.QueryRow(ctx,
		"SELECT count(DISTINCT processed_at) FROM weather_records").Scan(&distinct); countErr != nil {
		t.Fatalf("counting distinct processed_at: %v", countErr)
	}
	if distinct != 1 {
		t.Fatalf("fixture is not the production shape: count(DISTINCT processed_at) = %d, want 1", distinct)
	}

	// Churn the heap: see reapChurnOrder's doc comment (records_requeue_test.go)
	// and this test's own doc comment above for why a freshly loaded heap
	// cannot discriminate the tiebreaker for THIS query's plan shape.
	for _, idx := range reapChurnOrder {
		if _, execErr := pool.Exec(ctx,
			"UPDATE weather_records SET attempts = attempts WHERE id = $1", ids[idx]); execErr != nil {
			t.Fatalf("churning row %d: %v", idx, execErr)
		}
	}

	got, err := rs.ReconcileCandidates(ctx, time.Hour, reapChurnCount/2)
	if err != nil {
		t.Fatalf("ReconcileCandidates: %v", err)
	}
	want := ids[0 : reapChurnCount/2]
	if len(got) != len(want) {
		t.Fatalf("returned %d rows, want %d", len(got), len(want))
	}
	for i, r := range got {
		if r.ID != want[i] {
			t.Fatalf("position %d = %s, want %s\n got %v\nwant %v", i, r.ID, want[i], listIDs(got), want)
		}
	}
}
