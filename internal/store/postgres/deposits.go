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

const markInternalizedSQL = `
UPDATE deposits
   SET txid = $2, vout = $3, satoshis = $4, internalized_at = now()
 WHERE suffix = $1`

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
	ct, err := s.db.Exec(ctx, markInternalizedSQL, suffix, txID, vout, sats)
	if err != nil {
		return classify(err)
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
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
