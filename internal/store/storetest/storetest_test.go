package storetest_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
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

// --- Fix round 1 regression tests ---
//
// The three tests below cover the three findings from the first review of
// this package: an unbounded pool close that could wedge cleanup forever, a
// missing guard against two tests sharing a Schema.Name concurrently, and a
// substring-collision blind spot in Schema.Validate.

// TestSchemaValidateRejectsSubstringCollision is the regression test for the
// minor finding: strings.Contains alone let
// Schema{Name: "a", CreateSQL: "CREATE SCHEMA abc"} validate even though
// CreateSQL actually names an entirely different schema, "abc". Validate is
// the one automated check keeping Schema's three fields honest, so this
// blind spot defeated its whole purpose.
func TestSchemaValidateRejectsSubstringCollision(t *testing.T) {
	collisions := []storetest.Schema{
		{Name: "a", CreateSQL: "CREATE SCHEMA abc", DropSQL: "DROP SCHEMA a CASCADE"},
		{Name: "a", CreateSQL: "CREATE SCHEMA a", DropSQL: "DROP SCHEMA abc CASCADE"},
		{
			Name:      "wp_test",
			CreateSQL: "CREATE SCHEMA IF NOT EXISTS wp_test_harness",
			DropSQL:   "DROP SCHEMA IF EXISTS wp_test CASCADE",
		},
	}
	for i, s := range collisions {
		if err := s.Validate(); err == nil {
			t.Errorf("case %d: Validate() = nil, want an error (Name is only a substring of a different identifier)", i)
		}
	}

	// A genuine whole-identifier match, with punctuation on both sides, must
	// still pass — the tightened check must not simply refuse everything.
	ok := storetest.Schema{
		Name:      "wp_ok",
		CreateSQL: "CREATE SCHEMA IF NOT EXISTS wp_ok",
		DropSQL:   "DROP SCHEMA IF EXISTS wp_ok CASCADE",
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("a genuine whole-identifier match failed Validate: %v", err)
	}
}

// guardHelperEnv gates TestHelperPoolGuardRacers so its real body runs only
// when TestPoolRefusesConcurrentReuseOfTheSameSchemaName re-invokes `go test`
// as a subprocess selecting exactly that test.
//
// Two tests sharing one Schema.Name are SUPPOSED to have exactly one fail —
// and testing.T has no supported way to treat a subtest's failure as an
// expected, non-propagating outcome: (*testing.common).Fail walks up through
// every c.parent and sets it failed too, unconditionally, all the way to the
// top-level test — confirmed directly against the stdlib source, and
// separately confirmed by an earlier version of this test, which reported
// --- FAIL on the wrapping test even though its OWN pass/fail assertion
// never fired, solely because one nested racer subtest failed as designed.
// Running the racers inside a subprocess is what lets the real, permanently
// green top-level test observe "which one failed, and with what message" as
// plain captured output, while still exercising the actual in-process guard:
// both racer goroutines below share THAT SUBPROCESS's own activeSchemas map,
// which is the thing that needs proving.
const guardHelperEnv = "STORETEST_GUARD_HELPER"

// guardHelperSchema is the disposable schema TestHelperPoolGuardRacers races
// against. Dedicated to this one test; nothing else in this package touches
// it.
var guardHelperSchema = storetest.Schema{
	Name:      "wp_test_harness_parallel_guard",
	CreateSQL: "CREATE SCHEMA IF NOT EXISTS wp_test_harness_parallel_guard",
	DropSQL:   "DROP SCHEMA IF EXISTS wp_test_harness_parallel_guard CASCADE",
}

// TestHelperPoolGuardRacers is not one of this package's real tests: absent
// guardHelperEnv it skips immediately, so an ordinary `go test ./...` run
// only ever sees a harmless SKIP here. Its own pass/fail, when it does run
// for real, is irrelevant to and invisible from the process that spawned
// it — only its captured stdout is.
func TestHelperPoolGuardRacers(t *testing.T) {
	if os.Getenv(guardHelperEnv) == "" {
		t.Skip("only runs as a subprocess of TestPoolRefusesConcurrentReuseOfTheSameSchemaName")
	}

	const racers = 2
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			t.Run(fmt.Sprintf("racer-%d", i), func(t *testing.T) {
				storetest.Pool(t, guardHelperSchema, 2)
			})
		}(i)
	}
	close(start)
	wg.Wait()
}

