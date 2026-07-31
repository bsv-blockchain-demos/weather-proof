# Running the Postgres store tests

The Go store tests in `internal/store/...` need a real PostgreSQL 17 server.
Everything else in the module — including the whole HTTP surface, which runs
against `internal/store/fake` — needs nothing.

## Locally

```sh
make pg-up        # starts the compose `postgres` service on 127.0.0.1:5432
make go-test-pg   # runs ./internal/store/... against it
make pg-psql      # a psql shell inside the container
make pg-down      # stops it (the postgres_data volume survives)
```

There is no host `psql` in this project's environment, which is why `pg-psql`
execs into the container rather than invoking a local client.

With no server running, `go test ./...` still passes: the Postgres tests
SKIP. That is deliberate, and it is also dangerous, which is what the next
section is about.

## Why there are two environment variables

`WEATHER_TEST_POSTGRES_DSN` is the connection string. When it is unset, the
harness calls `t.Skip`.

A skipped test is reported as a pass. So if the variable were ever renamed,
the `env:` key mistyped, or the `services: postgres` block dropped from the
workflow, every store test would skip and CI would go green while executing
nothing. `-count=1` does not help — it defeats a stale cached PASS, not a
vacuous one.

`WEATHER_TEST_REQUIRE_POSTGRES` closes that hole. When it is set and the DSN
is not, the harness calls `t.Fatal` instead of `t.Skip`. CI sets both. You can
see the tripwire work:

```sh
WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1
```

Expected: `WEATHER_TEST_POSTGRES_DSN is unset while WEATHER_TEST_REQUIRE_POSTGRES
is set: the Postgres suite must never skip in CI`.

## Why not a build tag

A file behind `//go:build integration` is invisible to `go vet ./...`,
`go build ./...` and golangci-lint. This was verified by hiding a genuine type
error behind that tag: both vet and build exited 0 and reported nothing. The
runtime env-var skip keeps every file compiled, vetted and linted, and makes
only execution conditional.

## Why some SKIP lines are expected

Two tests in `internal/store/storetest` — `TestHelperPoolGuardRacers` and
`TestHelperPoolWedgedConnection` — are subprocess payloads, not tests in their
own right. Each `t.Skip`s immediately under an ordinary `go test` invocation
and only runs its real body when a sibling test — `TestPoolRefusesConcurrentReuseOfTheSameSchemaName`
or `TestPoolCleansUpAfterAWedgedConnection` — re-invokes `go test -run=...` as
a subprocess with a private env var set. That SKIP is harmless: it is the
payload test declining to run outside its driver's subprocess, not the
Postgres suite silently declining to run at all.

The convention this establishes, and that any later subprocess-helper test
must follow: name it `TestHelper<Something>`, gate its body on a driver-set
env var, and `t.Skip` when that var is unset. The CI skip-guard (below)
tolerates a `--- SKIP` under exactly that name prefix and fails the build on
any other skip. A real test skipping for any other reason — including a
future `TestHelper*`-named test that skips for a reason unrelated to the
subprocess-payload pattern — is exactly what the guard exists to catch, so
don't reach for the prefix as a way to silence it.

## Isolation, and the one rule it imposes

Each test package owns ONE Postgres schema, named in a `storetest.Schema`
value. `storetest.Pool` drops and recreates that schema before every test and
drops it again on cleanup, and pins `search_path` to it as a connection
runtime parameter so every pooled connection lands inside it.

The schema name is a compile-time constant rather than a per-test random
string because a generated name would have to be concatenated into a
`CREATE SCHEMA` statement at runtime, and building SQL text at runtime is
precisely what this codebase forbids everywhere else (gosec G202, and the
Task 17 test that PREPAREs every statement).

**The rule that follows: never call `t.Parallel()` in a test that touches
Postgres.** Tests within a package run sequentially by default, which is what
makes the shared schema safe. Different packages must use different schema
names — `wp_test_harness` for `storetest`, `wp_test_store` for
`internal/store/postgres` — which is what makes parallel package execution
safe.

## The pool-size trap

Any concurrency test must run against a pool whose `MaxConns` is at least the
goroutine count, and must ASSERT that. A pool pinned to one connection
serializes the race, so the test passes while proving nothing. This is a
recorded failure in a sibling repository, where a one-connection fixture left
every `FOR UPDATE SKIP LOCKED` path untested on the production engine.

## CI

`.github/workflows/go.yml` has two jobs. `check` is the pre-existing one (vet,
build, test, lint, the golden gate) plus one added guard step that fails when
any package has no test files. `integration (postgres)` is new: it runs a
`postgres:17-alpine` service container and executes `./internal/store/...`
with `-race` and `-v`, then repeats the claim-concurrency test with `-count=5`.

Three things in that job are assertions rather than conveniences, and all
three exist because a green CI job that executed nothing is the worst possible
outcome:

- both env keys are checked with `test -n` BEFORE `go test` runs, so a
  mistyped `env:` key fails loudly instead of skipping the whole suite;
- the `-v` output is grepped for `--- SKIP`, because `go test` prints those
  lines only under `-v` and a fully skipped package otherwise prints a bare
  `ok` — except a SKIP under a `TestHelper*` name, which is tolerated for the
  reason in "Why some SKIP lines are expected" above; any other skip still
  fails the build;
- the claim-race step greps its own output for `--- PASS:
  TestClaimPendingNeverDoubleClaims`, because `go test -run` with a pattern
  matching nothing exits 0.

The split into two jobs is deliberate: a service-container failure must not be
able to take down vet, lint or the golden gate.
