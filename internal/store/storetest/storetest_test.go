package storetest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

// harnessSchema is this package's private Postgres schema. It is a package
// constant rather than a per-test random name on purpose: a generated name
// would have to be concatenated into a CREATE SCHEMA statement at runtime,
// and building SQL text at runtime is exactly what this codebase forbids
// (gosec G202) and what Task 17 mechanically disproves. The price is that
// tests in this package must never call t.Parallel().
var harnessSchema = storetest.Schema{
	Name:      "wp_test_harness",
	CreateSQL: "CREATE SCHEMA IF NOT EXISTS wp_test_harness",
	DropSQL:   "DROP SCHEMA IF EXISTS wp_test_harness CASCADE",
}

func TestSchemaValidateCatchesAMismatch(t *testing.T) {
	if err := harnessSchema.Validate(); err != nil {
		t.Fatalf("the real schema failed Validate: %v", err)
	}

	bad := []storetest.Schema{
		{Name: "", CreateSQL: "CREATE SCHEMA x", DropSQL: "DROP SCHEMA x"},
		{Name: "a", CreateSQL: "CREATE SCHEMA b", DropSQL: "DROP SCHEMA a CASCADE"},
		{Name: "a", CreateSQL: "CREATE SCHEMA a", DropSQL: "DROP SCHEMA b CASCADE"},
		{Name: "a", CreateSQL: "", DropSQL: "DROP SCHEMA a CASCADE"},
		{Name: "a", CreateSQL: "CREATE SCHEMA a", DropSQL: ""},
	}
	for i, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("case %d: Validate() = nil, want an error", i)
		}
	}
}

// TestPoolPinsTheSchemaOnEveryConnection is the harness's own contract test.
// It also proves the tripwire wiring: with no DSN it skips, and CI sets
// WEATHER_TEST_REQUIRE_POSTGRES so a missing DSN is a t.Fatal instead.
func TestPoolPinsTheSchemaOnEveryConnection(t *testing.T) {
	pool := storetest.Pool(t, harnessSchema, 8)
	ctx := context.Background()

	if got := pool.Config().MaxConns; got != 8 {
		t.Fatalf("MaxConns = %d, want 8", got)
	}

	// Ten round trips, which with MinConns 2 and MaxConns 8 will touch more
	// than one physical connection.
	for i := range 10 {
		var schema string
		if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
			t.Fatalf("round trip %d: %v", i, err)
		}
		if schema != harnessSchema.Name {
			t.Fatalf("round trip %d: current_schema() = %q, want %q", i, schema, harnessSchema.Name)
		}
	}

	// A table created here must land inside the private schema.
	if _, err := pool.Exec(ctx, "CREATE TABLE probe (i int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	var nspname string
	err := pool.QueryRow(ctx, `
		SELECT n.nspname FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname = 'probe'`).Scan(&nspname)
	if err != nil {
		t.Fatalf("locating probe: %v", err)
	}
	if nspname != harnessSchema.Name {
		t.Fatalf("probe landed in schema %q, want %q", nspname, harnessSchema.Name)
	}
}

// TestPoolStartsFromAnEmptySchema must hold no matter what the previous test
// left behind. It is the guard on Lesson 1: an assertion that only
// discriminates when the file is run with -run is not a test.
func TestPoolStartsFromAnEmptySchema(t *testing.T) {
	pool := storetest.Pool(t, harnessSchema, 4)
	ctx := context.Background()

	var tables int
	err := pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_tables WHERE schemaname = $1", harnessSchema.Name).Scan(&tables)
	if err != nil {
		t.Fatalf("counting tables: %v", err)
	}
	if tables != 0 {
		t.Fatalf("schema %q has %d tables at test start, want 0", harnessSchema.Name, tables)
	}
}

func TestEnvVarNamesAreStable(t *testing.T) {
	if storetest.DSNEnv != "WEATHER_TEST_POSTGRES_DSN" {
		t.Errorf("DSNEnv = %q", storetest.DSNEnv)
	}
	if storetest.RequireEnv != "WEATHER_TEST_REQUIRE_POSTGRES" {
		t.Errorf("RequireEnv = %q", storetest.RequireEnv)
	}
	// The workflow and the Makefile both hard-code these names. If either
	// changes, every Postgres test skips and the job goes green while testing
	// nothing, which is what RequireEnv exists to prevent.
	if strings.ToUpper(storetest.DSNEnv) != storetest.DSNEnv {
		t.Error("DSNEnv must be upper case")
	}
}
