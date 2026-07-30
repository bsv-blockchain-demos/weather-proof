# What B1 learned, for B2, B3 and Plan C

Plan B1 (the Postgres store) ran 19 tasks. Every one needed at least one fix round.
This is the transferable part — the things that cost real time, and the corrections to
claims that turned out to be wrong. Read it before writing tests for a later plan.

## 1. The dominant defect class: the fixture cannot reach the branch

**Seventeen** times in B1 a test looked correct, passed, and did not catch the bug it
existed to catch. Every single one was found by *mutating the code and re-running*, and
not one was found by reading. The shape is almost always identical: the assertion is
fine, but no fixture row can drive the branch it guards.

Concrete instances, so the pattern is recognisable:

- A `Snapshot` fixture gave four buckets the count `1`. Swapping their `Scan`
  destinations — reporting *pending* as *processing* — passed the whole suite. Fixed by
  giving every bucket a distinct count.
- The plan's headline concurrency test passed against a `ClaimPending` mutated to
  return **zero rows**: every assertion held vacuously on an empty result. Fixed with a
  liveness assertion.
- A "newer reading only" station guard could be deleted with the suite green, because
  every fixture began from a fresh station whose prior value was null.
- A `status` filter was untested because every fixture row already had that status.
- A dropped `id = ANY($1)` filter went uncaught because no fixture had a row
  deliberately *excluded* from the call — so a write meant for two rows touching every
  row was invisible.
- An adversarial-input table asserted only "nothing errors", which is satisfied by a
  search matching **nothing at all**. It needed a positive control.
- A "does not alias" test compared a mutated pointer against the same variable the seed
  helper had aliased it to, so the mutation corrupted both sides equally and the test
  passed with the fix removed.

**Practice:** for every test that exists to catch a specific defect, break the code in a
scratch copy outside the repo and confirm the test fails **under the full package
suite**, never under a `-run` filter. Two of B1's instances only discriminated in
isolation, because an earlier test in the same file had already changed shared state.

## 2. Prose in a frozen interface does not prevent violations

One rule — *a non-positive limit returns zero rows with a nil error, never unbounded* —
was violated **seven** times:

1. `ClaimPending` shipped violating it, with the rule already written in the interface.
2. `ReapExpired`/`Requeue` did the same, with the rule quoted verbatim in the task brief.
3. A `clampLimit` chokepoint was introduced. Correct fix.
4. `List` routed through it but had **no test proving the route**, so a refactor could
   bypass the chokepoint silently.
5. `ListStations`' specified code had no guard at all.
6. `ReconcileCandidates` omitted it again — the fifth consecutive brief to do so.
7. Three *fake* methods returned everything on a non-positive limit, diverging from
   Postgres in the opposite direction, while the fake's own `ClaimPending`/`List`
   clamped correctly.

Three distinct lessons, learned in order:

- A chokepoint stops the mistake being made somewhere new.
- Only a **per-call-site test** stops the chokepoint being stepped around.
- Fixing one implementation does not fix the other.

And the sibling parameter got missed entirely: `Offset` had no rule, no guard and no
test, and the fake **panicked** on a negative value — found only in the final
whole-branch review. When a rule protects one parameter, check its siblings.

## 3. Fake-versus-Postgres divergence is the expensive class

B2 has no database: the fake is its only test substrate. So every divergence is a B2
test that passes locally and fails in production. B1 produced, and fixed, at least:
negative-limit behaviour (panic vs error vs everything-returned), three disagreeing sort
orders, returned records aliasing the fake's internal state, a missing `error` column
write, and station search differing on **18 of 30** realistic queries — including
prefixes like `brist`, which is search-as-you-type against a live search box.

The shared conformance suite in `internal/store/storetest/conformance.go` is the only
structural defence. It runs one test set against both subjects and **names which one
diverged**. Extend it rather than writing per-implementation tests.

Two residual divergences are documented and deliberate: `PendingDeposits`' primary sort
key cannot be gated through the black-box `mk(t) store.Store` signature because the
fake's clock is frozen; and station search still differs on stemming, multi-word queries
and `websearch_to_tsquery` operators. Both are named in the relevant doc comments.

## 4. Postgres specifics that cost time

- **`created_at`/`processed_at` default to the *transaction* timestamp**, so every row
  written by one call shares it to the microsecond. Ties are the common case, not the
  exception, so every ordered query needs an `id` tiebreaker. Without one, two
  legitimate plans returned 19 of 20 *different* rows at the same offset.
