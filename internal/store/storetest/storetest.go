// Package storetest is the Postgres test harness.
//
// It solves two problems that are easy to get wrong in opposite directions.
//
// The first is ergonomics: a developer with no Postgres running must still be
// able to run `go test ./...` and get a green tree. RequireDSN therefore skips
// when WEATHER_TEST_POSTGRES_DSN is unset.
//
// The second is that a skip is indistinguishable from a pass. If the DSN
// variable were ever renamed, the service block dropped from the workflow, or
// the env: key mistyped, every store test would skip and CI would report
// success while executing nothing — and `-count=1` does not help, because a
// SKIP is a PASS. RequireDSN therefore calls t.Fatal instead of t.Skip when
// WEATHER_TEST_REQUIRE_POSTGRES is set, and CI sets it.
//
// A build tag would have been the obvious alternative and is the wrong answer:
// a tagged file is invisible to `go vet ./...`, `go build ./...` and
// golangci-lint, so it rots silently. A genuine type error hidden behind
// //go:build integration was verified to leave both vet and build at exit 0.
// The runtime env-var skip keeps every file compiled, vetted and linted, and
// makes only execution conditional.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
)

// The two environment variables the harness reads. The workflow and the
// Makefile hard-code both names.
const (
	// DSNEnv holds a single Postgres URL. It is deliberately NOT the
	// production PG_HOST/PG_PORT/PG_USER/PG_PASSWORD/PG_DATABASE pieces: tests
	// must not be coupled to production config validation, and one URL is one
	// thing to get right.
	DSNEnv = "WEATHER_TEST_POSTGRES_DSN"

	// RequireEnv turns the local-dev skip into a hard failure. CI sets it.
	RequireEnv = "WEATHER_TEST_REQUIRE_POSTGRES"
)

// cleanupBudget bounds the schema drop. t.Context() is canceled just BEFORE
// cleanup functions run, so cleanup must build its own context.
const cleanupBudget = 30 * time.Second

// RequireDSN returns the test DSN, skipping locally and failing in CI.
func RequireDSN(t testing.TB) string {
	t.Helper()
	if dsn := os.Getenv(DSNEnv); dsn != "" {
		return dsn
	}
	if os.Getenv(RequireEnv) != "" {
		t.Fatalf("%s is unset while %s is set: the Postgres suite must never skip in CI", DSNEnv, RequireEnv)
	}
	t.Skipf("set %s to run this test (make pg-up; see docs/testing-postgres.md)", DSNEnv)
	return ""
}

// Schema is a test package's private Postgres schema.
//
// The three fields look redundant and are not. A schema name cannot be a bind
// parameter, and building "CREATE SCHEMA " + name at runtime is SQL string
// construction — the thing gosec G202 flags and the thing this codebase's
// whole SQL discipline forbids. So the caller supplies the WHOLE statement as
// a compile-time constant and Name is used only for bind parameters and
// assertions. Validate is what keeps the three in agreement.
//
// The consequence, which must be stated rather than discovered: every test
// package gets ONE schema, shared by all of its tests and dropped and
// recreated before each one. Tests in such a package must never call
// t.Parallel(). Different packages must use different Name values, which is
// what makes parallel package execution safe.
type Schema struct {
	Name      string
	CreateSQL string
	DropSQL   string
}

// Validate reports whether the three fields agree.
func (s Schema) Validate() error {
	if s.Name == "" {
		return errors.New("storetest: Schema.Name is empty")
	}
	if !strings.Contains(s.CreateSQL, s.Name) {
		return fmt.Errorf("storetest: Schema.CreateSQL does not mention %q", s.Name)
	}
	if !strings.Contains(s.DropSQL, s.Name) {
		return fmt.Errorf("storetest: Schema.DropSQL does not mention %q", s.Name)
	}
	return nil
}

// Pool returns a pool whose every connection is pinned to a freshly created,
// empty copy of schema.
//
// The schema is dropped and recreated on entry, so a crashed previous run
// cannot leak rows into this one, and dropped again on cleanup. Cleanup order
// matters and is enforced by registration order: t.Cleanup runs LIFO, so the
// drop is registered FIRST and the pool close SECOND, which means the pool is
// closed before the drop runs. DROP SCHEMA CASCADE against a schema with live
// sessions in it would otherwise block.
//
// maxConns must be at least the goroutine count of any concurrency test using
// this pool. A pool pinned to one connection serializes the race and makes
// such a test a vacuous pass.
func Pool(t testing.TB, schema Schema, maxConns int32) *pgxpool.Pool {
	t.Helper()
	if err := schema.Validate(); err != nil {
		t.Fatalf("storetest.Pool: %v", err)
	}
	dsn := RequireDSN(t)
	ctx := context.Background()

	admin, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("storetest.Pool: admin pool: %v", err)
	}
	// dropErr and createErr rather than err: the outer err is still live, and
	// govet's shadow check rejects a redeclaration. Every inner error in this
	// plan is named after its operation for exactly this reason.
	if _, dropErr := admin.Exec(ctx, schema.DropSQL); dropErr != nil {
		admin.Close()
		t.Fatalf("storetest.Pool: %s: %v", schema.DropSQL, dropErr)
	}
	if _, createErr := admin.Exec(ctx, schema.CreateSQL); createErr != nil {
		admin.Close()
		t.Fatalf("storetest.Pool: %s: %v", schema.CreateSQL, createErr)
	}

	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
		defer cancel()
		if _, dropErr := admin.Exec(dropCtx, schema.DropSQL); dropErr != nil {
			t.Errorf("storetest cleanup: %s: %v", schema.DropSQL, dropErr)
		}
		admin.Close()
	})

	cfg, err := postgres.PoolConfig(dsn)
	if err != nil {
		t.Fatalf("storetest.Pool: %v", err)
	}
	// Pinning search_path as a connection runtime parameter is what makes the
	// isolation total: pgx sends it at startup on EVERY pooled connection, so
	// no statement in any test needs to qualify a table name.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema.Name
	cfg.MaxConns = maxConns
	if cfg.MinConns > maxConns {
		cfg.MinConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("storetest.Pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if pingErr := pool.Ping(ctx); pingErr != nil {
		t.Fatalf("storetest.Pool: ping: %v", pingErr)
	}
	return pool
}
