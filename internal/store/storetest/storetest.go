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
	"sync"
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

// poolCloseBudget bounds how long cleanup waits for the pool built here to
// drain before giving up.
//
// puddle (pgxpool's underlying pool) blocks Close until every acquired
// connection is returned. A downstream test that fails while holding an
// acquired connection or an open transaction — pool.Begin then t.Fatal with
// no Rollback is the classic shape — never returns it, so the raw,
// unbounded pool.Close would wedge cleanup forever. Reproduced with
// `-timeout 20s`: the process hung the full 20s, was killed, and the real
// t.Fatal message was buried about 50 lines down a goroutine dump. Bounding
// the wait with postgres.ClosePool turns that into a fast, attributable
// failure instead, and lets the schema-drop cleanup still get its turn (see
// Pool's single consolidated cleanup below).
const poolCloseBudget = 3 * time.Second

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
	if !containsIdent(s.CreateSQL, s.Name) {
		return fmt.Errorf("storetest: Schema.CreateSQL does not mention %q as a whole identifier", s.Name)
	}
	if !containsIdent(s.DropSQL, s.Name) {
		return fmt.Errorf("storetest: Schema.DropSQL does not mention %q as a whole identifier", s.Name)
	}
	return nil
}

// isIdentByte reports whether b can appear inside one of the plain,
// lowercase, underscore-separated schema identifiers every Schema literal in
// this codebase uses.
func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// containsIdent reports whether name appears in sql as a whole identifier,
// not merely as a substring of some longer one.
//
// strings.Contains alone has a substring-collision blind spot:
// Schema{Name: "a", CreateSQL: "CREATE SCHEMA abc"} would validate even
// though CreateSQL actually names an entirely different schema, "abc". Since
// Validate is the one automated check keeping Schema's three fields honest,
// that gap defeats its whole purpose.
func containsIdent(sql, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i+len(name) <= len(sql); i++ {
		if sql[i:i+len(name)] != name {
			continue
		}
		beforeOK := i == 0 || !isIdentByte(sql[i-1])
		afterOK := i+len(name) == len(sql) || !isIdentByte(sql[i+len(name)])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

// activeSchemas guards against two tests in one process sharing a
// Schema.Name at the same time.
//
// Per-package schema isolation is this harness's whole design (see Schema's
// doc comment): one test's entry DROP SCHEMA ... CASCADE racing another
// test's already-pinned connection does not merely serialize, it corrupts
// state — reproduced as
// `ERROR: no schema has been selected to create in (SQLSTATE 3F000)` when
// the drop yanks the schema out from under a connection that already has it
// pinned as search_path. This map does not attempt to make concurrent use of
// one name work — that is out of scope by design — it only turns the
// corruption into an immediate, legible refusal.
var (
	activeSchemasMu sync.Mutex
	activeSchemas   = map[string]bool{}
)

// lockSchema registers name as in-use and reports whether it succeeded. A
// false return means another concurrently running test in this process
// already holds name.
func lockSchema(name string) bool {
	activeSchemasMu.Lock()
	defer activeSchemasMu.Unlock()
	if activeSchemas[name] {
		return false
	}
	activeSchemas[name] = true
	return true
}

// unlockSchema releases name so a later, sequential test may reuse it.
func unlockSchema(name string) {
	activeSchemasMu.Lock()
	defer activeSchemasMu.Unlock()
	delete(activeSchemas, name)
}

// Pool returns a pool whose every connection is pinned to a freshly created,
// empty copy of schema.
//
// The schema is dropped and recreated on entry, so a crashed previous run
// cannot leak rows into this one, and dropped again on cleanup. Cleanup order
// is explicit rather than an accident of t.Cleanup's LIFO registration order:
// a single consolidated callback closes the pool (bounded by
// poolCloseBudget) BEFORE attempting the schema drop (bounded by
// cleanupBudget), because a live session inside the schema could otherwise
// block DROP SCHEMA ... CASCADE. Both steps are independently bounded so
// neither a wedged connection nor a lock wait can hang the test process past
// its own -timeout, and the drop still runs even when the close budget is
// exceeded.
//
// Pool also refuses a second concurrent call for the same schema.Name in
// this process — see activeSchemas — rather than let two tests corrupt
// shared state.
//
// maxConns must be at least the goroutine count of any concurrency test using
// this pool. A pool pinned to one connection serializes the race and makes
// such a test a vacuous pass.
func Pool(t testing.TB, schema Schema, maxConns int32) *pgxpool.Pool {
	t.Helper()
	if err := schema.Validate(); err != nil {
		t.Fatalf("storetest.Pool: %v", err)
	}
	if !lockSchema(schema.Name) {
		t.Fatalf("storetest.Pool: schema %q is already in use by another concurrently running test in this "+
			"process. storetest.Pool shares one schema per Schema.Name and does not support two tests using "+
			"the same name at the same time — give each parallel test its own Schema.Name, or do not call "+
			"t.Parallel() on tests that share one.", schema.Name)
	}
	t.Cleanup(func() { unlockSchema(schema.Name) })

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

	// pool is filled in below only once pgxpool.NewWithConfig succeeds. It is
	// declared here, ahead of the one cleanup that closes it, so that
	// cleanup can run safely (skipping the close step) even on an early
	// t.Fatalf between here and the assignment.
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			if closeErr := postgres.ClosePool(pool, poolCloseBudget); closeErr != nil {
				t.Errorf("storetest cleanup: pool close: %v — likely cause: a test in this package failed "+
					"while holding an acquired connection or an open transaction (pool.Acquire or pool.Begin) "+
					"without releasing, committing or rolling it back on its failure path", closeErr)
			}
		}
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

	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("storetest.Pool: %v", err)
	}

	if pingErr := pool.Ping(ctx); pingErr != nil {
		t.Fatalf("storetest.Pool: ping: %v", pingErr)
	}
	return pool
}
