package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

var (
	_ func(context.Context, []string, string) error = (*postgres.RecordStore)(nil).FailPermanent
	_ func(context.Context, []string, string) error = (*postgres.RecordStore)(nil).RequeueInfra
	_ func(context.Context, []string, string) error = (*postgres.RecordStore)(nil).MarkUnknown
)

// rowState is the shape every assertion in this file reads.
type rowState struct {
	status        string
	attempts      int32
	adoptRequired bool
	errText       *string
	hasLease      bool
	hasProcessed  bool
	hasRef        bool
}

func readRowState(t testing.TB, pool *pgxpool.Pool, id string) rowState {
	t.Helper()
	var st rowState
	err := pool.QueryRow(context.Background(), `
		SELECT status, attempts, adopt_required, error,
		       claimed_at IS NOT NULL, processed_at IS NOT NULL, claim_ref IS NOT NULL
		  FROM weather_records WHERE id = $1`, id).
		Scan(&st.status, &st.attempts, &st.adoptRequired, &st.errText,
			&st.hasLease, &st.hasProcessed, &st.hasRef)
	if err != nil {
		t.Fatalf("readRowState(%s): %v", id, err)
	}
	return st
}

// claimTwo seeds and claims two rows, returning the store and their ids.
func claimTwo(t testing.TB, pool *pgxpool.Pool) (*postgres.RecordStore, []string) {
	t.Helper()
	seedPending(t, pool, 2, 1000)
	rs := postgres.NewRecordStore(pool)
	claimed, err := rs.ClaimPending(context.Background(), 2, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	ids := make([]string, 0, len(claimed))
	for _, r := range claimed {
		ids = append(ids, r.ID)
	}
	return rs, ids
}

func TestFailPermanentSpendsTheAttemptBudget(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs, ids := claimTwo(t, pool)
	ctx := context.Background()

	if err := rs.FailPermanent(ctx, ids, "script too large"); err != nil {
		t.Fatalf("FailPermanent: %v", err)
	}
	for _, id := range ids {
		st := readRowState(t, pool, id)
		if st.status != string(store.StatusFailed) {
			t.Errorf("%s status = %q, want failed", id, st.status)
		}
		if st.attempts != 1 {
			t.Errorf("%s attempts = %d, want 1 (only a permanent error spends the budget)", id, st.attempts)
		}
		if st.adoptRequired {
			t.Errorf("%s adopt_required = true; a permanent failure needs no adopt check", id)
		}
		if st.errText == nil || *st.errText != "script too large" {
			t.Errorf("%s error = %v, want %q", id, st.errText, "script too large")
		}
		if st.hasLease {
			t.Errorf("%s still holds a lease", id)
		}
		if !st.hasProcessed {
			t.Errorf("%s has no processed_at", id)
		}
	}
}

func TestRequeueInfraKeepsTheBudgetAndNeedsNoAdopt(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs, ids := claimTwo(t, pool)
	ctx := context.Background()

	if err := rs.RequeueInfra(ctx, ids, "storage server unreachable"); err != nil {
		t.Fatalf("RequeueInfra: %v", err)
	}
	for _, id := range ids {
		st := readRowState(t, pool, id)
		if st.status != string(store.StatusPending) {
			t.Errorf("%s status = %q, want pending", id, st.status)
		}
		if st.attempts != 0 {
			t.Errorf("%s attempts = %d, want 0 (infra errors never spend the budget)", id, st.attempts)
		}
		if st.adoptRequired {
			t.Errorf("%s adopt_required = true; the outcome is known to be a non-publish", id)
		}
		if st.hasLease {
			t.Errorf("%s still holds a lease", id)
		}
		if !st.hasRef {
			t.Errorf("%s lost its claim ref", id)
		}
	}
}

func TestMarkUnknownRequiresAnAdoptCheck(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs, ids := claimTwo(t, pool)
	ctx := context.Background()

	if err := rs.MarkUnknown(ctx, ids, "publish outcome ambiguous"); err != nil {
		t.Fatalf("MarkUnknown: %v", err)
	}
	for _, id := range ids {
		st := readRowState(t, pool, id)
		if st.status != string(store.StatusPending) {
			t.Errorf("%s status = %q, want pending", id, st.status)
		}
		if st.attempts != 0 {
			t.Errorf("%s attempts = %d, want 0", id, st.attempts)
		}
		if !st.adoptRequired {
			t.Errorf("%s adopt_required = false; an ambiguous outcome MUST force an adopt check, "+
				"or a row whose prior batch did publish is broadcast a second time", id)
		}
		if !st.hasRef {
			t.Errorf("%s lost its claim ref, which the adopt check needs", id)
		}
		if st.hasLease {
			t.Errorf("%s still holds a lease", id)
		}
	}
}

func TestClassifierWritesOnlyTouchProcessingRows(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	pendingIDs := seedPending(t, pool, 2, 1000)
	rs := postgres.NewRecordStore(pool)

	// Nothing has been claimed, so all three writes must be no-ops.
	if err := rs.FailPermanent(ctx, pendingIDs, "x"); err != nil {
		t.Fatalf("FailPermanent: %v", err)
	}
	if err := rs.RequeueInfra(ctx, pendingIDs, "y"); err != nil {
		t.Fatalf("RequeueInfra: %v", err)
	}
	if err := rs.MarkUnknown(ctx, pendingIDs, "z"); err != nil {
		t.Fatalf("MarkUnknown: %v", err)
	}
	for _, id := range pendingIDs {
		st := readRowState(t, pool, id)
		if st.status != string(store.StatusPending) {
			t.Errorf("%s status = %q, want an untouched pending", id, st.status)
		}
		if st.attempts != 0 || st.adoptRequired || st.errText != nil {
			t.Errorf("%s was modified: %+v", id, st)
		}
	}

	// An unknown id is also a no-op rather than an error.
	if err := rs.FailPermanent(ctx, []string{"no-such-row"}, "x"); err != nil {
		t.Fatalf("FailPermanent for an unknown id: %v", err)
	}
	// An empty id list is a no-op.
	if err := rs.MarkUnknown(ctx, nil, "x"); err != nil {
		t.Fatalf("MarkUnknown with a nil slice: %v", err)
	}
}

// excludedSnapshot is rowState reduced to plain, pointer-free fields.
//
// It exists to close a specific hole: comparing two rowState values directly
// would compare *string fields (errText) across two independent scans, and a
// naive "hold onto the earlier read as the expected value" approach is
// exactly the shape of Task 2's shared-reason-pointer defect — a comparison
// built on a shared or aliased pointer can appear consistent on both sides
// even when the underlying data changed, because nothing forces the two
// sides to be independently sourced values rather than the same memory
// viewed twice. Every field of excludedSnapshot is a value type (string,
// int32, bool), so it is directly `==`-comparable, and `before != after` can
// only be true if the actual COLUMN value changed between the two
// snapshotExcluded calls — there is no pointer for a bug to hide behind.
type excludedSnapshot struct {
	status   string
	attempts int32
	adopt    bool
	hasError bool
	errText  string
	hasLease bool
	hasRef   bool

	// hasProcessed is here because failPermanentSQL sets `processed_at = now()`.
	// readRowState has always read the column, but this snapshot dropped it, so a
	// dropped-id-filter mutation that stamped processed_at on the EXCLUDED row
	// moved a column these three tests exist to prove did not move — and
	// `before != after` stayed false for it.
	hasProcessed bool
}

// snapshotExcluded reads id's full state and immediately dereferences the
// nullable error column into a value (hasError, errText), so the returned
// excludedSnapshot shares no memory with any other snapshot taken before or
// after it.
func snapshotExcluded(t testing.TB, pool *pgxpool.Pool, id string) excludedSnapshot {
	t.Helper()
	st := readRowState(t, pool, id)
	snap := excludedSnapshot{
		status:       st.status,
		attempts:     st.attempts,
		adopt:        st.adoptRequired,
		hasLease:     st.hasLease,
		hasProcessed: st.hasProcessed,
		hasRef:       st.hasRef,
	}
	if st.errText != nil {
		snap.hasError = true
		snap.errText = *st.errText
	}
	return snap
}

// TestFailPermanentLeavesAnExcludedProcessingRowUntouched closes the gap
// mutation testing found: a predicate that widened from
// `r.id = ANY($1::text[]) AND r.status = 'processing'` to just
// `r.status = 'processing'` (i.e. the id filter silently dropped while the
// status guard stayed) passed every other test in this file, because no
// fixture here ever claims MORE rows than it then names in a single call.
// This is the worst-case direction for FailPermanent specifically: an
// infrastructure blip meant for two records would instead terminally fail
// the whole in-flight batch, and 'failed' is not reversible by a later
// retry.
//
// Three rows are claimed so that one can be excluded from the call while two
// are targeted — excluding the ONLY other row would leave nothing in the
// batch for the predicate to over-match against, since a single-row claim
// has no sibling to leak into.
func TestFailPermanentLeavesAnExcludedProcessingRowUntouched(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedPending(t, pool, 3, 1000)
	rs := postgres.NewRecordStore(pool)

	claimed, err := rs.ClaimPending(ctx, 3, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d rows, want 3", len(claimed))
	}
	ids := make([]string, 0, len(claimed))
	for _, r := range claimed {
		ids = append(ids, r.ID)
	}
	targeted := ids[:2]
	excludedID := ids[2]

	before := snapshotExcluded(t, pool, excludedID)

	if err := rs.FailPermanent(ctx, targeted, "script too large"); err != nil {
		t.Fatalf("FailPermanent: %v", err)
	}

	// The two named rows must actually have moved, or the exclusion
	// assertion below would hold vacuously because the call did nothing at
	// all rather than correctly scoping itself to `targeted`.
	for _, id := range targeted {
		st := readRowState(t, pool, id)
		if st.status != string(store.StatusFailed) {
			t.Errorf("targeted row %s status = %q, want failed", id, st.status)
		}
		if st.attempts != 1 {
			t.Errorf("targeted row %s attempts = %d, want 1", id, st.attempts)
		}
	}

	after := snapshotExcluded(t, pool, excludedID)
	if after != before {
		t.Errorf("excluded row %s (not named in the FailPermanent call) changed: before %+v, after %+v",
			excludedID, before, after)
	}
	if after.status != string(store.StatusProcessing) {
		t.Errorf("excluded row %s status = %q, want still processing", excludedID, after.status)
	}
}

// TestRequeueInfraLeavesAnExcludedProcessingRowUntouched is
// TestFailPermanentLeavesAnExcludedProcessingRowUntouched's sibling for
// RequeueInfra: the same dropped-id-filter mutation would return the WHOLE
// batch to pending rather than just the two infra-affected rows, silently
// requeuing a row that may have nothing wrong with it and letting a second,
// unrelated worker pick it up mid-flight.
func TestRequeueInfraLeavesAnExcludedProcessingRowUntouched(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedPending(t, pool, 3, 1000)
	rs := postgres.NewRecordStore(pool)

	claimed, err := rs.ClaimPending(ctx, 3, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d rows, want 3", len(claimed))
	}
	ids := make([]string, 0, len(claimed))
	for _, r := range claimed {
		ids = append(ids, r.ID)
	}
	targeted := ids[:2]
	excludedID := ids[2]

	before := snapshotExcluded(t, pool, excludedID)

	if err := rs.RequeueInfra(ctx, targeted, "storage server unreachable"); err != nil {
		t.Fatalf("RequeueInfra: %v", err)
	}

	for _, id := range targeted {
		st := readRowState(t, pool, id)
		if st.status != string(store.StatusPending) {
			t.Errorf("targeted row %s status = %q, want pending", id, st.status)
		}
		if st.hasLease {
			t.Errorf("targeted row %s still holds a lease", id)
		}
	}

	after := snapshotExcluded(t, pool, excludedID)
	if after != before {
		t.Errorf("excluded row %s (not named in the RequeueInfra call) changed: before %+v, after %+v",
			excludedID, before, after)
	}
	if after.status != string(store.StatusProcessing) {
		t.Errorf("excluded row %s status = %q, want still processing", excludedID, after.status)
	}
}

// TestMarkUnknownLeavesAnExcludedProcessingRowUntouched is
// TestFailPermanentLeavesAnExcludedProcessingRowUntouched's sibling for
// MarkUnknown: the same dropped-id-filter mutation would stamp
// adopt_required on the WHOLE batch, forcing an unnecessary adopt check on a
// row whose outcome was never actually ambiguous.
func TestMarkUnknownLeavesAnExcludedProcessingRowUntouched(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedPending(t, pool, 3, 1000)
	rs := postgres.NewRecordStore(pool)

	claimed, err := rs.ClaimPending(ctx, 3, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d rows, want 3", len(claimed))
	}
	ids := make([]string, 0, len(claimed))
	for _, r := range claimed {
		ids = append(ids, r.ID)
	}
	targeted := ids[:2]
	excludedID := ids[2]

	before := snapshotExcluded(t, pool, excludedID)

	if err := rs.MarkUnknown(ctx, targeted, "publish outcome ambiguous"); err != nil {
		t.Fatalf("MarkUnknown: %v", err)
	}

	for _, id := range targeted {
		st := readRowState(t, pool, id)
		if st.status != string(store.StatusPending) {
			t.Errorf("targeted row %s status = %q, want pending", id, st.status)
		}
		if !st.adoptRequired {
			t.Errorf("targeted row %s adopt_required = false, want true", id)
		}
	}

	after := snapshotExcluded(t, pool, excludedID)
	if after != before {
		t.Errorf("excluded row %s (not named in the MarkUnknown call) changed: before %+v, after %+v",
			excludedID, before, after)
	}
	if after.status != string(store.StatusProcessing) {
		t.Errorf("excluded row %s status = %q, want still processing", excludedID, after.status)
	}
}
