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
