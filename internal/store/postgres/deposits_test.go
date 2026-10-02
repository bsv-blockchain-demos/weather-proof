package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

var (
	_ func(context.Context, store.Deposit) error                = (*postgres.DepositStore)(nil).NewDeposit
	_ func(context.Context) ([]store.Deposit, error)            = (*postgres.DepositStore)(nil).PendingDeposits
	_ func(context.Context, string, string, int32, int64) error = (*postgres.DepositStore)(nil).MarkInternalized
	_ func(context.Context, string) (bool, error)               = (*postgres.PreflightStore)(nil).PreflightOK
	_ func(context.Context, string) error                       = (*postgres.PreflightStore)(nil).RecordPreflight
)

func TestDepositLifecycle(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ds := postgres.NewDepositStore(pool)

	first := store.Deposit{
		Suffix: "c3VmZml4LW9uZQ==", Prefix: "cHJlZml4LW9uZQ==",
		Address:       "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
		LockingScript: "76a914" + "00112233445566778899aabbccddeeff00112233" + "88ac",
	}
	second := store.Deposit{
		Suffix: "c3VmZml4LXR3bw==", Prefix: "cHJlZml4LXR3bw==",
		Address:       "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
		LockingScript: "76a914" + "ffeeddccbbaa99887766554433221100ffeeddcc" + "88ac",
	}

	if err := ds.NewDeposit(ctx, first); err != nil {
		t.Fatalf("NewDeposit first: %v", err)
	}
	if err := ds.NewDeposit(ctx, second); err != nil {
		t.Fatalf("NewDeposit second: %v", err)
	}

	pending, err := ds.PendingDeposits(ctx)
	if err != nil {
		t.Fatalf("PendingDeposits: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}
	for _, d := range pending {
		if d.InternalizedAt != nil {
			t.Errorf("%s is already internalized", d.Suffix)
		}
		if d.TxID != nil || d.Vout != nil || d.Satoshis != nil {
			t.Errorf("%s has premature outpoint data: %+v", d.Suffix, d)
		}
		if d.CreatedAt.IsZero() {
			t.Errorf("%s has a zero CreatedAt", d.Suffix)
		}
		if d.Prefix == "" || d.Address == "" || d.LockingScript == "" {
			t.Errorf("%s lost a column: %+v", d.Suffix, d)
		}
	}

	if markErr := ds.MarkInternalized(ctx, first.Suffix,
		"7f2c4e1d8a9b0c3e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7081920", 1, 250000); markErr != nil {
		t.Fatalf("MarkInternalized: %v", markErr)
	}

	pending, err = ds.PendingDeposits(ctx)
	if err != nil {
		t.Fatalf("PendingDeposits after internalize: %v", err)
	}
	if len(pending) != 1 || pending[0].Suffix != second.Suffix {
		got := make([]string, 0, len(pending))
		for _, d := range pending {
			got = append(got, d.Suffix)
		}
		t.Fatalf("pending = %v, want only %q", got, second.Suffix)
	}

	var txid string
	var vout int32
	var sats int64
	err = pool.QueryRow(ctx,
		"SELECT txid, vout, satoshis FROM deposits WHERE suffix = $1", first.Suffix).
		Scan(&txid, &vout, &sats)
	if err != nil {
		t.Fatalf("reading the internalized deposit: %v", err)
	}
	if txid != "7f2c4e1d8a9b0c3e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7081920" || vout != 1 || sats != 250000 {
		t.Fatalf("outpoint = (%q, %d, %d)", txid, vout, sats)
	}
}

func TestDuplicateDepositSuffixIsErrConflict(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ds := postgres.NewDepositStore(pool)

	d := store.Deposit{Suffix: "dup", Prefix: "p", Address: "a", LockingScript: "76a9"}
	if err := ds.NewDeposit(ctx, d); err != nil {
		t.Fatalf("first NewDeposit: %v", err)
	}
	err := ds.NewDeposit(ctx, d)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate NewDeposit error = %v, want store.ErrConflict", err)
	}
}

func TestMarkInternalizedUnknownSuffixIsErrNotFound(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ds := postgres.NewDepositStore(pool)

	err := ds.MarkInternalized(ctx, "no-such-suffix", "txid", 0, 1000)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("MarkInternalized error = %v, want store.ErrNotFound", err)
	}
}

// TestMarkInternalizedTwiceIsErrConflictAndWritesNothing is the column-level
// half of the claim the conformance suite can only assert an error for: the
// DepositStore interface has no read path for an internalized deposit, so the
// stored outpoint is only reachable through the pool.
//
// Before `AND internalized_at IS NULL` the second call matched the same row,
// REPLACED the txid, vout and satoshis of the first internalization and returned
// nil — destroying the only record tying the deposit to a real outpoint, and
// reporting success while doing it.
func TestMarkInternalizedTwiceIsErrConflictAndWritesNothing(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ds := postgres.NewDepositStore(pool)

	const firstTxID = "7f2c4e1d8a9b0c3e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7081920"
	const secondTxID = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	d := store.Deposit{Suffix: "twice", Prefix: "p", Address: "a", LockingScript: "76a9"}
	if err := ds.NewDeposit(ctx, d); err != nil {
		t.Fatalf("NewDeposit: %v", err)
	}
	if err := ds.MarkInternalized(ctx, d.Suffix, firstTxID, 1, 250000); err != nil {
		t.Fatalf("first MarkInternalized: %v", err)
	}

	err := ds.MarkInternalized(ctx, d.Suffix, secondTxID, 9, 999)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second MarkInternalized = %v, want store.ErrConflict", err)
	}
	// ErrConflict and not ErrNotFound: the row EXISTS, and a caller has to be
	// able to tell "already resolved" from "no such deposit" — with the guard in
	// place both affect zero rows.
	if errors.Is(err, store.ErrNotFound) {
		t.Error("second MarkInternalized reported ErrNotFound for a deposit that exists")
	}

	var txid string
	var vout int32
	var sats int64
	if scanErr := pool.QueryRow(ctx,
		"SELECT txid, vout, satoshis FROM deposits WHERE suffix = $1", d.Suffix).
		Scan(&txid, &vout, &sats); scanErr != nil {
		t.Fatalf("re-reading the deposit: %v", scanErr)
	}
	if txid != firstTxID || vout != 1 || sats != 250000 {
		t.Fatalf("outpoint = (%q, %d, %d), want the FIRST internalization preserved", txid, vout, sats)
	}
}

