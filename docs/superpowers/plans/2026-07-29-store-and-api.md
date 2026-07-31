# Store and API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Postgres-backed persistence layer (`internal/store` + `internal/store/postgres`) to the weather-proof Go module, with an atomic `FOR UPDATE SKIP LOCKED` record claim, a lease reaper, every read query the HTTP API will need, an in-memory fake for datastore-free testing, ONE conformance suite that holds the fake and the SQL to the same contract, the redacting `config.Secret` both later plans need, and a CI job in which those Postgres tests genuinely run and cannot silently skip.

**Architecture:** `internal/store` holds pure Go types (`Record`, `Station`, `Stats`, `Snapshot`, `Status`) and four interfaces (`RecordStore`, `StationStore`, `DepositStore`, `PreflightStore`) aggregated into a `Store` struct of interfaces — they cannot be embedded into one interface because `RecordStore` and `StationStore` both declare `List` and `Get` with different signatures. `internal/store/postgres` is the only package that imports pgx; every statement it runs is a compile-time constant with numbered bind parameters, and `internal/store` never imports it, which is what keeps the HTTP layer (a later plan) testable with `internal/store/fake` and no database. Test isolation is one dedicated Postgres schema per test *package*, dropped and recreated before every test, with the migration re-applied each time.

**Tech Stack:** Go 1.26.3, `github.com/jackc/pgx/v5` v5.10.0 (`pgxpool`, `pgx.CollectRows` + `pgx.RowToStructByName`, `pgx.BeginFunc`), `github.com/google/uuid` v1.6.0 (`NewV7` — Postgres 17 has no `uuidv7()`), PostgreSQL 17 (`postgres:17-alpine`), GitHub Actions service containers, golangci-lint v2.12.2.

## Global Constraints

- **No TypeScript is deleted or edited.** `src/`, `tests/`, `frontend/`, `package.json`, `tsconfig.json`, `jest.config.js` and `docker-compose.yaml`'s `mongodb`/`app`/`frontend`/`setup` services stay exactly as they are. This plan is purely additive: it adds Go packages and one new compose service. The `app` service is the live TypeScript backend and still requires `MONGO_URI`; removing `mongodb` would break it.
- **`.github/workflows/go.yml`'s existing `check` job must stay green.** Postgres work goes in a NEW job so a service-container failure can never take down vet, build, test, lint or the golden gate. Exactly one step is ADDED to `check` (the vacuous-green guard); no existing step is modified.
- **The four status literals are exactly `pending`, `processing`, `completed`, `failed`** — lowercase, no others. They are the `text` values in the `status` column, the values of `store.Status`, and the four keys of the frontend's `statusStyles` map. Never `Pending`, never `PENDING`, never an int enum.
- **The four chain-status literals are exactly `arc-accepted`, `unmined`, `mined`, `aborted`.** This is a separate column and a separate Go type; the SPA never sees it.
- **Zero `//nolint` directives.** `nolintlint` runs with `allow-unused: false` and `require-explanation: true`, so an unnecessary `//nolint` is itself a lint failure. Design around every lint rule instead of suppressing it.
- **`gosec` G304 fires on `os.ReadFile` with a non-constant path.** Never read a file at a computed path. The migration script is loaded with `//go:embed`, which is a compile-time read and has no G304 exposure.
- **`gosec` G404 rejects `math/rand`.** Never import it. This plan needs no randomness at all: schema names are compile-time constants, and record ids come from `uuid.NewV7()`.
- **`gosec` G101 fires on a credential-shaped literal assigned to a named constant or variable, INCLUDING in a test file.** Measured with this repository's exact config: `const probePassword = "p@ss:w/rd?#&secret"` reports `G101: Potential hardcoded credentials`, and `const dsn = "postgres://u:pw@localhost:5432/d?..."` reports `Password in URL`. An inline literal passed straight to a function call does not fire, because the rule inspects assignments and const specs. So a test that needs a credential-shaped value builds it from parts at runtime (`var probePassword = "p@ss" + ":w/rd" + "?#&" + "secret"`, which is a binary expression rather than a basic literal) and builds a connection string with `DSNParts.DSN()` rather than writing `u:pw@` into a constant. Do not reach for `//nolint`; there are none in this repository.
- **`gofmt` is an enabled FORMATTER, not just a linter, and `run.tests: true` means test files count.** Every block in this plan is written gofmt-clean, but a hand-edit can undo that in one keystroke: an aligned `var (...)` block whose longest line changes, or a doc comment containing a space-indented code block (Go 1.19+ gofmt rewrites those to tabs). **Every task's gate therefore begins with `gofmt -w ./internal && test -z "$(gofmt -l ./internal)"`.** Note also that `golangci-lint` truncates at 3 findings per rule by default, so every gate in this plan passes `--max-same-issues=0`: without it a fourth identical formatting failure is invisible.
- **`gosec` G201/G202 fire on `fmt.Sprintf`-built and `+`-concatenated SQL.** Every statement in `internal/store/postgres` is a package-level `const` (or a `const` built by concatenating other `const`s at package level, which is a compile-time constant expression and not a runtime concatenation). No statement text is ever assembled at runtime. Where an identifier genuinely cannot be a bind parameter (test schema names), the caller supplies the WHOLE statement as a constant, never the identifier.
- **MEASURED, and it is worse than it looks: gosec's SQL rules do not protect pgx code AT ALL.** Running golangci-lint v2.12.2 with this repository's exact `.golangci.json` over a probe: `db.ExecContext(ctx, "SELECT id FROM t ORDER BY "+frag)` on a `*sql.DB` fires **G202**, and `fmt.Sprintf` into a `*sql.DB` call fires **G201** — but the identical concatenation into `pgxpool.Pool.Exec` produces **0 issues**. G201/G202 recognize `database/sql` sinks only, and this codebase never uses `database/sql`. A green lint is therefore no evidence whatsoever that dynamic SQL is absent. Two mechanical guards replace it, both in Task 17: every statement the package declares is PREPAREd against a real server (which only succeeds for static, parameterized text), and the package's own `.go` sources are scanned via `//go:embed *.go` for SQL-construction shapes.
- **Concatenating SQL constants at package level is allowed and is the intended style.** `const claimSQL = "UPDATE … RETURNING " + recordColumnsAliased` is a compile-time constant expression, not a runtime concatenation, and it was verified to produce 0 lint findings. Runtime concatenation of a non-constant is forbidden regardless of what the linter does or does not say.
- **`govet` runs with `shadow` enabled.** Never redeclare a live `err`, including inside a `defer` closure or an `if err := …` that shadows an outer `err`. This is the single easiest rule in the plan to break by reflex, because `if err := f(); err != nil` is idiomatic Go everywhere else. **The mechanical rule: once a function has an `err` that is still read later, every inner error binding is NAMED AFTER ITS OPERATION** — `execErr`, `scanErr`, `queryErr`, `rowsErr`, `dropErr`, `completeErr`, `upsertErr` — or the outer `err` is reused by plain assignment (`err = …`). Every test in this plan follows that convention; a worker adding an assertion must follow it too.
- **`prealloc` runs with `range-loops: true`.** Any slice appended to inside a `range` loop must be created with `make([]T, 0, n)` first.
- **`misspell` runs with `locale: US` and `run.tests: true`, so test files are linted — including TEST NAMES.** Write `marshaled`, `serialized`, `behavior`, `normalized`, `synthesized`, `canceled`, `honors`. Never `marshalled`, `behaviour`, `normalise`, `cancelled`, `honours`. The repository's `misspell.ignore-rules` list contains `honour` but NOT `honours`: misspell keys inflections separately, so `Honours` inside a `func TestXHonoursY` name is still a finding and still breaks the mandatory `0 issues` gate.
- **`exhaustive` runs with `default-signifies-exhaustive: false`.** A `switch` over `store.Status` or `store.ChainStatus` must name all four members; a bare `default` does not satisfy it.
- **`errorlint` is enabled.** Classify errors with `errors.Is` / `errors.As`, never by matching `err.Error()` text.
- **`bodyclose`, `noctx`, `rowserrcheck`, `sqlclosecheck`, `errchkjson`, `musttag`, `nosprintfhostport`, `unparam`, `unconvert`, `recvcheck`, `embeddedstructfieldcheck`, `gosmopolitan` are all enabled.** In particular `noctx` forbids any context-free query call, so every store method takes and passes a `context.Context`.
- **`golangci-lint run` SILENTLY IGNORES unknown config keys and reports 0 issues, while `golangci-lint config verify` REJECTS them — and `golangci-lint-action` runs `config verify` first.** If any task touches `.golangci.json`, that task MUST run `golangci-lint config verify` before committing. No task in this plan needs to touch it.
- **`golangci-lint-action` must stay at v7 or newer** with `version: v2.12.2`. A v6 action cannot run a v2 linter.
- **`QueryExecModeSimpleProtocol` is forbidden, and it is settable from outside the binary.** pgx reads `default_query_exec_mode` from the connection-string runtime params, and `simple_protocol` switches every query to client-side text interpolation. `postgres.PoolConfig` asserts against it and fails startup. Never pass a `pgx.QueryExecMode` as the first query argument either.
- **A `*pgconn.PgError` must never reach an HTTP response body.** Its `Error()` is `severity: message (SQLSTATE code)` and the struct carries `Detail`, `Hint`, `ConstraintName`, `ColumnName` and `TableName`. `internal/store/postgres` translates every driver error to `store.ErrNotFound`, `store.ErrConflict` or `postgres.ErrOperation` wrapped with the 5-character SQLSTATE and nothing else.
- **Every ordered record query carries the `id` tiebreaker.** `created_at` defaults to `now()`, which is the TRANSACTION timestamp, so all rows written by one poll share one `created_at` to the microsecond (measured: 40 rows in one transaction → `count(DISTINCT created_at) = 1`). `ORDER BY created_at DESC` alone is a non-total order and `LIMIT`/`OFFSET` over it is unspecified; at production shape two legitimate plans returned 19 of 20 different rows at the same offset. Always `created_at DESC, id DESC` (or `created_at ASC, id ASC` in the claim).
- **`RETURNING *` and `SELECT *` are forbidden.** `pgx.RowToStructByName` — and `RowToStructByNameLax`, verified identically — treat a column with no matching struct field as a hard runtime error. `RETURNING *` couples every query to the table's full column list forever, and the next migration breaks all of them at runtime with no compile-time signal. Write explicit column lists.
- **PostgreSQL 17 has no `uuidv7()`** (verified: `ERROR: function uuidv7() does not exist (SQLSTATE 42883)`). Record ids are generated in Go with `uuid.NewV7()`. `gen_random_uuid()` IS core in PG 17 and needs no `pgcrypto`, but this design does not use it — see the claim-ref constraint below.
- **`ClaimPending` takes the claim ref as a parameter.** `COALESCE(claim_ref, gen_random_uuid())` evaluates the VOLATILE `gen_random_uuid()` once per updated row (measured: a 21-row claim produced 21 distinct refs), which destroys the batch label that the later adopt design uses as its idempotency key. One `uuid.NewV7()` per call, passed as `$2`.
- **Never call `t.Parallel()` in any test that touches Postgres.** All Postgres tests in a package share one schema name and each test drops and recreates it; a parallel test would delete a sibling's rows mid-run. Per-test unique schema names were considered and rejected: generating them requires building `CREATE SCHEMA <ident>` at runtime, which is exactly the SQL-string construction this plan forbids everywhere else.
- **Postgres-dependent tests must FAIL LOUDLY in CI, never skip.** `storetest.RequireDSN` skips when `WEATHER_TEST_POSTGRES_DSN` is unset — unless `WEATHER_TEST_REQUIRE_POSTGRES` is set, in which case it calls `t.Fatal`. CI sets both, so a renamed variable or a dropped service block is a red build rather than a silent all-skip pass.
- **`go test ./...` exits 0 for a package with no test files, and `-count=1` does not help.** Every package this plan creates ships with test files, and the `check` job gains a guard step that fails when any package has neither `TestGoFiles` nor `XTestGoFiles`.
- **No test may depend on test ordering or on state another test left behind.** Every Postgres test starts from `storetest.Fresh`, which drops the schema, recreates it, re-applies the migration and hands back an empty database. Every assertion must hold when the test is run alone with `-run`, when run with its whole package, and when run five times with `-count=5`.
- **The concurrency test must assert INVARIANTS, not schedules.** Assert "no id was returned twice" and "the count claimed is ≤ the count seeded", never "worker 3 won row 2". The winning split is scheduler-dependent.
- **`pgxpool.MaxConns` must be ≥ the goroutine count in any concurrency test, and the test must assert that.** A pool pinned to one connection serializes the race and makes the test silently vacuous. This is a recorded house failure: go-wallet-toolbox's `FOR UPDATE SKIP LOCKED` paths went untested for exactly this reason.
- **Client-IP trust rule (project-wide; implemented by the API plan, recorded here because it is a hard requirement and its omission was a defect in an earlier spec draft):** no forwarding header may be read until the immediate TCP peer is proven to be in `TRUSTED_PROXY_CIDRS`. Untrusted peer ⇒ key on the peer address and consult NO header. Trusted peer ⇒ `CF-Connecting-IP` first, else `X-Forwarded-For` walked from the RIGHT taking the first hop NOT in the trusted set. Always call `.Unmap()` before a `netip.Prefix.Contains` check (`10.0.0.0/8` does NOT contain `::ffff:10.1.2.3` without it). Canonicalize IPv6 keys to a `/64`. Never use `httprate.KeyByRealIP` (trusts `X-Forwarded-For[0]` with no peer check) or `httprate.CanonicalizeIP` (returns `"::"` for any 4-in-6 address, collapsing every mapped IPv4 client into one bucket). An empty `TRUSTED_PROXY_CIDRS` is legal but must emit exactly one boot WARN; a malformed entry is a startup error.
- **Rate-limiter scopes must be DISJOINT (project-wide; implemented by the API plan, recorded here for the same reason as the client-IP rule — it was a defect in an earlier draft and B2 does not exist yet).** `/api` mounted before `/api/events` means one SSE request decrements BOTH buckets, and `EventSource` auto-reconnects, so the first 429 becomes a retry loop that cannot drain. The SSE limiter is its own sub-router and an `/api/events` request must consume EXACTLY ONE bucket, with a test asserting that the general bucket's remaining count is unchanged by an SSE request.
- **`/api/health`, `/api/ready` and `/api/ops` are registered BEFORE the limiter middleware (project-wide; implemented by the API plan).** A liveness probe that can be rate-limited turns a traffic spike into a pod restart.
- **The limiter's numbers are part of the contract, not a tuning detail (project-wide; implemented by the API plan).** 600 requests/minute general with burst 120; 60/minute on `POST /api/verify`; 30 new SSE streams/minute, which must NOT decrement the general bucket; 12 concurrent streams per IP; a global stream semaphore of 500 answering 503 beyond it; `PROOF_RATE_LIMIT_PER_MIN` default 60 and `>= 1` (zero means unlimited on an unauthenticated outbound proxy); and `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset` plus `Retry-After` on every 429. **The key map is BOUNDED at 10 000 entries with eviction**, because an untrusted peer can otherwise mint keys until the process is out of memory — with a test asserting that N spoofed keys arriving from an untrusted peer produce exactly ONE map entry.
- **A NUL byte is not a legal Postgres `text` parameter value, so user-controlled strings are validated before they reach a bind (project-wide; the store half is Tasks 1, 12 and 13).** Verified against `postgres:17-alpine` with pgx v5.10.0: binding `"\x00nul"` to `SELECT $1::text` fails with `invalid byte sequence for encoding "UTF8": 0x00 (SQLSTATE 22021)`. The extended query protocol prevents INJECTION but does not make the value legal, so `?search=%00` and `/api/weather/%00` are a 500 unless something rejects them — Go's `r.URL.Query().Get` and path decoding both yield a literal NUL. `store.ValidText` (Task 1) is the single definition of "bindable text"; the postgres store applies it so that a NUL id is a MISS and a NUL search matches nothing (Tasks 12 and 13), and B2 applies it in its parse/clamp helpers so the same input is a 400 rather than an empty 200. Two layers on purpose: the API layer gives the right status code, and the store layer is total so no future caller can produce a 500.
- **`POST /api/verify` is an unauthenticated write path and stays bounded the same way (project-wide; the store half is Task 15):** the caller chooses only WHICH existing rows are refreshed, never the VALUE. `SetBlockHeights` takes a height that is only ever assigned from a decoded WhatsOnChain `blockheight` field, the API DTO has no height field at all, and both table updates happen in ONE transaction.
- **`go.mod` and `go.sum` are committed in the same commit as the code that needs them.** The `check` job runs `go mod tidy` followed by `git diff --exit-code go.mod go.sum`.

## Scope note

**Yes — the original Plan B scope (Postgres store + read API + security parity) is too large for one plan, and every research extract that examined it independently reached the same conclusion.** Counted honestly it is 40-45 TDD tasks and several thousand lines: roughly 15-18 for persistence, 22-26 for the HTTP surface, and 16-20 for security parity (overlapping the HTTP surface). A 40-task plan is precisely the mechanism that produces a multi-thousand-line PR in which the security-parity checklist gets skimmed — which the design spec already records as a defect in an earlier draft.

**Proposed split (three plans):**

| Plan | Scope | Depends on | Est. tasks |
|---|---|---|---|
| **B1 — this document** | `internal/config/secret.go` (the redacting `Secret` type only — the rest of the package is B2's first task); `internal/store` types + interfaces + `store.ValidText` + in-memory fake; `internal/store/postgres` (pool, DSN, migrations, atomic claim, reaper, all list/get/stats/snapshot/station/block-height queries, deposits, preflight); the shared fake-vs-Postgres conformance suite; the CI Postgres job, the vacuous-green guard, govulncheck, digest-pinned actions and the CodeQL `go` language; the store-layer half of security parity. | Plan A only | **19** |
| **B2 — `docs/superpowers/plans/2026-07-30-read-api.md`** | **`internal/config` FIRST** (it is assigned here because no other plan owns it and B2's limiter cannot be built without it): env parse plus a `Validate()` that returns a startup error listing every missing secret, so there is NO compiled-in default for `SERVER_PRIVATE_KEY` or `POSTGRES_PASSWORD` and the pod CrashLoopBackOffs rather than running on the publicly known key; `TRUSTED_PROXY_CIDRS` parsed as CIDRs with a malformed entry a startup error and an empty list emitting exactly one boot WARN; `PROOF_RATE_LIMIT_PER_MIN >= 1`; and the `Secret` type from B1 wrapping `SERVER_PRIVATE_KEY` and `TEMPEST_API_KEY` as well as the Postgres password. Then `internal/api`: the `isoMillis` time type and the nullable-pointer rule, DTOs embedding `internal/weather.WeatherData`, the clamp/parse helpers (which is where `store.ValidText` becomes a 400), the four list/detail handlers with golden files, routing including the bare `?` and the `{$}` trailing-slash pair, request-id + panic-recovery + opaque-error middleware, security-headers middleware, the write-deadline middleware, `internal/ratelimit` (the trusted-CIDR client-IP resolver AND the 10 000-entry bounded-key counter — one task, never two), the per-scope limiter wiring with scope-disjoint buckets and health/ready/ops registered before the limiter, the SSE hub and handler, `/api/health`, `/api/ready`, the `/api/ops` shape, and the two `http.Server`s with their asymmetric timeouts. Runs entirely against `internal/store/fake` — no database, so it lands on the existing `check` job. | B1's `internal/store` package and `internal/config`'s `Secret` (types + interfaces + fake). It must NOT import `internal/store/postgres`. | ~22 |
| **B3 — `docs/superpowers/plans/2026-07-31-verify-and-proof.md`** | `internal/verify` (the `POST /api/verify` body gate, the ≤100 cap, `MaxBytesReader`→413, `errgroup.SetLimit(8)`, the hardened WhatsOnChain client behind a one-method `LookupBlockHeight` interface, and the two-table persistence call into B1's `SetBlockHeights`); `internal/proof` (the 64-hex gate, the store gate via B1's `TxIDExists`, the bounded LRU with 10 min positive / 30 s negative TTL, `singleflight`, and BRC-95 `beef.AtomicBytes` framing); plus the §6.0 security-parity acceptance checklist as its gate. | B1 + B2 | ~8 |

B2 can start as soon as `internal/store/store.go` and `internal/store/interfaces.go` compile — the dependency is types-only. **Task 1 and Task 2 of this plan therefore freeze a contract that B2 develops against, and they must be reviewed with that in mind: a signature change after B2 starts costs rework in both plans.** B3 is genuinely last, because it is the only part that needs an egress-interface design decision.

**What B1 owns of security parity** (the rest is B2/B3): every statement is static parameterized SQL, mechanically proven by PREPAREing all of them (Task 17); the `default_query_exec_mode=simple_protocol` startup assertion (Task 3, and re-asserted live in Task 17); `*pgconn.PgError` never escaping the package — including on the cancellation branch, which is the one place a DSN fragment could otherwise ride out inside a pgx connect error (Task 7 defines the classifier, Task 17 gates it with a canceled context); the four-literal status allowlist as a typed `Status` with `Valid()` (Task 1); `store.ValidText` making a NUL byte a miss rather than a 22021, which is what makes the next claim true (Tasks 1, 12, 13); `websearch_to_tsquery` proven non-raising over 25 adversarial inputs, two of which carry a NUL byte, so the search box cannot 500 (Task 13); the `Secret` type whose `String()`, `LogValue()` and `MarshalJSON()` all redact, defined once in `internal/config` so `SERVER_PRIVATE_KEY` and `TEMPEST_API_KEY` can reuse it rather than growing a second copy that is missing a method, and a DSN built with `net/url` rather than string concatenation (Task 3); `SetBlockHeights` structurally unable to take a caller-supplied height AND unable to raise a station's displayed height from a non-completed row (Task 15); and static analysis of the Go code itself, by adding `go` to the repository's CodeQL default setup, which today lists only `actions`/`javascript`/`typescript` (Task 18).

---

## File Structure

| File | Created / Modified | Single responsibility |
|---|---|---|
| `go.mod`, `go.sum` | Modified (Tasks 1, 3) | Adds `github.com/google/uuid` v1.6.0 and `github.com/jackc/pgx/v5` v5.10.0 as direct requirements, plus their indirects. |
| `internal/store/store.go` | Created (Task 1) | The datastore-agnostic domain types, the three sentinel errors, and `ValidText`. No imports of pgx, no SQL, no HTTP. |
| `internal/store/store_test.go` | Created (Task 1) | Pins the exact wire literals of `Status`/`ChainStatus`, the allowlist behavior of `Valid()`, `ValidText`'s NUL rejection, and `Stats.TotalDataPoints`. |
| `internal/store/interfaces.go` | Created (Task 2) | The four store interfaces, the `Store` struct of interfaces, and `Pinger`. Nothing else. |
| `internal/store/fake/fake.go` | Created (Task 2) | An in-memory `Store` for datastore-free tests, with an injectable clock and injectable failures. Carries the doc comment stating it is NOT evidence for the claim query. |
| `internal/store/fake/fake_test.go` | Created (Task 2) | Compile-time conformance to all four interfaces plus behavioral tests of the fake's own contract. |
| `internal/config/secret.go` | Created (Task 3) | The `Secret` type, whose `String()`, `LogValue()` and `MarshalJSON()` all redact and whose `Reveal()` is the single escape hatch. A LEAF package with no dependencies, so `internal/config` (B2), `internal/store/postgres` and any future caller share ONE definition — the spec puts it here, and a hand-copied second copy is how the copy ends up missing `MarshalJSON`. |
| `internal/config/secret_test.go` | Created (Task 3) | Proves the value survives no fmt verb, no slog attribute and no `json.Marshal` — as a field, as a pointer field and as an embedded field. |
| `internal/store/postgres/dsn.go` | Created (Task 3) | `DSNParts.DSN()`: assembles a connection string with `net/url` so a password is escaped, never concatenated, and never logged. Its `Password` field is a `config.Secret`; this package declares no `Secret` of its own and no alias to it. |
| `internal/store/postgres/pool.go` | Created (Task 3) | `PoolConfig` (parse, assert against simple protocol, tune), `NewPool`, `ClosePool` with a caller-enforced budget. |
| `internal/store/postgres/pool_test.go` | Created (Task 3) | Database-free tests of DSN escaping, the simple-protocol rejection and the close budget. |
| `internal/store/storetest/storetest.go` | Created (Task 4), Modified (Task 6) | The Postgres test harness: `RequireDSN` with the fail-in-CI tripwire, the `Schema` value object, `Pool`, and (Task 6) `Fresh`. |
| `internal/store/storetest/storetest_test.go` | Created (Task 4) | Proves the tripwire fires, the schema is pinned on every pooled connection, and the schema is dropped on cleanup. |
| `.github/workflows/go.yml` | Modified (Tasks 5, 18) | Task 5 adds the `integration (postgres)` job and one guard step to `check`; Task 18 adds a govulncheck step and pins all five actions by digest. No pre-existing step is edited. |
| `Makefile` | Modified (Task 5) | Adds `pg-up`, `pg-down`, `pg-psql`, `go-test-pg`. Existing targets untouched. |
| `docker-compose.yaml` | Modified (Task 5) | Adds a loopback-bound `postgres` service and a `postgres_data` volume, additively. |
| `docs/testing-postgres.md` | Created (Task 5) | How to run the Postgres suite locally and why the tripwire exists. |
| `internal/store/postgres/migrations.sql` | Created (Task 6) | The whole schema: five tables, SEVEN named indexes (one unique dedupe index, five record indexes, one station GIN index), the three CHECK constraints, the `app_stats` singleton. Idempotent. The count is spelled out because `migrations_test.go` asserts it and an earlier draft double-counted the dedupe index. |
| `internal/store/postgres/migrate.go` | Created (Task 6) | `//go:embed`s `migrations.sql` and applies it. |
| `internal/store/postgres/migrations_test.go` | Created (Task 6) | Run-twice idempotency, every CHECK constraint rejecting its bad row, the dedupe index raising 23505, the generated `search_tsv`, and that `uuidv7()` is absent while `gen_random_uuid()` is present. |
| `internal/store/postgres/errors.go` | Created (Task 7) | The SQLSTATE constants, `ErrOperation`, and `classify` — the only place a driver error is translated. |
| `internal/store/postgres/records.go` | Created (Task 7), Modified (Tasks 8-12, 15) | `RecordStore` and its 14 methods. All SQL is package-level `const`. |
| `internal/store/postgres/records_insert_test.go` | Created (Task 7) | Dedupe returning `false, nil`; a PK collision returning `ErrConflict`; the 33-field jsonb round trip; the classifier not leaking. |
| `internal/store/postgres/records_claim_test.go` | Created (Task 8) | The 12-goroutine zero-double-claim invariant, the `MaxConns` guard, and the one-ref-per-call assertion. |
| `internal/store/postgres/records_complete_test.go` | Created (Task 9) | The single transaction over records, `app_stats` and `stations`; the no-op on a second call. |
| `internal/store/postgres/records_classify_test.go` | Created (Task 10) | `FailPermanent` / `RequeueInfra` / `MarkUnknown`: which columns each writes and that each only touches `processing` rows. |
| `internal/store/postgres/records_requeue_test.go` | Created (Task 11) | The reaper with a backdated `claimed_at` and no sleeps; `Requeue` with and without `DryRun`. |
| `internal/store/postgres/records_read_test.go` | Created (Task 12) | The total order under a shared `created_at`, pagination totals, filters, `Get`'s `ErrNotFound`, `TxIDExists`. |
| `internal/store/postgres/stations.go` | Created (Task 13), Modified (Task 14) | `StationStore`: `Upsert`, `Get`, `List` with the one-parsed-value search split, and `Stats`. |
| `internal/store/postgres/stations_test.go` | Created (Task 13) | Upsert insert-then-update, the three search branches, and the 14-input `websearch_to_tsquery` fuzz. |
| `internal/store/postgres/stats_test.go` | Created (Task 14) | `Stats` as a live `activeStations` count and `Snapshot`'s six filtered counts. |
| `internal/store/postgres/records_chain_test.go` | Created (Task 15) | `SetBlockHeights` writing both tables in one transaction, no-op on an unknown txid, and `ReconcileCandidates`. |
| `internal/store/postgres/deposits.go` | Created (Task 16) | `DepositStore` and `PreflightStore`. |
| `internal/store/postgres/deposits_test.go` | Created (Task 16) | Deposit lifecycle and preflight fingerprint round trip. |
| `internal/store/postgres/postgres.go` | Created (Task 17) | `AllQueries()` and `New(pool) store.Store` — the package's assembly point and the list Task 17 prepares. |
| `internal/store/postgres/security_test.go` | Created (Task 17) | Every statement PREPAREs; `classify` leaks nothing, including on a canceled context; the live simple-protocol rejection; the unauthenticated verify path cannot touch a non-completed row. |
| `internal/store/storetest/conformance.go` | Created (Task 19) | `RunStoreConformance`: the behavioral assertions that must hold for BOTH implementations, so the fake cannot drift away from the SQL while B2 tests against it. |
| `internal/store/fake/conformance_test.go` | Created (Task 19) | Runs the conformance suite over the fake, with no database. |
| `internal/store/postgres/conformance_test.go` | Created (Task 19) | Runs the identical suite over `postgres.New(pool)` behind `storetest.Fresh`. |

---

## Task 1: Store types, the exact status literals, and the nullable-pointer rule

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

Consumes (already in the repo, from Plan A — read `internal/weather/types.go` if you need more):
```go
package weather
const DataFieldsPerRecord = 33
type WeatherData struct { /* 33 exported fields, snake_case json tags, alphabetical */ }
```

Produces (every later task and both later plans depend on these exact names and types):
```go
package store // import "github.com/bsv-blockchain-demos/weather-proof/internal/store"

var ErrNotFound error
var ErrConflict error
var ErrInvalidText error
func ValidText(s string) error

type Status string
const (
    StatusPending    Status = "pending"
    StatusProcessing Status = "processing"
    StatusCompleted  Status = "completed"
    StatusFailed     Status = "failed"
)
func (s Status) Valid() bool

type ChainStatus string
const (
    ChainARCAccepted ChainStatus = "arc-accepted"
    ChainUnmined     ChainStatus = "unmined"
    ChainMined       ChainStatus = "mined"
    ChainAborted     ChainStatus = "aborted"
)
func (c ChainStatus) Valid() bool

type Record struct {
    ID              string
    StationID       int64
    Timestamp       time.Time
    ObservationTime time.Time
    Data            weather.WeatherData
    Status          Status
    Attempts        int32
    ClaimRef        *uuid.UUID
    AdoptRequired   bool
    ClaimedAt       *time.Time
    TxID            *string
    OutputIndex     *int32
    BlockHeight     *int64
    ChainStatus     *ChainStatus
    MinedAt         *time.Time
    Error           *string
    CreatedAt       time.Time
    ProcessedAt     *time.Time
}

type NewRecord struct {
    ID              string
    StationID       int64
    Timestamp       time.Time
    ObservationTime time.Time
    Data            weather.WeatherData
}

type Station struct {
    StationID       int64
    Name            string
    Location        string
    Latitude        *float64
    Longitude       *float64
    IsActive        bool
    TxRecords       int64
    LastReading     *time.Time
    LastTemp        *float64
    LastConditions  string
    LastBlockHeight *int64
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type Stats struct {
    ActiveStations  int64
    TotalTx         int64
    TotalRecords    int64
    LastRecordWrite *time.Time
}
func (s Stats) TotalDataPoints() int64

type Snapshot struct {
    PendingRows             int64
    ProcessingRows          int64
    FailedRows              int64
    StillUnminedOlderThan1h int64
    MinedCount              int64
    AbortedCount            int64
}

type ListFilter struct {
    StationID *int64
    Status    *Status
    Limit     int
    Offset    int
}

type StationFilter struct {
    Search string
    Limit  int
    Offset int
}

type Publication struct {
    RecordID    string
    OutputIndex int32
}

type BlockHeightUpdate struct {
    TxID        string
    BlockHeight int64
    MinedAt     *time.Time
}

type RequeueFilter struct {
    Status    Status
    Since     time.Duration
    StationID *int64
    Limit     int
    DryRun    bool
}

type Deposit struct {
    Suffix         string
    Prefix         string
    Address        string
    LockingScript  string
    CreatedAt      time.Time
    TxID           *string
    Vout           *int32
    Satoshis       *int64
    InternalizedAt *time.Time
}
```

### Steps

- [ ] **Add the `google/uuid` dependency.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go get github.com/google/uuid@v1.6.0
  ```
  Expected output includes `go: added github.com/google/uuid v1.6.0`. (`go.mod` currently has one direct requirement, `github.com/bsv-blockchain/go-sdk v1.3.2`.)

- [ ] **Write the failing test.** Create `internal/store/store_test.go` with exactly this content:
  ```go
  package store_test

  import (
  	"errors"
  	"sort"
  	"strings"
  	"testing"
  	"time"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  )

  // TestStatusWireLiterals pins the four strings the frontend indexes its
  // statusStyles map with. If any of them changes, VerificationBadge renders an
  // undefined className and the row reads "Not On-Chain" forever, silently.
  func TestStatusWireLiterals(t *testing.T) {
  	cases := []struct {
  		got  store.Status
  		want string
  	}{
  		{store.StatusPending, "pending"},
  		{store.StatusProcessing, "processing"},
  		{store.StatusCompleted, "completed"},
  		{store.StatusFailed, "failed"},
  	}
  	for _, c := range cases {
  		if string(c.got) != c.want {
  			t.Errorf("status literal = %q, want %q", string(c.got), c.want)
  		}
  	}

  	all := []string{
  		string(store.StatusPending),
  		string(store.StatusProcessing),
  		string(store.StatusCompleted),
  		string(store.StatusFailed),
  	}
  	sort.Strings(all)
  	want := []string{"completed", "failed", "pending", "processing"}
  	if len(all) != len(want) {
  		t.Fatalf("status count = %d, want %d", len(all), len(want))
  	}
  	for i := range want {
  		if all[i] != want[i] {
  			t.Fatalf("sorted statuses = %v, want %v", all, want)
  		}
  	}
  }

  // TestStatusValid is the allowlist behind the ?status= query parameter. The
  // TypeScript silently drops an unknown value and answers 200 with unfiltered
  // results; the Go API returns 400, which needs this to be exact.
  func TestStatusValid(t *testing.T) {
  	valid := []store.Status{
  		store.StatusPending, store.StatusProcessing,
  		store.StatusCompleted, store.StatusFailed,
  	}
  	for _, s := range valid {
  		if !s.Valid() {
  			t.Errorf("Status(%q).Valid() = false, want true", string(s))
  		}
  	}

  	invalid := []store.Status{
  		"", "PENDING", "Pending", "pending ", " pending", "unknown",
  		"arc-accepted", "mined", "0", "pending,failed",
  	}
  	for _, s := range invalid {
  		if s.Valid() {
  			t.Errorf("Status(%q).Valid() = true, want false", string(s))
  		}
  	}
  }

  // TestChainStatusWireLiterals pins the four chain-detail literals. These are a
  // different column and a different type from Status; the SPA never sees them.
  func TestChainStatusWireLiterals(t *testing.T) {
  	cases := []struct {
  		got  store.ChainStatus
  		want string
  	}{
  		{store.ChainARCAccepted, "arc-accepted"},
  		{store.ChainUnmined, "unmined"},
  		{store.ChainMined, "mined"},
  		{store.ChainAborted, "aborted"},
  	}
  	for _, c := range cases {
  		if string(c.got) != c.want {
  			t.Errorf("chain status literal = %q, want %q", string(c.got), c.want)
  		}
  	}

  	invalid := []store.ChainStatus{"", "ARC-ACCEPTED", "arc_accepted", "pending", "unknown"}
  	for _, c := range invalid {
  		if c.Valid() {
  			t.Errorf("ChainStatus(%q).Valid() = true, want false", string(c))
  		}
  	}
  }

  // TestValidTextRejectsOnlyWhatPostgresCannotBind is the single definition of
  // "bindable text" in this module.
  //
  // MEASURED against postgres:17-alpine with pgx v5.10.0: binding a string
  // containing 0x00 to even `SELECT $1::text` fails with `invalid byte sequence
  // for encoding "UTF8": 0x00 (SQLSTATE 22021)`. The extended query protocol
  // prevents INJECTION; it does not make the value legal. Without this gate
  // `GET /api/stations?search=%00` and `GET /api/weather/%00` are both 500s,
  // because Go's query and path decoding hand a literal NUL straight through.
  //
  // Nothing else is rejected. A tab, a newline, an emoji and a lone surrogate
  // escape are all bindable, and rejecting them would break real search terms.
  func TestValidTextRejectsOnlyWhatPostgresCannotBind(t *testing.T) {
  	bad := []string{"\x00", "\x00nul", "nul\x00", "a\x00b", string([]byte{0})}
  	for _, s := range bad {
  		if err := store.ValidText(s); !errors.Is(err, store.ErrInvalidText) {
  			t.Errorf("ValidText(%q) = %v, want store.ErrInvalidText", s, err)
  		}
  	}

  	ok := []string{
  		"", " ", "bristol", "1001", "\t\n", "Ünïcödé", "🌦",
  		"'; DROP TABLE stations; --", strings.Repeat("a", 4096),
  	}
  	for _, s := range ok {
  		if err := store.ValidText(s); err != nil {
  			t.Errorf("ValidText(%q) = %v, want nil", s, err)
  		}
  	}
  }

  // TestStatsTotalDataPoints proves the multiplier comes from
  // weather.DataFieldsPerRecord and is not a second hand-written 33.
  func TestStatsTotalDataPoints(t *testing.T) {
  	cases := []struct {
  		records int64
  		want    int64
  	}{
  		{0, 0},
  		{1, 33},
  		{7, 231},
  		{3178, 104874},
  	}
  	for _, c := range cases {
  		s := store.Stats{TotalRecords: c.records}
  		if got := s.TotalDataPoints(); got != c.want {
  			t.Errorf("Stats{TotalRecords: %d}.TotalDataPoints() = %d, want %d", c.records, got, c.want)
  		}
  	}
  }

  // TestNullableTimestampsArePointers is a compile-time-shaped guard on the rule
  // that every nullable timestamp is a *time.Time. A zero time.Time marshals to
  // "0001-01-01T00:00:00Z", which the frontend renders as 01/01/0001 where it
  // intends an em dash, so the API layer needs a real nil to project to null.
  func TestNullableTimestampsArePointers(t *testing.T) {
  	var r store.Record
  	if r.ClaimedAt != nil || r.MinedAt != nil || r.ProcessedAt != nil {
  		t.Fatal("zero Record must have nil nullable timestamps")
  	}
  	var st store.Station
  	if st.LastReading != nil || st.LastTemp != nil || st.LastBlockHeight != nil {
  		t.Fatal("zero Station must have nil nullable fields")
  	}
  	var s store.Stats
  	if s.LastRecordWrite != nil {
  		t.Fatal("zero Stats must have a nil LastRecordWrite")
  	}
  	// Assigning through the pointers must compile and round-trip.
  	now := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	r.ProcessedAt = &now
  	if !r.ProcessedAt.Equal(now) {
  		t.Fatal("ProcessedAt did not round-trip")
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/... -count=1
  ```
  Expected failure text: `internal/store/store_test.go:9:2: no required module provides package github.com/bsv-blockchain-demos/weather-proof/internal/store` (or `build constraints exclude all Go files` / `package … is not in std` — any variant reporting that the package does not exist).

- [ ] **Write the implementation.** Create `internal/store/store.go` with exactly this content:
  ```go
  // Package store is the persistence seam.
  //
  // It holds the domain types and the interfaces every layer above persistence
  // programs against, and it imports NO database driver. That is deliberate and
  // load-bearing: internal/api and internal/proof are testable against
  // internal/store/fake with no database at all, which is what keeps the bulk of
  // the HTTP test suite running in the ordinary CI job. Nothing in this package
  // may ever import internal/store/postgres.
  package store

  import (
  	"errors"
  	"strings"
  	"time"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
  )

  // The three sentinel errors every implementation returns instead of a driver
  // error. A *pgconn.PgError carries SQL text, column names, constraint names
  // and sometimes column VALUES in its Error() string, so it must never travel
  // far enough to reach an HTTP response body.
  var (
  	// ErrNotFound is returned by Get when no row matches.
  	ErrNotFound = errors.New("store: not found")

  	// ErrConflict is returned when a write collided in a way that is NOT the
  	// expected steady state. Note that Insert's dedupe collision is not a
  	// conflict: a station re-reporting the same observation_time is normal and
  	// reports inserted=false with a nil error.
  	ErrConflict = errors.New("store: conflicting row")

  	// ErrInvalidText reports a string that a Postgres text parameter cannot
  	// carry at all. See ValidText.
  	ErrInvalidText = errors.New("store: text value contains a NUL byte")
  )

  // ValidText reports whether s can be bound to a text parameter.
  //
  // There is exactly one thing wrong with a Go string from Postgres's point of
  // view, and it is the NUL byte. Measured against postgres:17-alpine with pgx
  // v5.10.0: binding a string containing 0x00 to `SELECT $1::text` fails with
  // `invalid byte sequence for encoding "UTF8": 0x00 (SQLSTATE 22021)`. The
  // extended query protocol prevents INJECTION — it does not make the value
  // legal — so a request carrying %00 is a 500 unless something rejects it
  // first, and Go's query-string and path decoding both hand a literal NUL
  // straight through.
  //
  // This is deliberately a leaf function in the datastore-agnostic package,
  // because BOTH layers need it and for different reasons: the API layer maps a
  // failure to 400 (the right status), and internal/store/postgres treats a
  // failure as a MISS (so the store is total and no future caller can produce a
  // 500 from user input). Nothing else is rejected: tabs, newlines and any valid
  // UTF-8 are legitimate search terms.
  func ValidText(s string) error {
  	if strings.ContainsRune(s, 0) {
  		return ErrInvalidText
  	}
  	return nil
  }

  // Status is the lifecycle status of a weather record, and it is the exact
  // string stored in the status text column and emitted on the wire.
  //
  // It is a named string type rather than an int enum because it encodes and
  // scans directly against a text column, and because the four values are the
  // four keys the frontend indexes its statusStyles map with. An unknown value
  // does not crash the SPA but makes the row read "Not On-Chain" forever, so the
  // set is closed and the API rejects anything outside it with a 400.
  type Status string

  // The only four status values that exist. There are no others.
  const (
  	StatusPending    Status = "pending"
  	StatusProcessing Status = "processing"
  	StatusCompleted  Status = "completed"
  	StatusFailed     Status = "failed"
  )

  // Valid reports whether s is one of the four known statuses. It is the
  // allowlist behind the ?status= query parameter.
  func (s Status) Valid() bool {
  	switch s {
  	case StatusPending, StatusProcessing, StatusCompleted, StatusFailed:
  		return true
  	default:
  		return false
  	}
  }

  // ChainStatus is the on-chain settlement detail of a published record. It is a
  // SEPARATE column and a separate type from Status: the SPA never sees it, and
  // conflating the two is how an "aborted" transaction ends up reported as a
  // "failed" record.
  type ChainStatus string

  // The only four chain statuses that exist.
  const (
  	ChainARCAccepted ChainStatus = "arc-accepted"
  	ChainUnmined     ChainStatus = "unmined"
  	ChainMined       ChainStatus = "mined"
  	ChainAborted     ChainStatus = "aborted"
  )

  // Valid reports whether c is one of the four known chain statuses.
  func (c ChainStatus) Valid() bool {
  	switch c {
  	case ChainARCAccepted, ChainUnmined, ChainMined, ChainAborted:
  		return true
  	default:
  		return false
  	}
  }

  // Record is one row of weather_records.
  //
  // Every nullable column is a POINTER. That is not a style choice: a zero
  // time.Time marshals to "0001-01-01T00:00:00Z", which the frontend's
  // formatDateTime renders as 01/01/0001 rather than the em dash it intends for
  // an absent value, and a zero string is indistinguishable from an empty error
  // message. The db tags are read by pgx.RowToStructByName and must match the
  // column names in every explicit SELECT and RETURNING list exactly.
  type Record struct {
  	ID              string              `db:"id"`
  	StationID       int64               `db:"station_id"`
  	Timestamp       time.Time           `db:"timestamp"`
  	ObservationTime time.Time           `db:"observation_time"`
  	Data            weather.WeatherData `db:"data"`
  	Status          Status              `db:"status"`
  	Attempts        int32               `db:"attempts"`
  	ClaimRef        *uuid.UUID          `db:"claim_ref"`
  	AdoptRequired   bool                `db:"adopt_required"`
  	ClaimedAt       *time.Time          `db:"claimed_at"`
  	TxID            *string             `db:"txid"`
  	OutputIndex     *int32              `db:"output_index"`
  	BlockHeight     *int64              `db:"block_height"`
  	ChainStatus     *ChainStatus        `db:"chain_status"`
  	MinedAt         *time.Time          `db:"mined_at"`
  	Error           *string             `db:"error"`
  	CreatedAt       time.Time           `db:"created_at"`
  	ProcessedAt     *time.Time          `db:"processed_at"`
  }

  // NewRecord is a record as the poller can supply it.
  //
  // It is a separate type from Record on purpose: a caller inserting a fresh
  // reading has no business setting status, attempts, claim_ref or any of the
  // publication columns, and giving it a Record would let it try.
  type NewRecord struct {
  	ID              string              `db:"id"`
  	StationID       int64               `db:"station_id"`
  	Timestamp       time.Time           `db:"timestamp"`
  	ObservationTime time.Time           `db:"observation_time"`
  	Data            weather.WeatherData `db:"data"`
  }

  // Station is one row of stations.
  //
  // Note what is NOT here: the derived "online" flag. The store returns IsActive
  // and LastReading and the API derives online from the poll rate, which keeps
  // the freshness window in config, keeps the DTO test deterministic without
  // freezing now(), and stops the poll rate leaking into persistence.
  type Station struct {
  	StationID       int64      `db:"station_id"`
  	Name            string     `db:"name"`
  	Location        string     `db:"location"`
  	Latitude        *float64   `db:"latitude"`
  	Longitude       *float64   `db:"longitude"`
  	IsActive        bool       `db:"is_active"`
  	TxRecords       int64      `db:"tx_records"`
  	LastReading     *time.Time `db:"last_reading"`
  	LastTemp        *float64   `db:"last_temp"`
  	LastConditions  string     `db:"last_conditions"`
  	LastBlockHeight *int64     `db:"last_block_height"`
  	CreatedAt       time.Time  `db:"created_at"`
  	UpdatedAt       time.Time  `db:"updated_at"`
  }

  // Stats is the four-value dashboard summary, plus the record count the fourth
  // value is derived from.
  type Stats struct {
  	ActiveStations  int64      `db:"active_stations"`
  	TotalTx         int64      `db:"total_tx"`
  	TotalRecords    int64      `db:"total_records"`
  	LastRecordWrite *time.Time `db:"last_record_write"`
  }

  // TotalDataPoints is the frontend's totalDataPoints tile.
  //
  // The multiplier is weather.DataFieldsPerRecord and never a literal 33. There
  // is exactly one definition of the field count in this module and a second
  // copy is how the two drift.
  func (s Stats) TotalDataPoints() int64 {
  	return s.TotalRecords * weather.DataFieldsPerRecord
  }

  // Snapshot is the row-count half of the operational heartbeat.
  type Snapshot struct {
  	PendingRows             int64 `db:"pending_rows"`
  	ProcessingRows          int64 `db:"processing_rows"`
  	FailedRows              int64 `db:"failed_rows"`
  	StillUnminedOlderThan1h int64 `db:"still_unmined_older_than_1h"`
  	MinedCount              int64 `db:"mined_count"`
  	AbortedCount            int64 `db:"aborted_count"`
  }

  // ListFilter is the record list query. A nil StationID or Status means the
  // filter is absent, which the SQL expresses as a NULL-able bind parameter
  // rather than a conditionally assembled WHERE clause.
  type ListFilter struct {
  	StationID *int64
  	Status    *Status
  	Limit     int
  	Offset    int
  }

  // StationFilter is the station list query. Search is already trimmed by the
  // caller; an empty Search means no filter and no ranking.
  type StationFilter struct {
  	Search string
  	Limit  int
  	Offset int
  }

  // Publication is one record's placement in a published transaction.
  type Publication struct {
  	RecordID    string
  	OutputIndex int32
  }

  // BlockHeightUpdate refreshes the mined height of every record sharing a txid.
  //
  // BlockHeight is deliberately a plain int64 with no way for an HTTP caller to
  // supply it: on the unauthenticated verify path the caller chooses only WHICH
  // rows are refreshed, and the value comes from the block explorer.
  type BlockHeightUpdate struct {
  	TxID        string
  	BlockHeight int64
  	MinedAt     *time.Time
  }

  // RequeueFilter is the operator-driven bulk requeue. DryRun counts without
  // writing.
  type RequeueFilter struct {
  	Status    Status
  	Since     time.Duration
  	StationID *int64
  	Limit     int
  	DryRun    bool
  }

  // Deposit is one operator funding deposit awaiting internalization.
  type Deposit struct {
  	Suffix         string     `db:"suffix"`
  	Prefix         string     `db:"prefix"`
  	Address        string     `db:"address"`
  	LockingScript  string     `db:"locking_script"`
  	CreatedAt      time.Time  `db:"created_at"`
  	TxID           *string    `db:"txid"`
  	Vout           *int32     `db:"vout"`
  	Satoshis       *int64     `db:"satoshis"`
  	InternalizedAt *time.Time `db:"internalized_at"`
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/... -count=1
  ```
  Expected output: `ok  	github.com/bsv-blockchain-demos/weather-proof/internal/store	0.0Xs`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go mod tidy && git diff --exit-code go.mod go.sum ; go vet ./... && go build ./... && go test ./... -count=1
  ```
  `git diff --exit-code` is expected to print the go.mod/go.sum diff and exit non-zero the FIRST time (because `go get` already wrote them and they are not yet committed) — that is fine, it is the uncommitted-change diff, not a tidiness failure. `go vet`, `go build` and `go test` must all pass with no output beyond `ok` lines.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add go.mod go.sum internal/store/store.go internal/store/store_test.go && git commit -m "store: domain types and the four status literals"
  ```

---

## Task 2: Store interfaces, the aggregate Store struct, and the in-memory fake

**Files:**
- Create: `internal/store/interfaces.go`
- Create: `internal/store/fake/fake.go`
- Create: `internal/store/fake/fake_test.go`

**Interfaces:**

Consumes (Task 1): `store.Status`, `store.StatusPending`, `store.StatusProcessing`, `store.StatusCompleted`, `store.StatusFailed`, `store.ChainStatus`, `store.ChainARCAccepted`, `store.Record`, `store.NewRecord`, `store.Station`, `store.Stats`, `store.Snapshot`, `store.ListFilter`, `store.StationFilter`, `store.Publication`, `store.BlockHeightUpdate`, `store.RequeueFilter`, `store.Deposit`, `store.ErrNotFound`, `store.ErrConflict`.

Produces:
```go
package store

type RecordStore interface {
    Insert(ctx context.Context, r NewRecord) (bool, error)
    ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]Record, error)
    Complete(ctx context.Context, txID string, pubs []Publication) (Stats, error)
    FailPermanent(ctx context.Context, ids []string, reason string) error
    RequeueInfra(ctx context.Context, ids []string, reason string) error
    MarkUnknown(ctx context.Context, ids []string, reason string) error
    ReapExpired(ctx context.Context, lease time.Duration, limit int) ([]Record, error)
    Requeue(ctx context.Context, f RequeueFilter) (int64, error)
    List(ctx context.Context, f ListFilter) ([]Record, int64, error)
    Get(ctx context.Context, id string) (Record, error)
    TxIDExists(ctx context.Context, txID string) (bool, error)
    SetBlockHeights(ctx context.Context, ups []BlockHeightUpdate) error
    ReconcileCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]Record, error)
    Snapshot(ctx context.Context) (Snapshot, error)
}

type StationStore interface {
    Upsert(ctx context.Context, s Station) error
    List(ctx context.Context, f StationFilter) ([]Station, int64, error)
    Get(ctx context.Context, stationID int64) (Station, error)
    Stats(ctx context.Context) (Stats, error)
}

type DepositStore interface {
    NewDeposit(ctx context.Context, d Deposit) error
    PendingDeposits(ctx context.Context) ([]Deposit, error)
    MarkInternalized(ctx context.Context, suffix, txID string, vout int32, sats int64) error
}

type PreflightStore interface {
    PreflightOK(ctx context.Context, fingerprint string) (bool, error)
    RecordPreflight(ctx context.Context, fingerprint string) error
}

type Pinger interface {
    Ping(ctx context.Context) error
}

type Store struct {
    Records   RecordStore
    Stations  StationStore
    Deposits  DepositStore
    Preflight PreflightStore
    Health    Pinger
}
```

```go
package fake // import ".../internal/store/fake"

func New() *Store

// Store is the in-memory implementation. Its three exported fields are the
// injection points; all other state is unexported (maps guarded by one mutex).
type Store struct {
    Now     func() time.Time // injectable clock; defaults to 2026-04-17T15:40:00Z
    FailAll error            // when non-nil, every method except Ping returns it
    PingErr error            // returned by Ping
}

func (s *Store) Store() store.Store           // the aggregate seam, all five members wired
func (s *Store) Stations() store.StationStore  // the station view (see below)
func (s *Store) SeedRecord(r store.Record)
func (s *Store) SeedStation(st store.Station)

// *Store implements store.RecordStore, store.DepositStore, store.PreflightStore
// and store.Pinger directly. store.StationStore is reached through Stations():
// *Store cannot carry both Get(ctx, string) and Get(ctx, int64), so its station
// methods are named GetStation and ListStations and a tiny adapter renames them.
// Upsert and Stats are unambiguous and live on *Store itself.
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/fake/fake_test.go` with exactly this content:
  ```go
  package fake_test

  import (
  	"context"
  	"errors"
  	"testing"
  	"time"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
  )

  // Compile-time conformance. If any interface method signature drifts, this
  // fails to build rather than failing at runtime in a later plan.
  //
  // StationStore is asserted against the adapter rather than against *fake.Store,
  // because *fake.Store cannot carry both Get(ctx, string) and Get(ctx, int64).
  // That is the same asymmetry that forces store.Store to be a struct of
  // interfaces rather than one composed interface.
  var (
  	_ store.RecordStore    = (*fake.Store)(nil)
  	_ store.DepositStore   = (*fake.Store)(nil)
  	_ store.PreflightStore = (*fake.Store)(nil)
  	_ store.Pinger         = (*fake.Store)(nil)
  	_ store.StationStore   = fake.New().Stations()
  )

  func newRecord(id string, stationID int64, obs time.Time) store.NewRecord {
  	return store.NewRecord{
  		ID:              id,
  		StationID:       stationID,
  		Timestamp:       obs,
  		ObservationTime: obs,
  		Data:            weather.WeatherData{AirTemperature: 18, Conditions: "Clear"},
  	}
  }

  func TestStoreAggregateIsWired(t *testing.T) {
  	f := fake.New()
  	agg := f.Store()
  	if agg.Records == nil || agg.Stations == nil || agg.Deposits == nil ||
  		agg.Preflight == nil || agg.Health == nil {
  		t.Fatal("fake.Store().Store() left a field nil")
  	}
  }

  func TestInsertDedupeIsNotAnError(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  	inserted, err := f.Insert(ctx, newRecord("a", 1000, obs))
  	if err != nil || !inserted {
  		t.Fatalf("first Insert = (%v, %v), want (true, nil)", inserted, err)
  	}

  	inserted, err = f.Insert(ctx, newRecord("b", 1000, obs))
  	if err != nil {
  		t.Fatalf("duplicate Insert error = %v, want nil", err)
  	}
  	if inserted {
  		t.Fatal("duplicate Insert = true, want false")
  	}

  	// A repeated ID with a DIFFERENT dedupe key is a genuine surprise, and the
  	// Postgres store reports it as store.ErrConflict (the ON CONFLICT arbiter
  	// covers (station_id, observation_time), not the primary key). The fake must
  	// agree, or every B2 test of the 409 path passes against a fake that cannot
  	// produce one.
  	if _, conflictErr := f.Insert(ctx, newRecord("a", 2000, obs.Add(time.Hour))); !errors.Is(conflictErr, store.ErrConflict) {
  		t.Fatalf("Insert with a repeated id = %v, want store.ErrConflict", conflictErr)
  	}
  }

  // TestFakeStationSearchMatchesTheSQLBranches is the anti-drift test on the one
  // divergence that would be invisible: if ListStations ignored f.Search, every
  // B2 test of ?search= would pass while proving nothing at all.
  //
  // The fake cannot reproduce websearch_to_tsquery's ranking and does not try.
  // What it reproduces is the CONTRACT both implementations share, which
  // Task 19's conformance suite asserts against both: an all-digits search is an
  // exact station_id lookup, anything else matches name or location
  // case-insensitively, a NUL byte matches nothing without erroring, and the
  // total is the unpaged count.
  func TestFakeStationSearchMatchesTheSQLBranches(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	f.SeedStation(store.Station{StationID: 1000, Name: "Harbor Mast", Location: "Bristol Docks", IsActive: true})
  	f.SeedStation(store.Station{StationID: 1001, Name: "Clifton Ridge", Location: "Bristol Downs", IsActive: true})
  	f.SeedStation(store.Station{StationID: 2000, Name: "Kelvin Yard", Location: "Glasgow", IsActive: true})
  	ss := f.Stations()

  	cases := []struct {
  		search string
  		want   int64
  	}{
  		{"", 3},
  		{"1001", 1},
  		{"424242", 0},
  		{"bristol", 2},
  		{"KELVIN", 1},
  		{"reykjavik", 0},
  		{"12345abc", 0},
  		{"\x00nul", 0},
  	}
  	for _, c := range cases {
  		sts, total, err := ss.List(ctx, store.StationFilter{Search: c.search, Limit: 50})
  		if err != nil {
  			t.Errorf("List(search=%q) error = %v, want nil", c.search, err)
  			continue
  		}
  		if total != c.want {
  			t.Errorf("List(search=%q) total = %d, want %d", c.search, total, c.want)
  		}
  		if int64(len(sts)) != c.want {
  			t.Errorf("List(search=%q) returned %d rows, want %d", c.search, len(sts), c.want)
  		}
  	}
  }

  // TestFakeCompleteMatchesTheSQLStationSemantics pins the two station-bump rules
  // the SQL has and an earlier fake did not: a missing station row is NOT created
  // (bumpStationsSQL matches nothing), and last_temp/last_conditions advance only
  // when the reading is NEWER than the stored last_reading.
  func TestFakeCompleteMatchesTheSQLStationSemantics(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  	// No station row: the record still completes and the counters still move,
  	// but no station is invented.
  	if _, err := f.Insert(ctx, newRecord("orphan", 4242, base)); err != nil {
  		t.Fatalf("Insert: %v", err)
  	}
  	claimed, err := f.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	if _, completeErr := f.Complete(ctx, "orphan-tx",
  		[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
  		t.Fatalf("Complete: %v", completeErr)
  	}
  	if _, getErr := f.Stations().Get(ctx, 4242); !errors.Is(getErr, store.ErrNotFound) {
  		t.Fatalf("station 4242 = %v, want store.ErrNotFound: Complete must not invent a station", getErr)
  	}

  	// An OLDER reading completing later must not overwrite last_temp.
  	f.SeedStation(store.Station{StationID: 1000, IsActive: true})
  	newer := store.NewRecord{
  		ID: "newer", StationID: 1000, Timestamp: base.Add(time.Hour), ObservationTime: base.Add(time.Hour),
  		Data: weather.WeatherData{AirTemperature: 21, Conditions: "Clear"},
  	}
  	older := store.NewRecord{
  		ID: "older", StationID: 1000, Timestamp: base, ObservationTime: base,
  		Data: weather.WeatherData{AirTemperature: 4, Conditions: "Snow"},
  	}
  	for _, r := range []store.NewRecord{newer, older} {
  		if _, insertErr := f.Insert(ctx, r); insertErr != nil {
  			t.Fatalf("Insert %s: %v", r.ID, insertErr)
  		}
  		batch, claimErr := f.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
  		if claimErr != nil {
  			t.Fatalf("ClaimPending %s: %v", r.ID, claimErr)
  		}
  		if _, completeErr := f.Complete(ctx, "tx-"+r.ID,
  			[]store.Publication{{RecordID: batch[0].ID, OutputIndex: 0}}); completeErr != nil {
  			t.Fatalf("Complete %s: %v", r.ID, completeErr)
  		}
  	}
  	st, err := f.Stations().Get(ctx, 1000)
  	if err != nil {
  		t.Fatalf("station Get: %v", err)
  	}
  	if st.LastTemp == nil || *st.LastTemp != 21 {
  		t.Errorf("last_temp = %v, want the newer reading's 21", st.LastTemp)
  	}
  	if st.LastConditions != "Clear" {
  		t.Errorf("last_conditions = %q, want Clear", st.LastConditions)
  	}
  	if st.TxRecords != 2 {
  		t.Errorf("tx_records = %d, want 2 (both records counted)", st.TxRecords)
  	}
  }

  func TestGetUnknownIsErrNotFound(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	for _, id := range []string{"nope", "", "0", "not-a-uuid"} {
  		if _, err := f.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
  			t.Errorf("Get(%q) error = %v, want store.ErrNotFound", id, err)
  		}
  	}
  }

  func TestStationGetUnknownIsErrNotFound(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	ss := f.Stations()
  	for _, id := range []int64{0, -1, 999} {
  		if _, err := ss.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
  			t.Errorf("station Get(%d) error = %v, want store.ErrNotFound", id, err)
  		}
  	}
  }

  func TestListIsNewestFirstWithIDTiebreak(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	shared := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	f.Now = func() time.Time { return shared }

  	obs := shared
  	for _, id := range []string{"id-1", "id-2", "id-3"} {
  		obs = obs.Add(time.Minute)
  		if _, err := f.Insert(ctx, newRecord(id, 1000, obs)); err != nil {
  			t.Fatalf("Insert %s: %v", id, err)
  		}
  	}

  	recs, total, err := f.List(ctx, store.ListFilter{Limit: 10})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 3 {
  		t.Fatalf("total = %d, want 3", total)
  	}
  	want := []string{"id-3", "id-2", "id-1"}
  	for i := range want {
  		if recs[i].ID != want[i] {
  			t.Fatalf("List order = %v, want %v", ids(recs), want)
  		}
  	}
  }

  func TestListFiltersAndPaginates(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	for i := range 5 {
  		r := newRecord(string(rune('a'+i)), int64(1000+i%2), base.Add(time.Duration(i)*time.Minute))
  		if _, err := f.Insert(ctx, r); err != nil {
  			t.Fatalf("Insert: %v", err)
  		}
  	}

  	station := int64(1000)
  	_, total, err := f.List(ctx, store.ListFilter{StationID: &station, Limit: 10})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 3 {
  		t.Fatalf("station total = %d, want 3", total)
  	}

  	pending := store.StatusPending
  	_, total, err = f.List(ctx, store.ListFilter{Status: &pending, Limit: 10})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 5 {
  		t.Fatalf("pending total = %d, want 5", total)
  	}

  	page1, _, err := f.List(ctx, store.ListFilter{Limit: 2, Offset: 0})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	page2, _, err := f.List(ctx, store.ListFilter{Limit: 2, Offset: 2})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	for _, a := range page1 {
  		for _, b := range page2 {
  			if a.ID == b.ID {
  				t.Fatalf("id %q on both pages", a.ID)
  			}
  		}
  	}
  }

  func TestClaimCompleteAndStats(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	for i := range 3 {
  		if _, err := f.Insert(ctx, newRecord(string(rune('a'+i)), 1000, base.Add(time.Duration(i)*time.Minute))); err != nil {
  			t.Fatalf("Insert: %v", err)
  		}
  	}
  	if err := f.Upsert(ctx, store.Station{StationID: 1000, IsActive: true}); err != nil {
  		t.Fatalf("Upsert: %v", err)
  	}

  	ref := uuid.Must(uuid.NewV7())
  	claimed, err := f.ClaimPending(ctx, 2, ref)
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	if len(claimed) != 2 {
  		t.Fatalf("claimed %d, want 2", len(claimed))
  	}
  	for _, r := range claimed {
  		if r.Status != store.StatusProcessing {
  			t.Fatalf("claimed status = %q, want processing", string(r.Status))
  		}
  		if r.ClaimRef == nil || *r.ClaimRef != ref {
  			t.Fatalf("claim ref = %v, want %v", r.ClaimRef, ref)
  		}
  		if r.ClaimedAt == nil {
  			t.Fatal("claimed row has a nil ClaimedAt")
  		}
  	}

  	pubs := make([]store.Publication, 0, len(claimed))
  	for i, r := range claimed {
  		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
  	}
  	stats, err := f.Complete(ctx, "deadbeef", pubs)
  	if err != nil {
  		t.Fatalf("Complete: %v", err)
  	}
  	if stats.TotalTx != 1 {
  		t.Fatalf("TotalTx = %d, want 1", stats.TotalTx)
  	}
  	if stats.TotalRecords != 2 {
  		t.Fatalf("TotalRecords = %d, want 2", stats.TotalRecords)
  	}
  	if stats.TotalDataPoints() != 66 {
  		t.Fatalf("TotalDataPoints = %d, want 66", stats.TotalDataPoints())
  	}
  	if stats.ActiveStations != 1 {
  		t.Fatalf("ActiveStations = %d, want 1", stats.ActiveStations)
  	}
  	if stats.LastRecordWrite == nil {
  		t.Fatal("LastRecordWrite is nil after Complete")
  	}

  	ok, err := f.TxIDExists(ctx, "deadbeef")
  	if err != nil || !ok {
  		t.Fatalf("TxIDExists = (%v, %v), want (true, nil)", ok, err)
  	}
  	ok, err = f.TxIDExists(ctx, "cafebabe")
  	if err != nil || ok {
  		t.Fatalf("TxIDExists(unknown) = (%v, %v), want (false, nil)", ok, err)
  	}
  }

  func TestReapExpiredPreservesRefAndSetsAdopt(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	claimed := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	f.Now = func() time.Time { return claimed.Add(9 * time.Minute) }
  	ref := uuid.Must(uuid.NewV7())

  	f.SeedRecord(store.Record{
  		ID: "stranded", StationID: 1, Status: store.StatusProcessing,
  		ClaimedAt: &claimed, ClaimRef: &ref, CreatedAt: claimed,
  		ObservationTime: claimed, Timestamp: claimed,
  	})
  	fresh := claimed.Add(8*time.Minute + 30*time.Second)
  	f.SeedRecord(store.Record{
  		ID: "inflight", StationID: 2, Status: store.StatusProcessing,
  		ClaimedAt: &fresh, CreatedAt: claimed,
  		ObservationTime: claimed.Add(time.Minute), Timestamp: claimed,
  	})

  	reaped, err := f.ReapExpired(ctx, 5*time.Minute, 100)
  	if err != nil {
  		t.Fatalf("ReapExpired: %v", err)
  	}
  	if len(reaped) != 1 || reaped[0].ID != "stranded" {
  		t.Fatalf("reaped = %v, want [stranded]", ids(reaped))
  	}
  	if !reaped[0].AdoptRequired {
  		t.Fatal("reaped row must have AdoptRequired true")
  	}
  	if reaped[0].ClaimRef == nil || *reaped[0].ClaimRef != ref {
  		t.Fatal("reaped row must keep its prior claim ref")
  	}
  	if reaped[0].Attempts != 0 {
  		t.Fatalf("reaped Attempts = %d, want 0 (the reaper never spends the budget)", reaped[0].Attempts)
  	}
  }

  func TestSnapshotCounts(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	now := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	f.Now = func() time.Time { return now }
  	old := now.Add(-2 * time.Hour)
  	mined := store.ChainMined
  	aborted := store.ChainAborted

  	f.SeedRecord(store.Record{ID: "p", Status: store.StatusPending})
  	claimedAt := now
  	f.SeedRecord(store.Record{ID: "w", Status: store.StatusProcessing, ClaimedAt: &claimedAt})
  	f.SeedRecord(store.Record{ID: "f", Status: store.StatusFailed})
  	f.SeedRecord(store.Record{ID: "u", Status: store.StatusCompleted, ProcessedAt: &old})
  	f.SeedRecord(store.Record{ID: "m", Status: store.StatusCompleted, ProcessedAt: &old, ChainStatus: &mined})
  	f.SeedRecord(store.Record{ID: "a", Status: store.StatusFailed, ChainStatus: &aborted})

  	snap, err := f.Snapshot(ctx)
  	if err != nil {
  		t.Fatalf("Snapshot: %v", err)
  	}
  	if snap.PendingRows != 1 || snap.ProcessingRows != 1 || snap.FailedRows != 2 {
  		t.Fatalf("snapshot = %+v", snap)
  	}
  	if snap.StillUnminedOlderThan1h != 1 {
  		t.Fatalf("StillUnminedOlderThan1h = %d, want 1", snap.StillUnminedOlderThan1h)
  	}
  	if snap.MinedCount != 1 || snap.AbortedCount != 1 {
  		t.Fatalf("mined/aborted = %d/%d, want 1/1", snap.MinedCount, snap.AbortedCount)
  	}
  }

  func TestFailAllAndPingErrAreInjectable(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	boom := errors.New("boom")
  	f.FailAll = boom
  	// Three values: RecordStore.List returns ([]Record, int64, error). Two
  	// blanks is still legal — dogsled is configured with
  	// max-blank-identifiers 2.
  	if _, _, err := f.List(ctx, store.ListFilter{Limit: 1}); !errors.Is(err, boom) {
  		t.Fatalf("List error = %v, want boom", err)
  	}
  	if _, err := f.Get(ctx, "x"); !errors.Is(err, boom) {
  		t.Fatalf("Get error = %v, want boom", err)
  	}
  	f.FailAll = nil
  	f.PingErr = boom
  	if err := f.Ping(ctx); !errors.Is(err, boom) {
  		t.Fatalf("Ping error = %v, want boom", err)
  	}
  }

  func TestDepositsAndPreflight(t *testing.T) {
  	ctx := context.Background()
  	f := fake.New()
  	if err := f.NewDeposit(ctx, store.Deposit{Suffix: "s1", Prefix: "p1", Address: "addr", LockingScript: "76a9"}); err != nil {
  		t.Fatalf("NewDeposit: %v", err)
  	}
  	pending, err := f.PendingDeposits(ctx)
  	if err != nil {
  		t.Fatalf("PendingDeposits: %v", err)
  	}
  	if len(pending) != 1 {
  		t.Fatalf("pending = %d, want 1", len(pending))
  	}
  	// markErr, not err: the outer err is still live, and govet's shadow check
  	// rejects a redeclaration.
  	if markErr := f.MarkInternalized(ctx, "s1", "txid", 0, 5000); markErr != nil {
  		t.Fatalf("MarkInternalized: %v", markErr)
  	}
  	pending, err = f.PendingDeposits(ctx)
  	if err != nil {
  		t.Fatalf("PendingDeposits: %v", err)
  	}
  	if len(pending) != 0 {
  		t.Fatalf("pending after internalize = %d, want 0", len(pending))
  	}

  	ok, err := f.PreflightOK(ctx, "fp")
  	if err != nil || ok {
  		t.Fatalf("PreflightOK = (%v, %v), want (false, nil)", ok, err)
  	}
  	if recordErr := f.RecordPreflight(ctx, "fp"); recordErr != nil {
  		t.Fatalf("RecordPreflight: %v", recordErr)
  	}
  	ok, err = f.PreflightOK(ctx, "fp")
  	if err != nil || !ok {
  		t.Fatalf("PreflightOK = (%v, %v), want (true, nil)", ok, err)
  	}
  }

  func ids(recs []store.Record) []string {
  	out := make([]string, 0, len(recs))
  	for _, r := range recs {
  		out = append(out, r.ID)
  	}
  	return out
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/... -count=1
  ```
  Expected failure text: `no required module provides package github.com/bsv-blockchain-demos/weather-proof/internal/store/fake` — the package does not exist yet.

- [ ] **Write the interfaces.** Create `internal/store/interfaces.go` with exactly this content:
  ```go
  package store

  import (
  	"context"
  	"time"

  	"github.com/google/uuid"
  )

  // RecordStore is the weather_records seam.
  type RecordStore interface {
  	// Insert writes one fresh reading. A collision on
  	// (station_id, observation_time) reports inserted=false with a NIL error:
  	// a station re-reporting the same observation is the intended steady
  	// state, not an exception.
  	Insert(ctx context.Context, r NewRecord) (bool, error)

  	// ClaimPending atomically moves up to n pending rows to processing and
  	// returns them.
  	//
  	// ref is supplied by the CALLER, one per call. It must not be generated
  	// inside SQL: gen_random_uuid() is VOLATILE and Postgres evaluates it once
  	// per updated row, so a 21-row claim produces 21 distinct refs and the
  	// batch label that the adopt design uses as its idempotency key ceases to
  	// exist. Rows that were previously claimed and then reaped KEEP their
  	// prior ref, so one claim can legitimately return several distinct refs
  	// and the caller must partition the batch by ref.
  	ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]Record, error)

  	// Complete marks the given publications completed under one txid and
  	// returns the refreshed stats. Records, app_stats and the station counters
  	// move in ONE transaction. A second call with the same publications finds
  	// no processing rows and is a no-op that does not double-count.
  	Complete(ctx context.Context, txID string, pubs []Publication) (Stats, error)

  	// FailPermanent marks rows failed and spends one attempt each.
  	FailPermanent(ctx context.Context, ids []string, reason string) error

  	// RequeueInfra returns rows to pending WITHOUT spending an attempt and
  	// without requiring an adopt check: the outcome is known to be a
  	// non-publish.
  	RequeueInfra(ctx context.Context, ids []string, reason string) error

  	// MarkUnknown returns rows to pending with adopt_required set, because the
  	// publish outcome is ambiguous and a prior action may already exist.
  	MarkUnknown(ctx context.Context, ids []string, reason string) error

  	// ReapExpired reclaims rows stranded in processing past the lease. It
  	// returns whole Records rather than ids because the caller needs
  	// claim_ref to log and to reason about the adopt path.
  	ReapExpired(ctx context.Context, lease time.Duration, limit int) ([]Record, error)

  	// Requeue is the operator-driven bulk requeue. It returns the affected
  	// count and writes nothing when f.DryRun is true.
  	Requeue(ctx context.Context, f RequeueFilter) (int64, error)

  	// List returns one page of records newest-first plus the unpaged total.
  	//
  	// There is deliberately no separate ListByStation, which the design's
  	// interface table names: a station filter is one nullable field of
  	// ListFilter, served by one static statement with a NULL-able bind
  	// parameter. Two methods would mean two statements with the same ordering
  	// and pagination rules to keep in agreement.
  	List(ctx context.Context, f ListFilter) ([]Record, int64, error)

  	// Get returns one record, or ErrNotFound.
  	Get(ctx context.Context, id string) (Record, error)

  	// TxIDExists reports whether any record carries this txid. It is the
  	// anti-amplification gate in front of the BEEF proof endpoint: without it,
  	// an attacker iterating 64-hex values drives one upstream call per
  	// request and burns the shared keyless block-explorer budget the storage
  	// server also needs.
  	TxIDExists(ctx context.Context, txID string) (bool, error)

  	// SetBlockHeights refreshes the mined height of every record sharing each
  	// txid, and the corresponding stations.last_block_height, in ONE
  	// transaction. It no-ops for a txid that matches no row.
  	SetBlockHeights(ctx context.Context, ups []BlockHeightUpdate) error

  	// ReconcileCandidates returns completed rows that are not yet known mined
  	// and were processed longer ago than olderThan.
  	ReconcileCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]Record, error)

  	// Snapshot is the row-count half of the operational heartbeat.
  	Snapshot(ctx context.Context) (Snapshot, error)
  }

  // StationStore is the stations seam.
  type StationStore interface {
  	// Upsert inserts or refreshes a station's identity columns. It never
  	// touches the counters, which only Complete moves.
  	Upsert(ctx context.Context, s Station) error

  	// List returns one page of stations plus the unpaged total. When
  	// f.Search parses as an integer it is an exact station_id lookup ordered
  	// by station_id; otherwise it is a full-text match ordered by rank.
  	List(ctx context.Context, f StationFilter) ([]Station, int64, error)

  	// Get returns one station, or ErrNotFound.
  	Get(ctx context.Context, stationID int64) (Station, error)

  	// Stats returns the four dashboard values.
  	Stats(ctx context.Context) (Stats, error)
  }

  // DepositStore is the operator funding-deposit seam.
  type DepositStore interface {
  	NewDeposit(ctx context.Context, d Deposit) error
  	PendingDeposits(ctx context.Context) ([]Deposit, error)
  	MarkInternalized(ctx context.Context, suffix, txID string, vout int32, sats int64) error
  }

  // PreflightStore caches the boot-time preflight fingerprint.
  //
  // Note the ctx: every other store method needs one for the per-call budget
  // and for shutdown cancellation, and preflight runs on the boot path behind
  // the same deadlines.
  type PreflightStore interface {
  	PreflightOK(ctx context.Context, fingerprint string) (bool, error)
  	RecordPreflight(ctx context.Context, fingerprint string) error
  }

  // Pinger is the readiness probe's whole dependency. It is separate from the
  // four data interfaces so /api/ready can be wired to a pool without being
  // handed the ability to write.
  type Pinger interface {
  	Ping(ctx context.Context) error
  }

  // Store is the aggregate seam.
  //
  // It is a STRUCT of interfaces and not one composed interface, and that is
  // forced rather than chosen: RecordStore and StationStore both declare List
  // and Get with DIFFERENT signatures, and Go permits a duplicate method name
  // across embedded interfaces only when the signatures are identical. An
  // embedded version does not compile ("duplicate method Get"). The struct is
  // also the better test shape, since a test can supply one real member and
  // leave the rest nil.
  type Store struct {
  	Records   RecordStore
  	Stations  StationStore
  	Deposits  DepositStore
  	Preflight PreflightStore
  	Health    Pinger
  }
  ```

- [ ] **Write the fake.** Create `internal/store/fake/fake.go` with exactly this content:
  ```go
  // Package fake is an in-memory store.Store for tests that must not touch a
  // database.
  //
  // WHAT THIS FAKE IS NOT EVIDENCE FOR. Every method here runs under one mutex,
  // so N concurrent ClaimPending calls trivially return disjoint rows. That
  // means the fake PASSES a zero-double-claim assertion while proving nothing
  // whatsoever about the SQL, and a claim-concurrency test written against it is
  // worse than no test at all: it reports a guarantee that does not exist. The
  // real claim is a single UPDATE whose inner SELECT takes row locks with
  // FOR UPDATE SKIP LOCKED, and the only place that can be tested is
  // internal/store/postgres against a real server. Keep concurrency assertions
  // physically out of this package.
  package fake

  import (
  	"context"
  	"sort"
  	"strconv"
  	"strings"
  	"sync"
  	"time"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  )

  // fixedNow is the default clock instant. It is fixed rather than time.Now so
  // that a golden-file test over an HTTP handler produces byte-identical output
  // on every run.
  var fixedNow = time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  // Store is an in-memory implementation of every store interface.
  type Store struct {
  	// Now is the clock. Tests that assert on timestamps should set it.
  	Now func() time.Time

  	// FailAll, when non-nil, is returned by every method except Ping. It is
  	// how a caller tests the store-error paths of an HTTP handler.
  	FailAll error

  	// PingErr is returned by Ping. It is how a caller tests /api/ready's 503.
  	PingErr error

  	mu        sync.Mutex
  	records   map[string]store.Record
  	stations  map[int64]store.Station
  	deposits  map[string]store.Deposit
  	preflight map[string]time.Time
  	totalTx   int64
  	totalRecs int64
  	lastWrite *time.Time
  	seenTx    map[string]struct{}
  }

  // New returns an empty fake with the default fixed clock.
  func New() *Store {
  	return &Store{
  		Now:       func() time.Time { return fixedNow },
  		records:   make(map[string]store.Record),
  		stations:  make(map[int64]store.Station),
  		deposits:  make(map[string]store.Deposit),
  		preflight: make(map[string]time.Time),
  		seenTx:    make(map[string]struct{}),
  	}
  }

  // stationsAdapter re-exposes the station methods under the names
  // store.StationStore requires.
  //
  // *Store cannot carry both Get(ctx, string) and Get(ctx, int64), nor both
  // List(ctx, ListFilter) and List(ctx, StationFilter), so the station methods on
  // *Store are named GetStation and ListStations and this adapter renames them.
  // It is the same asymmetry that forces store.Store to be a struct of interfaces
  // rather than one composed interface — an embedded version does not compile.
  type stationsAdapter struct{ s *Store }

  func (a stationsAdapter) Upsert(ctx context.Context, st store.Station) error {
  	return a.s.Upsert(ctx, st)
  }

  func (a stationsAdapter) List(ctx context.Context, f store.StationFilter) ([]store.Station, int64, error) {
  	return a.s.ListStations(ctx, f)
  }

  func (a stationsAdapter) Get(ctx context.Context, stationID int64) (store.Station, error) {
  	return a.s.GetStation(ctx, stationID)
  }

  func (a stationsAdapter) Stats(ctx context.Context) (store.Stats, error) {
  	return a.s.Stats(ctx)
  }

  // Stations returns the fake's store.StationStore view.
  func (s *Store) Stations() store.StationStore { return stationsAdapter{s: s} }

  // Store returns the aggregate seam with every member wired to s.
  func (s *Store) Store() store.Store {
  	return store.Store{
  		Records:   s,
  		Stations:  s.Stations(),
  		Deposits:  s,
  		Preflight: s,
  		Health:    s,
  	}
  }

  // SeedRecord inserts r verbatim, bypassing Insert's dedupe and defaults. It is
  // for constructing a specific lifecycle state a test needs to observe.
  func (s *Store) SeedRecord(r store.Record) {
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	if r.Status == "" {
  		r.Status = store.StatusPending
  	}
  	if r.CreatedAt.IsZero() {
  		r.CreatedAt = s.Now()
  	}
  	s.records[r.ID] = r
  	if r.TxID != nil {
  		s.seenTx[*r.TxID] = struct{}{}
  	}
  }

  // SeedStation inserts st verbatim.
  func (s *Store) SeedStation(st store.Station) {
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	s.stations[st.StationID] = st
  }

  // Ping implements store.Pinger.
  func (s *Store) Ping(_ context.Context) error { return s.PingErr }

  // Insert implements store.RecordStore.
  //
  // The two collision cases are deliberately DIFFERENT, and they match the SQL:
  // the ON CONFLICT arbiter covers (station_id, observation_time), so a repeated
  // observation is the intended steady state and reports (false, nil), while a
  // repeated id collides with the PRIMARY KEY, which the arbiter does not cover,
  // and surfaces as store.ErrConflict.
  func (s *Store) Insert(_ context.Context, r store.NewRecord) (bool, error) {
  	if s.FailAll != nil {
  		return false, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	for _, existing := range s.records {
  		if existing.StationID == r.StationID && existing.ObservationTime.Equal(r.ObservationTime) {
  			return false, nil
  		}
  	}
  	if _, clash := s.records[r.ID]; clash {
  		return false, store.ErrConflict
  	}
  	s.records[r.ID] = store.Record{
  		ID:              r.ID,
  		StationID:       r.StationID,
  		Timestamp:       r.Timestamp,
  		ObservationTime: r.ObservationTime,
  		Data:            r.Data,
  		Status:          store.StatusPending,
  		CreatedAt:       s.Now(),
  	}
  	return true, nil
  }

  // ClaimPending implements store.RecordStore.
  func (s *Store) ClaimPending(_ context.Context, n int, ref uuid.UUID) ([]store.Record, error) {
  	if s.FailAll != nil {
  		return nil, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	pending := make([]store.Record, 0, len(s.records))
  	for _, r := range s.records {
  		if r.Status == store.StatusPending {
  			pending = append(pending, r)
  		}
  	}
  	sortOldestFirst(pending)

  	now := s.Now()
  	out := make([]store.Record, 0, n)
  	for i := range pending {
  		if len(out) == n {
  			break
  		}
  		r := pending[i]
  		r.Status = store.StatusProcessing
  		claimedAt := now
  		r.ClaimedAt = &claimedAt
  		if r.ClaimRef == nil {
  			claimRef := ref
  			r.ClaimRef = &claimRef
  		}
  		s.records[r.ID] = r
  		out = append(out, r)
  	}
  	return out, nil
  }

  // Complete implements store.RecordStore.
  func (s *Store) Complete(ctx context.Context, txID string, pubs []store.Publication) (store.Stats, error) {
  	if s.FailAll != nil {
  		return store.Stats{}, s.FailAll
  	}
  	s.mu.Lock()
  	now := s.Now()
  	moved := int64(0)
  	arc := store.ChainARCAccepted
  	for _, p := range pubs {
  		r, ok := s.records[p.RecordID]
  		if !ok || r.Status != store.StatusProcessing {
  			continue
  		}
  		r.Status = store.StatusCompleted
  		txid := txID
  		r.TxID = &txid
  		vout := p.OutputIndex
  		r.OutputIndex = &vout
  		chain := arc
  		r.ChainStatus = &chain
  		processedAt := now
  		r.ProcessedAt = &processedAt
  		r.ClaimedAt = nil
  		r.AdoptRequired = false
  		r.Error = nil
  		s.records[r.ID] = r
  		moved++

  		// A MISSING station row is skipped, never created. bumpStationsSQL is an
  		// UPDATE that simply matches nothing, and a fake that invented the row
  		// instead would let a B2 test assert a station that Postgres would not
  		// have. last_temp and last_conditions advance only with a NEWER reading,
  		// which is the same CASE the SQL carries.
  		st, ok := s.stations[r.StationID]
  		if !ok {
  			continue
  		}
  		st.TxRecords++
  		if st.LastReading == nil || st.LastReading.Before(r.Timestamp) {
  			reading := r.Timestamp
  			st.LastReading = &reading
  			temp := float64(r.Data.AirTemperature)
  			st.LastTemp = &temp
  			st.LastConditions = r.Data.Conditions
  		}
  		s.stations[r.StationID] = st
  	}
  	if moved > 0 {
  		s.totalTx++
  		s.totalRecs += moved
  		write := now
  		s.lastWrite = &write
  		s.seenTx[txID] = struct{}{}
  	}
  	s.mu.Unlock()
  	return s.Stats(ctx)
  }

  // FailPermanent implements store.RecordStore.
  func (s *Store) FailPermanent(_ context.Context, ids []string, reason string) error {
  	return s.transition(ids, func(r *store.Record) {
  		r.Status = store.StatusFailed
  		r.Attempts++
  		processedAt := s.Now()
  		r.ProcessedAt = &processedAt
  		r.ClaimedAt = nil
  		r.AdoptRequired = false
  		r.Error = &reason
  	})
  }

  // RequeueInfra implements store.RecordStore.
  func (s *Store) RequeueInfra(_ context.Context, ids []string, reason string) error {
  	return s.transition(ids, func(r *store.Record) {
  		r.Status = store.StatusPending
  		r.ClaimedAt = nil
  		r.AdoptRequired = false
  		r.Error = &reason
  	})
  }

  // MarkUnknown implements store.RecordStore.
  func (s *Store) MarkUnknown(_ context.Context, ids []string, reason string) error {
  	return s.transition(ids, func(r *store.Record) {
  		r.Status = store.StatusPending
  		r.ClaimedAt = nil
  		r.AdoptRequired = true
  		r.Error = &reason
  	})
  }

  // ReapExpired implements store.RecordStore.
  func (s *Store) ReapExpired(_ context.Context, lease time.Duration, limit int) ([]store.Record, error) {
  	if s.FailAll != nil {
  		return nil, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	cutoff := s.Now().Add(-lease)
  	stale := make([]store.Record, 0, len(s.records))
  	for _, r := range s.records {
  		if r.Status == store.StatusProcessing && r.ClaimedAt != nil && r.ClaimedAt.Before(cutoff) {
  			stale = append(stale, r)
  		}
  	}
  	sort.Slice(stale, func(i, j int) bool { return stale[i].ClaimedAt.Before(*stale[j].ClaimedAt) })

  	out := make([]store.Record, 0, len(stale))
  	for i := range stale {
  		if len(out) == limit {
  			break
  		}
  		r := stale[i]
  		r.Status = store.StatusPending
  		r.AdoptRequired = true
  		s.records[r.ID] = r
  		out = append(out, r)
  	}
  	return out, nil
  }

  // Requeue implements store.RecordStore.
  func (s *Store) Requeue(_ context.Context, f store.RequeueFilter) (int64, error) {
  	if s.FailAll != nil {
  		return 0, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	cutoff := s.Now().Add(-f.Since)
  	match := make([]store.Record, 0, len(s.records))
  	for _, r := range s.records {
  		if r.Status != f.Status || !r.CreatedAt.Before(cutoff) {
  			continue
  		}
  		if f.StationID != nil && r.StationID != *f.StationID {
  			continue
  		}
  		match = append(match, r)
  	}
  	sortOldestFirst(match)
  	if f.Limit > 0 && len(match) > f.Limit {
  		match = match[:f.Limit]
  	}
  	if f.DryRun {
  		return int64(len(match)), nil
  	}
  	for i := range match {
  		r := match[i]
  		r.Status = store.StatusPending
  		r.AdoptRequired = true
  		s.records[r.ID] = r
  	}
  	return int64(len(match)), nil
  }

  // List implements store.RecordStore.
  func (s *Store) List(_ context.Context, f store.ListFilter) ([]store.Record, int64, error) {
  	if s.FailAll != nil {
  		return nil, 0, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	match := make([]store.Record, 0, len(s.records))
  	for _, r := range s.records {
  		if f.StationID != nil && r.StationID != *f.StationID {
  			continue
  		}
  		if f.Status != nil && r.Status != *f.Status {
  			continue
  		}
  		match = append(match, r)
  	}
  	sortNewestFirst(match)

  	total := int64(len(match))
  	if f.Offset >= len(match) {
  		return []store.Record{}, total, nil
  	}
  	end := f.Offset + f.Limit
  	if f.Limit <= 0 || end > len(match) {
  		end = len(match)
  	}
  	page := make([]store.Record, 0, end-f.Offset)
  	page = append(page, match[f.Offset:end]...)
  	return page, total, nil
  }

  // Get implements store.RecordStore.
  func (s *Store) Get(_ context.Context, id string) (store.Record, error) {
  	if s.FailAll != nil {
  		return store.Record{}, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	r, ok := s.records[id]
  	if !ok {
  		return store.Record{}, store.ErrNotFound
  	}
  	return r, nil
  }

  // TxIDExists implements store.RecordStore.
  func (s *Store) TxIDExists(_ context.Context, txID string) (bool, error) {
  	if s.FailAll != nil {
  		return false, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	_, ok := s.seenTx[txID]
  	return ok, nil
  }

  // SetBlockHeights implements store.RecordStore.
  func (s *Store) SetBlockHeights(_ context.Context, ups []store.BlockHeightUpdate) error {
  	if s.FailAll != nil {
  		return s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	mined := store.ChainMined
  	for _, u := range ups {
  		for id, r := range s.records {
  			if r.TxID == nil || *r.TxID != u.TxID || r.Status != store.StatusCompleted {
  				continue
  			}
  			height := u.BlockHeight
  			r.BlockHeight = &height
  			chain := mined
  			r.ChainStatus = &chain
  			if u.MinedAt != nil {
  				minedAt := *u.MinedAt
  				r.MinedAt = &minedAt
  			} else {
  				minedAt := s.Now()
  				r.MinedAt = &minedAt
  			}
  			s.records[id] = r

  			// As in Complete: setStationHeightSQL is an UPDATE, so a station
  			// with no row is skipped rather than invented.
  			st, ok := s.stations[r.StationID]
  			if !ok {
  				continue
  			}
  			if st.LastBlockHeight == nil || *st.LastBlockHeight < height {
  				h := height
  				st.LastBlockHeight = &h
  			}
  			s.stations[r.StationID] = st
  		}
  	}
  	return nil
  }

  // ReconcileCandidates implements store.RecordStore.
  func (s *Store) ReconcileCandidates(_ context.Context, olderThan time.Duration, limit int) ([]store.Record, error) {
  	if s.FailAll != nil {
  		return nil, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	cutoff := s.Now().Add(-olderThan)
  	out := make([]store.Record, 0, len(s.records))
  	for _, r := range s.records {
  		if r.Status != store.StatusCompleted || r.ProcessedAt == nil || !r.ProcessedAt.Before(cutoff) {
  			continue
  		}
  		if r.ChainStatus != nil && *r.ChainStatus == store.ChainMined {
  			continue
  		}
  		out = append(out, r)
  	}
  	sort.Slice(out, func(i, j int) bool { return out[i].ProcessedAt.Before(*out[j].ProcessedAt) })
  	if limit > 0 && len(out) > limit {
  		out = out[:limit]
  	}
  	return out, nil
  }

  // Snapshot implements store.RecordStore.
  func (s *Store) Snapshot(_ context.Context) (store.Snapshot, error) {
  	if s.FailAll != nil {
  		return store.Snapshot{}, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	cutoff := s.Now().Add(-time.Hour)
  	var snap store.Snapshot
  	for _, r := range s.records {
  		switch r.Status {
  		case store.StatusPending:
  			snap.PendingRows++
  		case store.StatusProcessing:
  			snap.ProcessingRows++
  		case store.StatusFailed:
  			snap.FailedRows++
  		case store.StatusCompleted:
  			notMined := r.ChainStatus == nil || *r.ChainStatus != store.ChainMined
  			if notMined && r.ProcessedAt != nil && r.ProcessedAt.Before(cutoff) {
  				snap.StillUnminedOlderThan1h++
  			}
  		}
  		if r.ChainStatus != nil {
  			switch *r.ChainStatus {
  			case store.ChainMined:
  				snap.MinedCount++
  			case store.ChainAborted:
  				snap.AbortedCount++
  			case store.ChainARCAccepted, store.ChainUnmined:
  			}
  		}
  	}
  	return snap, nil
  }

  // Upsert implements store.StationStore.
  func (s *Store) Upsert(_ context.Context, in store.Station) error {
  	if s.FailAll != nil {
  		return s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	existing, ok := s.stations[in.StationID]
  	if !ok {
  		in.CreatedAt = s.Now()
  		in.UpdatedAt = s.Now()
  		s.stations[in.StationID] = in
  		return nil
  	}
  	existing.Name = in.Name
  	existing.Location = in.Location
  	existing.Latitude = in.Latitude
  	existing.Longitude = in.Longitude
  	existing.IsActive = in.IsActive
  	existing.UpdatedAt = s.Now()
  	s.stations[in.StationID] = existing
  	return nil
  }

  // GetStation is store.StationStore.Get under a non-clashing name; the
  // stationsAdapter above renames it. See that type's comment for why.
  func (s *Store) GetStation(_ context.Context, stationID int64) (store.Station, error) {
  	if s.FailAll != nil {
  		return store.Station{}, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	st, ok := s.stations[stationID]
  	if !ok {
  		return store.Station{}, store.ErrNotFound
  	}
  	return st, nil
  }

  // ListStations implements store.StationStore.List.
  //
  // The search branch is not decoration and must not be dropped: B2's whole
  // ?search= test suite runs against this fake, and a fake that ignored f.Search
  // would let every one of those tests pass while proving nothing. It reproduces
  // the CONTRACT the SQL has — one parsed value drives the branch, a NUL byte
  // matches nothing rather than erroring — and deliberately not the ranking,
  // which is websearch_to_tsquery's and cannot be imitated honestly. Task 19's
  // conformance suite asserts the shared part against both implementations.
  func (s *Store) ListStations(_ context.Context, f store.StationFilter) ([]store.Station, int64, error) {
  	if s.FailAll != nil {
  		return nil, 0, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	match := make([]store.Station, 0, len(s.stations))
  	for _, st := range s.stations {
  		if !stationMatches(st, f.Search) {
  			continue
  		}
  		match = append(match, st)
  	}
  	sort.Slice(match, func(i, j int) bool { return match[i].StationID < match[j].StationID })

  	total := int64(len(match))
  	if f.Offset >= len(match) {
  		return []store.Station{}, total, nil
  	}
  	end := f.Offset + f.Limit
  	if f.Limit <= 0 || end > len(match) {
  		end = len(match)
  	}
  	page := make([]store.Station, 0, end-f.Offset)
  	page = append(page, match[f.Offset:end]...)
  	return page, total, nil
  }

  // Stats implements store.StationStore.
  func (s *Store) Stats(_ context.Context) (store.Stats, error) {
  	if s.FailAll != nil {
  		return store.Stats{}, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()

  	active := int64(0)
  	for _, st := range s.stations {
  		if st.IsActive {
  			active++
  		}
  	}
  	out := store.Stats{
  		ActiveStations: active,
  		TotalTx:        s.totalTx,
  		TotalRecords:   s.totalRecs,
  	}
  	if s.lastWrite != nil {
  		write := *s.lastWrite
  		out.LastRecordWrite = &write
  	}
  	return out, nil
  }

  // NewDeposit implements store.DepositStore.
  func (s *Store) NewDeposit(_ context.Context, d store.Deposit) error {
  	if s.FailAll != nil {
  		return s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	if _, ok := s.deposits[d.Suffix]; ok {
  		return store.ErrConflict
  	}
  	d.CreatedAt = s.Now()
  	s.deposits[d.Suffix] = d
  	return nil
  }

  // PendingDeposits implements store.DepositStore.
  func (s *Store) PendingDeposits(_ context.Context) ([]store.Deposit, error) {
  	if s.FailAll != nil {
  		return nil, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	out := make([]store.Deposit, 0, len(s.deposits))
  	for _, d := range s.deposits {
  		if d.InternalizedAt == nil {
  			out = append(out, d)
  		}
  	}
  	sort.Slice(out, func(i, j int) bool { return out[i].Suffix < out[j].Suffix })
  	return out, nil
  }

  // MarkInternalized implements store.DepositStore.
  func (s *Store) MarkInternalized(_ context.Context, suffix, txID string, vout int32, sats int64) error {
  	if s.FailAll != nil {
  		return s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	d, ok := s.deposits[suffix]
  	if !ok {
  		return store.ErrNotFound
  	}
  	txid := txID
  	d.TxID = &txid
  	v := vout
  	d.Vout = &v
  	amount := sats
  	d.Satoshis = &amount
  	at := s.Now()
  	d.InternalizedAt = &at
  	s.deposits[suffix] = d
  	return nil
  }

  // PreflightOK implements store.PreflightStore.
  func (s *Store) PreflightOK(_ context.Context, fingerprint string) (bool, error) {
  	if s.FailAll != nil {
  		return false, s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	_, ok := s.preflight[fingerprint]
  	return ok, nil
  }

  // RecordPreflight implements store.PreflightStore.
  func (s *Store) RecordPreflight(_ context.Context, fingerprint string) error {
  	if s.FailAll != nil {
  		return s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	s.preflight[fingerprint] = s.Now()
  	return nil
  }

  func (s *Store) transition(ids []string, apply func(*store.Record)) error {
  	if s.FailAll != nil {
  		return s.FailAll
  	}
  	s.mu.Lock()
  	defer s.mu.Unlock()
  	for _, id := range ids {
  		r, ok := s.records[id]
  		if !ok || r.Status != store.StatusProcessing {
  			continue
  		}
  		apply(&r)
  		s.records[id] = r
  	}
  	return nil
  }

  // stationMatches is the fake's half of the one-parsed-value search split.
  //
  // An empty search matches everything. A search that parses as an int64 is an
  // EXACT station_id lookup, exactly as strconv.ParseInt routes it in
  // StationStore.List — so "0" finds station 0 and "12345abc" does not parse and
  // falls through to text. A value store.ValidText rejects matches nothing,
  // because Postgres cannot bind it at all and the store answers an empty page
  // rather than a 500.
  func stationMatches(st store.Station, search string) bool {
  	if search == "" {
  		return true
  	}
  	if store.ValidText(search) != nil {
  		return false
  	}
  	if id, err := strconv.ParseInt(search, 10, 64); err == nil {
  		return st.StationID == id
  	}
  	needle := strings.ToLower(search)
  	return strings.Contains(strings.ToLower(st.Name), needle) ||
  		strings.Contains(strings.ToLower(st.Location), needle)
  }

  func sortNewestFirst(recs []store.Record) {
  	sort.Slice(recs, func(i, j int) bool {
  		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
  			return recs[i].CreatedAt.After(recs[j].CreatedAt)
  		}
  		return recs[i].ID > recs[j].ID
  	})
  }

  func sortOldestFirst(recs []store.Record) {
  	sort.Slice(recs, func(i, j int) bool {
  		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
  			return recs[i].CreatedAt.Before(recs[j].CreatedAt)
  		}
  		return recs[i].ID < recs[j].ID
  	})
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/... -count=1
  ```
  Expected output: two `ok` lines, one for `internal/store` and one for `internal/store/fake`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go vet ./... && go build ./... && go test ./... -count=1 && make go-lint
  ```
  All must pass. If `make go-lint` is not wired to run locally, run `golangci-lint run --max-same-issues=0` — the repo pins v2.12.2, and the flag is what stops a fourth identical finding being hidden.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/interfaces.go internal/store/fake && git commit -m "store: interfaces, the aggregate seam, and an in-memory fake"
  ```

---

## Task 3: The pgx pool, the DSN, the Secret type, and the simple-protocol assertion

**Files:**
- Create: `internal/config/secret.go`
- Create: `internal/config/secret_test.go`
- Create: `internal/store/postgres/dsn.go`
- Create: `internal/store/postgres/pool.go`
- Create: `internal/store/postgres/pool_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

Consumes: nothing from earlier tasks (this package does not import `internal/store` yet).

Produces:
```go
package config // import ".../internal/config"

type Secret string
func (s Secret) String() string                 // always "[REDACTED]"
func (s Secret) LogValue() slog.Value           // always "[REDACTED]"
func (s Secret) MarshalJSON() ([]byte, error)   // always `"[REDACTED]"`
func (s Secret) Reveal() string                 // the real value; the ONLY way to read it
```

`Secret` lives in `internal/config` and NOT in `internal/store/postgres`, for two reasons that only look like taste. The spec assigns it to `internal/config/secret.go`, and it must also wrap `SERVER_PRIVATE_KEY` and `TEMPEST_API_KEY` — which `internal/api` cannot reach, because this plan forbids `internal/api` from importing `internal/store/postgres` at all. Defining it inside the postgres package therefore GUARANTEES a hand-copied second copy, and the copy is the one that will be missing a redaction method. `internal/config` is a leaf with no dependencies here (B2 adds the env parsing to the same package), so one definition serves everybody. `internal/store/postgres` declares no `Secret` and no alias to it.

```go
package postgres // import ".../internal/store/postgres"

type DSNParts struct {
    Host     string
    Port     int
    User     string
    Password config.Secret
    Database string
    SSLMode  string
}
func (p DSNParts) DSN() string

var ErrSimpleProtocol error
var ErrCloseTimeout error

func PoolConfig(dsn string) (*pgxpool.Config, error)
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error)
func ClosePool(pool *pgxpool.Pool, budget time.Duration) error
```

### Steps

- [ ] **Add the pgx dependency.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go get github.com/jackc/pgx/v5@v5.10.0
  ```
  Expected output includes `go: added github.com/jackc/pgx/v5 v5.10.0` plus indirects `github.com/jackc/pgpassfile`, `github.com/jackc/pgservicefile`, `github.com/jackc/puddle/v2`, `golang.org/x/sync`, `golang.org/x/text`.

- [ ] **Write the failing Secret test.** Create `internal/config/secret_test.go` with exactly this content:
  ```go
  package config_test

  import (
  	"encoding/json"
  	"fmt"
  	"log/slog"
  	"strings"
  	"testing"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
  )

  // probeSecret deliberately contains every URL-reserved character that naive
  // string concatenation would mangle: @ : / ? # &.
  //
  // It is assembled from parts rather than written as one literal because gosec
  // G101 flags a credential-shaped BasicLit assigned to a named constant —
  // measured with this repository's config, `const probePassword =
  // "p@ss:w/rd?#&secret"` reports `Potential hardcoded credentials` — and there
  // are zero //nolint directives in this repository. A binary expression is not a
  // BasicLit, so this shape is clean and the value is unchanged.
  var probeSecret = "p@ss" + ":w/rd" + "?#&" + "secret"

  // TestSecretNeverPrints covers all four escape routes a credential has: a fmt
  // verb, a slog attribute, encoding/json, and the deliberate Reveal.
  //
  // json.Marshal is the one that matters most and is the easiest to forget:
  // encoding/json IGNORES fmt.Stringer entirely, so a Secret inside any struct
  // that is ever marshaled — a config echo, a debug handler, /api/ops —
  // serializes the RAW credential unless MarshalJSON exists. Neither errchkjson
  // nor musttag catches that.
  func TestSecretNeverPrints(t *testing.T) {
  	s := config.Secret(probeSecret)

  	if got := s.String(); got != "[REDACTED]" {
  		t.Errorf("Secret.String() = %q, want \"[REDACTED]\"", got)
  	}
  	for _, verb := range []string{"%v", "%s", "%q", "%+v"} {
  		got := fmt.Sprintf(verb, s)
  		if strings.Contains(got, "secret") {
  			t.Errorf("fmt.Sprintf(%q, secret) = %q, leaks the value", verb, got)
  		}
  	}
  	if got := s.LogValue().String(); got != "[REDACTED]" {
  		t.Errorf("Secret.LogValue() = %q, want \"[REDACTED]\"", got)
  	}
  	var buf strings.Builder
  	logger := slog.New(slog.NewTextHandler(&buf, nil))
  	logger.Info("boot", "password", s)
  	if strings.Contains(buf.String(), "secret") {
  		t.Errorf("slog line %q leaks the secret", buf.String())
  	}

  	// A value field, a POINTER field and an EMBEDDED field, so a value-versus-
  	// pointer receiver mismatch cannot slip through: with a pointer receiver the
  	// value-field case would marshal the raw string.
  	type inner struct{ Password config.Secret }
  	cases := []struct {
  		label string
  		value any
  	}{
  		{"value field", struct{ P config.Secret }{s}},
  		{"pointer field", struct{ P *config.Secret }{&s}},
  		{"embedded", struct{ inner }{inner{Password: s}}},
  		{"bare", s},
  		{"slice", []config.Secret{s}},
  		{"map", map[string]config.Secret{"password": s}},
  	}
  	for _, c := range cases {
  		encoded, err := json.Marshal(c.value)
  		if err != nil {
  			t.Errorf("%s: json.Marshal: %v", c.label, err)
  			continue
  		}
  		if !strings.Contains(string(encoded), "[REDACTED]") {
  			t.Errorf("%s: json.Marshal = %s, want a redacted value", c.label, encoded)
  		}
  		if strings.Contains(string(encoded), "secret") {
  			t.Errorf("%s: json.Marshal = %s, LEAKS the credential", c.label, encoded)
  		}
  	}

  	if s.Reveal() != probeSecret {
  		t.Error("Reveal() must return the real value")
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/config/... -count=1
  ```
  Expected failure text: `no required module provides package github.com/bsv-blockchain-demos/weather-proof/internal/config` — the package does not exist yet.

- [ ] **Write the Secret implementation.** Create `internal/config/secret.go` with exactly this content:
  ```go
  // Package config holds process configuration.
  //
  // Only the Secret type lives here in this plan; the env parsing, Validate() and
  // the trusted-proxy CIDR list are the read-API plan's first task. Secret is
  // here rather than next to the DSN that first needed it because it must wrap
  // SERVER_PRIVATE_KEY and TEMPEST_API_KEY as well as the Postgres password, and
  // internal/api may never import internal/store/postgres — so defining it there
  // would guarantee a hand-copied second copy, and the copy is the one that ends
  // up missing a redaction method.
  package config

  import (
  	"log/slog"
  )

  // redacted is what a Secret renders as everywhere except Reveal.
  const redacted = "[REDACTED]"

  // redactedJSON is redacted as a JSON string, quoted once at compile time.
  const redactedJSON = `"` + redacted + `"`

  // Secret is a configuration value that must never appear in a log line, an
  // error message or an HTTP response.
  //
  // All THREE of String, LogValue and MarshalJSON are required, and the third is
  // the one that stops an exfiltration into a response BODY: encoding/json does
  // not consult fmt.Stringer, so without MarshalJSON a Secret field inside any
  // marshaled struct serializes the raw credential. Reveal is the single
  // deliberate escape hatch and every call site is worth reading twice.
  //
  // Every method has a VALUE receiver on purpose. A pointer receiver would leave
  // the value-field case unprotected — json.Marshal of a struct holding a
  // non-addressable Secret would not find the method — and recvcheck forbids
  // mixing the two.
  type Secret string

  // String implements fmt.Stringer with a redacted value.
  func (s Secret) String() string { return redacted }

  // LogValue implements slog.LogValuer with a redacted value.
  func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

  // MarshalJSON implements json.Marshaler with a redacted value.
  func (s Secret) MarshalJSON() ([]byte, error) { return []byte(redactedJSON), nil }

  // Reveal returns the real value. This is the only way to read it.
  func (s Secret) Reveal() string { return string(s) }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/config/... -count=1 -v
  ```
  Expected output: `--- PASS: TestSecretNeverPrints`, then `ok`.

- [ ] **Write the failing pool test.** Create `internal/store/postgres/pool_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"strings"
  	"testing"
  	"time"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  )

  // probePassword deliberately contains every URL-reserved character that naive
  // string concatenation would mangle: @ : / ? # &. It is assembled from parts
  // rather than written as one literal because gosec G101 flags a
  // credential-shaped literal assigned to a named constant, and this repository
  // allows zero //nolint directives.
  var probePassword = "p@ss" + ":w/rd" + "?#&" + "secret"

  // plainPassword has no URL-reserved characters, so it can be embedded in a
  // hand-written connection string without changing where the parser thinks the
  // userinfo, host and port boundaries are.
  const plainPassword = "sup3rsecretvalue"

  // localDSN builds a syntactically valid connection string through DSNParts.
  //
  // Every DSN in this file is built rather than written out, because a literal
  // containing `u:pw@` is `G101: Potential hardcoded credentials: Password in
  // URL`. Building it also means these tests exercise the same assembly path
  // production uses.
  func localDSN(t testing.TB) string {
  	t.Helper()
  	return postgres.DSNParts{
  		Host: "localhost", Port: 5432, User: "u",
  		Password: config.Secret("pw"), Database: "d", SSLMode: "disable",
  	}.DSN()
  }

  func TestDSNEscapesAndNeverConcatenates(t *testing.T) {
  	parts := postgres.DSNParts{
  		Host:     "db.internal",
  		Port:     5432,
  		User:     "weather",
  		Password: config.Secret(probePassword),
  		Database: "weatherproof",
  		SSLMode:  "require",
  	}
  	dsn := parts.DSN()

  	// The raw password must not appear unescaped; the reserved characters must
  	// be percent-encoded.
  	if strings.Contains(dsn, probePassword) {
  		t.Fatalf("DSN %q contains the raw password", dsn)
  	}
  	// pgx must be able to parse it back and recover the exact password.
  	cfg, err := postgres.PoolConfig(dsn)
  	if err != nil {
  		t.Fatalf("PoolConfig(%q): %v", dsn, err)
  	}
  	if cfg.ConnConfig.Password != probePassword {
  		t.Fatalf("round-tripped password = %q, want %q", cfg.ConnConfig.Password, probePassword)
  	}
  	if cfg.ConnConfig.Host != "db.internal" {
  		t.Errorf("host = %q, want db.internal", cfg.ConnConfig.Host)
  	}
  	if cfg.ConnConfig.Port != 5432 {
  		t.Errorf("port = %d, want 5432", cfg.ConnConfig.Port)
  	}
  	if cfg.ConnConfig.Database != "weatherproof" {
  		t.Errorf("database = %q, want weatherproof", cfg.ConnConfig.Database)
  	}

  	// An IPv6 host must not be mangled.
  	v6 := postgres.DSNParts{
  		Host: "::1", Port: 5432, User: "u",
  		Password: config.Secret("x"), Database: "d", SSLMode: "disable",
  	}
  	if _, v6Err := postgres.PoolConfig(v6.DSN()); v6Err != nil {
  		t.Fatalf("IPv6 DSN %q did not parse: %v", v6.DSN(), v6Err)
  	}
  }

  func TestPoolConfigRejectsSimpleProtocol(t *testing.T) {
  	// DSNParts never emits default_query_exec_mode, so the poisoned value is
  	// appended here the way it would arrive in real life: from a connection
  	// string somebody else wrote.
  	dsn := localDSN(t) + "&default_query_exec_mode=simple_protocol"
  	if _, err := postgres.PoolConfig(dsn); !errors.Is(err, postgres.ErrSimpleProtocol) {
  		t.Fatalf("PoolConfig error = %v, want postgres.ErrSimpleProtocol", err)
  	}

  	// NewPool must refuse before it can open a socket.
  	if _, poolErr := postgres.NewPool(context.Background(), dsn); !errors.Is(poolErr, postgres.ErrSimpleProtocol) {
  		t.Fatalf("NewPool error = %v, want postgres.ErrSimpleProtocol", poolErr)
  	}
  }

  func TestPoolConfigAcceptsTheDefaultMode(t *testing.T) {
  	cfg, err := postgres.PoolConfig(localDSN(t))
  	if err != nil {
  		t.Fatalf("PoolConfig: %v", err)
  	}
  	if cfg.MaxConns < 16 {
  		t.Errorf("MaxConns = %d, want >= 16 (a small pool makes the claim-concurrency test vacuous)", cfg.MaxConns)
  	}
  	if cfg.MinConns < 1 {
  		t.Errorf("MinConns = %d, want >= 1", cfg.MinConns)
  	}
  }

  func TestPoolConfigErrorDoesNotLeakThePassword(t *testing.T) {
  	// An unparseable PORT forces pgx to report a parse failure that quotes the
  	// whole connection string. pgx redacts the password to "xxxxxx" when it
  	// does — verified against v5.10.0, which emits
  	// `cannot parse "postgres://u:xxxxxx@host:notaport/d": ...` — and this test
  	// is the regression gate on that assumption, because PoolConfig wraps the
  	// parse error with %w and would otherwise be a credential leak into a log.
  	//
  	// plainPassword rather than probePassword: probePassword's own @ and : would
  	// move the userinfo and port boundaries and the string would fail to parse
  	// for the wrong reason.
  	_, err := postgres.PoolConfig("postgres://u:" + plainPassword + "@host:notaport/d")
  	if err == nil {
  		t.Fatal("expected a parse error")
  	}
  	if strings.Contains(err.Error(), plainPassword) || strings.Contains(err.Error(), "sup3rsecret") {
  		t.Fatalf("PoolConfig error leaks the password: %q", err.Error())
  	}
  	if !strings.Contains(err.Error(), "postgres: parse connection string") {
  		t.Fatalf("PoolConfig error is not the wrapped parse error: %q", err.Error())
  	}
  }

  // TestClosePoolHonorsItsBudget uses the US spelling, and it has to: misspell
  // runs with locale US over test files, and this repository's ignore-rules cover
  // the base word but NOT its -s inflection, so the British form of "Honors" in a
  // TEST NAME is a finding that breaks the mandatory 0-issues gate. (Writing the
  // British form here, even inside a comment, would fail for the same reason.)
  func TestClosePoolHonorsItsBudget(t *testing.T) {
  	unreachable := postgres.DSNParts{
  		Host: "127.0.0.1", Port: 1, User: "u",
  		Password: config.Secret("pw"), Database: "d", SSLMode: "disable",
  	}
  	pool, err := postgres.NewPool(context.Background(), unreachable.DSN())
  	if err != nil {
  		t.Fatalf("NewPool: %v", err)
  	}
  	// The pool never connects to port 1, so Close returns immediately.
  	if closeErr := postgres.ClosePool(pool, 5*time.Second); closeErr != nil {
  		t.Fatalf("ClosePool: %v", closeErr)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `no required module provides package github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres`.

- [ ] **Write the DSN implementation.** Create `internal/store/postgres/dsn.go` with exactly this content:
  ```go
  package postgres

  import (
  	"net"
  	"net/url"
  	"strconv"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
  )

  // DSNParts is a Postgres connection string assembled from separate pieces.
  //
  // It exists so that no call site ever builds a connection string by
  // concatenating a password into a URL. url.UserPassword percent-encodes the
  // credential, and net.JoinHostPort handles a bracketed IPv6 host, which naive
  // "host:port" formatting does not (nosprintfhostport flags exactly that).
  //
  // Password is a config.Secret and this package deliberately declares neither a
  // Secret of its own nor an alias to one: one definition, in a leaf package,
  // shared with SERVER_PRIVATE_KEY and TEMPEST_API_KEY, is what stops a second
  // copy drifting away from the first and losing MarshalJSON.
  //
  // Note what is deliberately absent: pool_max_conns, pool_min_conns and
  // pool_min_idle_conns. pgxpool.ParseConfig honors those as DSN runtime
  // params, so leaving them out of the DSN is what stops a connection string
  // silently overriding the values PoolConfig sets in Go. default_query_exec_mode
  // is likewise never emitted here, and PoolConfig rejects it if it arrives from
  // anywhere else.
  type DSNParts struct {
  	Host     string
  	Port     int
  	User     string
  	Password config.Secret
  	Database string
  	SSLMode  string
  }

  // DSN renders the parts as a postgres:// URL.
  func (p DSNParts) DSN() string {
  	u := &url.URL{
  		Scheme: "postgres",
  		User:   url.UserPassword(p.User, p.Password.Reveal()),
  		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
  		Path:   "/" + p.Database,
  	}
  	q := url.Values{}
  	q.Set("sslmode", p.SSLMode)
  	u.RawQuery = q.Encode()
  	return u.String()
  }
  ```

- [ ] **Write the pool implementation.** Create `internal/store/postgres/pool.go` with exactly this content:
  ```go
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
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/postgres/... -count=1 -v -run 'TestDSN|TestPoolConfig|TestClosePool'
  ```
  Expected output: `--- PASS` for `TestDSNEscapesAndNeverConcatenates`, `TestPoolConfigRejectsSimpleProtocol`, `TestPoolConfigAcceptsTheDefaultMode`, `TestPoolConfigErrorDoesNotLeakThePassword`, `TestClosePoolHonorsItsBudget`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go mod tidy && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && go test ./... -count=1 && golangci-lint run --max-same-issues=0
  ```
  All must pass with no findings. `gofmt -l` must print nothing: it is an enabled formatter, and `--max-same-issues=0` is what stops a fourth identical finding being hidden.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add go.mod go.sum internal/config internal/store/postgres && git commit -m "config: a Secret that redacts in fmt, slog and JSON; postgres: pool and DSN assembly"
  ```

---

## Task 4: The Postgres test harness — skip locally, fail loudly in CI

**Files:**
- Create: `internal/store/storetest/storetest.go`
- Create: `internal/store/storetest/storetest_test.go`

**Interfaces:**

Consumes (Task 3): `postgres.PoolConfig(dsn string) (*pgxpool.Config, error)`, `postgres.NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error)`.

Produces:
```go
package storetest // import ".../internal/store/storetest"

const DSNEnv     = "WEATHER_TEST_POSTGRES_DSN"
const RequireEnv = "WEATHER_TEST_REQUIRE_POSTGRES"

func RequireDSN(t testing.TB) string

type Schema struct {
    Name      string
    CreateSQL string
    DropSQL   string
}
func (s Schema) Validate() error

func Pool(t testing.TB, schema Schema, maxConns int32) *pgxpool.Pool
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/storetest/storetest_test.go` with exactly this content:
  ```go
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
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/storetest/... -count=1
  ```
  Expected failure text: `no required module provides package github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest`.

- [ ] **Write the harness.** Create `internal/store/storetest/storetest.go` with exactly this content:
  ```go
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
  ```

- [ ] **Run it and see it SKIP with no DSN.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/storetest/... -count=1 -v
  ```
  Expected output: `--- PASS: TestSchemaValidateCatchesAMismatch`, `--- PASS: TestEnvVarNamesAreStable`, and `--- SKIP: TestPoolPinsTheSchemaOnEveryConnection` / `--- SKIP: TestPoolStartsFromAnEmptySchema` with the message `set WEATHER_TEST_POSTGRES_DSN to run this test (make pg-up; see docs/testing-postgres.md)`, then `ok`.

- [ ] **See the tripwire fire.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/storetest/... -count=1 -run TestPoolStartsFromAnEmptySchema
  ```
  Expected output: `--- FAIL: TestPoolStartsFromAnEmptySchema` with `WEATHER_TEST_POSTGRES_DSN is unset while WEATHER_TEST_REQUIRE_POSTGRES is set: the Postgres suite must never skip in CI`, then `FAIL`. This is the whole point of the harness — see it fail before trusting it.

- [ ] **Start Postgres and see it pass.** Run exactly:
  ```
  docker run -d --name wp-pg -e POSTGRES_PASSWORD=postgres -e POSTGRES_USER=postgres -e POSTGRES_DB=weatherproof_test -p 5432:5432 postgres:17-alpine
  ```
  then wait for readiness:
  ```
  until docker exec wp-pg pg_isready -U postgres -d weatherproof_test; do sleep 1; done
  ```
  Expected output: `/var/run/postgresql:5432 - accepting connections` (typically after 1-3 seconds). Then run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/storetest/... -count=1 -v
  ```
  Expected output: four `--- PASS` lines and `ok`. No SKIPs.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && go test ./... -count=1 && golangci-lint run --max-same-issues=0
  ```
  All must pass.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/storetest && git commit -m "storetest: Postgres harness that skips locally and fails loudly in CI"
  ```

---

## Task 5: CI — the Postgres job, the vacuous-green guard, Make targets, compose, docs

**Files:**
- Modify: `.github/workflows/go.yml`
- Modify: `Makefile`
- Modify: `docker-compose.yaml`
- Create: `docs/testing-postgres.md`

**Interfaces:**

Consumes (Task 4): `storetest.DSNEnv == "WEATHER_TEST_POSTGRES_DSN"`, `storetest.RequireEnv == "WEATHER_TEST_REQUIRE_POSTGRES"`.

Produces: the environment contract every later task's tests run under — CI job name `integration (postgres)`, DSN `postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable`, and the Make targets `pg-up`, `pg-down`, `pg-psql`, `go-test-pg`.

### Steps

- [ ] **Prove the vacuous-green detector passes on the current tree first.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go list -f '{{if and (eq (len .TestGoFiles) 0) (eq (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}' ./...
  ```
  Expected output: nothing at all. If it prints a package path, that package has no tests and this task cannot proceed until it does. (`{{...}}` without a leading `$` is not GitHub expression syntax, so it needs no escaping in YAML.)

- [ ] **Prove the detector actually detects.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && mkdir -p internal/probe && printf 'package probe\n\n// Doc is a placeholder.\nconst Doc = "x"\n' > internal/probe/probe.go && go list -f '{{if and (eq (len .TestGoFiles) 0) (eq (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}' ./... ; rm -rf internal/probe
  ```
  Expected output: `github.com/bsv-blockchain-demos/weather-proof/internal/probe`, then the directory is removed. Confirm with `git status --short` that the tree is clean again.

- [ ] **Add the guard step to the existing `check` job.** In `.github/workflows/go.yml`, immediately AFTER the existing `- name: Test` step (the one running `go test ./... -count=1`) and BEFORE the `- name: Lint` step, insert exactly — **at the SAME indentation as the existing `- name: Test` step, which is 6 spaces for the `- name:` line**; every YAML block in this plan is written at its final file indentation, and pasting this one two columns short makes `- name:` a sibling of `steps:` rather than an item of it, which fails to parse:
  ```yaml
        # `go test ./...` exits 0 for a package with no test files, and -count=1
        # does not help: it defeats a stale cached PASS, not a vacuous one. This
        # step fails the build when any package would be reported as a pass while
        # executing nothing. It passes on the tree as of this commit.
        - name: No package is silently untested
          run: |
            set -euo pipefail
            missing=$(go list -f '{{if and (eq (len .TestGoFiles) 0) (eq (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}' ./...)
            if [ -n "$missing" ]; then
              echo "Packages with no test files (go test ./... reports these as a pass):"
              echo "$missing"
              exit 1
            fi
  ```
  Change nothing else in the `check` job.

- [ ] **Add the Postgres job.** At the END of `.github/workflows/go.yml`, as a second entry under the existing top-level `jobs:` key (same indentation as `check:`), append exactly:
  ```yaml
    # Postgres-backed store tests, in a SEPARATE job so the `check` job stays
    # byte-identical apart from the untested-package guard and a service-container
    # failure can never take down vet, build, lint or the golden gate. The
    # top-level `permissions: contents: read` above covers this job too.
    integration:
      name: integration (postgres)
      runs-on: ubuntu-latest
      services:
        postgres:
          image: postgres:17-alpine
          env:
            POSTGRES_USER: postgres
            POSTGRES_PASSWORD: postgres
            POSTGRES_DB: weatherproof_test
          ports:
            - 5432:5432
          # -d weatherproof_test rather than a bare pg_isready, so the gate means
          # "the target database answers", not merely "the postmaster is up".
          # 10 retries at 5 s is a 50 s budget against a ~2 s observed readiness.
          options: >-
            --health-cmd "pg_isready -U postgres -d weatherproof_test"
            --health-interval 5s
            --health-timeout 5s
            --health-retries 10
      steps:
        - name: Checkout code
          uses: actions/checkout@v4

        - name: Set up Go
          uses: actions/setup-go@v5
          with:
            go-version-file: go.mod

        # WEATHER_TEST_REQUIRE_POSTGRES turns storetest's local-dev t.Skip into a
        # t.Fatal. A renamed DSN variable, a mistyped env: key or a dropped
        # service block is therefore a RED build, not a silent all-skip pass.
        #
        # The two `test -n` lines are the belt to that braces: if BOTH env keys
        # were mistyped, RequireDSN would see neither, every Postgres test would
        # t.Skip, and both jobs would be green. This fails before `go test` runs.
        #
        # -v plus the SKIP grep is what makes the acceptance criterion
        # "nothing skips in CI" actually checkable: `go test` prints `--- SKIP`
        # lines ONLY under -v, and a fully skipped package otherwise prints a
        # bare `ok`.
        - name: Store tests against Postgres
          env:
            WEATHER_TEST_POSTGRES_DSN: postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable
            WEATHER_TEST_REQUIRE_POSTGRES: "1"
          run: |
            set -euo pipefail
            test -n "$WEATHER_TEST_POSTGRES_DSN"
            test -n "$WEATHER_TEST_REQUIRE_POSTGRES"
            go test -race -count=1 -timeout 10m -v ./internal/store/... 2>&1 | tee /tmp/store-tests.log
            if grep -q -- '--- SKIP' /tmp/store-tests.log; then
              echo "a Postgres test skipped in CI; the suite must never skip here"
              grep -- '--- SKIP' /tmp/store-tests.log
              exit 1
            fi

        # One pass of a concurrency test is weak evidence. Repeat the claim race
        # on its own so a scheduling-dependent escape shows up.
        #
        # The grep is not decoration. `go test -run` with a pattern matching
        # NOTHING exits 0, so without it this step goes green while executing
        # zero assertions the moment the test is renamed or deleted — a
        # vacuous-green hole in the step guarding the highest-value invariant in
        # the whole design. The regex is anchored for the same reason.
        - name: Claim concurrency, repeated
          env:
            WEATHER_TEST_POSTGRES_DSN: postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable
            WEATHER_TEST_REQUIRE_POSTGRES: "1"
          run: |
            set -euo pipefail
            out=$(go test -race -count=5 -timeout 10m -run '^TestClaimPendingNeverDoubleClaims$' -v ./internal/store/postgres/)
            echo "$out"
            echo "$out" | grep -q -- '--- PASS: TestClaimPendingNeverDoubleClaims' || {
              echo "the claim race did not run"
              exit 1
            }
  ```
  The `-run` pattern names a test that does not exist yet; Task 8 creates it. **This step is therefore RED between this commit and Task 8's, deliberately** — that is the whole point of the `grep`, because the alternative (a `-run` pattern matching nothing exiting 0) is a hole that stays open forever. Land Tasks 5 through 8 on one branch and expect this one step to be red in between; the acceptance gate is the end of the branch, not the middle of it.

- [ ] **Verify the workflow YAML parses AND that both vacuous-green guards are in it.** The parse check alone would accept a workflow whose assertions had been quietly dropped, which is the failure mode these steps exist to prevent. Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && python3 -c "
  import yaml
  d = yaml.safe_load(open('.github/workflows/go.yml'))
  print(sorted(d['jobs']))
  check = d['jobs']['check']['steps']
  print(len(check), 'check steps')
  assert sorted(d['jobs']) == ['check', 'integration']
  assert len(check) == 9, len(check)
  names = [s.get('name') for s in check]
  assert 'No package is silently untested' in names, names
  integ = {s['name']: s.get('run', '') for s in d['jobs']['integration']['steps']}
  store = integ['Store tests against Postgres']
  assert 'test -n \"\$WEATHER_TEST_POSTGRES_DSN\"' in store, store
  assert '--- SKIP' in store and '-v ' in store, store
  claim = integ['Claim concurrency, repeated']
  assert '^TestClaimPendingNeverDoubleClaims\$' in claim, claim
  assert '--- PASS: TestClaimPendingNeverDoubleClaims' in claim, claim
  print('OK')
  "
  ```
  Expected output: `['check', 'integration']`, then `9 check steps`, then `OK`.

- [ ] **Add the Make targets.** At the END of `Makefile`, append exactly:
  ```makefile

  # ---------------------------------------------------------------------------
  # Postgres for the Go store tests.
  #
  # Named pg-*/go-* because this Makefile's `build`, `up`, `down` and `test`
  # belong to the Docker/TypeScript workflow and must keep working. New targets
  # use `docker compose` (the v2+ plugin spelling) rather than the legacy
  # hyphenated `docker-compose` used above; reconciling the two is a later plan.
  # There is no host psql client in this project's environment, so pg-psql execs
  # into the container.
  # ---------------------------------------------------------------------------
  .PHONY: pg-up pg-down pg-psql go-test-pg

  WEATHER_TEST_POSTGRES_DSN ?= postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable

  pg-up:
  	docker compose up -d postgres
  	@echo "Waiting for Postgres..."
  	@until docker compose exec -T postgres pg_isready -U postgres -d weatherproof_test >/dev/null 2>&1; do sleep 1; done
  	@echo "Postgres ready on 127.0.0.1:5432 (database weatherproof_test)"

  pg-down:
  	docker compose stop postgres

  pg-psql:
  	docker compose exec postgres psql -U postgres -d weatherproof_test

  go-test-pg:
  	WEATHER_TEST_POSTGRES_DSN='$(WEATHER_TEST_POSTGRES_DSN)' \
  	WEATHER_TEST_REQUIRE_POSTGRES=1 \
  	go test -race -count=1 -timeout 10m ./internal/store/...
  ```

- [ ] **Add the compose service, additively.** In `docker-compose.yaml`, add a `postgres` service alongside the existing `mongodb`, `app`, `frontend` and `setup` services — do not remove or edit any of them, because `app` is the live TypeScript backend and still needs `MONGO_URI`. Insert exactly:
  ```yaml
    # Added for the Go store's tests. Purely additive: mongodb stays because the
    # `app` service is the running TypeScript backend and requires MONGO_URI.
    # Swapping mongo out belongs to the plan that deletes the TypeScript.
    # Bound to 127.0.0.1 so the database is not exposed on the LAN.
    postgres:
      image: postgres:17-alpine
      container_name: weather-chain-postgres
      restart: unless-stopped
      environment:
        POSTGRES_USER: postgres
        POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-postgres}
        POSTGRES_DB: weatherproof_test
      ports:
        - "127.0.0.1:5432:5432"
      volumes:
        - postgres_data:/var/lib/postgresql/data
      networks:
        - weather-chain-network
      healthcheck:
        test: ["CMD-SHELL", "pg_isready -U postgres -d weatherproof_test"]
        interval: 5s
        timeout: 5s
        retries: 5
        start_period: 10s
  ```
  and under the existing top-level `volumes:` key, alongside `mongodb_data` and `mongodb_config`, add exactly:
  ```yaml
    postgres_data:
      driver: local
  ```

- [ ] **Verify compose still parses and nothing was lost.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && docker compose config --services | sort
  ```
  Expected output, in this order: `app`, `frontend`, `mongodb`, `postgres`. (`setup` is behind its profile and does not list.)

- [ ] **Write the docs.** Create `docs/testing-postgres.md` with exactly this content:
  ```markdown
  # Running the Postgres store tests

  The Go store tests in `internal/store/...` need a real PostgreSQL 17 server.
  Everything else in the module — including the whole HTTP surface, which runs
  against `internal/store/fake` — needs nothing.

  ## Locally

  ```
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

  ```
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
    `ok`;
  - the claim-race step greps its own output for `--- PASS:
    TestClaimPendingNeverDoubleClaims`, because `go test -run` with a pattern
    matching nothing exits 0.

  The split into two jobs is deliberate: a service-container failure must not be
  able to take down vet, lint or the golden gate.
  ```

- [ ] **Verify the whole local loop.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && docker rm -f wp-pg >/dev/null 2>&1 ; make pg-up && make go-test-pg
  ```
  Expected output: `Postgres ready on 127.0.0.1:5432 (database weatherproof_test)` then `ok` lines for `internal/store`, `internal/store/fake`, `internal/store/postgres` and `internal/store/storetest`, with NO `[no tests to run]` and NO skip messages.

- [ ] **Verify the untested-package guard and the rest of `check`.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go mod tidy && git diff --exit-code go.mod go.sum && go vet ./... && go build ./... && go test ./... -count=1 && go list -f '{{if and (eq (len .TestGoFiles) 0) (eq (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}' ./... && golangci-lint run --max-same-issues=0
  ```
  Expected: no output from `git diff`, no output from `go list`, and `ok` lines plus `0 issues` from the linter.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add .github/workflows/go.yml Makefile docker-compose.yaml docs/testing-postgres.md && git commit -m "ci: postgres service job, untested-package guard, pg make targets"
  ```

---

## Task 6: migrations.sql, the migration runner, and the schema invariants

**Files:**
- Create: `internal/store/postgres/migrations.sql`
- Create: `internal/store/postgres/migrate.go`
- Create: `internal/store/postgres/migrations_test.go`
- Modify: `internal/store/storetest/storetest.go`

**Interfaces:**

Consumes (Task 3): `postgres.PoolConfig`, `postgres.NewPool`. Consumes (Task 4): `storetest.Schema`, `storetest.Pool(t testing.TB, schema Schema, maxConns int32) *pgxpool.Pool`, `storetest.RequireDSN`.

Produces:
```go
package postgres
type Execer interface {
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
func Migrate(ctx context.Context, db Execer) error

package storetest
func Fresh(t testing.TB, schema Schema) *pgxpool.Pool // Pool with maxConns 16, migrated
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/migrations_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5"
  	"github.com/jackc/pgx/v5/pgconn"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  // storeSchema is this package's private Postgres schema. See
  // storetest.Schema's doc comment for why all three strings are spelled out
  // and why no test in this package may call t.Parallel().
  var storeSchema = storetest.Schema{
  	Name:      "wp_test_store",
  	CreateSQL: "CREATE SCHEMA IF NOT EXISTS wp_test_store",
  	DropSQL:   "DROP SCHEMA IF EXISTS wp_test_store CASCADE",
  }

  // sqlState returns the SQLSTATE of err, or "" if err is not a Postgres error.
  func sqlState(err error) string {
  	var pgErr *pgconn.PgError
  	if errors.As(err, &pgErr) {
  		return pgErr.Code
  	}
  	return ""
  }

  func TestMigrateIsIdempotent(t *testing.T) {
  	pool := storetest.Pool(t, storeSchema, 4)
  	ctx := context.Background()

  	if err := postgres.Migrate(ctx, pool); err != nil {
  		t.Fatalf("first Migrate: %v", err)
  	}
  	if err := postgres.Migrate(ctx, pool); err != nil {
  		t.Fatalf("second Migrate: %v", err)
  	}
  	if err := postgres.Migrate(ctx, pool); err != nil {
  		t.Fatalf("third Migrate: %v", err)
  	}

  	var tables int
  	err := pool.QueryRow(ctx,
  		"SELECT count(*) FROM pg_tables WHERE schemaname = $1", storeSchema.Name).Scan(&tables)
  	if err != nil {
  		t.Fatalf("counting tables: %v", err)
  	}
  	if tables != 5 {
  		t.Fatalf("tables = %d, want 5 (weather_records, stations, app_stats, deposits, app_preflight)", tables)
  	}

  	// The NAMES, not a count. A count is an opaque integer that has to be
  	// bumped by hand and was wrong in an earlier draft of this plan (it said 8,
  	// double-counting ux_records_station_obs as both the dedupe index and one of
  	// the record indexes, while the migration creates 7). Comparing names means
  	// adding an index forces exactly one edit in exactly one place, and the
  	// failure message says which index is missing rather than "7 != 8".
  	//
  	// Note the parentheses around the OR: `A AND B OR A AND C` happens to parse
  	// correctly here because AND binds tighter, but writing it out is how the
  	// next person avoids finding out the hard way.
  	rows, err := pool.Query(ctx, `
  		SELECT indexname FROM pg_indexes
  		 WHERE schemaname = $1
  		   AND (indexname LIKE 'ix_%' OR indexname LIKE 'ux_%')
  		 ORDER BY indexname`, storeSchema.Name)
  	if err != nil {
  		t.Fatalf("listing indexes: %v", err)
  	}
  	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
  	if err != nil {
  		t.Fatalf("collecting index names: %v", err)
  	}
  	want := []string{
  		"ix_records_list",
  		"ix_records_reconcile",
  		"ix_records_station_created",
  		"ix_records_status_created",
  		"ix_records_txid",
  		"ix_stations_search",
  		"ux_records_station_obs",
  	}
  	if len(got) != len(want) {
  		t.Fatalf("named indexes = %v (%d), want %v (%d: 5 record + 1 unique dedupe + 1 station GIN)",
  			got, len(got), want, len(want))
  	}
  	for i := range want {
  		if got[i] != want[i] {
  			t.Fatalf("named indexes = %v, want %v", got, want)
  		}
  	}
  }

  func TestAppStatsIsASingleton(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	var rows, id int
  	if err := pool.QueryRow(ctx, "SELECT count(*), min(id) FROM app_stats").Scan(&rows, &id); err != nil {
  		t.Fatalf("reading app_stats: %v", err)
  	}
  	if rows != 1 || id != 1 {
  		t.Fatalf("app_stats has %d rows with min id %d, want 1 row with id 1", rows, id)
  	}

  	// A second id is structurally impossible.
  	_, err := pool.Exec(ctx, "INSERT INTO app_stats (id) VALUES (2)")
  	if got := sqlState(err); got != "23514" {
  		t.Fatalf("inserting app_stats id=2 gave SQLSTATE %q (err %v), want 23514", got, err)
  	}
  }

  func TestStatusCheckConstraintRejectsAnUnknownStatus(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	_, err := pool.Exec(ctx, `
  		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
  		VALUES ('x', 1, now(), now(), '{}'::jsonb, 'Pending')`)
  	if got := sqlState(err); got != "23514" {
  		t.Fatalf("status 'Pending' gave SQLSTATE %q (err %v), want 23514", got, err)
  	}

  	for _, bad := range []string{"", "PENDING", "unknown", "arc-accepted"} {
  		_, err := pool.Exec(ctx, `
  			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
  			VALUES ($1, 1, now(), now(), '{}'::jsonb, $2)`, "id-"+bad, bad)
  		if got := sqlState(err); got != "23514" {
  			t.Errorf("status %q gave SQLSTATE %q, want 23514", bad, got)
  		}
  	}

  	for _, good := range []string{"pending", "processing", "failed"} {
  		_, err := pool.Exec(ctx, `
  			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, claimed_at)
  			VALUES ($1, 1, now(), $2, '{}'::jsonb, $3, now())`,
  			"ok-"+good, time.Now().Add(time.Duration(len(good))*time.Second), good)
  		if err != nil {
  			t.Errorf("status %q was rejected: %v", good, err)
  		}
  	}
  }

  func TestProcessingRequiresALease(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	// This is the constraint that makes the TypeScript's claim shape —
  	// SELECT ids, then UPDATE ... SET status='processing' with no claimed_at —
  	// structurally unwritable, and it makes the reaper's
  	// `claimed_at < now() - lease` predicate total so no defensive
  	// `OR claimed_at IS NULL` is needed anywhere.
  	_, err := pool.Exec(ctx, `
  		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
  		VALUES ('a', 1, now(), now(), '{}'::jsonb, 'processing')`)
  	if got := sqlState(err); got != "23514" {
  		t.Fatalf("processing without claimed_at gave SQLSTATE %q (err %v), want 23514", got, err)
  	}

  	if _, insertErr := pool.Exec(ctx, `
  		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  		VALUES ('b', 1, now(), now(), '{}'::jsonb)`); insertErr != nil {
  		t.Fatalf("inserting a pending row: %v", insertErr)
  	}
  	_, err = pool.Exec(ctx, "UPDATE weather_records SET status='processing' WHERE id='b'")
  	if got := sqlState(err); got != "23514" {
  		t.Fatalf("naive claim UPDATE gave SQLSTATE %q (err %v), want 23514", got, err)
  	}
  }

  func TestCompletedRequiresPublicationColumns(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	cases := []struct {
  		name string
  		sql  string
  	}{
  		{"no txid", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, output_index, processed_at)
  			VALUES ('a', 1, now(), now(), '{}'::jsonb, 'completed', 0, now())`},
  		{"no output_index", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, txid, processed_at)
  			VALUES ('b', 1, now(), now(), '{}'::jsonb, 'completed', 'tx', now())`},
  		{"no processed_at", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, txid, output_index)
  			VALUES ('c', 1, now(), now(), '{}'::jsonb, 'completed', 'tx', 0)`},
  	}
  	for _, c := range cases {
  		_, err := pool.Exec(ctx, c.sql)
  		if got := sqlState(err); got != "23514" {
  			t.Errorf("%s: SQLSTATE %q (err %v), want 23514", c.name, got, err)
  		}
  	}

  	if _, err := pool.Exec(ctx, `
  		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
  		VALUES ('ok', 1, now(), now(), '{}'::jsonb, 'completed', 'tx', 0, now())`); err != nil {
  		t.Fatalf("a fully published completed row was rejected: %v", err)
  	}
  }

  func TestChainStatusCheckConstraint(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	for _, good := range []string{"arc-accepted", "unmined", "mined", "aborted"} {
  		_, err := pool.Exec(ctx, `
  			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, chain_status)
  			VALUES ($1, 1, now(), $2, '{}'::jsonb, $3)`,
  			"g-"+good, time.Now().Add(time.Duration(len(good))*time.Second), good)
  		if err != nil {
  			t.Errorf("chain_status %q was rejected: %v", good, err)
  		}
  	}
  	for _, bad := range []string{"", "MINED", "arc_accepted", "pending"} {
  		_, err := pool.Exec(ctx, `
  			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, chain_status)
  			VALUES ($1, 2, now(), now(), '{}'::jsonb, $2)`, "b-"+bad, bad)
  		if got := sqlState(err); got != "23514" {
  			t.Errorf("chain_status %q gave SQLSTATE %q, want 23514", bad, got)
  		}
  	}
  }

  func TestDedupeIndexRaisesAUniqueViolation(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	if _, err := pool.Exec(ctx, `
  		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  		VALUES ('a', 1000, $1, $1, '{}'::jsonb)`, obs); err != nil {
  		t.Fatalf("first insert: %v", err)
  	}
  	_, err := pool.Exec(ctx, `
  		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  		VALUES ('b', 1000, $1, $1, '{}'::jsonb)`, obs)
  	var pgErr *pgconn.PgError
  	if !errors.As(err, &pgErr) {
  		t.Fatalf("duplicate (station_id, observation_time) error = %v, want a *pgconn.PgError", err)
  	}
  	if pgErr.Code != "23505" {
  		t.Fatalf("SQLSTATE = %q, want 23505", pgErr.Code)
  	}
  	if pgErr.ConstraintName != "ux_records_station_obs" {
  		t.Fatalf("ConstraintName = %q, want ux_records_station_obs", pgErr.ConstraintName)
  	}
  }

  func TestStationSearchVectorIsGenerated(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	// The two-argument to_tsvector('english', ...) is IMMUTABLE, which is what
  	// makes a generated column legal. The one-argument form is only STABLE and
  	// would be rejected at CREATE TABLE, so this also proves the migration did
  	// not get "simplified".
  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, name, location) VALUES (7, 'Harbor Mast', 'Bristol Docks')"); err != nil {
  		t.Fatalf("insert station: %v", err)
  	}
  	var tsv string
  	if err := pool.QueryRow(ctx, "SELECT search_tsv::text FROM stations WHERE station_id = 7").Scan(&tsv); err != nil {
  		t.Fatalf("reading search_tsv: %v", err)
  	}
  	if tsv == "" {
  		t.Fatal("search_tsv is empty; the generated column did not populate")
  	}
  	var hit int
  	err := pool.QueryRow(ctx,
  		"SELECT count(*) FROM stations WHERE search_tsv @@ websearch_to_tsquery('english', $1)", "bristol").Scan(&hit)
  	if err != nil {
  		t.Fatalf("search: %v", err)
  	}
  	if hit != 1 {
  		t.Fatalf("search for bristol matched %d rows, want 1", hit)
  	}
  }

  func TestUUIDv7IsNotAvailableButGenRandomUUIDIs(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	// uuidv7() is a PostgreSQL 18 builtin. The design specifies uuidv7 record
  	// ids, so they MUST be generated in Go with uuid.NewV7(); a DB-side default
  	// would make the migration fail outright on the pinned 17-alpine image.
  	_, err := pool.Exec(ctx, "SELECT uuidv7()")
  	if got := sqlState(err); got != "42883" {
  		t.Fatalf("SELECT uuidv7() gave SQLSTATE %q (err %v), want 42883 undefined_function", got, err)
  	}

  	// gen_random_uuid() is core in PG 13+ and needs no pgcrypto, which is why
  	// the migration must NOT contain CREATE EXTENSION pgcrypto (it can fail
  	// outright on a locked-down managed server).
  	var u string
  	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&u); err != nil {
  		t.Fatalf("gen_random_uuid(): %v", err)
  	}
  	if len(u) != 36 {
  		t.Fatalf("gen_random_uuid() = %q, want a 36-character uuid", u)
  	}

  	var exts int
  	if err := pool.QueryRow(ctx,
  		"SELECT count(*) FROM pg_extension WHERE extname = 'pgcrypto'").Scan(&exts); err != nil {
  		t.Fatalf("checking pg_extension: %v", err)
  	}
  	if exts != 0 {
  		t.Fatal("pgcrypto is installed; the migration must not create it")
  	}
  }

  func TestCreatedAtIsTheTransactionTimestamp(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	// This is why every ordered record query needs the id tiebreaker: now() is
  	// transaction_timestamp(), so a whole poll batch shares ONE created_at and
  	// `ORDER BY created_at DESC` alone is a non-total order.
  	tx, err := pool.Begin(ctx)
  	if err != nil {
  		t.Fatalf("Begin: %v", err)
  	}
  	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	for i := range 19 {
  		if _, err := tx.Exec(ctx, `
  			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  			VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
  			"r"+string(rune('a'+i)), int64(1000+i), base, base.Add(time.Duration(i)*time.Minute)); err != nil {
  			t.Fatalf("insert %d: %v", i, err)
  		}
  	}
  	if err := tx.Commit(ctx); err != nil {
  		t.Fatalf("Commit: %v", err)
  	}

  	var distinct int
  	if err := pool.QueryRow(ctx, "SELECT count(DISTINCT created_at) FROM weather_records").Scan(&distinct); err != nil {
  		t.Fatalf("counting distinct created_at: %v", err)
  	}
  	if distinct != 1 {
  		t.Fatalf("count(DISTINCT created_at) = %d over one transaction, want 1", distinct)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `undefined: postgres.Migrate` and `undefined: storetest.Fresh` — the compile fails before any test runs.

- [ ] **Write the migration script.** Create `internal/store/postgres/migrations.sql` with exactly this content:
  ```sql
  -- The whole schema, applied at startup and idempotent.
  --
  -- Deliberately absent: CREATE EXTENSION pgcrypto (gen_random_uuid() is core in
  -- PG 13+, and CREATE EXTENSION can fail outright on a locked-down managed
  -- server), any uuidv7() default (that is a PG 18 builtin; ids come from Go's
  -- uuid.NewV7()), and ix_stations_active (a boolean index over ~19 mostly-true
  -- rows has no query behind it — both station queries seq-scan at that size).

  -- ---------- weather records: the durable queue and the record table ----------
  CREATE TABLE IF NOT EXISTS weather_records (
    id               text        PRIMARY KEY,            -- uuidv7, generated in Go
    station_id       bigint      NOT NULL,
    timestamp        timestamptz NOT NULL,               -- reading timestamp
    observation_time timestamptz NOT NULL,               -- dedupe key
    data             jsonb       NOT NULL,
    status           text        NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','processing','completed','failed')),
    attempts         int         NOT NULL DEFAULT 0,
    claim_ref        uuid,                               -- batch label, survives reaping
    adopt_required   boolean     NOT NULL DEFAULT false,
    claimed_at       timestamptz,
    txid             text,
    output_index     int,
    block_height     bigint,
    chain_status     text
                     CHECK (chain_status IS NULL
                            OR chain_status IN ('arc-accepted','unmined','mined','aborted')),
    mined_at         timestamptz,
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    processed_at     timestamptz,

    -- Makes the naive claim shape structurally unwritable. A SELECT-then-UPDATE
    -- claim that forgets claimed_at is rejected with SQLSTATE 23514, and the
    -- reaper's `claimed_at < now() - lease` predicate becomes total, so no
    -- defensive `OR claimed_at IS NULL` is needed anywhere.
    CONSTRAINT ck_records_processing_leased
      CHECK (status <> 'processing' OR claimed_at IS NOT NULL),

    -- Encodes the invariant the DTO layer would otherwise have to trust: a
    -- completed record always has a txid, an output index and a processed_at.
    CONSTRAINT ck_records_completed_published
      CHECK (status <> 'completed'
             OR (txid IS NOT NULL AND output_index IS NOT NULL AND processed_at IS NOT NULL))
  );

  -- Structural duplicate prevention and the ON CONFLICT arbiter. NOT nullable,
  -- NOT partial. Mongo had no unique index anywhere, so this is new capability
  -- rather than a port. Also serves count(*) WHERE station_id = $1.
  CREATE UNIQUE INDEX IF NOT EXISTS ux_records_station_obs
    ON weather_records (station_id, observation_time);

  -- The claim's inner SELECT: Index Scan with Index Cond status='pending'.
  CREATE INDEX IF NOT EXISTS ix_records_status_created
    ON weather_records (status, created_at);

  -- The station history page. Delivers an Index Only Scan with Heap Fetches 0.
  CREATE INDEX IF NOT EXISTS ix_records_station_created
    ON weather_records (station_id, created_at DESC, id DESC);

  -- The global record list. The id tiebreaker is what makes the order TOTAL:
  -- created_at defaults to now(), which is the TRANSACTION timestamp, so a
  -- whole poll batch shares one value.
  CREATE INDEX IF NOT EXISTS ix_records_list
    ON weather_records (created_at DESC, id DESC);

  -- The proof/verify existence gate and the reconciler's match-by-txid.
  CREATE INDEX IF NOT EXISTS ix_records_txid
    ON weather_records (txid) WHERE txid IS NOT NULL;

  -- ReconcileCandidates.
  CREATE INDEX IF NOT EXISTS ix_records_reconcile
    ON weather_records (processed_at)
    WHERE status = 'completed' AND (chain_status IS NULL OR chain_status <> 'mined');

  -- ---------- stations ----------
  CREATE TABLE IF NOT EXISTS stations (
    station_id        bigint      PRIMARY KEY,
    name              text        NOT NULL DEFAULT '',
    location          text        NOT NULL DEFAULT '',
    latitude          double precision,
    longitude         double precision,
    is_active         boolean     NOT NULL DEFAULT true,
    tx_records        bigint      NOT NULL DEFAULT 0,
    last_reading      timestamptz,
    -- NULLABLE, and the API never OMITS it: the frontend tests
    -- `lastTemp !== null` strictly, so an absent key renders "undefined°C".
    last_temp         double precision,
    last_conditions   text        NOT NULL DEFAULT '',
    last_block_height bigint,
    -- The TWO-argument to_tsvector is IMMUTABLE and therefore legal in a
    -- generated column. The one-argument to_tsvector(text) is only STABLE and
    -- would be rejected at CREATE TABLE. Do not "simplify" this.
    search_tsv        tsvector    GENERATED ALWAYS AS (
                        to_tsvector('english',
                          coalesce(name,'') || ' ' || coalesce(location,''))) STORED,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
  );
  CREATE INDEX IF NOT EXISTS ix_stations_search ON stations USING gin (search_tsv);

  -- ---------- stats: a transactionally-maintained singleton ----------
  CREATE TABLE IF NOT EXISTS app_stats (
    id                smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    total_tx          bigint      NOT NULL DEFAULT 0,    -- DISTINCT on-chain transactions
    total_records     bigint      NOT NULL DEFAULT 0,    -- completed records
    last_record_write timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now()
  );
  INSERT INTO app_stats (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

  -- ---------- operator deposits ----------
  CREATE TABLE IF NOT EXISTS deposits (
    suffix          text        PRIMARY KEY,
    prefix          text        NOT NULL,
    address         text        NOT NULL,
    locking_script  text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    txid            text,
    vout            int,
    satoshis        bigint,
    internalized_at timestamptz
  );

  -- ---------- preflight fingerprint cache ----------
  CREATE TABLE IF NOT EXISTS app_preflight (
    fingerprint text        PRIMARY KEY,
    ok_at       timestamptz NOT NULL
  );
  ```

- [ ] **Write the migration runner.** Create `internal/store/postgres/migrate.go` with exactly this content:
  ```go
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
  ```

- [ ] **Add `Fresh` to the harness.** Append exactly this to `internal/store/storetest/storetest.go`:
  ```go
  // freshMaxConns is the pool size Fresh hands out. It must stay above the
  // goroutine count of every concurrency test in the tree, because a pool
  // smaller than the worker count serializes the race and turns the test into a
  // vacuous pass.
  const freshMaxConns = 16

  // Fresh returns a migrated, empty database in schema's private namespace.
  //
  // Every Postgres test should start here rather than at Pool: the only tests
  // that want an unmigrated schema are the migration tests themselves.
  func Fresh(t testing.TB, schema Schema) *pgxpool.Pool {
  	t.Helper()
  	pool := Pool(t, schema, freshMaxConns)
  	if err := postgres.Migrate(context.Background(), pool); err != nil {
  		t.Fatalf("storetest.Fresh: %v", err)
  	}
  	return pool
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1 -v
  ```
  Expected output: `--- PASS` for `TestMigrateIsIdempotent`, `TestAppStatsIsASingleton`, `TestStatusCheckConstraintRejectsAnUnknownStatus`, `TestProcessingRequiresALease`, `TestCompletedRequiresPublicationColumns`, `TestChainStatusCheckConstraint`, `TestDedupeIndexRaisesAUniqueViolation`, `TestStationSearchVectorIsGenerated`, `TestUUIDv7IsNotAvailableButGenRandomUUIDIs`, `TestCreatedAtIsTheTransactionTimestamp`, plus the five Task 3 pool tests (`TestSecretNeverPrints` moved to `internal/config` with the type), then `ok`.

- [ ] **Confirm it still passes when each test runs alone.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -run TestProcessingRequiresALease -v && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=3 -run TestMigrateIsIdempotent -v
  ```
  Both must pass. This is the ordering-independence check: an assertion that only discriminates under a `-run` filter, or only on the first `-count` iteration, is not a test.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && go test ./... -count=1 && golangci-lint run --max-same-issues=0
  ```
  All must pass with no findings.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/migrations.sql internal/store/postgres/migrate.go internal/store/postgres/migrations_test.go internal/store/storetest/storetest.go && git commit -m "postgres: the schema, an idempotent migration runner, and its invariants"
  ```

---

## Task 7: Insert with ON CONFLICT dedupe, and the error classifier

**Files:**
- Create: `internal/store/postgres/errors.go`
- Create: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/records_insert_test.go`

**Interfaces:**

Consumes (Task 1): `store.NewRecord`, `store.Record`, `store.ErrNotFound`, `store.ErrConflict`, `store.Status`. Consumes (Task 2): the `store.RecordStore` method set, whose `Insert` signature is `Insert(ctx context.Context, r NewRecord) (bool, error)`. Consumes (Task 6): `storetest.Fresh(t testing.TB, schema Schema) *pgxpool.Pool`; and from `migrations_test.go`, already in package `postgres_test`, the package-level `storeSchema` variable and the `sqlState(err error) string` helper — use both, do NOT redeclare either.

Produces:
```go
package postgres

var ErrOperation error   // opaque: everything unclassified
var ErrTransient error   // 40001, 40P01, 57014, 53300 — the retryable bucket

const recordColumns string        // the 18 columns, unqualified
const recordColumnsAliased string // the 18 columns, qualified with the r alias

type RecordStore struct { /* unexported */ }
func NewRecordStore(db *pgxpool.Pool) *RecordStore
func (s *RecordStore) Insert(ctx context.Context, r store.NewRecord) (bool, error)
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/records_insert_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"testing"
  	"time"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
  )

  // Compile-time proof that Insert's signature is exactly the one
  // store.RecordStore declares. A method value on a nil pointer is legal
  // because it is never called.
  var _ func(context.Context, store.NewRecord) (bool, error) = (*postgres.RecordStore)(nil).Insert

  // fullWeatherData returns a WeatherData with all 33 fields set to distinct,
  // non-zero values, so a jsonb round trip that drops or transposes any field is
  // detectable rather than accidentally correct.
  func fullWeatherData() weather.WeatherData {
  	return weather.WeatherData{
  		AirDensity:                      1.204521,
  		AirTemperature:                  18,
  		Brightness:                      41234,
  		Conditions:                      "Clear",
  		DeltaT:                          3,
  		DewPoint:                        11,
  		FeelsLike:                       19,
  		Icon:                            "clear-day",
  		IsPrecipLocalDayRainCheck:       true,
  		IsPrecipLocalYesterdayRainCheck: false,
  		LightningStrikeCountLast1hr:     2,
  		LightningStrikeCountLast3hr:     7,
  		LightningStrikeLastDistance:     13,
  		LightningStrikeLastDistanceMsg:  "13 km away",
  		LightningStrikeLastEpoch:        1776441600,
  		PrecipAccumLocalDay:             3,
  		PrecipAccumLocalYesterday:       5,
  		PrecipMinutesLocalDay:           17,
  		PrecipMinutesLocalYesterday:     41,
  		PrecipProbability:               22,
  		PressureTrend:                   "steady",
  		RelativeHumidity:                63,
  		SeaLevelPressure:                1013,
  		SolarRadiation:                  312,
  		StationPressure:                 1009.874321,
  		Time:                            1776441600,
  		UV:                              4,
  		WetBulbGlobeTemperature:         16,
  		WetBulbTemperature:              14,
  		WindAvg:                         6,
  		WindDirection:                   214,
  		WindDirectionCardinal:           "SW",
  		WindGust:                        11,
  	}
  }

  func TestInsertReturnsTrueForAFreshRow(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs := postgres.NewRecordStore(pool)
  	ctx := context.Background()
  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  	inserted, err := rs.Insert(ctx, store.NewRecord{
  		ID: "rec-1", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
  	})
  	if err != nil {
  		t.Fatalf("Insert: %v", err)
  	}
  	if !inserted {
  		t.Fatal("Insert = false for a fresh row, want true")
  	}

  	var status, id string
  	var attempts int32
  	err = pool.QueryRow(ctx,
  		"SELECT id, status, attempts FROM weather_records WHERE id = $1", "rec-1").
  		Scan(&id, &status, &attempts)
  	if err != nil {
  		t.Fatalf("reading back: %v", err)
  	}
  	if status != string(store.StatusPending) {
  		t.Errorf("status = %q, want %q", status, string(store.StatusPending))
  	}
  	if attempts != 0 {
  		t.Errorf("attempts = %d, want 0", attempts)
  	}
  }

  func TestInsertDedupeIsFalseAndNotAnError(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs := postgres.NewRecordStore(pool)
  	ctx := context.Background()
  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  	if _, err := rs.Insert(ctx, store.NewRecord{
  		ID: "rec-1", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
  	}); err != nil {
  		t.Fatalf("first Insert: %v", err)
  	}

  	// A station re-reporting the same observation_time is the intended steady
  	// state, so it must NOT be an error — only inserted=false.
  	inserted, err := rs.Insert(ctx, store.NewRecord{
  		ID: "rec-2", StationID: 1000, Timestamp: obs.Add(time.Second), ObservationTime: obs, Data: fullWeatherData(),
  	})
  	if err != nil {
  		t.Fatalf("duplicate Insert error = %v, want nil", err)
  	}
  	if inserted {
  		t.Fatal("duplicate Insert = true, want false")
  	}

  	var rows int
  	if countErr := pool.QueryRow(ctx, "SELECT count(*) FROM weather_records").Scan(&rows); countErr != nil {
  		t.Fatalf("counting: %v", countErr)
  	}
  	if rows != 1 {
  		t.Fatalf("rows = %d, want 1", rows)
  	}

  	// A different observation_time on the same station IS a new row.
  	inserted, err = rs.Insert(ctx, store.NewRecord{
  		ID: "rec-3", StationID: 1000, Timestamp: obs, ObservationTime: obs.Add(time.Minute), Data: fullWeatherData(),
  	})
  	if err != nil || !inserted {
  		t.Fatalf("Insert with a new observation_time = (%v, %v), want (true, nil)", inserted, err)
  	}
  }

  func TestInsertPrimaryKeyCollisionIsErrConflict(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs := postgres.NewRecordStore(pool)
  	ctx := context.Background()
  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  	if _, err := rs.Insert(ctx, store.NewRecord{
  		ID: "same-id", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
  	}); err != nil {
  		t.Fatalf("first Insert: %v", err)
  	}

  	// Same id, DIFFERENT dedupe key: the ON CONFLICT arbiter does not cover the
  	// primary key, so this is a genuine surprise and must surface as a conflict.
  	_, err := rs.Insert(ctx, store.NewRecord{
  		ID: "same-id", StationID: 2000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
  	})
  	if !errors.Is(err, store.ErrConflict) {
  		t.Fatalf("Insert error = %v, want store.ErrConflict", err)
  	}
  }

  func TestClassifiedErrorsLeakNothing(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs := postgres.NewRecordStore(pool)
  	ctx := context.Background()
  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  	if _, err := rs.Insert(ctx, store.NewRecord{
  		ID: "leak", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
  	}); err != nil {
  		t.Fatalf("first Insert: %v", err)
  	}
  	_, err := rs.Insert(ctx, store.NewRecord{
  		ID: "leak", StationID: 2000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
  	})
  	if err == nil {
  		t.Fatal("expected a conflict")
  	}

  	// The message may name the SQLSTATE and nothing else. A *pgconn.PgError's
  	// own Error() plus its Detail/Hint/ConstraintName/ColumnName/TableName
  	// fields disclose schema and sometimes column values, and the API layer must
  	// never be in a position where echoing an error is dangerous.
  	msg := err.Error()
  	forbidden := []string{
  		"INSERT", "insert", "SELECT", "select", "weather_records",
  		"ux_records_station_obs", "pkey", "duplicate key", "Key (", "DETAIL", "HINT",
  	}
  	for _, bad := range forbidden {
  		if contains(msg, bad) {
  			t.Errorf("classified error %q contains %q", msg, bad)
  		}
  	}
  	if !contains(msg, "23505") {
  		t.Errorf("classified error %q does not name its SQLSTATE", msg)
  	}
  }

  func TestJSONBRoundTripsAllThirtyThreeFields(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs := postgres.NewRecordStore(pool)
  	ctx := context.Background()
  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	want := fullWeatherData()

  	if _, err := rs.Insert(ctx, store.NewRecord{
  		ID: "rt", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: want,
  	}); err != nil {
  		t.Fatalf("Insert: %v", err)
  	}

  	var got weather.WeatherData
  	if err := pool.QueryRow(ctx, "SELECT data FROM weather_records WHERE id = $1", "rt").Scan(&got); err != nil {
  		t.Fatalf("scanning data: %v", err)
  	}
  	// WeatherData is 33 comparable scalars, so == is a total comparison.
  	if got != want {
  		t.Fatalf("jsonb round trip changed the record:\n got %+v\nwant %+v", got, want)
  	}

  	// And the stored document has exactly 33 keys, which is what
  	// Stats.TotalDataPoints multiplies by.
  	var keys int
  	if err := pool.QueryRow(ctx,
  		"SELECT count(*) FROM jsonb_object_keys((SELECT data FROM weather_records WHERE id = $1))", "rt").
  		Scan(&keys); err != nil {
  		t.Fatalf("counting keys: %v", err)
  	}
  	if keys != weather.DataFieldsPerRecord {
  		t.Fatalf("stored jsonb has %d keys, want %d", keys, weather.DataFieldsPerRecord)
  	}
  }

  // contains is strings.Contains under a local name, so that the forbidden-token
  // loop reads as a single predicate.
  func contains(haystack, needle string) bool {
  	return len(needle) > 0 && len(haystack) >= len(needle) &&
  		indexOf(haystack, needle) >= 0
  }

  func indexOf(haystack, needle string) int {
  	for i := 0; i+len(needle) <= len(haystack); i++ {
  		if haystack[i:i+len(needle)] == needle {
  			return i
  		}
  	}
  	return -1
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `undefined: postgres.RecordStore` and `undefined: postgres.NewRecordStore` — the package does not compile.

- [ ] **Write the error classifier.** Create `internal/store/postgres/errors.go` with exactly this content:
  ```go
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
  ```

- [ ] **Write the record store and Insert.** Create `internal/store/postgres/records.go` with exactly this content:
  ```go
  package postgres

  import (
  	"context"

  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  )

  // recordColumns is the explicit column list of weather_records, in the exact
  // order and with the exact names store.Record's db tags declare.
  //
  // It is spelled out because SELECT * and RETURNING * are forbidden here:
  // pgx.RowToStructByName treats a column with no matching struct field as a
  // hard RUNTIME error, and RowToStructByNameLax was verified to behave
  // identically — Lax only relaxes struct fields with no matching column, never
  // the reverse. A star select would therefore couple every query in this
  // package to the table's full column list forever, and the next migration
  // would break all of them at runtime with no compile-time signal and no test
  // coverage unless the test database already had the new column.
  const recordColumns = `id, station_id, timestamp, observation_time, data, status, attempts,
         claim_ref, adopt_required, claimed_at, txid, output_index, block_height,
         chain_status, mined_at, error, created_at, processed_at`

  // recordColumnsAliased is recordColumns qualified with the r alias, for the
  // RETURNING clause of an `UPDATE weather_records AS r`.
  const recordColumnsAliased = `r.id, r.station_id, r.timestamp, r.observation_time, r.data,
         r.status, r.attempts, r.claim_ref, r.adopt_required, r.claimed_at, r.txid,
         r.output_index, r.block_height, r.chain_status, r.mined_at, r.error,
         r.created_at, r.processed_at`

  // insertRecordSQL is the poller's write.
  //
  // ON CONFLICT DO NOTHING against ux_records_station_obs is the structural
  // duplicate guard: Mongo had no unique index anywhere, so nothing prevented
  // duplicate rows before. A collision reports zero rows affected, which Insert
  // turns into inserted=false with a nil error.
  const insertRecordSQL = `
  INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  VALUES ($1, $2, $3, $4, $5)
  ON CONFLICT (station_id, observation_time) DO NOTHING`

  // RecordStore is the pgx/v5 implementation of store.RecordStore.
  type RecordStore struct {
  	db *pgxpool.Pool
  }

  // NewRecordStore returns a RecordStore backed by db.
  func NewRecordStore(db *pgxpool.Pool) *RecordStore {
  	return &RecordStore{db: db}
  }

  // Insert implements store.RecordStore.
  //
  // The Data field is passed as a plain weather.WeatherData value: pgtype's JSONB
  // codec has an encode plan for a struct (it marshals it) and a scan plan for a
  // struct pointer (it unmarshals), so no pgtype wrapper and no []byte hop is
  // needed in either direction.
  func (s *RecordStore) Insert(ctx context.Context, r store.NewRecord) (bool, error) {
  	ct, err := s.db.Exec(ctx, insertRecordSQL,
  		r.ID, r.StationID, r.Timestamp, r.ObservationTime, r.Data)
  	if err != nil {
  		return false, classify(err)
  	}
  	return ct.RowsAffected() == 1, nil
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1 -v -run 'TestInsert|TestClassified|TestJSONB'
  ```
  Expected output: `--- PASS: TestInsertReturnsTrueForAFreshRow`, `--- PASS: TestInsertDedupeIsFalseAndNotAnError`, `--- PASS: TestInsertPrimaryKeyCollisionIsErrConflict`, `--- PASS: TestClassifiedErrorsLeakNothing`, `--- PASS: TestJSONBRoundTripsAllThirtyThreeFields`, then `ok`.

- [ ] **Run the whole package and the lint gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/errors.go internal/store/postgres/records.go internal/store/postgres/records_insert_test.go && git commit -m "postgres: Insert with structural dedupe, and an error classifier that leaks nothing"
  ```

---

## Task 8: ClaimPending — the atomic claim, proven against a real race

**Files:**
- Modify: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/records_claim_test.go`

**Interfaces:**

Consumes (Task 1): `store.Record`, `store.Status`, `store.StatusPending`, `store.StatusProcessing`. Consumes (Task 2): the interface signature `ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]Record, error)`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `postgres.RecordStore`, `postgres.NewRecordStore`, `recordColumnsAliased`, `classify`, and — from `migrations_test.go` in package `postgres_test` — `storeSchema`; and from `records_insert_test.go`, `fullWeatherData()`. Do not redeclare any of them.

Produces:
```go
package postgres
func (s *RecordStore) ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]store.Record, error)
func collectRecords(rows pgx.Rows) ([]store.Record, error) // package-internal helper
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/records_claim_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"sort"
  	"sync"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  var _ func(context.Context, int, uuid.UUID) ([]store.Record, error) = (*postgres.RecordStore)(nil).ClaimPending

  // seedCallCount gives every seedPending call its own observation_time window.
  //
  // Without it the helper is silently NON-COMPOSABLE: (station_id,
  // observation_time) is the dedupe key of ux_records_station_obs, and a fixed
  // base plus i minutes means two calls for the SAME station both write
  // base+0 minutes and the second dies with SQLSTATE 23505. That is not
  // hypothetical — TestSetBlockHeightsNeverLowersAStationHeight calls
  // publishOneBatch twice on station 1000 with n=1, and it failed exactly this
  // way. Tests in a package run sequentially (no t.Parallel anywhere near
  // Postgres), so a plain counter is sufficient and needs no mutex.
  var seedCallCount int

  // seedPending inserts n pending rows in ONE transaction and returns their ids
  // in creation order.
  //
  // One transaction is the production shape: created_at defaults to now(), which
  // is transaction_timestamp(), so every row a poll writes shares one created_at
  // to the microsecond. Seeding them one statement at a time would give each row
  // a distinct created_at and would silently make every ordering test weaker.
  //
  // The dedupe key is (station_id, observation_time), so each CALL gets its own
  // day and each row inside a call its own minute. Repeat calls for one station
  // are therefore safe, which several tests depend on.
  func seedPending(t testing.TB, pool *pgxpool.Pool, n int, stationID int64) []string {
  	t.Helper()
  	ctx := context.Background()
  	ids := make([]string, 0, n)
  	seedCallCount++
  	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC).
  		Add(time.Duration(seedCallCount) * 24 * time.Hour)

  	tx, err := pool.Begin(ctx)
  	if err != nil {
  		t.Fatalf("seedPending: Begin: %v", err)
  	}
  	// Without this rollback a seeding failure is a WHOLE-PACKAGE CI TIMEOUT
  	// rather than a test failure: t.Fatalf calls runtime.Goexit while tx still
  	// holds a pooled connection, so the t.Cleanup(pool.Close) that storetest.Pool
  	// registered blocks forever inside puddle's WaitGroup and the package burns
  	// the full -timeout 10m before printing a goroutine dump that buries the real
  	// error. Measured: adding this line turned a 10-minute hang into a 0.10s
  	// failure. Rollback after a successful Commit is a no-op in pgx (it returns
  	// ErrTxClosed), so discarding the error is correct and keeps errcheck quiet
  	// without a //nolint.
  	defer func() { _ = tx.Rollback(ctx) }()
  	for i := range n {
  		id, idErr := uuid.NewV7()
  		if idErr != nil {
  			t.Fatalf("seedPending: uuid: %v", idErr)
  		}
  		ids = append(ids, id.String())
  		if _, execErr := tx.Exec(ctx, `
  			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  			VALUES ($1, $2, $3, $4, $5)`,
  			id.String(), stationID, base, base.Add(time.Duration(i)*time.Minute), fullWeatherData(),
  		); execErr != nil {
  			t.Fatalf("seedPending: insert %d: %v", i, execErr)
  		}
  	}
  	if commitErr := tx.Commit(ctx); commitErr != nil {
  		t.Fatalf("seedPending: Commit: %v", commitErr)
  	}
  	return ids
  }

  // TestClaimPendingNeverDoubleClaims is the headline invariant of this whole
  // plan, and the CI workflow repeats it with -count=5.
  //
  // What it replaces: the TypeScript claim was two statements with no lock, no
  // version guard and no conditional filter (find pending, then bulkWrite with
  // filter {_id: id} rather than {_id: id, status: 'pending'}). Overlapping pods
  // avoided double-publishing only by accident — both then spent from the same
  // funding basket and the loser's createAction failed as a double spend — and
  // under the new design the app passes no inputs at all, so the server funds
  // each pod from DIFFERENT fuel UTXOs and BOTH succeed. A SELECT-then-UPDATE
  // port would therefore be STRICTLY WORSE than the TypeScript it replaces.
  //
  // The assertions are INVARIANTS, never schedules: which worker wins which row
  // is scheduler-dependent, and asserting on that would flake immediately.
  func TestClaimPendingNeverDoubleClaims(t *testing.T) {
  	const (
  		workers = 12
  		seeded  = 40
  		batch   = 7
  	)
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	// Without this the test is silently vacuous: a pool smaller than the worker
  	// count serializes the race, so the claim would appear correct however it
  	// was written. This exact fixture defect left a sibling repository's
  	// SKIP LOCKED paths untested on the production engine.
  	if got := pool.Config().MaxConns; got < workers {
  		t.Fatalf("pool MaxConns = %d, need >= %d or this test cannot observe a race", got, workers)
  	}

  	seedPending(t, pool, seeded, 1000)
  	rs := postgres.NewRecordStore(pool)

  	var mu sync.Mutex
  	seen := make(map[string]int, seeded)
  	refs := make(map[uuid.UUID]int, workers)
  	errs := make([]error, 0, workers)

  	var wg sync.WaitGroup
  	start := make(chan struct{})
  	for range workers {
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			ref, refErr := uuid.NewV7()
  			if refErr != nil {
  				mu.Lock()
  				errs = append(errs, refErr)
  				mu.Unlock()
  				return
  			}
  			<-start
  			recs, err := rs.ClaimPending(ctx, batch, ref)
  			mu.Lock()
  			defer mu.Unlock()
  			if err != nil {
  				errs = append(errs, err)
  				return
  			}
  			for _, r := range recs {
  				seen[r.ID]++
  				if r.ClaimRef != nil {
  					refs[*r.ClaimRef]++
  				}
  			}
  		}()
  	}
  	close(start)
  	wg.Wait()

  	for _, err := range errs {
  		t.Errorf("worker error: %v", err)
  	}

  	doubled := make([]string, 0, len(seen))
  	worst := 0
  	for id, n := range seen {
  		if n > 1 {
  			doubled = append(doubled, id)
  		}
  		if n > worst {
  			worst = n
  		}
  	}
  	if len(doubled) != 0 {
  		t.Fatalf("claim double-allocated %d of %d rows (worst row claimed %d times)",
  			len(doubled), len(seen), worst)
  	}
  	if len(seen) > seeded {
  		t.Fatalf("claimed %d distinct rows from %d seeded", len(seen), seeded)
  	}

  	// Every claimed row must be processing with a lease and a ref, and the
  	// remaining rows must still be pending. If the claim leaked a row into
  	// processing without returning it, the pipeline would stall silently.
  	var processing, pending int
  	err := pool.QueryRow(ctx, `
  		SELECT count(*) FILTER (WHERE status = 'processing'),
  		       count(*) FILTER (WHERE status = 'pending')
  		  FROM weather_records`).Scan(&processing, &pending)
  	if err != nil {
  		t.Fatalf("counting: %v", err)
  	}
  	if processing != len(seen) {
  		t.Fatalf("processing rows = %d, but %d were returned to callers", processing, len(seen))
  	}
  	if processing+pending != seeded {
  		t.Fatalf("processing+pending = %d, want %d", processing+pending, seeded)
  	}

  	var leaseless, refless int
  	err = pool.QueryRow(ctx, `
  		SELECT count(*) FILTER (WHERE status = 'processing' AND claimed_at IS NULL),
  		       count(*) FILTER (WHERE status = 'processing' AND claim_ref IS NULL)
  		  FROM weather_records`).Scan(&leaseless, &refless)
  	if err != nil {
  		t.Fatalf("counting lease/ref: %v", err)
  	}
  	if leaseless != 0 || refless != 0 {
  		t.Fatalf("%d processing rows have no lease and %d have no ref, want 0 and 0", leaseless, refless)
  	}
  }

  // TestClaimPendingStampsExactlyOneRefPerCall is the regression gate on the
  // gen_random_uuid() defect: COALESCE(claim_ref, gen_random_uuid()) evaluates
  // the VOLATILE function once per updated row, so a 21-row claim produces 21
  // distinct refs and the batch label that the adopt design uses as its
  // idempotency key ceases to exist. The ref must therefore be a bind parameter.
  func TestClaimPendingStampsExactlyOneRefPerCall(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedPending(t, pool, 21, 1000)
  	rs := postgres.NewRecordStore(pool)

  	ref := uuid.Must(uuid.NewV7())
  	recs, err := rs.ClaimPending(ctx, 21, ref)
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	if len(recs) != 21 {
  		t.Fatalf("claimed %d rows, want 21", len(recs))
  	}
  	for _, r := range recs {
  		if r.ClaimRef == nil || *r.ClaimRef != ref {
  			t.Fatalf("row %s has ref %v, want %v", r.ID, r.ClaimRef, ref)
  		}
  		if r.Status != store.StatusProcessing {
  			t.Fatalf("row %s status = %q, want processing", r.ID, string(r.Status))
  		}
  		if r.ClaimedAt == nil {
  			t.Fatalf("row %s has no lease", r.ID)
  		}
  		if r.Attempts != 0 {
  			t.Fatalf("row %s attempts = %d; the claim must never spend the attempt budget", r.ID, r.Attempts)
  		}
  	}

  	var distinct int
  	if err := pool.QueryRow(ctx,
  		"SELECT count(DISTINCT claim_ref) FROM weather_records WHERE claim_ref IS NOT NULL").Scan(&distinct); err != nil {
  		t.Fatalf("counting refs: %v", err)
  	}
  	if distinct != 1 {
  		t.Fatalf("count(DISTINCT claim_ref) = %d after one 21-row claim, want 1", distinct)
  	}
  }

  // TestClaimPendingPreservesAPriorRef covers the reaped-row case: a row that
  // was claimed, reaped and re-claimed keeps its ORIGINAL ref, which is what
  // makes the adopt check able to look up the prior batch. One claim can
  // therefore legitimately return several distinct refs, and the publisher must
  // partition its batch rather than assuming one action per claim.
  func TestClaimPendingPreservesAPriorRef(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ids := seedPending(t, pool, 2, 1000)
  	rs := postgres.NewRecordStore(pool)

  	priorRef := uuid.Must(uuid.NewV7())
  	if _, err := pool.Exec(ctx,
  		"UPDATE weather_records SET claim_ref = $1 WHERE id = $2", priorRef, ids[0]); err != nil {
  		t.Fatalf("stamping a prior ref: %v", err)
  	}

  	newRef := uuid.Must(uuid.NewV7())
  	recs, err := rs.ClaimPending(ctx, 2, newRef)
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	if len(recs) != 2 {
  		t.Fatalf("claimed %d rows, want 2", len(recs))
  	}
  	byID := make(map[string]store.Record, len(recs))
  	for _, r := range recs {
  		byID[r.ID] = r
  	}
  	if got := byID[ids[0]].ClaimRef; got == nil || *got != priorRef {
  		t.Fatalf("row with a prior ref got %v, want the prior %v", got, priorRef)
  	}
  	if got := byID[ids[1]].ClaimRef; got == nil || *got != newRef {
  		t.Fatalf("fresh row got %v, want the new %v", got, newRef)
  	}
  }

  // TestClaimPendingIsFIFOAcrossATiedBatch pins the queue's batch-split behavior:
  // the first claim takes the oldest four rows and the second takes the next four.
  //
  // WHAT IT DOES NOT DO, stated because an earlier draft of this comment claimed
  // the opposite: it is NOT a regression gate on `, c.id ASC`. MEASURED on
  // postgres:17-alpine — with the tiebreaker deleted, and again with
  // ix_records_status_created also removed so the planner must sort, this test
  // still PASSED on three consecutive runs. At twenty rows in a freshly loaded
  // heap the untied query happens to agree with the tied one. The tiebreaker is
  // still mandatory, and the evidence for it is elsewhere:
  // TestCreatedAtIsTheTransactionTimestamp proves the whole batch shares one
  // created_at, and `LIMIT`/`OFFSET` over a non-total order is unspecified — at
  // production shape two legitimate plans for the identical untied query returned
  // 19 of 20 different rows at the same offset. Do not delete `, c.id ASC` on the
  // strength of this test staying green.
  //
  // The assertion compares SETS PER BATCH, never positions within a batch, and
  // that is a correctness requirement rather than a stylistic preference:
  // PostgreSQL does not define the order in which `UPDATE … RETURNING` emits
  // rows. The subquery's ORDER BY chooses WHICH rows are locked and updated; the
  // ModifyTable node then returns them in whatever order it processed them, which
  // today happens to follow the heap and would change the moment rows are updated
  // in place. What IS guaranteed, and what the queue depends on, is that the
  // FIRST claim takes the oldest four and the SECOND takes the next four.
  func TestClaimPendingIsFIFOAcrossATiedBatch(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ids := seedPending(t, pool, 10, 1000)
  	rs := postgres.NewRecordStore(pool)

  	// uuidv7 ids sort in generation order, so ids is already the intended FIFO
  	// order and the tiebreaker makes it the actual one.
  	first, err := rs.ClaimPending(ctx, 4, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("first claim: %v", err)
  	}
  	second, err := rs.ClaimPending(ctx, 4, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("second claim: %v", err)
  	}

  	batches := []struct {
  		label string
  		got   []store.Record
  		want  []string
  	}{
  		{"first claim", first, ids[0:4]},
  		{"second claim", second, ids[4:8]},
  	}
  	for _, b := range batches {
  		got := make([]string, 0, len(b.got))
  		for _, r := range b.got {
  			got = append(got, r.ID)
  		}
  		sort.Strings(got)
  		want := make([]string, 0, len(b.want))
  		want = append(want, b.want...)
  		sort.Strings(want)
  		if len(got) != len(want) {
  			t.Fatalf("%s returned %d rows, want %d", b.label, len(got), len(want))
  		}
  		for i := range want {
  			if got[i] != want[i] {
  				t.Fatalf("%s = %v, want the batch %v (seeded order %v)", b.label, got, want, ids)
  			}
  		}
  	}

  	// The two remaining rows must be the LAST two seeded, which is the property
  	// that actually says "FIFO" rather than "some four then some four". Read
  	// straight from the table rather than through List, which does not exist
  	// until Task 12.
  	rows, err := pool.Query(ctx,
  		"SELECT id FROM weather_records WHERE status = 'pending' ORDER BY id ASC")
  	if err != nil {
  		t.Fatalf("listing pending rows: %v", err)
  	}
  	remaining, err := pgx.CollectRows(rows, pgx.RowTo[string])
  	if err != nil {
  		t.Fatalf("collecting pending ids: %v", err)
  	}
  	if len(remaining) != 2 {
  		t.Fatalf("%d rows still pending, want 2", len(remaining))
  	}
  	left := map[string]bool{ids[8]: true, ids[9]: true}
  	for _, id := range remaining {
  		if !left[id] {
  			t.Fatalf("row %s is still pending but is not one of the last two seeded %v", id, ids[8:])
  		}
  	}
  }

  func TestClaimPendingOnAnEmptyQueueReturnsNoRows(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	recs, err := rs.ClaimPending(ctx, 10, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending on an empty queue: %v", err)
  	}
  	if len(recs) != 0 {
  		t.Fatalf("claimed %d rows from an empty queue", len(recs))
  	}
  }
  ```

- [ ] **Run it and see it fail to compile.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `(*postgres.RecordStore)(nil).ClaimPending undefined (type *postgres.RecordStore has no field or method ClaimPending)`.

- [ ] **Write the NAIVE implementation deliberately, so the test can be seen to have power.** A concurrency test that has never been observed to fail is worthless. Append exactly this to `internal/store/postgres/records.go`:
  ```go
  // naiveClaimSelectSQL and naiveClaimUpdateSQL are a TEMPORARY, DELIBERATELY
  // BROKEN claim. They exist for exactly one test run, so that
  // TestClaimPendingNeverDoubleClaims is observed to FAIL before the real claim
  // is written. Delete both, and the ClaimPending body that uses them, in the
  // next step.
  const naiveClaimSelectSQL = `
  SELECT id FROM weather_records
   WHERE status = 'pending'
   ORDER BY created_at ASC, id ASC
   LIMIT $1`

  const naiveClaimUpdateSQL = `
  UPDATE weather_records AS r
     SET status = 'processing', claimed_at = now(), claim_ref = COALESCE(r.claim_ref, $2)
   WHERE r.id = $1
  RETURNING ` + recordColumnsAliased

  // ClaimPending implements store.RecordStore. TEMPORARY NAIVE VERSION.
  func (s *RecordStore) ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]store.Record, error) {
  	rows, err := s.db.Query(ctx, naiveClaimSelectSQL, n)
  	if err != nil {
  		return nil, classify(err)
  	}
  	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
  	if err != nil {
  		return nil, classify(err)
  	}

  	out := make([]store.Record, 0, len(ids))
  	for _, id := range ids {
  		updated, uErr := s.db.Query(ctx, naiveClaimUpdateSQL, id, ref)
  		if uErr != nil {
  			return nil, classify(uErr)
  		}
  		recs, cErr := pgx.CollectRows(updated, pgx.RowToStructByName[store.Record])
  		if cErr != nil {
  			return nil, classify(cErr)
  		}
  		out = append(out, recs...)
  	}
  	return out, nil
  }
  ```
  and add `"github.com/google/uuid"` and `"github.com/jackc/pgx/v5"` to the file's import block.

- [ ] **Run it and SEE THE DOUBLE-ALLOCATION.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run TestClaimPendingNeverDoubleClaims -v
  ```
  Expected failure text, with the numbers varying by schedule but `worst` typically equal to the worker count: `claim double-allocated 7 of 7 rows (worst row claimed 12 times)`. All twelve workers read the same first seven pending ids in their unlocked SELECT and then each wrote them. **Do not proceed until you have seen this fail.** If it passes, the pool is too small (check the `MaxConns` guard fired) or the workers are not actually overlapping.

- [ ] **Replace it with the atomic claim.** In `internal/store/postgres/records.go`, DELETE `naiveClaimSelectSQL`, `naiveClaimUpdateSQL` and the temporary `ClaimPending`, and append exactly this in their place:
  ```go
  // claimSQL is the atomic claim.
  //
  // WHAT THIS IS, AND WHAT IT IS NOT. It is a mandatory REPLACEMENT for a
  // guarantee the system used to get by accident, not an optimization. In the
  // TypeScript the claim was never atomic: it read pending ids, then wrote them
  // with a filter of {_id: id} rather than {_id: id, status: 'pending'}, so any
  // overlapping process claimed the same rows. Duplicate publishing was
  // prevented only because both processes then spent from the same funding
  // basket and the loser's createAction failed as a double spend — funding-UTXO
  // contention was doing unintended duty as the only cross-process serializer of
  // RECORD processing. Under the new design the app passes no inputs and no
  // input BEEF, so it selects no funding UTXOs: two overlapping processes would
  // each be funded from DIFFERENT fuel outputs and BOTH would succeed, putting
  // the same readings on chain twice, paying for both, and orphaning whichever
  // transaction did not land last. Measured on this schema: the SELECT-then-
  // UPDATE shape double-claimed 399 of 420 rows with one row taken 12 times;
  // this shape double-claimed 0.
  //
  // FOR UPDATE SKIP LOCKED must be in the SUBQUERY — it is not legal on the
  // outer UPDATE. The inner SELECT takes a row-level exclusive lock on each
  // candidate inside the SAME statement as the UPDATE, so there is no
  // read-modify-write window at all. SKIP LOCKED rather than plain FOR UPDATE
  // matters operationally: plain FOR UPDATE would serialize correctly but block
  // for the holder's whole transaction, which next to a 60-second publish is
  // latency-fatal.
  //
  // SCOPE LIMIT, stated because the natural reading is more generous than the
  // truth: the row lock lives only for the claim transaction. Durable exclusion
  // afterwards is the status column. This closes the concurrent-claim window and
  // NOTHING else — it does not make publishing idempotent across a crash between
  // the action committing server-side and Complete landing. That window is the
  // adopt path's job, and neither substitutes for the other.
  //
  // attempts is deliberately untouched: the error classifier has sole ownership
  // of the attempt budget. ORDER BY c.id ASC is not decoration — created_at
  // defaults to now(), which is transaction_timestamp(), so every row a poll
  // wrote shares one created_at and the tiebreaker is what gives the FIFO queue
  // a total order. uuidv7 is what makes id a MEANINGFUL tiebreaker rather than
  // an arbitrary one: it sorts in generation order.
  const claimSQL = `
  UPDATE weather_records AS r
     SET status     = 'processing',
         claimed_at = now(),
         claim_ref  = COALESCE(r.claim_ref, $2)
   WHERE r.id IN (
           SELECT c.id
             FROM weather_records AS c
            WHERE c.status = 'pending'
            ORDER BY c.created_at ASC, c.id ASC
            LIMIT $1
              FOR UPDATE SKIP LOCKED
         )
  RETURNING ` + recordColumnsAliased

  // ClaimPending implements store.RecordStore.
  //
  // ref is a parameter rather than a gen_random_uuid() call inside the SQL
  // because gen_random_uuid() is VOLATILE: Postgres evaluates it once per
  // updated row, so a 21-row claim would stamp 21 different refs and the batch
  // label the adopt design uses as its idempotency key would not exist.
  // Measured: the inline form produced 21 distinct refs for 21 rows; this form
  // produces exactly 1.
  func (s *RecordStore) ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]store.Record, error) {
  	rows, err := s.db.Query(ctx, claimSQL, n, ref)
  	if err != nil {
  		return nil, classify(err)
  	}
  	return collectRecords(rows)
  }

  // collectRecords drains rows into []store.Record.
  //
  // pgx.CollectRows closes rows and returns rows.Err() itself, so there is no
  // separate Close or Err to forget. The db struct tags on store.Record are what
  // RowToStructByName matches against, which is why every query in this package
  // names its columns explicitly and in the same order.
  func collectRecords(rows pgx.Rows) ([]store.Record, error) {
  	recs, err := pgx.CollectRows(rows, pgx.RowToStructByName[store.Record])
  	if err != nil {
  		return nil, classify(err)
  	}
  	return recs, nil
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run TestClaimPending -v
  ```
  Expected output: `--- PASS` for `TestClaimPendingNeverDoubleClaims`, `TestClaimPendingStampsExactlyOneRefPerCall`, `TestClaimPendingPreservesAPriorRef`, `TestClaimPendingIsFIFOAcrossATiedBatch`, `TestClaimPendingOnAnEmptyQueueReturnsNoRows`, then `ok`.

- [ ] **Repeat the race, because one pass is weak evidence.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=10 -race -run TestClaimPendingNeverDoubleClaims
  ```
  Expected output: `ok` with no failures. This is the same shape the CI job runs at `-count=5`.

- [ ] **Reproduce the measured limit of the FIFO test, so you know what it is and is not worth.** Delete `, c.id ASC` from `claimSQL`'s subquery `ORDER BY`, and ALSO comment out the `CREATE INDEX … ix_records_status_created` statement in `migrations.sql` — dropping the index with `psql` does nothing, because `storetest.Fresh` re-applies the migration before every test and recreates it. Then run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=3 -run TestClaimPendingIsFIFOAcrossATiedBatch -v
  ```
  **Expected output: PASS, three times.** That is the measured result on `postgres:17-alpine`, and it is recorded here rather than left as a surprise: at twenty rows in a freshly loaded heap the untied query agrees with the tied one, with or without the index. So this test is NOT a regression gate on the tiebreaker, and its doc comment says so — check that it still does. If your run instead FAILS (`first claim = [...], want the batch [...]`), that is better news, not a problem: the test discriminates on your setup, so strengthen the comment to say so.

  Either way **restore `, c.id ASC` and `migrations.sql`**, then re-run the whole `TestClaimPending` set. The tiebreaker is mandatory regardless of this test: `LIMIT` over a non-total order is unspecified, `TestCreatedAtIsTheTransactionTimestamp` proves the order really is non-total, and at production shape two legitimate plans for the untied query returned 19 of 20 different rows at the same offset. A green test is not permission to delete it.

- [ ] **Confirm the plan Postgres chose.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && docker compose exec -T postgres psql -U postgres -d weatherproof_test -c "SET search_path TO wp_test_store; EXPLAIN SELECT c.id FROM weather_records AS c WHERE c.status = 'pending' ORDER BY c.created_at ASC, c.id ASC LIMIT 7 FOR UPDATE SKIP LOCKED;"
  ```
  Expected output: a plan containing `LockRows`, `Index Scan using ix_records_status_created` and `Index Cond: (status = 'pending'::text)`. (The schema is dropped at test cleanup, so run this while a test is not in progress; if the table is absent, re-run one test first. A `Seq Scan` here means `ix_records_status_created` is missing from the migration.)

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/records.go internal/store/postgres/records_claim_test.go && git commit -m "postgres: atomic claim via FOR UPDATE SKIP LOCKED, with one ref per batch"
  ```

---

## Task 9: Complete — records, app_stats and station counters in one transaction

**Files:**
- Modify: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/records_complete_test.go`

**Interfaces:**

Consumes (Task 1): `store.Publication`, `store.Stats`, `store.Record`, `store.StatusCompleted`, `store.ChainARCAccepted`, `weather.WeatherData`. Consumes (Task 2): `Complete(ctx context.Context, txID string, pubs []Publication) (Stats, error)`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `postgres.NewRecordStore`, `classify`. Consumes (Task 8): `collectRecords`, `seedPending` (from `records_claim_test.go`, package `postgres_test`), `fullWeatherData` (from `records_insert_test.go`), `storeSchema` (from `migrations_test.go`).

Produces:
```go
package postgres
type rowQuerier interface {
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
const statsSQL string
func readStats(ctx context.Context, q rowQuerier) (store.Stats, error)
func (s *RecordStore) Complete(ctx context.Context, txID string, pubs []store.Publication) (store.Stats, error)
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/records_complete_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"strings"
  	"testing"
  	"time"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  var _ func(context.Context, string, []store.Publication) (store.Stats, error) = (*postgres.RecordStore)(nil).Complete

  func TestCompleteMovesRecordsStatsAndStationsTogether(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, name, location, is_active) VALUES (1000, 'A', 'Bristol', true)"); err != nil {
  		t.Fatalf("seeding station: %v", err)
  	}
  	seedPending(t, pool, 3, 1000)

  	claimed, err := rs.ClaimPending(ctx, 3, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	if len(claimed) != 3 {
  		t.Fatalf("claimed %d rows, want 3", len(claimed))
  	}

  	pubs := make([]store.Publication, 0, len(claimed))
  	for i, r := range claimed {
  		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
  	}

  	stats, err := rs.Complete(ctx, "a1b2c3", pubs)
  	if err != nil {
  		t.Fatalf("Complete: %v", err)
  	}

  	// ONE transaction per CreateAction, not one per record.
  	if stats.TotalTx != 1 {
  		t.Errorf("TotalTx = %d, want 1 (one per action, never per record)", stats.TotalTx)
  	}
  	if stats.TotalRecords != 3 {
  		t.Errorf("TotalRecords = %d, want 3", stats.TotalRecords)
  	}
  	if stats.TotalDataPoints() != 99 {
  		t.Errorf("TotalDataPoints = %d, want 99", stats.TotalDataPoints())
  	}
  	if stats.ActiveStations != 1 {
  		t.Errorf("ActiveStations = %d, want 1", stats.ActiveStations)
  	}
  	if stats.LastRecordWrite == nil {
  		t.Error("LastRecordWrite is nil after Complete")
  	}

  	// Every record row is fully published, which the
  	// ck_records_completed_published constraint also enforces.
  	rows, err := pool.Query(ctx, `
  		SELECT status, txid, output_index, chain_status, processed_at IS NOT NULL,
  		       claimed_at IS NULL, adopt_required, error IS NULL
  		  FROM weather_records ORDER BY output_index ASC`)
  	if err != nil {
  		t.Fatalf("reading records: %v", err)
  	}
  	defer rows.Close()
  	seen := 0
  	for rows.Next() {
  		var status, txid, chain string
  		var vout int32
  		var hasProcessed, leaseCleared, adopt, errCleared bool
  		if scanErr := rows.Scan(&status, &txid, &vout, &chain, &hasProcessed, &leaseCleared, &adopt, &errCleared); scanErr != nil {
  			t.Fatalf("scan: %v", scanErr)
  		}
  		if status != string(store.StatusCompleted) {
  			t.Errorf("status = %q, want completed", status)
  		}
  		if txid != "a1b2c3" {
  			t.Errorf("txid = %q, want a1b2c3", txid)
  		}
  		if chain != string(store.ChainARCAccepted) {
  			t.Errorf("chain_status = %q, want arc-accepted", chain)
  		}
  		if int(vout) != seen {
  			t.Errorf("output_index = %d, want %d", vout, seen)
  		}
  		if !hasProcessed || !leaseCleared || adopt || !errCleared {
  			t.Errorf("row %d: processed=%v leaseCleared=%v adopt=%v errCleared=%v",
  				seen, hasProcessed, leaseCleared, adopt, errCleared)
  		}
  		seen++
  	}
  	if rowsErr := rows.Err(); rowsErr != nil {
  		t.Fatalf("rows: %v", rowsErr)
  	}
  	if seen != 3 {
  		t.Fatalf("saw %d rows, want 3", seen)
  	}

  	// The station counters moved in the same transaction.
  	var txRecords int64
  	var lastTemp float64
  	var lastConditions string
  	var lastReading time.Time
  	err = pool.QueryRow(ctx, `
  		SELECT tx_records, last_temp, last_conditions, last_reading
  		  FROM stations WHERE station_id = 1000`).
  		Scan(&txRecords, &lastTemp, &lastConditions, &lastReading)
  	if err != nil {
  		t.Fatalf("reading station: %v", err)
  	}
  	if txRecords != 3 {
  		t.Errorf("stations.tx_records = %d, want 3", txRecords)
  	}
  	// air_temperature is FieldInteger, so last_temp can only ever be integral —
  	// a fractional example value such as 18.3 is unachievable by construction.
  	if lastTemp != 18 {
  		t.Errorf("stations.last_temp = %v, want 18", lastTemp)
  	}
  	if lastConditions != "Clear" {
  		t.Errorf("stations.last_conditions = %q, want Clear", lastConditions)
  	}
  	if lastReading.IsZero() {
  		t.Error("stations.last_reading was not set")
  	}
  }

  func TestCompleteAcrossTwoStationsSplitsTheCounters(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	for _, id := range []int64{1000, 2000} {
  		if _, err := pool.Exec(ctx,
  			"INSERT INTO stations (station_id, is_active) VALUES ($1, true)", id); err != nil {
  			t.Fatalf("seeding station %d: %v", id, err)
  		}
  	}
  	seedPending(t, pool, 2, 1000)
  	seedPending(t, pool, 3, 2000)

  	claimed, err := rs.ClaimPending(ctx, 5, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	pubs := make([]store.Publication, 0, len(claimed))
  	for i, r := range claimed {
  		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
  	}
  	if _, completeErr := rs.Complete(ctx, "multi", pubs); completeErr != nil {
  		t.Fatalf("Complete: %v", completeErr)
  	}

  	counts := map[int64]int64{}
  	rows, err := pool.Query(ctx, "SELECT station_id, tx_records FROM stations ORDER BY station_id")
  	if err != nil {
  		t.Fatalf("reading stations: %v", err)
  	}
  	defer rows.Close()
  	for rows.Next() {
  		var id, n int64
  		if scanErr := rows.Scan(&id, &n); scanErr != nil {
  			t.Fatalf("scan: %v", scanErr)
  		}
  		counts[id] = n
  	}
  	if rowsErr := rows.Err(); rowsErr != nil {
  		t.Fatalf("rows: %v", rowsErr)
  	}
  	if counts[1000] != 2 || counts[2000] != 3 {
  		t.Fatalf("tx_records = %v, want map[1000:2 2000:3]", counts)
  	}

  	// Still ONE transaction, even across two stations.
  	var totalTx int64
  	if statsErr := pool.QueryRow(ctx, "SELECT total_tx FROM app_stats WHERE id = 1").Scan(&totalTx); statsErr != nil {
  		t.Fatalf("reading app_stats: %v", statsErr)
  	}
  	if totalTx != 1 {
  		t.Fatalf("total_tx = %d, want 1", totalTx)
  	}
  }

  func TestCompleteIsANoOpWhenNothingIsProcessing(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, is_active) VALUES (1000, true)"); err != nil {
  		t.Fatalf("seeding station: %v", err)
  	}
  	seedPending(t, pool, 2, 1000)
  	claimed, err := rs.ClaimPending(ctx, 2, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	pubs := make([]store.Publication, 0, len(claimed))
  	for i, r := range claimed {
  		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
  	}

  	first, err := rs.Complete(ctx, "once", pubs)
  	if err != nil {
  		t.Fatalf("first Complete: %v", err)
  	}
  	if first.TotalTx != 1 || first.TotalRecords != 2 {
  		t.Fatalf("first Complete stats = %+v, want TotalTx 1 TotalRecords 2", first)
  	}

  	// A retried Complete must not double-count: the rows are no longer
  	// processing, so the UPDATE matches nothing.
  	second, err := rs.Complete(ctx, "once", pubs)
  	if err != nil {
  		t.Fatalf("second Complete: %v", err)
  	}
  	if second.TotalTx != 1 || second.TotalRecords != 2 {
  		t.Fatalf("second Complete stats = %+v, want the same TotalTx 1 TotalRecords 2", second)
  	}

  	var txRecords int64
  	if stationErr := pool.QueryRow(ctx,
  		"SELECT tx_records FROM stations WHERE station_id = 1000").Scan(&txRecords); stationErr != nil {
  		t.Fatalf("reading station: %v", stationErr)
  	}
  	if txRecords != 2 {
  		t.Fatalf("stations.tx_records = %d after a retried Complete, want 2", txRecords)
  	}

  	// An empty publication list is also a no-op.
  	empty, err := rs.Complete(ctx, "none", []store.Publication{})
  	if err != nil {
  		t.Fatalf("Complete with no publications: %v", err)
  	}
  	if empty.TotalTx != 1 {
  		t.Fatalf("TotalTx = %d after an empty Complete, want 1", empty.TotalTx)
  	}
  }

  // TestCompleteIsTolerantOfAMissingStation covers the case the SQL deliberately
  // allows: a record can arrive before its station upsert has landed, so
  // bumpStationsSQL matching nothing must NOT fail the publish. The station row
  // is not created either — it is an UPDATE, and inventing a station here would
  // put a row on the dashboard that the poller never reported.
  func TestCompleteIsTolerantOfAMissingStation(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	seedPending(t, pool, 1, 4242)
  	claimed, err := rs.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	pubs := []store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}
  	if _, completeErr := rs.Complete(ctx, "orphan", pubs); completeErr != nil {
  		t.Fatalf("Complete with no stations row: %v", completeErr)
  	}

  	var status string
  	if readErr := pool.QueryRow(ctx,
  		"SELECT status FROM weather_records WHERE id = $1", claimed[0].ID).Scan(&status); readErr != nil {
  		t.Fatalf("reading record: %v", readErr)
  	}
  	if status != string(store.StatusCompleted) {
  		t.Fatalf("status = %q, want completed", status)
  	}

  	var stations int
  	if countErr := pool.QueryRow(ctx,
  		"SELECT count(*) FROM stations WHERE station_id = 4242").Scan(&stations); countErr != nil {
  		t.Fatalf("counting stations: %v", countErr)
  	}
  	if stations != 0 {
  		t.Fatalf("stations rows for 4242 = %d, want 0: Complete must not invent a station", stations)
  	}

  	// Completing an id that does not exist at all is also a no-op rather than an
  	// error, and must not move total_tx a second time.
  	ghost, err := rs.Complete(ctx, "ghost", []store.Publication{{RecordID: "does-not-exist", OutputIndex: 0}})
  	if err != nil {
  		t.Fatalf("Complete for a missing id: %v", err)
  	}
  	if ghost.TotalTx != 1 {
  		t.Fatalf("TotalTx = %d after completing a missing id, want 1", ghost.TotalTx)
  	}
  }

  // TestCompleteRollsBackWhollyOnFailure is the atomicity assertion, and it needs
  // a REAL failure to make.
  //
  // An earlier draft of this test conceded in a comment that it could not produce
  // one (txid is unconstrained text, so no argument is invalid) and asserted the
  // two no-op paths instead — which TestCompleteIsANoOpWhenNothingIsProcessing
  // already covers, leaving the central claim of Complete untested behind a name
  // that said otherwise. A temporary CHECK constraint on the station counter is
  // the missing lever: the record UPDATE and the app_stats bump succeed, the
  // station bump then violates the constraint, and the whole transaction must
  // unwind. Without pgx.BeginFunc's rollback, weather_records would be left
  // 'completed' with total_tx at 0 and no way to notice.
  func TestCompleteRollsBackWhollyOnFailure(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, is_active) VALUES (1000, true)"); err != nil {
  		t.Fatalf("seeding station: %v", err)
  	}
  	seedPending(t, pool, 3, 1000)
  	claimed, err := rs.ClaimPending(ctx, 3, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	if len(claimed) != 3 {
  		t.Fatalf("claimed %d rows, want 3", len(claimed))
  	}
  	pubs := make([]store.Publication, 0, len(claimed))
  	for i, r := range claimed {
  		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
  	}

  	// Three records land on one station, so the bump sets tx_records = 3 and
  	// this rejects it with 23514.
  	if _, addErr := pool.Exec(ctx,
  		"ALTER TABLE stations ADD CONSTRAINT ck_probe CHECK (tx_records < 3)"); addErr != nil {
  		t.Fatalf("adding the probe constraint: %v", addErr)
  	}

  	_, completeErr := rs.Complete(ctx, "wedged", pubs)
  	if completeErr == nil {
  		t.Fatal("Complete succeeded while the station bump was constrained; " +
  			"the failure this test needs did not happen")
  	}
  	// classify maps a check violation to the opaque bucket, so the constraint
  	// NAME must not travel with it.
  	if !errors.Is(completeErr, postgres.ErrOperation) {
  		t.Errorf("Complete error = %v, want postgres.ErrOperation", completeErr)
  	}
  	if strings.Contains(completeErr.Error(), "ck_probe") {
  		t.Errorf("Complete error %q names the constraint", completeErr.Error())
  	}

  	// NOTHING may have committed: not the records, not app_stats, not the
  	// station counter.
  	var processing, completed int
  	if countErr := pool.QueryRow(ctx, `
  		SELECT count(*) FILTER (WHERE status = 'processing'),
  		       count(*) FILTER (WHERE status = 'completed')
  		  FROM weather_records`).Scan(&processing, &completed); countErr != nil {
  		t.Fatalf("counting records: %v", countErr)
  	}
  	if processing != 3 || completed != 0 {
  		t.Fatalf("after the rollback: processing = %d, completed = %d, want 3 and 0",
  			processing, completed)
  	}
  	var totalTx, totalRecords, txRecords int64
  	if statsErr := pool.QueryRow(ctx,
  		"SELECT total_tx, total_records FROM app_stats WHERE id = 1").Scan(&totalTx, &totalRecords); statsErr != nil {
  		t.Fatalf("reading app_stats: %v", statsErr)
  	}
  	if totalTx != 0 || totalRecords != 0 {
  		t.Fatalf("app_stats moved despite the rollback: total_tx = %d, total_records = %d",
  			totalTx, totalRecords)
  	}
  	if stationErr := pool.QueryRow(ctx,
  		"SELECT tx_records FROM stations WHERE station_id = 1000").Scan(&txRecords); stationErr != nil {
  		t.Fatalf("reading station: %v", stationErr)
  	}
  	if txRecords != 0 {
  		t.Fatalf("stations.tx_records = %d despite the rollback, want 0", txRecords)
  	}

  	// And with the constraint gone the identical call succeeds, which proves the
  	// rollback left the batch in a retryable state rather than a stuck one.
  	if _, dropErr := pool.Exec(ctx,
  		"ALTER TABLE stations DROP CONSTRAINT ck_probe"); dropErr != nil {
  		t.Fatalf("dropping the probe constraint: %v", dropErr)
  	}
  	stats, err := rs.Complete(ctx, "wedged", pubs)
  	if err != nil {
  		t.Fatalf("retried Complete: %v", err)
  	}
  	if stats.TotalTx != 1 || stats.TotalRecords != 3 {
  		t.Fatalf("retried Complete stats = %+v, want TotalTx 1 TotalRecords 3", stats)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `(*postgres.RecordStore)(nil).Complete undefined (type *postgres.RecordStore has no field or method Complete)`.

- [ ] **Write the implementation.** Append exactly this to `internal/store/postgres/records.go`, and add `"time"` and `"github.com/bsv-blockchain-demos/weather-proof/internal/weather"` to its import block:
  ```go
  // completeRecordsSQL publishes one batch under one txid.
  //
  // The `AND r.status = 'processing'` predicate is what makes a retried Complete
  // a no-op instead of a double count. The publication list arrives as two
  // parallel arrays through unnest rather than as N statements, so the whole
  // batch is one round trip. Two empty arrays are a clean no-op, which is what
  // the retried and empty-batch paths rely on.
  //
  // `FROM unnest(...) AS u(cols)` and NOT
  // `FROM (SELECT * FROM unnest(...) AS t(cols)) AS u`: the wrapping subquery
  // would work, but it puts a literal `SELECT *` in the statement, which the
  // no-star-selects guard in sqldiscipline_test.go rejects on sight — and that
  // guard exists for a good reason, so the statement is written not to need an
  // exemption.
  const completeRecordsSQL = `
  UPDATE weather_records AS r
     SET status = 'completed', txid = $1, output_index = u.output_index,
         chain_status = 'arc-accepted', processed_at = now(), error = NULL,
         claimed_at = NULL, adopt_required = false
    FROM unnest($2::text[], $3::int[]) AS u(record_id, output_index)
   WHERE r.id = u.record_id AND r.status = 'processing'
  RETURNING r.station_id, r.timestamp, r.data`

  // bumpAppStatsSQL increments the singleton. total_tx moves by ONE per action,
  // never per record: counting per record is one of the two stats bugs this
  // design fixes.
  const bumpAppStatsSQL = `
  UPDATE app_stats
     SET total_tx = total_tx + 1,
         total_records = total_records + $1,
         last_record_write = greatest(coalesce(last_record_write, now()), now()),
         updated_at = now()
   WHERE id = 1`

  // bumpStationsSQL moves the per-station counters. A station with no row simply
  // matches nothing, which is deliberate: a record can legitimately arrive
  // before its station upsert has landed, and failing the publish for that would
  // be worse than a missing counter. It is an UPDATE and never an upsert, so a
  // station the poller has not reported never appears on the dashboard.
  //
  // last_temp and last_conditions move ONLY when this batch is newer, under the
  // same predicate as last_reading. Assigning them unconditionally (which an
  // earlier draft did) desynchronises them: a batch completing late with an OLDER
  // reading would leave last_reading at the newer instant while overwriting the
  // temperature with the older one, so the dashboard would show a stale
  // temperature stamped with a fresh time and nothing would detect it. The fake
  // has the same rule, and Task 19's conformance suite asserts it on both.
  const bumpStationsSQL = `
  UPDATE stations AS s
     SET tx_records = s.tx_records + u.n,
         last_reading = greatest(coalesce(s.last_reading, u.ts), u.ts),
         last_temp = CASE WHEN s.last_reading IS NULL OR u.ts > s.last_reading
                          THEN u.temp ELSE s.last_temp END,
         last_conditions = CASE WHEN s.last_reading IS NULL OR u.ts > s.last_reading
                                THEN u.conditions ELSE s.last_conditions END,
         updated_at = now()
    FROM unnest($1::bigint[], $2::bigint[], $3::timestamptz[],
                $4::double precision[], $5::text[])
           AS u(station_id, n, ts, temp, conditions)
   WHERE s.station_id = u.station_id`

  // statsSQL is the four dashboard values.
  //
  // activeStations is a LIVE count rather than a stored counter (~20 rows, free),
  // so it can never be stuck at 0 — which was the other of the two stats bugs.
  // total_records is multiplied by weather.DataFieldsPerRecord in Go, never by a
  // literal 33 in SQL: the constant already exists once in this module and a
  // second copy is how the two drift.
  const statsSQL = `
  SELECT (SELECT count(*) FROM stations WHERE is_active) AS active_stations,
         total_tx, total_records, last_record_write
    FROM app_stats WHERE id = 1`

  // rowQuerier is the single-row query surface shared by *pgxpool.Pool and
  // pgx.Tx, so readStats can run either inside a transaction or standalone.
  type rowQuerier interface {
  	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
  }

  // readStats reads the four dashboard values through q.
  func readStats(ctx context.Context, q rowQuerier) (store.Stats, error) {
  	var out store.Stats
  	err := q.QueryRow(ctx, statsSQL).
  		Scan(&out.ActiveStations, &out.TotalTx, &out.TotalRecords, &out.LastRecordWrite)
  	if err != nil {
  		return store.Stats{}, classify(err)
  	}
  	return out, nil
  }

  // stationDelta is the aggregate one Complete applies to one station.
  type stationDelta struct {
  	n          int64
  	ts         time.Time
  	temp       float64
  	conditions string
  }

  // movedRecord is the projection completeRecordsSQL returns.
  type movedRecord struct {
  	stationID int64
  	ts        time.Time
  	data      weather.WeatherData
  }

  // Complete implements store.RecordStore.
  //
  // Records, app_stats and the station counters move in ONE transaction. The
  // atomicity requirement transfers from the Mongo session the TypeScript used;
  // the mechanism does not. pgx.BeginFunc commits on a nil return and rolls back
  // otherwise, and it is a free function taking a structural interface, so
  // *pgxpool.Pool satisfies it directly and there is no defer-rollback
  // boilerplate to get wrong (and no shadowed err in a deferred closure, which
  // govet's shadow check would reject).
  func (s *RecordStore) Complete(ctx context.Context, txID string, pubs []store.Publication) (store.Stats, error) {
  	var out store.Stats
  	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
  		ids := make([]string, 0, len(pubs))
  		vouts := make([]int32, 0, len(pubs))
  		for _, p := range pubs {
  			ids = append(ids, p.RecordID)
  			vouts = append(vouts, p.OutputIndex)
  		}

  		rows, qErr := tx.Query(ctx, completeRecordsSQL, txID, ids, vouts)
  		if qErr != nil {
  			return classify(qErr)
  		}
  		moved, cErr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (movedRecord, error) {
  			var m movedRecord
  			scanErr := row.Scan(&m.stationID, &m.ts, &m.data)
  			return m, scanErr
  		})
  		if cErr != nil {
  			return classify(cErr)
  		}

  		if len(moved) == 0 {
  			// Nothing was in processing. A retried Complete, or one for ids
  			// that no longer exist, must not touch a single counter.
  			stats, sErr := readStats(ctx, tx)
  			if sErr != nil {
  				return sErr
  			}
  			out = stats
  			return nil
  		}

  		deltas := make(map[int64]*stationDelta, len(moved))
  		order := make([]int64, 0, len(moved))
  		for _, m := range moved {
  			d, ok := deltas[m.stationID]
  			if !ok {
  				d = &stationDelta{}
  				deltas[m.stationID] = d
  				order = append(order, m.stationID)
  			}
  			d.n++
  			if m.ts.After(d.ts) {
  				d.ts = m.ts
  				// air_temperature is FieldInteger in the wire schema, so
  				// last_temp can only ever hold an integral value. The column
  				// stays double precision because the API contract needs a
  				// *float64 that is never omitted.
  				d.temp = float64(m.data.AirTemperature)
  				d.conditions = m.data.Conditions
  			}
  		}

  		stationIDs := make([]int64, 0, len(order))
  		counts := make([]int64, 0, len(order))
  		timestamps := make([]time.Time, 0, len(order))
  		temps := make([]float64, 0, len(order))
  		conditions := make([]string, 0, len(order))
  		for _, id := range order {
  			d := deltas[id]
  			stationIDs = append(stationIDs, id)
  			counts = append(counts, d.n)
  			timestamps = append(timestamps, d.ts)
  			temps = append(temps, d.temp)
  			conditions = append(conditions, d.conditions)
  		}

  		if _, execErr := tx.Exec(ctx, bumpAppStatsSQL, int64(len(moved))); execErr != nil {
  			return classify(execErr)
  		}
  		if _, execErr := tx.Exec(ctx, bumpStationsSQL,
  			stationIDs, counts, timestamps, temps, conditions); execErr != nil {
  			return classify(execErr)
  		}

  		stats, sErr := readStats(ctx, tx)
  		if sErr != nil {
  			return sErr
  		}
  		out = stats
  		return nil
  	})
  	if err != nil {
  		return store.Stats{}, err
  	}
  	return out, nil
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run TestComplete -v
  ```
  Expected output: `--- PASS` for `TestCompleteMovesRecordsStatsAndStationsTogether`, `TestCompleteAcrossTwoStationsSplitsTheCounters`, `TestCompleteIsANoOpWhenNothingIsProcessing`, `TestCompleteIsTolerantOfAMissingStation`, `TestCompleteRollsBackWhollyOnFailure`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/records.go internal/store/postgres/records_complete_test.go && git commit -m "postgres: Complete moves records, stats and station counters in one transaction"
  ```

---

## Task 10: The three classifier writes — FailPermanent, RequeueInfra, MarkUnknown

**Files:**
- Modify: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/records_classify_test.go`

**Interfaces:**

Consumes (Task 1): `store.StatusFailed`, `store.StatusPending`, `store.StatusProcessing`. Consumes (Task 2): `FailPermanent(ctx context.Context, ids []string, reason string) error`, `RequeueInfra(ctx context.Context, ids []string, reason string) error`, `MarkUnknown(ctx context.Context, ids []string, reason string) error`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `postgres.NewRecordStore`, `classify`. Consumes (Task 8): `recordColumnsAliased`, `seedPending`; plus `storeSchema` from `migrations_test.go`.

Produces:
```go
package postgres
func (s *RecordStore) FailPermanent(ctx context.Context, ids []string, reason string) error
func (s *RecordStore) RequeueInfra(ctx context.Context, ids []string, reason string) error
func (s *RecordStore) MarkUnknown(ctx context.Context, ids []string, reason string) error
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/records_classify_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"testing"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  var (
  	_ func(context.Context, []string, string) error = (*postgres.RecordStore)(nil).FailPermanent
  	_ func(context.Context, []string, string) error = (*postgres.RecordStore)(nil).RequeueInfra
  	_ func(context.Context, []string, string) error = (*postgres.RecordStore)(nil).MarkUnknown
  )

  // rowState is the shape every assertion in this file reads.
  type rowState struct {
  	status        string
  	attempts      int32
  	adoptRequired bool
  	errText       *string
  	hasLease      bool
  	hasProcessed  bool
  	hasRef        bool
  }

  func readRowState(t testing.TB, pool *pgxpool.Pool, id string) rowState {
  	t.Helper()
  	var st rowState
  	err := pool.QueryRow(context.Background(), `
  		SELECT status, attempts, adopt_required, error,
  		       claimed_at IS NOT NULL, processed_at IS NOT NULL, claim_ref IS NOT NULL
  		  FROM weather_records WHERE id = $1`, id).
  		Scan(&st.status, &st.attempts, &st.adoptRequired, &st.errText,
  			&st.hasLease, &st.hasProcessed, &st.hasRef)
  	if err != nil {
  		t.Fatalf("readRowState(%s): %v", id, err)
  	}
  	return st
  }

  // claimTwo seeds and claims two rows, returning the store and their ids.
  func claimTwo(t testing.TB, pool *pgxpool.Pool) (*postgres.RecordStore, []string) {
  	t.Helper()
  	seedPending(t, pool, 2, 1000)
  	rs := postgres.NewRecordStore(pool)
  	claimed, err := rs.ClaimPending(context.Background(), 2, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	ids := make([]string, 0, len(claimed))
  	for _, r := range claimed {
  		ids = append(ids, r.ID)
  	}
  	return rs, ids
  }

  func TestFailPermanentSpendsTheAttemptBudget(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs, ids := claimTwo(t, pool)
  	ctx := context.Background()

  	if err := rs.FailPermanent(ctx, ids, "script too large"); err != nil {
  		t.Fatalf("FailPermanent: %v", err)
  	}
  	for _, id := range ids {
  		st := readRowState(t, pool, id)
  		if st.status != string(store.StatusFailed) {
  			t.Errorf("%s status = %q, want failed", id, st.status)
  		}
  		if st.attempts != 1 {
  			t.Errorf("%s attempts = %d, want 1 (only a permanent error spends the budget)", id, st.attempts)
  		}
  		if st.adoptRequired {
  			t.Errorf("%s adopt_required = true; a permanent failure needs no adopt check", id)
  		}
  		if st.errText == nil || *st.errText != "script too large" {
  			t.Errorf("%s error = %v, want %q", id, st.errText, "script too large")
  		}
  		if st.hasLease {
  			t.Errorf("%s still holds a lease", id)
  		}
  		if !st.hasProcessed {
  			t.Errorf("%s has no processed_at", id)
  		}
  	}
  }

  func TestRequeueInfraKeepsTheBudgetAndNeedsNoAdopt(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs, ids := claimTwo(t, pool)
  	ctx := context.Background()

  	if err := rs.RequeueInfra(ctx, ids, "storage server unreachable"); err != nil {
  		t.Fatalf("RequeueInfra: %v", err)
  	}
  	for _, id := range ids {
  		st := readRowState(t, pool, id)
  		if st.status != string(store.StatusPending) {
  			t.Errorf("%s status = %q, want pending", id, st.status)
  		}
  		if st.attempts != 0 {
  			t.Errorf("%s attempts = %d, want 0 (infra errors never spend the budget)", id, st.attempts)
  		}
  		if st.adoptRequired {
  			t.Errorf("%s adopt_required = true; the outcome is known to be a non-publish", id)
  		}
  		if st.hasLease {
  			t.Errorf("%s still holds a lease", id)
  		}
  		if !st.hasRef {
  			t.Errorf("%s lost its claim ref", id)
  		}
  	}
  }

  func TestMarkUnknownRequiresAnAdoptCheck(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	rs, ids := claimTwo(t, pool)
  	ctx := context.Background()

  	if err := rs.MarkUnknown(ctx, ids, "publish outcome ambiguous"); err != nil {
  		t.Fatalf("MarkUnknown: %v", err)
  	}
  	for _, id := range ids {
  		st := readRowState(t, pool, id)
  		if st.status != string(store.StatusPending) {
  			t.Errorf("%s status = %q, want pending", id, st.status)
  		}
  		if st.attempts != 0 {
  			t.Errorf("%s attempts = %d, want 0", id, st.attempts)
  		}
  		if !st.adoptRequired {
  			t.Errorf("%s adopt_required = false; an ambiguous outcome MUST force an adopt check, "+
  				"or a row whose prior batch did publish is broadcast a second time", id)
  		}
  		if !st.hasRef {
  			t.Errorf("%s lost its claim ref, which the adopt check needs", id)
  		}
  		if st.hasLease {
  			t.Errorf("%s still holds a lease", id)
  		}
  	}
  }

  func TestClassifierWritesOnlyTouchProcessingRows(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	pendingIDs := seedPending(t, pool, 2, 1000)
  	rs := postgres.NewRecordStore(pool)

  	// Nothing has been claimed, so all three writes must be no-ops.
  	if err := rs.FailPermanent(ctx, pendingIDs, "x"); err != nil {
  		t.Fatalf("FailPermanent: %v", err)
  	}
  	if err := rs.RequeueInfra(ctx, pendingIDs, "y"); err != nil {
  		t.Fatalf("RequeueInfra: %v", err)
  	}
  	if err := rs.MarkUnknown(ctx, pendingIDs, "z"); err != nil {
  		t.Fatalf("MarkUnknown: %v", err)
  	}
  	for _, id := range pendingIDs {
  		st := readRowState(t, pool, id)
  		if st.status != string(store.StatusPending) {
  			t.Errorf("%s status = %q, want an untouched pending", id, st.status)
  		}
  		if st.attempts != 0 || st.adoptRequired || st.errText != nil {
  			t.Errorf("%s was modified: %+v", id, st)
  		}
  	}

  	// An unknown id is also a no-op rather than an error.
  	if err := rs.FailPermanent(ctx, []string{"no-such-row"}, "x"); err != nil {
  		t.Fatalf("FailPermanent for an unknown id: %v", err)
  	}
  	// An empty id list is a no-op.
  	if err := rs.MarkUnknown(ctx, nil, "x"); err != nil {
  		t.Fatalf("MarkUnknown with a nil slice: %v", err)
  	}
  }
  ```
- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `(*postgres.RecordStore)(nil).FailPermanent undefined (type *postgres.RecordStore has no field or method FailPermanent)`, plus the same for `RequeueInfra` and `MarkUnknown`.

- [ ] **Write the implementation.** Append exactly this to `internal/store/postgres/records.go`:
  ```go
  // The three terminal writes the publisher's error classifier makes. All three
  // share the same guard — `AND r.status = 'processing'` — so a write for a row
  // that was already reaped, already completed or never claimed is a no-op
  // rather than a corrupting overwrite.
  //
  // What differs between them is exactly the three columns that carry the
  // classification, and each difference is load-bearing:
  //
  //	FailPermanent  status=failed   attempts+1  adopt_required=false
  //	RequeueInfra   status=pending  attempts    adopt_required=false
  //	MarkUnknown    status=pending  attempts    adopt_required=TRUE
  //
  // Only a permanent error spends the attempt budget. Only an AMBIGUOUS outcome
  // sets adopt_required: it is the bit that makes the next claim run an adopt
  // check first, and without it a row whose prior batch did in fact publish gets
  // broadcast a second time. Preserving claim_ref alone is not enough, because a
  // pending row with a ref but adopt_required false runs no adopt check at all.
  const failPermanentSQL = `
  UPDATE weather_records AS r
     SET status = 'failed', attempts = r.attempts + 1, error = $2,
         processed_at = now(), claimed_at = NULL, adopt_required = false
   WHERE r.id = ANY($1::text[]) AND r.status = 'processing'`

  const requeueInfraSQL = `
  UPDATE weather_records AS r
     SET status = 'pending', error = $2, claimed_at = NULL, adopt_required = false
   WHERE r.id = ANY($1::text[]) AND r.status = 'processing'`

  const markUnknownSQL = `
  UPDATE weather_records AS r
     SET status = 'pending', error = $2, claimed_at = NULL, adopt_required = true
   WHERE r.id = ANY($1::text[]) AND r.status = 'processing'`

  // FailPermanent implements store.RecordStore.
  func (s *RecordStore) FailPermanent(ctx context.Context, ids []string, reason string) error {
  	return s.classifierWrite(ctx, failPermanentSQL, ids, reason)
  }

  // RequeueInfra implements store.RecordStore.
  func (s *RecordStore) RequeueInfra(ctx context.Context, ids []string, reason string) error {
  	return s.classifierWrite(ctx, requeueInfraSQL, ids, reason)
  }

  // MarkUnknown implements store.RecordStore.
  func (s *RecordStore) MarkUnknown(ctx context.Context, ids []string, reason string) error {
  	return s.classifierWrite(ctx, markUnknownSQL, ids, reason)
  }

  // classifierWrite runs one of the three terminal statements.
  //
  // stmt is always one of the three package constants above. It is a parameter
  // of a private method and never derived from input, which is the only shape in
  // which passing SQL as a value is acceptable: there is no code path by which a
  // caller can supply a statement.
  func (s *RecordStore) classifierWrite(ctx context.Context, stmt string, ids []string, reason string) error {
  	if len(ids) == 0 {
  		return nil
  	}
  	if _, err := s.db.Exec(ctx, stmt, ids, reason); err != nil {
  		return classify(err)
  	}
  	return nil
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run 'TestFailPermanent|TestRequeueInfra|TestMarkUnknown|TestClassifierWrites' -v
  ```
  Expected output: `--- PASS` for `TestFailPermanentSpendsTheAttemptBudget`, `TestRequeueInfraKeepsTheBudgetAndNeedsNoAdopt`, `TestMarkUnknownRequiresAnAdoptCheck`, `TestClassifierWritesOnlyTouchProcessingRows`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/records.go internal/store/postgres/records_classify_test.go && git commit -m "postgres: the three classifier writes and their adopt/attempt semantics"
  ```

---

## Task 11: The requeue transitions — ReapExpired and Requeue

**Files:**
- Modify: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/records_requeue_test.go`

**Interfaces:**

Consumes (Task 1): `store.Record`, `store.RequeueFilter`, `store.StatusPending`, `store.StatusProcessing`, `store.StatusFailed`. Consumes (Task 2): `ReapExpired(ctx context.Context, lease time.Duration, limit int) ([]Record, error)`, `Requeue(ctx context.Context, f RequeueFilter) (int64, error)`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `postgres.NewRecordStore`, `recordColumnsAliased`, `classify`. Consumes (Task 8): `collectRecords`, `seedPending`; plus `storeSchema` from `migrations_test.go` and `fullWeatherData` from `records_insert_test.go`.

Produces:
```go
package postgres
func intervalArg(d time.Duration) string
func (s *RecordStore) ReapExpired(ctx context.Context, lease time.Duration, limit int) ([]store.Record, error)
func (s *RecordStore) Requeue(ctx context.Context, f store.RequeueFilter) (int64, error)
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/records_requeue_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  var (
  	_ func(context.Context, time.Duration, int) ([]store.Record, error) = (*postgres.RecordStore)(nil).ReapExpired
  	_ func(context.Context, store.RequeueFilter) (int64, error)         = (*postgres.RecordStore)(nil).Requeue
  )

  // TestReapExpiredReclaimsOnlyStrandedRows uses a BACKDATED claimed_at and no
  // sleeps at all.
  //
  // Never sleep for the lease. The production lease is five minutes, so sleeping
  // is a non-starter, and shortening the lease to something sleepable is the
  // classic timing flake. Writing claimed_at directly is deterministic, instant,
  // and needs no timer.
  func TestReapExpiredReclaimsOnlyStrandedRows(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	priorRef := uuid.Must(uuid.NewV7())
  	if _, err := pool.Exec(ctx, `
  		INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
  		VALUES ('stranded', 1, now(), now(), $1, 'processing', now() - interval '9 minutes', $2)`,
  		fullWeatherData(), priorRef); err != nil {
  		t.Fatalf("seeding the stranded row: %v", err)
  	}
  	if _, err := pool.Exec(ctx, `
  		INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
  		VALUES ('inflight', 2, now(), now(), $1, 'processing', now() - interval '30 seconds', $2)`,
  		fullWeatherData(), uuid.Must(uuid.NewV7())); err != nil {
  		t.Fatalf("seeding the in-flight row: %v", err)
  	}
  	if _, err := pool.Exec(ctx, `
  		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  		VALUES ('fresh', 3, now(), now(), $1)`, fullWeatherData()); err != nil {
  		t.Fatalf("seeding the never-claimed row: %v", err)
  	}

  	reaped, err := rs.ReapExpired(ctx, 5*time.Minute, 100)
  	if err != nil {
  		t.Fatalf("ReapExpired: %v", err)
  	}
  	if len(reaped) != 1 {
  		got := make([]string, 0, len(reaped))
  		for _, r := range reaped {
  			got = append(got, r.ID)
  		}
  		t.Fatalf("reaped %v, want exactly [stranded]", got)
  	}
  	r := reaped[0]
  	if r.ID != "stranded" {
  		t.Fatalf("reaped %q, want stranded", r.ID)
  	}
  	if r.Status != store.StatusPending {
  		t.Errorf("reaped status = %q, want pending", string(r.Status))
  	}
  	if !r.AdoptRequired {
  		t.Error("reaped row must have adopt_required true: it is the only signal that " +
  			"tells a reaped row apart from a never-claimed one, and without it a row " +
  			"whose prior batch did publish is broadcast a second time")
  	}
  	if r.ClaimRef == nil || *r.ClaimRef != priorRef {
  		t.Errorf("reaped claim ref = %v, want the preserved %v", r.ClaimRef, priorRef)
  	}
  	if r.Attempts != 0 {
  		t.Errorf("reaped attempts = %d, want 0: only a permanent error spends the budget", r.Attempts)
  	}
  	if r.ClaimedAt == nil {
  		t.Error("claimed_at must be LEFT SET: it is the ops timeline evidence, and " +
  			"adopt_required is the distinguishing signal, not claimed_at")
  	}

  	// The in-flight and never-claimed rows are untouched.
  	var inflightStatus, freshStatus string
  	var freshAdopt bool
  	if err := pool.QueryRow(ctx,
  		"SELECT status FROM weather_records WHERE id = 'inflight'").Scan(&inflightStatus); err != nil {
  		t.Fatalf("reading inflight: %v", err)
  	}
  	if inflightStatus != string(store.StatusProcessing) {
  		t.Errorf("inflight status = %q, want processing", inflightStatus)
  	}
  	if err := pool.QueryRow(ctx,
  		"SELECT status, adopt_required FROM weather_records WHERE id = 'fresh'").
  		Scan(&freshStatus, &freshAdopt); err != nil {
  		t.Fatalf("reading fresh: %v", err)
  	}
  	if freshStatus != string(store.StatusPending) || freshAdopt {
  		t.Errorf("fresh row = (%q, adopt %v), want (pending, false)", freshStatus, freshAdopt)
  	}
  }

  // TestReapExpiredHonorsItsLimit uses the US spelling of "Honors" because
  // misspell runs with locale US over test files and this repository's
  // ignore-rules do not cover the -s inflection.
  //
  // The fixture seeds all four stale rows in ONE transaction, which is the
  // PRODUCTION shape: claimSQL stamps `claimed_at = now()` on a whole batch inside
  // one statement, so every row of a real stranded batch shares one claimed_at to
  // the microsecond. Five separate Execs — which an earlier draft used — would give
  // five distinct values and quietly test a situation that cannot occur. The
  // count(DISTINCT claimed_at) assertion is what keeps the fixture honest.
  //
  // The assertion is "two successive limited reaps return DISJOINT sets that
  // together cover the batch" rather than "len == 2", because that is the property
  // a caller depends on. Note honestly what it is NOT: like the claim and list
  // ordering tests, it was MEASURED to pass with `, c.id ASC` deleted at this row
  // count. The tiebreaker is still required — `LIMIT` over a non-total order is
  // unspecified, and the failure it prevents (overlapping reaps leaving part of a
  // batch stranded for another whole lease) is silent when it happens.
  func TestReapExpiredHonorsItsLimit(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	tx, err := pool.Begin(ctx)
  	if err != nil {
  		t.Fatalf("Begin: %v", err)
  	}
  	defer func() { _ = tx.Rollback(ctx) }()
  	for i := range 4 {
  		if _, execErr := tx.Exec(ctx, `
  			INSERT INTO weather_records
  			  (id, station_id, timestamp, observation_time, data, status, claimed_at, claim_ref)
  			VALUES ($1, $2, now(), $3, $4, 'processing', now() - interval '9 minutes', $5)`,
  			"s"+string(rune('a'+i)), int64(i), time.Now().Add(time.Duration(i)*time.Minute),
  			fullWeatherData(), uuid.Must(uuid.NewV7())); execErr != nil {
  			t.Fatalf("seeding %d: %v", i, execErr)
  		}
  	}
  	if commitErr := tx.Commit(ctx); commitErr != nil {
  		t.Fatalf("Commit: %v", commitErr)
  	}

  	var distinct int
  	if countErr := pool.QueryRow(ctx,
  		"SELECT count(DISTINCT claimed_at) FROM weather_records").Scan(&distinct); countErr != nil {
  		t.Fatalf("counting distinct claimed_at: %v", countErr)
  	}
  	if distinct != 1 {
  		t.Fatalf("fixture is not the production shape: count(DISTINCT claimed_at) = %d, want 1", distinct)
  	}

  	firstReap, err := rs.ReapExpired(ctx, 5*time.Minute, 2)
  	if err != nil {
  		t.Fatalf("first ReapExpired: %v", err)
  	}
  	if len(firstReap) != 2 {
  		t.Fatalf("first reap returned %d rows with limit 2", len(firstReap))
  	}

  	var stillProcessing int
  	if countErr := pool.QueryRow(ctx,
  		"SELECT count(*) FROM weather_records WHERE status = 'processing'").Scan(&stillProcessing); countErr != nil {
  		t.Fatalf("counting: %v", countErr)
  	}
  	if stillProcessing != 2 {
  		t.Fatalf("processing rows = %d after a limited reap, want 2", stillProcessing)
  	}

  	// Return the first two to processing is NOT what happens next: they are
  	// pending now, so a second reap must pick up the OTHER two and nothing else.
  	secondReap, err := rs.ReapExpired(ctx, 5*time.Minute, 2)
  	if err != nil {
  		t.Fatalf("second ReapExpired: %v", err)
  	}
  	seen := make(map[string]int, 4)
  	for _, r := range firstReap {
  		seen[r.ID]++
  	}
  	for _, r := range secondReap {
  		seen[r.ID]++
  	}
  	for id, n := range seen {
  		if n != 1 {
  			t.Errorf("row %s was reaped %d times across two limited reaps, want 1", id, n)
  		}
  	}
  	if len(seen) != 4 {
  		t.Fatalf("two limited reaps covered %d of 4 rows: %v", len(seen), seen)
  	}
  }

  func TestReapExpiredOnAnIdleQueueReturnsNothing(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	seedPending(t, pool, 3, 1000)

  	reaped, err := rs.ReapExpired(ctx, 5*time.Minute, 100)
  	if err != nil {
  		t.Fatalf("ReapExpired: %v", err)
  	}
  	if len(reaped) != 0 {
  		t.Fatalf("reaped %d rows from a queue with nothing in processing", len(reaped))
  	}
  }

  func TestRequeueDryRunCountsWithoutWriting(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	for i := range 4 {
  		if _, err := pool.Exec(ctx, `
  			INSERT INTO weather_records
  			  (id, station_id, timestamp, observation_time, data, status, attempts,
  			   processed_at, created_at)
  			VALUES ($1, $2, now(), $3, $4, 'failed', 5, now() - interval '2 hours',
  			        now() - interval '2 hours')`,
  			"f"+string(rune('a'+i)), int64(1000+i%2),
  			time.Now().Add(time.Duration(i)*time.Minute), fullWeatherData()); err != nil {
  			t.Fatalf("seeding %d: %v", i, err)
  		}
  	}

  	n, err := rs.Requeue(ctx, store.RequeueFilter{
  		Status: store.StatusFailed, Since: time.Hour, Limit: 100, DryRun: true,
  	})
  	if err != nil {
  		t.Fatalf("Requeue dry run: %v", err)
  	}
  	if n != 4 {
  		t.Fatalf("dry run counted %d, want 4", n)
  	}

  	var stillFailed int
  	if err := pool.QueryRow(ctx,
  		"SELECT count(*) FROM weather_records WHERE status = 'failed'").Scan(&stillFailed); err != nil {
  		t.Fatalf("counting: %v", err)
  	}
  	if stillFailed != 4 {
  		t.Fatalf("a dry run wrote to %d rows", 4-stillFailed)
  	}
  }

  func TestRequeueWritesAndFiltersByStation(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	for i := range 4 {
  		if _, err := pool.Exec(ctx, `
  			INSERT INTO weather_records
  			  (id, station_id, timestamp, observation_time, data, status, attempts,
  			   processed_at, created_at)
  			VALUES ($1, $2, now(), $3, $4, 'failed', 5, now() - interval '2 hours',
  			        now() - interval '2 hours')`,
  			"f"+string(rune('a'+i)), int64(1000+i%2),
  			time.Now().Add(time.Duration(i)*time.Minute), fullWeatherData()); err != nil {
  			t.Fatalf("seeding %d: %v", i, err)
  		}
  	}

  	station := int64(1000)
  	n, err := rs.Requeue(ctx, store.RequeueFilter{
  		Status: store.StatusFailed, Since: time.Hour, StationID: &station, Limit: 100,
  	})
  	if err != nil {
  		t.Fatalf("Requeue: %v", err)
  	}
  	if n != 2 {
  		t.Fatalf("requeued %d rows for station 1000, want 2", n)
  	}

  	rows, err := pool.Query(ctx,
  		"SELECT station_id, status, adopt_required, attempts FROM weather_records ORDER BY id")
  	if err != nil {
  		t.Fatalf("reading rows: %v", err)
  	}
  	defer rows.Close()
  	requeued, untouched := 0, 0
  	for rows.Next() {
  		var stationID int64
  		var status string
  		var adopt bool
  		var attempts int32
  		if err := rows.Scan(&stationID, &status, &adopt, &attempts); err != nil {
  			t.Fatalf("scan: %v", err)
  		}
  		switch stationID {
  		case 1000:
  			if status != string(store.StatusPending) || !adopt {
  				t.Errorf("station 1000 row = (%q, adopt %v), want (pending, true)", status, adopt)
  			}
  			if attempts != 5 {
  				t.Errorf("requeue must not reset attempts, got %d, want 5", attempts)
  			}
  			requeued++
  		default:
  			if status != string(store.StatusFailed) {
  				t.Errorf("station %d row = %q, want an untouched failed", stationID, status)
  			}
  			untouched++
  		}
  	}
  	if err := rows.Err(); err != nil {
  		t.Fatalf("rows: %v", err)
  	}
  	if requeued != 2 || untouched != 2 {
  		t.Fatalf("requeued %d untouched %d, want 2 and 2", requeued, untouched)
  	}
  }

  func TestRequeueRespectsTheSinceWindow(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	if _, err := pool.Exec(ctx, `
  		INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, processed_at, created_at)
  		VALUES ('old', 1, now(), now(), $1, 'failed', now() - interval '2 hours',
  		        now() - interval '2 hours')`, fullWeatherData()); err != nil {
  		t.Fatalf("seeding old: %v", err)
  	}
  	if _, err := pool.Exec(ctx, `
  		INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, processed_at)
  		VALUES ('recent', 2, now(), now(), $1, 'failed', now())`, fullWeatherData()); err != nil {
  		t.Fatalf("seeding recent: %v", err)
  	}

  	n, err := rs.Requeue(ctx, store.RequeueFilter{
  		Status: store.StatusFailed, Since: time.Hour, Limit: 100,
  	})
  	if err != nil {
  		t.Fatalf("Requeue: %v", err)
  	}
  	if n != 1 {
  		t.Fatalf("requeued %d rows, want 1 (only the row older than the window)", n)
  	}
  	var recentStatus string
  	if err := pool.QueryRow(ctx,
  		"SELECT status FROM weather_records WHERE id = 'recent'").Scan(&recentStatus); err != nil {
  		t.Fatalf("reading recent: %v", err)
  	}
  	if recentStatus != string(store.StatusFailed) {
  		t.Fatalf("recent row status = %q, want an untouched failed", recentStatus)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `(*postgres.RecordStore)(nil).ReapExpired undefined` and `(*postgres.RecordStore)(nil).Requeue undefined`.

- [ ] **Write the implementation.** Append exactly this to `internal/store/postgres/records.go`, and add `"strconv"` to its import block:
  ```go
  // reapExpiredSQL reclaims rows stranded in processing past the lease.
  //
  // SKIP LOCKED here too, so a reap tick never blocks behind a live claim or a
  // Complete. attempts is NOT touched: only a permanent error spends the budget.
  // claim_ref is PRESERVED and claimed_at is deliberately LEFT SET — claimed_at
  // is the ops timeline evidence, and adopt_required is the distinguishing
  // signal:
  //
  //	pending,   adopt_required=true,  claim_ref NOT NULL -> REAPED, adopt-check first
  //	pending,   adopt_required=false, claim_ref IS NULL  -> never claimed, publish fresh
  //	processing, claimed_at within lease                 -> untouched
  //
  // Both the reaper and the error classifier converge on this same transition, so
  // neither is dead code: the classifier writes it when it knows the outcome is
  // ambiguous (resolved in seconds), and the reaper is the backstop for the
  // process dying before that write landed (resolved after the lease).
  //
  // The ck_records_processing_leased CHECK is what makes the
  // `claimed_at < now() - lease` predicate TOTAL, so no defensive
  // `OR claimed_at IS NULL` is needed.
  //
  // `, c.id ASC` for the same reason claimSQL carries it, and the reasoning is
  // NOT weaker here: claimSQL sets `claimed_at = now()` for every row it touches
  // in one statement, and now() is transaction_timestamp(), so an entire claimed
  // batch shares one claimed_at to the microsecond. `ORDER BY c.claimed_at ASC`
  // alone is therefore a non-total order over exactly the rows the reaper looks
  // at, and `LIMIT` over a non-total order selects an unspecified subset — so two
  // successive limited reaps could return overlapping sets and leave part of the
  // batch stranded for another lease.
  const reapExpiredSQL = `
  UPDATE weather_records AS r
     SET status = 'pending', adopt_required = true
   WHERE r.id IN (
           SELECT c.id
             FROM weather_records AS c
            WHERE c.status = 'processing'
              AND c.claimed_at < now() - $1::interval
            ORDER BY c.claimed_at ASC, c.id ASC
            LIMIT $2
              FOR UPDATE SKIP LOCKED
         )
  RETURNING ` + recordColumnsAliased

  // requeueCountSQL is the DryRun half of Requeue: the identical predicate with
  // no write, so a dry run can never disagree with the real thing about which
  // rows it would touch.
  const requeueCountSQL = `
  SELECT count(*) FROM (
    SELECT c.id
      FROM weather_records AS c
     WHERE c.status = $1
       AND c.created_at < now() - $2::interval
       AND ($3::bigint IS NULL OR c.station_id = $3)
     ORDER BY c.created_at ASC, c.id ASC
     LIMIT $4
  ) AS t`

  // requeueSQL is the operator-driven bulk requeue.
  //
  // adopt_required is set for the same reason the reaper sets it: a requeued row
  // may already have been published by a prior batch, and preserving claim_ref
  // without the flag would run no adopt check at all. attempts is NOT reset —
  // an operator requeue is not an amnesty on the budget.
  const requeueSQL = `
  UPDATE weather_records AS r
     SET status = 'pending', adopt_required = true, error = 'requeued by operator'
   WHERE r.id IN (
           SELECT c.id
             FROM weather_records AS c
            WHERE c.status = $1
              AND c.created_at < now() - $2::interval
              AND ($3::bigint IS NULL OR c.station_id = $3)
            ORDER BY c.created_at ASC, c.id ASC
            LIMIT $4
              FOR UPDATE SKIP LOCKED
         )`

  // intervalArg renders d for a $n::interval bind parameter.
  //
  // This builds a VALUE, not SQL text: the statements above are constants and d
  // travels as a parameter through the extended protocol. Microseconds is the
  // finest unit a Postgres interval carries, so no precision is lost.
  func intervalArg(d time.Duration) string {
  	return strconv.FormatInt(d.Microseconds(), 10) + " microseconds"
  }

  // ReapExpired implements store.RecordStore.
  func (s *RecordStore) ReapExpired(ctx context.Context, lease time.Duration, limit int) ([]store.Record, error) {
  	rows, err := s.db.Query(ctx, reapExpiredSQL, intervalArg(lease), limit)
  	if err != nil {
  		return nil, classify(err)
  	}
  	return collectRecords(rows)
  }

  // Requeue implements store.RecordStore.
  func (s *RecordStore) Requeue(ctx context.Context, f store.RequeueFilter) (int64, error) {
  	if f.DryRun {
  		var n int64
  		err := s.db.QueryRow(ctx, requeueCountSQL,
  			string(f.Status), intervalArg(f.Since), f.StationID, f.Limit).Scan(&n)
  		if err != nil {
  			return 0, classify(err)
  		}
  		return n, nil
  	}
  	ct, err := s.db.Exec(ctx, requeueSQL,
  		string(f.Status), intervalArg(f.Since), f.StationID, f.Limit)
  	if err != nil {
  		return 0, classify(err)
  	}
  	return ct.RowsAffected(), nil
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run 'TestReap|TestRequeue' -v
  ```
  Expected output: `--- PASS` for `TestReapExpiredReclaimsOnlyStrandedRows`, `TestReapExpiredHonorsItsLimit`, `TestReapExpiredOnAnIdleQueueReturnsNothing`, `TestRequeueDryRunCountsWithoutWriting`, `TestRequeueWritesAndFiltersByStation`, `TestRequeueRespectsTheSinceWindow`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/records.go internal/store/postgres/records_requeue_test.go && git commit -m "postgres: lease reaper and operator requeue, both setting adopt_required"
  ```

---

## Task 12: List, Get and TxIDExists — the total order and the 404 seam

**Files:**
- Modify: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/records_read_test.go`

**Interfaces:**

Consumes (Task 1): `store.Record`, `store.ListFilter`, `store.Status`, `store.ErrNotFound`. Consumes (Task 2): `List(ctx context.Context, f ListFilter) ([]Record, int64, error)`, `Get(ctx context.Context, id string) (Record, error)`, `TxIDExists(ctx context.Context, txID string) (bool, error)`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `postgres.NewRecordStore`, `recordColumns`, `classify`. Consumes (Task 8): `collectRecords`, `seedPending`; plus `storeSchema` from `migrations_test.go` and `fullWeatherData` from `records_insert_test.go`.

Produces:
```go
package postgres
func (s *RecordStore) List(ctx context.Context, f store.ListFilter) ([]store.Record, int64, error)
func (s *RecordStore) Get(ctx context.Context, id string) (store.Record, error)
func (s *RecordStore) TxIDExists(ctx context.Context, txID string) (bool, error)
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/records_read_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
  )

  var (
  	_ func(context.Context, store.ListFilter) ([]store.Record, int64, error) = (*postgres.RecordStore)(nil).List
  	_ func(context.Context, string) (store.Record, error)                    = (*postgres.RecordStore)(nil).Get
  	_ func(context.Context, string) (bool, error)                            = (*postgres.RecordStore)(nil).TxIDExists
  )

  func listIDs(recs []store.Record) []string {
  	out := make([]string, 0, len(recs))
  	for _, r := range recs {
  		out = append(out, r.ID)
  	}
  	return out
  }

  // TestListIsATotalOrderAcrossATiedBatch asserts that paging through a whole
  // tied batch in fours reconstructs the entire order with no repeats, no gaps and
  // a stable total. It also asserts its own fixture is the production shape
  // (count(DISTINCT created_at) = 1), which is the part that matters most: a
  // fixture with distinct created_at values would make every ordering claim in
  // this file weaker without anybody noticing.
  //
  // WHAT IT DOES NOT DO, and an earlier draft of this comment claimed otherwise by
  // calling it "the regression gate on the id tiebreaker": it does not
  // discriminate against removing `, id DESC`. MEASURED on postgres:17-alpine —
  // with the tiebreaker deleted, and again with ix_records_list removed from the
  // migration so the planner must sort, this test still PASSED three times over.
  //
  // The tiebreaker stays anyway, and the reason is not this test. created_at
  // defaults to now(), which is transaction_timestamp(), so every row one poll
  // writes shares a single created_at to the microsecond (measured: 19 rows in one
  // transaction gave count(DISTINCT created_at) = 1). `ORDER BY created_at DESC`
  // alone is therefore a NON-TOTAL order over an entire batch, and LIMIT/OFFSET
  // over a non-total order is UNSPECIFIED — at production shape two legitimate
  // plans for the identical untied query returned 19 of 20 DIFFERENT rows at the
  // same offset. "It passed at twenty rows on an empty table" is not a guarantee.
  func TestListIsATotalOrderAcrossATiedBatch(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seeded := seedPending(t, pool, 20, 1000)
  	rs := postgres.NewRecordStore(pool)

  	var distinct int
  	if err := pool.QueryRow(ctx,
  		"SELECT count(DISTINCT created_at) FROM weather_records").Scan(&distinct); err != nil {
  		t.Fatalf("counting distinct created_at: %v", err)
  	}
  	if distinct != 1 {
  		t.Fatalf("fixture is not the production shape: count(DISTINCT created_at) = %d, want 1", distinct)
  	}

  	// Newest first means reverse-uuidv7 order, because uuidv7 sorts in
  	// generation order.
  	want := make([]string, 0, len(seeded))
  	for i := len(seeded) - 1; i >= 0; i-- {
  		want = append(want, seeded[i])
  	}

  	// Paging through in fours must reconstruct the whole order exactly, with no
  	// repeats and no gaps.
  	got := make([]string, 0, len(seeded))
  	for offset := 0; offset < len(seeded); offset += 4 {
  		page, total, err := rs.List(ctx, store.ListFilter{Limit: 4, Offset: offset})
  		if err != nil {
  			t.Fatalf("List offset %d: %v", offset, err)
  		}
  		if total != int64(len(seeded)) {
  			t.Fatalf("total at offset %d = %d, want %d", offset, total, len(seeded))
  		}
  		got = append(got, listIDs(page)...)
  	}
  	if len(got) != len(want) {
  		t.Fatalf("paged %d ids, want %d", len(got), len(want))
  	}
  	for i := range want {
  		if got[i] != want[i] {
  			t.Fatalf("position %d = %s, want %s\n got %v\nwant %v", i, got[i], want[i], got, want)
  		}
  	}
  }

  func TestListFiltersByStationAndStatus(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	seedPending(t, pool, 3, 1000)
  	seedPending(t, pool, 2, 2000)

  	claimed, err := rs.ClaimPending(ctx, 2, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	if len(claimed) != 2 {
  		t.Fatalf("claimed %d, want 2", len(claimed))
  	}

  	// No filters at all.
  	all, total, err := rs.List(ctx, store.ListFilter{Limit: 100})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 5 || len(all) != 5 {
  		t.Fatalf("unfiltered = %d rows / total %d, want 5 / 5", len(all), total)
  	}

  	// Station only.
  	station := int64(1000)
  	byStation, total, err := rs.List(ctx, store.ListFilter{StationID: &station, Limit: 100})
  	if err != nil {
  		t.Fatalf("List by station: %v", err)
  	}
  	if total != 3 || len(byStation) != 3 {
  		t.Fatalf("station 1000 = %d rows / total %d, want 3 / 3", len(byStation), total)
  	}
  	for _, r := range byStation {
  		if r.StationID != 1000 {
  			t.Fatalf("station filter leaked station %d", r.StationID)
  		}
  	}

  	// Status only.
  	processing := store.StatusProcessing
  	byStatus, total, err := rs.List(ctx, store.ListFilter{Status: &processing, Limit: 100})
  	if err != nil {
  		t.Fatalf("List by status: %v", err)
  	}
  	if total != 2 || len(byStatus) != 2 {
  		t.Fatalf("processing = %d rows / total %d, want 2 / 2", len(byStatus), total)
  	}

  	// Both.
  	pending := store.StatusPending
  	both, total, err := rs.List(ctx, store.ListFilter{StationID: &station, Status: &pending, Limit: 100})
  	if err != nil {
  		t.Fatalf("List by both: %v", err)
  	}
  	if total != int64(len(both)) {
  		t.Fatalf("total %d disagrees with page length %d", total, len(both))
  	}
  	for _, r := range both {
  		if r.StationID != 1000 || r.Status != store.StatusPending {
  			t.Fatalf("combined filter leaked (%d, %q)", r.StationID, string(r.Status))
  		}
  	}

  	// A status that matches nothing.
  	failed := store.StatusFailed
  	none, total, err := rs.List(ctx, store.ListFilter{Status: &failed, Limit: 100})
  	if err != nil {
  		t.Fatalf("List by failed: %v", err)
  	}
  	if total != 0 || len(none) != 0 {
  		t.Fatalf("failed = %d rows / total %d, want 0 / 0", len(none), total)
  	}
  }

  func TestListOnAnEmptyTableAndPastTheEnd(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	// total 0 is what the API turns into totalPages 0. If totalPages were 1
  	// instead, the frontend's Next button would never disable.
  	recs, total, err := rs.List(ctx, store.ListFilter{Limit: 20})
  	if err != nil {
  		t.Fatalf("List on an empty table: %v", err)
  	}
  	if total != 0 || len(recs) != 0 {
  		t.Fatalf("empty table = %d rows / total %d, want 0 / 0", len(recs), total)
  	}

  	seedPending(t, pool, 3, 1000)
  	recs, total, err = rs.List(ctx, store.ListFilter{Limit: 20, Offset: 100})
  	if err != nil {
  		t.Fatalf("List past the end: %v", err)
  	}
  	if total != 3 {
  		t.Fatalf("total past the end = %d, want 3", total)
  	}
  	if len(recs) != 0 {
  		t.Fatalf("page past the end has %d rows, want 0", len(recs))
  	}
  }

  func TestGetRoundTripsEverySchemaField(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	want := fullWeatherData()
  	inserted, err := rs.Insert(ctx, store.NewRecord{
  		ID: "the-one", StationID: 4242, Timestamp: obs, ObservationTime: obs, Data: want,
  	})
  	if err != nil || !inserted {
  		t.Fatalf("Insert = (%v, %v)", inserted, err)
  	}

  	got, err := rs.Get(ctx, "the-one")
  	if err != nil {
  		t.Fatalf("Get: %v", err)
  	}
  	if got.ID != "the-one" {
  		t.Errorf("ID = %q", got.ID)
  	}
  	if got.StationID != 4242 {
  		t.Errorf("StationID = %d, want 4242", got.StationID)
  	}
  	if !got.Timestamp.Equal(obs) {
  		t.Errorf("Timestamp = %v, want %v", got.Timestamp, obs)
  	}
  	if !got.ObservationTime.Equal(obs) {
  		t.Errorf("ObservationTime = %v, want %v", got.ObservationTime, obs)
  	}
  	if got.Data != want {
  		t.Errorf("Data round trip changed:\n got %+v\nwant %+v", got.Data, want)
  	}
  	if got.Status != store.StatusPending {
  		t.Errorf("Status = %q, want pending", string(got.Status))
  	}
  	if got.Attempts != 0 {
  		t.Errorf("Attempts = %d, want 0", got.Attempts)
  	}
  	if got.AdoptRequired {
  		t.Error("AdoptRequired = true on a fresh row")
  	}
  	if got.CreatedAt.IsZero() {
  		t.Error("CreatedAt is zero")
  	}
  	// Every nullable column must come back as a genuine nil, not a zero value:
  	// a zero time.Time marshals to "0001-01-01T00:00:00Z", which the frontend
  	// renders as 01/01/0001 where it intends an em dash.
  	if got.ClaimRef != nil || got.ClaimedAt != nil || got.TxID != nil ||
  		got.OutputIndex != nil || got.BlockHeight != nil || got.ChainStatus != nil ||
  		got.MinedAt != nil || got.Error != nil || got.ProcessedAt != nil {
  		t.Errorf("a fresh row has a non-nil nullable field: %+v", got)
  	}
  	if weather.DataFieldsPerRecord != 33 {
  		t.Fatalf("DataFieldsPerRecord = %d; this test's fixture assumes 33", weather.DataFieldsPerRecord)
  	}
  }

  func TestGetUnknownIDIsErrNotFoundAndNeverAnError500(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	// id is `text`, so a malformed id is a MISS rather than a cast failure, which
  	// makes the TypeScript's CastError-into-500 wart structurally impossible and
  	// makes 400-vs-404 purely a handler choice.
  	//
  	// "The database cannot raise" would be too strong, and an earlier draft of
  	// this comment said it: a NUL byte is NOT a bindable text value, and binding
  	// one fails with SQLSTATE 22021 before the predicate is ever evaluated
  	// (measured). Get therefore screens the id with store.ValidText first, so the
  	// property the API layer relies on — user input on this path can never
  	// produce a 500 — is delivered by code rather than by assumption. The NUL
  	// case is in the table below for exactly that reason.
  	for _, id := range []string{
  		"", "not-a-uuid", "0", "../../etc/passwd", "'; DROP TABLE weather_records; --",
  		"00000000-0000-0000-0000-000000000000", "\x00", "nul\x00byte",
  	} {
  		_, err := rs.Get(ctx, id)
  		if !errors.Is(err, store.ErrNotFound) {
  			t.Errorf("Get(%q) error = %v, want store.ErrNotFound", id, err)
  		}
  	}
  }

  func TestTxIDExistsIsTheProofGate(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, is_active) VALUES (1000, true)"); err != nil {
  		t.Fatalf("seeding station: %v", err)
  	}
  	seedPending(t, pool, 1, 1000)
  	claimed, err := rs.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	const txid = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
  	if _, completeErr := rs.Complete(ctx, txid,
  		[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
  		t.Fatalf("Complete: %v", completeErr)
  	}

  	ok, err := rs.TxIDExists(ctx, txid)
  	if err != nil || !ok {
  		t.Fatalf("TxIDExists(known) = (%v, %v), want (true, nil)", ok, err)
  	}

  	// This is the anti-amplification control in front of the BEEF proof
  	// endpoint: an attacker iterating 64-hex values must be answered from the
  	// database, with no outbound call at all.
  	for _, unknown := range []string{
  		"0000000000000000000000000000000000000000000000000000000000000000",
  		"", "not-hex", txid + "extra", "\x00", txid[:63] + "\x00",
  	} {
  		ok, existsErr := rs.TxIDExists(ctx, unknown)
  		if existsErr != nil {
  			t.Errorf("TxIDExists(%q) error = %v, want nil", unknown, existsErr)
  		}
  		if ok {
  			t.Errorf("TxIDExists(%q) = true, want false", unknown)
  		}
  	}
  }

  func TestListAndGetSurviveAConcurrentWriter(t *testing.T) {
  	// Read paths must not deadlock or error while the claim is running, which is
  	// the shape production actually has: the API serves reads on the same pool
  	// the processor claims on.
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	seedPending(t, pool, 30, 1000)

  	done := make(chan error, 1)
  	go func() {
  		_, err := rs.ClaimPending(ctx, 10, uuid.Must(uuid.NewV7()))
  		done <- err
  	}()
  	for range 20 {
  		if _, _, err := rs.List(ctx, store.ListFilter{Limit: 20}); err != nil {
  			t.Errorf("List during a claim: %v", err)
  		}
  	}
  	if err := <-done; err != nil {
  		t.Fatalf("concurrent ClaimPending: %v", err)
  	}
  	assertPoolIsHealthy(t, pool)
  }

  func assertPoolIsHealthy(t testing.TB, pool *pgxpool.Pool) {
  	t.Helper()
  	if err := pool.Ping(context.Background()); err != nil {
  		t.Fatalf("pool is unhealthy after the test: %v", err)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `(*postgres.RecordStore)(nil).List undefined`, `(*postgres.RecordStore)(nil).Get undefined` and `(*postgres.RecordStore)(nil).TxIDExists undefined`.

- [ ] **Write the implementation.** Append exactly this to `internal/store/postgres/records.go`:
  ```go
  // listRecordsSQL is the record list.
  //
  // The optional filters are NULL-able bind parameters in ONE static statement
  // rather than a conditionally assembled WHERE clause. That is the rule for
  // every optional filter in this package: if a clause is ever assembled at all,
  // only fixed literal fragments may be appended and every value goes in the
  // args slice.
  //
  // ORDER BY created_at DESC, id DESC — the tiebreaker is mandatory, not
  // decoration. See claimSQL's comment and TestListIsATotalOrderAcrossATiedBatch.
  //
  // ACCEPTED RESIDUAL, stated rather than hidden: the tiebreaker makes the order
  // TOTAL, which is necessary but not sufficient for stable pagination. Page
  // boundaries still shift when a new poll lands between the page-1 and page-2
  // requests, because OFFSET counts from a moving head. Keyset pagination
  // (WHERE (created_at, id) < ($1, $2)) would fix both, but the frontend sends
  // page=, so OFFSET is required.
  const listRecordsSQL = `
  SELECT ` + recordColumns + `
    FROM weather_records
   WHERE ($1::bigint IS NULL OR station_id = $1)
     AND ($2::text   IS NULL OR status = $2)
   ORDER BY created_at DESC, id DESC
   LIMIT $3 OFFSET $4`

  // countRecordsSQL is the unpaged total for pagination. Its predicate is
  // character-for-character the same as listRecordsSQL's, so the two can never
  // disagree about what is in scope.
  const countRecordsSQL = `
  SELECT count(*) FROM weather_records
   WHERE ($1::bigint IS NULL OR station_id = $1)
     AND ($2::text   IS NULL OR status = $2)`

  // getRecordSQL is the detail read. The id column is text, so a malformed id is
  // a miss rather than a database error — which, together with the store.ValidText
  // screen in Get, is why this path can never produce a 500 for bad input.
  const getRecordSQL = `
  SELECT ` + recordColumns + `
    FROM weather_records WHERE id = $1`

  // txIDExistsSQL is the anti-amplification gate in front of the BEEF proof
  // endpoint. The partial index ix_records_txid ... WHERE txid IS NOT NULL serves
  // it.
  const txIDExistsSQL = `SELECT 1 FROM weather_records WHERE txid = $1 LIMIT 1`

  // List implements store.RecordStore.
  func (s *RecordStore) List(ctx context.Context, f store.ListFilter) ([]store.Record, int64, error) {
  	// A *store.Status encodes as text or NULL; passing the pointer straight
  	// through is what makes the NULL-able predicate work.
  	var statusArg *string
  	if f.Status != nil {
  		value := string(*f.Status)
  		statusArg = &value
  	}

  	rows, err := s.db.Query(ctx, listRecordsSQL, f.StationID, statusArg, f.Limit, f.Offset)
  	if err != nil {
  		return nil, 0, classify(err)
  	}
  	recs, err := collectRecords(rows)
  	if err != nil {
  		return nil, 0, err
  	}

  	var total int64
  	if err := s.db.QueryRow(ctx, countRecordsSQL, f.StationID, statusArg).Scan(&total); err != nil {
  		return nil, 0, classify(err)
  	}
  	return recs, total, nil
  }

  // Get implements store.RecordStore.
  //
  // The store.ValidText screen is not defensive noise. A NUL byte cannot be bound
  // to a text parameter at all — measured, SQLSTATE 22021 invalid byte sequence,
  // raised before the predicate is evaluated — and `GET /api/weather/%00`
  // delivers one straight from Go's path decoding. No record id can contain a NUL
  // (they are uuidv7 strings), so such an id is a MISS by definition, and
  // answering ErrNotFound is both true and the only answer that keeps this path
  // unable to 500. B2 additionally rejects the input with a 400 at its parse
  // layer; this is the belt to that braces.
  func (s *RecordStore) Get(ctx context.Context, id string) (store.Record, error) {
  	if store.ValidText(id) != nil {
  		return store.Record{}, store.ErrNotFound
  	}
  	rows, err := s.db.Query(ctx, getRecordSQL, id)
  	if err != nil {
  		return store.Record{}, classify(err)
  	}
  	rec, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[store.Record])
  	if err != nil {
  		return store.Record{}, classify(err)
  	}
  	return rec, nil
  }

  // TxIDExists implements store.RecordStore.
  //
  // Same screen as Get, and it matters more here: this is the anti-amplification
  // gate in front of the proof endpoint, so it is reachable unauthenticated with
  // an arbitrary path segment. A txid containing a NUL matches no row, so
  // (false, nil) is the truthful answer as well as the un-500-able one.
  func (s *RecordStore) TxIDExists(ctx context.Context, txID string) (bool, error) {
  	if store.ValidText(txID) != nil {
  		return false, nil
  	}
  	var one int
  	err := s.db.QueryRow(ctx, txIDExistsSQL, txID).Scan(&one)
  	if err != nil {
  		if errors.Is(err, pgx.ErrNoRows) {
  			return false, nil
  		}
  		return false, classify(err)
  	}
  	return true, nil
  }
  ```
  and add `"errors"` to the file's import block.

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run 'TestList|TestGet|TestTxIDExists' -v
  ```
  Expected output: `--- PASS` for `TestListIsATotalOrderAcrossATiedBatch`, `TestListFiltersByStationAndStatus`, `TestListOnAnEmptyTableAndPastTheEnd`, `TestGetRoundTripsEverySchemaField`, `TestGetUnknownIDIsErrNotFoundAndNeverAnError500`, `TestTxIDExistsIsTheProofGate`, `TestListAndGetSurviveAConcurrentWriter`, then `ok`.

- [ ] **Reproduce the measured limit of the total-order test, exactly as Task 8 does for the claim.** Delete `, id DESC` from `listRecordsSQL`'s `ORDER BY` and comment out the `CREATE INDEX … ix_records_list` statement in `migrations.sql` (a `psql` DROP INDEX is useless here: `storetest.Fresh` re-applies the migration before every test). Then run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=3 -run TestListIsATotalOrderAcrossATiedBatch -v
  ```
  **Expected output: PASS, three times** — the measured result, recorded here so nobody has to guess. Twenty rows on an empty table are returned in insertion order whether the query says so or not, index or no index, so this test pins page continuity and the fixture's shape but does NOT gate the tiebreaker. Its doc comment says exactly that; confirm it still does. A run that FAILS (`position 0 = …, want …`) is a better outcome and should be written into the comment instead.

  **Restore `, id DESC` and `migrations.sql`** and re-run the whole `TestList` set. Keeping the tiebreaker is not conditional on this test failing: `LIMIT`/`OFFSET` over a non-total order is unspecified, and at production shape two legitimate plans for the untied query returned 19 of 20 different rows at the same offset.

- [ ] **Confirm the list uses its index.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && docker compose exec -T postgres psql -U postgres -d weatherproof_test -c "SET search_path TO wp_test_store; EXPLAIN SELECT id FROM weather_records WHERE (NULL::bigint IS NULL OR station_id = NULL) AND (NULL::text IS NULL OR status = NULL) ORDER BY created_at DESC, id DESC LIMIT 20 OFFSET 0;"
  ```
  Expected output: a plan naming `ix_records_list`. At a tiny row count Postgres may legitimately choose a sequential scan and sort instead — that is fine and not a failure; the index earns its keep at production volume. What must NOT appear is any error, which would mean the statement text is wrong.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/records.go internal/store/postgres/records_read_test.go && git commit -m "postgres: List with a total order, Get with ErrNotFound, and the txid proof gate"
  ```

---

## Task 13: Stations — Upsert, Get, and the one-parsed-value search split

**Files:**
- Create: `internal/store/postgres/stations.go`
- Create: `internal/store/postgres/stations_test.go`

**Interfaces:**

Consumes (Task 1): `store.Station`, `store.StationFilter`, `store.ErrNotFound`. Consumes (Task 2): `Upsert(ctx context.Context, s Station) error`, `List(ctx context.Context, f StationFilter) ([]Station, int64, error)`, `Get(ctx context.Context, stationID int64) (Station, error)`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `classify`; plus `storeSchema` from `migrations_test.go`.

Produces:
```go
package postgres
const stationColumns string
type StationStore struct { /* unexported */ }
func NewStationStore(db *pgxpool.Pool) *StationStore
func (s *StationStore) Upsert(ctx context.Context, in store.Station) error
func (s *StationStore) Get(ctx context.Context, stationID int64) (store.Station, error)
func (s *StationStore) List(ctx context.Context, f store.StationFilter) ([]store.Station, int64, error)
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/stations_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"strings"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  var (
  	_ func(context.Context, store.Station) error                                 = (*postgres.StationStore)(nil).Upsert
  	_ func(context.Context, int64) (store.Station, error)                        = (*postgres.StationStore)(nil).Get
  	_ func(context.Context, store.StationFilter) ([]store.Station, int64, error) = (*postgres.StationStore)(nil).List
  )

  func seedStations(t testing.TB, pool *pgxpool.Pool) {
  	t.Helper()
  	ctx := context.Background()
  	rows := []struct {
  		id       int64
  		name     string
  		location string
  		active   bool
  	}{
  		{1000, "Harbor Mast", "Bristol Docks", true},
  		{1001, "Clifton Ridge", "Bristol Downs", true},
  		{1002, "Severn Beach", "Gloucestershire", false},
  		{2000, "Kelvin Yard", "Glasgow", true},
  	}
  	for _, r := range rows {
  		if _, err := pool.Exec(ctx,
  			"INSERT INTO stations (station_id, name, location, is_active) VALUES ($1, $2, $3, $4)",
  			r.id, r.name, r.location, r.active); err != nil {
  			t.Fatalf("seeding station %d: %v", r.id, err)
  		}
  	}
  }

  func stationIDs(sts []store.Station) []int64 {
  	out := make([]int64, 0, len(sts))
  	for _, s := range sts {
  		out = append(out, s.StationID)
  	}
  	return out
  }

  func TestStationUpsertInsertsThenUpdates(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ss := postgres.NewStationStore(pool)

  	lat, lon := 51.4545, -2.5879
  	if err := ss.Upsert(ctx, store.Station{
  		StationID: 1000, Name: "Harbor Mast", Location: "Bristol Docks",
  		Latitude: &lat, Longitude: &lon, IsActive: true,
  	}); err != nil {
  		t.Fatalf("first Upsert: %v", err)
  	}

  	got, err := ss.Get(ctx, 1000)
  	if err != nil {
  		t.Fatalf("Get: %v", err)
  	}
  	if got.Name != "Harbor Mast" || got.Location != "Bristol Docks" || !got.IsActive {
  		t.Fatalf("after insert: %+v", got)
  	}
  	if got.Latitude == nil || *got.Latitude != lat {
  		t.Fatalf("latitude = %v, want %v", got.Latitude, lat)
  	}
  	if got.TxRecords != 0 {
  		t.Fatalf("TxRecords = %d, want 0", got.TxRecords)
  	}
  	if got.LastReading != nil || got.LastTemp != nil || got.LastBlockHeight != nil {
  		t.Fatalf("a fresh station must have nil nullable fields: %+v", got)
  	}
  	if got.LastConditions != "" {
  		t.Fatalf("LastConditions = %q, want the empty string (NOT null: the frontend types it as a plain string)", got.LastConditions)
  	}

  	// Simulate the counters having moved, then upsert again: the identity
  	// columns refresh and the COUNTERS MUST SURVIVE. An upsert that reset
  	// tx_records would silently zero the whole dashboard on every poll.
  	if _, execErr := pool.Exec(ctx, `
  		UPDATE stations
  		   SET tx_records = 17, last_temp = 18, last_conditions = 'Clear', last_reading = now()
  		 WHERE station_id = 1000`); execErr != nil {
  		t.Fatalf("simulating counters: %v", execErr)
  	}

  	if upsertErr := ss.Upsert(ctx, store.Station{
  		StationID: 1000, Name: "Harbor Mast II", Location: "Bristol Harborside", IsActive: false,
  	}); upsertErr != nil {
  		t.Fatalf("second Upsert: %v", upsertErr)
  	}
  	got, err = ss.Get(ctx, 1000)
  	if err != nil {
  		t.Fatalf("Get after update: %v", err)
  	}
  	if got.Name != "Harbor Mast II" || got.Location != "Bristol Harborside" || got.IsActive {
  		t.Fatalf("after update: %+v", got)
  	}
  	if got.TxRecords != 17 {
  		t.Fatalf("TxRecords = %d after upsert, want the preserved 17", got.TxRecords)
  	}
  	if got.LastTemp == nil || *got.LastTemp != 18 {
  		t.Fatalf("LastTemp = %v after upsert, want the preserved 18", got.LastTemp)
  	}
  	if got.LastConditions != "Clear" {
  		t.Fatalf("LastConditions = %q after upsert, want the preserved Clear", got.LastConditions)
  	}
  	if got.Latitude != nil {
  		t.Fatalf("Latitude = %v; the second upsert supplied nil and identity columns DO refresh", got.Latitude)
  	}
  }

  func TestStationGetUnknownIsErrNotFound(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ss := postgres.NewStationStore(pool)

  	for _, id := range []int64{0, -1, 999999, 9223372036854775807} {
  		if _, err := ss.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
  			t.Errorf("Get(%d) error = %v, want store.ErrNotFound", id, err)
  		}
  	}
  }

  func TestStationListNoSearchIsAscendingByID(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	ss := postgres.NewStationStore(pool)

  	sts, total, err := ss.List(ctx, store.StationFilter{Limit: 50})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 4 {
  		t.Fatalf("total = %d, want 4", total)
  	}
  	want := []int64{1000, 1001, 1002, 2000}
  	got := stationIDs(sts)
  	if len(got) != len(want) {
  		t.Fatalf("got %v, want %v", got, want)
  	}
  	for i := range want {
  		if got[i] != want[i] {
  			t.Fatalf("got %v, want %v", got, want)
  		}
  	}

  	// Paging.
  	page2, total, err := ss.List(ctx, store.StationFilter{Limit: 2, Offset: 2})
  	if err != nil {
  		t.Fatalf("List page 2: %v", err)
  	}
  	if total != 4 {
  		t.Fatalf("total on page 2 = %d, want 4", total)
  	}
  	got = stationIDs(page2)
  	if len(got) != 2 || got[0] != 1002 || got[1] != 2000 {
  		t.Fatalf("page 2 = %v, want [1002 2000]", got)
  	}
  }

  // TestStationListNumericSearchIsAnExactLookup pins the rule that ONE parsed
  // value drives BOTH the filter and the sort.
  //
  // A deliberate behavior change from the TypeScript: JS parseInt("12345abc") is
  // 12345, so the TypeScript treats that as an exact station-id lookup. Go's
  // strconv.Atoi errors on it, so it routes to full-text search instead. Go's
  // behavior is the better one and matches the spec's wording, but it IS a
  // visible change on a free-text search box and is asserted here so it is
  // stated rather than discovered.
  func TestStationListNumericSearchIsAnExactLookup(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	ss := postgres.NewStationStore(pool)

  	sts, total, err := ss.List(ctx, store.StationFilter{Search: "1001", Limit: 50})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 1 || len(sts) != 1 || sts[0].StationID != 1001 {
  		t.Fatalf("search 1001 = %v / total %d, want [1001] / 1", stationIDs(sts), total)
  	}

  	// A numeric search matching nothing is 200 with no rows, never an error.
  	sts, total, err = ss.List(ctx, store.StationFilter{Search: "424242", Limit: 50})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 0 || len(sts) != 0 {
  		t.Fatalf("search 424242 = %v / total %d, want none", stationIDs(sts), total)
  	}

  	// "0" parses, so it is an exact lookup for station 0 and finds nothing.
  	// In the TypeScript this exact input is a 500: the filter becomes
  	// stationId: 0 while the sort expression `search && !parseInt(search,10)` is
  	// truthy for "0", so Mongo is asked to sort by text score with no $text in
  	// the filter and errors.
  	sts, total, err = ss.List(ctx, store.StationFilter{Search: "0", Limit: 50})
  	if err != nil {
  		t.Fatalf("List with search=0: %v", err)
  	}
  	if total != 0 || len(sts) != 0 {
  		t.Fatalf("search 0 = %v / total %d, want none", stationIDs(sts), total)
  	}

  	// "12345abc" does NOT parse in Go, so it becomes a text search.
  	if _, _, err := ss.List(ctx, store.StationFilter{Search: "12345abc", Limit: 50}); err != nil {
  		t.Fatalf("List with search=12345abc: %v", err)
  	}
  }

  func TestStationListTextSearchRanksAndFallsBackToID(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	ss := postgres.NewStationStore(pool)

  	sts, total, err := ss.List(ctx, store.StationFilter{Search: "bristol", Limit: 50})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 2 {
  		t.Fatalf("search bristol total = %d, want 2", total)
  	}
  	for _, s := range sts {
  		if !strings.Contains(strings.ToLower(s.Location), "bristol") {
  			t.Errorf("station %d (%q) matched a bristol search", s.StationID, s.Location)
  		}
  	}

  	// A search matching a name rather than a location.
  	sts, total, err = ss.List(ctx, store.StationFilter{Search: "kelvin", Limit: 50})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 1 || len(sts) != 1 || sts[0].StationID != 2000 {
  		t.Fatalf("search kelvin = %v / total %d, want [2000] / 1", stationIDs(sts), total)
  	}

  	// A search matching nothing.
  	sts, total, err = ss.List(ctx, store.StationFilter{Search: "reykjavik", Limit: 50})
  	if err != nil {
  		t.Fatalf("List: %v", err)
  	}
  	if total != 0 || len(sts) != 0 {
  		t.Fatalf("search reykjavik = %v / total %d, want none", stationIDs(sts), total)
  	}
  }

  // TestStationSearchNeverErrorsOnAdversarialInput is the fuzz gate.
  //
  // websearch_to_tsquery is DEFINED never to raise on arbitrary input, which is
  // exactly why it must be used instead of to_tsquery: to_tsquery RAISES on
  // unbalanced quotes or a bare &, |, ! or :, and on a public search box that is
  // a 500 on a keystroke.
  //
  // The function being total is necessary but NOT sufficient, and the NUL entries
  // in the table below are why. A NUL byte never reaches websearch_to_tsquery: it
  // fails while pgx is BINDING the parameter, with SQLSTATE 22021 invalid byte
  // sequence (measured against postgres:17-alpine with pgx v5.10.0), so
  // `?search=%00` was a 500 until List learned to screen f.Search with
  // store.ValidText. Keep those entries: they are the regression test on the only
  // input in this table that ever actually broke.
  func TestStationSearchNeverErrorsOnAdversarialInput(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	ss := postgres.NewStationStore(pool)

  	inputs := []string{
  		"", " ", "0", "00", "0abc", ":*", "&|!()", "\"unclosed", "\\", "\\\\",
  		"<->", "a:b:c", "'; DROP TABLE stations; --", "%", "_", "*",
  		strings.Repeat("a", 200), "\x00nul", "nul\x00", "bristol OR 1=1", "-bristol",
  		"NOT", "AND", "()()()", "\t\n",
  	}
  	if len(inputs) < 14 {
  		t.Fatalf("the fuzz table has %d inputs, want at least 14", len(inputs))
  	}
  	for _, in := range inputs {
  		sts, total, err := ss.List(ctx, store.StationFilter{Search: in, Limit: 50})
  		if err != nil {
  			t.Errorf("List(search=%q) error = %v, want nil", in, err)
  			continue
  		}
  		if total < 0 {
  			t.Errorf("List(search=%q) total = %d", in, total)
  		}
  		if int64(len(sts)) > total {
  			t.Errorf("List(search=%q) returned %d rows with total %d", in, len(sts), total)
  		}
  	}
  }

  func TestStationListReflectsLastReadingForOnlineDerivation(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	ss := postgres.NewStationStore(pool)

  	fresh := time.Now().UTC().Add(-2 * time.Minute)
  	if _, err := pool.Exec(ctx,
  		"UPDATE stations SET last_reading = $1 WHERE station_id = 1000", fresh); err != nil {
  		t.Fatalf("setting last_reading: %v", err)
  	}

  	// The store returns IsActive and LastReading and does NOT derive "online".
  	// That keeps the freshness window in config, keeps the DTO test
  	// deterministic without freezing now(), and stops the poll rate leaking
  	// into persistence.
  	got, err := ss.Get(ctx, 1000)
  	if err != nil {
  		t.Fatalf("Get: %v", err)
  	}
  	if !got.IsActive {
  		t.Error("IsActive = false, want true")
  	}
  	if got.LastReading == nil {
  		t.Fatal("LastReading is nil")
  	}
  	if got.LastReading.Sub(fresh).Abs() > time.Second {
  		t.Errorf("LastReading = %v, want ~%v", got.LastReading, fresh)
  	}

  	inactive, err := ss.Get(ctx, 1002)
  	if err != nil {
  		t.Fatalf("Get 1002: %v", err)
  	}
  	if inactive.IsActive {
  		t.Error("station 1002 IsActive = true, want false")
  	}
  	if inactive.LastReading != nil {
  		t.Errorf("station 1002 LastReading = %v, want nil", inactive.LastReading)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `undefined: postgres.StationStore` and `undefined: postgres.NewStationStore`.

- [ ] **Write the implementation.** Create `internal/store/postgres/stations.go` with exactly this content:
  ```go
  package postgres

  import (
  	"context"
  	"strconv"

  	"github.com/jackc/pgx/v5"
  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  )

  // stationColumns is the explicit column list of stations, matching
  // store.Station's db tags in order. As everywhere in this package, SELECT * is
  // forbidden.
  const stationColumns = `station_id, name, location, latitude, longitude, is_active, tx_records,
         last_reading, last_temp, last_conditions, last_block_height, created_at, updated_at`

  // upsertStationSQL refreshes a station's IDENTITY columns only.
  //
  // What it deliberately does not touch: tx_records, last_reading, last_temp,
  // last_conditions and last_block_height. Those are counters that only Complete
  // and SetBlockHeights move, and an upsert that reset them would zero the whole
  // dashboard on every poll.
  const upsertStationSQL = `
  INSERT INTO stations (station_id, name, location, latitude, longitude, is_active, updated_at)
  VALUES ($1, $2, $3, $4, $5, $6, now())
  ON CONFLICT (station_id) DO UPDATE
     SET name       = EXCLUDED.name,
         location   = EXCLUDED.location,
         latitude   = EXCLUDED.latitude,
         longitude  = EXCLUDED.longitude,
         is_active  = EXCLUDED.is_active,
         updated_at = now()`

  const getStationSQL = `
  SELECT ` + stationColumns + `
    FROM stations WHERE station_id = $1`

  // The three list branches. ONE parsed value drives BOTH the filter and the
  // sort, which is what removes the TypeScript's ?search=0 500 (there the filter
  // and the sort were decided by two different expressions that disagreed for
  // the input "0").
  //
  // Each branch is a WHOLE constant statement rather than a shared skeleton with
  // an appended fragment. That is the pattern this codebase requires wherever a
  // query shape varies: map an opaque input to a complete const query, never
  // concatenate.
  const listStationsAllSQL = `
  SELECT ` + stationColumns + `
    FROM stations ORDER BY station_id ASC LIMIT $1 OFFSET $2`

  const countStationsAllSQL = `SELECT count(*) FROM stations`

  const listStationsByIDSQL = `
  SELECT ` + stationColumns + `
    FROM stations WHERE station_id = $1 ORDER BY station_id ASC LIMIT $2 OFFSET $3`

  const countStationsByIDSQL = `SELECT count(*) FROM stations WHERE station_id = $1`

  // websearch_to_tsquery and NEVER to_tsquery. to_tsquery RAISES on unbalanced
  // quotes or a bare &, |, ! or :, which on a public search box is a 500 from a
  // single keystroke. websearch_to_tsquery is defined never to error on arbitrary
  // input; verified over 25 adversarial strings including 200 characters, ":*",
  // "&|!()" and an unclosed quote.
  //
  // A NUL byte is the one input this statement cannot receive at all: it fails
  // during parameter BINDING with SQLSTATE 22021, before any function runs. List
  // screens it out with store.ValidText, so the "cannot 500" property belongs to
  // the pair and not to websearch_to_tsquery alone.
  const listStationsSearchSQL = `
  SELECT ` + stationColumns + `
    FROM stations
   WHERE search_tsv @@ websearch_to_tsquery('english', $1)
   ORDER BY ts_rank(search_tsv, websearch_to_tsquery('english', $1)) DESC, station_id ASC
   LIMIT $2 OFFSET $3`

  const countStationsSearchSQL = `
  SELECT count(*) FROM stations WHERE search_tsv @@ websearch_to_tsquery('english', $1)`

  // StationStore is the pgx/v5 implementation of store.StationStore.
  type StationStore struct {
  	db *pgxpool.Pool
  }

  // NewStationStore returns a StationStore backed by db.
  func NewStationStore(db *pgxpool.Pool) *StationStore {
  	return &StationStore{db: db}
  }

  // Upsert implements store.StationStore.
  func (s *StationStore) Upsert(ctx context.Context, in store.Station) error {
  	_, err := s.db.Exec(ctx, upsertStationSQL,
  		in.StationID, in.Name, in.Location, in.Latitude, in.Longitude, in.IsActive)
  	if err != nil {
  		return classify(err)
  	}
  	return nil
  }

  // Get implements store.StationStore.
  func (s *StationStore) Get(ctx context.Context, stationID int64) (store.Station, error) {
  	rows, err := s.db.Query(ctx, getStationSQL, stationID)
  	if err != nil {
  		return store.Station{}, classify(err)
  	}
  	st, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[store.Station])
  	if err != nil {
  		return store.Station{}, classify(err)
  	}
  	return st, nil
  }

  // List implements store.StationStore.
  //
  // The branch is chosen in Go by strconv.Atoi, which is a deliberate behavior
  // change from the TypeScript's parseInt: JS parseInt("12345abc") returns 12345
  // and so treats that as an exact id lookup, whereas Atoi errors and this routes
  // it to full-text search. Go's behavior is the better one and matches the
  // spec's phrasing, but it is a visible change on a free-text box.
  func (s *StationStore) List(ctx context.Context, f store.StationFilter) ([]store.Station, int64, error) {
  	if f.Search == "" {
  		return s.listStations(ctx, listStationsAllSQL, countStationsAllSQL,
  			[]any{f.Limit, f.Offset}, nil)
  	}
  	// A search Postgres cannot even BIND matches nothing, and says so with an
  	// empty page rather than an error. Binding a NUL byte fails with SQLSTATE
  	// 22021 before websearch_to_tsquery is reached (measured), and
  	// `?search=%00` hands one over from Go's query decoding, so without this the
  	// public search box 500s on a single crafted request. Empty results rather
  	// than store.ErrInvalidText is deliberate at THIS layer: the store stays
  	// total, and B2's parse helpers return the 400 that tells the caller why.
  	if store.ValidText(f.Search) != nil {
  		return []store.Station{}, 0, nil
  	}
  	// ParseInt with bitSize 64 rather than Atoi, so a value that overflows the
  	// bigint column is a parse FAILURE and falls through to text search rather
  	// than reaching Postgres and raising 22003 numeric_out_of_range, which the
  	// API layer would have to render as a 500.
  	id, err := strconv.ParseInt(f.Search, 10, 64)
  	if err == nil {
  		return s.listStations(ctx, listStationsByIDSQL, countStationsByIDSQL,
  			[]any{id, f.Limit, f.Offset}, []any{id})
  	}
  	return s.listStations(ctx, listStationsSearchSQL, countStationsSearchSQL,
  		[]any{f.Search, f.Limit, f.Offset}, []any{f.Search})
  }

  // listStations runs one of the three branch pairs.
  //
  // listStmt and countStmt are always two of the six package constants above.
  // They are parameters of a private method and can never be derived from input:
  // List's switch is the only caller and it passes constants.
  func (s *StationStore) listStations(
  	ctx context.Context,
  	listStmt, countStmt string,
  	listArgs, countArgs []any,
  ) ([]store.Station, int64, error) {
  	rows, err := s.db.Query(ctx, listStmt, listArgs...)
  	if err != nil {
  		return nil, 0, classify(err)
  	}
  	sts, err := pgx.CollectRows(rows, pgx.RowToStructByName[store.Station])
  	if err != nil {
  		return nil, 0, classify(err)
  	}

  	var total int64
  	if err := s.db.QueryRow(ctx, countStmt, countArgs...).Scan(&total); err != nil {
  		return nil, 0, classify(err)
  	}
  	return sts, total, nil
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run TestStation -v
  ```
  Expected output: `--- PASS` for `TestStationUpsertInsertsThenUpdates`, `TestStationGetUnknownIsErrNotFound`, `TestStationListNoSearchIsAscendingByID`, `TestStationListNumericSearchIsAnExactLookup`, `TestStationListTextSearchRanksAndFallsBackToID`, `TestStationSearchNeverErrorsOnAdversarialInput`, `TestStationListReflectsLastReadingForOnlineDerivation`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/stations.go internal/store/postgres/stations_test.go && git commit -m "postgres: stations upsert/get/list with the one-parsed-value search split"
  ```

---

## Task 14: Stats and Snapshot

**Files:**
- Modify: `internal/store/postgres/stations.go`
- Modify: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/stats_test.go`

**Interfaces:**

Consumes (Task 1): `store.Stats`, `store.Stats.TotalDataPoints()`, `store.Snapshot`, `weather.DataFieldsPerRecord`. Consumes (Task 2): `Stats(ctx context.Context) (Stats, error)` on `store.StationStore`, `Snapshot(ctx context.Context) (Snapshot, error)` on `store.RecordStore`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 9): `statsSQL`, `readStats(ctx context.Context, q rowQuerier) (store.Stats, error)`, `rowQuerier`. Consumes (Task 13): `postgres.StationStore`, `postgres.NewStationStore`; plus `storeSchema` from `migrations_test.go`, `fullWeatherData` from `records_insert_test.go`, `seedPending` from `records_claim_test.go`, `seedStations` from `stations_test.go`.

Produces:
```go
package postgres
const snapshotSQL string
func (s *StationStore) Stats(ctx context.Context) (store.Stats, error)
func (s *RecordStore) Snapshot(ctx context.Context) (store.Snapshot, error)
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/stats_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"testing"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
  )

  var (
  	_ func(context.Context) (store.Stats, error)    = (*postgres.StationStore)(nil).Stats
  	_ func(context.Context) (store.Snapshot, error) = (*postgres.RecordStore)(nil).Snapshot
  )

  // TestStatsActiveStationsIsALiveCount is the regression gate on one of the two
  // stats bugs this design fixes: activeStations was a STORED counter that could
  // get stuck at 0. It is now `SELECT count(*) FROM stations WHERE is_active`
  // (~20 rows, free), so being stuck is structurally impossible.
  func TestStatsActiveStationsIsALiveCount(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	ss := postgres.NewStationStore(pool)

  	stats, err := ss.Stats(ctx)
  	if err != nil {
  		t.Fatalf("Stats: %v", err)
  	}
  	if stats.ActiveStations != 3 {
  		t.Fatalf("ActiveStations = %d, want 3 (one of the four seeded stations is inactive)", stats.ActiveStations)
  	}

  	if _, execErr := pool.Exec(ctx,
  		"UPDATE stations SET is_active = false WHERE station_id = 1000"); execErr != nil {
  		t.Fatalf("deactivating: %v", execErr)
  	}
  	stats, err = ss.Stats(ctx)
  	if err != nil {
  		t.Fatalf("Stats: %v", err)
  	}
  	if stats.ActiveStations != 2 {
  		t.Fatalf("ActiveStations = %d after a deactivation, want 2", stats.ActiveStations)
  	}

  	if _, allErr := pool.Exec(ctx, "UPDATE stations SET is_active = false"); allErr != nil {
  		t.Fatalf("deactivating all: %v", allErr)
  	}
  	stats, err = ss.Stats(ctx)
  	if err != nil {
  		t.Fatalf("Stats: %v", err)
  	}
  	if stats.ActiveStations != 0 {
  		t.Fatalf("ActiveStations = %d with everything inactive, want 0", stats.ActiveStations)
  	}
  }

  func TestStatsOnAnEmptyDatabase(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ss := postgres.NewStationStore(pool)

  	stats, err := ss.Stats(ctx)
  	if err != nil {
  		t.Fatalf("Stats: %v", err)
  	}
  	if stats.ActiveStations != 0 || stats.TotalTx != 0 || stats.TotalRecords != 0 {
  		t.Fatalf("stats on an empty database = %+v, want all zero", stats)
  	}
  	if stats.LastRecordWrite != nil {
  		t.Fatalf("LastRecordWrite = %v on an empty database, want nil (a zero time.Time "+
  			"would marshal to 0001-01-01T00:00:00Z and render as 01/01/0001)", stats.LastRecordWrite)
  	}
  	if stats.TotalDataPoints() != 0 {
  		t.Fatalf("TotalDataPoints = %d, want 0", stats.TotalDataPoints())
  	}
  }

  func TestStatsAfterPublishing(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	rs := postgres.NewRecordStore(pool)
  	ss := postgres.NewStationStore(pool)

  	seedPending(t, pool, 4, 1000)
  	claimed, err := rs.ClaimPending(ctx, 4, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("ClaimPending: %v", err)
  	}
  	pubs := make([]store.Publication, 0, len(claimed))
  	for i, r := range claimed {
  		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
  	}
  	if _, completeErr := rs.Complete(ctx, "tx-one", pubs); completeErr != nil {
  		t.Fatalf("Complete: %v", completeErr)
  	}

  	stats, err := ss.Stats(ctx)
  	if err != nil {
  		t.Fatalf("Stats: %v", err)
  	}
  	if stats.TotalTx != 1 {
  		t.Errorf("TotalTx = %d, want 1", stats.TotalTx)
  	}
  	if stats.TotalRecords != 4 {
  		t.Errorf("TotalRecords = %d, want 4", stats.TotalRecords)
  	}
  	// The multiplier is weather.DataFieldsPerRecord and never a literal 33 in
  	// SQL: one definition per module, or the two drift.
  	if want := int64(4) * weather.DataFieldsPerRecord; stats.TotalDataPoints() != want {
  		t.Errorf("TotalDataPoints = %d, want %d", stats.TotalDataPoints(), want)
  	}
  	if stats.LastRecordWrite == nil {
  		t.Error("LastRecordWrite is nil after a publish")
  	}
  }

  func TestSnapshotCountsEveryBucket(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	// One row per bucket, written directly so the state is unambiguous.
  	fixtures := []struct {
  		id   string
  		stmt string
  	}{
  		{"pending", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  			VALUES ('pending', 1, now(), now(), $1)`},
  		{"processing", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, claimed_at)
  			VALUES ('processing', 2, now(), now(), $1, 'processing', now())`},
  		{"failed", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
  			VALUES ('failed', 3, now(), now(), $1, 'failed')`},
  		{"unmined-old", `INSERT INTO weather_records
  			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
  			VALUES ('unmined-old', 4, now(), now(), $1, 'completed', 'tx-a', 0,
  			        now() - interval '2 hours', 'arc-accepted')`},
  		{"unmined-fresh", `INSERT INTO weather_records
  			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
  			VALUES ('unmined-fresh', 5, now(), now(), $1, 'completed', 'tx-b', 0, now(), 'arc-accepted')`},
  		{"mined", `INSERT INTO weather_records
  			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status, block_height)
  			VALUES ('mined', 6, now(), now(), $1, 'completed', 'tx-c', 0,
  			        now() - interval '3 hours', 'mined', 900001)`},
  		{"aborted", `INSERT INTO weather_records
  			(id, station_id, timestamp, observation_time, data, status, chain_status)
  			VALUES ('aborted', 7, now(), now(), $1, 'failed', 'aborted')`},
  		{"null-chain-old", `INSERT INTO weather_records
  			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
  			VALUES ('null-chain-old', 8, now(), now(), $1, 'completed', 'tx-d', 0,
  			        now() - interval '5 hours')`},
  	}
  	for _, f := range fixtures {
  		if _, err := pool.Exec(ctx, f.stmt, fullWeatherData()); err != nil {
  			t.Fatalf("seeding %s: %v", f.id, err)
  		}
  	}

  	snap, err := rs.Snapshot(ctx)
  	if err != nil {
  		t.Fatalf("Snapshot: %v", err)
  	}
  	if snap.PendingRows != 1 {
  		t.Errorf("PendingRows = %d, want 1", snap.PendingRows)
  	}
  	if snap.ProcessingRows != 1 {
  		t.Errorf("ProcessingRows = %d, want 1", snap.ProcessingRows)
  	}
  	if snap.FailedRows != 2 {
  		t.Errorf("FailedRows = %d, want 2 (failed plus aborted)", snap.FailedRows)
  	}
  	// unmined-old and null-chain-old qualify; unmined-fresh is inside the hour
  	// and mined is excluded by chain_status.
  	if snap.StillUnminedOlderThan1h != 2 {
  		t.Errorf("StillUnminedOlderThan1h = %d, want 2", snap.StillUnminedOlderThan1h)
  	}
  	if snap.MinedCount != 1 {
  		t.Errorf("MinedCount = %d, want 1", snap.MinedCount)
  	}
  	if snap.AbortedCount != 1 {
  		t.Errorf("AbortedCount = %d, want 1", snap.AbortedCount)
  	}
  }

  func TestSnapshotOnAnEmptyDatabaseIsAllZero(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	snap, err := rs.Snapshot(ctx)
  	if err != nil {
  		t.Fatalf("Snapshot: %v", err)
  	}
  	if snap != (store.Snapshot{}) {
  		t.Fatalf("snapshot on an empty database = %+v, want the zero value", snap)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `(*postgres.StationStore)(nil).Stats undefined` and `(*postgres.RecordStore)(nil).Snapshot undefined`.

- [ ] **Add Stats to the station store.** Append exactly this to `internal/store/postgres/stations.go`:
  ```go
  // Stats implements store.StationStore.
  //
  // The statement lives in records.go next to Complete, which reads it inside the
  // publish transaction; readStats is shared so the two can never disagree about
  // what the four dashboard values are.
  func (s *StationStore) Stats(ctx context.Context) (store.Stats, error) {
  	return readStats(ctx, s.db)
  }
  ```

- [ ] **Add Snapshot to the record store.** Append exactly this to `internal/store/postgres/records.go`:
  ```go
  // snapshotSQL is the row-count half of the operational heartbeat, in one query.
  //
  // This is a Seq Scan and will stay one: the chain_status and processed_at
  // filters cannot be served by ix_records_status_created. That is fine at a
  // 60-second sampler interval and ~105k rows a year, but it IS an
  // unbounded-growth full scan, so it is written down here rather than being a
  // surprise in year five.
  const snapshotSQL = `
  SELECT count(*) FILTER (WHERE status = 'pending')    AS pending_rows,
         count(*) FILTER (WHERE status = 'processing') AS processing_rows,
         count(*) FILTER (WHERE status = 'failed')     AS failed_rows,
         count(*) FILTER (WHERE status = 'completed'
                            AND (chain_status IS NULL OR chain_status <> 'mined')
                            AND processed_at < now() - interval '1 hour')
                                                       AS still_unmined_older_than_1h,
         count(*) FILTER (WHERE chain_status = 'mined')   AS mined_count,
         count(*) FILTER (WHERE chain_status = 'aborted') AS aborted_count
    FROM weather_records`

  // Snapshot implements store.RecordStore.
  func (s *RecordStore) Snapshot(ctx context.Context) (store.Snapshot, error) {
  	var out store.Snapshot
  	err := s.db.QueryRow(ctx, snapshotSQL).Scan(
  		&out.PendingRows, &out.ProcessingRows, &out.FailedRows,
  		&out.StillUnminedOlderThan1h, &out.MinedCount, &out.AbortedCount)
  	if err != nil {
  		return store.Snapshot{}, classify(err)
  	}
  	return out, nil
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run 'TestStats|TestSnapshot' -v
  ```
  Expected output: `--- PASS` for `TestStatsActiveStationsIsALiveCount`, `TestStatsOnAnEmptyDatabase`, `TestStatsAfterPublishing`, `TestSnapshotCountsEveryBucket`, `TestSnapshotOnAnEmptyDatabaseIsAllZero`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/stations.go internal/store/postgres/records.go internal/store/postgres/stats_test.go && git commit -m "postgres: live activeStations Stats and the six-bucket Snapshot"
  ```

---

## Task 15: SetBlockHeights and ReconcileCandidates

**Files:**
- Modify: `internal/store/postgres/records.go`
- Create: `internal/store/postgres/records_chain_test.go`

**Interfaces:**

Consumes (Task 1): `store.BlockHeightUpdate`, `store.Record`, `store.ChainMined`, `store.StatusCompleted`. Consumes (Task 2): `SetBlockHeights(ctx context.Context, ups []BlockHeightUpdate) error`, `ReconcileCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]Record, error)`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `recordColumns`, `classify`, `postgres.NewRecordStore`. Consumes (Task 8): `collectRecords`, `seedPending`. Consumes (Task 11): `intervalArg(d time.Duration) string`. Consumes (Task 13): `postgres.NewStationStore`; plus `storeSchema` from `migrations_test.go`, `fullWeatherData` from `records_insert_test.go`, `seedStations` from `stations_test.go`.

Produces:
```go
package postgres
func (s *RecordStore) SetBlockHeights(ctx context.Context, ups []store.BlockHeightUpdate) error
func (s *RecordStore) ReconcileCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]store.Record, error)
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/records_chain_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  var (
  	_ func(context.Context, []store.BlockHeightUpdate) error            = (*postgres.RecordStore)(nil).SetBlockHeights
  	_ func(context.Context, time.Duration, int) ([]store.Record, error) = (*postgres.RecordStore)(nil).ReconcileCandidates
  )

  // publishOneBatch seeds n pending rows for stationID, claims them and completes
  // them under txid. It returns the completed record ids.
  //
  // It is safe to call twice for the SAME station: seedPending gives every call
  // its own observation_time window, so the second call cannot collide with the
  // first on ux_records_station_obs. (It could, before seedPending grew its
  // per-call offset — the symptom was SQLSTATE 23505 inside the helper, from a
  // test whose actual assertion never ran.) It does NOT seed the stations row;
  // callers that assert on station columns call seedStations first.
  func publishOneBatch(t testing.TB, pool *pgxpool.Pool, stationID int64, n int, txid string) []string {
  	t.Helper()
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	seedPending(t, pool, n, stationID)

  	claimed, err := rs.ClaimPending(ctx, n, uuid.Must(uuid.NewV7()))
  	if err != nil {
  		t.Fatalf("publishOneBatch: ClaimPending: %v", err)
  	}
  	pubs := make([]store.Publication, 0, len(claimed))
  	ids := make([]string, 0, len(claimed))
  	for i, r := range claimed {
  		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
  		ids = append(ids, r.ID)
  	}
  	if _, completeErr := rs.Complete(ctx, txid, pubs); completeErr != nil {
  		t.Fatalf("publishOneBatch: Complete: %v", completeErr)
  	}
  	return ids
  }

  // TestSetBlockHeightsWritesBothTablesInOneTransaction is the store half of the
  // unauthenticated verify write path.
  //
  // The security invariant this preserves: the CALLER chooses only WHICH existing
  // rows are refreshed, never the VALUE. The height comes from the block explorer
  // and is assigned into store.BlockHeightUpdate by the verify handler, and the
  // HTTP request DTO has no height field at all. If a rewrite ever lets a caller
  // supply blockHeight, that is trivial data forgery on a proof-of-existence
  // demo.
  func TestSetBlockHeightsWritesBothTablesInOneTransaction(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	rs := postgres.NewRecordStore(pool)
  	ssv := postgres.NewStationStore(pool)

  	const txid = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
  	ids := publishOneBatch(t, pool, 1000, 3, txid)

  	minedAt := time.Date(2026, 4, 17, 16, 0, 0, 0, time.UTC)
  	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
  		{TxID: txid, BlockHeight: 900123, MinedAt: &minedAt},
  	}); err != nil {
  		t.Fatalf("SetBlockHeights: %v", err)
  	}

  	for _, id := range ids {
  		rec, err := rs.Get(ctx, id)
  		if err != nil {
  			t.Fatalf("Get %s: %v", id, err)
  		}
  		if rec.BlockHeight == nil || *rec.BlockHeight != 900123 {
  			t.Errorf("%s block height = %v, want 900123", id, rec.BlockHeight)
  		}
  		if rec.ChainStatus == nil || *rec.ChainStatus != store.ChainMined {
  			t.Errorf("%s chain status = %v, want mined", id, rec.ChainStatus)
  		}
  		if rec.MinedAt == nil || !rec.MinedAt.Equal(minedAt) {
  			t.Errorf("%s mined at = %v, want %v", id, rec.MinedAt, minedAt)
  		}
  		if rec.Status != store.StatusCompleted {
  			t.Errorf("%s status = %q, want an unchanged completed", id, string(rec.Status))
  		}
  	}

  	// stations.last_block_height moved in the SAME transaction. The frontend's
  	// auto-verify loop re-fires forever if the refreshed list does not already
  	// carry the height, because its `attempted` ref is per-mount and does not
  	// survive navigation.
  	st, err := ssv.Get(ctx, 1000)
  	if err != nil {
  		t.Fatalf("Get station: %v", err)
  	}
  	if st.LastBlockHeight == nil || *st.LastBlockHeight != 900123 {
  		t.Fatalf("stations.last_block_height = %v, want 900123", st.LastBlockHeight)
  	}
  }

  func TestSetBlockHeightsNeverLowersAStationHeight(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	rs := postgres.NewRecordStore(pool)
  	ssv := postgres.NewStationStore(pool)

  	publishOneBatch(t, pool, 1000, 1, "tx-high")
  	publishOneBatch(t, pool, 1000, 1, "tx-low")

  	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
  		{TxID: "tx-high", BlockHeight: 900500},
  	}); err != nil {
  		t.Fatalf("SetBlockHeights high: %v", err)
  	}
  	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
  		{TxID: "tx-low", BlockHeight: 900100},
  	}); err != nil {
  		t.Fatalf("SetBlockHeights low: %v", err)
  	}

  	st, err := ssv.Get(ctx, 1000)
  	if err != nil {
  		t.Fatalf("Get station: %v", err)
  	}
  	if st.LastBlockHeight == nil || *st.LastBlockHeight != 900500 {
  		t.Fatalf("stations.last_block_height = %v, want the greatest seen 900500", st.LastBlockHeight)
  	}
  }

  func TestSetBlockHeightsNoOpsForAnUnknownTxID(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	seedStations(t, pool)
  	rs := postgres.NewRecordStore(pool)
  	ssv := postgres.NewStationStore(pool)

  	// Station 1001 rather than 1000, and not arbitrarily. `unparam` flags a
  	// parameter every call site passes the same value for, and it is right to: a
  	// helper whose station id is decorative is a helper nobody can tell is
  	// honoring it. seedStations creates 1001, so this covers a second station on
  	// the same path.
  	ids := publishOneBatch(t, pool, 1001, 1, "tx-real")

  	// This is the second property that bounds the unauthenticated write path:
  	// the update matches nothing when no row carries the txid.
  	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
  		{TxID: "0000000000000000000000000000000000000000000000000000000000000000", BlockHeight: 999999},
  		{TxID: "", BlockHeight: 1},
  		{TxID: "not-a-txid", BlockHeight: 2},
  	}); err != nil {
  		t.Fatalf("SetBlockHeights for unknown txids: %v", err)
  	}

  	rec, err := rs.Get(ctx, ids[0])
  	if err != nil {
  		t.Fatalf("Get: %v", err)
  	}
  	if rec.BlockHeight != nil {
  		t.Fatalf("an unrelated row got block height %v", rec.BlockHeight)
  	}
  	st, err := ssv.Get(ctx, 1001)
  	if err != nil {
  		t.Fatalf("Get station: %v", err)
  	}
  	if st.LastBlockHeight != nil {
  		t.Fatalf("an unrelated station got last_block_height %v", st.LastBlockHeight)
  	}

  	// An empty update list is a no-op, not an error.
  	if err := rs.SetBlockHeights(ctx, nil); err != nil {
  		t.Fatalf("SetBlockHeights(nil): %v", err)
  	}
  }

  func TestSetBlockHeightsOnlyTouchesCompletedRows(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	ssv := postgres.NewStationStore(pool)

  	// The station row MUST exist for this test to mean anything. An earlier draft
  	// seeded only the record, so the station half of the write went unobserved:
  	// setStationHeightSQL's subquery had no status filter, and a txid carried only
  	// by an aborted row still raised that station's displayed last_block_height.
  	// With no stations row the UPDATE matched nothing for an unrelated reason and
  	// the hole was invisible.
  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, is_active) VALUES (1, true)"); err != nil {
  		t.Fatalf("seeding station: %v", err)
  	}

  	// A failed row that nevertheless carries a txid — the aborted case.
  	if _, insertErr := pool.Exec(ctx, `
  		INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, txid, chain_status)
  		VALUES ('aborted', 1, now(), now(), $1, 'failed', 'tx-aborted', 'aborted')`,
  		fullWeatherData()); insertErr != nil {
  		t.Fatalf("seeding: %v", insertErr)
  	}

  	if setErr := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
  		{TxID: "tx-aborted", BlockHeight: 900777},
  	}); setErr != nil {
  		t.Fatalf("SetBlockHeights: %v", setErr)
  	}

  	rec, err := rs.Get(ctx, "aborted")
  	if err != nil {
  		t.Fatalf("Get: %v", err)
  	}
  	if rec.BlockHeight != nil {
  		t.Fatalf("an aborted row got block height %v; verify must only refresh completed rows", rec.BlockHeight)
  	}
  	if rec.ChainStatus == nil || *rec.ChainStatus != store.ChainAborted {
  		t.Fatalf("chain status = %v, want an unchanged aborted", rec.ChainStatus)
  	}

  	// And the station it belongs to must be untouched. This is the assertion the
  	// unauthenticated verify path actually needs: an attacker who knows the txid
  	// of a transaction that was ABORTED must not be able to make the dashboard
  	// claim that station is confirmed at a height.
  	st, err := ssv.Get(ctx, 1)
  	if err != nil {
  		t.Fatalf("Get station: %v", err)
  	}
  	if st.LastBlockHeight != nil {
  		t.Fatalf("stations.last_block_height = %v from an ABORTED row's txid, want nil", st.LastBlockHeight)
  	}
  }

  func TestReconcileCandidatesSelectsOnlyUnminedCompletedRows(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	fixtures := []string{
  		// Old, completed, no chain status at all: a candidate.
  		`INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
  		 VALUES ('cand-null', 1, now(), now(), $1, 'completed', 'tx-1', 0, now() - interval '5 hours')`,
  		// Old, completed, arc-accepted: also a candidate.
  		`INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
  		 VALUES ('cand-arc', 2, now(), now(), $1, 'completed', 'tx-2', 0, now() - interval '4 hours', 'arc-accepted')`,
  		// Old, completed, already mined: NOT a candidate.
  		`INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
  		 VALUES ('mined', 3, now(), now(), $1, 'completed', 'tx-3', 0, now() - interval '6 hours', 'mined')`,
  		// Recent, completed, unmined: NOT a candidate yet.
  		`INSERT INTO weather_records
  		  (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
  		 VALUES ('fresh', 4, now(), now(), $1, 'completed', 'tx-4', 0, now(), 'arc-accepted')`,
  		// Pending: NOT a candidate.
  		`INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
  		 VALUES ('pending', 5, now(), now(), $1)`,
  	}
  	for i, stmt := range fixtures {
  		if _, err := pool.Exec(ctx, stmt, fullWeatherData()); err != nil {
  			t.Fatalf("seeding %d: %v", i, err)
  		}
  	}

  	cands, err := rs.ReconcileCandidates(ctx, time.Hour, 100)
  	if err != nil {
  		t.Fatalf("ReconcileCandidates: %v", err)
  	}
  	got := make(map[string]bool, len(cands))
  	for _, c := range cands {
  		got[c.ID] = true
  	}
  	if len(cands) != 2 || !got["cand-null"] || !got["cand-arc"] {
  		ids := make([]string, 0, len(cands))
  		for _, c := range cands {
  			ids = append(ids, c.ID)
  		}
  		t.Fatalf("candidates = %v, want exactly [cand-null cand-arc]", ids)
  	}

  	// Oldest first, so a reconciler tick always makes progress on the worst
  	// backlog.
  	if cands[0].ID != "cand-arc" && cands[0].ID != "cand-null" {
  		t.Fatalf("unexpected first candidate %q", cands[0].ID)
  	}
  	if !cands[0].ProcessedAt.Before(*cands[1].ProcessedAt) {
  		t.Fatalf("candidates are not oldest-first: %v then %v", cands[0].ProcessedAt, cands[1].ProcessedAt)
  	}

  	// The limit is honored.
  	limited, err := rs.ReconcileCandidates(ctx, time.Hour, 1)
  	if err != nil {
  		t.Fatalf("ReconcileCandidates with limit 1: %v", err)
  	}
  	if len(limited) != 1 {
  		t.Fatalf("limit 1 returned %d rows", len(limited))
  	}
  }

  func TestReconcileCandidatesOnAnEmptyDatabase(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)

  	cands, err := rs.ReconcileCandidates(ctx, time.Hour, 100)
  	if err != nil {
  		t.Fatalf("ReconcileCandidates: %v", err)
  	}
  	if len(cands) != 0 {
  		t.Fatalf("candidates = %d on an empty database, want 0", len(cands))
  	}
  }
  ```
  Add `"github.com/jackc/pgx/v5/pgxpool"` to that file's import block (`publishOneBatch` takes `*pgxpool.Pool`).

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `(*postgres.RecordStore)(nil).SetBlockHeights undefined` and `(*postgres.RecordStore)(nil).ReconcileCandidates undefined`.

- [ ] **Write the implementation.** Append exactly this to `internal/store/postgres/records.go`:
  ```go
  // setBlockHeightSQL refreshes the mined height of every record sharing a txid.
  //
  // `AND status = 'completed'` is what stops an aborted row (which can carry a
  // txid) being reported as mined. mined_at falls back to now() when the caller
  // has no upstream timestamp.
  const setBlockHeightSQL = `
  UPDATE weather_records
     SET block_height = $2,
         chain_status = 'mined',
         mined_at     = coalesce($3::timestamptz, now())
   WHERE txid = $1 AND status = 'completed'`

  // setStationHeightSQL mirrors the height onto every station that has a record
  // in this transaction. greatest(...) means a later confirmation for an OLDER
  // transaction can never lower a station's displayed height.
  //
  // `AND r.status = 'completed'` in the subquery, for the SAME reason
  // setBlockHeightSQL carries it, and its absence was a real hole rather than a
  // theoretical one: an aborted row keeps its txid, so without the filter a txid
  // that exists ONLY on aborted or failed rows still raised that station's
  // displayed last_block_height — on the unauthenticated verify path, where the
  // caller chooses the txids. The record filter alone was not enough because the
  // two statements select their targets independently.
  const setStationHeightSQL = `
  UPDATE stations AS s
     SET last_block_height = greatest(coalesce(s.last_block_height, $2), $2),
         updated_at        = now()
   WHERE s.station_id IN (
           SELECT r.station_id FROM weather_records AS r
            WHERE r.txid = $1 AND r.status = 'completed'
         )`

  // reconcileCandidatesSQL finds completed rows that are not yet known mined.
  // The partial index ix_records_reconcile serves it.
  //
  // `, id ASC` for the third and last time in this file, and the premise is
  // identical: completeRecordsSQL sets `processed_at = now()` for a whole batch in
  // one statement, so every row published together shares one processed_at to the
  // microsecond. Without the tiebreaker `LIMIT $2` takes an unspecified subset of
  // a tied batch, so two reconciler ticks can keep re-reading the same rows while
  // others in the same batch are never looked at.
  const reconcileCandidatesSQL = `
  SELECT ` + recordColumns + `
    FROM weather_records
   WHERE status = 'completed'
     AND (chain_status IS NULL OR chain_status <> 'mined')
     AND processed_at < now() - $1::interval
   ORDER BY processed_at ASC, id ASC
   LIMIT $2`

  // SetBlockHeights implements store.RecordStore.
  //
  // Both tables move in ONE transaction. The atomicity requirement transfers from
  // the Mongo session the TypeScript used on this path; the mechanism does not.
  //
  // SECURITY INVARIANT, and it is the whole reason this endpoint can be
  // unauthenticated: an HTTP caller supplies only txids. The BlockHeight in every
  // update is assigned from a block explorer's response by the verify handler,
  // and the request DTO has no height field. Do not add one.
  func (s *RecordStore) SetBlockHeights(ctx context.Context, ups []store.BlockHeightUpdate) error {
  	if len(ups) == 0 {
  		return nil
  	}
  	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
  		for _, u := range ups {
  			if _, err := tx.Exec(ctx, setBlockHeightSQL, u.TxID, u.BlockHeight, u.MinedAt); err != nil {
  				return classify(err)
  			}
  			if _, err := tx.Exec(ctx, setStationHeightSQL, u.TxID, u.BlockHeight); err != nil {
  				return classify(err)
  			}
  		}
  		return nil
  	})
  }

  // ReconcileCandidates implements store.RecordStore.
  func (s *RecordStore) ReconcileCandidates(
  	ctx context.Context, olderThan time.Duration, limit int,
  ) ([]store.Record, error) {
  	rows, err := s.db.Query(ctx, reconcileCandidatesSQL, intervalArg(olderThan), limit)
  	if err != nil {
  		return nil, classify(err)
  	}
  	return collectRecords(rows)
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run 'TestSetBlockHeights|TestReconcile' -v
  ```
  Expected output: `--- PASS` for `TestSetBlockHeightsWritesBothTablesInOneTransaction`, `TestSetBlockHeightsNeverLowersAStationHeight`, `TestSetBlockHeightsNoOpsForAnUnknownTxID`, `TestSetBlockHeightsOnlyTouchesCompletedRows`, `TestReconcileCandidatesSelectsOnlyUnminedCompletedRows`, `TestReconcileCandidatesOnAnEmptyDatabase`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/records.go internal/store/postgres/records_chain_test.go && git commit -m "postgres: block-height persistence in one transaction, and reconcile candidates"
  ```

---

## Task 16: Deposits and Preflight

**Files:**
- Create: `internal/store/postgres/deposits.go`
- Create: `internal/store/postgres/deposits_test.go`

**Interfaces:**

Consumes (Task 1): `store.Deposit`, `store.ErrNotFound`, `store.ErrConflict`. Consumes (Task 2): `NewDeposit(ctx context.Context, d Deposit) error`, `PendingDeposits(ctx context.Context) ([]Deposit, error)`, `MarkInternalized(ctx context.Context, suffix, txID string, vout int32, sats int64) error`, `PreflightOK(ctx context.Context, fingerprint string) (bool, error)`, `RecordPreflight(ctx context.Context, fingerprint string) error`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 7): `classify`; plus `storeSchema` from `migrations_test.go`.

Produces:
```go
package postgres
const depositColumns string
type DepositStore struct { /* unexported */ }
func NewDepositStore(db *pgxpool.Pool) *DepositStore
func (s *DepositStore) NewDeposit(ctx context.Context, d store.Deposit) error
func (s *DepositStore) PendingDeposits(ctx context.Context) ([]store.Deposit, error)
func (s *DepositStore) MarkInternalized(ctx context.Context, suffix, txID string, vout int32, sats int64) error

type PreflightStore struct { /* unexported */ }
func NewPreflightStore(db *pgxpool.Pool) *PreflightStore
func (s *PreflightStore) PreflightOK(ctx context.Context, fingerprint string) (bool, error)
func (s *PreflightStore) RecordPreflight(ctx context.Context, fingerprint string) error
```

### Steps

- [ ] **Write the failing test.** Create `internal/store/postgres/deposits_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"testing"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  var (
  	_ func(context.Context, store.Deposit) error                = (*postgres.DepositStore)(nil).NewDeposit
  	_ func(context.Context) ([]store.Deposit, error)            = (*postgres.DepositStore)(nil).PendingDeposits
  	_ func(context.Context, string, string, int32, int64) error = (*postgres.DepositStore)(nil).MarkInternalized
  	_ func(context.Context, string) (bool, error)               = (*postgres.PreflightStore)(nil).PreflightOK
  	_ func(context.Context, string) error                       = (*postgres.PreflightStore)(nil).RecordPreflight
  )

  func TestDepositLifecycle(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ds := postgres.NewDepositStore(pool)

  	first := store.Deposit{
  		Suffix: "c3VmZml4LW9uZQ==", Prefix: "cHJlZml4LW9uZQ==",
  		Address:       "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
  		LockingScript: "76a914" + "00112233445566778899aabbccddeeff00112233" + "88ac",
  	}
  	second := store.Deposit{
  		Suffix: "c3VmZml4LXR3bw==", Prefix: "cHJlZml4LXR3bw==",
  		Address:       "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
  		LockingScript: "76a914" + "ffeeddccbbaa99887766554433221100ffeeddcc" + "88ac",
  	}

  	if err := ds.NewDeposit(ctx, first); err != nil {
  		t.Fatalf("NewDeposit first: %v", err)
  	}
  	if err := ds.NewDeposit(ctx, second); err != nil {
  		t.Fatalf("NewDeposit second: %v", err)
  	}

  	pending, err := ds.PendingDeposits(ctx)
  	if err != nil {
  		t.Fatalf("PendingDeposits: %v", err)
  	}
  	if len(pending) != 2 {
  		t.Fatalf("pending = %d, want 2", len(pending))
  	}
  	for _, d := range pending {
  		if d.InternalizedAt != nil {
  			t.Errorf("%s is already internalized", d.Suffix)
  		}
  		if d.TxID != nil || d.Vout != nil || d.Satoshis != nil {
  			t.Errorf("%s has premature outpoint data: %+v", d.Suffix, d)
  		}
  		if d.CreatedAt.IsZero() {
  			t.Errorf("%s has a zero CreatedAt", d.Suffix)
  		}
  		if d.Prefix == "" || d.Address == "" || d.LockingScript == "" {
  			t.Errorf("%s lost a column: %+v", d.Suffix, d)
  		}
  	}

  	if markErr := ds.MarkInternalized(ctx, first.Suffix,
  		"7f2c4e1d8a9b0c3e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7081920", 1, 250000); markErr != nil {
  		t.Fatalf("MarkInternalized: %v", markErr)
  	}

  	pending, err = ds.PendingDeposits(ctx)
  	if err != nil {
  		t.Fatalf("PendingDeposits after internalize: %v", err)
  	}
  	if len(pending) != 1 || pending[0].Suffix != second.Suffix {
  		got := make([]string, 0, len(pending))
  		for _, d := range pending {
  			got = append(got, d.Suffix)
  		}
  		t.Fatalf("pending = %v, want only %q", got, second.Suffix)
  	}

  	var txid string
  	var vout int32
  	var sats int64
  	err = pool.QueryRow(ctx,
  		"SELECT txid, vout, satoshis FROM deposits WHERE suffix = $1", first.Suffix).
  		Scan(&txid, &vout, &sats)
  	if err != nil {
  		t.Fatalf("reading the internalized deposit: %v", err)
  	}
  	if txid != "7f2c4e1d8a9b0c3e5f6a7b8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7081920" || vout != 1 || sats != 250000 {
  		t.Fatalf("outpoint = (%q, %d, %d)", txid, vout, sats)
  	}
  }

  func TestDuplicateDepositSuffixIsErrConflict(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ds := postgres.NewDepositStore(pool)

  	d := store.Deposit{Suffix: "dup", Prefix: "p", Address: "a", LockingScript: "76a9"}
  	if err := ds.NewDeposit(ctx, d); err != nil {
  		t.Fatalf("first NewDeposit: %v", err)
  	}
  	err := ds.NewDeposit(ctx, d)
  	if !errors.Is(err, store.ErrConflict) {
  		t.Fatalf("duplicate NewDeposit error = %v, want store.ErrConflict", err)
  	}
  }

  func TestMarkInternalizedUnknownSuffixIsErrNotFound(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ds := postgres.NewDepositStore(pool)

  	err := ds.MarkInternalized(ctx, "no-such-suffix", "txid", 0, 1000)
  	if !errors.Is(err, store.ErrNotFound) {
  		t.Fatalf("MarkInternalized error = %v, want store.ErrNotFound", err)
  	}
  }

  func TestPendingDepositsOnAnEmptyTable(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ds := postgres.NewDepositStore(pool)

  	pending, err := ds.PendingDeposits(ctx)
  	if err != nil {
  		t.Fatalf("PendingDeposits: %v", err)
  	}
  	if len(pending) != 0 {
  		t.Fatalf("pending = %d, want 0", len(pending))
  	}
  }

  func TestPreflightRoundTrip(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	ps := postgres.NewPreflightStore(pool)

  	const fp = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

  	ok, err := ps.PreflightOK(ctx, fp)
  	if err != nil {
  		t.Fatalf("PreflightOK before: %v", err)
  	}
  	if ok {
  		t.Fatal("PreflightOK = true before any record")
  	}

  	if recordErr := ps.RecordPreflight(ctx, fp); recordErr != nil {
  		t.Fatalf("RecordPreflight: %v", recordErr)
  	}
  	ok, err = ps.PreflightOK(ctx, fp)
  	if err != nil {
  		t.Fatalf("PreflightOK after: %v", err)
  	}
  	if !ok {
  		t.Fatal("PreflightOK = false after RecordPreflight")
  	}

  	// Recording the same fingerprint twice must be idempotent, since it runs on
  	// every boot.
  	if secondErr := ps.RecordPreflight(ctx, fp); secondErr != nil {
  		t.Fatalf("second RecordPreflight: %v", secondErr)
  	}
  	var rows int
  	if countErr := pool.QueryRow(ctx, "SELECT count(*) FROM app_preflight").Scan(&rows); countErr != nil {
  		t.Fatalf("counting: %v", countErr)
  	}
  	if rows != 1 {
  		t.Fatalf("app_preflight has %d rows, want 1", rows)
  	}

  	// An unrelated fingerprint is still absent.
  	ok, err = ps.PreflightOK(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
  	if err != nil {
  		t.Fatalf("PreflightOK unrelated: %v", err)
  	}
  	if ok {
  		t.Fatal("PreflightOK = true for an unrelated fingerprint")
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `undefined: postgres.DepositStore` and `undefined: postgres.PreflightStore`.

- [ ] **Write the implementation.** Create `internal/store/postgres/deposits.go` with exactly this content:
  ```go
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
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run 'TestDeposit|TestDuplicateDeposit|TestMarkInternalized|TestPendingDeposits|TestPreflight' -v
  ```
  Expected output: `--- PASS` for `TestDepositLifecycle`, `TestDuplicateDepositSuffixIsErrConflict`, `TestMarkInternalizedUnknownSuffixIsErrNotFound`, `TestPendingDepositsOnAnEmptyTable`, `TestPreflightRoundTrip`, then `ok`.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/deposits.go internal/store/postgres/deposits_test.go && git commit -m "postgres: operator deposits and the preflight fingerprint cache"
  ```

---

## Task 17: The store-layer security gate and the interface-satisfaction proof

**Files:**
- Create: `internal/store/postgres/postgres.go`
- Create: `internal/store/postgres/sqldiscipline_test.go`
- Create: `internal/store/postgres/security_test.go`

**Interfaces:**

Consumes (Task 1): `store.ErrNotFound`, `store.ErrConflict`. Consumes (Task 2): `store.Store`, `store.RecordStore`, `store.StationStore`, `store.DepositStore`, `store.PreflightStore`, `store.Pinger`. Consumes (Task 3): `postgres.PoolConfig`, `postgres.NewPool`, `postgres.ErrSimpleProtocol`. Consumes (Task 4): `storetest.RequireDSN`, `storetest.Schema`. Consumes (Task 6): `storetest.Fresh`, `Migrate`. Consumes (Tasks 7-16): every `*SQL` constant, `postgres.NewRecordStore`, `postgres.NewStationStore`, `postgres.NewDepositStore`, `postgres.NewPreflightStore`, `ErrOperation`, `ErrTransient`; plus `storeSchema` from `migrations_test.go` and `fullWeatherData` from `records_insert_test.go`.

Produces:
```go
package postgres
func AllQueries() map[string]string
func New(pool *pgxpool.Pool) store.Store
```

### Steps

- [ ] **Write the failing tests.** Create `internal/store/postgres/sqldiscipline_test.go` with exactly this content. Note that this file is an INTERNAL test (`package postgres`, not `postgres_test`) because it inspects the package's own source and does not touch a database, and because it must not import `storetest` — `storetest` imports this package, so an in-package test importing it would be a cycle.
  ```go
  package postgres

  import (
  	"embed"
  	"regexp"
  	"strings"
  	"testing"
  )

  // sources is this package's own Go source, embedded at COMPILE time.
  //
  // Embedding rather than reading from disk is what keeps this test free of a
  // non-constant file path, which gosec G304 would flag — and the repository
  // allows zero //nolint directives.
  //
  //go:embed *.go
  var sources embed.FS

  // packageSources returns every non-test .go file in this package, keyed by
  // name. Test files are excluded: a test may legitimately hold an inline SQL
  // literal as a fixture.
  func packageSources(t *testing.T) map[string]string {
  	t.Helper()
  	entries, err := sources.ReadDir(".")
  	if err != nil {
  		t.Fatalf("reading embedded sources: %v", err)
  	}
  	out := make(map[string]string, len(entries))
  	for _, e := range entries {
  		name := e.Name()
  		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
  			continue
  		}
  		body, readErr := sources.ReadFile(name)
  		if readErr != nil {
  			t.Fatalf("reading %s: %v", name, readErr)
  		}
  		out[name] = string(body)
  	}
  	if len(out) < 6 {
  		t.Fatalf("embedded only %d non-test sources (%v); the //go:embed pattern is wrong",
  			len(out), keysOf(out))
  	}
  	return out
  }

  // keysOf is generic over the value type so it serves both the source map
  // (map[string]string) and the allowlist (map[string]bool).
  func keysOf[V any](m map[string]V) []string {
  	out := make([]string, 0, len(m))
  	for k := range m {
  		out = append(out, k)
  	}
  	return out
  }

  // stripLineComments removes // comments from body.
  //
  // Without this, every check below would false-positive on its own
  // documentation: records.go's doc comment explains why star selects are
  // forbidden, and the phrase it uses to explain that is the phrase being
  // searched for. Stripping comments is the safe direction to be wrong in —
  // a missed comment produces a spurious FAILURE, never a spurious pass.
  //
  // Limitation, stated so nobody is surprised: a // inside a string literal
  // would also truncate the line. No non-test source in this package contains
  // one (the connection URL is assembled by net/url, not written literally), and
  // if that ever changes the symptom is a false failure, not a false pass.
  func stripLineComments(body string) string {
  	lines := strings.Split(body, "\n")
  	out := make([]string, 0, len(lines))
  	for _, line := range lines {
  		if i := strings.Index(line, "//"); i >= 0 {
  			line = line[:i]
  		}
  		out = append(out, line)
  	}
  	return strings.Join(out, "\n")
  }

  // stripSQLLineComments is stripLineComments for SQL: it drops everything from a
  // `--` to the end of the line.
  //
  // It exists because the migration guard below FAILED on the migration's own
  // documentation. migrations.sql's header says `-- Deliberately absent: CREATE
  // EXTENSION pgcrypto …, any uuidv7() default …`, which contains both forbidden
  // tokens LITERALLY, so a scan of the raw script reports two errors on a
  // perfectly correct migration. Measured before this helper existed: `the
  // migration references uuidv7(), which is a PostgreSQL 18 builtin` and `the
  // migration creates an extension`.
  //
  // Rewording the comments was the alternative and is the worse fix: they are the
  // load-bearing explanation of why those two features are absent, and the next
  // person to wonder "why no pgcrypto?" needs them. Same limitation as
  // stripLineComments, in the same safe direction: a `--` inside a string literal
  // would truncate the line, producing a false FAILURE and never a false pass. No
  // statement in migrations.sql contains one.
  func stripSQLLineComments(body string) string {
  	lines := strings.Split(body, "\n")
  	out := make([]string, 0, len(lines))
  	for _, line := range lines {
  		if i := strings.Index(line, "--"); i >= 0 {
  			line = line[:i]
  		}
  		out = append(out, line)
  	}
  	return strings.Join(out, "\n")
  }

  // queryCallRe finds every query call written in this package's standard shape.
  // The alternation is ordered longest-first so QueryRow cannot be partially
  // matched as Query.
  var queryCallRe = regexp.MustCompile(`\.(?:QueryRow|Query|Exec)\(ctx,\s*([^\s,)]+)`)

  // anyQueryCallRe finds every query call however it is written, so the count can
  // be compared against queryCallRe's and a call with a differently named context
  // variable cannot slip past unexamined.
  var anyQueryCallRe = regexp.MustCompile(`\.(?:QueryRow|Query|Exec|SendBatch|CopyFrom)\(`)

  // statementIdentRe is the only acceptable first argument after ctx: an
  // identifier ending in SQL.
  var statementIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*SQL$`)

  // allowedStatementVars are the two names that hold a statement passed as a
  // parameter of a private method. Both are documented at their declaration as
  // never being derivable from input.
  var allowedStatementVars = map[string]bool{
  	"stmt":      true, // classifierWrite
  	"listStmt":  true, // StationStore.listStations
  	"countStmt": true, // StationStore.listStations
  }

  // TestEveryQueryTakesAConstantStatement is the mechanical guard that replaces
  // the linter.
  //
  // MEASURED: gosec's G201 and G202 recognize database/sql sinks ONLY. Running
  // golangci-lint v2.12.2 with this repository's config, a runtime concatenation
  // into (*sql.DB).ExecContext fires G202 while the identical concatenation into
  // (*pgxpool.Pool).Exec produces ZERO findings. Since this codebase never uses
  // database/sql, a green lint is no evidence at all that dynamic SQL is absent.
  // This test is the evidence.
  func TestEveryQueryTakesAConstantStatement(t *testing.T) {
  	for name, raw := range packageSources(t) {
  		body := stripLineComments(raw)
  		matched := queryCallRe.FindAllStringSubmatch(body, -1)
  		total := anyQueryCallRe.FindAllString(body, -1)
  		if len(matched) != len(total) {
  			t.Errorf("%s: %d query calls but only %d match the ctx-first shape; "+
  				"a call this test cannot inspect is not allowed", name, len(total), len(matched))
  		}
  		for _, m := range matched {
  			arg := m[1]
  			if statementIdentRe.MatchString(arg) || allowedStatementVars[arg] {
  				continue
  			}
  			t.Errorf("%s: query called with %q; the statement must be an identifier "+
  				"ending in SQL, or one of %v", name, arg, keysOf(allowedStatementVars))
  		}
  	}
  }

  // TestNoSprintfInThePackage closes G201's blind spot directly. Errors are
  // wrapped with fmt.Errorf, which is unaffected; there is no legitimate use of
  // Sprintf in this package at all.
  func TestNoSprintfInThePackage(t *testing.T) {
  	for name, raw := range packageSources(t) {
  		if strings.Contains(stripLineComments(raw), "Sprintf") {
  			t.Errorf("%s contains Sprintf; SQL must never be formatted, and nothing "+
  				"else here needs it", name)
  		}
  	}
  }

  // TestSimpleProtocolAppearsOnlyWhereItIsRejected proves the ban is a runtime
  // assertion and not merely a convention. The value can arrive from the
  // connection string, outside the binary.
  func TestSimpleProtocolAppearsOnlyWhereItIsRejected(t *testing.T) {
  	occurrences := 0
  	for name, raw := range packageSources(t) {
  		n := strings.Count(stripLineComments(raw), "QueryExecModeSimpleProtocol")
  		if n > 0 && name != "pool.go" {
  			t.Errorf("%s mentions QueryExecModeSimpleProtocol; only pool.go may, and only to reject it", name)
  		}
  		occurrences += n
  	}
  	if occurrences != 1 {
  		t.Errorf("QueryExecModeSimpleProtocol appears %d times, want exactly 1 (the rejection in pool.go)", occurrences)
  	}
  }

  // TestNoStarSelects guards the star-select ban. pgx's RowToStructByName treats
  // a column with no matching struct field as a hard runtime error, and
  // RowToStructByNameLax was verified to behave identically, so a star select
  // silently couples every query to the table's full column list forever and the
  // next migration breaks all of them with no compile-time signal.
  //
  // Note that count(*) and FILTER (WHERE ...) are unaffected: the patterns require
  // the asterisk to follow the keyword directly.
  func TestNoStarSelects(t *testing.T) {
  	for name, raw := range packageSources(t) {
  		lowered := strings.ToLower(stripLineComments(raw))
  		for _, bad := range []string{"select *", "returning *", "select  *", "select\n *"} {
  			if strings.Contains(lowered, bad) {
  				t.Errorf("%s contains %q; explicit column lists only", name, bad)
  			}
  		}
  	}
  }

  // constSQLRe finds every SQL statement constant declared in this package.
  var constSQLRe = regexp.MustCompile(`(?m)^const ([A-Za-z][A-Za-z0-9]*SQL) `)

  // TestAllQueriesIsComplete makes it impossible to add a statement without also
  // subjecting it to the PREPARE gate in security_test.go. A new statement that
  // is never prepared is a statement whose syntax and static-ness nothing checks.
  func TestAllQueriesIsComplete(t *testing.T) {
  	declared := make(map[string]bool)
  	for _, raw := range packageSources(t) {
  		for _, m := range constSQLRe.FindAllStringSubmatch(stripLineComments(raw), -1) {
  			declared[m[1]] = true
  		}
  	}
  	if len(declared) < 20 {
  		t.Fatalf("found only %d SQL constants; the scan is not working", len(declared))
  	}

  	registered := AllQueries()
  	for name := range declared {
  		key := strings.TrimSuffix(name, "SQL")
  		if _, ok := registered[key]; !ok {
  			t.Errorf("const %s is not registered in AllQueries() under the key %q", name, key)
  		}
  	}
  	for key := range registered {
  		if !declared[key+"SQL"] {
  			t.Errorf("AllQueries() has key %q but no const %sSQL is declared", key, key)
  		}
  	}
  }

  // TestMigrationsScriptAvoidsUnavailableFeatures pins two hard failures on the
  // pinned PostgreSQL 17 image.
  func TestMigrationsScriptAvoidsUnavailableFeatures(t *testing.T) {
  	if migrationsSQL == "" {
  		t.Fatal("migrationsSQL is empty; the //go:embed directive did not fire")
  	}
  	// STATEMENTS only. Scanning the raw script matches the migration's own header
  	// comment, which names both forbidden features in order to explain why they
  	// are absent — a guard that fails on its subject's documentation is worse than
  	// no guard, because the only cheap way to make it pass is to delete the
  	// documentation.
  	body := stripSQLLineComments(migrationsSQL)
  	lowered := strings.ToLower(body)
  	if strings.Contains(lowered, "uuidv7()") {
  		t.Error("the migration references uuidv7(), which is a PostgreSQL 18 builtin; " +
  			"record ids must be generated in Go with uuid.NewV7()")
  	}
  	if strings.Contains(lowered, "create extension") {
  		t.Error("the migration creates an extension; gen_random_uuid() is core in PG 13+ " +
  			"and CREATE EXTENSION can fail outright on a locked-down managed server")
  	}
  	// The two-argument to_tsvector is IMMUTABLE and therefore legal in a
  	// generated column; the one-argument form is only STABLE and would be
  	// rejected at CREATE TABLE. Checked against the stripped body so that
  	// mentioning it in a comment cannot satisfy the requirement.
  	if !strings.Contains(body, "to_tsvector('english'") {
  		t.Error("the generated search column must use the two-argument to_tsvector('english', ...)")
  	}
  	for _, required := range []string{
  		"ck_records_processing_leased",
  		"ck_records_completed_published",
  		"ux_records_station_obs",
  		"ix_records_status_created",
  		"ix_records_station_created",
  		"ix_records_list",
  		"ix_records_txid",
  		"ix_records_reconcile",
  		"ix_stations_search",
  	} {
  		if !strings.Contains(body, required) {
  			t.Errorf("the migration is missing %s", required)
  		}
  	}
  }
  ```

- [ ] **Also write the database-backed security test.** Create `internal/store/postgres/security_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"context"
  	"errors"
  	"strings"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5/pgconn"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  // Compile-time conformance. This is the point at which the package is proven to
  // implement the whole seam: if any signature drifted while the methods were
  // being added one task at a time, the build breaks here.
  var (
  	_ store.RecordStore    = (*postgres.RecordStore)(nil)
  	_ store.StationStore   = (*postgres.StationStore)(nil)
  	_ store.DepositStore   = (*postgres.DepositStore)(nil)
  	_ store.PreflightStore = (*postgres.PreflightStore)(nil)
  )

  func TestNewWiresEveryMember(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	agg := postgres.New(pool)
  	if agg.Records == nil {
  		t.Error("Store.Records is nil")
  	}
  	if agg.Stations == nil {
  		t.Error("Store.Stations is nil")
  	}
  	if agg.Deposits == nil {
  		t.Error("Store.Deposits is nil")
  	}
  	if agg.Preflight == nil {
  		t.Error("Store.Preflight is nil")
  	}
  	if agg.Health == nil {
  		t.Error("Store.Health is nil")
  	}
  	if err := agg.Health.Ping(context.Background()); err != nil {
  		t.Fatalf("Store.Health.Ping: %v", err)
  	}
  }

  // TestEveryStatementPreparesAgainstARealServer is the mechanical proof that
  // every statement this package runs is static, parameterized SQL.
  //
  // PREPARE only succeeds for fixed statement text with numbered placeholders, so
  // a statement assembled at runtime, or one with a syntax error, or one whose
  // placeholder numbering is not contiguous, cannot pass. This test is what
  // replaces the linter: gosec's G201/G202 were MEASURED not to fire on pgx sinks
  // at all.
  func TestEveryStatementPreparesAgainstARealServer(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()

  	queries := postgres.AllQueries()
  	if len(queries) < 20 {
  		t.Fatalf("AllQueries() returned %d statements; that cannot be the whole package", len(queries))
  	}

  	conn, err := pool.Acquire(ctx)
  	if err != nil {
  		t.Fatalf("Acquire: %v", err)
  	}
  	defer conn.Release()

  	for name, stmt := range queries {
  		if _, prepErr := conn.Conn().Prepare(ctx, "prep_"+name, stmt); prepErr != nil {
  			t.Errorf("statement %q does not prepare: %v\n---\n%s\n---", name, prepErr, stmt)
  		}
  	}
  }

  // TestNoDriverErrorEscapesTheStore provokes real Postgres errors through the
  // public API and asserts that nothing they return carries anything but a
  // five-character SQLSTATE.
  //
  // A *pgconn.PgError's Error() is "severity: message (SQLSTATE code)", and the
  // struct also carries Detail, Hint, ConstraintName, ColumnName and TableName —
  // any of which can disclose schema and sometimes column VALUES. The TypeScript
  // this replaces returned {error: 'Internal server error', message: err.message}
  // from its global handler, echoing exactly that.
  func TestNoDriverErrorEscapesTheStore(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	ds := postgres.NewDepositStore(pool)
  	ss := postgres.NewStationStore(pool)

  	// The last four are the CONNECTION-string tokens, and they belong here
  	// because of the branch this test used not to exercise: pgx's connect failure
  	// reads `failed to connect to \`host=… user=… database=…\`: dial tcp …`, and
  	// it satisfies errors.Is(err, context.DeadlineExceeded) — so a classify that
  	// returned the cancellation error unchanged would hand the host, the user and
  	// the database name to its caller.
  	forbidden := []string{
  		"SELECT", "INSERT", "UPDATE", "DELETE", "select", "insert", "update",
  		"weather_records", "app_stats", "stations", "deposits", "app_preflight",
  		"ux_records_station_obs", "ck_records", "deposits_pkey", "weather_records_pkey",
  		"duplicate key", "Key (", "DETAIL", "HINT", "constraint", "column",
  		"host=", "user=", "database=", "dial",
  	}

  	assertOpaque := func(label string, err error) {
  		t.Helper()
  		if err == nil {
  			t.Fatalf("%s: expected an error", label)
  		}
  		var pgErr *pgconn.PgError
  		if errors.As(err, &pgErr) {
  			t.Fatalf("%s: a *pgconn.PgError escaped the store: %v", label, err)
  		}
  		msg := err.Error()
  		for _, bad := range forbidden {
  			if strings.Contains(msg, bad) {
  				t.Errorf("%s: error %q contains the forbidden token %q", label, msg, bad)
  			}
  		}
  	}

  	// 23505 through Insert: same id, different dedupe key.
  	obs := fullWeatherData()
  	if _, err := rs.Insert(ctx, store.NewRecord{
  		ID: "dup", StationID: 1, Data: obs,
  	}); err != nil {
  		t.Fatalf("seeding: %v", err)
  	}
  	_, insertErr := rs.Insert(ctx, store.NewRecord{ID: "dup", StationID: 2, Data: obs})
  	assertOpaque("Insert conflict", insertErr)
  	if !errors.Is(insertErr, store.ErrConflict) {
  		t.Errorf("Insert conflict is not store.ErrConflict: %v", insertErr)
  	}

  	// 23505 through NewDeposit.
  	d := store.Deposit{Suffix: "s", Prefix: "p", Address: "a", LockingScript: "76a9"}
  	if err := ds.NewDeposit(ctx, d); err != nil {
  		t.Fatalf("seeding deposit: %v", err)
  	}
  	assertOpaque("NewDeposit conflict", ds.NewDeposit(ctx, d))

  	// ErrNoRows through Get, on both stores.
  	_, getErr := rs.Get(ctx, "no-such-record")
  	assertOpaque("record Get miss", getErr)
  	if !errors.Is(getErr, store.ErrNotFound) {
  		t.Errorf("record Get miss is not store.ErrNotFound: %v", getErr)
  	}
  	_, stationErr := ss.Get(ctx, 987654321)
  	assertOpaque("station Get miss", stationErr)
  	if !errors.Is(stationErr, store.ErrNotFound) {
  		t.Errorf("station Get miss is not store.ErrNotFound: %v", stationErr)
  	}

  	// The CANCELLATION branch, which is the classifier's weakest and was the one
  	// this test did not cover. It must stay classifiable — a client disconnect is
  	// not a database fault, and the API layer distinguishes them — while carrying
  	// none of the driver's text.
  	canceledCtx, cancel := context.WithCancel(ctx)
  	cancel()
  	_, _, listErr := rs.List(canceledCtx, store.ListFilter{Limit: 1})
  	assertOpaque("List on a canceled context", listErr)
  	if !errors.Is(listErr, context.Canceled) {
  		t.Errorf("List on a canceled context = %v, want errors.Is(…, context.Canceled)", listErr)
  	}

  	// A deadline that has already passed takes the same branch through a
  	// different sentinel.
  	expiredCtx, cancelExpired := context.WithDeadline(ctx, time.Now().Add(-time.Second))
  	defer cancelExpired()
  	_, deadlineErr := rs.Get(expiredCtx, "anything")
  	assertOpaque("Get past its deadline", deadlineErr)
  	if !errors.Is(deadlineErr, context.DeadlineExceeded) {
  		t.Errorf("Get past its deadline = %v, want errors.Is(…, context.DeadlineExceeded)", deadlineErr)
  	}
  }

  // TestUnauthenticatedVerifyPathCannotTouchNonCompletedRows is the §6.0
  // parity assertion for the one WRITE an unauthenticated caller can trigger.
  //
  // POST /api/verify lets the caller choose WHICH txids are refreshed. Both halves
  // of SetBlockHeights therefore filter on status='completed': the record UPDATE
  // and — the half that was missing — setStationHeightSQL's subquery. Without the
  // second filter, knowing the txid of an ABORTED transaction was enough to raise
  // a station's displayed last_block_height, which on a proof-of-existence demo is
  // data forgery through a public endpoint.
  func TestUnauthenticatedVerifyPathCannotTouchNonCompletedRows(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	ss := postgres.NewStationStore(pool)

  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, is_active) VALUES (77, true)"); err != nil {
  		t.Fatalf("seeding station: %v", err)
  	}
  	// Both rows carry the SAME txid and belong to the SAME station, which is the
  	// shape that makes the station half of the write observable. observation_time
  	// is offset per row because (station_id, observation_time) is unique.
  	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
  	for i, seed := range []struct {
  		id     string
  		status string
  		chain  string
  	}{
  		{"was-aborted", "failed", "aborted"},
  		{"still-pending", "pending", "unmined"},
  	} {
  		if _, execErr := pool.Exec(ctx, `
  			INSERT INTO weather_records
  			  (id, station_id, timestamp, observation_time, data, status, txid, chain_status)
  			VALUES ($1, 77, $2, $2, $3, $4, 'tx-shared', $5)`,
  			seed.id, obs.Add(time.Duration(i)*time.Minute), fullWeatherData(),
  			seed.status, seed.chain); execErr != nil {
  			t.Fatalf("seeding %s: %v", seed.id, execErr)
  		}
  	}

  	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
  		{TxID: "tx-shared", BlockHeight: 900999},
  	}); err != nil {
  		t.Fatalf("SetBlockHeights: %v", err)
  	}

  	for _, id := range []string{"was-aborted", "still-pending"} {
  		rec, err := rs.Get(ctx, id)
  		if err != nil {
  			t.Fatalf("Get %s: %v", id, err)
  		}
  		if rec.BlockHeight != nil {
  			t.Errorf("%s got block height %v; only completed rows may be marked mined", id, rec.BlockHeight)
  		}
  	}
  	st, err := ss.Get(ctx, 77)
  	if err != nil {
  		t.Fatalf("Get station: %v", err)
  	}
  	if st.LastBlockHeight != nil {
  		t.Fatalf("stations.last_block_height = %v from txids on non-completed rows, want nil",
  			st.LastBlockHeight)
  	}
  }

  // TestSimpleProtocolIsRejectedWithARealDSN re-asserts the ban against the DSN
  // the rest of the suite uses, so the assertion is proven on the shape that
  // actually ships rather than only on a synthetic string.
  func TestSimpleProtocolIsRejectedWithARealDSN(t *testing.T) {
  	dsn := storetest.RequireDSN(t)

  	// The real DSN must be acceptable.
  	if _, err := postgres.PoolConfig(dsn); err != nil {
  		t.Fatalf("PoolConfig on the CI DSN: %v", err)
  	}

  	sep := "?"
  	if strings.Contains(dsn, "?") {
  		sep = "&"
  	}
  	poisoned := dsn + sep + "default_query_exec_mode=simple_protocol"
  	if _, err := postgres.PoolConfig(poisoned); !errors.Is(err, postgres.ErrSimpleProtocol) {
  		t.Fatalf("PoolConfig on a poisoned DSN = %v, want postgres.ErrSimpleProtocol", err)
  	}
  	if _, err := postgres.NewPool(context.Background(), poisoned); !errors.Is(err, postgres.ErrSimpleProtocol) {
  		t.Fatalf("NewPool on a poisoned DSN = %v, want postgres.ErrSimpleProtocol", err)
  	}
  }

  // TestAdversarialInputReachesNoSQLText runs hostile strings through every
  // public read path. Under the extended query protocol none of them can alter a
  // statement, and this asserts the observable consequence: an answer, never an
  // error and never a different query.
  func TestAdversarialInputReachesNoSQLText(t *testing.T) {
  	pool := storetest.Fresh(t, storeSchema)
  	ctx := context.Background()
  	rs := postgres.NewRecordStore(pool)
  	ss := postgres.NewStationStore(pool)

  	if _, err := pool.Exec(ctx,
  		"INSERT INTO stations (station_id, name, location, is_active) VALUES (1000, 'A', 'Bristol', true)"); err != nil {
  		t.Fatalf("seeding: %v", err)
  	}

  	// "\x00" is in this table for a different reason from the rest. The others
  	// prove the extended query protocol keeps a value a value; the NUL byte proves
  	// store.ValidText screens the ONE value the protocol cannot carry at all
  	// (SQLSTATE 22021 at bind time, measured). Before that screen existed, these
  	// three assertions failed on this single payload and the honest reading was not
  	// "weaken the assertion" but "the search box really does 500 on %00".
  	payloads := []string{
  		"'; DROP TABLE weather_records; --",
  		"' OR '1'='1",
  		"1; DELETE FROM stations",
  		"$1", "$$", "%s", "\\'", "\x00", "a\x00b", "''''",
  		strings.Repeat("'", 100),
  		"UNION SELECT NULL,NULL,NULL",
  	}
  	for _, p := range payloads {
  		if _, err := rs.Get(ctx, p); !errors.Is(err, store.ErrNotFound) {
  			t.Errorf("Get(%q) error = %v, want store.ErrNotFound", p, err)
  		}
  		if ok, err := rs.TxIDExists(ctx, p); err != nil || ok {
  			t.Errorf("TxIDExists(%q) = (%v, %v), want (false, nil)", p, ok, err)
  		}
  		if _, _, err := ss.List(ctx, store.StationFilter{Search: p, Limit: 50}); err != nil {
  			t.Errorf("station List(search=%q) error = %v, want nil", p, err)
  		}
  	}

  	// The tables are all still there.
  	var records, stations int
  	if err := pool.QueryRow(ctx, "SELECT count(*) FROM weather_records").Scan(&records); err != nil {
  		t.Fatalf("counting records: %v", err)
  	}
  	if err := pool.QueryRow(ctx, "SELECT count(*) FROM stations").Scan(&stations); err != nil {
  		t.Fatalf("counting stations: %v", err)
  	}
  	if stations != 1 {
  		t.Fatalf("stations = %d after the payloads, want 1", stations)
  	}
  	if records != 0 {
  		t.Fatalf("records = %d, want 0", records)
  	}
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/... -count=1
  ```
  Expected failure text: `undefined: AllQueries` (from the internal test) and `undefined: postgres.New` plus `undefined: postgres.AllQueries` (from the external test).

- [ ] **Write the assembly point.** Create `internal/store/postgres/postgres.go` with exactly this content:
  ```go
  package postgres

  import (
  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  )

  // New assembles the aggregate seam over one pool.
  //
  // Health is the pool itself: *pgxpool.Pool.Ping is exactly store.Pinger, and it
  // is what /api/ready calls. Note the deliberate asymmetry in how a failing
  // Ping is handled upstream — the database being unreachable must remove the pod
  // from the Service (readiness) and must NEVER restart it (liveness), because
  // Postgres is a single-replica Recreate deployment and every node drain or
  // image bump makes it briefly unreachable.
  func New(pool *pgxpool.Pool) store.Store {
  	return store.Store{
  		Records:   NewRecordStore(pool),
  		Stations:  NewStationStore(pool),
  		Deposits:  NewDepositStore(pool),
  		Preflight: NewPreflightStore(pool),
  		Health:    pool,
  	}
  }

  // AllQueries returns every statement this package executes, keyed by the name
  // of its constant with the SQL suffix removed.
  //
  // It is exported for exactly one reason: a test PREPAREs all of them against a
  // real server, which is the mechanical proof that each is static, parameterized
  // SQL with contiguous placeholders and valid syntax. That proof is necessary
  // because gosec's SQL rules were MEASURED not to fire on pgx sinks at all — a
  // runtime concatenation into (*sql.DB).ExecContext raises G202, while the
  // identical concatenation into (*pgxpool.Pool).Exec raises nothing. A green
  // lint is therefore not evidence.
  //
  // sqldiscipline_test.go asserts that this map is COMPLETE: every `const
  // <name>SQL` declared in the package must appear here, so a new statement
  // cannot be added without also being prepared.
  //
  // migrations.sql is deliberately absent. It is a zero-argument multi-statement
  // DDL script, so it cannot be prepared, and it has nothing to interpolate.
  func AllQueries() map[string]string {
  	return map[string]string{
  		// records.go
  		"insertRecord":        insertRecordSQL,
  		"claim":               claimSQL,
  		"completeRecords":     completeRecordsSQL,
  		"bumpAppStats":        bumpAppStatsSQL,
  		"bumpStations":        bumpStationsSQL,
  		"stats":               statsSQL,
  		"failPermanent":       failPermanentSQL,
  		"requeueInfra":        requeueInfraSQL,
  		"markUnknown":         markUnknownSQL,
  		"reapExpired":         reapExpiredSQL,
  		"requeueCount":        requeueCountSQL,
  		"requeue":             requeueSQL,
  		"listRecords":         listRecordsSQL,
  		"countRecords":        countRecordsSQL,
  		"getRecord":           getRecordSQL,
  		"txIDExists":          txIDExistsSQL,
  		"snapshot":            snapshotSQL,
  		"setBlockHeight":      setBlockHeightSQL,
  		"setStationHeight":    setStationHeightSQL,
  		"reconcileCandidates": reconcileCandidatesSQL,

  		// stations.go
  		"upsertStation":       upsertStationSQL,
  		"getStation":          getStationSQL,
  		"listStationsAll":     listStationsAllSQL,
  		"countStationsAll":    countStationsAllSQL,
  		"listStationsByID":    listStationsByIDSQL,
  		"countStationsByID":   countStationsByIDSQL,
  		"listStationsSearch":  listStationsSearchSQL,
  		"countStationsSearch": countStationsSearchSQL,

  		// deposits.go
  		"newDeposit":       newDepositSQL,
  		"pendingDeposits":  pendingDepositsSQL,
  		"markInternalized": markInternalizedSQL,
  		"preflightOK":      preflightOKSQL,
  		"recordPreflight":  recordPreflightSQL,
  	}
  }
  ```

- [ ] **Run it and see it pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -race -run 'TestEvery|TestNo|TestSimpleProtocol|TestAllQueries|TestMigrationsScript|TestNew|TestAdversarial' -v
  ```
  Expected output: `--- PASS` for `TestEveryQueryTakesAConstantStatement`, `TestNoSprintfInThePackage`, `TestSimpleProtocolAppearsOnlyWhereItIsRejected`, `TestNoStarSelects`, `TestAllQueriesIsComplete`, `TestMigrationsScriptAvoidsUnavailableFeatures`, `TestNewWiresEveryMember`, `TestEveryStatementPreparesAgainstARealServer`, `TestNoDriverErrorEscapesTheStore`, `TestUnauthenticatedVerifyPathCannotTouchNonCompletedRows`, `TestSimpleProtocolIsRejectedWithARealDSN`, `TestAdversarialInputReachesNoSQLText`, then `ok`.

- [ ] **Prove the discipline test actually discriminates.** Temporarily add a rogue statement to `internal/store/postgres/records.go`:
  ```go
  // TEMPORARY: proving the guard has teeth. Delete after observing the failure.
  func (s *RecordStore) rogue(ctx context.Context, order string) error {
  	_, err := s.db.Exec(ctx, "SELECT id FROM weather_records ORDER BY "+order)
  	return err
  }
  ```
  then run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && golangci-lint run --max-same-issues=0 ./internal/store/... ; WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -run TestEveryQueryTakesAConstantStatement -v
  ```
  Expected: golangci-lint reports the function as unused but NOT as a SQL problem — that is the measured gosec gap, and seeing it matters. Then the test FAILS with `records.go: query called with "\"SELECT id FROM weather_records ORDER BY \"+order"; the statement must be an identifier ending in SQL`. **Delete the `rogue` method before proceeding**, and re-run the test to see it pass again.

- [ ] **Prove the migration guard discriminates, and that comment-stripping did not defang it.** The guard scans the migration with SQL comments removed, so the obvious worry is that it now sees nothing. Put the forbidden token in a real STATEMENT: temporarily change `migrations.sql`'s `app_preflight` table to
  ```sql
    fingerprint text        PRIMARY KEY DEFAULT uuidv7()::text,
  ```
  then run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -run 'TestMigrationsScriptAvoidsUnavailableFeatures|TestMigrateIsIdempotent' -v
  ```
  Expected: `--- FAIL: TestMigrationsScriptAvoidsUnavailableFeatures` with `the migration references uuidv7(), which is a PostgreSQL 18 builtin`, AND `--- FAIL: TestMigrateIsIdempotent` with SQLSTATE 42883 — which is the point of the guard: on the pinned 17-alpine image that migration does not merely lint badly, it cannot be applied at all. **Revert `migrations.sql` before proceeding** and re-run both to see them pass.

- [ ] **Prove the completeness test discriminates.** Temporarily add to `internal/store/postgres/records.go`:
  ```go
  // TEMPORARY: proving the completeness guard has teeth. Delete after observing.
  const unregisteredSQL = `SELECT 1`
  ```
  then run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -run TestAllQueriesIsComplete -v
  ```
  Expected failure text: `const unregisteredSQL is not registered in AllQueries() under the key "unregistered"`. **Delete the constant before proceeding**, and re-run to see it pass.

- [ ] **Run the entire suite the way CI will.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test -race -count=1 -timeout 10m ./internal/store/... && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test -race -count=5 -timeout 10m -run 'TestClaimPendingNeverDoubleClaims' ./internal/store/postgres/
  ```
  Expected output: `ok` for `internal/store`, `internal/store/fake`, `internal/store/postgres` and `internal/store/storetest`, then a second `ok` for the repeated claim race. No SKIPs anywhere.

- [ ] **Run the `check` job's steps in full.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go mod tidy && git diff --exit-code go.mod go.sum && go vet ./... && go build ./... && go test ./... -count=1 && go list -f '{{if and (eq (len .TestGoFiles) 0) (eq (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}' ./... && golangci-lint run --max-same-issues=0 && go test ./internal/weather -run TestGolden -update -count=1 && git diff --exit-code internal/weather/testdata/golden/
  ```
  Expected: no output from either `git diff`, no output from `go list`, `0 issues` from the linter, and `ok` lines throughout. The final `git diff` proves Plan A's golden gate is untouched.

- [ ] **Confirm no test depends on ordering.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/postgres/ -count=1 -shuffle=on -race -v 2>&1 | tail -20
  ```
  Expected output: a `-test.shuffle <seed>` line and `ok`. Run it three times; every seed must pass. Any failure here means a test is reading state a sibling left behind, and `storetest.Fresh` is not being called at the top of it.

- [ ] **Confirm the tripwire still fires now the suite is large.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 2>&1 | tail -20
  ```
  Expected output: `FAIL` for `internal/store/postgres` and `internal/store/storetest` with `WEATHER_TEST_POSTGRES_DSN is unset while WEATHER_TEST_REQUIRE_POSTGRES is set: the Postgres suite must never skip in CI`. `internal/store` and `internal/store/fake` still pass, because they need no database.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/postgres/postgres.go internal/store/postgres/sqldiscipline_test.go internal/store/postgres/security_test.go && git commit -m "postgres: SQL-discipline gate, PREPARE-every-statement proof, and interface conformance"
  ```

- [ ] **Stop the local Postgres.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && make pg-down && docker rm -f wp-pg
  ```
  Expected output: the compose service stops, and `wp-pg` is removed if the standalone container from Task 4 is still running (an error saying "No such container" is fine).

---

## Task 18: CI hardening — govulncheck and pinned action SHAs

**Files:**
- Modify: `.github/workflows/go.yml`

**Interfaces:**

Consumes (Task 5): the `check` job with its `No package is silently untested` step, and the `integration` job. Produces: no Go identifiers — this task's deliverable is a workflow that pins its actions by digest and fails on a known dependency vulnerability.

This is a separate task from Task 5 on purpose. A reviewer can reasonably approve the Postgres service job while rejecting an added scanner, or the reverse, and `govulncheck` can go red on a dependency this plan did not choose. Keeping them apart means neither blocks the other.

**Why gitleaks is NOT added here, stated rather than silently omitted.** The design's security review calls for a secret scanner, and it is right to. But the repository currently has the literal `SERVER_PRIVATE_KEY` hex committed in five tracked files (`src/config/env.ts`, `src/service/wallet.ts`, `.env.example`, `docker-compose.yaml`, `QUICKSTART.md`), and `Dockerfile:45` copies `.env.example` into the published image. Adding gitleaks would turn CI red immediately, and the only two ways to make it green are to allowlist those five paths — which is exactly the laundering that hides the problem — or to purge the literal and ROTATE the key, which edits TypeScript (out of scope for this plan) and requires an operator action outside the repository. So gitleaks belongs to the change that rotates the key. Record that as a follow-up rather than pretending the gap is closed.

### Steps

- [ ] **Check the current dependency set for known vulnerabilities BEFORE wiring the gate**, so a red build is diagnosed rather than discovered. Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go run golang.org/x/vuln/cmd/govulncheck@latest ./...
  ```
  Expected output: `No vulnerabilities found.` If it reports a finding, stop and resolve it (usually `go get <module>@<fixed version>` followed by `go mod tidy`) before adding the CI step — a gate that is red on the day it lands teaches everyone to ignore it.

- [ ] **Add the govulncheck step to the `check` job.** In `.github/workflows/go.yml`, immediately AFTER the `- name: Lint` step and BEFORE the `- name: Golden file is not stale or laundered` step, insert exactly:
  ```yaml
      # CodeQL does not flag dependency CVEs at all, so this step is not
      # redundant with code scanning even once `go` is added to it (the next step
      # of this task does that). The two cover different things: govulncheck
      # scans the dependency graph against the Go vulnerability database, CodeQL
      # scans first-party code. Pinned to @latest deliberately: a vulnerability
      # database is only useful when it is current.
      - name: Vulnerability scan
        run: go run golang.org/x/vuln/cmd/govulncheck@latest ./...
  ```

- [ ] **Verify the workflow still parses and the step landed in the right place.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && python3 -c "
  import yaml
  d = yaml.safe_load(open('.github/workflows/go.yml'))
  names = [s.get('name') for s in d['jobs']['check']['steps']]
  print(names)
  assert names.index('Vulnerability scan') == names.index('Lint') + 1, names
  assert names[-1].startswith('Golden'), names
  print('OK')
  "
  ```
  Expected output: the ordered step-name list, then `OK`.

- [ ] **Resolve the three action tags to 40-character SHAs.** Floating major tags are mutable: `actions/checkout@v4` is whatever the `v4` tag currently points at, so a compromised or simply changed release runs in a job that has repository read access. Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && for spec in actions/checkout:v4 actions/setup-go:v5 golangci/golangci-lint-action:v7; do repo="${spec%%:*}"; tag="${spec##*:}"; sha=$(gh api "repos/$repo/commits/$tag" --jq .sha); ver=$(gh api "repos/$repo/releases/latest" --jq .tag_name); echo "$repo  $sha  # $ver"; done
  ```
  Expected output: three lines, each a `owner/repo`, a 40-character hex SHA, and a `# vX.Y.Z` comment. Record all three.

- [ ] **Pin the actions.** In `.github/workflows/go.yml`, replace each `uses:` line with the resolved digest plus the version as a trailing comment, in BOTH jobs. The `check` job's `Checkout code` and `Set up Go` steps and its `Lint` step, and the `integration` job's `Checkout code` and `Set up Go` steps. The shape is exactly:
  ```yaml
        - name: Checkout code
          uses: actions/checkout@<40-char sha from the previous step>  # v4.2.2
  ```
  ```yaml
        - name: Set up Go
          uses: actions/setup-go@<40-char sha from the previous step>  # v5.5.0
  ```
  ```yaml
        - name: Lint
          uses: golangci/golangci-lint-action@<40-char sha from the previous step>  # v7.0.0
          with:
            version: v2.12.2
  ```
  Substitute the SHAs and the version comments printed by the previous step — do NOT copy the example version numbers above, which are illustrative. Dependabot updates digest pins and rewrites the trailing comment, so this does not become stale work.

- [ ] **Verify every `uses:` is now pinned.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && grep -n 'uses:' .github/workflows/go.yml && echo "--- unpinned (must be empty) ---" && grep -n 'uses:.*@v[0-9]' .github/workflows/go.yml
  ```
  Expected output: five `uses:` lines each with a 40-character SHA and a trailing `# vX.Y.Z`, then the header, then NOTHING under it. Any line printed under the header is still on a floating tag.

- [ ] **Confirm the top-level permissions block is untouched.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && python3 -c "
  import yaml
  d = yaml.safe_load(open('.github/workflows/go.yml'))
  print('top-level:', d.get('permissions'))
  assert d.get('permissions') == {'contents': 'read'}, d.get('permissions')
  print('OK')
  "
  ```
  Expected output: `top-level: {'contents': 'read'}` then `OK`. Declaring any `permissions` block sets every unlisted scope to none, so no `id-token: none` line is needed and none must be added. This block already exists and must not regress.

- [ ] **Add `go` to CodeQL, because right now the backend language is scanned by nothing.** This is the control the repository has TODAY for its TypeScript and would silently lose the moment the backend becomes Go — it is the control whose alerts commit `5cfea93` existed to close. Confirm the gap first:
  ```
  cd "$(git rev-parse --show-toplevel)" && gh api repos/bsv-blockchain-demos/weather-proof/code-scanning/default-setup
  ```
  Expected output (verified live on 2026-07-29, alongside `query_suite`, `threat_model`, `schedule` and the runner fields): `"state":"configured"` with `"languages":["actions","javascript","javascript-typescript","typescript"]` — note the absence of `go`. Then add it, keeping every language already there:
  ```
  cd "$(git rev-parse --show-toplevel)" && gh api --method PATCH repos/bsv-blockchain-demos/weather-proof/code-scanning/default-setup -f 'languages[]=actions' -f 'languages[]=go' -f 'languages[]=javascript-typescript' -f 'languages[]=typescript'
  ```

- [ ] **Verify `go` is actually configured, rather than assuming the PATCH took.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && gh api repos/bsv-blockchain-demos/weather-proof/code-scanning/default-setup --jq '.state, .languages' && gh api repos/bsv-blockchain-demos/weather-proof/code-scanning/default-setup --jq '.languages | index("go") // empty' | grep -q . && echo "go is configured"
  ```
  Expected output: `configured`, the language list including `go`, then `go is configured`.

  **If the PATCH fails with 403** — default setup is an admin-only endpoint and the agent running this plan may not have that right — do NOT quietly move on and do NOT paper over it with a `.github/workflows/codeql.yml` bolted on as a side effect. Either land an advanced setup workflow deliberately (one job, `permissions: {contents: read, security-events: write, actions: read}`, matrix `[go, javascript-typescript, actions]`, actions digest-pinned exactly like this task's others) or record it as item 5 of "Known issues carried forward" — which this plan already does — with the exact command above and a named owner. What is not acceptable is the state this plan started in: the fact mentioned only inside a YAML comment, no action, and no entry in the list that claims to enumerate what B1 could not close.

- [ ] **Run the full local gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && go test ./... -count=1 && golangci-lint run --max-same-issues=0 && go run golang.org/x/vuln/cmd/govulncheck@latest ./...
  ```
  All must pass, ending with `No vulnerabilities found.`

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add .github/workflows/go.yml && git commit -m "ci: govulncheck gate, digest-pinned actions, CodeQL go language"
  ```

---

## Task 19: One conformance suite, run against both implementations

**Files:**
- Create: `internal/store/storetest/conformance.go`
- Create: `internal/store/fake/conformance_test.go`
- Create: `internal/store/postgres/conformance_test.go`

**Why this task exists.** B2's ENTIRE HTTP test suite runs against `internal/store/fake`. That is the right design — it is what keeps twenty-odd handler tasks off a service container — but it has a failure mode with no natural detector: every place the fake and the SQL disagree, a B2 test passes while proving nothing about production. Four such divergences were found by reading the two implementations side by side, and none of them was covered by any test: the fake ignored `StationFilter.Search` completely while `StationStore.List` has three branches; the fake advanced `last_temp` only for a newer reading while `bumpStationsSQL` assigned it unconditionally; the fake CREATED a missing station row while `bumpStationsSQL` matches nothing; and the fake never reported `ErrConflict` for a duplicate id while Postgres does. Tasks 2, 9 and 13 fix all four. This task is the mechanism that keeps them fixed, and it is the only one available: a shared table of assertions, run twice.

**What it deliberately does NOT assert.** Ranking (`ts_rank` has no in-memory equivalent worth faking), ordering of `UPDATE … RETURNING` rows (undefined in PostgreSQL), and anything concurrent — the fake serializes everything under one mutex, so a concurrency assertion here would be the exact vacuous pass `internal/store/fake`'s package comment warns about. Concurrency stays in Task 8.

**Interfaces:**

Consumes (Task 1): every domain type, `store.ErrNotFound`, `store.ErrConflict`. Consumes (Task 2): `store.Store`, `fake.New`. Consumes (Task 6): `storetest.Fresh`. Consumes (Task 17): `postgres.New(pool) store.Store`; plus `storeSchema` from `migrations_test.go`.

Produces:
```go
package storetest
func RunStoreConformance(t *testing.T, name string, mk func(t *testing.T) store.Store)
```

### Steps

- [ ] **Write the failing callers first**, so the suite is written against two real callers rather than one. Create `internal/store/fake/conformance_test.go` with exactly this content:
  ```go
  package fake_test

  import (
  	"testing"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  // The fake half of the shared suite. No database, so it runs in the ordinary
  // `check` job — which is the point: this is the substrate the whole read-API
  // plan tests against, and it is only trustworthy while this file and its
  // Postgres twin agree.
  func TestFakeConformsToTheStoreContract(t *testing.T) {
  	storetest.RunStoreConformance(t, "fake", func(_ *testing.T) store.Store {
  		return fake.New().Store()
  	})
  }
  ```
  and `internal/store/postgres/conformance_test.go` with exactly this content:
  ```go
  package postgres_test

  import (
  	"testing"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
  )

  // The Postgres half. Fresh drops, recreates and re-migrates the package schema,
  // so each subtest that asks for a store gets an empty database — and no subtest
  // may call t.Parallel(), which RunStoreConformance does not.
  func TestPostgresConformsToTheStoreContract(t *testing.T) {
  	storetest.RunStoreConformance(t, "postgres", func(sub *testing.T) store.Store {
  		return postgres.New(storetest.Fresh(sub, storeSchema))
  	})
  }
  ```

- [ ] **Run it and see it fail.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/... -count=1
  ```
  Expected failure text: `undefined: storetest.RunStoreConformance` in both `internal/store/fake` and `internal/store/postgres`.

- [ ] **Write the suite.** Create `internal/store/storetest/conformance.go` with exactly this content:
  ```go
  package storetest

  import (
  	"context"
  	"errors"
  	"testing"
  	"time"

  	"github.com/google/uuid"

  	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
  	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
  )

  // conformanceBase is the fixed instant every case builds its timestamps from.
  // Fixed rather than time.Now so a failure message is the same on every run.
  var conformanceBase = time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

  // RunStoreConformance asserts the behavior that EVERY store.Store implementation
  // must have, and it is meant to be called twice: once over internal/store/fake
  // with no database, once over internal/store/postgres behind Fresh.
  //
  // WHY THIS IS NOT OPTIONAL. The read-API plan's whole test suite runs against
  // the fake. Any behavior where the fake and the SQL disagree is a place where a
  // handler test passes while proving nothing, and there is no other detector for
  // that class of defect — a reviewer would have to read both implementations side
  // by side and notice. Four such divergences existed before this suite: an
  // ignored search filter, an unconditional last_temp assignment, a station row
  // invented by the fake, and a missing ErrConflict on a duplicate id.
  //
  // WHAT IT DELIBERATELY OMITS. Full-text RANKING (ts_rank has no honest
  // in-memory analog), the order of UPDATE … RETURNING rows (PostgreSQL does not
  // define it), and everything concurrent: the fake runs every method under one
  // mutex, so a concurrency assertion here would pass trivially and report a
  // guarantee that does not exist. That is why the claim race lives only in
  // internal/store/postgres.
  //
  // mk is called once per subtest and must return an EMPTY store. No subtest calls
  // t.Parallel(), because the Postgres implementation of mk shares one schema.
  func RunStoreConformance(t *testing.T, name string, mk func(t *testing.T) store.Store) {
  	t.Helper()
  	t.Run(name+"/InsertDedupeIsNotAnErrorButADuplicateIDIs", func(t *testing.T) {
  		ctx := context.Background()
  		s := mk(t)
  		obs := conformanceBase

  		inserted, err := s.Records.Insert(ctx, newConformanceRecord("rec-a", 1000, obs, 18, "Clear"))
  		if err != nil || !inserted {
  			t.Fatalf("first Insert = (%v, %v), want (true, nil)", inserted, err)
  		}

  		// Same (station_id, observation_time): the intended steady state.
  		inserted, err = s.Records.Insert(ctx, newConformanceRecord("rec-b", 1000, obs, 18, "Clear"))
  		if err != nil {
  			t.Fatalf("duplicate observation Insert error = %v, want nil", err)
  		}
  		if inserted {
  			t.Error("duplicate observation Insert = true, want false")
  		}

  		// Same id, different dedupe key: a genuine surprise.
  		_, conflictErr := s.Records.Insert(ctx,
  			newConformanceRecord("rec-a", 2000, obs.Add(time.Hour), 18, "Clear"))
  		if !errors.Is(conflictErr, store.ErrConflict) {
  			t.Errorf("duplicate id Insert = %v, want store.ErrConflict", conflictErr)
  		}
  	})

  	t.Run(name+"/GetAndTxIDExistsAreTotalOverHostileInput", func(t *testing.T) {
  		ctx := context.Background()
  		s := mk(t)
  		for _, id := range []string{
  			"", "0", "not-a-uuid", "'; DROP TABLE weather_records; --", "\x00", "a\x00b",
  		} {
  			if _, err := s.Records.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
  				t.Errorf("Get(%q) error = %v, want store.ErrNotFound", id, err)
  			}
  			ok, err := s.Records.TxIDExists(ctx, id)
  			if err != nil || ok {
  				t.Errorf("TxIDExists(%q) = (%v, %v), want (false, nil)", id, ok, err)
  			}
  		}
  	})

  	t.Run(name+"/StationSearchSplitsOnOneParsedValue", func(t *testing.T) {
  		ctx := context.Background()
  		s := mk(t)
  		stations := []store.Station{
  			{StationID: 1000, Name: "Harbor Mast", Location: "Bristol Docks", IsActive: true},
  			{StationID: 1001, Name: "Clifton Ridge", Location: "Bristol Downs", IsActive: true},
  			{StationID: 2000, Name: "Kelvin Yard", Location: "Glasgow", IsActive: true},
  		}
  		for _, st := range stations {
  			if err := s.Stations.Upsert(ctx, st); err != nil {
  				t.Fatalf("Upsert %d: %v", st.StationID, err)
  			}
  		}

  		cases := []struct {
  			search string
  			want   int64
  			reason string
  		}{
  			{"", 3, "no filter"},
  			{"1001", 1, "an all-digits search is an EXACT station_id lookup"},
  			{"424242", 0, "a numeric miss is an empty page, never an error"},
  			{"0", 0, "\"0\" parses, so it looks up station 0 and finds nothing"},
  			{"bristol", 2, "a text search matches location, case-insensitively"},
  			{"kelvin", 1, "a text search matches name too"},
  			{"reykjavik", 0, "a text miss is an empty page"},
  			{"\x00nul", 0, "a NUL byte cannot be bound at all: empty page, no error"},
  		}
  		for _, c := range cases {
  			sts, total, err := s.Stations.List(ctx, store.StationFilter{Search: c.search, Limit: 50})
  			if err != nil {
  				t.Errorf("List(search=%q) error = %v, want nil (%s)", c.search, err, c.reason)
  				continue
  			}
  			if total != c.want {
  				t.Errorf("List(search=%q) total = %d, want %d (%s)", c.search, total, c.want, c.reason)
  			}
  			if int64(len(sts)) != c.want {
  				t.Errorf("List(search=%q) rows = %d, want %d (%s)", c.search, len(sts), c.want, c.reason)
  			}
  		}
  	})

  	t.Run(name+"/CompleteNeverInventsAStation", func(t *testing.T) {
  		ctx := context.Background()
  		s := mk(t)
  		if _, err := s.Records.Insert(ctx,
  			newConformanceRecord("orphan", 4242, conformanceBase, 18, "Clear")); err != nil {
  			t.Fatalf("Insert: %v", err)
  		}
  		claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
  		if err != nil {
  			t.Fatalf("ClaimPending: %v", err)
  		}
  		if len(claimed) != 1 {
  			t.Fatalf("claimed %d rows, want 1", len(claimed))
  		}
  		if _, completeErr := s.Records.Complete(ctx, "orphan-tx",
  			[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
  			t.Fatalf("Complete with no stations row: %v", completeErr)
  		}
  		if _, getErr := s.Stations.Get(ctx, 4242); !errors.Is(getErr, store.ErrNotFound) {
  			t.Fatalf("station 4242 = %v, want store.ErrNotFound: the station bump is an "+
  				"UPDATE and must not create a row the poller never reported", getErr)
  		}
  	})

  	t.Run(name+"/CompleteAdvancesStationReadingsOnlyForwards", func(t *testing.T) {
  		ctx := context.Background()
  		s := mk(t)
  		if err := s.Stations.Upsert(ctx, store.Station{StationID: 1000, IsActive: true}); err != nil {
  			t.Fatalf("Upsert: %v", err)
  		}

  		// The NEWER reading is published first, then an OLDER one. last_temp and
  		// last_conditions must still describe the newer reading: assigning them
  		// unconditionally would leave last_reading fresh and the temperature
  		// stale, which no consumer could detect.
  		batches := []struct {
  			id   string
  			ts   time.Time
  			temp int64
  			cond string
  			txid string
  		}{
  			{"newer", conformanceBase.Add(time.Hour), 21, "Clear", "tx-newer"},
  			{"older", conformanceBase, 4, "Snow", "tx-older"},
  		}
  		for _, b := range batches {
  			if _, err := s.Records.Insert(ctx,
  				newConformanceRecord(b.id, 1000, b.ts, b.temp, b.cond)); err != nil {
  				t.Fatalf("Insert %s: %v", b.id, err)
  			}
  			claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
  			if err != nil {
  				t.Fatalf("ClaimPending %s: %v", b.id, err)
  			}
  			if len(claimed) != 1 {
  				t.Fatalf("claimed %d rows for %s, want 1", len(claimed), b.id)
  			}
  			if _, completeErr := s.Records.Complete(ctx, b.txid,
  				[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
  				t.Fatalf("Complete %s: %v", b.id, completeErr)
  			}
  		}

  		st, err := s.Stations.Get(ctx, 1000)
  		if err != nil {
  			t.Fatalf("station Get: %v", err)
  		}
  		if st.TxRecords != 2 {
  			t.Errorf("tx_records = %d, want 2: every completed record counts", st.TxRecords)
  		}
  		if st.LastReading == nil || !st.LastReading.Equal(conformanceBase.Add(time.Hour)) {
  			t.Errorf("last_reading = %v, want the newer %v", st.LastReading, conformanceBase.Add(time.Hour))
  		}
  		if st.LastTemp == nil || *st.LastTemp != 21 {
  			t.Errorf("last_temp = %v, want the newer reading's 21", st.LastTemp)
  		}
  		if st.LastConditions != "Clear" {
  			t.Errorf("last_conditions = %q, want Clear", st.LastConditions)
  		}
  	})

  	t.Run(name+"/StatsAndSnapshotStartEmpty", func(t *testing.T) {
  		ctx := context.Background()
  		s := mk(t)
  		stats, err := s.Stations.Stats(ctx)
  		if err != nil {
  			t.Fatalf("Stats: %v", err)
  		}
  		if stats != (store.Stats{}) {
  			t.Errorf("Stats on an empty store = %+v, want the zero value", stats)
  		}
  		if stats.TotalDataPoints() != 0 {
  			t.Errorf("TotalDataPoints = %d, want 0", stats.TotalDataPoints())
  		}
  		snap, err := s.Records.Snapshot(ctx)
  		if err != nil {
  			t.Fatalf("Snapshot: %v", err)
  		}
  		if snap != (store.Snapshot{}) {
  			t.Errorf("Snapshot on an empty store = %+v, want the zero value", snap)
  		}
  	})

  	t.Run(name+"/DepositAndPreflightLifecycle", func(t *testing.T) {
  		ctx := context.Background()
  		s := mk(t)
  		d := store.Deposit{Suffix: "s1", Prefix: "p1", Address: "addr", LockingScript: "76a9"}
  		if err := s.Deposits.NewDeposit(ctx, d); err != nil {
  			t.Fatalf("NewDeposit: %v", err)
  		}
  		if dupErr := s.Deposits.NewDeposit(ctx, d); !errors.Is(dupErr, store.ErrConflict) {
  			t.Errorf("duplicate NewDeposit = %v, want store.ErrConflict", dupErr)
  		}
  		pending, err := s.Deposits.PendingDeposits(ctx)
  		if err != nil {
  			t.Fatalf("PendingDeposits: %v", err)
  		}
  		if len(pending) != 1 {
  			t.Fatalf("pending = %d, want 1", len(pending))
  		}
  		if missErr := s.Deposits.MarkInternalized(ctx, "nope", "tx", 0, 1); !errors.Is(missErr, store.ErrNotFound) {
  			t.Errorf("MarkInternalized for an unknown suffix = %v, want store.ErrNotFound", missErr)
  		}
  		if markErr := s.Deposits.MarkInternalized(ctx, "s1", "tx", 1, 250000); markErr != nil {
  			t.Fatalf("MarkInternalized: %v", markErr)
  		}
  		pending, err = s.Deposits.PendingDeposits(ctx)
  		if err != nil {
  			t.Fatalf("PendingDeposits after internalize: %v", err)
  		}
  		if len(pending) != 0 {
  			t.Fatalf("pending after internalize = %d, want 0", len(pending))
  		}

  		ok, err := s.Preflight.PreflightOK(ctx, "fp")
  		if err != nil || ok {
  			t.Fatalf("PreflightOK before = (%v, %v), want (false, nil)", ok, err)
  		}
  		if recordErr := s.Preflight.RecordPreflight(ctx, "fp"); recordErr != nil {
  			t.Fatalf("RecordPreflight: %v", recordErr)
  		}
  		// Twice, because it runs on every boot with the same fingerprint.
  		if secondErr := s.Preflight.RecordPreflight(ctx, "fp"); secondErr != nil {
  			t.Fatalf("second RecordPreflight: %v", secondErr)
  		}
  		ok, err = s.Preflight.PreflightOK(ctx, "fp")
  		if err != nil || !ok {
  			t.Fatalf("PreflightOK after = (%v, %v), want (true, nil)", ok, err)
  		}
  	})

  	t.Run(name+"/PingSucceeds", func(t *testing.T) {
  		s := mk(t)
  		if err := s.Health.Ping(context.Background()); err != nil {
  			t.Fatalf("Ping: %v", err)
  		}
  	})
  }

  // newConformanceRecord builds a NewRecord whose data carries the two fields the
  // station counters read, so a case can assert on last_temp and last_conditions.
  //
  // temp is int64 because that is what Plan A froze: weather.WeatherData's
  // AirTemperature is `int64` with a `json:"air_temperature"` tag. It is fed into
  // a `double precision` column, which is why last_temp can never be fractional.
  func newConformanceRecord(id string, stationID int64, ts time.Time, temp int64, conditions string) store.NewRecord {
  	return store.NewRecord{
  		ID:              id,
  		StationID:       stationID,
  		Timestamp:       ts,
  		ObservationTime: ts,
  		Data: weather.WeatherData{
  			AirTemperature: temp,
  			Conditions:     conditions,
  		},
  	}
  }
  ```
  `temp` is `int64` because `internal/weather/types.go:93` declares `AirTemperature int64` — checked, not assumed. If Plan A's schema ever changes that field's type, match it exactly rather than casting at the call site.

- [ ] **Run it and see BOTH halves pass.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -run Conforms -v
  ```
  Expected output: `--- PASS: TestFakeConformsToTheStoreContract` and `--- PASS: TestPostgresConformsToTheStoreContract`, each with the eight named subtests under it, then `ok` for both packages. Every subtest name appears twice in the log — once with the `fake/` prefix and once with `postgres/` — which is the whole point.

- [ ] **Prove the suite actually catches a divergence.** Temporarily make the fake wrong in the way it was wrong before Task 2 fixed it: in `internal/store/fake/fake.go`, change `stationMatches` to `return true` unconditionally. Then run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && go test ./internal/store/fake/ -count=1 -run Conforms -v
  ```
  Expected failure text: `List(search="1001") total = 3, want 1 (an all-digits search is an EXACT station_id lookup)` plus the same for `bristol`, `kelvin` and the NUL case. **Revert `stationMatches` before proceeding** and re-run to see it pass. A conformance suite nobody has seen fail is decoration.

- [ ] **Run the full gate.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && WEATHER_TEST_POSTGRES_DSN='postgres://postgres:postgres@localhost:5432/weatherproof_test?sslmode=disable' WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1 -race && gofmt -w ./internal && test -z "$(gofmt -l ./internal)" && go vet ./... && go build ./... && golangci-lint run --max-same-issues=0
  ```
  All must pass with `0 issues`.

- [ ] **Commit.** Run exactly:
  ```
  cd "$(git rev-parse --show-toplevel)" && git add internal/store/storetest/conformance.go internal/store/fake/conformance_test.go internal/store/postgres/conformance_test.go && git commit -m "storetest: one conformance suite, run against the fake and Postgres"
  ```

---

## Acceptance: what a reviewer should verify before merging

Not a task — the checklist a fresh reviewer works through. Every item has a command that produces its own evidence.

- [ ] **CodeQL scans Go.** `gh api repos/bsv-blockchain-demos/weather-proof/code-scanning/default-setup --jq '.languages'` includes `go`. Before this plan the list was `["actions","javascript","javascript-typescript","typescript"]`, so once the backend is Go, `internal/store/postgres` was scanned by nothing — and static analysis of the backend language is a control the TypeScript has today. If the PATCH could not be made (admin-only endpoint), item 5 of "Known issues carried forward" must name an owner and carry the exact command; a comment inside an unrelated CI step does not count.
- [ ] **The fake and Postgres are held to ONE contract.** `go test ./internal/store/... -run Conforms -v` shows every conformance subtest twice, once under `fake/` and once under `postgres/`. This is the only thing standing between B2's entirely-fake-backed test suite and a whole plan's worth of tests that pass while proving nothing.
- [ ] **The `check` job gained exactly two steps and no existing `run:` was edited.** `git diff main -- .github/workflows/go.yml` shows the `No package is silently untested` step, the `Vulnerability scan` step, the whole new `integration` job, and `uses:` lines rewritten from floating tags to digests. No pre-existing step's `run:` line changes.
- [ ] **Every action is pinned by digest.** `grep -c 'uses:.*@v[0-9]' .github/workflows/go.yml` prints `0`.
- [ ] **The top-level permissions block still reads `contents: read` and nothing more.**
- [ ] **govulncheck is clean.** `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` prints `No vulnerabilities found.`
- [ ] **The gitleaks deferral is understood, not overlooked.** Task 18 documents why a secret scanner cannot land here: the `SERVER_PRIVATE_KEY` literal is committed in five tracked files and copied into the published image, so gitleaks belongs to the change that ROTATES the key. Confirm a follow-up issue exists.
- [ ] **No TypeScript was touched.** `git diff --stat main -- src/ tests/ frontend/ package.json tsconfig.json jest.config.js` prints nothing.
- [ ] **`docker-compose.yaml` is additive.** `docker compose config --services | sort` prints `app`, `frontend`, `mongodb`, `postgres`.
- [ ] **The claim race has been observed failing.** Ask for the terminal output of Task 8's naive step. A concurrency test that has never been seen to fail is not evidence. Expected text: `claim double-allocated 7 of 7 rows (worst row claimed 12 times)`.
- [ ] **The SQL-discipline test has been observed failing.** Ask for Task 17's `rogue` step output, including the golangci-lint line that shows gosec did NOT flag it.
- [ ] **The migration guard, the conformance suite and the two ordering gates have each been run against a deliberate break.** Ask for the output of Task 17's `uuidv7()`-in-a-statement step, Task 19's `stationMatches → true` step, and Task 8's and Task 12's tiebreaker-deletion steps. The last two are the interesting ones: if either ordering test PASSED with the tiebreaker removed, the plan requires the tiebreaker restored AND the test's comment rewritten to stop claiming a guarantee it does not deliver — so check the comment, not just the green run.
- [ ] **The tripwire has been observed failing.** `WEATHER_TEST_REQUIRE_POSTGRES=1 go test ./internal/store/... -count=1` must be RED.
- [ ] **Nothing skips in CI, and the job PROVES it rather than the reviewer eyeballing it.** The `integration (postgres)` job has three machine gates, all of which must be present in `.github/workflows/go.yml`: `test -n` on both env vars before `go test` runs (so a mistyped `env:` key cannot silently skip the whole suite), `-v` plus a `grep -q -- '--- SKIP'` that fails the step (because `go test` prints SKIP lines only under `-v`, and a fully skipped package otherwise prints a bare `ok`), and a `grep` for `--- PASS: TestClaimPendingNeverDoubleClaims` on the repeated claim race (because `go test -run` with a pattern matching nothing exits 0). Verify by reading the two steps, not by reading the log.
- [ ] **Order independence.** `go test ./internal/store/postgres/ -shuffle=on -race -count=1` passes on three different seeds.
- [ ] **`internal/store` does not import pgx.** `go list -deps ./internal/store | grep -c jackc` prints `0`. This is what keeps the read-API plan testable with no database, and it is the single constraint that must not regress.
- [ ] **The fake's doc comment carries the warning.** `internal/store/fake/fake.go` states that the fake is not evidence for the claim query and that concurrency assertions must stay out of that package.
- [ ] **Every statement prepares.** `TestEveryStatementPreparesAgainstARealServer` passes and `AllQueries()` has 33 entries.
- [ ] **No driver error escapes, on the cancellation branch too.** `TestNoDriverErrorEscapesTheStore` passes including its canceled-context and expired-deadline cases, `classify` is the only function in the package that mentions `pgconn.PgError`, and the forbidden-token list contains `host=`, `user=`, `database=` and `dial` — the tokens a pgx CONNECT failure carries while still satisfying `errors.Is(err, context.DeadlineExceeded)`.
- [ ] **A NUL byte cannot 500 anything.** `store.ValidText` exists, `RecordStore.Get`, `RecordStore.TxIDExists` and `StationStore.List` all apply it, and the adversarial tables in `stations_test.go`, `security_test.go` and `storetest/conformance.go` all still contain NUL inputs. Measured: binding `"\x00"` to a text parameter fails with SQLSTATE 22021, so `?search=%00` and `/api/weather/%00` were 500s before the screen existed. The fix must be the guard, never a deleted fixture.
- [ ] **`Secret` is defined once and redacts in JSON.** `grep -rn 'type Secret' internal/` prints exactly one line, in `internal/config/secret.go`, and `TestSecretNeverPrints` asserts `json.Marshal` output for a value field, a pointer field and an embedded field. `encoding/json` ignores `fmt.Stringer`, so `MarshalJSON` is the only thing standing between a `Secret` and a response body.
- [ ] **Lint is clean with zero `//nolint`, and nothing was truncated.** `golangci-lint run --max-same-issues=0` prints `0 issues` (without that flag the 4th identical finding is hidden), `gofmt -l ./internal` prints nothing, and `grep -rn 'nolint' internal/` prints nothing.
- [ ] **Plan A's golden gate is intact.** `go test ./internal/weather -run TestGolden -update -count=1 && git diff --exit-code internal/weather/testdata/golden/` leaves the tree clean.

## Handover to the read-API plan (B2)

The frozen contract B2 programs against, and the two things it must not do.

**Frozen types** — `store.Status` (the four literals), `store.ChainStatus`, `store.Record`, `store.NewRecord`, `store.Station`, `store.Stats` with `TotalDataPoints()`, `store.Snapshot`, `store.ListFilter`, `store.StationFilter`, `store.Publication`, `store.BlockHeightUpdate`, `store.RequeueFilter`, `store.Deposit`, `store.ErrNotFound`, `store.ErrConflict`, `store.ErrInvalidText`, `store.ValidText`.

**Frozen seam** — `store.Store{Records, Stations, Deposits, Preflight, Health}`, the four interfaces, and `store.Pinger`.

**Also frozen, and in `internal/config` rather than the store** — `config.Secret`, whose `String()`, `LogValue()` and `MarshalJSON()` all render `[REDACTED]` and whose `Reveal()` is the only way to read the value. Use it for `SERVER_PRIVATE_KEY` and `TEMPEST_API_KEY` too. Do NOT define a second `Secret`: the whole reason it is in a leaf package is that `internal/api` may never import `internal/store/postgres`, and a hand-copied copy is how one of them ends up without `MarshalJSON` and serializes a raw credential into a response body.

**Test double** — `fake.New()` returns a `*fake.Store` with an injectable `Now func() time.Time` (defaulting to a fixed `2026-04-17T15:40:00Z`, which is what makes golden files byte-stable), an injectable `FailAll error` for exercising 500 paths, and an injectable `PingErr error` for exercising `/api/ready`'s 503. `f.Store()` returns the aggregate; `f.Stations()` returns the `store.StationStore` view; `f.SeedRecord` and `f.SeedStation` write verbatim state.

**The fake is only trustworthy because of `storetest.RunStoreConformance` (Task 19), and that is B2's problem too.** B2's entire suite runs against the fake, so every fake-versus-SQL divergence is a B2 test that passes while proving nothing — four such divergences existed in the first draft of this plan, including a `StationFilter.Search` the fake ignored completely. **When B2 needs behavior the fake does not yet have, add the assertion to the conformance suite FIRST and make both implementations satisfy it.** Adding it to the fake alone reintroduces exactly the class of defect Task 19 exists to close.

**Who owns `internal/config`: B2, as its FIRST task.** It is called out here because no plan owned it in the first draft and three §6 controls live there, one of which blocks B2 outright (the limiter cannot read a trusted-CIDR list nobody parses). B2 must land, before any handler: env parsing with a `Validate()` that returns a startup error naming every missing secret — no compiled-in default for `SERVER_PRIVATE_KEY` or `POSTGRES_PASSWORD`, so the pod CrashLoopBackOffs instead of running on the publicly known key; `TRUSTED_PROXY_CIDRS` parsed as CIDRs, a malformed entry a startup error, an empty list emitting exactly one boot WARN; and `PROOF_RATE_LIMIT_PER_MIN >= 1`, because zero means unlimited on an unauthenticated outbound proxy.

**Where NUL validation lives, stated so it is not done twice badly or zero times.** `store.ValidText` is the single definition. The store applies it so it is TOTAL — a NUL id is `ErrNotFound`, a NUL search is an empty page — which means no handler can produce a 500 from it. **B2 applies it in the clamp/parse helpers and returns 400**, because "not found" is the wrong answer to a malformed request and an empty result set hides the mistake from the caller. Both layers, on purpose: the store's job is to be un-500-able, the API's job is to be truthful.

**The rate-limiter contract B2 must implement, repeated here in full so that B2 cannot be authored without it** (all of it is also in this plan's Global Constraints): the client-IP trust model — peer gate first, no header read until `r.RemoteAddr` is inside `TRUSTED_PROXY_CIDRS`, `CF-Connecting-IP` first then `X-Forwarded-For` walked from the RIGHT, `.Unmap()` before every `netip.Prefix.Contains`, IPv6 keyed to `/64`, and `httprate.KeyByRealIP` and `httprate.CanonicalizeIP` both banned by name; scope-DISJOINT limiters, so one `/api/events` request decrements exactly one bucket and never both (with a test asserting the general bucket's remaining count is unchanged by an SSE request), because `/api` mounted before `/api/events` plus `EventSource` auto-reconnect turns the first 429 into a retry loop; `/api/health`, `/api/ready` and `/api/ops` registered BEFORE the limiter; and the numbers — 600/min general with burst 120, 60/min on `POST /api/verify`, 30 new SSE streams/min not drawn from the general bucket, 12 concurrent streams per IP, a global stream semaphore of 500 answering 503, `PROOF_RATE_LIMIT_PER_MIN` default 60, and `RateLimit-Limit`/`RateLimit-Remaining`/`RateLimit-Reset`/`Retry-After` on every 429 — plus a key map BOUNDED at 10 000 entries with eviction, and a test proving that N spoofed keys from an untrusted peer create exactly ONE entry.

**Two prohibitions.** `internal/api` must NEVER import `internal/store/postgres` — the acceptance check `go list -deps ./internal/store | grep -c jackc` exists to keep that true, and it is what lets B2's whole suite run in the ordinary `check` job with no service container. And `postgres.ErrOperation`, `postgres.ErrTransient` and any wrapped SQLSTATE must never reach a response body: B2 maps `store.ErrNotFound` to 404, `store.ErrConflict` to 409, and everything else to an opaque `{"error":"internal server error","request_id":"…"}`.

**Three things B1 deliberately left for B2.** The `isoMillis` marshaling type and the nullable-pointer projection (`store.Record` carries real `*time.Time`s so that a null is expressible, but formatting is the DTO layer's job). Station "online" derivation (the store returns `IsActive` and `LastReading`; the freshness window belongs in config). And `totalPages` (the store returns a raw `total`; the handler must emit `0` when `total` is `0`, or the frontend's Next button never disables).

## Known issues carried forward

Six things B1 discovered or inherited and could not close. Each belongs to a named later plan or a named owner, and each is written down here because the failure mode is silent.

1. **The lease bound in the design's config rule 14 is wrong, and Plan C must not implement it as written.** Rule 14 states `PROCESSING_LEASE > CreateAction_per_call_timeout`, i.e. 5m > 60s. But the error classifier retries a double-spend outcome on the SAME batch up to 5 times WITHIN one tick, each attempt bounded at 60 s. 5 × 60 s = 300 s = exactly the 5m default. Concretely: if attempts 1-4 each time out against a wedged storage server, the row's lease has 60 s left when attempt 5 starts; the lease expires mid-attempt, the reaper flips the row to pending with `adopt_required`, the next 3-second tick claims it, and a SECOND action is published while attempt 5's first action may still land. That is the duplicate on-chain write the whole design exists to prevent, reachable with no crash at all. The fix is to give the whole claim→publish→complete leg ONE parent context with a budget and restate rule 14 against that budget rather than the per-call timeout; restating it as `PROCESSING_LEASE > MaxDoubleSpendRetries × CreateAction_timeout` also works but stops being correct if the retry count changes. B1's `ReapExpired(ctx, lease, limit)` takes the lease as a parameter and is correct either way — the defect is in what Plan C will pass it, and in the rule-14 test, which passes today while the hole is open because it compares 5m against 60s.

2. **OFFSET pagination still drifts, and the design overstates the fix.** The `id` tiebreaker makes the order TOTAL, which is necessary but not sufficient: page boundaries still move when a new poll lands between the page-1 and page-2 requests, because OFFSET counts from a moving head. Keyset pagination (`WHERE (created_at, id) < ($1, $2)`) would close it, but the frontend sends `page=`, so OFFSET is required. This is ACCEPTED, not fixed, and should be described that way.

3. **`stations.last_temp` can never be fractional, so the design's example values are unachievable.** `air_temperature` is `FieldInteger` in the frozen wire schema, and `last_temp` is fed from it, so a sample response showing `"lastTemp": 18.3` cannot be produced and a golden file asserting it would be unsatisfiable. The `double precision` column and the never-omit-the-key rule are both correct and ship as specified; only the example values need correcting to integers. The larger question is Plan C's: Tempest reports `air_temperature` fractionally, and the mapper rule as written REJECTS a fractional value in an integer field, which taken literally rejects essentially every real reading and leaves `weather_records` empty. That must be resolved before Plan C's poller runs — either `air_temperature` becomes `FieldFloat` (a wire-format change needing a version-bump discussion) or the mapper rounds rather than rejects for that field. B1 is unaffected because its tests seed the store directly.

4. **CodeQL had no `go` language, and Task 18's PATCH may not be within the running agent's rights.** Verified live on 2026-07-29: `gh api repos/bsv-blockchain-demos/weather-proof/code-scanning/default-setup` returns `"languages":["actions","javascript","javascript-typescript","typescript"]`. Once the backend is Go, `internal/store/postgres` is scanned by no static analyzer at all — and static analysis of the backend language is a control the TypeScript has TODAY; it is the control whose alerts commit `5cfea93` existed to close. `govulncheck` is not a substitute: it scans the dependency graph against a CVE database, not first-party code. Task 18 tries to close this with `gh api --method PATCH repos/bsv-blockchain-demos/weather-proof/code-scanning/default-setup -f 'languages[]=actions' -f 'languages[]=go' -f 'languages[]=javascript-typescript' -f 'languages[]=typescript'` followed by a re-read asserting `go` is present. **If that returns 403, this item is the deliverable**: default setup is admin-only, so a repository admin must run that exact command, or an advanced `.github/workflows/codeql.yml` must be added deliberately (one job, `permissions: {contents: read, security-events: write, actions: read}`, matrix `[go, javascript-typescript, actions]`, actions digest-pinned). Owner: whoever holds admin on `bsv-blockchain-demos/weather-proof`. This is NOT closed by a comment in a CI step, which is where an earlier draft of this plan left it.

5. **`internal/config` is assigned to B2 but does not exist yet, so three §6 controls are unbuilt at the end of B1.** B1 creates `internal/config/secret.go` and nothing else in that package. Until B2's first task lands: there is no `Validate()` refusing to boot without `SERVER_PRIVATE_KEY` and `POSTGRES_PASSWORD` (spec §8.15 rule 1, the direct remedy for the leaked key and the single most important §6.6 control), no `TRUSTED_PROXY_CIDRS` parsing with the malformed-entry startup error and the one-line empty-list WARN (rule 17), and no `PROOF_RATE_LIMIT_PER_MIN >= 1` floor (rule 18). None of them is reachable from B1's scope — the store has no config surface — but the gap is silent, because a missing `Validate()` looks exactly like a service that starts fine. B2 must land config BEFORE any handler, and a reviewer of B2 should check that ordering first.

6. **The leaked `SERVER_PRIVATE_KEY` needs ROTATION, not just un-defaulting, and that is why Task 18 does not add gitleaks.** The literal hex is committed in `src/config/env.ts`, `src/service/wallet.ts`, `.env.example`, `docker-compose.yaml` and `QUICKSTART.md`, and `Dockerfile:45` copies `.env.example` into the published `weather-proof-back` image, so the key is in git history AND in a published artifact. Removing the default does not un-leak it. Until the key is rotated and the literal purged, a secret scanner in CI can only be green by allowlisting the five files, which is the laundering that hides the problem.