// TestPoolRefusesConcurrentReuseOfTheSameSchemaName is the regression test
// for the important finding: two tests sharing a Schema.Name under
// t.Parallel() used to corrupt shared state instead of failing cleanly,
// reproduced as `ERROR: no schema has been selected to create in
// (SQLSTATE 3F000)` when one test's entry DROP SCHEMA ... CASCADE yanked the
// schema out from under the other's already-pinned connection.
//
// It re-invokes `go test` (every argument a compile-time constant, so this
// does not trip gosec G204) to run TestHelperPoolGuardRacers in a
// subprocess, then asserts on ITS captured output: exactly one racer must
// pass and exactly one must fail, the failing one with this package's own
// clear refusal message, and never with a raw Postgres SQLSTATE.
func TestPoolRefusesConcurrentReuseOfTheSameSchemaName(t *testing.T) {
	storetest.RequireDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "test", "-run=^TestHelperPoolGuardRacers$", "-v", ".")
	cmd.Env = append(os.Environ(), guardHelperEnv+"=1")
	out, _ := cmd.CombinedOutput() // a non-nil error here is EXPECTED: one racer fails on purpose.
	output := string(out)
	if ctx.Err() != nil {
		t.Fatalf("guard-racer subprocess did not complete within its budget — output:\n%s", output)
	}

	passCount := strings.Count(output, "--- PASS: TestHelperPoolGuardRacers/racer-")
	failCount := strings.Count(output, "--- FAIL: TestHelperPoolGuardRacers/racer-")
	if passCount != 1 || failCount != 1 {
		t.Fatalf("subprocess racers: got %d PASS and %d FAIL, want exactly 1 and 1 — output:\n%s",
			passCount, failCount, output)
	}
	if !strings.Contains(output, "already in use by another concurrently running test in this process") {
		t.Fatalf("the failing racer did not report the expected concurrent-reuse refusal — output:\n%s", output)
	}
	if strings.Contains(output, "3F000") {
		t.Fatalf("a racer hit the raw Postgres error the guard exists to prevent — output:\n%s", output)
	}
}

// wedgedHelperEnv gates TestHelperPoolWedgedConnection the same way
// guardHelperEnv gates TestHelperPoolGuardRacers, and for the same reason:
// its failure is deliberate and must not propagate into a test that should
// stay green.
const wedgedHelperEnv = "STORETEST_WEDGED_HELPER"

// wedgedHelperSchema is the disposable schema TestHelperPoolWedgedConnection
// uses. Dedicated to this one test.
var wedgedHelperSchema = storetest.Schema{
	Name:      "wp_test_harness_wedged",
	CreateSQL: "CREATE SCHEMA IF NOT EXISTS wp_test_harness_wedged",
	DropSQL:   "DROP SCHEMA IF EXISTS wp_test_harness_wedged CASCADE",
}

// TestHelperPoolWedgedConnection is not one of this package's real tests:
// absent wedgedHelperEnv it skips immediately. When it does run for real (as
// a subprocess), it deliberately fails while holding an open transaction —
// pool.Begin then t.Fatal with no Rollback — the exact shape that used to
// wedge pool.Close forever.
func TestHelperPoolWedgedConnection(t *testing.T) {
	if os.Getenv(wedgedHelperEnv) == "" {
		t.Skip("only runs as a subprocess of TestPoolCleansUpAfterAWedgedConnection")
	}
	pool := storetest.Pool(t, wedgedHelperSchema, 4)
	if _, err := pool.Begin(context.Background()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Deliberately no Commit/Rollback: this is the exact shape that used to
	// wedge pool.Close forever.
	t.Fatal("simulated downstream failure: a transaction was left open and never rolled back")
}

// TestPoolCleansUpAfterAWedgedConnection is the regression test for the
// critical finding: t.Cleanup(pool.Close) used to register the raw,
// unbounded pgxpool.Pool.Close, and puddle's Close "blocks until all
// resources are returned to pool and destroyed" — so a downstream test that
// fails while holding an acquired connection wedged the whole process's
// cleanup forever, with the real failure message buried in a goroutine dump
// once go test's own -timeout finally killed it.
//
// It re-invokes `go test` (constant arguments only, so gosec G204 does not
// fire) to run TestHelperPoolWedgedConnection in a subprocess — the same
// technique as the guard-racer test above, and for the same reason: that
// helper's failure is deliberate and must not propagate here. It then
// asserts, from OUTSIDE that subprocess, that it completed within a small
// bound and that the schema was actually dropped — not merely that nothing
// hung forever.
func TestPoolCleansUpAfterAWedgedConnection(t *testing.T) {
	dsn := storetest.RequireDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "test", "-run=^TestHelperPoolWedgedConnection$", "-v", ".")
	cmd.Env = append(os.Environ(), wedgedHelperEnv+"=1")

	start := time.Now()
	out, _ := cmd.CombinedOutput() // a non-nil error here is EXPECTED: the helper fails on purpose.
	elapsed := time.Since(start)
	output := string(out)

	if ctx.Err() != nil {
		t.Fatalf("cleanup after a wedged connection did not complete within its budget: pool.Close is hanging "+
			"again — see storetest.go's use of postgres.ClosePool. subprocess output:\n%s", output)
	}
	if elapsed > 15*time.Second {
		t.Errorf("cleanup after a wedged connection took %s, expected only a few seconds now that pool close "+
			"is bounded", elapsed)
	}
	if !strings.Contains(output, "simulated downstream failure") {
		t.Fatalf("helper subprocess did not run as expected — output:\n%s", output)
	}

	// The helper subprocess's own t.Cleanup callbacks, including the schema
	// drop, must have already run before it exited. Confirm the schema is
	// really gone rather than trusting that silently.
	verifyPool, err := postgres.NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatalf("verify: NewPool: %v", err)
	}
	defer func() {
		if closeErr := postgres.ClosePool(verifyPool, 5*time.Second); closeErr != nil {
			t.Errorf("verify: pool close: %v", closeErr)
		}
	}()

	var exists bool
	err = verifyPool.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", wedgedHelperSchema.Name).Scan(&exists)
	if err != nil {
		t.Fatalf("verify: querying pg_namespace: %v", err)
	}
	if exists {
		t.Fatalf("schema %q still exists after the wedged subprocess's cleanup ran — the schema drop must "+
			"still run even when the pool-close budget is exceeded", wedgedHelperSchema.Name)
	}
}