func TestPendingDepositsOnAnEmptyTable(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ds := postgres.NewDepositStore(pool)

	pending, err := ds.PendingDeposits(ctx)
	if err != nil {
		t.Fatalf("PendingDeposits: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}
}

func TestPreflightRoundTrip(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ps := postgres.NewPreflightStore(pool)

	const fp = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	ok, err := ps.PreflightOK(ctx, fp)
	if err != nil {
		t.Fatalf("PreflightOK before: %v", err)
	}
	if ok {
		t.Fatal("PreflightOK = true before any record")
	}

	if recordErr := ps.RecordPreflight(ctx, fp); recordErr != nil {
		t.Fatalf("RecordPreflight: %v", recordErr)
	}
	ok, err = ps.PreflightOK(ctx, fp)
	if err != nil {
		t.Fatalf("PreflightOK after: %v", err)
	}
	if !ok {
		t.Fatal("PreflightOK = false after RecordPreflight")
	}

	// Recording the same fingerprint twice must be idempotent, since it runs on
	// every boot.
	if secondErr := ps.RecordPreflight(ctx, fp); secondErr != nil {
		t.Fatalf("second RecordPreflight: %v", secondErr)
	}
	var rows int
	if countErr := pool.QueryRow(ctx, "SELECT count(*) FROM app_preflight").Scan(&rows); countErr != nil {
		t.Fatalf("counting: %v", countErr)
	}
	if rows != 1 {
		t.Fatalf("app_preflight has %d rows, want 1", rows)
	}

	// An unrelated fingerprint is still absent.
	ok, err = ps.PreflightOK(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("PreflightOK unrelated: %v", err)
	}
	if ok {
		t.Fatal("PreflightOK = true for an unrelated fingerprint")
	}
}

// TestPendingDepositsTiebreaksBySuffixUnderHeapChurn pins the `, suffix ASC`
// tiebreaker in pendingDepositsSQL's ORDER BY.
//
// EXPLAIN against this exact schema (measured, scratch database, not this
// suite's fixtures) shows deposits carries no index beyond its suffix primary
// key, so this query's plan shape is a Seq Scan feeding an explicit Sort node
// — the same shape records_requeue_test.go measured for reapExpiredSQL and
// stations_test.go measured for the search-rank tiebreaker. Forty deposits
// created in ONE transaction, so created_at ties for the whole group (the
// "transaction timestamp, identical across a batch" fact this task was
// briefed on), then churned into reapChurnOrder's permutation, were measured
// directly: WITH the suffix tiebreaker the returned order is exactly
// ascending d000..d039 regardless of churn; WITHOUT it, the returned order is
// exactly the churn permutation itself. reapChurnOrder/reapChurnCount are
// reused rather than redeclared, as stations_test.go already does — the
// property they provide (some heap order other than insertion order) is not
// query-specific.
func TestPendingDepositsTiebreaksBySuffixUnderHeapChurn(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ds := postgres.NewDepositStore(pool)

	// reapChurnCount and reapChurnOrder are declared in
	// records_requeue_test.go, in this same package.
	suffixes := make([]string, reapChurnCount)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i := range reapChurnCount {
		suffixes[i] = fmt.Sprintf("d%03d", i)
		if _, execErr := tx.Exec(ctx,
			"INSERT INTO deposits (suffix, prefix, address, locking_script) VALUES ($1, 'p', 'a', 's')",
			suffixes[i]); execErr != nil {
			t.Fatalf("seeding %d: %v", i, execErr)
		}
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// Churn the heap before listing: see the doc comment above for why a
	// freshly loaded heap cannot discriminate the tiebreaker for THIS query's
	// plan shape. The no-op SET prefix = prefix still moves the row to a new
	// physical heap slot under MVCC.
	for _, idx := range reapChurnOrder {
		if _, execErr := pool.Exec(ctx,
			"UPDATE deposits SET prefix = prefix WHERE suffix = $1", suffixes[idx]); execErr != nil {
			t.Fatalf("churning row %d: %v", idx, execErr)
		}
	}

	pending, err := ds.PendingDeposits(ctx)
	if err != nil {
		t.Fatalf("PendingDeposits: %v", err)
	}
	if len(pending) != len(suffixes) {
		t.Fatalf("got %d rows, want %d", len(pending), len(suffixes))
	}
	for i, want := range suffixes {
		if pending[i].Suffix != want {
			got := make([]string, len(pending))
			for j, d := range pending {
				got[j] = d.Suffix
			}
			t.Fatalf("got %v, want strictly ascending %v (tiebreaker not honored under churn)", got, suffixes)
		}
	}
}
