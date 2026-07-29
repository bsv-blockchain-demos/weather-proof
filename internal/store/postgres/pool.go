// Package postgres is the pgx/v5 implementation of the store interfaces.
//
// Two rules hold everywhere in this package and Task 17's security test is
// their mechanical proof. First, every statement is a package-level constant
// with numbered bind parameters: no statement text is assembled at runtime, so
// there is no interpolation surface at all. Second, no driver error escapes:
// classify translates everything to store.ErrNotFound, store.ErrConflict or
// ErrOperation carrying only a 5-character SQLSTATE, because a
// *pgconn.PgError's Error() and fields carry SQL text, table, column and
// constraint names and sometimes column values.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool sizing. MaxConns must stay comfortably above the goroutine count of
// the claim-concurrency test: a pool pinned small serializes the race and
// turns that test into a vacuous pass, which is a recorded house failure.
const (
	poolMaxConns = 16
	poolMinConns = 2
)

var (
	// ErrSimpleProtocol is returned when the connection string asks for the
	// simple query protocol.
	//
	// pgx reads default_query_exec_mode from the connection-string runtime
	// params, and simple_protocol is the one mode that interpolates arguments
	// client-side through internal/sanitize. That re-introduces the SQL-text
	// surface parameterization removes, cluster-wide, from a config value
	// outside the binary. The ban is therefore a runtime assertion and not a
	// code-review convention.
	ErrSimpleProtocol = errors.New("postgres: default_query_exec_mode=simple_protocol is forbidden")

	// ErrCloseTimeout is returned when the pool did not drain inside the
	// caller's budget.
	ErrCloseTimeout = errors.New("postgres: pool did not close within the budget")
)

// PoolConfig parses dsn, rejects the forbidden exec mode and applies this
// service's pool tuning.
//
// It is exported because the test harness needs to pin search_path on every
// pooled connection, which means mutating the config between parse and
// construction. pgxpool.NewWithConfig PANICS on a config that did not come
// from ParseConfig, so parse-then-mutate-then-construct is the only legal
// shape.
func PoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// pgx redacts the password to "xxxxxx" inside this message before
		// returning it, which is why wrapping it is safe. pool_test.go is the
		// regression gate on that.
		return nil, fmt.Errorf("postgres: parse connection string: %w", err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		return nil, ErrSimpleProtocol
	}
	cfg.MaxConns = poolMaxConns
	cfg.MinConns = poolMinConns
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	return cfg, nil
}

// NewPool builds a pool from dsn.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := PoolConfig(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: build pool: %w", err)
	}
	return pool, nil
}

// ClosePool closes pool but gives up after budget.
//
// pgxpool.Pool.Close takes no context and returns no error, so a caller with a
// shutdown deadline cannot enforce it any other way. On timeout the pool is
// left draining in the background: the process is exiting anyway, and blocking
// shutdown forever is the worse failure.
func ClosePool(pool *pgxpool.Pool, budget time.Duration) error {
	done := make(chan struct{})
	go func() {
		pool.Close()
		close(done)
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return ErrCloseTimeout
	}
}