- **To gate an ordering tiebreaker, read `EXPLAIN` first.** The technique is
  plan-shape dependent. Heap churn (shuffled no-op `UPDATE`s) discriminates an
  index-satisfied sort; an explicit `Sort` node was stable under churn at N=8 and needed
  N=40 with a full-permutation churn. Do not transfer the technique between queries.
- **`UPDATE … RETURNING` row order is undefined.** Compare results as **sets**. This was
  known for `ClaimPending` and discovered during B1 to apply to `ReapExpired` too, on a
  fresh table.
- **A VOLATILE function evaluates once per row.** `gen_random_uuid()` in a 21-row
  `UPDATE` produced 21 distinct values, destroying a batch label. Generate in Go, bind as
  a parameter.
- **`SELECT *` / `RETURNING *` are forbidden.** `pgx.RowToStructByName` treats an
  unmatched column as a hard runtime error, so a star projection breaks every query at
  runtime after the next migration with no compile-time signal. It matches by **name**,
  not position.
- **NUL and malformed UTF-8 are both rejected as `SQLSTATE 22021`.** `store.ValidText`
  needs *both* checks: NUL is itself valid UTF-8, so `utf8.ValidString` alone misses the
  case that originally 500'd the search box.
- **`pgx.BeginFunc` returns its own Begin/Commit errors**, which bypass any classifier
  applied only inside the closure. Those errors carry `user=`, `database=` and host:port.
- **`pgxpool.NewWithConfig` does not connect synchronously** — connect errors surface
  from a caller's first `Ping`/`Acquire`.

## 5. Corrections to things believed true mid-plan

- **`SKIP LOCKED` is not what prevents double-claiming.** Verified across 1,800
  concurrent claims and four plan shapes: bare `FOR UPDATE` produced zero double-claims.
  What is load-bearing is single-statement atomicity, `FOR UPDATE` row locks, and the
  `status = 'pending'` predicate living *inside* the locked subquery, which is what
  `EvalPlanQual` re-applies. `LockRows` sits below `Limit`, so a rechecked-away row never
  consumes a limit slot. `SKIP LOCKED` earns its place on latency (1ms vs 1004ms when a
  claim transaction is held open) and on removing a deadlock class (184 deadlocks vs a
  35 baseline).
- **Sort id lists before multi-row writes.** A writer touching a claimed batch in a
  different order than the claim deadlocks even under `SKIP LOCKED`.
- **`golangci-lint run` silently ignores unknown config keys; `config verify` rejects
  them**, and the action runs verify first. An invalid `funcorder` key passed locally for
  months and only failed in CI.
- **Standalone `gofumpt` is not the gate.** Its v0.10.0 binary disagrees with
  golangci-lint v2.12.2's embedded copy. The gate is
  `golangci-lint run --max-same-issues=0`. Reformatting to satisfy the standalone binary
  makes files *inconsistent* with CI.
- **`golangci-lint-action` must be v7+**; v6 refuses a v2 linter outright.
- **`go test ./...` exits 0 for a package with no test files**, and an integration test
  that skips for a missing DSN looks identical to one that passed. Hence the
  untested-package guard and `WEATHER_TEST_REQUIRE_POSTGRES`.

## 6. A security gate that cannot fail is worse than no gate

Task 17 added twelve gates and initially mutation-verified three. Of the remaining nine,
one turned out to be **structurally unfailable** for the mutation it names — a
source-text occurrence count cannot evaluate runtime semantics — and another had two real
escapes. Several are static-analysis checks over the package's own source, which is
exactly the shape that silently stops matching (a renamed constant, a tightened regex, a
moved file) and then passes for ever.

If a gate cannot be made to fail, fix it or delete it. Eleven real gates beat twelve with
one that lies. Where a lint-style gate is kept because a runtime sibling covers the same
property, say so at the gate.

Two known weaknesses remain, both documented in the final review and deliberately
deferred as B2-prerequisite work: the `classify` containment gate is a *call-site* test
covering 5 of 20 exported methods rather than a structural one, and the SQL-discipline
gate's `const`-anchored regex is bypassed by a `var`. Both want an AST-based replacement.

## 7. Design gaps found late, still open

The frozen `RecordStore` cannot express three transitions the design requires: spending
an attempt without terminating the row (so the attempt budget collapses to 1 rather than
3), resetting `attempts`, and writing `chain_status` `unmined`/`aborted` — which means
`Snapshot.AbortedCount` is structurally always zero, a bucket that was proved individually
wired and that nothing can increment. Resolve before Plan C relies on any of it.
