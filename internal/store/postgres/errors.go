package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// SQLSTATE codes this package reacts to, as named constants so no call site
// carries a bare five-character literal.
//
// Codes deliberately NOT listed, because they are all handled by the same
// opaque fallback: 23514 check_violation and 23502 not_null_violation (a bug
// in this package if reached, since every write satisfies the constraints by
// construction), 22P02 invalid_text_representation and 22003
// numeric_out_of_range (prevented upstream by parameter validation in the API
// layer, where a station id is range-checked before it reaches SQL).
const (
	sqlStateUniqueViolation    = "23505"
	sqlStateSerializationFail  = "40001"
	sqlStateDeadlockDetected   = "40P01"
	sqlStateQueryCanceled      = "57014"
	sqlStateTooManyConnections = "53300"
)

var (
	// ErrOperation is the opaque failure that every unclassified database error
	// becomes. It exists so that no caller can accidentally surface driver
	// detail: a *pgconn.PgError's Error() is "severity: message (SQLSTATE
	// code)" and the struct additionally carries Detail, Hint, ConstraintName,
	// ColumnName and TableName, any of which can disclose schema or column
	// values.
	ErrOperation = errors.New("store: database operation failed")

	// ErrTransient is the retryable bucket: serialization failure, deadlock,
	// query cancellation and connection exhaustion. The publisher's error
	// classifier maps this onto its infra class without importing pgconn.
	ErrTransient = errors.New("store: transient database failure")
)

// classify translates a driver error into this package's error vocabulary.
//
// It is the ONLY place a *pgconn.PgError is inspected, and nothing it returns
// carries anything beyond a five-character SQLSTATE or a bare context
// sentinel. Callers classify with errors.Is against store.ErrNotFound,
// store.ErrConflict, ErrTransient, ErrOperation, context.Canceled and
// context.DeadlineExceeded; errorlint forbids matching on error text and this
// is why.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	// Cancellation and deadline are the caller's own signal and must stay
	// CLASSIFIABLE, so that a client disconnect is distinguishable from a
	// database fault — but the driver's text must not travel with them. This is
	// the one branch where a DSN fragment could otherwise escape: a pgxpool
	// acquire or connect timeout satisfies errors.Is(err,
	// context.DeadlineExceeded) while its message is
	// `failed to connect to \`host=… user=… database=…\`: …`, which discloses
	// the host, the user and the database name. Re-wrapping the bare sentinel
	// keeps errors.Is working and drops the text.
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w", context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w", context.DeadlineExceeded)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		// A network or protocol failure with no SQLSTATE at all.
		return ErrOperation
	}

	switch pgErr.Code {
	case sqlStateUniqueViolation:
		return fmt.Errorf("%w (sqlstate %s)", store.ErrConflict, pgErr.Code)
	case sqlStateSerializationFail, sqlStateDeadlockDetected,
		sqlStateQueryCanceled, sqlStateTooManyConnections:
		return fmt.Errorf("%w (sqlstate %s)", ErrTransient, pgErr.Code)
	}
	return fmt.Errorf("%w (sqlstate %s)", ErrOperation, pgErr.Code)
}
