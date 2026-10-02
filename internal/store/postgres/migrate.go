package postgres

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// migrationsSQL is the whole schema.
//
// It is embedded at COMPILE time. That is not merely convenient: reading it
// from disk at runtime would mean os.ReadFile with a path, and gosec G304
// flags a non-constant path while the repository allows zero //nolint
// directives. Embedding removes the question.
//
//go:embed migrations.sql
var migrationsSQL string

// Execer is the subset of pgxpool.Pool and pgx.Tx that Migrate needs.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Migrate applies the schema. It is idempotent: every statement is
// CREATE ... IF NOT EXISTS or an ON CONFLICT DO NOTHING insert, so running it
// on every boot is correct and cheap.
//
// The script is passed to Exec with ZERO arguments, which pgx routes through
// the simple query protocol so that a multi-statement script is legal. That is
// not a violation of this package's no-simple-protocol rule: the rule exists
// because simple_protocol interpolates ARGUMENTS client-side, and there are no
// arguments here. The script is a compile-time constant with nothing in it to
// interpolate.
//
// The underlying error is wrapped rather than translated to a sentinel because
// migrations run at boot and their errors go to a log, never to an HTTP
// response body. Every other path in this package goes through classify.
func Migrate(ctx context.Context, db Execer) error {
	if _, err := db.Exec(ctx, migrationsSQL); err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	return nil
}
