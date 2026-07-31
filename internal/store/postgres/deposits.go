package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// depositColumns is the explicit column list of deposits, matching
// store.Deposit's db tags in order.
const depositColumns = `suffix, prefix, address, locking_script, created_at,
       txid, vout, satoshis, internalized_at`

const newDepositSQL = `
INSERT INTO deposits (suffix, prefix, address, locking_script)
VALUES ($1, $2, $3, $4)`

// pendingDepositsSQL lists deposits awaiting internalization, oldest first.
// The suffix tiebreaker makes the order total even when several deposits were
// created in one transaction.
const pendingDepositsSQL = `
SELECT ` + depositColumns + `
  FROM deposits WHERE internalized_at IS NULL
 ORDER BY created_at ASC, suffix ASC`

// markInternalizedSQL resolves the outpoint of a deposit that is still pending.
//
// `AND internalized_at IS NULL` is the whole point of the statement's shape.
// Without it the UPDATE matched any row with that suffix, so a second call —
// a retry, a duplicated operator action, a replayed message — REPLACED the
// stored txid, vout and satoshis and destroyed the record of the first
// internalization, which is the only thing tying the deposit to a real
// outpoint.
//
// The two EXISTS columns are what let the caller tell "no such deposit" from
// "already internalized": with the guard in place, both cases affect zero rows
// and RowsAffected alone cannot distinguish them. Kept as ONE statement rather
// than an UPDATE plus a follow-up probe so the answer comes from one snapshot.
const markInternalizedSQL = `
WITH updated AS (
  UPDATE deposits
     SET txid = $2, vout = $3, satoshis = $4, internalized_at = now()
   WHERE suffix = $1 AND internalized_at IS NULL
  RETURNING suffix
)
SELECT EXISTS (SELECT 1 FROM deposits WHERE suffix = $1) AS found,
       EXISTS (SELECT 1 FROM updated)                    AS updated`

const preflightOKSQL = `SELECT 1 FROM app_preflight WHERE fingerprint = $1`

// recordPreflightSQL is upserting rather than inserting because it runs on
// every boot with the same fingerprint whenever the configuration has not
// changed.
const recordPreflightSQL = `
INSERT INTO app_preflight (fingerprint, ok_at) VALUES ($1, now())
ON CONFLICT (fingerprint) DO UPDATE SET ok_at = now()`

// DepositStore is the pgx/v5 implementation of store.DepositStore.
type DepositStore struct {
	db *pgxpool.Pool
}

// NewDepositStore returns a DepositStore backed by db.
func NewDepositStore(db *pgxpool.Pool) *DepositStore {
	return &DepositStore{db: db}
}

// NewDeposit implements store.DepositStore.
//
// Only the four columns the caller can legitimately know are written. The
// outpoint columns stay NULL until MarkInternalized resolves them, which is
// what makes "pending" a property of the row rather than of a separate flag.
func (s *DepositStore) NewDeposit(ctx context.Context, d store.Deposit) error {
	_, err := s.db.Exec(ctx, newDepositSQL, d.Suffix, d.Prefix, d.Address, d.LockingScript)
	if err != nil {
		return classify(err)
	}
	return nil
}

// PendingDeposits implements store.DepositStore.
func (s *DepositStore) PendingDeposits(ctx context.Context) ([]store.Deposit, error) {
	rows, err := s.db.Query(ctx, pendingDepositsSQL)
	if err != nil {
		return nil, classify(err)
	}
	deposits, err := pgx.CollectRows(rows, pgx.RowToStructByName[store.Deposit])
	if err != nil {
		return nil, classify(err)
	}
	return deposits, nil
}

// MarkInternalized implements store.DepositStore.
func (s *DepositStore) MarkInternalized(
	ctx context.Context, suffix, txID string, vout int32, sats int64,
) error {
	var found, updated bool
	if err := s.db.QueryRow(ctx, markInternalizedSQL, suffix, txID, vout, sats).
		Scan(&found, &updated); err != nil {
		return classify(err)
	}
	switch {
	case !found:
		return store.ErrNotFound
	case !updated:
		return store.ErrConflict
	}
	return nil
}

// PreflightStore is the pgx/v5 implementation of store.PreflightStore.
type PreflightStore struct {
	db *pgxpool.Pool
}

// NewPreflightStore returns a PreflightStore backed by db.
func NewPreflightStore(db *pgxpool.Pool) *PreflightStore {
	return &PreflightStore{db: db}
}

// PreflightOK implements store.PreflightStore.
//
// ctx is present on both methods because preflight runs on the boot path behind
// the same per-call deadlines as everything else, and because a boot that hangs
// on a database round trip must be cancellable.
func (s *PreflightStore) PreflightOK(ctx context.Context, fingerprint string) (bool, error) {
	var one int
	err := s.db.QueryRow(ctx, preflightOKSQL, fingerprint).Scan(&one)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, classify(err)
	}
	return true, nil
}

// RecordPreflight implements store.PreflightStore.
func (s *PreflightStore) RecordPreflight(ctx context.Context, fingerprint string) error {
	if _, err := s.db.Exec(ctx, recordPreflightSQL, fingerprint); err != nil {
		return classify(err)
	}
	return nil
}
