package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// The two environment variables the Postgres suite reads, duplicated from
// storetest.DSNEnv/RequireEnv rather than imported: storetest imports this
// package (to call Migrate, NewPool, PoolConfig...), so this file — which
// needs package-internal access to reapExpiredSQL and must therefore live in
// package postgres, not postgres_test — cannot import storetest without
// creating an import cycle. Confirmed by trying it: `go vet` rejected it with
// "import cycle not allowed in test". The two literal strings are the whole
// cost of that constraint; everything else below reuses this package's own
// exported Migrate/NewPool/PoolConfig/ClosePool, the same functions
// storetest itself is built on.
const (
	reapBoundaryDSNEnv     = "WEATHER_TEST_POSTGRES_DSN"
	reapBoundaryRequireEnv = "WEATHER_TEST_REQUIRE_POSTGRES"
)

// reapBoundarySchemaName, reapBoundaryCreateSQL and reapBoundaryDropSQL are
// this file's own private schema, distinct from postgres_test's storeSchema
// (migrations_test.go). storetest.Schema's own doc comment requires
// different packages to use different schema names, and package postgres
// (this file) and package postgres_test (every black-box test in this
// directory) are different Go packages even though `go test` links them
// into one binary — this file cannot see postgres_test's unexported
// storeSchema var across that boundary, so it needs its own, and it must not
// collide with it.
//
// The two SQL strings are compile-time constants, never built by
// concatenating the name in at runtime — storetest.Schema's own doc comment
// explains why: that shape is exactly what gosec G202 (SQL query
// construction via string concatenation) flags, and this codebase's whole
// SQL discipline forbids it regardless of whether the concatenated piece is
// itself a constant.
const (
	reapBoundarySchemaName = "wp_test_reap_boundary"
	reapBoundaryCreateSQL  = "CREATE SCHEMA IF NOT EXISTS wp_test_reap_boundary"
	reapBoundaryDropSQL    = "DROP SCHEMA IF EXISTS wp_test_reap_boundary CASCADE"
)

// freshReapBoundaryPool is a minimal, self-contained replica of
// storetest.Pool + storetest.Fresh, scoped to this one file's single test.
// It skips (or, with reapBoundaryRequireEnv set, fails hard) exactly like
// storetest.RequireDSN, so this is the ordinary Postgres-availability skip
// every test in this suite already uses — not the special TestHelper*
// subprocess-gating pattern storetest_test.go uses elsewhere in this tree.
func freshReapBoundaryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(reapBoundaryDSNEnv)
	if dsn == "" {
		if os.Getenv(reapBoundaryRequireEnv) != "" {
			t.Fatalf("%s is unset while %s is set: the Postgres suite must never skip in CI",
				reapBoundaryDSNEnv, reapBoundaryRequireEnv)
		}
		t.Skipf("set %s to run this test (make pg-up; see docs/testing-postgres.md)", reapBoundaryDSNEnv)
	}
	ctx := context.Background()

	admin, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("freshReapBoundaryPool: admin pool: %v", err)
	}
	if _, dropErr := admin.Exec(ctx, reapBoundaryDropSQL); dropErr != nil {
		admin.Close()
		t.Fatalf("freshReapBoundaryPool: drop schema: %v", dropErr)
	}
	if _, createErr := admin.Exec(ctx, reapBoundaryCreateSQL); createErr != nil {
		admin.Close()
		t.Fatalf("freshReapBoundaryPool: create schema: %v", createErr)
	}

	cfg, err := PoolConfig(dsn)
	if err != nil {
		admin.Close()
		t.Fatalf("freshReapBoundaryPool: PoolConfig: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = reapBoundarySchemaName

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Close()
		t.Fatalf("freshReapBoundaryPool: NewWithConfig: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("freshReapBoundaryPool: ping: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := ClosePool(pool, 3*time.Second); closeErr != nil {
			t.Errorf("freshReapBoundaryPool cleanup: pool close: %v", closeErr)
		}
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, dropErr := admin.Exec(dropCtx, reapBoundaryDropSQL); dropErr != nil {
			t.Errorf("freshReapBoundaryPool cleanup: drop schema: %v", dropErr)
		}
		admin.Close()
	})

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("freshReapBoundaryPool: Migrate: %v", err)
	}
	return pool
}

// TestReapExpiredLeaseBoundaryIsStrict pins reapExpiredSQL's
// `claimed_at < now() - $1::interval` as STRICT: a row claimed EXACTLY lease
// ago is still within its lease and must be left processing, untouched; only
// a row strictly OLDER than the horizon may be reaped.
//
// WHY THIS RUNS reapExpiredSQL DIRECTLY, INSIDE ONE TRANSACTION, RATHER THAN
// CALLING THE PUBLIC ReapExpired METHOD TWICE (once to seed via a separate
// statement, once to reap). Postgres's now() is transaction_timestamp() —
// fixed for the lifetime of one transaction, not re-evaluated per statement.
// A seed INSERT through the pool and a later ReapExpired call through the
// pool are necessarily two SEPARATE transactions, and reasoned algebraically:
// if T0 is the seed transaction's now() and T1 is the later reap
// transaction's own now(), then T1 > T0 always holds, because real
// wall-clock time strictly elapses between any two separate round trips. A
// row seeded as `claimed_at = T0 - lease` therefore satisfies
// `claimed_at < T1 - lease` (since T0 < T1) REGARDLESS of whether the
// comparison is `<` or `<=` — the row is unavoidably strictly before the
// second call's cutoff, so it would be reaped either way, and such a fixture
// cannot discriminate the mutation this test exists to catch no matter how
// small the margin is made. Only running the seed INSERT and the
// reapExpiredSQL UPDATE inside the SAME transaction gives both statements the
// IDENTICAL now(), which is the only way to place a row's claimed_at EXACTLY
// on the cutoff rather than merely close to it. That requires the private
// reapExpiredSQL constant directly, hence this file is package postgres
// (white-box), following errors_internal_test.go's precedent for the same
// reason: a black-box test cannot reach the branch this assertion guards.
func TestReapExpiredLeaseBoundaryIsStrict(t *testing.T) {
	pool := freshReapBoundaryPool(t)
	ctx := context.Background()
	lease := 5 * time.Minute

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, insertErr := tx.Exec(ctx, `
		INSERT INTO weather_records
		  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
		VALUES ('on-horizon', 1, now(), now(), $1, 'processing', now() - $2::interval, $3)`,
		weather.WeatherData{}, intervalArg(lease), uuid.Must(uuid.NewV7())); insertErr != nil {
		t.Fatalf("seeding the on-horizon row: %v", insertErr)
	}

	rows, err := tx.Query(ctx, reapExpiredSQL, intervalArg(lease), 100)
	if err != nil {
		t.Fatalf("running reapExpiredSQL: %v", err)
	}
	reaped, err := collectRecords(rows)
	if err != nil {
		t.Fatalf("collecting reapExpiredSQL rows: %v", err)
	}
	if len(reaped) != 0 {
		got := make([]string, 0, len(reaped))
		for _, r := range reaped {
			got = append(got, r.ID)
		}
		t.Fatalf("reapExpiredSQL reaped %v, want nothing: a row exactly on the lease "+
			"horizon is not yet expired under a STRICT comparison", got)
	}

	var status string
	if err := tx.QueryRow(ctx,
		"SELECT status FROM weather_records WHERE id = 'on-horizon'").Scan(&status); err != nil {
		t.Fatalf("reading on-horizon: %v", err)
	}
	if status != "processing" {
		t.Fatalf("on-horizon row status = %q, want still processing (untouched): the "+
			"lease comparison must be STRICT (claimed_at < cutoff), so a row claimed "+
			"EXACTLY lease ago is not yet expired", status)
	}
}
