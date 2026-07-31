# weather-proof: TypeScript → Go backend port — design specification

**Date:** 2026-07-27
**Status:** Approved for implementation. Every decision in this document is made. There are no TBDs.
**Audience:** An engineer implementing the port who has seen none of the prior research. This document is self-contained.
**Source tree this was written against:** `bsv-blockchain-demos/weather-proof` at `origin/master` = **`5cfea93`**. Every claim about the TypeScript, the frontend, the Dockerfiles, the workflow and the manifests was read at that commit.
**Repos touched:** `weather-proof` (this repo), `bsva-infra-flux` (two separate PRs).

> **Naming.** The app is **weather-proof** everywhere: repo `github.com/bsv-blockchain-demos/weather-proof`, images `ghcr.io/bsv-blockchain-demos/weather-proof-{back,front}`, Flux base `apps/base/weather-proof/`, namespace `weather-proof`, hostname `weather-proof-${app_suffix}.bsvblockchain.tech`, Go module `github.com/bsv-blockchain-demos/weather-proof`.
> **Known wart, deliberately not migrated:** the AWS SSM parameter prefix is **`/apps/weather-chain/*`** (pre-rename). See §15.6. The string `weather-chain` also survives in `docker-compose.yaml` container/network names and the `Makefile` header; those are cosmetic and are cleaned up opportunistically, not as a migration.
> **CI branch is `master`,** not `main` (`.github/workflows/build.yml:8,11`; the GitHub API reports `default_branch: master`). Open PRs against `master`.

---

## 0. Read this first — the blocking prerequisite

**The fuel denomination must be raised to 50 satoshis before weather-proof goes live. It is a separate PR against an already-merged, live mainnet ConfigMap, and the storage-server pod must be recreated before this app publishes anything.**

The storage server's funder denomination lives in `apps/base/go-wallet-toolbox/infra-configmap.yaml` in `bsva-infra-flux`. That file is **already merged and running on mainnet** with `denomination_satoshis: 0`, which the server *derives* to **20 sat** from `expected_tx_size_bytes: 200`. At D=20 no weather record can be funded by a single fuel claim — the one-claim window at D=20 covers scripts of **24–33 bytes only**, and the smallest possible weather script is 36 B — so every weather transaction would cascade through 4–26 claims and cost **2.60× more** than at D=50 (§8.12).

### 0.1 The ordered prerequisite

| # | Action | Repo / target | Blocking? |
|---|---|---|---|
| **P1** | New PR setting `denomination_satoshis: 50` explicitly, plus `expected_tx_size_bytes: 500` so the *derived* value agrees with the override | `bsva-infra-flux`, `apps/base/go-wallet-toolbox/infra-configmap.yaml` | **Yes — must merge first** |
| **P2** | **Recreate the storage-server pod.** `infra-config.yaml` is mounted via `subPath`, and **subPath mounts never receive ConfigMap updates at all** — a running pod will never see the new value. `kubectl rollout restart deployment/app -n go-wallet-toolbox`, then confirm the resolved denomination in its logs | cluster `bsva-us-1`, ns `go-wallet-toolbox` | **Yes** |
| **P3** | A human with AWS SSM access (us-east-2, profile `bsva`, account 891377253213) creates `/apps/weather-chain/POSTGRES_PASSWORD` as a `SecureString`, **and verifies** whether the IAM policy on `arn:aws:iam::891377253213:role/eks-external-secrets-role-bsva-us-1` grants `ssm:GetParameter` on the *prefix* `/apps/weather-chain/*` or on an *enumerated* list of ARNs. If enumerated, the policy needs the new key. The failure mode is a silently stuck ExternalSecret, not an error | AWS | **Yes** |
| **P4** | Only then: merge the weather-proof manifest PR and uncomment `- ../base/weather-proof` in `apps/bsva-us-1/kustomization.yaml` | `bsva-infra-flux` | — |

### 0.2 Why 50 — and why the "frontier denomination" premise is deleted

The choice is made **for weather-proof alone.** The cluster server `https://go-wallet-us-1.bsvblockchain.tech` has exactly **one prospective tenant — this app — and currently zero transactions.**

An earlier draft of this spec justified D=40 as a "frontier denomination" that held a second, incumbent *throughput-dashboard* tenant cost-neutral. **That premise is false and is deleted, along with its prerequisite step.** `cmd/throughput_dashboard` in `go-wallet-toolbox` runs **its own** storage server in its own docker-compose (`SERVER_URL=http://infra:8100`, its own `infra-config-docker-throughput-mainnet.yaml`), never contacts the cluster server, is referenced by no Flux manifest, and is not deployed in `bsva-us-1`. There is no shared-tenant constraint, no cost-neutrality frontier, and **no prerequisite to change the dashboard's hardcoded `DenominationSatoshis`.** (That the dashboard hardcodes 30 while its own local config derives 20 is a real latent bug — **file it as a separate issue against `go-wallet-toolbox`**, §20 item 7. It is out of scope here.)

With that constraint removed, 50 is chosen on the weather workload's own merits:

| Property | At D=50 | At D=40, for contrast |
|---|---|---|
| One-claim script window (contiguous from 0) | **0–297 B** | 0–199 B |
| Headroom over the 169 B design worst case | **128 B (75.7 %)** | 30 B (17.8 %) |
| One-claims the repo's own 211 B `extremeWeatherData` fixture? | **Yes — 1 claim / 50 sat** | **No — 2 claims / 80 sat** |
| Claims for a saturated 21-output batch at S=169 | **11** | 16 |
| Fee multiplier over the honest miner fee, `D/(D−14.8)` | **1.4205×** | 1.5873× |
| Fan-out overhead per 100 fuel UTXOs | **7.348 %** | 9.17 % |
| Standing pool | **1000 × 50 = 50 000 sat (0.0005 BSV)** | 1500 × 40 = 60 000 sat |
| 0.1 BSV runway, worst case K=20/S=169 | **58.8 days** (§8.12) | 53 days |

D=50 costs marginally more per claim and **less in total**, because it needs fewer claims and amortises the fixed 360-sat leaf fan-out fee over 25 % more face value. It also has a property that simplifies the on-chain contract: **at D=50 a weather transaction never receives a change output** (§8.6).

### 0.3 The exact ConfigMap diff (P1)

```yaml
    utxo_management:
      strategy: throughput
      throughput:
-       expected_tx_size_bytes: 200         # derives ceil(200/1000*100) + 0 = 20 sat
+       # INERT once denomination_satoshis is explicit (Throughput.Denomination()
+       # short-circuits on an explicit value). Kept only so the DERIVED value agrees
+       # with the override: ceil(500/1000*100) + expected_output_satoshis(0) = 50.
+       # STATED HONESTLY: 500 B is NOT the real expected size of a weather tx. A
+       # routine 19-output batch signs at ~2.95 kB and a saturated 21-output batch at
+       # ~5.4 kB (spec §8.7). 500 exists to make the derivation equal the override.
+       expected_tx_size_bytes: 500
        expected_output_satoshis: 0         # MUST REMAIN 0 — see the warning below
-       denomination_satoshis: 0            # -> derived 20
+       # Chosen for the weather-proof tenant alone; the cluster server has no other
+       # tenant. One-claims every weather record up to 297 B (worst measured 169 B;
+       # the repo's own extreme fixture 211 B).
+       # MUST equal the client FuelKeeper's Denomination exactly, or every leaf
+       # fan-out is rejected by validateFuelShape — and that rejection is SILENT.
+       # MIRRORED BY (update together): weather-proof internal/config
+       #   DENOMINATION_SATOSHIS, shipped in apps/base/weather-proof/app-configmap.yaml
+       denomination_satoshis: 50
        target_tps: 1000
        expected_confirmation_seconds: 300
        pool_headroom_factor: 1.5
        target_pool_size: 0
        low_water_percent: 60
        high_water_percent: 100
        spend_policy: prefer_mined
        pool_basket: fuel
        reserve_basket: reserve
        fanout_outputs_per_tx: 100
        fanout_max_txs_per_round: 12000
        fanout_tree_depth: 2
        consolidation_inputs_per_tx: 1000
        top_up:
          enabled: true
          interval_seconds: 10
          start_immediately: true
```

**Nothing else in the ConfigMap changes.** `fee_model: {type: sat/kb, value: 100}` stays (ARC/GoBDK 465-reject below the 100 sat/kb floor). The basket names stay. `spend_policy: prefer_mined` stays.

> **Warning to write into the PR description.** `expected_output_satoshis` **must remain `0`.** If `denomination_satoshis` is ever deleted or reset to 0 while `expected_output_satoshis` holds the library default of **170**, the derived denomination silently becomes **220**, not 50 — and every client leaf fan-out at 50 is then rejected by `validateFuelShape` with **no error surfaced to the client** (`ensureChunks` logs `WARN "chunk fan-out failed, continuing with provisioned chunks"` and returns `nil`).

**Rollback** is reverting the server ConfigMap and the app's `DENOMINATION_SATOSHIS` **together**, never one side alone.

**Review-checklist addition (part of P1):** add a comment to `infra-configmap.yaml` naming every client that mirrors `denomination_satoshis` — currently only weather-proof's `internal/config` — and add that file to the ConfigMap's review checklist.

---

## 1. Goal and non-goals

### 1.1 Goal

Replace the TypeScript weather-proof backend with a single Go binary that:

1. Polls the Tempest weather API on a fixed interval and durably queues one record per station per poll.
2. Encodes each record into a **valid, deterministic** `OP_FALSE OP_RETURN` locking script in a fixed documented field order, **hard-capped at 297 bytes** (§7.6) and readable by its own decoder, and anchors batches of them on BSV mainnet. The format is **internal to this backend** — byte-parity with the historical TypeScript encoder is **not** a requirement (§7).
3. Serves the **five HTTP endpoints, one SSE stream and two probe endpoints** that the redesigned React SPA and kubelet actually need (§13), plus `GET /api/proof/{txid}`.
4. Replaces ~470 lines of hand-rolled hash-puzzle funding machinery with the go-wallet-toolbox **client-side FuelKeeper** plus server-side throughput funding — so no application code ever names a funding basket, builds a funding script, or selects a UTXO.
5. **Does not regress any security control the TypeScript has at `5cfea93`** — rate limiting, SSRF defence, injection/input-validation defence, workflow permissions (§6, a first-class hard requirement).
6. Survives unattended operation: an atomic record claim, bounded retries, no data-destroying failure modes, an idempotent publish leg, honest self-reporting, and a documented operator deposit path.

### 1.2 Non-goals

| Non-goal | Why |
|---|---|
| Redesigning the frontend | The redesign (`698c944`, `8602cd1`) is **kept**. Frontend changes in scope are limited to the five surgical fixes of §13.9 — a Docker build-arg, a `.dockerignore`, replacing a dead nginx config, deleting three dead components, and a one-line BEEF-resolution fix. |
| Making the landing page's numbers real | `/` renders entirely hardcoded fiction (`'19+'`, `'50,000+'`, `'1M+'`, a fake txid and block height) and makes **zero** backend calls. Wiring it to live data is **new product work**, not a port. Recorded as a follow-up. |
| Backfilling the existing MongoDB records | **Impossible, not merely declined.** The Atlas cluster is gone and it held the only copy of the txids (§14.1). |
| **Byte-parity with the historical TypeScript encoder** | **Released — and released permanently, not deferred.** The on-chain encoding is consumed by nothing outside this backend: the frontend's `verify.ts` is the only file in `frontend/src/` importing `@bsv/sdk`, and it parses BEEF and checks a merkle proof without ever reading the `OP_RETURN` payload (§7, §12.4). The requirement is a valid, deterministic, capped, round-trippable script — not a specific historical byte string. **Do not reinstate a TypeScript oracle, a pinned `@bsv/sdk`, a `parity/ts/` tree or a `make parity` target;** every one of those existed only to serve a constraint that no longer exists (§7.0). |
| A sweep tool for the legacy hash-puzzle UTXOs | Not currently possible — the old storage server's auth endpoint returns HTTP 500 (§14.3). Deferred, non-blocking. |
| Client-side verification that the on-chain script matches the displayed JSON | Requires meaningful frontend work. Recorded as a known weakness (§12.4). |
| Porting `src/notification/*` (Twilio) | Never instantiated. Replaced by structured logs + external alerts — **conditional on §11.3 shipping in the same PR** (§5 row 11). |
| Multi-replica app deployment | The FuelKeeper's `roundInFlight` is a per-process `atomic.Bool`, and the record claim is the only cross-pod serialiser. `replicas: 1`, `strategy: Recreate` (§15.3). |
| OpenTelemetry / Prometheus metrics | No scrape target exists in this cluster; `tracing.enabled: false` and no `observability` block. Observability is structured stdout + `/api/ops` (§11). |
| Re-attempting an nginx `/api` reverse proxy in the frontend image | **Ruled out.** See §15.1. |

---

## 2. Context

### 2.1 The TypeScript at `5cfea93` — including the four commits earlier research never saw

`src/` is 33 files / ~2 900 lines. **`HEAD` compiles clean** (`npm install` then `npx tsc --noEmit` exits 0 with zero diagnostics — an earlier claim that it did not compile was an artefact of a dropped local commit that added `src/scripts/prove-data-lock.ts`; **that file does not exist at `HEAD`**).

| Area | Files | Behaviour at HEAD |
|---|---|---|
| Config | `src/config/env.ts` | 14 env vars + a `validateConfig()` that throws. **A committed mainnet-capable default private key.** `src/service/wallet.ts:4-6` **re-reads `SERVER_PRIVATE_KEY`, `WALLET_STORAGE_URL` and `BSV_NETWORK` straight from `process.env` with its own duplicated defaults**, bypassing `config` and `validateConfig` — two sources of truth for the three most important variables. |
| Encoding | `src/format/{types,constants,schema,encoder,decoder}.ts`, `src/utils/float-encoder.ts` | 33 fields, alphabetical order, `OP_FALSE OP_RETURN OP_1` prefix, `@bsv/sdk` `Script.writeBn`/`writeBin` semantics, floats scaled ×1e6 through `Math.round`. **Byte-identical across all 10 recent commits** — `git log 60153c8..5cfea93 -- src/format src/utils` is empty and the diffstat for those paths is empty. *(The Go port keeps the **33-field list and its alphabetical order** from `schema.ts` and keeps minimal push encoding; it does **not** keep the JavaScript numeric semantics, because it does not have to match these bytes — §7.0.)* |
| Ingestion | `src/service/tempest.ts`, `src/service/queue.ts` | `getStations()` (1 h in-process cache) + **serial** per-station `better_forecast` with `AbortSignal.timeout(15 s)`; `setInterval(POLL_RATE=300 s)`; insert `status:'pending'`. |
| Publishing | `src/service/processor.ts`, `src/service/transaction.ts` | `setInterval(3 s)`; **`find({status:'pending'}).limit(100)` then a separate `bulkWrite` setting `processing`** — a non-atomic claim (§5.0). Spends hash-puzzle funding UTXOs with a `'20'+preimage` unlocking script. `acceptDelayedBroadcast: false`. Assumes `outputIndexes = [0..N-1]`. Failures return the whole batch to `pending` **forever**. |
| Funding | `src/service/setup.ts`, `src/service/monitor.ts`, `src/scripts/hash-puzzle.ts`, `src/scripts/setup-funding.ts` | Pre-mints 1 000 hash-puzzle UTXOs of 1 000 sat into a `funding` basket; a 60 s monitor tops it up. Each output has its **own random preimage**, stored only in the old storage server's `customInstructions`. |
| Concurrency guards | `src/service/wallet.ts:10-33` | `queueWeatherTx` = a real serialising promise chain. `queueFundingAction` = a **single-flight collapse** (added by `ea0c654`): a concurrent caller is handed the already-running promise and its own `fn` is never invoked. Both are module-level `let` in one Node process — **no protection across processes or restarts.** |
| doubleSpend retry | `src/service/transaction.ts:7-8,18-33,64-125` | `MAX_DOUBLE_SPEND_RETRIES = 5` + `isDoubleSpendError` (added by `11ecfd5`): `error.name === 'WERR_REVIEW_ACTIONS'` **and** `error.reviewActionResults` contains an entry with `status === 'doubleSpend'`. Re-runs `listOutputs({basket:'funding'})` on every attempt. |
| API | `src/api/index.ts`, `src/api/routes/{weather,stations,verify,proof,events}.ts` | Express; two `express-rate-limit` instances (added by `5cfea93`); CORS with `credentials: true`; 6 routes + SSE + `/api/health`. |
| Store | `src/db/{connection.ts,models/{weather-record,station,global-stats}.ts}` | Mongoose, 3 collections. `RecordStatus = pending\|processing\|completed\|failed` — **`'failed'` is never written by any code path**. **No unique index anywhere**, so nothing prevents duplicate `(stationId, timestamp)` rows. |
| Dead code | `src/notification/*` (Twilio never instantiated), `src/scripts/locking-scripts.ts`, `src/index.ts`, `processAllPending`, `createWeatherTransactionBatch` | — |

**Repo-state caveats that matter to the port:**

- **No lockfile is committed anywhere.** `.gitignore:3` ignores `package-lock.json` (and `yarn.lock`); `git ls-files | grep -i lock` returns nothing. Both Dockerfiles run `npm install`, not `npm ci`. `@bsv/sdk` is declared `^1.7.0` and currently resolves to **1.10.3**; `@bsv/wallet-toolbox-client` is `^1.0.0` resolving to 1.7.19. This is a **supply-chain** defect and is fixed for the surviving JavaScript — `frontend/package-lock.json` is created, un-ignored and committed, and `frontend/Dockerfile` switches to `npm ci` (§6.6). *(An earlier draft called this out because the encoder's byte-parity oracle hung off the floating `@bsv/sdk`. That oracle is deleted — §7.0 — so the floating dependency is now only a supply-chain concern, and only for the frontend.)*
- `ENCODING.md`'s "Field Order" section **contradicts** `src/format/schema.ts`. The schema is alphabetical. **Port from the code, never from that doc** (corrected as a deliverable, §19).
- README's "111 tests, 98 % statement coverage" is stale: `coverage/` covers only `src/format/*`, `src/utils/float-encoder.ts` and `src/index.ts`. **Service-layer coverage is zero.**
- `frontend/README.md:157` claims the Dockerfile "builds and serves via nginx". **It does not** (§15.1) — that sentence is what produced a phantom port mismatch in earlier research.
- `Dockerfile:54` says `EXPOSE 3000` while the app binds `API_PORT`, default **3001** (`src/config/env.ts:37`, `src/api/index.ts:74`). Documentation-only, but the Go image must `EXPOSE` what it binds.
- **Committed secrets: two distinct private keys across six locations.** Key A `bcc56b…0049` in `src/config/env.ts:11`, `src/service/wallet.ts:4`, `.env.example:2`, `QUICKSTART.md:41`, and `docker-compose.yaml:39` **and** `:97`. Key B `f03af0…139f` in `.env.docker:6`, directly above `BSV_NETWORK=main` — **a committed mainnet key**. `docker-compose.yaml:99` sets `BSV_NETWORK=main` for the `setup` service, so key A is used on **mainnet** by default in that profile. Mongo root credentials `admin/password` are also committed (`docker-compose.yaml:8-9,36,96`). `Dockerfile:45` does `COPY .env.example ./.env.example`, and `.dockerignore` excludes `.env` but **not** `.env.example` — so the published `weather-proof-back` image **ships a private key on disk at `/app/.env.example`**. Treat **both** keys as compromised.

### 2.2 Two GlobalStats fields are semantically wrong at HEAD — fix them deliberately

| Field | What HEAD does | Consequence |
|---|---|---|
| `totalTx` | Incremented by `records.length`, not by 1 per transaction (`src/service/processor.ts:148-160`). So the field named `totalTx` counts **records**. `totalDataPoints = totalTx × 33`. | The dashboard's "Total Tx on-chain" tile shows ~19× the real transaction count. |
| `activeStations` | Incremented by `stationResult.upsertedCount` from the *processor's* Station bulkWrite — but the **poller** already upserts every station from the Tempest API first, so `upsertedCount` is effectively always 0. | `activeStations` stays **0 forever**. `GET /api/stations` masks it with `globalStats?.activeStations ?? stationTotal`, but **`??` does not catch `0`**, so once the doc exists the dashboard shows **0 active stations** — and the SSE payload sends it straight from the doc with no fallback at all. |
| `Station.lastBlockHeight` | Declared, defaulted and read by the API but **never written by any code path** (grep: only `station.ts:23,41` and `stations.ts:69,114`). | The frontend masks it with a `??` fallback scan of the records page. |

The Go port defines all four stats honestly (§4.2, §13.3) and **does** write `last_block_height` (§10.6, §13.6).

### 2.3 Two live bugs in the shipped images

1. **Every WhatsOnChain explorer link on the live mainnet site points at testnet.** `VITE_BSV_NETWORK` is never passed as a Docker **build arg** — `build.yml:65-66` passes only `VITE_API_URL`, and `frontend/Dockerfile` declares only `ARG VITE_API_URL`. It *is* set as a **runtime** container env var in `weather-proof-front.yaml` and `docker-compose.yaml`, which does nothing for a statically built Vite bundle (`VITE_*` are compile-time substitutions burned into the JS). `frontend/.env` is gitignored so it does not exist in the CI checkout. Net: `import.meta.env.VITE_BSV_NETWORK` is `undefined` in the shipped bundle, `getNetwork()` falls through to `'test'` (`services/verify.ts:16-19`), and `WeatherDetail.tsx:141` links mainnet txids to `https://test.whatsonchain.com/tx/<txid>`. Fixed in §13.9.
2. **The rate limiter buckets the entire internet into one key.** `app.set('trust proxy', …)` appears **nowhere** in `src/` (grep confirms zero hits) and no custom `keyGenerator` is set, so `express-rate-limit` keys on `req.ip` = the immediate TCP peer, which behind cloudflared → Traefik is the proxy pod. The "100 req/min per IP" is **100 req/min total for the whole internet**, and the SSE limit is ~10 stream starts per minute globally — against a frontend that fires **two prefetch requests per row hover** across up to 50 dashboard rows. **Porting the limits verbatim would ship a self-DoS.** Fixed in §6.1.

### 2.4 What the storage server provides

The app targets **`https://go-wallet-us-1.bsvblockchain.tech`** — go-wallet-toolbox **v0.184.3**, live and healthy on mainnet, namespace `go-wallet-toolbox` in cluster `bsva-us-1` (AWS account 891377253213, us-east-2). An unauthenticated `GET` correctly returns 401.

It runs in **throughput mode**: a client issues a bare `CreateAction` with outputs and **no inputs**, and the server's funder claims exact-denomination P2PKH "fuel" UTXOs from the caller's `fuel` basket to cover the fee.

| Server key | Value | Consequence for this app |
|---|---|---|
| `fee_model` | `sat/kb`, `100` | fee = `ceil(size/1000 × 100)`. Not a free variable — ARC/GoBDK 465-reject below it. |
| `denomination_satoshis` | `0` → derived **20** → **must become 50** (§0) | The one number the client cannot discover at runtime and must hand-mirror. |
| `commission.satoshis` | `0` | Commission disabled (`Commission.Enabled()` is `Satoshis > 0`); no 34 B commission output. |
| `pool_basket` / `reserve_basket` | `fuel` / `reserve` | Baskets are per-user **and** per-storage-server. |
| `fanout_outputs_per_tx` | `100` | The client's `FanoutOutputsPerTx` must equal this **exactly** (§8.13). |
| `spend_policy` | `prefer_mined` → tiers `[mined, unproven]` | Minted fuel becomes claimable at **ARC acceptance**, not at mining. Excludes `sending`. |
| `top_up.interval_seconds` | `10` | Inherited by `FromThroughput`; **must be overridden** client-side. |
| `target_tps` / `expected_confirmation_seconds` / `pool_headroom_factor` | `1000` / `300` / `1.5` | `TargetPool()` = **450 000** UTXOs. Catastrophic if inherited (§8.13). |
| `fanout_max_txs_per_round` | `12000` | **Must be overridden** client-side. |
| `tracing.enabled` | `false`, no `observability` block | **No OTel gauges are exported today** (§11). |
| `max_rebroadcast_attempts` | `0` | A queued broadcast that fails is never retried by the server (§10.6). |

Also load-bearing:

- **Baskets are per-user and per-storage-server.** Reusing `/apps/weather-chain/SERVER_PRIVATE_KEY` buys a stable on-chain identity and nothing else: `fuel`, `reserve` and `default` all start **empty** on the new server (§14.3).
- **Deposits must land in `default` via `InternalizeAction` with `InternalizeProtocolWalletPayment`.** That path writes `BasketName = wdk.BasketNameForChange` ("default") and `Change: true`. A `BasketInsertion` into `reserve` writes `Change: false` and is **permanently unspendable**.
- **Ordinary change goes to `default`, not to the shaped basket** (`create.newOutputs` always assigns change `BasketName = wdk.BasketNameForChange`). Leaf fan-out leftovers therefore return to `default` and never pollute the reserve chunk count, so `countBasketOutputs(reserve)` is an **exact chunk count**.
- **`RandomizeOutputs` defaults to `true`** in the Go wallet (`mapping_create_action_args.go:108`), and `CreateActionResult` carries **no** output-index mapping. It must be pinned `false` — see §10.0.

---

## 3. Architecture

### 3.1 One binary, subcommands, eight supervised goroutines

`cmd/weather` is a single static binary with a small subcommand dispatcher (stdlib `flag`, no cobra):

| Subcommand | Purpose |
|---|---|
| `weather serve` (default when argv[1] is absent) | The long-running service. |
| `weather deposit-address` | Print a fresh BRC-29 self-payment address and record its derivation (§9.1). |
| `weather deposit --txid <id> [--vout n]` | Internalize a funding payment into `default` (§9.2). |
| `weather requeue --status failed [--since <dur>] [--station <id>] [--limit n] [--dry-run]` | Operator recovery from terminal `failed` (§10.5). |
| `weather preflight` | Run the two fuel-shape probes and exit; used in the deploy checklist (§8.14). |
| `weather stats-recompute` | Rebuild `app_stats` and `stations.tx_records` from `weather_records` (§4.2). Repairs counter drift; also the answer to "the dashboard tiles look wrong". |

`serve` runs **eight** goroutines under one `errgroup.Group` rooted at a `signal.NotifyContext(SIGINT, SIGTERM)`:

| # | Goroutine | Tick | Owner of | Fatal errors? |
|---|---|---|---|---|
| 1 | **Poller** | `POLL_RATE` (300 s), immediate first run + ≤30 s jitter | Tempest client, station cache, station upserts | No — only `ctx.Err()` |
| 2 | **Processor** (includes the lease reaper) | `PROCESSOR_INTERVAL` (3 s) | Claim → adopt-check → encode → publish → complete; circuit breaker | No — only `ctx.Err()` |
| 3 | **Keeper supervisor** | `FUEL_INTERVAL` (60 s), plus catch-up rounds | `fuelkeeper.RunOnce` under a per-round timeout | No — only `ctx.Err()` |
| 4 | **Reconciler** | `RECONCILE_INTERVAL` (5 min) | Mined/aborted transitions for `completed` rows; writes `block_height` and `stations.last_block_height` | No — only `ctx.Err()` |
| 5 | **Sampler** | 60 s | The heartbeat log line and the `/api/ops` snapshot | No — only `ctx.Err()` |
| 6 | **SSE hub** | event-driven + 60 s floor | The bounded client set; `stats_update` fan-out; per-client 30 s `:ping` | No — only `ctx.Err()` |
| 7 | **API HTTP server** | — | `API_PORT` (3001): the five public routes, the SSE stream, `/api/proof`, `/api/health`, `/api/ready` | Only a bind failure at boot |
| 8 | **Ops HTTP server** | — | `OPS_PORT` (9090), cluster-internal only: `/api/ops` (§11.2) | Only a bind failure at boot |

Every long-running loop uses an explicit `time.Ticker` **with its own single-flight guard**. This is not decoration: **none of the TypeScript's three loops has an overlap guard** — `setInterval(async () => { await f() })` re-enters whenever `f` is slower than the interval, and the poller's `f` (a serial per-station fetch with a 15 s timeout each) genuinely can be. None has jitter either.

### 3.2 Boot order — the storage server is *not* on the liveness path

Inverted relative to the naive wiring, because a storage-server outage must not stop weather collection or take down the read-only API:

```
1. load + validate config                     (fail closed, exit non-zero)
2. open pgxpool, run migrations                (fail closed, exit non-zero)
3. start POLLER       — wallet-free
4. start SAMPLER      — wallet-free (reports walletConnected:false)
5. start SSE HUB      — wallet-free
6. start HTTP SERVERS — wallet-free (all read endpoints, /api/health, /api/ready, /api/ops work)
7. go connectWallet(ctx):                      — background, UNBOUNDED backoff (1s -> 60s cap, jitter)
     a. build *wallet.Wallet + TimeoutWallet adapter
     b. Balance() liveness probe
     c. fuel preflight (§8.14)
          - validateFuelShape rejection         -> log ERROR, exit non-zero  (deterministic config bug)
          - ErrNotEnoughFunds / 5xx / transport -> WARN, degraded:"unfunded", CONTINUE
     d. publish the wallet handle to processor + keeper supervisor
     e. start KEEPER SUPERVISOR and RECONCILER (errgroup members, started late)
8. PROCESSOR starts at step 3 but stays parked until the wallet handle is published;
   rows accumulate as `pending`, which is correct durable-queue behaviour.
```

**The app never calls `os.Exit` on a storage-server *error* after boot.** The one exception is a `validateFuelShape` **validation** rejection — a deterministic client/server constant mismatch, not an outage, where crashlooping is the correct loud signal because the alternative is money-losing silence. The preflight fingerprint cache (§8.14) bounds the crashloop cost to one chunk + one leaf per config change per day.

Per-call context timeouts are sized **above** the go-sdk BRC-104 auth re-handshake path (which retries three times), separately from the HTTP client timeout:

| Call | Per-call ctx timeout | HTTP client |
|---|---|---|
| `CreateAction` (synchronous broadcast, §10.6) | 60 s | `http.Client{Timeout: 60s}`, `MaxIdleConnsPerHost: 128`, `MaxIdleConns: 512`, `IdleConnTimeout: 90s`, `CheckRedirect: ErrUseLastResponse` |
| `FanOutFuel` | 45 s | same |
| `ListOutputs` | 20 s | same |
| `ListActions` | 30 s (per **page**) | same |
| `InternalizeAction` (CLI only) | 60 s | same |
| `Balance` (probe) | 20 s | same |
| Tempest `stations` / `better_forecast` | 15 s (TS parity) | separate client, same hardening |
| WhatsOnChain `GET /tx/{txid}` (verify) | 10 s per request, 20 s per batch | separate client, same hardening + `io.LimitReader(1 MiB)` |

### 3.3 Data flow

```
  Tempest API                        Postgres                        go-wallet-us-1        WhatsOnChain
       |                                 |                                 |                    |
  [1] POLLER -- stations (1h cache) --> UPSERT stations                    |                    |
       |       bounded-concurrent        INSERT records status='pending'    |                    |
       |       better_forecast           ON CONFLICT (station_id,           |                    |
       |                                 observation_time) DO NOTHING       |                    |
       |                                 |                                 |                    |
  [2] PROCESSOR <-- ClaimPending(21) ----|  UPDATE ... FOR UPDATE SKIP      |                    |
       |            (atomic, RETURNING)  |  LOCKED, stamps claim_ref        |                    |
       +- adopt-check prior claim_refs ------------------- ListActions{label} ->                 |
       +- encode + size-check per record (poison isolation)                  |                   |
       +- CreateAction{N zero-sat OP_RETURN outputs, RandomizeOutputs:false, |  funder claims    |
       |               Labels:["weather","batch-<uuid>"]} ----------------->|  fuel UTXOs       |
       +- Complete(id, txid, vout) + app_stats + station counters -> ONE tx  |                   |
       +- notify SSE HUB                 |                                 |                    |
                                         |                                 |                    |
  [3] KEEPER SUPERVISOR - RunOnce ------------------ ListOutputs(fuel) ---->|                    |
       |                                 |           FanOutFuel(chunk/leaf) |                    |
  [4] RECONCILER ------------------------------------ ListActions{"weather"}->                   |
  [5] SAMPLER --- heartbeat log + /api/ops snapshot                                              |
  [6] SSE HUB --- event: stats_update to every connected /api/events client                      |
  [7] HTTP --- /api/stations, /api/stations/{id}, /api/weather, /api/weather/{id},                |
              POST /api/verify -------------------------------------------------------------->   |
              /api/events, /api/proof/{txid}, /api/health, /api/ready
  [8] OPS  --- /api/ops on OPS_PORT (cluster-internal, NOT published on the Service)
```

### 3.4 Where the FuelKeeper sits, and why it is client-side

The FuelKeeper runs **beside** the processor, never in its path. The processor issues a bare `CreateAction`; the server's funder silently claims fuel. The keeper's only job is to keep the `fuel` basket stocked:

```
default basket --(chunk fan-out: 1 tx, K outputs of 6,000 sat)--> reserve basket
reserve basket --(leaf fan-out: 1 tx per chunk, 100 outputs of 50 sat)--> fuel basket
```

It is client-side because **fan-out transactions are signed with the operator's private key, which lives in this process**. The storage server orchestrates and validates the shapes but does not hold the key. This is a real property — and it is also the limit of the security story. §12 states plainly that the new scheme **moves** trust rather than removing it, and is **not** safer than the hash puzzle it replaces.

Deliberate keeper choices:

- **We own the schedule.** We call `RunOnce` from our own supervisor rather than `keeper.Run`, so a stuck round is observable (`roundInFlight` makes `RunOnce` return instantly, which otherwise looks identical to a healthy no-op).
- **Serial minting** (`MintConcurrency = 1`): a 4-leaf round has no latency problem, and concurrent leaves collide on reserve-chunk selection in `mintOneLeaf`'s retry path.
- **No `SetStreamActive(true)`**: this is not a stream. `StreamLeafCap` / `StreamYieldMultiple` are inert.
- **A de-duplicated top-up must not apply the winner's accounting twice** — see §5 row 4 for the TypeScript bug this rule exists to avoid reproducing.

---

## 4. Package layout and schema

Module path: **`github.com/bsv-blockchain-demos/weather-proof`**, `go 1.26.3` (matching go-wallet-toolbox so its toolchain requirement is satisfiable).

**Go code lives at the repo ROOT, alongside `frontend/`.** Not in a `backend/` subdirectory. Reasons in order of weight: (1) `build.yml`'s build contexts are already `.` for the back image and `./frontend` for the front image, so the CI wiring — precisely what broke in the reverted `v0.0.9` — does not change at all; (2) `go.mod` at the module root is what makes the canonical import path resolve without a `replace`; (3) `frontend/` contains no `.go` files, so `go build ./...`, `go vet ./...` and `go test ./...` skip it automatically; (4) root `internal/` means only this module can import it.

### 4.1 Packages

| Path | Purpose | Replaces (TS) |
|---|---|---|
| `cmd/weather/main.go` | Subcommand dispatch; `serve` wiring and lifecycle only. No business logic, no package-level state. | `src/app.ts` (minus funding) |
| `cmd/weather/deposit.go` | `deposit-address` and `deposit`. | **new** — sole successor to `src/scripts/setup-funding.ts` |
| `cmd/weather/requeue.go` | `requeue`. | **new** |
| `cmd/weather/preflight.go` | `preflight` (same code path as boot). | **new** |
| `cmd/weather/statsrecompute.go` | `stats-recompute`. | **new** (§2.2) |
| `internal/config/config.go` | One flat `Config`; env loading; the mirrored fuel knobs. **No default private key — fails closed.** The single source of truth: nothing else in the tree reads `os.Getenv`. | `src/config/env.ts` **and** the duplicate defaults in `src/service/wallet.ts:4-6` |
| `internal/config/secret.go` | `Secret` type: `String()`, `LogValue()`, `MarshalJSON()` all return `"[REDACTED]"`; `Reveal()` is the only accessor. | **new** (§12.5) |
| `internal/config/validate.go` | Aggregating `Validate(subcommand)` that collects **all** errors and **never echoes a value**. Imports `internal/fuelmath` **only** — never `internal/fuel`, which would close the cycle `config → fuel → config`. | `validateConfig()` |
| `internal/weather/types.go` | `WeatherData` with 33 snake_case json tags declared **alphabetically**; `FieldType`; `FieldDefinition`; `Version=1`, `FloatScale=1e6`, `FloatEpsilon=1e-6`, `DataFieldsPerRecord=33`. | `src/format/types.ts` + `constants.ts` |
| `internal/weather/schema.go` | `FieldSchema`: the 33-entry ordered slice, `air_density` → `wind_gust`. **Order is the wire format.** Ported from `schema.ts`, never from `ENCODING.md`. | `src/format/schema.ts` |
| `internal/weather/scriptnum.go` | `appendScriptNum(s *script.Script, n int64) error`: the **minimal-push** opcode branches (`OP_0`, `OP_1NEGATE`, `OP_1`..`OP_16`) plus `interpreter.ScriptNumber.Bytes()` for the magnitude, appended with `script.AppendPushData` (§7.2). Minimal push is kept because **script validity** requires it, not because anything else emits these bytes. | `float-encoder.ts` (number half) |
| `internal/weather/float.go` | `EncodeFloat`, `DecodeFloat`, `ValidateFloatPrecision`. | `src/utils/float-encoder.ts` |
| `internal/weather/encoder.go` | `Encode(WeatherData) (*script.Script, error)`, `EncodeHex`. Typed errors (§7.5). Pure, no I/O. | `src/format/encoder.ts` |
| `internal/weather/decoder.go` | `Decode`, `DecodeHex`, `IsValidScript`. **Exactly ONE layout — the one `encoder.go` writes** (36 chunks, §7.7). No historical-layout tolerance: *the prefix-less legacy fallback and the pre-`e2ae463` field order were removed because the only records in those layouts are unlocatable (§14.1) and nothing else produces them.* | `src/format/decoder.ts` + the read half of `locking-scripts.ts` |
| `internal/weather/testdata/golden/*.json` | The **self-generated** golden files (§7.4) — the encoder's own frozen output, regenerated by `go test ./internal/weather -run TestGolden -update`. *Replaces the deleted `vectors.json`, which was generated from TypeScript.* | **new** |
| `internal/tempest/client.go` | `Stations(ctx)`, `CurrentConditions(ctx,id)`; ctx timeouts, retry+backoff, 1 h station cache, **bounded concurrency** (TS is serial), hardened outbound client. | `src/service/tempest.ts` |
| `internal/tempest/mapper.go` | Response → `WeatherData`. **Sane Go semantics, documented in the file header** (§7.5): a JSON number destined for an integer field is accepted only if integral (a fractional value is a *rejected reading*, not a silently truncated one — the deliberate change from JS `parseInt` truncation, which was only ever mimicked for parity); `''`/`false`/`0` fallbacks for **absent** fields; **reject the whole reading** when a field is present but unparseable. | `mapToWeatherData` |
| `internal/store/store.go` | The persistence seam. `Record`, `Station`, `Stats`, `Status` (exactly the 4 wire literals), and four interfaces (§4.3). Everything above these is datastore-agnostic. | `src/db/models/*` (interfaces extracted) |
| `internal/store/postgres/*.go` | pgx/v5 implementation: `records.go`, `claim.go`, `stations.go`, `stats.go`, `deposits.go`, `preflight.go`, `migrations.sql`. All queries **parameterised**; `QueryExecModeSimpleProtocol` is forbidden. | `src/db/connection.ts` + the query halves of the route files |
| `internal/walletconn/walletconn.go` | Build the storage-server wallet: pooled hardened transport, retry-with-backoff around a `Balance()` probe. | `src/service/wallet.ts` (lazy singleton → injected dependency) |
| `internal/walletconn/timeout.go` | `TimeoutWallet` wrapping **every** call in its own `context.WithTimeout`: `CreateAction`, `ListActions`, `ListOutputs`, `FanOutFuel`, `InternalizeAction`, `GetPublicKey`, `Balance`. | **new** |
| `internal/fuel/fuel.go` | `FromThroughput(mirrored, p.Denomination)` **then the mandatory overrides** (§8.13). Takes a plain `fuel.Params` value struct filled by `main.go`, so it never imports `internal/config`. | **replaces** `src/service/setup.ts` + `src/service/monitor.ts` |
| `internal/fuelmath/window.go` | **Leaf package, imports nothing local**: `MaxOneClaimScriptBytes(d)`, `ClaimsRequired(n,s,d)`, `feeFor(size)` (§8.4). This is what lets `internal/config/validate.go` use it. | **new** |
| `internal/fuel/preflight.go` | The two-shape preflight, error classification, fingerprint cache. | **new** |
| `internal/fuel/supervisor.go` | The `RunOnce` loop: per-round ctx timeout, catch-up, `lastRoundStart`/`lastRoundSuccess`/`lastKeeperMintAt`, stuck-round WARN, **single-flight**. | **new** |
| `internal/funding/address.go` | Derive a BRC-29 self-payment address for a fresh random `(prefix, suffix)` pair; persist it. Logic **copied** from `cmd/throughput_dashboard/internal/funding/address.go` (that package is under another module's `internal/` and cannot be imported). | **new** |
| `internal/funding/internalize.go` | Fetch Atomic BEEF via `services.GetBEEF` + `beef.AtomicBytes`, **resolve the vout by scanning outputs for the expected P2PKH lock**, call `InternalizeAction` with `InternalizeProtocolWalletPayment`. | **new** — successor to `setup-funding.ts` |
| `internal/publisher/publisher.go` | ~60 lines of surviving domain logic: encode N records, partition poison records, one `CreateAction` built exactly as §10.0 specifies, return txid + vouts. A one-method `ActionCreator` interface so it unit-tests with a fake. | `src/service/transaction.ts` (listOutputs / inputBEEF / inputs / preimage all deleted) |
| `internal/publisher/errors.go` | The error classifier: `ClassPermanent` / `ClassDoubleSpend` / `ClassInfra` / `ClassUnknown` (§10.3). **Typed errors only, never string matching.** | `isDoubleSpendError` (`11ecfd5`) generalised |
| `internal/pipeline/poller.go` | Single-flight ticker; immediate first poll + jitter; bounded-concurrent fetch; **honours `ctx.Done` between stations**, not only between polls; per-station error accounting; station upserts. | `src/service/queue.ts` |
| `internal/pipeline/processor.go` | Single-flight ticker: claim → adopt → encode/partition → publish → complete/classify. | `src/service/processor.ts` |
| `internal/pipeline/reaper.go` | Reclaims rows stranded in `processing` past the lease; preserves `claim_ref`, sets `adopt_required`. | `recoverStuckRecords` generalised |
| `internal/pipeline/reconciler.go` | Maps wallet action status onto `completed` rows; writes `block_height` and `stations.last_block_height` (§10.6). | **new** |
| `internal/pipeline/breaker.go` | Circuit breaker: 3 s → 60 s exponential with jitter, opened by infra-class errors. | **new** |
| `internal/api/api.go` | `http.NewServeMux` with Go 1.22 method+path patterns; explicit server timeouts; route registration **order** (health/ready before the limiter, §6.1); the two `http.Server`s. | `src/api/index.ts` |
| `internal/api/{stations,weather,verify,events,proof,health,ops}.go` | One file per endpoint group (§13). | `src/api/routes/*` |
| `internal/api/dto.go` | Wire DTOs. `isoMillis` time type; non-nil `blockchain`; **no `omitempty` on any field in the §13.1 crash list**. | the response shapes in `src/api/routes/*` |
| `internal/api/middleware.go` | Request id, panic recovery, opaque error responses, per-write deadline via `http.NewResponseController`, `MaxBytesReader`, the rate limiters, client-IP extraction (§6). | the two `express-rate-limit` instances (the `cors` middleware is **dropped**, §6.5) |
| `internal/api/sse.go` | The SSE hub: bounded client set, named `stats_update` events, per-client 30 s `:ping`, `http.Flusher` per write, `ctx.Done` cleanup, global concurrency semaphore. | `src/service/sse.ts` + `src/api/routes/events.ts` |
| `internal/verify/verify.go` | The WhatsOnChain confirmation path: 64-hex gate, batch cap, `errgroup.SetLimit(8)`, hardened client, transactional `block_height` persistence. | `src/api/routes/verify.ts` |
| `internal/proof/proof.go` + `cache.go` | Store-gated BEEF lookup: 64-hex validation → **store existence check** → `services.GetBEEF` → `beef.AtomicBytes(txidHash)`. Bounded LRU, TTLs, `singleflight`, per-IP limit, WoC 429 backoff. | `src/api/routes/proof.ts` |
| `internal/obs/sampler.go` | The 60 s heartbeat line and the `/api/ops` snapshot. | replaces `src/notification/*` + the 60 s `console.log` in `app.ts:89-99` |
| `internal/ratelimit/` | Bounded-key sliding-window limiter + client-IP extraction with a trusted-CIDR allowlist (§6.1). | **new** |
| `.golangci.json`, `Makefile`, `Dockerfile`, `docker-compose.yaml`, `.github/workflows/{build,go}.yml`, `frontend/.dockerignore`, `frontend/nginx.conf` (static-only rewrite) | Toolbox `.golangci.json` verbatim with the `gci` prefix changed; two-stage `golang:1.26-alpine` → `gcr.io/distroless/static-debian12`, `CGO_ENABLED=0`, `USER 65532`, `EXPOSE 3001`; compose swaps mongo for `postgres:17-alpine` and **drops the `setup` profile**; Makefile loses `mongo-shell` and gains `build test lint up down migrate` — *no `parity` / `parity-regen` targets: they invoked a TypeScript oracle that no longer exists (§7.0). Golden files are regenerated by `go test … -update`, never by make.* | `Dockerfile`, `docker-compose.yaml`, `Makefile`, `jest.config.js` |

### 4.2 `migrations.sql` (run at startup, idempotent)

```sql
-- ---------- weather records: the durable queue and the record table ----------
CREATE TABLE IF NOT EXISTS weather_records (
  id               text        PRIMARY KEY,            -- uuidv7, path-safe, opaque
  station_id       bigint      NOT NULL,
  timestamp        timestamptz NOT NULL,               -- reading timestamp (TS parity: poll instant)
  observation_time timestamptz NOT NULL,               -- dedupe key (§10.8)
  data             jsonb       NOT NULL,
  status           text        NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','processing','completed','failed')),
  attempts         int         NOT NULL DEFAULT 0,
  claim_ref        uuid,                               -- batch label, survives reaping (§10.2)
  adopt_required   boolean     NOT NULL DEFAULT false,
  claimed_at       timestamptz,
  txid             text,
  output_index     int,
  block_height     bigint,                             -- written by verify (§13.6) + reconciler
  chain_status     text,                               -- arc-accepted | unmined | mined | aborted
  mined_at         timestamptz,
  error            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  processed_at     timestamptz
);

-- Structural duplicate prevention (§10.8). NOT nullable, NOT partial.
CREATE UNIQUE INDEX IF NOT EXISTS ux_records_station_obs
  ON weather_records (station_id, observation_time);

CREATE INDEX IF NOT EXISTS ix_records_status_created ON weather_records (status, created_at);
CREATE INDEX IF NOT EXISTS ix_records_station_created
  ON weather_records (station_id, created_at DESC, id DESC);   -- the station-page list sort
CREATE INDEX IF NOT EXISTS ix_records_list ON weather_records (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS ix_records_txid ON weather_records (txid) WHERE txid IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_records_reconcile ON weather_records (processed_at)
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
  last_temp         double precision,                  -- NULLABLE, and the API never OMITS it
  last_conditions   text        NOT NULL DEFAULT '',
  last_block_height bigint,
  search_tsv        tsvector    GENERATED ALWAYS AS (
                      to_tsvector('english',
                        coalesce(name,'') || ' ' || coalesce(location,''))) STORED,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_stations_search ON stations USING gin (search_tsv);
CREATE INDEX IF NOT EXISTS ix_stations_active ON stations (is_active);

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
  suffix          text PRIMARY KEY,          -- base64, random 16 bytes
  prefix          text        NOT NULL,      -- base64, random 16 bytes
  address         text        NOT NULL,
  locking_script  text        NOT NULL,      -- hex, used for vout resolution
  created_at      timestamptz NOT NULL DEFAULT now(),
  txid            text,
  vout            int,
  satoshis        bigint,
  internalized_at timestamptz
);

-- ---------- preflight fingerprint cache ----------
CREATE TABLE IF NOT EXISTS app_preflight (
  fingerprint text PRIMARY KEY,   -- sha256(D | FanoutOutputsPerTx | ChunkFeeHeadroom | url | net)
  ok_at       timestamptz NOT NULL
);
```

Six record indexes, each justified: unique dedupe, queue scan, station history (with the `created_at DESC` order the station page requires), global list pagination (with the `id` tiebreaker that makes offset pagination stable under batch inserts), proof/verify gating, reconciler scan. Mongoose's three redundant single-field indexes are dropped.

**Stats semantics, fixing the two HEAD bugs of §2.2.** In the same Postgres transaction as `Complete`:

```
app_stats.total_tx          += 1                     -- ONE per CreateAction, not per record
app_stats.total_records     += len(batch)
app_stats.last_record_write  = greatest(last_record_write, now())
stations.tx_records         += (records for that station)
stations.last_reading / last_temp / last_conditions  <- from the newest record in the batch
```

`activeStations` is **not** stored: it is `SELECT count(*) FROM stations WHERE is_active` (~20 rows, free), so it can never be stuck at 0. `totalDataPoints = total_records × 33`. `weather stats-recompute` rebuilds `app_stats` from `weather_records` (`count(DISTINCT txid)`, `count(*) FILTER (WHERE status='completed')`, `max(processed_at)`) plus `stations.tx_records`, so drift is repairable without a migration.

### 4.3 Store interfaces

| Interface | Methods |
|---|---|
| `RecordStore` | `Insert`, `ClaimPending`, `Complete`, `FailPermanent`, `RequeueInfra`, `MarkUnknown`, `ReapExpired`, `List`, `ListByStation`, `Get`, `Requeue`, `SetBlockHeights`, `ReconcileCandidates`, `Snapshot` |
| `StationStore` | `Upsert`, `List` (page + search), `Get`, `Stats` |
| `DepositStore` | `NewDeposit`, `PendingDeposits`, `MarkInternalized` |
| `PreflightStore` | `PreflightOK(fingerprint) (bool, error)`, `RecordPreflight(fingerprint) error` |

There is **no `StampRef`**: the claim `UPDATE … RETURNING` stamps `claim_ref` itself (§10.2), so a separate method would have no caller.

---

## 5. What is deleted — evidence-based, unit by unit

~470 lines disappear. **Each row states what the code at `5cfea93` actually does — including the three upstream funding fixes earlier research never read — and then either why the throughput mechanism genuinely subsumes it, or exactly what the Go port must reimplement.** Where subsumption cannot be supported, it is not claimed.

### 5.0 The single most important finding in this section

> **The funding-UTXO double-spend that `ea0c654` and `11ecfd5` wrestled with was doing unintended duty as the only cross-pod serialiser of RECORD processing.**
>
> `src/service/processor.ts:76-96` does `WeatherRecord.find({status:'pending'}).sort({createdAt:1}).limit(100)` and **then** a separate `bulkWrite` setting `status='processing'`. There is no atomic claim, no `findOneAndUpdate`, no version guard, no lease. Two overlapping pods therefore **select the same pending records**. Today that is survivable only because both pods then try to spend from the same `funding` basket: the loser's `createAction` fails with `WERR_REVIEW_ACTIONS`/`doubleSpend`, its batch rolls back to `pending`, and exactly one on-chain write happens. `11ecfd5` made that collision **quiet**; it did not remove it — and by making it quiet it **hid the duplicate-claim bug underneath**.
>
> **Under server-side throughput funding the app no longer selects funding UTXOs, so that collision disappears.** Two overlapping pods would both call `CreateAction` with only data outputs, the server would fund each from **different** fuel UTXOs, and **both would succeed**: the same weather readings written to chain twice, both transactions paid for, and the DB row's txid whichever write lands last — the other transaction orphaned and invisible.
>
> Two consequences, both mandatory:
> 1. **The atomic claim (§10.2) is not a nicety and not a fix for a race upstream already solved. It is the sole replacement for a protection that currently exists by accident.** A `SELECT`-then-`UPDATE` Go port would be **strictly worse** than the TypeScript.
> 2. **`replicas: 1` + `strategy: Recreate` is load-bearing**, and the FuelKeeper's single-flight problem relocates into the keeper intact (row 4).

### 5.1 The deletion table

| # | Deleted | ~Lines | What the code at `5cfea93` does | Verdict |
|---|---|--:|---|---|
| 1 | **`src/service/processor.ts`'s claim half** (`find` + `bulkWrite`) | ~25 | Non-atomic two-step claim; no lease, no guard. Overlapping pods claim the same rows and are serialised only by funding-UTXO collision. | **NOT subsumed — replaced by something strictly stronger, and mandatory.** `internal/store/postgres/claim.go`: `UPDATE … SET status='processing', claimed_at=now(), claim_ref=COALESCE(claim_ref, gen_random_uuid()) WHERE id IN (SELECT id FROM weather_records WHERE status='pending' ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING *`. See §5.0. |
| 2 | `src/service/setup.ts` in full (`createFundingOutputs`, `getFundingOutputCount`, `ensureFundingOutputs`) | ~85 | Mints `FUNDING_BATCH_SIZE=1000` hash-puzzle outputs of `FUNDING_OUTPUT_AMOUNT=1000` sat per refill = **1 000 000 sat per refill transaction**. | **Subsumed.** This *is* the fan-out the FuelKeeper performs (`default` → 6 000-sat reserve chunks → 50-sat fuel leaves). |
| 3 | `src/service/monitor.ts` in full (`checkFundingBasket`, `startMonitoringLoop`) | ~77 | 60 s `setInterval`, no overlap guard, no jitter. The whole low-water/refill decision. | **Subsumed** by `keeper.RunOnce()` on our own supervised ticker — **plus** the single-flight guard the `setInterval` lacked (§3.1). |
| 4 | `queueFundingAction`'s single-flight (`src/service/wallet.ts:25-33`, added by **`ea0c654`**) | 6 | Not a queue: `if (fundingAction !== null) return fundingAction as Promise<T>` hands a concurrent caller the **already-running** promise and never invokes its `fn`. A JavaScript module-level singleton in one Node process — no OS mutex, no advisory lock, no DB lock, **nothing cross-pod**. The race it fixed was refill **amplification**: `monitor.checkFundingBasket` (60 s) and `processor.checkAndRefillFunding` (pre-flight) both call `createFundingOutputs(1000)`; under the old serial chain a burst of N triggers spent **N million satoshis** and created N,000 UTXOs. | **NOT subsumed — relocated, and it must get stronger.** The same two-trigger shape exists in the keeper (a ticker plus a pre-flight top-up on an empty pool), so `internal/fuel/supervisor.go` needs a single-flight guard or it reintroduces exactly the fund-drain amplification `ea0c654` fixed. **And a per-process guard is insufficient here in a way it was not for the app:** two pods each fanning out from the same shared `default` basket collide on the same default UTXO — the original `ea0c654`/`11ecfd5` failure reproduced *inside* the keeper. **Decision: single-replica deployment with `strategy: Recreate`** (maxSurge 0, so a rolling deploy never overlaps). A Postgres advisory lock (`pg_try_advisory_lock` on a fixed key, released on ctx cancel, holder pod name logged) is the stated upgrade path if the API is ever scaled for read traffic; **not implemented in v1**, and the invariant is written into both the manifest and `supervisor.go`. **Also carry `ea0c654`'s own bug forward as an explicit rule:** `setup.ts:80-95` unconditionally does `cachedFundingCount += count` and logs `Successfully created ${count} funding outputs in transaction: ${txid}` **even when the call was coalesced and its `createAction` never ran** — inflating the in-memory gauge by 1 000 and emitting a false success log carrying the *other* call's txid. It self-heals at the next 60 s `getFundingOutputCount`. **The Go keeper's de-duplicated top-up request must not apply the winner's accounting twice, and must not log a mint it did not perform.** |
| 5 | `getFundingOutputCount`'s gauge (fixed by **`cc5baef`**) | 3 | Before the fix, `const { outputs } = await wallet.listOutputs({basket:'funding'})` with **no `limit`**. The SDK's `validateListOutputsArgs` defaults `limit` to **10** (`validationHelpers.js:881`), so `outputs.length` could never exceed 10, was always below `FUNDING_BASKET_MIN=200`, and `checkFundingBasket` therefore **refilled unconditionally on every 60 s tick: 1 000 000 sat/minute (~1.44 BSV/day)** of self-minted UTXOs plus fees, while the basket grew without bound. `cc5baef` reads `outputs.totalOutputs` instead. | **Subsumed, but the lesson is a hard rule.** The Go keeper and sampler must measure the pool with a **true total** (`ListOutputs(...).TotalOutputs`, or a COUNT) and **never a page length**; and **a low-water check that can never be satisfied silently drains the wallet at the loop interval.** A unit test asserts the gauge is not a `len()`. |
| 6 | `src/scripts/hash-puzzle.ts` and every trace of the `funding` basket, `customInstructions` preimages, and the `'20'+preimage` unlocking script | ~89 | A per-output random preimage created inside the per-output loop; the preimage lives **only** in the old storage server's `customInstructions`. | **Subsumed.** Under throughput funding the server's funder claims exact-denomination P2PKH fuel signed with the operator's key. There is no application-visible locking or unlocking script. |
| 7 | `src/scripts/setup-funding.ts`, `npm run setup`, the Makefile `setup` target, the compose `setup` service/profile | ~91 | Pre-mints a fixed 1 000-UTXO inventory; `SETUP_OUTPUT_COUNT` with a TTY prompt; `BSV_NETWORK=main` pinned in the compose profile next to the committed key. | **Subsumed by the continuous keeper — except for one thing.** Its **only** successor is the operator deposit path of **§9**, a different and much smaller component, and it is a named deliverable, not an afterthought. |
| 8 | `src/service/transaction.ts`'s funding half: the `listOutputs` round-trip, `inputBEEF`, the `inputs` array, the `customInstructions` guards, `"No funding outputs available"` | ~40 | Selects one funding UTXO, builds the unlocking script, passes explicit inputs. | **Subsumed.** `createWeatherTransaction` collapses to encode-then-`CreateAction`; the **absence** of `Inputs`/`InputBEEF` is precisely what puts the action on the server's funding path. |
| 9 | The `doubleSpend` retry loop (`src/service/transaction.ts:18-33,105-121`, added by **`11ecfd5`**) | ~35 | `MAX_DOUBLE_SPEND_RETRIES = 5`; `isDoubleSpendError` = `error.name === 'WERR_REVIEW_ACTIONS'` **and** `error.reviewActionResults` contains `status === 'doubleSpend'`. Re-runs `listOutputs({basket:'funding', include:'entire transactions', limit:1, includeCustomInstructions:true})` on every attempt, relying on the documented side effect that a failed `createAction` marks the offending input spent internally so the next `listOutputs` returns a different UTXO. Non-doubleSpend errors rethrow immediately. **The commit message names the production failure verbatim: during a rolling deploy two pods run simultaneously, both spend the same funding UTXOs, and the surviving pod then loops on stale UTXOs failing every 3 s** with log spam and zero progress. | **PARTIALLY subsumed. The error class does NOT vanish and the classifier must survive.** The app no longer *selects* inputs, so the app-selected double-spend is gone — but **the server's funder can still hand out a fuel UTXO another actor already spent** (two FuelKeepers, or a funder race). `internal/publisher/errors.go` must recognise the same shape — `WERR_REVIEW_ACTIONS` with `reviewActionResults[].status == "doubleSpend"` — as `ClassDoubleSpend` and **retry with fresh fuel, up to 5 attempts, without advancing `attempts`**. The retry moves from app-selected inputs to server-selected fuel; it does not disappear. |
| 10 | Env vars `FUNDING_OUTPUT_AMOUNT`, `FUNDING_BASKET_MIN`, `FUNDING_BATCH_SIZE`, `MONITOR_INTERVAL`, `SETUP_OUTPUT_COUNT` and their `validateConfig` rules | ~15 | Size the hand-rolled inventory. The deployed manifest ships a **misspelled `FUNDING_BACKET_MIN: 10`**, so the real floor has silently been the 200 default for months. | **Subsumed — but they must be REPLACED, not merely dropped.** New keeper knobs (`DENOMINATION_SATOSHIS`, `FUEL_TARGET_POOL_SIZE`, `FUEL_INTERVAL`, `FANOUT_OUTPUTS_PER_TX`, `FUEL_FANOUT_MAX_TXS_PER_ROUND`) are exposed as validated env and shipped in `app-configmap.yaml`, **or the app is unoperable in production.** The misspelling defect dies with the variable — and is the archetype of the bug an explicit validated config table prevents. **That table is §8.15**, one row per env var plus 20 numbered validation rules; a key not in it is not read, and a key in it that is absent is either defaulted or a startup error, never silently ignored. |
| 11 | `src/notification/*` in full (interface, Console, Twilio, 9 call sites) | ~120 | Four call sites are funding bookkeeping and die with it. Twilio is **never instantiated** (`app.ts:69` hardcodes `ConsoleNotification`; no env var selects an implementation; its constructor throws if any of four `TWILIO_*` vars is missing, and all default `''`). | **Subsumed with one hard condition.** `sendError('CRITICAL: Insufficient funds in wallet')` is **the only out-of-band signal an operator gets that the wallet is empty**, and it exists on two paths. It must survive as a metric/alert (§11.3 alarms 2, 3 and 5) **shipping in the same PR**. If those alarms slip, a minimal in-process WARN→webhook path stays in the Go port. |
| 12 | `src/scripts/locking-scripts.ts:createWeatherDataLockingScript`, `processor.ts:processAllPending`, `transaction.ts:createWeatherTransactionBatch`, `src/index.ts` | ~60 | The first is a one-line duplicate of `encodeToHex`; the next two are exported but never called; the barrel becomes the exported identifier set of `internal/weather`. | **Subsumed.** |
| 13 | The "unknown status" tolerance and the 60 s stats `console.log` (`app.ts:89-99`) | ~20 | Free-form status handling; a bare console line. | **Subsumed.** Status becomes a closed Go enum projected onto exactly the four wire literals (§13.8); stats become one SQL query exposed as structured log fields, `/api/ops` and the SSE stream. |
| 14 | Both committed private keys, in all six locations | 6 | `src/config/env.ts:11`, `src/service/wallet.ts:4`, `.env.example:2`, `QUICKSTART.md:41`, `docker-compose.yaml:39` and `:97` (key A); `.env.docker:6` (key B, mainnet). `validateConfig` does **not** check the key, so a missing env var silently runs on a publicly known key. | **Subsumed.** The Go config has **no default key** and fails closed. Both keys are **compromised**; neither may be reused. `.dockerignore` gains `.env*` wholesale and nothing `COPY`s an `.env*` into any image (§6.6). |
| 15 | The unbounded, untracked retry semantics | — | On any transaction failure the whole batch is `bulkWrite`n back to `pending` with the error string stored, and the next 3 s tick re-selects the same rows (`createdAt` ASC, limit 100) **forever**. No attempts counter, no backoff, no dead-letter, no jitter. **`'failed'` is never written by any code path** (grep: only `weather-record.ts:7,45`, `weather.ts:25`, `queue.ts:170` reference it), so the frontend's Failed filter can never match. **A permanently poisonous record sits at the head of the queue and blocks every subsequent record indefinitely.** If the rollback itself fails, rows stay `processing` and are rescued only by `recoverStuckRecords()` at the next startup. | **NOT subsumed — must be reimplemented.** An `attempts` counter, error classification where **only permanent errors consume the budget** (§10.3), terminal `failed` at 3 attempts, a circuit breaker, poison-record isolation (§10.7), a lease reaper, and `weather requeue` (§10.5). **`failed` finally becomes reachable**, which makes the frontend's existing Failed filter meaningful. |
| 16 | `MONGO_URI`, the mongoose `__v` field | — | `MONGO_URI` is required by `validateConfig`. **No unique index exists anywhere in the Mongo schema**, so nothing prevents duplicate `(stationId, timestamp)` rows today. | **Subsumed and improved.** `POSTGRES_*` replaces it; `UNIQUE (station_id, observation_time)` prevents the duplicates Mongo allowed (§10.8). |
| 17 | The mongoose transaction in `POST /api/verify` (`verify.ts:84-101`) | ~18 | `mongoose.startSession()` + `session.withTransaction(...)` on the `blockHeight` write. **MongoDB transactions require a replica set or mongos**; docker-compose runs a single `mongo:8.0` with no `--replSet` and no keyfile, so this path throws `IllegalOperation` and the route returns **500** — meaning `blockHeight` is never persisted in that deployment shape and `useAutoVerify` silently fails on every page. | **Subsumed and an improvement.** Postgres has real transactions. Two consequences: (a) **do not assume any existing `blockHeight` data exists anywhere** — and note that no Mongo dump can be taken either, because the cluster is gone (§14.1); (b) record this as an improvement, not a regression. |
| 18 | The `GET /api/stations?search=0` 500 (`stations.ts:29-50`) | — | The filter branch does `const asNum = parseInt(search,10); if (!isNaN(asNum)) filter.stationId = asNum; else filter.$text = {$search: search}`, but the **sort** branch is chosen independently with `search && !parseInt(search,10) ? {score:{$meta:'textScore'}} : {stationId:1}`. For `search='0'`, `parseInt` is `0` which is **falsy**, so the sort takes the textScore branch while the filter has no `$text` clause — Mongo rejects a `$meta:'textScore'` sort without a text query. Any string parsing to numeric zero (`'0'`, `'00'`, `'0abc'`) reproduces it. | **Not ported.** The Go handler derives filter **and** sort from **one** parsed value (§13.3). |
| 19 | All of `tests/` (7 jest files) and `jest.config.js`; everything under `src/` **except** the files retained by row 20 | — | The service-layer suite tests code that no longer exists (its coverage was zero anyway); the format suite tests the TypeScript encoder, which the Go encoder no longer has to agree with (§7.0). The Go encoder's own coverage is §17.2 tests 1–5. | **Subsumed.** |
| 20 | **RETAINED IN PLACE — not relocated, not pinned, not deleted by this port** | — | — | `src/format/{types,constants,schema,encoder,decoder}.ts` and `src/utils/float-encoder.ts` **stay exactly where they are.** They are the human-readable authority for the **33-field list and its order**, which the Go port keeps unchanged (§7.1), and `git log --follow` provenance for it. Nothing in the running system imports them; they are excluded from the Docker build and from Go CI lint. Deleting them is a **later, separate, non-blocking cleanup** once §7.4's golden files are committed — no Go test or build step depends on them at any point. **REMOVED from this row and deliberately not reinstated:** the `git mv` into `parity/ts/`, the cut-down `parity/ts/package.json`, the exact `@bsv/sdk` `1.10.3` pin and the committed `parity/ts/package-lock.json`. All four existed **only** to keep a byte-parity oracle regenerable; with parity released (§7.0) they are pure maintenance burden, and a pinned oracle nothing consults is worse than none — it invites someone to "restore the contract" later. |

### 5.2 Rows an earlier draft contained that are deleted outright

| Earlier claim | Why it is deleted |
|---|---|
| "Delete `src/scripts/prove-data-lock.ts` (38 lines, breaks `npm run build`)" | **The file does not exist at `HEAD`.** `git ls-tree HEAD src/scripts/` returns exactly three blobs: `hash-puzzle.ts`, `locking-scripts.ts`, `setup-funding.ts`; the file appears only in a dropped local commit and a stash. `npx tsc --noEmit` at `HEAD` exits **0**. The parity-generation caveat about excluding it, and the Makefile hack, go with it — and with the whole parity-generation step (§7.0). The carried-forward `docs/specs/prove-data-lock.md` is **also dropped** — it has no source in the tree being ported. |
| "`package-lock.json` survives, moved to `parity/ts/`" | **Doubly wrong now.** It does not exist in the repo and is gitignored; and **there is no `parity/ts/` at all** — the byte-parity oracle is deleted (§7.0, row 20). The only lockfile this port creates and un-ignores is **`frontend/package-lock.json`**, for supply-chain reasons (§6.6). |
| "The TS encoder must be retained as a pinned golden oracle, and the vectors generated from it before any deletion" | **The constraint it served is gone.** Nothing outside this backend decodes the weather script — `frontend/src/services/verify.ts` is the only `@bsv/sdk` importer in the SPA and it never reads the `OP_RETURN` payload (§7.0, §12.4). The goldens are now self-generated from the Go encoder (§7.4), so no ordering dependency exists between the Go work and any TypeScript change. |
| "`FOR UPDATE SKIP LOCKED` structurally removes races upstream still has" | Overstated in one direction and understated in another. Upstream **still has** the non-atomic claim; what upstream added (`11ecfd5`) is a retry that **masks its symptom**. And the Go claim is not an improvement over a broken guard — it is the **replacement for an accidental one** (§5.0). |
| "the funding machinery is deleted because the keeper/server does this now" | True of the **mechanics**, false of the **guarantees**. Rows 1, 4, 5, 9, 11 and 15 state the behaviour the port must reimplement. |

---

## 6. SECURITY PARITY — every control the TypeScript has at `5cfea93`, and its required Go equivalent

**This section is a hard requirement.** Commit `5cfea93` ("Fix CodeQL security alerts") exists precisely because GitHub's code scanning found real defects. All four of its measures — plus the controls that predate it and the residual gaps it did **not** close — must be present in the Go port, or the port regresses against a commit whose whole purpose was to close published security alerts.

**What `5cfea93` actually contains** (5 files, +34/−4), so nobody describes it as more than it is: a top-level `permissions: contents: read` in `build.yml`; `express-rate-limit ^7.5.0`; two rate limiters in `src/api/index.ts`; `encodeURIComponent` on the txid in `verify.ts`; a status allowlist + `stationId` `isNaN` guard in `weather.ts`. In two of the four cases the alert was already mitigated by pre-existing validation.

### 6.0 The acceptance checklist

Paste this into the PR as a regression gate. Every line is **MUST**.

| # | Control |
|---|---|
| 1 | Per-IP sliding-window limiter on the `/api` prefix, keyed on the **real client IP**, with a **bounded** key map; 429 + `Retry-After` + `RateLimit-*` headers. **`CF-Connecting-IP` and `X-Forwarded-For` are consulted ONLY when `r.RemoteAddr` is inside `TRUSTED_PROXY_CIDRS`**; otherwise the key is `r.RemoteAddr`. An unset list trusts nothing and logs a WARN (§6.1) |
| 2 | A separate stricter limiter on `/api/events` that does **not** also decrement the global bucket, plus a **hard cap on concurrent SSE connections** |
| 3 | `/api/health` and `/api/ready` registered **before** the limiter (or explicitly skipped) |
| 4 | 64-hex regexp gate on every txid in `POST /api/verify` and `GET /api/proof/{txid}`, with 400 on failure, **before any I/O** |
| 5 | Hard cap of **100** txids per verify request, plus `errgroup.SetLimit(8)` on the outbound fan-out and a whole-batch deadline |
| 6 | Every outbound `http.Client` has `Timeout`, `CheckRedirect: http.ErrUseLastResponse`, and `io.LimitReader` on every body; every request uses `http.NewRequestWithContext` |
| 7 | WoC/Tempest base URLs are compile-time consts selected by config, never by request data |
| 8 | Status allowlist and integer parsing with explicit error branches; `page`/`limit` clamped with **no NaN-equivalent path** |
| 9 | All pgx queries parameterised; **no `QueryExecModeSimpleProtocol`**; no value interpolation; an `ORDER BY` allowlist the moment server-side sorting is introduced |
| 10 | `websearch_to_tsquery` (never `to_tsquery`) for station search |
| 11 | `http.MaxBytesReader` on POST bodies; `ReadHeaderTimeout` set on every `http.Server` |
| 12 | Opaque error responses with a request id; `*pgconn.PgError` **never** echoed |
| 13 | No compiled-in default for `SERVER_PRIVATE_KEY` or `POSTGRES_PASSWORD` — fail fast, and redact in logs |
| 14 | No `.env*` in any image; `.dockerignore` covers `.env*` in **both** build contexts |
| 15 | `go.sum` committed; `GOFLAGS=-mod=readonly`; distroless non-root final image |
| 16 | Explicit top-level `permissions: contents: read` in every workflow, write scopes only on the publishing job, actions pinned to SHAs, CodeQL enabled for **`go`**, `govulncheck` and `gitleaks` in CI |
| 17 | `POST /api/verify` remains a write path where the caller chooses only **which rows refresh**, never the `blockHeight` **value** |
| 18 | CORS middleware **removed** together with `CORS_ORIGIN` (§6.5) — never `*`, never a reflected `Origin`, and `Allow-Credentials` never set |
| 19 | `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` on every API response, and a CSP on the frontend nginx (§6.6). The TS has **none** — this is a beyond-parity control, and it is in this gate so it cannot quietly not ship |
| 20 | Because `WriteTimeout` must be **0** for SSE, **every non-SSE handler** carries a per-response write deadline via `http.NewResponseController` (§6.7). The SSE handler explicitly does not, and relies on `r.Context().Done()` |
| 21 | `Validate()` enforces **all 20 numbered config rules of §8.15** and returns every failure, not the first; no error string echoes a configured value |

### 6.1 Rate limiting — port the configuration, **not** the implementation, and fix the keying

**TypeScript at HEAD** (`src/api/index.ts:24-42`), two `express-rate-limit` v7 instances, both `standardHeaders: true, legacyHeaders: false`, default in-process `MemoryStore`, 429 on exceed:

| Mount | Window | Max | Body |
|---|---|---|---|
| `app.use('/api', apiLimiter)` | 60 s | 100 | `{error:'Too many requests, please try again later'}` |
| `app.use('/api/events', sseLimiter)` | 60 s | 10 | `{error:'Too many SSE connections, please try again later'}` |

Three defects the Go port must **not** reproduce:

1. **No trust-proxy config → one global bucket.** `app.set('trust proxy', …)` appears nowhere in `src/` and no custom `keyGenerator` is set, so `req.ip` is the immediate TCP peer = the cloudflared/ingress pod. **The "per IP" limits are global**: 100 req/min and ~10 SSE starts/min for the entire internet. `express-rate-limit` v7 also emits an `ERR_ERL_UNEXPECTED_X_FORWARDED_FOR` validation error when XFF is present but trust proxy is unset, which is presumably being logged and ignored today.
2. **Mount order double-charges SSE.** `/api` is registered first, so an `/api/events` request consumes a slot in **both** buckets — and `EventSource` auto-reconnects on error, so once the 429 fires the client retries in a loop, each retry consuming budget again.
3. **`/api/health` sits inside the rate-limited prefix** (`app.use('/api', …)` at `:32`, `app.get('/api/health', …)` at `:45`). Combined with (1), kubelet probes share the single global bucket, so a traffic spike can push liveness/readiness into 429 and **get the pod killed**.

**Required Go implementation** — `internal/ratelimit` + `internal/api/middleware.go`:

**The peer gate comes first, and it is the whole control.** Every forwarding header is client-settable *unless* something upstream provably overwrote it, so no header may be consulted until the **immediate peer** is known to be one of our proxies. `CF-Connecting-IP` is "not settable by the client" **only for traffic that actually traversed Cloudflare**; an in-cluster caller, a direct hit on Traefik, or any future non-proxied path can set it freely. Trusting it unconditionally would be the same defect as trusting `XFF[0]` — a limiter bypass (a fresh key per request) *and* the unbounded-key memory DoS — just spelled with a different header name.

```go
// clientIP resolves the real client address.
//
// STEP 0 (mandatory, and the reason this function is safe at all): is the IMMEDIATE
// peer one of ours? If net.ParseIP(host(r.RemoteAddr)) is NOT inside any
// TRUSTED_PROXY_CIDRS entry, return r.RemoteAddr and consult NO header. Both
// CF-Connecting-IP and X-Forwarded-For are client-settable on any path that did not
// traverse our proxies, so an untrusted peer gets exactly one key: its own address.
//
// Only when the peer IS trusted, in this order and NOTHING else:
//   1. CF-Connecting-IP  — set by Cloudflare and preserved through cloudflared. Trusted
//                          ONLY because step 0 established the request came from
//                          Cloudflare/cloudflared, which overwrites it.
//   2. X-Forwarded-For, walked from the RIGHT, returning the first hop that is NOT in
//      TRUSTED_PROXY_CIDRS
//   3. r.RemoteAddr
//
// NEVER take X-Forwarded-For[0] blindly: that is both a limiter bypass (spoof a new IP
// per request) and the memory-DoS vector for the key map. Do NOT use a generic realip
// middleware that trusts the whole XFF chain.
//
// FAIL CLOSED: an unset or unparseable TRUSTED_PROXY_CIDRS trusts NOTHING, so every
// request keys on r.RemoteAddr. That degrades to §6.1 defect 1 (one global bucket per
// proxy pod) but it is safe and, crucially, LOUD — see the boot log below.
```

**`TRUSTED_PROXY_CIDRS` is load-bearing in both directions, so it cannot be allowed to be silently empty.** With it empty the rightmost XFF hop is the proxy pod itself and the algorithm degenerates back to defect 1 / §2.3 bug 2 — the single global bucket this section exists to remove — with no error anywhere. Therefore:

- `Validate()` parses every entry as a CIDR and rejects malformed ones (rule 17).
- When the list is **empty**, boot emits a single `WARN` naming the variable, the ConfigMap path and the consequence (`"TRUSTED_PROXY_CIDRS empty: forwarding headers ignored, rate limiting is per-proxy-pod and therefore effectively global"`), and the heartbeat carries `trustedProxyCIDRs: 0` so alarm 10 has a companion signal. It is a WARN, not a fatal, because failing closed still serves traffic correctly — but it is never invisible.
- Shipped value and refresh owner are in §15.3.

| Scope | Limit | Notes |
|---|---|---|
| `/api/*` general | **600 req/min per client IP**, burst 120 — the scope therefore advertises and enforces **`RateLimit-Limit: 720`** (see the correction below the table) | Raised deliberately from 100. Derivation: one `/explorer` visit is 1 SSE + 2 stations pages + **two prefetch requests per hovered row across up to 50 rows** = ~103 requests, so a 100/min ceiling is tripped by a *single engaged user*. 600 covers a legitimate session with margin while still bounding one abusive IP. |
| `POST /api/verify` | **60 req/min per client IP**, on top of the general limiter | The expensive endpoint (outbound fan-out). The frontend needs at most one per page view. |
| `GET /api/events` new streams | **30/min per client IP** | Own sub-router; **must not** decrement the general bucket. React StrictMode mounts twice in dev, i.e. two connections per tab. |
| `GET /api/events` concurrent | **12 per client IP**, plus a **global semaphore of 500** → `503` when full | The TypeScript has **no** concurrency cap at all (`sse.ts:17` is an unbounded `Set` and each client pins a Response, a 30 s interval timer and a socket), so its 10/min rate limit is the only thing between the app and unbounded goroutine/socket growth. |
| `GET /api/proof/{txid}` | `PROOF_RATE_LIMIT_PER_MIN`, default **60** per client IP | Unauthenticated outbound proxy. |
| `/api/health`, `/api/ready`, `/api/ops` | **Exempt** | Registered before the limiter middleware. Fixes defect (3). |

Implementation: `github.com/go-chi/httprate` (`httprate.Limit(600, time.Minute, httprate.WithKeyFuncs(clientIPKey), httprate.WithLimitHandler(json429))`) is the closest semantic match to `express-rate-limit`'s fixed window. A stdlib-only alternative is `golang.org/x/time/rate` with a mutex-guarded `map[string]*rate.Limiter` **plus eviction** (TTL janitor or an LRU capped at 10 k keys) — the unbounded-map memory DoS is the standard mistake and is worse in Go than Node because a spoofable key source lets an attacker mint unlimited entries. Note `x/time/rate` is a token bucket and smooths rather than reproducing the fixed-window burst-then-block shape.

**Correction (Plan B2, Task 15 ruled, Task 22 recorded): the general scope advertises and enforces `RateLimit-Limit: 720`, not 600.** Burst is permanent capacity rather than a start-of-window allowance, so the implementation's `Decision.Limit` is `limit + burst` (600 + 120) and the **721st** request in a window is the first 429. This was ruled deliberately; the documents state what the code does.

Emit `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset` and `Retry-After`. **Two tests, not one** (§17.2 test 13): the limiter key is not the proxy address when `CF-Connecting-IP` arrives from a **trusted** peer, **and** `CF-Connecting-IP` from an **untrusted** peer is ignored entirely so a header-spoofing client cannot mint a second key.

**Where else limiting belongs.** Keep the app-level limiter **and** add a coarse edge rule, because they see different things. Inside the cluster, nginx-ingress/Traefik `limit-rps`-style annotations would key on the cloudflared pod's source IP — the same broken key — so ingress-level per-IP limiting is **useless here**. **Cloudflare is the only layer with the true client IP**: add a WAF Rate Limiting Rule scoped to the weather-proof hostname (e.g. 600 req/min per IP on `/api/*`, 10 s block); confirm the plan tier first (§20 item 3). **The in-process limiter is required regardless** — it is the only place that can enforce the SSE concurrency cap and the per-request txid cap.

**Frontend interaction to be aware of:** the SPA has **no 429 handling**. A 429 on `POST /api/verify` surfaces as a red "Verification error" line, and in `useAutoVerify` it **un-marks `attempted`, so it retries on the next render** — a 429 there can become a retry loop. That is a second, independent reason the general limit is 600 and not 100.

### 6.2 SSRF — keep both layers; validation outranks escaping

**The actual vulnerable outbound path was `POST /api/verify` → WhatsOnChain**, not Tempest. Before `5cfea93`: `fetch(\`${base}/tx/${txid}\`)` by raw template interpolation of a request-body value. The fix was `encodeURIComponent(txid)`.

Two precise points:

1. **The real defence already existed and is stronger than the fix.** `TXID_RE = /^[a-fA-F0-9]{64}$/` at `verify.ts:13` is applied to every element at `:43-46` with a 400 on failure, so no path-traversal or host-switching payload could ever reach the fetch. `encodeURIComponent` silences the CodeQL taint path; it is belt-and-braces.
2. **The host is not user-controlled.** `WOC_BASE` is a hardcoded two-entry map at `verify.ts:8-11` selected by `config.BSV_NETWORK` with a `'test'` fallback at `:49`.

`GET /api/proof/:txid` was **already** guarded (`proof.ts:19-22`: the same regex + 400 before `services.getBeefForTxid`, with the network taken from config at `:8`) and `5cfea93` did not touch it.

**Go requirements:**

- Package-level `var txidRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)`; reject with **400 before any I/O**. **Do not let "we escape it" become the reason to drop the regexp.**
- Base URLs as untyped string **consts** selected by `BSV_NETWORK`, never assembled from request data. Note the upstream field name is lowercase **`blockheight`**, and the TS reads `data.blockheight && data.blockheight > 0 ? data.blockheight : null`.
- Build paths with `url.JoinPath` / `url.PathEscape`, never `fmt.Sprintf` on a raw value.
- Audit that any go-sdk / wallet-toolbox `Services` constructor takes the network from **config**, never from the request.

**Constrain the outbound client — there is no TypeScript equivalent to port, and a naive Go port is strictly worse.** TS uses `AbortSignal.timeout(15000)` in `tempest.ts:4,59,96` but **plain unbounded `fetch`** in `verify.ts:55`. **Go's `http.DefaultClient` has no timeout at all.** Every outbound client (WoC, Tempest, storage server) must be a package-level `*http.Client` with:

| Setting | Value / rule |
|---|---|
| `Timeout` | 10–15 s (matching `tempest.ts`'s 15 s) |
| `Transport` | `MaxIdleConnsPerHost`, `TLSHandshakeTimeout`, `ResponseHeaderTimeout` all set |
| `CheckRedirect` | `return http.ErrUseLastResponse` — so a 302 from a hijacked or compromised upstream cannot pivot to `169.254.169.254` (IMDS), the Postgres service, or the storage server's admin surface |
| Body | wrapped in `io.LimitReader` (1 MiB for a WoC tx, larger for BEEF) **before** `json.Decode`, so a huge upstream response cannot OOM the pod |
| Request | `http.NewRequestWithContext` everywhere, so the API request's context cancels the outbound call when the client disconnects |
| DialContext | **no** custom resolver for user-supplied hosts — there are none and there must never be |

### 6.3 Outbound amplification — the gap `5cfea93` did NOT close, and the more dangerous half

`verify.ts:38-46` validates only that `txids` is a non-empty array of 64-hex strings — **no length cap** — then `:52-63` does `Promise.allSettled(txids.map(fetch WoC))` with **no concurrency limit**. `express.json()`'s default body limit is 100 kB, i.e. roughly **1 300–1 470 txids** in one accepted request, each producing one outbound WhatsOnChain request. At the surviving 100 req/min budget that is on the order of **147 000 outbound requests per minute** from one pod: a DoS against a third-party API, an easy way to get the cluster egress IP rate-limited or banned, and local socket/FD exhaustion.

**Go requirements (a new control, not a port):**

| Control | Value | Why that value |
|---|---|---|
| `http.MaxBytesReader(w, r.Body, 64<<10)` → 413 | 64 KiB | Ample for 100 txids (68 bytes each). Go has no `express.json()` default. |
| `json.NewDecoder(...).DisallowUnknownFields()` | — | Unexpected keys rejected rather than silently ignored. |
| Hard cap `len(txids) <= 100` → 400 | **100** | The frontend never needs more: `useAutoVerify` sends the unconfirmed subset of one records page and `StationRecords.tsx:11` sets `LIMIT = 20`; `useVerification` sends exactly 1. The weather list `limit` is itself clamped to ≤100. |
| `errgroup` with `g.SetLimit(8)` | 8 | Bounded outbound concurrency. |
| Whole-batch `context.WithTimeout` | 20 s | Plus 10 s per request. |

### 6.4 Injection and input validation

**What `5cfea93` fixed (CodeQL High).** Before: `if (req.query.status) filter.status = req.query.status`. Because Express's default query parser is `qs` in *extended* mode, `req.query.status` can be an **object**, so `?status[$ne]=x` produced `filter.status = {$ne:'x'}` — turning an equality filter into a Mongo operator and enabling `$regex` CPU exhaustion. The fix is an explicit allowlist `VALID_STATUSES = ['pending','processing','completed','failed']` with `includes()` (an object fails `includes`, so the key is **silently dropped** — HTTP 200 with unfiltered results, **not** a 400). The same commit guarded `stationId` with `parseInt` + `!isNaN`.

**How this maps onto Postgres + pgx.** The injection **class** is eliminated: pgx/v5 uses the extended query protocol, so values are never interpolated into SQL text. **The validation requirement is not eliminated** — port the allowlist for *input-contract* reasons (return 400 on garbage instead of silently unfiltered results, which is a wrong answer rather than an error), and because it is also the enum the frontend's status projection depends on.

The four places the port will be tempted to build SQL dynamically:

| Site | Rule |
|---|---|
| Optional filters | Use `($1::text IS NULL OR status = $1)` / `($2::bigint IS NULL OR station_id = $2)`, or a named-args builder. **Never** `fmt.Sprintf` a value in. If a WHERE clause is assembled, append only **fixed literal fragments** and push every value into the args slice. |
| `ORDER BY` | Postgres cannot parameterise an identifier — `ORDER BY $1` silently sorts by a constant. **Not needed today**: all sorting in the redesigned frontend is client-side within the current page (`Dashboard.tsx:196-211`, `StationRecords.tsx`, and `SortableTh` is presentation-only), the API accepts no sort param, and the server sorts are **fixed literals**. If server-side sort is ever added it becomes the codebase's first identifier-interpolation site and needs a `map[string]string` allowlist mapping an opaque key to a hardcoded `column ASC`/`column DESC` fragment, with a default branch. |
| `LIMIT`/`OFFSET` | Safe as bind params; still clamp them (below), and cap `page` so `OFFSET` stays under ~100 000 (a huge OFFSET is a cheap CPU DoS). |
| Search | **`websearch_to_tsquery('english', $1)`**, never `to_tsquery`. `to_tsquery` **raises a syntax error** on arbitrary input (unbalanced quotes, bare `&`, `\|`, `!`, `:`), which pgx surfaces as a query error and the handler turns into a **500 on every search-box keystroke**. `websearch_to_tsquery` is defined never to error on arbitrary input. The generated `search_tsv` + GIN index of §4.2 backs it, mirroring `StationSchema.index({name:'text', location:'text'})`. (`ILIKE '%' || $1 || '%'` with the wildcards concatenated **inside** the SQL text is the alternative, but a leading wildcard cannot use a btree index and would need `pg_trgm`.) Preserve the numeric shortcut: if the search parses as an integer, do an exact `station_id = $1` lookup instead. |

**The NaN-unsafe clamp must not be reproduced.** Both TS list endpoints use `Math.max(1, parseInt(req.query.page ?? '1', 10))` and `Math.min(100, Math.max(1, parseInt(...)))`. **`Math.max`/`Math.min` propagate NaN** (verified: `node -e 'Math.max(1,parseInt("abc",10))'` prints `NaN`), so `?limit=abc` yields `page=NaN, limit=NaN, skip=NaN`, which flow into `.skip()`/`.limit()` and into the response body as `"page": null`. In Go: `strconv.Atoi` and **branch on `err`** — on error use the default, never propagate — then clamp with explicit `if`s or a `clampInt(v,min,max)` helper.

**Complete user-controlled input inventory and its required Go gate:**

| Input | TS gate at HEAD | Required Go gate |
|---|---|---|
| `GET /api/weather?page` | `max(1, parseInt(…'1'))`, NaN-unsafe | `Atoi` + err branch → default 1; clamp ≥ 1 |
| `GET /api/weather?limit` | `min(100, max(1, …'20'))`, NaN-unsafe | clamp `[1,100]`, default 20 |
| `GET /api/weather?status` | allowlist, **silently dropped** | allowlist of the four literals; **400** on an unknown value |
| `GET /api/weather?stationId` | `parseInt` + `!isNaN` | `Atoi` + err → ignore; range-checked |
| `GET /api/weather/:id` | **none** — a raw ObjectId string to `findById`; an invalid id throws a `CastError` caught into a **500**, not a 404 | Parse explicitly (`uuid.Parse`) → **400**; `pgx.ErrNoRows` → **404**. Never let a parse failure become a 500 |
| `GET /api/stations?page`/`limit` | clamps (limit ≤ 200, default 50) | same numbers, NaN-safe |
| `GET /api/stations?search` | `.trim()` only — and `req.query.search?.trim()` **throws a TypeError on `?search[a]=1`**, which the catch turns into a 500 | `r.URL.Query().Get()` always returns a string, so the type confusion vanishes. Trim; numeric → exact id; else `websearch_to_tsquery` |
| `GET /api/stations/:stationId` | `parseInt` + `isNaN` → 400 | same, **plus** a range check that it fits the column type: `strconv.Atoi` accepts values that overflow an `int32` and would otherwise produce a Postgres `22003` → 500 |
| `GET /api/proof/:txid` | 64-hex regex + 400 | same |
| `POST /api/verify` body | array-ness + non-empty + per-element 64-hex + 400; **no length cap** | same + the cap and body limits of §6.3 |
| `GET /api/events` | none | none needed |

**There is no authentication or authorisation on any endpoint, and `POST /api/verify` is an unauthenticated WRITE path** (it `UPDATE`s `blockHeight` on rows matching a caller-supplied txid, `verify.ts:91-95`). It is bounded because the update no-ops when no row matches **and the written value comes from WhatsOnChain, not the caller**. The Go port must preserve exactly that property: **the caller controls only which existing rows get refreshed, never the value written.** If a rewrite ever lets the caller supply `blockHeight`, that becomes trivial data forgery on a proof-of-existence demo.

**Irrelevant in Go — do not port these, so nobody assumes the class is handled:** the entire Mongo operator-injection class (`$ne`/`$gt`/`$regex`/`$where`/`$text` smuggling) has no analogue; Express's `qs` extended-parser type-confusion hazard vanishes because `r.URL.Query().Get()` always returns a string; `mongoose.startSession`/`withTransaction` is replaced by `pgx` `Begin`/`Commit`/`Rollback` with `defer` (the **atomicity requirement** transfers, the mechanism does not); `encodeURIComponent` is redundant once the regexp gate is enforced.

### 6.5 CORS — removed, deliberately

**TypeScript:** `app.use(cors({origin: config.CORS_ORIGIN, credentials: true}))`, a **single** origin string, defaulting to `http://localhost:5173`, with `credentials: true` even though the app has no cookies, sessions or auth headers.

**Decision: delete the CORS middleware and the `CORS_ORIGIN` env var.** One line of why: **both dev and prod are already same-origin**, so a middleware nothing exercises is pure liability — it is the thing someone later sets to `*`.

- **Production** is same-origin by construction: `API_BASE = import.meta.env.VITE_API_URL || ''` in **both** consumers (`services/api.ts:3`, `hooks/useLiveStats.ts:5`), and the Ingress path-splits `/api` to the backend on the **same hostname** (§15.2).
- **Local dev is same-origin too:** `frontend/vite.config.ts:6-13` already declares `server.proxy['/api'] → http://localhost:3001`.
- `credentials: true` with a reflected origin is the classic escalation and buys nothing here.
- If the deployed `CORS_ORIGIN` were ever unset, the TS silently falls back to `localhost:5173` and the browser blocks the real site — a failure mode that disappears with the variable. (Commit `6b40836` flipped that default to a legacy host and broke local dev; the revert put it back. §15.1.)

**Documented re-add path** (one line in `docs/RUNBOOK.md`): a future cross-origin consumer adds `github.com/rs/cors` with `AllowedOrigins` from an env **allowlist**, `AllowedMethods {GET, POST, OPTIONS}`, and **`AllowCredentials: false`**. Never `{"*"}`, never a reflected `Origin`.

### 6.6 Secrets, images and supply chain

| Item | TS at HEAD | Go requirement |
|---|---|---|
| `SERVER_PRIVATE_KEY` | Defaults to a literal hex key compiled into the app; `validateConfig` does not check it; the same key is in five other files; `src/service/wallet.ts:4` re-reads it from `process.env` with its own duplicate default | **No default.** `Validate()` returns a startup error listing every missing secret so the pod `CrashLoopBackOff`s rather than running on a publicly known key. `config.Secret` type (§12.5). Continue pulling from SSM `/apps/weather-chain/SERVER_PRIVATE_KEY` |
| `POSTGRES_PASSWORD` | n/a | **No default**; SSM (§0 P3); redacted in logs |
| `.env*` in images | `Dockerfile:45` does `COPY .env.example ./.env.example`; `.dockerignore` excludes `.env` but not `.env.example`, so the published back image **ships a private key** | **`COPY` no `.env*` file.** `.dockerignore` covers `.env*` wholesale. **And there is no `frontend/.dockerignore` at all** — Docker resolves `.dockerignore` relative to the build context, and the frontend context is `./frontend` (`docker-compose.yaml:72`, `build.yml:59`), so the repo-root file does not apply. `docker build ./frontend` currently copies `frontend/.env` (which contains `VITE_API_URL=http://localhost:3001`), the host's darwin/arm64 `node_modules` **after** `npm install` (clobbering the container install), and a stale February `dist`. **Concrete proof the leak is real:** the stale prebuilt `frontend/dist/assets/index-DfIBVyIr.js` in the working tree contains the literal `http://localhost:3001` (1 occurrence). CI escapes it only because a fresh checkout has none of those files. **Add `frontend/.dockerignore` with `node_modules/`, `dist/`, `.env*`, `.DS_Store`** |
| Dependency pinning | **No lockfile committed anywhere**; all deps are caret ranges; both Dockerfiles use `npm install` | `go.mod` **and** `go.sum` committed; `GOFLAGS=-mod=readonly`; `go mod download` against the committed `go.sum` in the builder stage; `go mod verify` and a CI check that `go mod tidy` produces no diff; the `toolchain` directive pinned; the builder base image pinned **by digest**. Never bypass `sum.golang.org`. **And fix the surviving JS in the same PR:** un-ignore and commit **`frontend/package-lock.json`**, and switch `frontend/Dockerfile` to `npm ci`. Pinning the frontend's `@bsv/sdk` to an exact version stays worthwhile **for supply-chain reasons** — it is live code on the verification path (`verify.ts`) — but it is **no longer an encoder requirement**: there is no `parity/ts/` lockfile and no version-pinned oracle (§7.0) |
| Error leakage | Route handlers are clean (generic messages, detail logged server-side; `proof.ts:41-45` even reclassifies an upstream "not found" into a 404) but the **global handler at `src/api/index.ts:57-63` returns `{error:'Internal server error', message: err.message}`** — echoing raw error text, which for a Mongo/driver/wallet error can disclose connection strings, hostnames, collection names or file paths | A recover-and-respond middleware that logs the error + stack with `slog` at error level **including a correlation id**, and returns only `{"error":"internal server error","request_id":"…"}`. **Never `fmt` the err into the body.** For pgx: never return `err.Error()` on a `*pgconn.PgError` (it contains SQL text, column and constraint names). Classify: `pgx.ErrNoRows`→404, `23505`→409, `22P02`→400, else 500 opaque. **Keep** the good "not mined yet"→404 reclassification, since the frontend depends on it |
| Container hardening | Backend is good: multi-stage, `dumb-init` as ENTRYPOINT, explicit non-root user 1001. **Frontend runs `serve` as ROOT with no `USER` directive** (`frontend/Dockerfile:22-31`) | Backend: `gcr.io/distroless/static-debian12` final stage, `CGO_ENABLED=0`, static binary, `USER 65532:65532`, `EXPOSE 3001` (the port it actually binds). No `dumb-init` needed — a Go binary is a well-behaved PID 1 provided it installs `signal.NotifyContext` for SIGTERM/SIGINT (mirroring `src/app.ts:166-174`). Frontend: `nginxinc/nginx-unprivileged`, non-root (§15.5). Manifest `securityContext`: `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`, **plus resource limits** (currently absent everywhere, and `DOCKER.md` already flags it) |
| Security headers | **None** — no helmet, no CSP, no HSTS | Add `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` on API responses. A CSP belongs on the **frontend** nginx; note `frontend/index.html` loads DM Sans + Outfit from `fonts.googleapis.com`/`fonts.gstatic.com`, so a strict CSP needs those hosts or self-hosted fonts |

### 6.7 Server timeouts and the SSE exception

Go's `http.Server` has no useful defaults; `ReadHeaderTimeout` in particular is the Slowloris defence Go lacks.

**Correction (Plan B2, Task 21 measured, Task 22 recorded): `gosec` G112 does NOT flag a missing `ReadHeaderTimeout` on its own.** It fires only when a server composite literal carries **neither** `ReadHeaderTimeout` **nor** `ReadTimeout`. Since every server in this design sets `ReadTimeout`, dropping `ReadHeaderTimeout` leaves lint completely green. The only gate on this field is the runtime test `TestReadHeaderTimeoutIsSetOnBothServers` — this paragraph previously asserted the opposite, and anything that relied on the linter here was uncovered.

| Setting | API server (`API_PORT`) | Ops server (`OPS_PORT`) |
|---|---|---|
| `ReadHeaderTimeout` | **10 s** | 10 s |
| `ReadTimeout` | 15 s | 15 s |
| `WriteTimeout` | **0 (disabled)** — required for SSE | 15 s |
| `IdleTimeout` | 120 s | 120 s |
| `MaxHeaderBytes` | 16 KiB | 16 KiB |

Because `WriteTimeout` must be 0 for the never-ending SSE response, **every non-SSE handler is wrapped in middleware that sets a per-response write deadline** via `http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30*time.Second))`. That is one small middleware instead of a third `http.Server`, and it is directly testable. The SSE handler explicitly does **not** set a write deadline; it relies on `r.Context().Done()` and the 30 s heartbeat.

### 6.8 GitHub workflow permissions and CI scanning

**What `5cfea93` tightened:** a top-level `permissions:\n  contents: read` block in `.github/workflows/build.yml` between `on:` and `jobs:`. The `build-and-push` job already carried a job-level `{contents: read, packages: write}`; `get_tag` now inherits read-only. That is the whole of the workflow hardening — **preserve it verbatim.** Declaring any `permissions` block sets every unlisted scope to none, so no `id-token: none` line is needed.

**Gaps to close beyond parity:**

| Gap | Fix |
|---|---|
| Actions pinned to floating major tags (`actions/checkout@v4`, `docker/login-action@v3`, `docker/build-push-action@v5`) | Pin to full 40-char SHAs with a `# v4.2.2` trailing comment; let Dependabot bump them. Bump build-push-action to v6. |
| **Code scanning has no `go` language.** Scanning is enabled via GitHub **default setup** (there is no `codeql.yml`; `.github/workflows` contains only `build.yml` plus templates), state `configured`, languages `[actions, javascript, javascript-typescript, typescript]`. **If the backend becomes Go and nothing changes, the Go code is scanned by nothing at all** — a silent regression against a commit whose whole purpose was closing scanner alerts | Add `go` to the default-setup language list (keeping `javascript-typescript` for `frontend/` and `actions` for the workflows), or switch to an advanced CodeQL workflow whose job carries `permissions: {contents: read, security-events: write, actions: read}` and nothing else |
| No dependency-vulnerability scanning | `govulncheck ./...` in CI — CodeQL's Go query pack will not flag dependency CVEs |
| No secret scanning in CI | `gitleaks` (or trufflehog), so a key literal cannot be committed again |
| No Go build/lint/test workflow | New `.github/workflows/go.yml` (§15.8) |
| Both build steps in `build.yml` are named "Build and push frontend Docker image" — one of them builds the **backend** | Fix. A copy-paste label like that is how the wrong context gets edited. |
| The org's default `GITHUB_TOKEN` permission may already be read-only | Unknowable from the repo (it is an org setting). Carry the explicit block regardless — relying on an org default is not a control you can see in review. |

---

## 7. Encoder specification — a deterministic INTERNAL format

### 7.0 Why this is not a byte-parity problem

**The weather script encoding is consumed by nothing outside this backend.** That is the fact the whole of §7 now rests on, and it is verified, not assumed:

| Evidence | Verification |
|---|---|
| **The SPA's only BSV consumer never reads the payload** | `grep -rln '@bsv/sdk' frontend/src/` returns **exactly one file**: `frontend/src/services/verify.ts`. That file does exactly three things — `Transaction.fromHexBEEF(beefHex)`, assert `tx.merklePath` is present, and `await tx.verify(chainTracker)` against a `WhatsOnChain` tracker. It **never** calls a decoder, never inspects `tx.outputs`, never touches an `OP_RETURN` chunk. |
| **Weather values reach the UI as JSON, not as script** | Every rendered field comes from the API's `data` object (§13.2, §13.5). The script is never parsed client-side — which §12.4 already records as a known weakness of the *verification story*, and which here is what makes the encoding internal. |
| **The backend is the only decoder** | `internal/weather/decoder.go` exists solely so the app can read back what it wrote (`GET /api/proof`, reconciliation, the §14.4 chain-rebuild procedure). It reads **one** layout: the one `encoder.go` writes. |

So the encoder has **no external contract to match.** Its requirements are:

| # | Requirement | Enforced by |
|---|---|---|
| a | **A valid script**: `OP_FALSE OP_RETURN`, a version byte, then the 33 fields. **Minimal push-data encoding** — kept because script validity requires it, not for parity (§7.2) | §17.2 test 1 |
| b | **A fixed, documented field order** — the existing alphabetical 33-field schema, unchanged. The wire format is **not** redesigned; that option was considered and declined (§7.1) | §17.2 test 1 |
| c | **A hard 297-byte cap.** This is money, not style: 297 B is the top of the contiguous one-claim fuel window at D=50, so one byte more silently costs a **second fuel claim per record** (§7.6, §8.3) | §17.2 test 3 |
| d | **Encoder/decoder round-trip**, because the app's own proof and verification paths read the script back (§7.7) | §17.2 test 4 |
| e | **Determinism** — identical input, identical bytes, always. No map iteration in the encode path, no wall clock, no randomness (§7.4) | §17.2 tests 1, 2 |
| f | **Self-generated golden files**, so an accidental change to field order or number encoding fails loudly instead of quietly writing a different record shape (§7.4) | §17.1 |

**Byte-parity with the historical TypeScript encoder is NOT a requirement.** Stated flatly so that nobody reinstates the machinery: there is no correct historical byte string to match, because the only consumer that could have cared does not read the bytes. A symmetric encoder+decoder change is *harmless* here — it produces a different-but-self-consistent record — which is precisely why §7.4's goldens are a **change detector**, not a correctness oracle.

**What was dropped, and why — the audit trail.** An earlier draft of this section treated byte-exactness with the TypeScript at `5cfea93` as the highest-risk part of the port, and built five mechanisms to guarantee and prove it: (1) `@bsv/sdk` pinned to exactly `1.10.3` with a committed lockfile as a golden oracle; (2) the TS encoder `git mv`d into a frozen `parity/ts/` tree so the vectors stayed regenerable; (3) `internal/weather/testdata/vectors.json` **generated from that TypeScript**, with `make parity` / `make parity-regen` targets, an `oracleDigest` anchor and a CI `git diff --exit-code` job; (4) a hand-rolled `appendScriptNum` reproducing `@bsv/sdk`'s `Script.writeBn` bit-for-bit and an FMA-safe `jsRound` emulating ECMAScript's half-up-toward-+∞ rounding; (5) decoder support for **three** historical on-chain layouts, including a `FieldSchemaV0` transcription of the pre-`e2ae463` time-first field order, plus a human-gated task to pull a real on-chain script off WhatsOnChain as a fixture. **All five are removed.** Two facts killed them. First, the frontend evidence above: nothing outside this backend ever decoded the script, so parity bought nothing. Second, the historical records are **unlocatable** — the MongoDB Atlas cluster that held their txids no longer resolves (§14.1), so there is no set of old records for a multi-layout decoder to read and no on-chain fixture to fetch. Retaining a version-pinned oracle that nothing consults would have been worse than deleting it: it reads as an obligation and invites a future engineer to "restore the contract" and re-inherit JavaScript's numeric semantics for no reason.

**What the freedom is spent on:** `go-sdk`'s own helpers instead of JavaScript emulation (§7.2), and Go's own rounding with the rule written down (§7.3).

### 7.1 The format

```
OP_FALSE OP_RETURN            00 6a          (2 bytes, added in commit c44b7ae)
version                       51             (OP_1 — an OPCODE, not a data push)
33 fields in FieldSchema order (alphabetical, air_density -> wind_gust)
```

**The 33-field schema and its alphabetical order are KEPT, unchanged.** Redesigning the wire format was considered and **declined** — the field list is verified against `src/format/schema.ts`, it works, and a redesign would spend risk for nothing. `FieldSchema` is a 33-entry **ordered slice** in `internal/weather/schema.go`; **order is the wire format**, and it is ported from `schema.ts`, never from `ENCODING.md` (whose Field Order section is factually wrong — §19).

Measured against the repo's own `tests/fixtures/weather-samples.ts` `sampleWeatherData`: script = **99 bytes**. Serialised output = 8 (value) + 1 (varint scriptlen) + 99 = **108 bytes**. All weather outputs carry `satoshis: 0` and no basket, and the `OP_FALSE OP_RETURN` prefix makes them provably unspendable, so they add no UTXO to any basket. *(Sizes are a property of the field schema and of minimal push encoding, so they carry over unchanged; the specific historical hex string is **not** a normative target — §7.4's Go-generated goldens are the record of what this app writes.)*

Per field type. `appendScriptNum` is §7.2; `AppendPushData` is `go-sdk`'s own — signature and behaviour verified at `github.com/bsv-blockchain/go-sdk v1.3.2`:

| Type | Emission |
|---|---|
| `integer` | `appendScriptNum(s, int64(v))` |
| `float` | `appendScriptNum(s, scaleFloat(v))` — ×1e6 then rounded per §7.3 |
| `string` | `s.AppendPushData([]byte(str))` — **`go-sdk`'s `PushDataPrefix` already emits exactly the minimal form** (verified empirically at v1.3.2): `0x00` for empty, a 1-byte length opcode `0x01..0x4b` for 1..75 bytes, `OP_PUSHDATA1 (0x4c) <len:1>` for 76..255, `OP_PUSHDATA2 (0x4d) <len:2 LE>` for 256..65535. Above 65535 it emits `OP_PUSHDATA4`, so **our** code returns `ErrStringTooLong` before calling it — a cheap defensive guard that the 297-byte cap (§7.6) makes unreachable in practice |
| `boolean` | `appendScriptNum(s, 0)` / `appendScriptNum(s, 1)` → `OP_0` / `OP_1` |

### 7.2 `appendScriptNum` — minimal push, using `go-sdk`'s own helpers

**Do not hand-roll the little-endian sign-magnitude serialisation, and do not emulate `@bsv/sdk`'s `Script.writeBn`.** `go-sdk` already implements exactly that encoding in `script/interpreter`. Four branches, in this order — the first three exist because **minimal push encoding is a script-validity rule**, and only the fourth needs a serialiser:

```go
import (
    "github.com/bsv-blockchain/go-sdk/script"
    "github.com/bsv-blockchain/go-sdk/script/interpreter"
)

func appendScriptNum(s *script.Script, n int64) error {
    if n > maxScriptInt || n < -maxScriptInt {          // maxScriptInt = 1<<53 - 1
        return fmt.Errorf("%w: %d", ErrNumberOutOfRange, n)
    }
    switch {
    case n == 0:
        return s.AppendOpcodes(script.Op0)              // 0x00
    case n == -1:
        return s.AppendOpcodes(script.Op1NEGATE)        // 0x4f
    case n >= 1 && n <= 16:
        return s.AppendOpcodes(script.Op1 + byte(n) - 1) // 0x51..0x60
    default:
        // Fresh value per call: Bytes() mutates its receiver (see below).
        sn := &interpreter.ScriptNumber{Val: big.NewInt(n), AfterGenesis: true}
        return s.AppendPushData(sn.Bytes())
    }
}
```

**Real signatures, read from `/Users/personal/go/pkg/mod/github.com/bsv-blockchain/go-sdk@v1.3.2/script/` — not guessed:**

| Symbol | Signature | Note |
|---|---|---|
| `script.AppendPushData` | `func (s *Script) AppendPushData(d []byte) error` | Delegates to `EncodePushDatas` → `PushDataPrefix`; minimal for all lengths ≤ 0xFFFF |
| `script.AppendOpcodes` | `func (s *Script) AppendOpcodes(oo ...uint8) error` | **Rejects** `OpDATA1..OpPUSHDATA4` (`0x01..0x4e`) with `ErrInvalidOpcodeType` — so it cannot be used for pushes, only for the opcode branches |
| `script.PushDataPrefix` | `func PushDataPrefix(data []byte) ([]byte, error)` | Exported; the minimal-prefix rule of §7.1 |
| `script.EncodePushDatas` | `func EncodePushDatas(parts [][]byte) ([]byte, error)` | — |
| `interpreter.ScriptNumber` | `type ScriptNumber struct { Val *big.Int; AfterGenesis bool }` | Fields are **exported**; construct directly |
| `interpreter.MakeScriptNumber` | `func MakeScriptNumber(bb []byte, scriptNumLen int, requireMinimal, afterGenesis bool) (*ScriptNumber, error)` | The decode direction (§7.7) |
| `(*ScriptNumber).Bytes` | `func (n *ScriptNumber) Bytes() []byte` | Little-endian with a sign bit — exactly the encoding required |
| `(*ScriptNumber).Set` | `func (n *ScriptNumber) Set(i int64) *ScriptNumber` | — |
| `script.Op0` / `OpZERO` / `OpFALSE` | `byte = 0x00` | Three names, same value |
| `script.Op1NEGATE` | `byte = 0x4f` | — |
| `script.Op1` / `OpONE` … `script.Op16` | `byte = 0x51 … 0x60` | — |
| `script.OpRETURN` | `byte = 0x6a` | — |

**Three verified gotchas, each of which would be a silent defect:**

1. **`Bytes()` mutates its receiver on negative values.** It does `if isNegative { n.Neg() }` and never restores the sign. Measured: a `ScriptNumber` holding `-128` returns `8080` on the first `Bytes()` call and **`8000` on the second**, with `Val` left at `+128`. **Always construct a fresh `ScriptNumber` per call** — never cache one, never `Set()` and reuse across fields.
2. **`AfterGenesis` is a capacity hint only, at v1.3.2 — do not rely on that.** The `!AfterGenesis` branch clamps to `MaxInt32`/`MinInt32`, but the clamped slice feeds only `make([]byte, 0, len(bb)+1)`; the encoding loop re-reads `n.Val`. Measured: `2^53−1` yields `ffffffffffff1f` with `AfterGenesis` either `true` or `false`. It is still set **explicitly `true`**, because the intent is full-width values and a future SDK release could make that branch load-bearing.
3. **`AppendBigInt` is the wrong function.** `func (s *Script) AppendBigInt(bInt big.Int) error` is literally `AppendPushData(bInt.Bytes())` — big-endian magnitude, **no sign byte**. Measured: `AppendBigInt(-128)` emits `0180`, i.e. it encodes `−128` as `+128`. Undecodable. Never use it.

Verified output of the branches above (`go-sdk v1.3.2`): `0 → OP_0`, `-1 → OP_1NEGATE`, `1..16 → 0x51..0x60`, `17 → 0111`, `127 → 017f`, `128 → 028000`, `-128 → 028080`, `2147483647 → 04ffffff7f`, `2147483648 → 050000008000`, `9007199254740991 → 07ffffffffffff1f`.

### 7.3 Float scaling — choose sane Go semantics and DOCUMENT the rule

The two `float` fields are scaled ×1e6 and stored as integers. **The ECMAScript `Math.round` emulation is removed:** it existed only so the rounding matched JavaScript's half-up-toward-+∞ behaviour on exact negative halves, which mattered only for parity.

**The rule, chosen and documented — write it verbatim into a comment above the function:**

> **Scaled floats are rounded half away from zero, using Go's `math.Round`.** `-1.5e-6` therefore encodes as `-2`, not `-1`. This is Go's native and least surprising behaviour, it is symmetric about zero, and it is deliberately *not* JavaScript's `Math.round` (which rounds half toward +∞ and would give `-1`). Nothing outside this backend decodes these values (§7.0), so no external consumer can observe the difference; the golden files of §7.4 pin it against accidental change.

```go
// scaleFloat converts a physical value to its on-chain integer representation.
// Rounding: half away from zero (math.Round). See the rule above.
func scaleFloat(v float64) (int64, error) {
    if math.IsNaN(v) || math.IsInf(v, 0) {
        return 0, fmt.Errorf("%w: %v", ErrNonFinite, v)      // §7.5
    }
    scaled := math.Round(v * FloatScale)
    if scaled > maxScriptInt || scaled < -maxScriptInt {
        return 0, fmt.Errorf("%w: %v", ErrNumberOutOfRange, v)
    }
    return int64(scaled), nil
}
```

Two details that are **not** parity concerns and survive on their own merits:

- **The non-finite guard is mandatory.** `int64(math.NaN())` is implementation-defined in Go, so one junk Tempest value would otherwise commit arbitrary bytes to mainnet with no error at all. Reject before converting (§7.5).
- **The range check precedes the conversion**, so `appendScriptNum`'s own `maxScriptInt` bound is never reached by way of an overflowing float.

*Removed with the JS emulation:* the `jsRound` helper, the FMA-defeating `float64(scaled * float64(FloatScale))` dance, and the pinned divergence vectors (`-1.2345675`, `-0.0000005`, `-1.5e-6`, `-2.5e-6`) that existed purely to prove agreement with ECMAScript. **Keep those four values as a rounding-rule table test** (§17.2 test 5) — same inputs, but asserted against the Go rule above rather than against JavaScript.

### 7.4 The golden-file contract: `internal/weather/testdata/golden/`

The goldens are **self-generated from this Go encoder**. They are a **change detector**, not a correctness oracle: their job is to make an accidental edit to field order, number encoding or the rounding rule fail loudly in CI, so that such a change is always a deliberate, reviewed one. There is no external byte string to agree with (§7.0).

| Rule | Detail |
|---|---|
| **Provenance, and the reproducibility trap** | The file records `formatVersion` and `schemaFieldCount` and **nothing else**. It must contain **no wall-clock timestamp and no git revision.** Both were tried and both break regenerate-and-diff: a `generatedAt` differs between two runs seconds apart, and `git rev-parse HEAD` is stable within a run but changes on the very next commit — so every subsequent commit would fail the diff — and it differs between a full clone and a shallow CI clone. **A golden file must be a pure function of the code under test.** |
| **Regeneration is explicit and Go-native** | `go test ./internal/weather -run TestGolden -update` rewrites the files behind an `-update` flag registered with `flag.Bool`. Default `go test` **verifies** and fails on any difference. There is **no `make parity` / `make parity-regen`**: those drove a TypeScript oracle that no longer exists (§7.0). |
| **CI cannot be satisfied by regenerating** | `go.yml` runs the plain `go test ./...` (which verifies) **and** a `git diff --exit-code internal/weather/testdata/golden/` step, so a drifting encoder fails CI rather than a test file being quietly rewritten (§15.8). |
| **Determinism is asserted, not assumed** | A test encodes the same record 100 times and asserts identical bytes each time, and the encode path is reviewed for the three determinism hazards: **map iteration** (forbidden — `FieldSchema` is an ordered slice, never a map), **wall-clock reads**, and **randomness**. |
| No TypeScript in the loop | Nothing in the generation path shells out to `node`, `tsc` or `npm`, so no ordering dependency exists between the Go tests and any TypeScript change. *(Consequently the earlier draft's "generate the vectors BEFORE any TypeScript deletion lands" constraint is deleted — it no longer means anything.)* |

Contents:

1. **Encoder goldens** for a fixed set of records, each stored as `{name, data, scriptHex, scriptLen}`: the **36 B** all-zero floor, the **99 B** real Tempest sample, the repo's own **211 B** `extremeWeatherData` fixture, an all-negative-values record (exercising the `OP_1NEGATE` and sign-bit branches), and a **297 B** record sitting exactly on the cap.
2. **The `appendScriptNum` table**: `0 → 00`, `-1 → 4f`, `1..16 → 51..60`, `17 → 0111`, `127 → 017f`, `128 → 028000`, `-128 → 028080`, `2147483647 → 04ffffff7f`, `2147483648 → 050000008000`, `9007199254740991 → 07ffffffffffff1f`, plus the two out-of-range cases that must **error**.
3. **The push-length boundary table**: byte lengths 0, 1, 75, 76, 255, 256 — the `PushDataPrefix` transitions verified in §7.2.
4. **The rounding-rule table** of §7.3, asserted against the documented Go rule.
5. **Negative cases** (§7.5): NaN, ±Inf, a non-integral value in an integer field, `|n| > 2^53−1`.
6. **Round-trip coverage** (§7.7): every golden record decodes back to a value deeply equal to its input, plus a property test over randomly generated in-range records. **The round trip is the correctness test; the goldens are the change detector.** Both are required — a symmetric encoder+decoder edit round-trips perfectly and only the goldens catch it.

### 7.5 Error semantics — stated and enforced on their own merits

The distinction below is **not** a parity artefact and survives unchanged: **an absent field is a defaulted field; a present-but-garbage field is a rejected reading.** The reason is data integrity, not TypeScript. A port that defaulted everything to zero would silently publish a record asserting `air_temperature = 0` for a station whose sensor returned `"n/a"` — a plausible-looking lie written to mainnet forever. Enforce it in **two** places:

| Layer | Rule |
|---|---|
| `internal/tempest/mapper.go` | **Absent** field (`undefined`/`null`/key missing) → the documented fallback: `0`, `""`, `false`. **Present but unparseable** (NaN, ±Inf, a non-numeric string in a numeric field, a **non-integral number in an integer field**, a non-string in a string field) → **reject the whole reading**: do not insert the row, log WARN with station id and field name, count it as `stationsRejected`. |
| `internal/weather/encoder.go` | Return a **typed error** (`ErrNonFinite`, `ErrNonIntegral`, `ErrNumberOutOfRange`, `ErrStringTooLong`, `ErrScriptTooLarge`) on non-finite floats, non-integral values in integer fields, `\|n\| > 2^53−1`, and a script over the cap. Never write approximate bytes, and never return a script the cap forbids. `ErrStringTooLong` fires only above the 65535-byte push limit; a shorter-but-oversized string is caught by the §7.6 cap and surfaces as `ErrScriptTooLarge` → `script_too_large` on the row. |

**Why the non-finite guard is load-bearing:** in Go, `int64(math.NaN())` is **implementation-defined**, so one junk Tempest value would otherwise commit arbitrary bytes to mainnet with no error raised anywhere. That is a wrong on-chain record, and **no golden file can catch it** — the goldens only see the inputs the test supplies. The guard in `scaleFloat` (§7.3) is the only thing standing there.

*(Changed from the earlier draft: integer fields now **reject** a fractional value rather than truncating it. Truncation was mimicry of JavaScript's `parseInt`, adopted for parity only. Rejecting is the safer rule and is the documented behaviour — §4.1's `mapper.go` row.)*

### 7.6 The script size cap

**`WEATHER_MAX_SCRIPT_BYTES = 297`** (default) — the top of the **contiguous** one-claim window at D=50 (§8.3).

Do **not** use 331 (the top of the disjoint island `[322,331]`): 298–321 in between costs 2 claims / 100 sat, so a 331 cap guarantees nothing and would silently double the cost of any record landing in the gap. Do **not** copy the 250 from the very first analysis: it happens to be safe at D=50 but wastes 47 B of usable window, and it was actively **wrong** at D=40 (window edge 199). **The number must be re-derived per denomination, never copied.**

Headroom at 297: **128 B over the 169 B design worst case (75.7 %)**, 109 B over a 188 B all-numeric-extremes synthetic record, and 86 B over the repo's own **211 B** `extremeWeatherData` fixture.

**Say it correctly: "169 B is the expected worst case; the publisher enforces the real bound at 297 B."** Do **not** say "records cannot exceed 169 B" — `icon`, `conditions`, `pressure_trend` and `lightning_strike_last_distance_msg` are **free-form strings** in `FIELD_SCHEMA`, not enums, so nothing in the encoder bounds the script. (At D=40 this distinction would have mattered: the 211 B fixture falls in D=40's 200–223 two-claim gap.)

Records exceeding the cap are marked terminal `failed` **individually** (§10.7) with `error = "script_too_large: N bytes > cap"` and raise alarm 8 — exceeding 297 B means Tempest introduced a much longer enum string and a human must look. `WEATHER_ALLOW_MULTICLAIM_SCRIPTS=true` is the incident escape hatch: it downgrades the config check to a WARN and permits paying 2 claims per record until the schema question is settled.

**The cap is enforced in the encoder, not only in the publisher.** `Encode` returns `ErrScriptTooLarge` for anything over the configured bound, so no oversized script can reach `CreateAction` by any path; the publisher's partition step (§10.7) consumes that typed error to isolate the row. This is the one hard numeric constraint that survives from the original §7 unchanged — it is **fuel arithmetic, not style**, and it is the reason a longer script is a correctness bug rather than an aesthetic one.

**The boundary is pinned by test:** a synthetic record encoding to **exactly 297 B is accepted**, one encoding to **298 B is rejected** with `ErrScriptTooLarge`, and the rejection is asserted to happen **before** `CreateAction` is called. The 211 B `extremeWeatherData` fixture is asserted ≤ 297 B. A golden record sitting exactly on the cap is committed (§7.4 item 1), so a future field addition that pushes the worst case over the line fails loudly.

### 7.7 The decoder — ONE layout, the one we write

The decoder exists so the **app** can read back its own records: `GET /api/proof`, reconciliation, and the §14.4 chain-rebuild procedure. It must round-trip whatever §7.1's encoder emits, and nothing else.

**Exactly one layout is supported.** *Removed: the prefix-less legacy fallback (bare `51 <fields>`, 34 chunks) for records written before `c44b7ae`, and the pre-`e2ae463` time-first field order (`FieldSchemaV0`) that an earlier draft carried as a third layout.* Reason: the only records in those layouts are on chain but **unlocatable** — the Atlas cluster that held their txids no longer resolves (§14.1) — so multi-layout support would be untestable code guarding against inputs that cannot arrive. A decoder that silently accepts three field orders is also a live hazard: two of them are pure permutations of the same 33 chunks, so a mis-selected layout reads as plausible garbage rather than as an error.

Use `go-sdk`'s own parser:

```go
ops, err := script.DecodeScript(raw, script.DecodeOptionsParseOpReturn)  // steps over OP_RETURN
```

`go-sdk`'s `DecodeScript` otherwise sets `op.Data = b` **including** the `0x6a` byte and swallows the remainder of the script into that one chunk. Verified signature: `func DecodeScript(b []byte, options ...DecodeOptions) ([]*ScriptChunk, error)`, with `DecodeOptionsParseOpReturn DecodeOptions = 0`.

**The chunk-count basis matters and is easy to get wrong.** `DecodeOptionsParseOpReturn` only stops `DecodeScript` from swallowing the remainder; it advances past the `0x6a` byte but **still appends the chunk**, because `ops = append(ops, op)` runs unconditionally at the bottom of the loop. A guard written on a 34-chunk basis therefore **accepts a script truncated by two fields**. **Count 36:** `OP_FALSE`, `OP_RETURN`, `OP_1`, then the 33 field pushes. Verified empirically at v1.3.2: decoding `006a5101ff0102` with `DecodeOptionsParseOpReturn` returns **5** chunks (`00`, `6a`, `51`, and the two pushes) — the `0x6a` chunk is present, exactly as this guard assumes.

For the number direction, `interpreter.MakeScriptNumber(bb, scriptNumLen, requireMinimal, afterGenesis)` is the inverse of §7.2's `Bytes()`; the small-int opcode branches (`OP_0`, `OP_1NEGATE`, `OP_1`..`OP_16`) are decoded from the opcode itself, since those chunks carry no data.

Decoder negatives to test: version ≠ 1 (hard reject), **fewer than 36 chunks** (hard reject), trailing chunks tolerated, a chunk whose push length is not minimal, and a `0x6a` appearing in a field position. *(The prefix-less 34-chunk fixture is removed with the legacy layout.)*

### 7.8 Version evolution warning

`VERSION` is emitted as a **single opcode**. Version 17 and above would stop being a 1-byte opcode, silently changing the prefix length from 3 to 4 bytes **and the chunk count guard from 36**. Write this in a comment above the constant.

Because the format is internal and single-layout (§7.7), a schema or version change is a **coordinated change to `encoder.go`, `decoder.go` and the goldens in one commit** — and the golden diff is what forces it to be noticed. Two constraints bind any future change regardless: it must keep the worst case under the **297-byte cap** (§7.6, or the cap and the server denomination move together), and it must keep `Decode(Encode(x)) == x`.

---

## 8. Fuel economics at D = 50

All arithmetic in this section was produced by driving the **real** `utxoCollector` (`newCollector` → `remaining` → `IsFunded` → `allocateUTXO` → `increaseSize` → `calculateChangeOutputs` → `prepareResult`) from a throwaway internal test in go-wallet-toolbox, replicating the `create.go` throughput call-site exactly (`txSats=0`, `initialTxSize = TransactionSizeFromScriptLengths(no inputs, N outputs of script len S)`, `outputCount=N`, `numberOfDesiredUTXOs` clamped to 1, `minimumDesiredUTXOValue=1000`, `maxChangeOutputsPerTx=1` as `create.go:432` pins it, `isSweep=false`, each allocated UTXO exactly D satoshis with `EstimatedInputSize=148`). Script sizes were measured by running the **real** TS encoder at `dist/format/encoder.js`. The scratch files were deleted and the toolbox working tree verified clean.

**Harness validation:** the identical sweep was run at D=40 as a control and reproduced the previous, independently-derived table **row-for-row with zero discrepancies**, including the disjoint-island boundaries at four different denominations. The harness is trustworthy for these numbers.

**What the harness does NOT cover:** it drives the collector arithmetic, not the SQL layer (`FindSmallestSufficientUTXOForUpdate`, `FOR UPDATE SKIP LOCKED`). That is sound for claim counts and fees — every fuel UTXO in the pool is exactly D satoshis, so whichever tiered best-fit branch runs returns an identical row — but these tables say **nothing** about lock contention or partial-pool behaviour.

### 8.1 T1 — Constants (verified in code, not assumed)

| Constant | Value | Source |
|---|---|---|
| `P2PKHEstimatedInputSize` | **148 B** | `pkg/internal/txutils/inputs_outputs_sizes.go` = 32+4+4+1+107 |
| `P2PKHOutputSize` / `changeOutputSize` | **34 B** | 1+25+8; `sql.go:25` |
| `txEnvelopeSize` | **8 B** | `tx_size.go:11` (version + locktime) |
| fee model | **sat/kb, value 100** | `infra-configmap.yaml` → fee = `ceil(size/1000 × 100)` |
| `MarginalFuelInputFee` | **15 sat** | `ceil(148/1000×100)` |
| `minSpendTxSize` → dustFloor | 192 B → **40 sat** | `max(1, ceil(192/1000×100)×2)` |
| commission | **disabled** | `commission.satoshis: 0` → `Commission.Enabled()` is `Satoshis > 0` |
| fuel basket on create | `NumberOfDesiredUTXOs=32`, `MinimumDesiredUTXOValue=1000` | DB defaults, `models/output_baskets.go:18-19` |
| throughput change budget | **exactly 1 output** | `create.go:421-433` pins `Existing = basket.NumberOfDesiredUTXOs` **and** `MaxChangeOutputs: 1` |
| weather output satoshis | **0** | `src/service/transaction.ts:56` |
| `initialTxSize(N,S)` | `10 + N×(S+9)` for S<253, N<253 | the script varint grows to 3 bytes at S≥253 |

### 8.2 T2 — Weather script sizes (re-measured at HEAD with the real encoder)

| Case | Measured S | Status |
|---|--:|---|
| Floor (all zero / empty strings) — `minimalWeatherData` | **36 B** | CONFIRMED |
| Real Tempest fixture — `sampleWeatherData` | **99 B** | CONFIRMED |
| Typical mid-range (rain-likely summer record) | **120–121 B** | CONFIRMED (1 claim at D=50 either way) |
| Design worst case | **169 B** | Stands (encoder unchanged) |
| All-numeric-extremes synthetic (every int at implausible maxima) | **188 B** | newly measured |
| The repo's own `extremeWeatherData` fixture (`icon: 'extreme-weather-<emoji>'`) | **211 B** | newly measured |

**Provenance note.** These sizes were measured by running the TypeScript encoder at `HEAD`. They **carry over to the Go encoder unchanged** because a script's length is determined by the field schema (kept identical, §7.1) and by minimal push encoding (kept, §7.2) — not by the rounding rule, which can move a scaled value by at most 1 and therefore changes a push length only if it crosses a sign-byte boundary. **Re-measure them from the Go encoder once §7.4's goldens exist** and treat those as the standing figures; the 128 B of headroom at the 297 B cap absorbs any single-byte drift in the meantime.

The 28-byte `icon` claim is corroborated: `icon` is a free-form `string` in `FIELD_SCHEMA` (`schema.ts:16`), not an enum, so a push costs `len+1` bytes. `'possibly-thunderstorm-night'` (27 chars) = 28 B vs `'possibly-thunderstorm-day'` (25 chars) = 26 B, and swapping them moves the encoded total by exactly 2 B, as measured.

**Decisive for D=50:** at D=40, S=211 costs **2 claims / 80 sat** because 211 falls in D=40's 200–223 two-claim gap. At D=50 it costs **1 claim / 50 sat**. **D=50 one-claims every script this codebase can currently produce, including its own extreme fixture. D=40 does not.**

### 8.3 T3 — One-claim script-size window per denomination

Swept S = 0…800 at N=1 through the real collector.

| D | One-claim S ranges | Contiguous-from-0 max | Two-claim gap |
|---|---|--:|---|
| 20 (live derived) | `24..33` only | **none** | — |
| 30 | `0..99`, `124..133` | 99 | 100..123 |
| 40 | `0..199`, `224..233` | 199 | 200..223 |
| **50 (chosen)** | **`0..297`, `322..331`** | **297** | **298..321** |
| 60 | `0..397`, `422..431` | 397 | 398..421 |

**Mechanism of the disjoint island, traced.** For S in `[298,321]` the first 148 B input briefly pushes `change() > 0`, which fires `calculateChangeOutputs` and adds a **sticky +34 B** change output (`increaseSize` is cumulative and the bump is never removed even when change goes negative again). That +34 B raises the fee past the 50 sat claimed, forcing a second claim. Traced at S=298: init 319 B / fee 32; claim 1 → +182 B (148 input + 34 change bump) → 501 B / fee 51 / change −1 → not funded; claim 2 → +182 → 683 B / fee 69 / change +31 → funded, **2 claims / 100 sat**. At S=322 the initial fee is already high enough that one 148 B input lands change at **exactly 0**, so the bump never fires: init 343 B / fee 35; claim 1 → +148 → 491 B / fee 50 / change 0 → funded in 1 claim.

**Structural result: the island is always exactly 10 bytes wide at every denomination** — D=30 `[124,133]`, D=40 `[224,233]`, D=50 `[322,331]`, D=60 `[422,431]`. It is the byte range over which `ceil(size/10)` stays pinned at exactly D, i.e. where post-one-input change is exactly 0 — a direct consequence of the 100 sat/kb fee granularity (1 sat per 10 B). **A 10-byte-wide target is useless as a configuration bound**, which is why the publisher cap is the top of the **contiguous** window (297) and the island is documented as a curiosity, never relied on.

### 8.4 The one-claim window and claim-count functions (implement in `internal/fuelmath/window.go`)

`internal/fuelmath` is a **leaf package with no local imports**, which is what allows `internal/config/validate.go` to depend on it without creating the cycle `config → fuel → config`.

**The fee must be evaluated the way the server evaluates it, in floating point.** `feeCalc.Calculate` for the `sat/kb` model is `math.Ceil(float64(size)/1000.0*float64(value))`, and that is **not** the same as the integer `ceil(size/10)` at 34 sizes below 8 kB — 70, 140, 280, 550, 560, 1090, 1100, 1110, 1120, 2180, 2200, 2220, 2240, … — because e.g. `280/1000*100` evaluates to `28.000000000000004`, so the fee is **29, not 28**.

```go
const feeValuePerKB = 100 // sat/kb, infra-configmap.yaml (§8.1)

// feeFor mirrors the server's feeCalc.Calculate EXACTLY. Do not "simplify" it to
// (size+9)/10: the two disagree at 34 sizes below 8 kB.
func feeFor(size int64) int64 {
    return int64(math.Ceil(float64(size) / 1000.0 * float64(feeValuePerKB)))
}

func initialTxSize(n, s int64) int64 {
    per := s + 9                    // 8 satoshis + 1-byte script varint + script
    if s >= 253 { per = s + 11 }    // 3-byte script varint
    return 10 + n*per
}

// oneClaimFunds reports whether ONE d-satoshi claim funds a one-record tx whose
// OP_RETURN script is s bytes.
func oneClaimFunds(s, d int64) bool {
    size := initialTxSize(1, s) + 148          // one P2PKH fuel input
    fee := feeFor(size)
    if d-fee > 0 {                             // calculateChangeOutputs adds 34 B, STICKY
        size += 34
        fee = feeFor(size)
    }
    return d >= fee
}

// MaxOneClaimScriptBytes returns the largest OP_RETURN script size that a single
// d-satoshi claim funds for a ONE-record transaction, counting only the CONTIGUOUS
// window from zero. Returns -1 when s=0 is not itself one-claim fundable.
func MaxOneClaimScriptBytes(d int64) int64 {
    if !oneClaimFunds(0, d) { return -1 }
    s := int64(0)
    for oneClaimFunds(s+1, d) { s++ }
    return s
}

// ClaimsRequired returns the number of d-satoshi fuel claims the funder needs for a
// tx with n OP_RETURN outputs of s script bytes and at most 1 change output.
// Drives config rule 12, the pool-floor rule (§8.15).
func ClaimsRequired(n, s, d int64) int64 {
    initial := initialTxSize(n, s)
    for k := int64(1); ; k++ {
        size := initial + 148*k
        fee := feeFor(size)
        if d*k-fee > 0 {                       // one change output, §8.1
            size += 34
            fee = feeFor(size)
        }
        if d*k >= fee { return k }
    }
}
```

`MaxOneClaimScriptBytes` reproduces T3 exactly: D=20 → −1, D=30 → 99, D=40 → 199, **D=50 → 297**, D=60 → 397.

**Why the loop and not the closed form.** The tempting `10d − 201` (`10d − 203` above the 253-byte varint step) agrees with the loop for almost every integer `d`, but is **one byte too high at d ∈ {28, 55, 56, 109, 110, 111, 112}**, and at **d = 17** it reports no window where the true contiguous max is **3** — all consequences of the float fee above. D=50 is unaffected, so every table here stands, but a shipped guard built on the closed form is wrong the moment the denomination moves. A table test asserts `MaxOneClaimScriptBytes` over **D ∈ {17, 20, 24, 28, 30, 40, 50, 55, 56, 60}** — the set that actually contains the closed form's failures; a D ∈ {20,30,40,50,60} sample cannot catch it.

### 8.5 T4 — Claims, satoshis, effective fee rate and change disposition at D=50

`charged` = `claims × 50`. `estFee`/`estSize` are the collector's internal estimate (which **always** includes a change output); `signedSz` is what is actually **broadcast** (change output omitted whenever it is dropped). `effRate` = `charged / signedSz × 1000`, since the whole claim is paid as fee.

**Single-record rows:**

| N | S | claims | charged | sat/rec | estFee | signedSz | honestFee | overpaid | effRate sat/kb | changeAmt | change output? |
|---|-----|---|-----|-------|-----|------|-----|----|-----|----|-----------------|
| 1 |  36 | 1 |  50 | 50.00 |  24 |  203 |  21 | 29 | 246 | 26 | NO — burned |
| 1 |  99 | 1 |  50 | 50.00 |  30 |  266 |  27 | 23 | 188 | 20 | NO — burned |
| 1 | 121 | 1 |  50 | 50.00 |  33 |  288 |  29 | 21 | 174 | 17 | NO — burned |
| 1 | 169 | 1 |  50 | 50.00 |  37 |  336 |  34 | 16 | 149 | 13 | NO — burned |
| 1 | 297 | 1 |  50 | 50.00 |  50 |  466 |  47 |  3 | 107 |  0 | NO — change is exactly 0 |

**Batch rows at S=121 and S=169:**

| N | S | claims | charged | sat/rec | estFee | signedSz | honestFee | overpaid | effRate | changeAmt | change output? |
|----|-----|----|-----|-------|-----|------|-----|----|-----|----|-------------|
|  5 | 121 |  2 | 100 | 20.00 |  99 |  956 |  96 |  4 | 105 |  1 | NO — burned |
| 10 | 121 |  4 | 200 | 20.00 | 194 | 1902 | 191 |  9 | 105 |  6 | NO — burned |
| 20 | 121 |  8 | 400 | 20.00 | 383 | 3794 | 380 | 20 | 105 | 17 | NO — burned |
| 21 | 121 |  8 | 400 | 19.05 | 396 | 3924 | 393 |  7 | 102 |  4 | NO — burned |
| 25 | 121 | 10 | 500 | 20.00 | 478 | 4740 | 474 | 26 | 105 | 22 | NO — burned |
|  5 | 169 |  3 | 150 | 30.00 | 138 | 1344 | 135 | 15 | 112 | 12 | NO — burned |
| 10 | 169 |  6 | 300 | 30.00 | 272 | 2678 | 268 | 32 | 112 | 28 | NO — burned |
| 20 | 169 | 11 | 550 | 27.50 | 524 | 5198 | 520 | 30 | 106 | 26 | NO — burned |
| 21 | 169 | 11 | 550 | 26.19 | 541 | 5376 | 538 | 12 | 102 |  9 | NO — burned |
| 25 | 169 | 13 | 650 | 26.00 | 642 | 6384 | 639 | 11 | 102 |  8 | NO — burned |

Note the distinction this table draws and that naive analysis conflates: **what the funder charges** (`claims × D`, computed against the collector's internal *estimate*, which always budgets a change output) is not the **honest fee of the final signed transaction**.

### 8.6 T5 — A weather transaction at D=50 NEVER gets a change output

Brute-forced N=1..30 × S=0..400 at D=50: the maximum `change()` ever reached at the funded point is **31 satoshis**, always below `dustFloor = 40`, so `prepareResult` sets `ChangeOutputsCount = 0` in **every** case and the entire claimed value is paid to the miner.

Closed form: **max change = D − 19** (the change-bump regime's net progress ceiling), so a change output first becomes possible only at **D ≥ 59**. Measured: D=55 → max 36 (never), D=56 → max 37 (never), D=60 → max 41 (sometimes kept).

Three consequences stated unconditionally:

1. **A weather transaction has exactly N outputs and no trailing change output**, so output indices are stable at `0..N-1` for the explorer and any verifier (§10.0, §13.5).
2. The entire claimed amount is paid to the miner; the effective fee rate is always `charged / true signed size`.
3. The collector's own `estFee`/`estSize` are **34 B and ~3 sat above what is actually broadcast**, so any table quoting collector estimates as broadcast sizes is wrong by that much. §8.5 separates them for exactly this reason.

### 8.7 T6 — Claim-cascade trace at the recommended cap, N=21, S=169

`initialTxSize = 10 + 21×(169+9) = 3748 B`, `txSats = 0`. Traced step-by-step out of the real collector:

| step | covered | txSize | Δsize | fee | Δfee | change | funded | remaining | chgN |
|----------|--------:|-------:|------:|----:|-----:|-------:|--------|----------:|-----:|
| init     |       0 |   3748 |     — | 375 |    — |   −375 | false  |       375 |    0 |
| claim 1  |      50 |   3896 |  +148 | 390 |  +15 |   −340 | false  |       340 |    0 |
| claim 2  |     100 |   4044 |  +148 | 405 |  +15 |   −305 | false  |       305 |    0 |
| claim 3  |     150 |   4192 |  +148 | 420 |  +15 |   −270 | false  |       270 |    0 |
| claim 4  |     200 |   4340 |  +148 | 434 |  +14 |   −234 | false  |       234 |    0 |
| claim 5  |     250 |   4488 |  +148 | 449 |  +15 |   −199 | false  |       199 |    0 |
| claim 6  |     300 |   4636 |  +148 | 464 |  +15 |   −164 | false  |       164 |    0 |
| claim 7  |     350 |   4784 |  +148 | 479 |  +15 |   −129 | false  |       129 |    0 |
| claim 8  |     400 |   4932 |  +148 | 494 |  +15 |    −94 | false  |        94 |    0 |
| claim 9  |     450 |   5080 |  +148 | 508 |  +14 |    −58 | false  |        58 |    0 |
| claim 10 |     500 |   5228 |  +148 | 523 |  +15 |    −23 | false  |        23 |    0 |
| **claim 11** | 550 |   5410 | **+182** | 541 | **+18** | **+9** | **TRUE** | −9 | 1 |

**Result:** 11 claims, **550 sat** charged, **26.19 sat/record**, `estFee` 541, `changeAmount` 9 → below dust 40 → `ChangeOutputsCount` forced to **0**, so all 550 sat goes to the miner. Broadcast tx = 11 P2PKH inputs + 21 data outputs = **5376 B**; honest fee at 100 sat/kb = 538 sat; overpayment only **12 sat**; effective rate **102 sat/kb** — just 2 % above the ARC floor. The `+182` on the final step is 148 (input) + 34 (change output), because `calculateChangeOutputs` fires only once `change() > 0`, and it fires **exactly once**, on the last claim.

**Companion trace, typical script (N=21, S=121):** init 2740 B / fee 274; 8 claims; claim 8 is the +182 bump step → 3958 B / fee 396 / change +4 → funded; **400 sat, 19.05 sat/record**, signed 3924 B, honest 393, effRate 102 sat/kb, change 4 burned.

**Convergence budget at D=50:** net progress per claim = `D − Δfee` = **50 − 15 = 35 sat** normally, **50 − 19 = 31 sat** worst case in the change-bump regime. Strictly positive with wide margin, so termination is guaranteed in ≤ `ceil(initialFee/31) + 2` claims. Compare D=40: 25 / 21 sat per claim — D=50 converges ~1.5× faster per claim and is far above `validateDenomination`'s `D > 15` floor. **`internal/config` nonetheless asserts its own denomination floor** rather than relying on the server's check, which ignores the bump — **config rule 8** (§8.15), stated as `MaxOneClaimScriptBytes(D) ≥ 36` (the 36 B encoder floor script of §8.2) and equivalent to **`D ≥ 24`** at the current fee model: `MaxOneClaimScriptBytes(23) = 29`, `MaxOneClaimScriptBytes(24) = 39`. (At D=20 net progress in the bump regime is **1 sat/claim**.)

### 8.8 T7 — Per-record cost is a SAWTOOTH; local maxima mapped

`sat/record = D×claims(N)/N` — a step function divided by a ramp. Measured at D=50, N=1..30 (`claims / sat-per-record`):

| N | S=36 | S=99 | S=121 | S=169 |
|---|------|------|-------|-------|
| 1 | 1/50.00 | 1/50.00 | 1/50.00 | 1/50.00 |
| 2 | 1/25.00 | 1/25.00 | 1/25.00 | 2/50.00 |
| 3 | 1/16.67 | 2/33.33 | 2/33.33 | 2/33.33 |
| 4 | 1/12.50 | 2/25.00 | 2/25.00 | 3/37.50 |
| 5 | 1/10.00 | 2/20.00 | 2/20.00 | 3/30.00 |
| 6 | 1/8.33 | 2/16.67 | 3/25.00 | 4/33.33 |
| 7 | 2/14.29 | 3/21.43 | 3/21.43 | 4/28.57 |
| 8 | 2/12.50 | 3/18.75 | 3/18.75 | 5/31.25 |
| 9 | 2/11.11 | 3/16.67 | 4/22.22 | 5/27.78 |
| 10 | 2/10.00 | 4/20.00 | 4/20.00 | 6/30.00 |
| 11 | 2/9.09 | 4/18.18 | 5/22.73 | 6/27.27 |
| 12 | 2/8.33 | 4/16.67 | 5/20.83 | 7/29.17 |
| 13 | 2/7.69 | 5/19.23 | 5/19.23 | 7/26.92 |
| 14 | 2/7.14 | 5/17.86 | 6/21.43 | 8/28.57 |
| 15 | 3/10.00 | 5/16.67 | 6/20.00 | 8/26.67 |
| 16 | 3/9.38 | 6/18.75 | 7/21.88 | 9/28.12 |
| 17 | 3/8.82 | 6/17.65 | 7/20.59 | 9/26.47 |
| 18 | 3/8.33 | 6/16.67 | 7/19.44 | 10/27.78 |
| 19 | 3/7.89 | 6/15.79 | 8/21.05 | 10/26.32 |
| 20 | 3/7.50 | 7/17.50 | 8/20.00 | 11/27.50 |
| **21** | **3/7.14** | **7/16.67** | **8/19.05** | **11/26.19** |
| 22 | 3/6.82 | 7/15.91 | 9/20.45 | 12/27.27 |
| 23 | 4/8.70 | 8/17.39 | 9/19.57 | 12/26.09 |
| 24 | 4/8.33 | 8/16.67 | 9/18.75 | 13/27.08 |
| 25 | 4/8.00 | 8/16.00 | 10/20.00 | 13/26.00 |
| 30 | 4/6.67 | 10/16.67 | 12/20.00 | 16/26.67 |

**LOCAL MAXIMA at D=50 — do NOT put the batch cap on any of these:**

| S | Local maxima (N) |
|--:|---|
| 36 | 7, 15, 23, 32 |
| 99 | 3, 7, 10, 13, 16, **20**, 23, 26, 29 |
| 121 | 3, 6, 9, 11, 14, 16, 19, 22, **25**, 28, 30 |
| 169 | **every even N**: 4, 6, 8, 10, 12, 14, 16, 18, **20**, 22, 24, 26, 28, 30, 32 |

**LOCAL MINIMA at D=50:**

| S | Local minima (N) |
|--:|---|
| 121 | 2, 5, 8, 10, 13, 15, 18, **21**, 24, 27, 29, 32 |
| 169 | **every odd N**: 3, 5, 7, 9, 11, 13, 15, 17, 19, **21**, 23, 25, 27, 29, 31 |

**Decisive:** N=20 is a local **maximum** at both S=99 (17.50) and S=169 (27.50). N=25 is a local maximum at S=121. N=22 is a local maximum at S=121 and S=169. N=23 is a local maximum at S=36. N=24 is a local maximum at S=169. **N=21 is the only value in 20..25 that is not a local maximum at ANY of the four script sizes** — it is a local **minimum** at both S=121 (19.05) and S=169 (26.19), and on a descent at S=36 and S=99.

**Asymptote — closed form, verified against measured rows at D=50:**

```
sat_per_record(inf) = D*(S+9) / (10*(D - 14.8))      where 14.8 = 148 B * 100 sat/kb / 1000
```

| S | closed form at D=50 | measured at N=2000 | agreement |
|---|--------------------:|-------------------:|-----------|
|  36 |  6.3920 |  6.4000 | 0.13 % |
| 121 | **18.4659** | 18.4750 | 0.05 % |
| 169 | **25.2841** | 25.3000 | 0.06 % |

Fee multiplier over the honest miner fee: `D/(D−14.8)` = **1.4205× at D=50** (vs 1.5873× at D=40, 3.846× at D=20). The formula also reproduced D=40 exactly (7.1429 / 20.6349 / 28.2540, multiplier 1.5873). **So the true batched-weather penalty of D=40 over D=50 is 11.7 %**, not the ~20 % a naive 24-vs-20 comparison at N=10 suggests.

### 8.9 Batch cap = 21

**`WEATHER_OUTPUTS_PER_TX = 21`**, validated `1..25`.

Rationale, in priority order:

1. **It is ≥ 20**, so the whole stated fleet (K ≤ 20 stations, polled every 300 s) fits into **one** `CreateAction` per poll. Splitting K=20 into two txs of 10 costs 2 × 6 = 12 claims / 600 sat at S=169 instead of 11 / 550 (**+9.1 %**), and 2 × 4 = 8 / 400 at S=99 instead of 7 / 350 (**+14.3 %**).
2. **It avoids every sawtooth local maximum** (§8.8). 21 is the unique value in 20..25 that is a local maximum nowhere, and is a local minimum at both S=121 and S=169.
3. **Raising the cap from 20 to 21 is FREE.** `claims(21) == claims(20)` at every measured script size: S=36 → 3, S=99 → 7, S=121 → 8, S=169 → 11. Identical claim count, identical satoshi charge per tx, identical burst rate, identical pool target. The only difference is that one extra station can join the fleet at **zero marginal fuel cost**, and a saturated batch sits on a trough instead of a peak.
4. Everything above 21 costs strictly more per saturated tx: N=22 needs 9 claims at S=121 and 12 at S=169; N=25 needs 10 and 13.

**Honest caveat: the cap is a ceiling, so it does not change today's bill.** With `POLL_RATE = 300 s` all K stations produce simultaneously, so the actual batch is `N = K` and the per-poll cost is set by K, not by the cap. Cap 21 vs cap 20 is cost-identical for today's fleet. The case for 21 is that it is free, that it makes the **saturated** steady state a trough rather than a peak, and that it buys one extra station for nothing.

**The Go port must add the range check itself.** The TypeScript validates `WEATHER_OUTPUTS_PER_TX` as **1..100 with a default of 100** (`src/config/env.ts:30`); the "1..25 validated range" of earlier drafts was a recommendation presented as an existing constraint. Go validates `1..25`, default **21**.

**Also note the real transaction rate.** `PROCESSOR_INTERVAL = 3 s` is how often the queue drainer **wakes**, not how often it publishes. The processor takes `find({status:'pending'}).limit(cap)` and puts the whole batch into **one** transaction; records are created by the poller once per `POLL_RATE = 300 s`, one per station. So in steady state the app publishes **one transaction every 5 minutes** containing one output per station: roughly **288 tx/day** and ~5 500 data outputs/day, i.e. **0.0033 tx/s**, not 0.33 tx/s. The ~2 867 idle 3-second ticks per 5-minute window do nothing but a cached-count check and an empty claim. Only a post-downtime backlog produces back-to-back saturated transactions — and that is exactly what the burst sizing below is for.

### 8.10 T8 — Pool sizing: `TargetPoolSize = 1000` (DOWN from 1500 at D=40)

**Burst rate.** `ClaimsRequired(21, 169, 50) = 11` (measured), `PROCESSOR_INTERVAL = 3 s`:

```
burst_rate = 11 / 3 = 3.6667 claims/s
```

**Refill horizon** `H = FUEL_INTERVAL (60 s) + fan-out RPC (~5 s) + unproven promotion (~10 s) = 75 s`.

**Low-water constraint** `0.60 × target ≥ burst_rate × H`:

```
target >= 3.6667 * 75 / 0.60 = 458.3   ->   floor = 459
```

**Chosen target = 1000.** Margin = `600 / 275 = 2.18×`. Equivalently the low-water reserve of 600 UTXOs supports a horizon of `600/3.6667 = 163.6 s`, i.e. 2.18× the 75 s budget — so even if the keeper interval slips to two minutes the pool holds.

**The real trigger arithmetic — read the guard before believing any round shape.** `runOnce` mints only when the pool is **strictly below** low water (`fuelkeeper/keeper.go:316-319`: `lowWater := target*LowWaterPercent/100; if inventory >= lowWater { return false, nil }`). So at the instant a round fires, `inventory ≤ 599`, and

```
deficit = 1000 − inventory  ≥ 401
leaves  = ceil(deficit / FanoutOutputsPerTx) = ceil((1000 − inventory)/100)  ∈ 5..10
```

**A 4-leaf round can never occur at target 1000.** A just-tripped pool (inventory 599 → deficit 401) mints **5** leaves and overshoots high water to **1099**; a cold start (inventory 0) mints exactly **10**. Any earlier statement of "a clean 400 deficit / 4 leaves" was an off-by-one against a strictly-below trigger and is deleted.

Why 1000 and not the bare 1.5× margin (689)? (a) 1000 makes low water **600**, so a triggered round is **5** leaves and a cold start is exactly **10** — both comfortably under `FanoutMaxTxsPerRound = 12`, and the cold start still completes in **one** round. **No target makes the round shape exact**, because the trigger is strictly-below: whatever the target, the deficit at the moment of firing is `target/… + 1` at best, so a whole number of leaves is unreachable by construction. 1000 is chosen because it makes both the steady and the cold-start shapes small, bounded and one-round — not because it divides evenly. (b) the extra margin is economically free — the whole standing pool is 50 000 sat; (c) cold start 0 → 1000 is exactly `ceil(1000/100) = 10` leaves, **one round**.

| Pool property at target 1000, D=50 | Value |
|---|---|
| Standing pool value | **1000 × 50 = 50 000 sat = 0.0005 BSV** |
| Low water (60 %) | 600 UTXOs |
| High water (100 %) | 1000 |
| Deficit at trigger (`inventory ≤ 599`) | ≥ **401** → `leaves = ceil((1000 − inventory)/100)` = **5..10** |
| Round shape, just-tripped pool | **5 leaves**, 500 fuel minted, overshoots to 1099 |
| Round shape, cold start (`inventory 0`) | **10 leaves**, 1000 fuel minted |
| Burst runway if the keeper dies mid-backlog | 1000 / 3.6667 = **272.7 s** |
| Steady runway (K=20, S=169, 11 claims per 300 s poll = 0.0367 claims/s) | 1000 / 0.0367 = **7.6 hours** |
| Refill rounds/day at K=20/S=169 (3168 claims/day ÷ 500 minted per steady round) | ~6.3 |
| Refill rounds/day at K=10/S=121 (1152 claims/day ÷ 500) | ~2.3 |

**Direction verified, not assumed** — raising D from 40 to 50 **lowers** the claim rate for the same transaction, so the target **falls**:

| quantity (S=169) | D=40 | D=50 | change |
|---|---:|---:|---|
| claims at N=20 | 15 | **11** | −26.7 % |
| claims at N=21 | 16 | **11** | −31.3 % |
| burst at cap 20 | 5.000/s | **3.667/s** | −26.7 % |
| floor at H=75 s, cap 20 | 625 | **459** | −26.6 % |
| floor at H=75 s, cap 21 | 667 | **459** | −31.2 % |
| chosen target | 1500 | **1000** | **−33.3 %** |
| standing pool | 60 000 sat / 0.0006 BSV | **50 000 sat / 0.0005 BSV** | −16.7 % sat |

Apples-to-apples on the earlier pass's own H=120 s assumption, D=50 cap 21 would give floor 733 and a 1.5× margin of 1100 — so ~1000 is the right neighbourhood on either horizon. **The prior 1500 does not carry forward**; keeping it would be a harmless but unjustified 3.27× over-provision.

`FUEL_INTERVAL = 60 s` is the **dominant** term in H (60 of 75 s), so H is largely a restatement of the keeper interval: halving it to 30 s would drop the floor to `3.6667×45/0.6 = 275`. The 2.18× margin is what absorbs error in the two *estimated* terms (~5 s and ~10 s); it tolerates H drifting to 163 s before low water is breached.

Note also that under `spend_policy: prefer_mined` (tiers `[mined, unproven]`) minted fuel becomes claimable at **ARC acceptance**, not at mining, so the server's 300 s `expected_confirmation_seconds` is the wrong horizon for this workload. The `sending`-tier gap — the keeper's `ListOutputs` count includes `sending` fuel the funder cannot yet claim — is absorbed by **pool size**, not by widening the shared `spend_policy` (§18.7).

### 8.11 T9 — Chunk sizing, leaf fan-out, and fan-out overhead

`ChunkFeeHeadroom = max(1000, 8×D) = max(1000, 400) = **1000**` at D=50 (the `8×D` branch loses, exactly as at D=40 — the default is correct and needs no override).

| Quantity | Formula | Value at D=50 |
|---|---|--:|
| chunk value | `FanoutOutputsPerTx × D + ChunkFeeHeadroom` | **100×50 + 1000 = 6 000 sat** |
| **server minimum** (`validateFuelShape`, reserve branch, `create.go:783`) | `fanout_outputs_per_tx × denomination` | **5 000 sat** |
| clears minimum? | 6000 ≥ 5000 | **yes, +20.0 %** (at D=40 it was 5000 vs 4000, +25.0 %) |
| leaf tx (1 in, 100 × P2PKH out, +1 change out) | measured | **3 592 B → fee 360 sat** |
| headroom covers leaf fee? | 1000 ≥ 360 | **yes, 640 sat slack** — 2.8× cover |
| leaf leftover | 6000 − 5000 − 360 | **640 sat → the `default` basket** |

**Leaf funding trace (real collector, with the `create.go` fuel-shape accounting `targetSat += Count*D`, `initialTxSize += Count*P2PKHOutputSize`, `outputCount += Count`).** `txSats = 5000` (shape value), `initialTxSize = 10 + 100×34 = 3410 B`, `outputCount = 100`, initial fee 341, remaining 5341. One 6 000-sat chunk claimed → txSize 3592 B (3410 + 148 input + 34 change output) → fee 360 → change 640 → **funded in 1 claim**. `ChangeOutputsCount = 1` (640 ≥ dustFloor 40, so a **real** change output IS minted here — unlike weather txs).

**The leftover goes to `default`, not `reserve`.** `create.newOutputs` writes ordinary change to `to.Ptr(wdk.BasketNameForChange)` and `wdk.BasketNameForChange = "default"`; only the shaped outputs go to `fuelShape.Basket`. So `countBasketOutputs(reserve)` stays an **exact chunk count**, never polluted by sub-chunk change, and `ensureChunks` cannot over-report available chunks. **The "reserve-count risk" from the first analysis does not exist.**

**Chunk-minting tx (`default` → `reserve`), k chunks in ONE tx** — confirmed that `ensureChunks` **batches**: it calls `fanOut` once with `Count = min(leaves − chunks, FanoutOutputsPerTx)`, not once per chunk:

| chunks k | signed size | fee | fee per chunk | value minted |
|---:|---:|---:|---:|---:|
| 1 | 226 B | 23 | 23.00 | 6 000 |
| 2 | 260 B | 26 | 13.00 | 12 000 |
| 4 | 328 B | 33 | 8.25 | 24 000 |
| **5 (our steady refill round, §8.10)** | **362 B** | **37** | **7.40** | **30 000** |
| 6 | 396 B | 40 | 6.67 | 36 000 |
| 8 | 464 B | 47 | 5.88 | 48 000 |
| **10 (cold start, §8.10)** | **532 B** | **54** | **5.40** | **60 000** |

**Fan-out overhead per 100 fuel UTXOs minted:**

```
leaf tx fee            360.00 sat
chunk tx fee share       7.40 sat   (37 / 5, the 5-chunk steady round of §8.10)
TOTAL                  367.40 sat
face value             100 * 50 = 5 000 sat
OVERHEAD = 367.40 / 5000 = 7.348 %
ALL-IN COST PER FUEL UTXO = 50 * 1.07348 = 53.674 sat
```

Sensitivity (the leaf fee is fixed at 360 regardless of round shape, so the percentage is denomination-sensitive but round-shape-insensitive — every daily figure below is stable to ±0.3 % regardless):

| chunks per round | fee/chunk | overhead | all-in per fuel |
|---:|---:|---:|---:|
| 1 | 23.00 | 7.660 % | 53.830 |
| 2 | 13.00 | 7.460 % | 53.730 |
| 4 | 8.25 | 7.365 % | 53.683 |
| **5 (our steady round)** | **7.40** | **7.348 %** | **53.674** |
| 6 | 6.67 | 7.333 % | 53.667 |
| **10 (cold start)** | **5.40** | **7.308 %** | **53.654** |

The whole spread from k=1 to k=10 is **0.35 percentage points**, which is why the off-by-one in the round shape moves no cost conclusion: every daily figure below is stable to ±0.3 % across the entire table.

Compare D=40 (6-chunk round): 366.67 / 4000 = 9.17 %, all-in 43.67 sat. **Raising D to 50 drops the fan-out overhead ratio from 9.17 % to 7.348 % (−19.9 % relative)** precisely because the fixed 360-sat leaf fee is amortised over 25 % more face value.

**Reserve does not hold a standing balance.** `ensureChunks` provisions exactly `leaves` chunks and `mintLeaves` consumes them in the same round; at rest the reserve basket holds ~0 chunks, with a transient of up to **5 × 6 000 = 30 000 sat** during a steady refill and **10 × 6 000 = 60 000 sat** during a cold start (§8.10 round shapes).

### 8.12 T10 — Daily burn, 30-day cost, and the deposit

`POLL_RATE 300 s` → **288 polls/day**. Cap 21 ≥ K for all K ≤ 20, so **one tx per poll with N = K**. All-in figures apply the **7.348 %** fan-out overhead of the 5-chunk steady round (§8.11). Recomputing at any other round shape in that table moves every figure below by less than 0.3 %, i.e. nothing changes to four significant figures.

**At S=121 (the requested script size):**

| K | claims/poll | sat/poll | fuel sat/day | all-in sat/day | BSV/day | 30-day sat | **30-day BSV** |
|---:|---:|---:|---:|---:|---:|---:|---:|
|  5 | 2 | 100 |  28 800 |  30 916 | 0.00030916 |   927 487 | **0.00927487** |
| 10 | 4 | 200 |  57 600 |  61 832 | 0.00061832 | 1 854 973 | **0.01854973** |
| 20 | 8 | 400 | 115 200 | 123 665 | 0.00123665 | 3 709 947 | **0.03709947** |

**Bracketing script sizes:**

| K | S | claims/poll | sat/poll | all-in sat/day | 30-day BSV |
|---:|---:|---:|---:|---:|---:|
|  5 |  99 | 2 | 100 |  30 916 | 0.00927487 |
|  5 | 169 | 3 | 150 |  46 374 | 0.01391230 |
| 10 |  99 | 4 | 200 |  61 832 | 0.01854973 |
| 10 | 169 | 6 | 300 |  92 749 | 0.02782461 |
| 20 |  99 | 7 | 350 | 108 207 | 0.03246204 |
| 20 | 169 | 11 | 550 | 170 039 | 0.05101176 |

**Standing float:**

| item | sat | BSV |
|---|---:|---:|
| Pool (1000 × 50) | 50 000 | 0.00050 |
| Reserve peak, steady round (**5** chunks × 6 000, transient within a round — §8.10) | 30 000 | 0.00030 |
| **Standing float (pool + steady-round reserve peak)** | **80 000** | **0.00080** |
| Cold start 0 → 1000: **10** chunks × 6 000 = 60 000 drawn, net of 10 × 640 returned to `default` | 53 600 net | 0.000536 |
| Worst instantaneous float (pool at target **plus** a cold-start reserve transient) | 110 000 | 0.00110 |

**The fuel derivation's own recommendation was a single 10 000 000 sat (0.1 BSV) deposit** — quoted verbatim as **160.5 days** at K=10/S=121, **80.3** at K=20/S=121, **58.4 at the absolute worst K=20/S=169**, with a 2 000 000 sat alert threshold. *(Recomputed against this section's all-in burn those become **161.7 / 80.9 / 58.8 days**; the prior figures are kept as quoted so the override is measured against what was actually recommended, and §0.2 cites the recomputed 58.8.)* **This spec deliberately overrides it downward.** The reason is §12.1: the exposure of the `default` basket is **delegated spending authority** under an unfixed CVE class, so the float is sized in **days**, not months. The arithmetic above is retained verbatim because it is what makes the override quantifiable rather than arbitrary.

| Adopted deposit policy | Value |
|---|---|
| **Standing deposit into `default`** | **1 500 000 sat = 0.015 BSV** |
| Burnable after the 80 000-sat standing float | 1 420 000 sat |
| Runway at expected K=10/S=121 (61 832 sat/day) | **23.0 days** |
| Runway at worst case K=20/S=169 (170 039 sat/day) | **8.3 days** |
| Top-up cadence | **weekly**, via `weather deposit` |
| **Maximum blast radius at any instant** | **≈ 1 580 000 sat ≈ 0.0158 BSV** |
| Low-balance alert on `default` | **250 000 sat** (≈ 4 days at expected burn) |

**Comparison with the live derived D=20** (why the ConfigMap change is not optional): at N=10/S=121, D=20 charges **26 claims / 520 sat** vs D=50's **4 claims / 200 sat** — **2.60× cheaper and 6.5× less `FOR UPDATE SKIP LOCKED` row contention per weather transaction.**

*(The D=20 comparison row is carried from a prior verified derivation, not re-measured in this pass; the D=20 one-claim window `[24,33]` was reproduced exactly. No USD figures are quoted — the BSV price is unverified.)*

### 8.13 Config delta — CLIENT FuelKeeper overrides

`FromThroughput(throughput, 50)` inherits the **live server's 1000-TPS shape**. Three fields are catastrophically wrong for a ~1-tx-per-5-minutes app and **must** be overridden; the rest are correct as inherited.

**`internal/config` is the single source for every mirrored number.** There are no Go constants for them: each is a validated environment variable shipped in `app-configmap.yaml`. `internal/fuel` receives them as a plain `fuel.Params` value struct filled by `cmd/weather/main.go`, so it never imports `internal/config`:

```go
// internal/fuel — Params carries the validated config values BY VALUE, which is what
// keeps this package out of the config -> fuel -> config import cycle.
type Params struct {
    Denomination         int64          // cfg.DenominationSatoshis
    TargetPoolSize       int64          // cfg.FuelTargetPoolSize
    Interval             time.Duration  // cfg.FuelInterval
    FanoutOutputsPerTx   int64          // cfg.FanoutOutputsPerTx
    FanoutMaxTxsPerRound int64          // cfg.FuelFanoutMaxTxsPerRound
}
```

| Config field | inherited by `FromThroughput` | **OVERRIDE to** | why |
|---|---:|---:|---|
| `Denomination` | 50 | **50 (keep)** | must equal the server's resolved denomination exactly, or every leaf shape fails `validateFuelShape` and minting is **silently** disabled |
| **`TargetPoolSize`** | **450 000** (`target_tps 1000 × expected_confirmation_seconds 300 × pool_headroom_factor 1.5`) | **1000** | inherited value is **450× too big**; the first round would compute `leaves = ceil(450000/100) = 4500` and `ensureChunks` would try to draw **4500 × 6000 = 27 000 000 sat (0.27 BSV)** out of `default` |
| **`Interval`** | **10 s** (`top_up.interval_seconds`) | **60 s** | 6× fewer wake-ups (10 s = 8 640 `ListOutputs` RPCs/day for a pool that moves ~3 times a day); also the dominant term in the 75 s refill horizon H |
| **`FanoutMaxTxsPerRound`** | **12 000** | **12** | must be **≥ 5 for a steady refill** (a just-tripped pool computes `leaves = 5`, §8.10) and **≥ 10 for cold start**, so 0 → 1000 completes in ONE round; rule 13 enforces the cold-start bound, which subsumes the steady one. 12 000 is meaningless here and bounds nothing (it would let a runaway round draw unbounded value from `default`) |
| `LowWaterPercent` | 60 | 60 (keep) | the 0.60 in the low-water constraint |
| `HighWaterPercent` | 100 | 100 (keep) | refills all the way to target; with the strictly-below trigger the deficit is ≥ 401, i.e. **5..10 leaves** (§8.10) |
| `FanoutOutputsPerTx` | 100 | **100 (keep, but set EXPLICITLY)** | must equal the server's `fanout_outputs_per_tx` or `validateFuelShape` rejects the count. Set explicitly because the failure is **silent in one direction** — see the invariant below |
| `ChunkFeeHeadroom` | 1000 (`max(1000, 8×50=400)`) | 1000 (keep) | already correct at D=50; covers the 360-sat leaf fee with 640 slack |
| `PoolBasket` | `"fuel"` | keep | must match server |
| `ReserveBasket` | `"reserve"` | keep | must match server |
| `MintConcurrency` | 0 → 1 (serial) | **1, set explicitly** | concurrent leaves collide on reserve-chunk selection in `mintOneLeaf`'s retry path. **A correctness choice, not a tuning knob** — not configurable |
| `Originator` | `"fuelkeeper"` | **`"weather-proof"`** | attribution only |
| `StreamLeafCap` | 0 → default 10 | leave 0 | only applies while `SetStreamActive(true)`; this app never streams |
| `StreamYieldMultiple` | 0 → default 3 | leave 0 | same |

**Never call `keeper.SetStreamActive(true)`.** A unit test asserts each of the three values under **OVERRIDE** differs from what `FromThroughput` would have inherited, so a future config sync cannot silently re-inherit them; `Denomination` and `FanoutOutputsPerTx` are asserted **equal** to the mirrored server values instead, by the mirror test of §8.14.

**The `FanoutOutputsPerTx` invariant, both directions** (comment this at the constant):

- **Too large** → the server rejects the leaf shape (`shape.Count > fanout_outputs_per_tx`). **Loud.**
- **Too small** → the keeper's chunk value is `FanoutOutputsPerTx × D + headroom`, but the server requires chunk satoshis ≥ **its** `fanout_outputs_per_tx × D`. A client value of 50 against a server value of 100 produces a 3 500-sat chunk against a 5 000-sat minimum; every chunk fan-out is rejected; `ensureChunks` returns 0 chunks and **no error**; `runOnce` and `Run` report success forever. **Silent.**

### 8.14 The boot preflight (`internal/fuel/preflight.go`)

A one-leaf `Count=1` probe **does not prove what it appears to prove**: `validateFuelShape` (`create.go:763-791`) checks only `Count ∈ [1, server_fanout_outputs_per_tx]` and, for a pool-basket shape, `Satoshis == server denomination`. A `Count=1` leaf passes for **any** client `FanoutOutputsPerTx` and never exercises the reserve chunk minimum — the single most dangerous misconfiguration (client 50 vs server 100) sails straight through.

The preflight therefore probes **both real shapes, in this order**:

| # | Probe | Catches | Cost |
|---|---|---|---|
| 1 | **Reserve chunk** at `Satoshis = FanoutOutputsPerTx × D + ChunkFeeHeadroom` = **6 000**, `Count = 1` | A client `FanoutOutputsPerTx` **too small** (where `ensureChunks` otherwise returns 0 chunks and no error) | one 6 000-sat chunk, recycled by the next round |
| 2 | **Leaf** with `Count = cfg.FanoutOutputsPerTx` (100) at `Satoshis = D` (50), spending the chunk from probe 1 | A client `FanoutOutputsPerTx` **too large** | ~5 360 sat, all of which becomes usable pool fuel |

| Outcome | Action |
|---|---|
| `validateFuelShape` **validation** error | `ERROR` log naming **both** constants and the ConfigMap path → **exit non-zero.** A deterministic config bug; crashlooping is the correct loud signal. |
| `ErrNotEnoughFunds` | `WARN`, set `degraded = "unfunded"`, **continue serving.** Never crash. |
| Transport error / 5xx / timeout | `WARN`, set `degraded = "storage-unreachable"`, **continue serving**, retry on the next keeper round. |

**Crashloop-burn guard.** Probes are gated behind `default` balance > 0 **and** the `app_preflight` fingerprint cache: `sha256(D | FanoutOutputsPerTx | ChunkFeeHeadroom | storage URL | network)`. If a successful probe exists for the same fingerprint within 24 h, skip. Cost is bounded to one chunk + one leaf per config change per day.

**Belt and braces:** a checked-in table test (`internal/fuel/mirror_test.go`) diffs `DENOMINATION_SATOSHIS` and `FANOUT_OUTPUTS_PER_TX` against the literal values in `bsva-infra-flux/apps/base/go-wallet-toolbox/infra-configmap.yaml`, citing the file by path in a code comment. It cannot detect a drifting *remote* config, but it makes a local edit fail CI.

### 8.15 CONFIGURATION — the complete inventory and the numbered validation rules

**This is the normative configuration surface.** §5 row 10 names "an explicit validated config table" as the remedy for the `FUNDING_BACKET_MIN` class of defect — a misspelled key that silently fell back to a default for months. That remedy only exists if the table exists in one place, so it is here, and nothing else in the tree reads `os.Getenv` (§4.1, `internal/config/config.go`). Rules are cited elsewhere in this document **by number**; when a rule is added, it takes the next free number and is never renumbered.

**Subcommand column key** — `S` = `serve` (the default, all eight goroutines), `D` = `deposit-address` / `deposit`, `R` = `requeue`, `X` = `stats-recompute`, `P` = `preflight`. `Validate(subcommand)` applies only the rules the subcommand's required set can satisfy: `D`/`R`/`X` need the database and (for `D`) the wallet, and **must succeed with `TEMPEST_API_KEY`, both ports and every fuel knob unset**.

| Env var | Type | Default | Validated range | Required for | Rules | Mirrored with |
|---|---|---|---|---|---|---|
| `SERVER_PRIVATE_KEY` | `Secret` | **none — fails closed** | parses as a private key for `BSV_NETWORK` | S, D, P | 1, 2 | SSM `/apps/weather-chain/SERVER_PRIVATE_KEY` |
| `POSTGRES_PASSWORD` | `Secret` | **none — fails closed** | non-empty | S, D, R, X | 1 | SSM; `postgres-deployment.yaml` `secretKeyRef` |
| `TEMPEST_API_KEY` | `Secret` | **none** | non-empty | **S only** | 3 | SSM |
| `BSV_NETWORK` | enum | `test` | `main` \| `test` | S, D, P | 4 | frontend build arg `VITE_BSV_NETWORK` (§13.9) |
| `WALLET_STORAGE_URL` | URL | none | absolute, scheme `https`, host only | S, D, P | 5 | — |
| `API_PORT` | int | `3001` | `1..65535`, ≠ `OPS_PORT` | S | 6 | Service/probe ports, `EXPOSE` (§6.6) |
| `OPS_PORT` | int | `9090` | `1..65535`, ≠ `API_PORT` | S | 6 | **not** published on the Service (§11.2) |
| `PG_HOST` | string | `postgres` | non-empty | S, D, R, X | 7 | `postgres-service.yaml` |
| `PG_PORT` | int | `5432` | `1..65535` | S, D, R, X | 7 | `postgres-service.yaml` |
| `PG_USER` | string | `weather` | non-empty | S, D, R, X | 7 | `postgres-deployment.yaml` |
| `PG_DATABASE` | string | `weather` | non-empty | S, D, R, X | 7 | `postgres-deployment.yaml` |
| `PG_SSLMODE` | enum | `disable` | `disable` \| `require` \| `verify-ca` \| `verify-full` | S, D, R, X | 7 | — |
| `DENOMINATION_SATOSHIS` | int | `50` | rule 8 ⇒ **≥ 24** | S, P | 8, 10, 12 | **server** `infra-configmap.yaml` `denomination_satoshis` |
| `FANOUT_OUTPUTS_PER_TX` | int | `100` | **exactly 100** | S, P | 9, 13 | **server** `infra-configmap.yaml` `fanout_outputs_per_tx` |
| `FUEL_TARGET_POOL_SIZE` | int | `1000` | `≥ 1`, and rules 12–13 | S | 12, 13, 16 | — (client-only override, §8.13) |
| `FUEL_INTERVAL` | duration | `60s` | `> 0`; term of `H` in rule 12 | S | 12, 15 | — |
| `FUEL_FANOUT_MAX_TXS_PER_ROUND` | int | `12` | rule 13 ⇒ **≥ 10** here | S | 13 | — |
| `WEATHER_OUTPUTS_PER_TX` | int | `21` | `1..25` | S | 11, 12 | — (TS shipped `1..100`, §8.9) |
| `WEATHER_MAX_SCRIPT_BYTES` | int | `297` | `36 ≤ v ≤ MaxOneClaimScriptBytes(D)` | S | 10, 12 | derived from `DENOMINATION_SATOSHIS` |
| `WEATHER_ALLOW_MULTICLAIM_SCRIPTS` | bool | `false` | — ; when `true`, rule 10 downgrades to WARN | S | 10 | incident escape hatch (§7.6) |
| `POLL_RATE` | duration | `300s` | `≥ 60s` | S | 15 | station-online window `3 × POLL_RATE` (§13.3) |
| `PROCESSOR_INTERVAL` | duration | `3s` | `> 0`; divisor in rule 12 | S | 12, 15 | — |
| `PROCESSING_LEASE` | duration | `5m` | `>` the `CreateAction` per-call timeout | S, R | 14 | §3.2 per-call budget |
| `RECONCILE_INTERVAL` | duration | `5m` | `> 0`, `≥ PROCESSOR_INTERVAL` | S | 15 | — |
| `TRUSTED_PROXY_CIDRS` | CIDR list | **empty = trust nothing** | every entry parses as a CIDR | S | 17 | cluster pod CIDR (§15.3) |
| `PROOF_RATE_LIMIT_PER_MIN` | int | `60` | `≥ 1` | S | 18 | §6.1 limiter table |
| `LOG_LEVEL` | enum | `info` | `debug` \| `info` \| `warn` \| `error` | S, D, R, X, P | 19 | — |

**The numbered rules.** Every one is enforced in `internal/config/validate.go` and asserted by §17.2 test 17.

| # | Rule | Predicate |
|---|---|---|
| 1 | **No compiled-in secret.** | `SERVER_PRIVATE_KEY != "" && POSTGRES_PASSWORD != ""`, with **no default of any kind** in the Go source. Fails closed so the pod `CrashLoopBackOff`s rather than running on a publicly known key (§6.0 control 13, §6.6). |
| 2 | **Key is well-formed and matches the network.** | `SERVER_PRIVATE_KEY` parses as a private key valid for `BSV_NETWORK`. The error names the variable, **never the value** (rule 20). |
| 3 | **Tempest key required only to ingest.** | `subcommand == serve ⇒ TEMPEST_API_KEY != ""`. Unset is legal for `D`/`R`/`X`/`P`. |
| 4 | **Network is a closed enum.** | `BSV_NETWORK ∈ {main, test}`. No fallback — the TS `'test'` fallback is what shipped mainnet links pointing at testnet (§2.3 bug 1). |
| 5 | **Storage URL is not assembled from data.** | `WALLET_STORAGE_URL` parses as an absolute URL with `scheme == "https"` and an empty path/query (§6.2 control 7). |
| 6 | **Ports are distinct and in range.** | `API_PORT ∈ [1,65535] && OPS_PORT ∈ [1,65535] && API_PORT != OPS_PORT`. Equality would silently bind `/api/ops` onto the world-reachable port (§11.2). |
| 7 | **Postgres DSN is complete.** | `PG_HOST != "" && PG_USER != "" && PG_DATABASE != "" && PG_PORT ∈ [1,65535] && PG_SSLMODE ∈ {disable, require, verify-ca, verify-full}`. |
| 8 | **Denomination floor — the encoder floor, not a magic number.** | `fuelmath.MaxOneClaimScriptBytes(DENOMINATION_SATOSHIS) ≥ 36`. **Derivation:** 36 B is the measured floor script — the all-zero/empty-string `minimalWeatherData` record (§8.2), i.e. the *smallest* script the encoder can emit. A denomination whose one-claim window does not even reach 36 B cannot one-claim **any** record. Evaluating the real loop of §8.4 (never the closed form): `MaxOneClaimScriptBytes(23) = 29 < 36`, `MaxOneClaimScriptBytes(24) = 39 ≥ 36`, so the rule is equivalent to **`D ≥ 24`** at the current fee model — and that is where the `D ≥ 24` quoted in §8.8 comes from. **State the rule as the predicate, not as `D ≥ 24`**: the constant moves the moment the fee model does, and `10D − 201` is wrong at `D ∈ {17, 28, 55, 56, …}` (§8.4). |
| 9 | **Fan-out width mirrors the server exactly.** | `FANOUT_OUTPUTS_PER_TX == 100`. Not a range — an equality against the server's `fanout_outputs_per_tx`, because being **too small fails silently** (§8.13 invariant). |
| 10 | **Script cap sits inside the contiguous one-claim window.** | `36 ≤ WEATHER_MAX_SCRIPT_BYTES ≤ fuelmath.MaxOneClaimScriptBytes(DENOMINATION_SATOSHIS)`. At D=50 the ceiling is **297** (§8.3). `WEATHER_ALLOW_MULTICLAIM_SCRIPTS=true` downgrades the upper bound to a WARN; the lower bound is never downgradeable. |
| 11 | **Batch cap.** | `WEATHER_OUTPUTS_PER_TX ∈ [1,25]` (§8.9). The Go port adds this range itself — the TS validated `1..100`. |
| 12 | **The pool-floor rule** (driven by `fuelmath.ClaimsRequired`, §8.4). | `(LowWaterPercent / 100) × FUEL_TARGET_POOL_SIZE ≥ (ClaimsRequired(WEATHER_OUTPUTS_PER_TX, WEATHER_MAX_SCRIPT_BYTES, DENOMINATION_SATOSHIS) / PROCESSOR_INTERVAL_seconds) × H_seconds`, where `H = FUEL_INTERVAL + 15s` (fan-out RPC ~5 s + unproven promotion ~10 s, §8.10). **Evaluated at the cap, not at the design worst case**, because `WEATHER_MAX_SCRIPT_BYTES` is the only runtime bound on S. At the shipped values: `ClaimsRequired(21, 297, 50) = 19`, burst `19/3 = 6.333/s`, `H = 75 s` ⇒ required low water **475**; actual low water is **600**, a **1.26×** margin. At the 169 B design worst case the same predicate needs 275 against 600 — the **2.18×** margin of §8.10. Both hold; the cap is the binding one. |
| 13 | **The one-round cold-start rule.** | `FUEL_FANOUT_MAX_TXS_PER_ROUND ≥ ceil(FUEL_TARGET_POOL_SIZE / FANOUT_OUTPUTS_PER_TX)` — at the shipped values `12 ≥ ceil(1000/100) = 10` ✓. This **subsumes** the steady-refill bound `ceil((TargetPoolSize − (lowWater − 1)) / FanoutOutputsPerTx) = ceil(401/100) = 5` (§8.10), so only the cold-start form is enforced. |
| 14 | **Lease strictly outlives a publish.** | `PROCESSING_LEASE > CreateAction_per_call_timeout` (60 s, §3.2), so the reaper can never reclaim a row whose publish is still in flight (§10.4). |
| 15 | **Intervals are positive and ordered.** | `FUEL_INTERVAL > 0 && PROCESSOR_INTERVAL > 0 && RECONCILE_INTERVAL > 0 && POLL_RATE ≥ 60s && RECONCILE_INTERVAL ≥ PROCESSOR_INTERVAL`. |
| 16 | **Water marks are a proper band.** | `FUEL_TARGET_POOL_SIZE ≥ 1 && LowWaterPercent ∈ [1,99] && HighWaterPercent ∈ (LowWaterPercent, 100]`. `Low ≥ High` would make `runOnce` mint forever or never. **`LowWaterPercent`/`HighWaterPercent` are deliberately NOT env vars** — they are inherited unchanged from `FromThroughput` (§8.13) and asserted here on the resolved `fuel.Params`, so the rule still fires if a future config sync moves them. |
| 17 | **Proxy trust is explicit and fails closed.** | Every `TRUSTED_PROXY_CIDRS` entry parses via `net.ParseCIDR`; a malformed entry is a **startup error**. An **empty** list is legal and means *trust nothing* — forwarding headers are ignored and the limiter keys on `r.RemoteAddr` — but it emits the boot WARN of §6.1 so the degradation is never silent. |
| 18 | **Proof limiter is a real limit.** | `PROOF_RATE_LIMIT_PER_MIN ≥ 1`. Zero would mean "unlimited" on an unauthenticated outbound proxy (§13.6). |
| 19 | **Log level is a closed enum.** | `LOG_LEVEL ∈ {debug, info, warn, error}`. |
| 20 | **Aggregate, and never echo a value.** | `Validate(subcommand)` evaluates **every** applicable rule and returns **all** failures joined, not the first. No produced error string contains any configured value — a test scans every error for configured key material (§12.5). |

---

## 9. Operator deposit path — the sole successor to `setup-funding.ts`

**Nothing fills the `default` basket automatically.** Every satoshi the FuelKeeper spends comes from `default`. Without this component, a first deploy is a running binary with zero fuel and no way to add money, and after the last human deposit the app dies **silently**: `ensureChunks` logs a WARN and returns `nil`, so `runOnce` and `Run` report no error at all.

Both subcommands are **copies (not imports)** of `cmd/throughput_dashboard/internal/funding/{address.go,internalize.go}` — that package lives under another module's `internal/` and cannot be imported.

### 9.1 `weather deposit-address`

```
$ weather deposit-address
address:  1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa
prefix:   1z2h3f...==      (base64, random 16 bytes)
suffix:   9k1q0w...==      (base64, random 16 bytes)
network:  main
NOTE: send from any wallet, then run:  weather deposit --txid <txid>
```

- Derives a **per-deposit** BRC-29 self-payment address using a fresh random `(derivationPrefix, derivationSuffix)` pair. **It does not use the dashboard's fixed constants** — reusing one derivation makes every deposit land on a single publicly derivable address.
- Persists `(suffix, prefix, address, locking_script)` into `deposits` so `weather deposit` can resolve them without the operator copying anything but a txid.

### 9.2 `weather deposit --txid <id> [--vout n]`

1. Look up `deposits` rows with `internalized_at IS NULL` (or the row for `--suffix` if given).
2. Fetch **Atomic BEEF**: `services.GetBEEF(ctx, txid, nil)` then `beef.AtomicBytes(txidHash)`.
3. **Resolve the vout by scanning the transaction's outputs for the expected P2PKH locking script** — never assume vout 0. `--vout` overrides the scan. If no output matches any pending deposit's script: exit non-zero with the list of scripts tried.
4. Call `InternalizeAction` with `InternalizeProtocolWalletPayment{derivationPrefix, derivationSuffix, senderIdentityKey}`.
5. Record `txid`, `vout`, `satoshis`, `internalized_at`; print the new `default` balance.

**Assertion and doc line, present in code and in the runbook:** wallet-payment internalization writes `BasketName = "default"` and `Change: true`. **Never deposit via `BasketInsertion` into `reserve`** — that writes `Change: false` and the satoshis are **permanently unspendable**.

`InternalizeAction` is on the `TimeoutWallet` adapter surface (§4.1).

**Test:** a table test asserts vout resolution succeeds when the deposit is at index 2 of a 4-output transaction, and fails cleanly when absent.

### 9.3 Deposit sizing

See §8.12: **1 500 000 sat (0.015 BSV) standing, topped up weekly**, deliberately overriding the fuel derivation's 0.1 BSV recommendation because the exposure is **delegated spending authority** (§12.1, §18.2).

---

## 10. Failure handling, idempotency, and the publish leg

### 10.0 The `CreateAction` arguments — normative

Every argument the publish leg depends on is decided here, in one place. `internal/publisher` builds exactly this and nothing else; two of these fields are the difference between correct and silently corrupted records, and neither has a safe default.

```go
wallet.CreateActionArgs{
    Description: fmt.Sprintf("Weather data storage (%d outputs)", len(recs)),
    Outputs: []wallet.CreateActionOutput{ // one per record, in claim order
        {
            Satoshis:          0,
            LockingScript:     encoded,   // §7 encoder output
            OutputDescription: "weather",
            Basket:            "",        // no basket: these are not our UTXOs to track
        },
        // ...
    },
    Labels: []string{"weather", "batch-" + claimRef.String()},
    Options: &wallet.CreateActionOptions{
        RandomizeOutputs:       false,
        ReturnTXIDOnly:         true,
        AcceptDelayedBroadcast: false,
        NoSend:                 false,
    },
}
```

| Field | Value | Why it is not a free choice |
|---|---|---|
| `Description` | `"Weather data storage (N outputs)"` | TS parity (`src/service/transaction.ts:66`). Human-readable in wallet action listings; the reconciler and adopt-check match on **labels and txid**, never on this string. |
| `OutputDescription` | `"weather"` | TS parity (`src/service/transaction.ts:59`). |
| `Satoshis` | `0` | An `OP_FALSE OP_RETURN` output is provably unspendable; the server's funder pays the fee from fuel. |
| `Basket` | empty | A basket insertion would ask the wallet to track 0-satoshi unspendable outputs as UTXOs. |
| `Labels` | `["weather", "batch-<claim_ref uuid>"]` | `weather` is the reconciler's filter (§10.6); `batch-<uuid>` is the **idempotency key** the adopt-check queries (§10.2). Both are written in the same server-side DB transaction as the action row. |
| `RandomizeOutputs` | **`false`** | Defaults to **`true`** (`mapping_create_action_args.go:108`) and `CreateActionResult` carries **no** output-index mapping, so leaving it default persists vouts that do not match the chain — corrupting `blockchain.outputIndex` on every record with no error anywhere. |
| `ReturnTXIDOnly` | **`true`** | Decided here, and §12.3 and §17.2 test 22 both depend on it: the result carries no outputs, which is precisely why a client-side pre-sign script check is *not possible today* and why the vout must be validated by a sampled runtime check instead of by the response. |
| `AcceptDelayedBroadcast` | **`false`** | §10.6 — synchronous accept is what lets an ARC rejection surface as a classifiable `CreateAction` error rather than a `completed` row on a txid that will never exist. |
| `NoSend` | `false` | There is no batching-across-actions story here; a `NoSend` action would need an explicit later `SendWith` that nothing in this design issues. |

`Inputs`, `InputBEEF` and any funding argument are **absent by construction** — that absence is what puts the action on the server's throughput funding path.

Because a weather transaction at D=50 **never** receives a change output (§8.6), the on-chain output set is exactly the N data outputs at indices `0..N-1`, in claim order.

### 10.1 What `FOR UPDATE SKIP LOCKED` does and does not fix

It removes **concurrent** claims of the same row — and per §5.0 that is not an optimisation, it is the **sole replacement** for a protection the TypeScript has only by accident.

It does **not** make publishing idempotent across crashes. Do not write, in code comments or docs, that it "structurally removes the double-broadcast bug" — it removes exactly one of the two mechanisms.

The remaining mechanism: any interruption between `CreateAction` committing **server-side** and `store.Complete` landing — a 60 s client timeout on a response that was actually committed, SIGKILL during a rollout, ctx cancel at shutdown — strands rows in `processing`. The reaper returns them to `pending`, and the next tick re-encodes and re-publishes the same readings: duplicate OP_RETURNs on mainnet, double fuel burn, the first txid lost, duplicate cards in the UI.

### 10.2 The fix: batch labels and adoption

| Step | Mechanism |
|---|---|
| **Claim** | `UPDATE weather_records SET status='processing', claimed_at=now(), claim_ref = COALESCE(claim_ref, gen_random_uuid()) WHERE id IN (SELECT id FROM weather_records WHERE status='pending' ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING *` — a **new** uuid for fresh rows, the **prior** uuid for reaped rows. |
| **Adopt-check** | Before publishing, group the claimed rows by distinct **prior** `claim_ref` (rows where `adopt_required` is true). For each, call `ListActions{Labels: ["batch-<uuid>"], IncludeOutputs: true}`. If an action exists: **adopt it** — re-encode each row and match its locking script against the action's outputs to recover the real vout, then `Complete(id, action.txid, vout)`. Rows whose script is absent from the action fall through to a fresh batch. **Ignore any matched action whose status is `aborted` or `failed` — treat it as absent and fall through to a fresh batch.** The requeue path (§10.5) deliberately reaches this case: a row driven to `failed` by the reconciler still carries the `claim_ref` of a real, aborted action, and adopting it would mark the row `completed` on a dead txid. |
| **Publish** | `CreateAction{..., Labels: ["weather", "batch-<uuid>"]}` — the full argument block is §10.0. The label is written in the same server-side DB transaction as the action row, so an adopted action is either fully visible with its label or does not exist. |
| **Complete** | `Complete(id, txid, vout)` clears `error`, sets `processed_at` and `chain_status = 'arc-accepted'`, and in the **same Postgres transaction** bumps `app_stats` and the per-station counters (§4.2), then notifies the SSE hub. |

Identical scripts (two stations reporting byte-identical readings) are matched greedily against unassigned vouts; either assignment is correct because the bytes are the same.

### 10.3 Error classification — only permanent errors consume the attempt budget

The processor ticks every 3 s. A 30-second storage-server restart, one 60 s wallet timeout, or an empty fuel pool would otherwise burn all three attempts in **nine seconds** and mark the entire claimed backlog terminal `failed`, with nothing to retry it. That is the single most likely operational event destroying data faster than any human can respond. (The TypeScript's opposite failure — unlimited untracked retries with a poison row blocking the queue head forever — is §5 row 15.)

| Class | Members | `attempts` | Row outcome | Breaker |
|---|---|---|---|---|
| **Permanent** (record-specific) | encode failure (§7.5), script > cap (§7.6), server **validation** rejection of the output/action | **++** | terminal `failed` at 3, with the specific error string | no |
| **DoubleSpend** | `WERR_REVIEW_ACTIONS` with `reviewActionResults[].status == "doubleSpend"` — the `11ecfd5` shape, now arising from **server-selected fuel** rather than app-selected inputs (§5 row 9) | **unchanged** | retry the same batch immediately with fresh fuel, up to **5** attempts within the tick; then requeue `pending` | opens on the 5th failure |
| **Infra** | `wdk.ErrNotEnoughFunds`, `context.DeadlineExceeded` on a **connect/read before the request was sent**, 5xx, auth/session re-handshake failure, connection reset, DNS, Postgres transient | **unchanged** | requeued `pending` | **opens** |
| **Unknown** | `CreateAction` timeout or ctx cancellation **after the request was written** | **unchanged** | `status='pending'`, `adopt_required=true`, `claim_ref` preserved, `attempts` unchanged | **opens** |

**The Unknown row transition is owned by the classifier, not by the reaper.** A *classified* Unknown outcome means the process is alive and knows the outcome is ambiguous, so it writes `pending` + `adopt_required` **before the goroutine returns** — the next tick's adopt-check resolves it in seconds rather than after a 5-minute lease expiry. The reaper is the backstop for the case the classifier cannot cover: the process died before that write landed, leaving the row `processing`; after `PROCESSING_LEASE` the reaper performs the identical transition. Both paths converge on the same state, and neither is dead code. §16.1 states the same rule for the shutdown case.

**A `CreateAction` timeout is never classified `failed`.** The classifier lives in `internal/publisher/errors.go` and uses **typed errors** — never `error.Error()` string matching. A test asserts that a funding error never advances `attempts`.

### 10.4 Circuit breaker and lease arithmetic

- **Breaker**: opened by any Infra/Unknown outcome. While open the processor **stops claiming new batches** and backs off exponentially with jitter, **3 s → 60 s cap**. One successful publish closes it. Its state is a field in `/api/ops` and in the heartbeat line.
- **Lease vs timeout**: `PROCESSING_LEASE = 5 min` is **strictly greater** than the `CreateAction` per-call timeout of **60 s**, so the reaper can never reclaim a row whose publish is still in flight. **Config rule 14** (§8.15) enforces the inequality; a unit test asserts it.
- **Residual duplicate window (accepted, §18.1)**: a duplicate requires the adopt-check to report "no such action" while the action exists — i.e. a label-index bug, a different `userID` scope, or a `ListActions` failure being misread. `ListActions` failures are classified Infra and do **not** trigger a republish (the breaker opens instead), so the practical window is narrow but non-zero.

### 10.5 `weather requeue` — the recovery path

```
$ weather requeue --status failed --since 24h [--station 12345] [--limit 500] [--dry-run]
```

Sets `status='pending'`, `attempts=0`, `error=NULL`, **`adopt_required=true`**, and leaves `claim_ref` intact. Both of the last two are required for the stated rationale to hold: §10.2 gates adoption on `adopt_required`, so preserving `claim_ref` alone would run **no adopt-check at all**, and a row whose prior batch *did* publish would be broadcast a second time. With the flag set, the requeued row's prior `claim_ref` is looked up first — and if the matched action is `aborted`/`failed` (the reconciler's own reason for marking the row `failed`) the adopt-check treats it as absent and the row goes into a fresh batch. Prints the affected count. `--dry-run` prints without writing.

This is the answer to "the wallet ran out of money overnight and 300 readings went terminal".

### 10.6 Broadcast semantics and reconciliation

**Decision: `AcceptDelayedBroadcast = false`.** This restores the TS behaviour that commit `fdfcd7e` deliberately chose. At 0.0033 tx/s the throughput argument for delayed broadcast is moot, and synchronous accept means an ARC rejection (465 min-fee, malformed, etc.) surfaces as a `CreateAction` error the classifier can act on, instead of a row marked `completed` on a txid that will never exist. The deployed server sets `max_rebroadcast_attempts: 0`, so a queued-and-failed broadcast would never be retried.

`completed` therefore means **"ARC accepted the transaction"**, not "mined". The reconciler supplies the rest.

Every `RECONCILE_INTERVAL` (5 min): **first** query the store for candidate rows — `completed`, `processed_at` older than 10 minutes, `chain_status IS NULL OR chain_status <> 'mined'` — served by `ix_records_reconcile`. **Then** page the wallet: `ListActions{Labels: ["weather"], Limit: 200, Offset: …}` ordered newest-first, matching each returned action to a candidate **by `txid`**. Stop paging as soon as every candidate is resolved, or as soon as the page's oldest action predates the oldest candidate's `processed_at`. **Cap the scan at 5 pages per tick** (1 000 actions); unresolved candidates carry to the next tick. Each page gets the §3.2 per-call `ListActions` budget of **30 s** — that is a per-page timeout, not a per-tick one.

This is pinned because the naive reading is unbounded: every weather action keeps the `weather` label forever (~105 k/year at 288 polls/day), so an unpaged `ListActions{Labels:["weather"]}` grows without limit, while the other naive reading — one `ListActions` per candidate txid — is a different implementation with a different cost profile. Neither is what this design specifies.

| Wallet action status | Row `status` | Row `chain_status` | Notes |
|---|---|---|---|
| `unproven` / `unmined` / `sending` / `nosend` | `completed` (unchanged) | `unmined` | **Never demote to `failed`.** |
| `completed` / mined (merkle path present) | `completed` | `mined`, `mined_at` set, **`block_height` written, and `stations.last_block_height` updated** | Never demoted afterwards. **This is what makes `lastBlockHeight` a real field for the first time** (§2.2). |
| `aborted` | **`failed`** | `aborted` | Sets `error = "aborted on chain: <detail>"`. This repo added the `aborted` standardized status in `e25fb238`/`eea21d8a` precisely for this. |
| `failed` | **`failed`** | `aborted` | Same. |

Counters exported into the sampler snapshot: `minedCount`, `stillUnminedOlderThan1h`, `abortedCount`.

### 10.7 Poison-record isolation

Encode and size-check **every** record **before** issuing `CreateAction`, and partition the batch:

1. Records that fail `Encode` or exceed `WEATHER_MAX_SCRIPT_BYTES` are marked terminal `failed` **individually**, with the specific error, and **dropped from the batch**.
2. The remainder is published.
3. Batch-wide `attempts++` is reserved for errors that genuinely affect all rows.
4. **Backstop**: on a batch-level Permanent failure at `attempts == 2`, the processor falls back to **single-record publishes** for that batch, so one bad row cannot take twenty good ones terminal.

Without this, one malformed reading fails the whole publish, increments `attempts` on all 21, and after three ticks terminally fails twenty innocent readings — while the poison record returns to poison the next batch. **This is a real observed shape in the TypeScript**, where the poison row sits at the head of a `createdAt ASC` queue and blocks everything behind it forever (§5 row 15).

### 10.8 Duplicate ingestion prevention

- `UNIQUE (station_id, observation_time)` plus `INSERT ... ON CONFLICT DO NOTHING`. `observation_time = to_timestamp(data->>'time')` when `time > 0`, else the poll's `date_trunc('second', poll_started_at)` bucket (a station reporting no timestamp dedupes per poll instead). **Mongo has no unique index anywhere**, so nothing prevents duplicate rows today.
- `replicas: 1` with `strategy: Recreate` in the manifest, **with a comment stating why** (§15.3).
- The poller does an **immediate first poll at boot** plus ≤ 30 s jitter — a bare ticker leaves the UI empty for the first 300 s after every deploy.

### 10.9 Error policy for the errgroup (all eight members)

| Rule | Detail |
|---|---|
| Long-running loops return **only** `ctx.Err()` | Any other return value cancels the root context and kills the process. |
| Fatal errors are limited to **boot-time wiring** | Config, Postgres, HTTP bind, and the preflight validation rejection (§8.14). |
| All in-loop failures are counted and rate-limit-logged | Tempest 500, a whole-poll `getStations` failure, a failed `CreateAction`, a transient Postgres error, a keeper round error — WARN/ERROR, **never** process termination. |
| A goroutine returning `nil` before `ctx.Done` is **fatal** | A ticker loop that quietly exits would otherwise leave the app half-running with no signal. The errgroup wrapper converts an early `nil` into an error. |
| Poller error accounting (replaces the TS `errors <= 3` magic number) | Named fields: `stationsAttempted`, `stationsFailed`, `stationsRejected` (unparseable, §7.5); the first 3 failures logged in detail, the rest summarized in one line per poll. |

---

## 11. Observability and alerting

**Neither telemetry path a naive design would assume exists.** `infra-configmap.yaml` sets `tracing.enabled: false` with no `observability` block, so `wallet.utxo.pool.runway_seconds`, `wallet.utxo.reserve.balance_satoshis` and `wallet.funder.claims{result}` are **not exported today**; `apps/bsva-us-1/kustomization.yaml` does not include `../base/prometheus-stack`; the only cluster telemetry is Coralogix/Datadog agents shipping container **stdout**. Server-side gauges also aggregate across all tenants (`poolSnapshot` has no `user_id` filter), so they could never report this app's runway anyway.

Therefore **the app self-reports, and the monitors are deliverables in the same PR** (this is the condition on §5 row 11).

### 11.1 The heartbeat (the primary signal)

One structured `slog` line per 60 s sampler tick, stable keys, JSON handler:

```json
{"level":"info","msg":"heartbeat","component":"sampler",
 "fuelPoolOutputs":987,"reserveOutputs":0,"reserveSatoshis":0,
 "defaultBalanceSats":1421340,"lastKeeperMintAt":"2026-07-27T09:14:02.000Z",
 "pendingRows":0,"processingRows":0,"failedRows":0,"stillUnminedOlderThan1h":0,
 "walletConnected":true,"degraded":"","breakerOpen":false,"sseClients":3,
 "lastPublishAt":"2026-07-27T09:20:03.000Z","minedCount":2831,"abortedCount":0}
```

**Silence is the alarm.** This matters because `runOnce` returns with **no log line at all** when inventory ≥ low water — at this app's steady state, *quiet is the healthy keeper signature*. **The rule "alert on the absence of fuel top-up round log lines" is explicitly deleted**; it would fire continuously. Alert on the absence of the **heartbeat** instead.

Measurement rules:

| Field | How |
|---|---|
| `fuelPoolOutputs` | cheap `ListOutputs(fuel)` **`TotalOutputs`** — **never a page length** (§5 row 5). Logged as an **upper bound**: it counts `sending`-status fuel the funder cannot yet claim (§18.7). |
| `reserveSatoshis` | **actually list** the reserve basket with an explicit `Limit: 100` and **sum `Satoshis`**. `countBasketOutputs` uses `Limit=1`/`TotalOutputs` and carries no values. Assert the page is complete; log a distinct warning if truncated. |
| `defaultBalanceSats` | `Balance()` / `ListOutputs(default)` sum. |
| row counts | one `count(*) FILTER (...)` query. |

### 11.2 `/api/ops`, `/api/health`, `/api/ready` — three endpoints, three jobs

Conflating them is how a Postgres restart becomes a CrashLoopBackOff.

| Endpoint | Listener | Semantics |
|---|---|---|
| `GET /api/health` | `API_PORT` (3001) | **Liveness only, and process-only.** No DB ping, no wallet check, no fuel state. Returns `{"status":"ok","timestamp":"…Z"}` — exactly the two keys and shape the TS already returns at `src/api/index.ts:45-54` — unconditionally while the process is up. Nothing in the frontend calls it; it exists for kubelet. **Registered before the rate limiter** (§6.1 defect 3). |
| `GET /api/ready` | `API_PORT` (3001) | **Readiness.** A fast `pgxpool.Ping`; **503** when the DB is unreachable, 200 otherwise. New; nothing in the frontend calls it. |
| `GET /api/ops` | **`OPS_PORT` (9090), cluster-internal only** | The full sampler snapshot — the heartbeat object plus `walletConnected` and `lastPublishAt`. Not consumed by the SPA. |

**Why liveness must not touch the DB.** Postgres is a single-replica `strategy: Recreate` Deployment, so every routine node drain, image bump or restart makes the DB briefly unreachable. If liveness pinged the DB, kubelet would restart the app pod on **every one** of those events — converting an accepted seconds-long read outage into a CrashLoopBackOff that **also stops the wallet-free Tempest poller**, i.e. it would drop weather observations for a DB event that has nothing to do with polling. **A DB outage must remove the pod from the Service (readiness), never restart it (liveness).** There is precedent for the split in this Flux repo (`apps/base/faucet/_base/deployment.yaml:44-56`).

**Why `/api/ops` is not on the public port.** §15.5 splits the single Ingress on prefix `/api`, so **anything registered on `:3001` under `/api` is world-reachable at the public hostname.** `/api/ops` publishes `defaultBalanceSats`, `reserveSatoshis`, `fuelPoolOutputs`, `degraded`, `breakerOpen`, `lastKeeperMintAt` and row counts — exactly the signal an attacker would use to time abuse of the unauthenticated `/api/proof` and `/api/verify` amplifiers. Binding it on a second `http.Server` on `OPS_PORT` keeps it off the routed port entirely, and `weather-proof-back.yaml`'s Service **deliberately does not publish port 9090**. Reach it with `kubectl port-forward`. If a future change must put it back on `:3001`, it needs an explicit Ingress-level deny or a shared-secret header, stated here.

### 11.3 Alarms (authored in the same PR)

**Destination:** the Coralogix alert webhook already used for go-wallet-toolbox alerts (Slack). **Owner:** D. Kellenschwiler (d.kellenschwiler@bsvassociation.org). Definitions live in `docs/alerts/weather-proof.md` and are created in Coralogix as part of the deploy checklist.

| # | Condition | Severity |
|---|---|---|
| 1 | Heartbeat line absent for 5 min | page |
| 2 | Keeper round logs `minted=0` while `fuelPoolOutputs < 600` (low water) | page |
| 3 | The keeper's `chunk fan-out failed, continuing with provisioned chunks` WARN appears | page |
| 4 | `reserveSatoshis` below two chunk values (12 000) **while a refill is in progress** | warn |
| 5 | `defaultBalanceSats < 250000` (≈ 4 days) | page |
| 6 | `failedRows` increasing over 15 min | warn |
| 7 | `stillUnminedOlderThan1h > 0` | warn |
| 8 | `script_too_large` appears in any log line | warn |
| 9 | `breakerOpen` true for more than 10 min | page |
| 10 | HTTP 429 rate exceeds 1 % of `/api` requests over 15 min (the limiter is mis-keyed or the limits are too tight — §6.1) | warn |
| 11 | `sseClients` at the global cap (503s being returned) | warn |

Two states are dangerous **because they do not error**: an unfunded keeper (WARN + `round complete minted=0`, `runOnce` returns `nil`) and the funder's transparent fallback from fuel to `default` (a WARN emitted on the **storage server**, not in this app). Alarms 2, 3 and 5 exist specifically for them, and they are the successors to the deleted `sendError('CRITICAL: Insufficient funds in wallet')` (§5 row 11).

`store.Snapshot` (the counts query) has exactly one consumer: the sampler. If that ever stops being true, delete it from the interface.

---

## 12. Security and trust boundary

### 12.1 State the residual risk honestly — the new scheme is **not** safer than the hash puzzle

On current `main` of go-wallet-toolbox, **the storage server signs storage-supplied output scripts verbatim.** `pkg/internal/assembler/create_action_tx_assembler.go` (`:28`, `:204-227`) is never given `args.Outputs`; for any output not labelled `ProvidedBy=storage` / `Purpose=change`, it signs the supplied `lockingScript` and `satoshis` **as-is** — and the `isChange` classifier is itself storage-controlled. This is the **CVE-2026-56744 / GHSA-36f9-7rg5-cpf8** class and it is **UNFIXED on main**.

Consequences that must appear in the code comments and in the PR description, not only here:

1. **Do not write** "the funding inventory was only as safe as the storage server" as a description of a *past* defect. The hash-puzzle scheme's weakness (anyone-can-spend-once-revealed, preimages held only in the old server's `customInstructions`) is real, but the replacement has its own delegation.
2. **Do not write** "the storage server never holds the keys" as a safety argument. The key stays in-process, but its **spending authority is effectively delegated**: a compromised or buggy storage server can redirect the entire standing float and every claimed fuel/chunk input, on mainnet, under a reused funded identity.
3. **The new scheme moves the same trust; it does not remove it.**

### 12.2 Sizing the exposure deliberately

Because the trust is delegated, the float is kept in **days**: standing deposit **1 500 000 sat (0.015 BSV)**, topped up weekly; maximum blast radius including the pool and a transient reserve ≈ **1 580 000 sat** (§8.12). This is a deliberate reversal of the fuel derivation's own "deposit 0.1 BSV / 160 days" recommendation, and §8.12 keeps that arithmetic so the reversal is quantified rather than asserted.

### 12.3 The options for actually closing it (recorded, not silently assumed)

| Option | Where | Status |
|---|---|---|
| Land exact-match verification in the assembler upstream: thread `args.Outputs` in and compare script + satoshis before signing | `go-wallet-toolbox` | **Recommended**; out of scope for this port; raise as a separate issue and link it from `docs/RUNBOOK.md` |
| Client-side pre-sign check in this app | `internal/publisher` | Not possible today — with `ReturnTXIDOnly: true` (§10.0) the result carries no outputs to check. Would require `ReturnTXIDOnly: false` and a full BEEF parse per publish. **Not adopted**; the cost/benefit is poor while option 1 is open. |
| Keep the float small | §12.2 | **Adopted.** |

### 12.4 Known weakness in the verification story

The SPA's confirmation path proves transaction **inclusion**, never that the on-chain `OP_FALSE OP_RETURN` script matches the displayed JSON — and at HEAD it does not even prove inclusion cryptographically: `POST /api/verify` asks WhatsOnChain for a block height and the browser trusts the answer. **No merkle proof crosses the wire to the browser on any live path today** (§13.6), while the landing page advertises "Merkle proof valid". Real end-to-end verification requires the frontend to fetch the BEEF, verify the merkle path against a header source, decode the output script and diff it against `data`. Out of scope; recorded here so nobody claims otherwise, and §13.6 keeps `GET /api/proof/{txid}` alive precisely so the path stays open.

### 12.5 Secret handling

`SERVER_PRIVATE_KEY`, `TEMPEST_API_KEY` and `POSTGRES_PASSWORD` are the `config.Secret` type:

```go
type Secret string
func (s Secret) String() string               { return "[REDACTED]" }
func (s Secret) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (s Secret) Reveal() string               { return string(s) }   // the only accessor
```

- `Validate()` error messages **never echo the offending value** — a test scans every produced error string for configured key material.
- If config is logged at startup it logs an **explicit field allowlist**, never the struct (`slog` would otherwise print the whole thing).
- **Both private keys currently committed to this repo are compromised; neither may be reused**, and the published back image ships one on disk today (§2.1, §6.6).

---

## 13. The API the redesigned frontend needs

**This is not an immutable contract.** The byte-for-byte-parity constraint of earlier drafts is **released**: key **order** is irrelevant everywhere (everything goes through `JSON.parse` and is read by name — the TS backend already emits the stats object in two *different* orders on two endpoints and the frontend is indifferent), and the frontend may be edited. What follows distinguishes **what the redesigned frontend genuinely requires** — where a wrong value visibly breaks or crashes the UI — from **what the old API happened to return**.

**There is no byte-parity concern anywhere in this project.** The HTTP layer is not a frozen contract, and neither is the on-chain encoder: its format is internal to the backend and parity with the historical TypeScript output is explicitly **not** a requirement (§7.0). The encoder's real constraints are validity, determinism, the 297-byte cap and a round-trip — none of which involve the old bytes.

### 13.0 Routes and the endpoint set

`frontend/src/App.tsx:41-48` declares exactly four routes:

```tsx
<Routes>
  <Route path="/" element={<LandingPage />} />
  <Route element={<ExplorerLayout />}>
    <Route path="/explorer" element={<Dashboard />} />
    <Route path="/station/:stationId" element={<StationRecords />} />
    <Route path="/weather/:id" element={<WeatherDetail />} />
  </Route>
</Routes>
```

| Route | Backend calls |
|---|---|
| `/` (LandingPage) | **ZERO.** Every number and every proof shown is a hardcoded literal (`LiveStatsBar.tsx:3-8`: `'19+'`, `'50,000+'`, `'1M+'`, `'5 min'`; `ProofDemoCard.tsx:42,80-81`: `TxID: a3f8c1...9d2e`, `Block #879,412`, "Brix and Columns Vineyard", 18.3 °C). It does **not** call `useLiveStats` or `useDashboard`. |
| `/explorer` (Dashboard) | `GET /api/stations?page&limit=50&search` + SSE `GET /api/events`. Prefetches the next page of the same endpoint; on row hover prefetches `GET /api/stations/:id` **and** `GET /api/weather?stationId&page=1&limit=20`. |
| `/station/:stationId` | `GET /api/stations/:stationId` + `GET /api/weather?stationId&page&limit=20`, then `POST /api/verify` for unconfirmed txids on that page. Prefetches the next records page and, on row hover, `GET /api/weather/:id`. |
| `/weather/:id` | `GET /api/weather/:id` + `POST /api/verify` with a single txid. |

**Complete backend call inventory** (grep-verified exhaustive, from `grep -rn "EventSource\|fetch(" frontend/src`):

| # | Call site | Method + path | Live? |
|---|---|---|---|
| 1 | `services/api.ts:22` | `GET ${API_BASE}/api/weather?${searchParams}` | LIVE |
| 2 | `services/api.ts:35` | `GET ${API_BASE}/api/weather/${id}` | LIVE |
| 3 | `services/api.ts:51` | `GET ${API_BASE}/api/proof/${txid}` | **DEAD — no caller** |
| 4 | `services/api.ts:76` | `GET ${API_BASE}/api/stations?${searchParams}` | LIVE |
| 5 | `services/api.ts:98` | `POST ${API_BASE}/api/verify` | LIVE |
| 6 | `services/api.ts:115` | `GET ${API_BASE}/api/stations/${stationId}` | LIVE |
| 7 | `hooks/useLiveStats.ts:27` | `new EventSource(${API_BASE}/api/events)` | LIVE |

`API_BASE = import.meta.env.VITE_API_URL || ''` is defined **twice, independently** (`services/api.ts:3`, `hooks/useLiveStats.ts:5`). Both must be satisfied by the same origin.

**So the Go backend serves:**

| Endpoint | Status |
|---|---|
| `GET /api/stations` | required |
| `GET /api/stations/{stationId}` | required |
| `GET /api/weather` | required |
| `GET /api/weather/{id}` | required |
| `POST /api/verify` | required |
| `GET /api/events` (SSE) | required |
| `GET /api/health` | required for kubelet, never called by the frontend |
| `GET /api/ready` | new, kubelet only |
| `GET /api/proof/{txid}` | **kept deliberately** — see §13.6 |
| `GET /api/ops` | `OPS_PORT` only, cluster-internal |

**Two routing tolerances that are not optional.** Both `services/api.ts:22` and `:76` hardcode the `?` in the template literal, so **`GET /api/weather?` and `GET /api/stations?` with an EMPTY query string are normal requests** — the Go router must not 400 or 404 on a bare trailing `?`. And Go 1.22 `ServeMux` does not match a trailing slash against an exact pattern, so register **both** `GET /api/weather` and `GET /api/weather/{$}` to the same handler (likewise for `/api/stations`). Do not rely on redirects.

**SPA fallback.** `BrowserRouter` over four paths means the static server **must** rewrite unknown paths to `index.html` (`serve -s` does this today via `--single`; the nginx replacement uses `try_files`). The route move `/` → `/explorer` is the only API-visible change the redesign made.

### 13.1 The hard crash list

The SPA has **no error boundary**. These are the values where a wrong answer white-screens a page rather than degrading:

| # | Violation | Effect |
|---|---|---|
| 1 | `station.txRecords` `null` or absent in the **stations list** | `station.txRecords.toLocaleString()` at `Dashboard.tsx:287` is **not** optional-chained → TypeError in render → the page blanks. **Required non-null number.** |
| 2 | `station.lastTemp` **key omitted** | The test is `station.lastTemp !== null ? \`${station.lastTemp}°C\` : '—'` — a **strict `!== null`**. An omitted key is `undefined`, `undefined !== null` is **true**, and the UI renders the literal string **"undefined°C"**. `lastTemp` **must be present and explicitly `null`** when unknown. **No `omitempty`.** |
| 3 | `record.blockchain` missing or `null` | `record.blockchain.txid` is accessed unguarded → throws. `blockchain` must always be a **nested object**; flattening it to `txid`/`block_height` would require frontend edits. |
| 4 | Any of the 24 rendered `data` fields absent or non-numeric on the **detail** page | `formatTemp(data.air_temperature)` calls `.toFixed(1)` at the very top of the card, and `fieldGroups` iterates 24 named keys calling `.toFixed()`, `Math.round()`, `.toString()` or raw interpolation. A TypeError inside render white-screens `/weather/:id`. |
| 5 | A `POST /api/verify` response value that is `null` | `useAutoVerify.ts:68` does `for (const [txid, { confirmed, blockHeight }] of Object.entries(results))` — destructuring `null` **throws**. Every key present must map to an **object**. |

The 24 required `data` keys, by how they are consumed:

| Consumption | Keys |
|---|---|
| **`.toFixed()` — required numeric** | `air_temperature`, `feels_like`, `dew_point`, `wet_bulb_temperature`, `wet_bulb_globe_temperature`, `delta_t`, `station_pressure`, `sea_level_pressure`, `air_density`, `wind_avg`, `wind_gust`, `uv`, `precip_accum_local_day`, `precip_accum_local_yesterday` |
| **`Math.round()` — required numeric** | `relative_humidity` |
| **`.toString()` — required numeric** | `lightning_strike_count_last_1hr`, `lightning_strike_count_last_3hr` |
| **raw interpolation — required numeric** (missing renders `"undefined"` but does not throw) | `wind_direction`, `solar_radiation`, `brightness`, `precip_probability`, `precip_minutes_local_day`, `precip_minutes_local_yesterday`, `lightning_strike_last_distance` |
| **required string** | `conditions`, `pressure_trend`, `wind_direction_cardinal`, `lightning_strike_last_distance_msg` (the last has an `\|\| 'N/A'` fallback) |
| **Never read by any live component** | `time`, `is_precip_local_day_rain_check`, `is_precip_local_yesterday_rain_check`, `lightning_strike_last_epoch`, `icon` (`icon` is read only by the dead `WeatherCard`) |

Those last five are genuinely incidental to the UI — **but they are part of the 33-field on-chain encoded payload and part of the `totalDataPoints = records × 33` derivation, so they must not be dropped from storage or from the encoder.**

**Invariant, tested:** all 33 `WeatherData` keys and all three `blockchain` keys always serialize; **no `omitempty` on any field in this crash list**.

### 13.2 `GET /api/weather` → 200

Request builder (literal, `services/api.ts:8-29`):

```ts
const searchParams = new URLSearchParams();
if (params.page)      searchParams.set('page', params.page.toString());
if (params.limit)     searchParams.set('limit', params.limit.toString());
if (params.status)    searchParams.set('status', params.status);
if (params.stationId) searchParams.set('stationId', params.stationId.toString());
const url = `${API_BASE}/api/weather?${searchParams}`;
```

Live callers only ever send `stationId` + `page` + `limit=20`. `status` is sent only by the dead `WeatherList` component. `stationId=0` would be silently dropped (falsy) — a latent edge case, not a real one.

| Param | Handling |
|---|---|
| `page` | `Atoi` + err branch → default 1; clamp ≥ 1. **`pagination.page` echoes the effective clamped page.** |
| `limit` | `Atoi` + err branch → default 20; clamp `[1, 100]` |
| `status` | allowlist of `pending\|processing\|completed\|failed`; **400** on an unknown value |
| `stationId` | `Atoi` + err branch → ignored on error; range-checked |

Response:

```json
{"items": [ {
  "id": "0192f0c1-…",
  "stationId": 12345,
  "timestamp": "2026-04-17T15:40:00.000Z",
  "data": { …all 33 weather fields… },
  "blockchain": {"txid": "<64 hex>|null", "outputIndex": 0, "blockHeight": 879412},
  "status": "completed",
  "createdAt": "2026-04-17T15:40:00.123Z",
  "processedAt": "2026-04-17T15:40:04.000Z"
} ], "pagination": {"page":1,"limit":20,"total":2701,"totalPages":136}}
```

Non-obvious requirements:

| Requirement | Why |
|---|---|
| **`items` order is load-bearing: sort `created_at DESC, id DESC`.** | `sortKey` initialises to `null` and `sortedRecords` returns `records` **unchanged** when `sortKey` is null, so the default view is exactly **server order**. The TS backend sorts `{createdAt: -1}` (`weather.ts:39`). Sorting ascending in Go would silently show oldest-first on every station page. The `id` tiebreaker makes offset pagination stable under batch inserts. |
| `timestamp` must be a **lexicographically sortable fixed-width UTC string** | `StationRecords.tsx:151` sorts with `a.timestamp.localeCompare(b.timestamp)`. A mixed-offset or variable-precision format sorts wrong. Use the `isoMillis` type (§13.8). |
| `totalPages` is **0 when `total` is 0** | Otherwise the Next button locks up. |
| `blockchain` is never `null`; its three keys are always present | Crash list item 3. `txid`/`blockHeight` are `null` until known; `outputIndex` is `null` until published (rendered raw, and `null` renders as nothing — harmless). |
| **No `error` key in list items** | The TS list route does not emit it and nothing in the list UI reads it. Keep the asymmetry. |
| `pagination.limit` | Never read by any component (Dashboard/StationRecords read `page`/`total`/`totalPages` only). Harmless to keep; kept for symmetry. |

### 13.3 `GET /api/stations` → 200

Request: `GET /api/stations?page=<n>&limit=50&search=<str>`, params appended only when **truthy**. Dashboard always sends `limit=50` (`PAGE_SIZE`) and `page ≥ 1`; `search` is omitted when empty.

| Param | Handling |
|---|---|
| `page` | `Atoi` + err → default 1; clamp ≥ 1 |
| `limit` | `Atoi` + err → default 50; clamp `[1, 200]` |
| `search` | trimmed. **ONE parsed value drives both filter and sort** — this is what fixes the `?search=0` 500 (§5 row 18): if it parses as an integer → exact `station_id = $1`, sort `station_id ASC`; else → `websearch_to_tsquery('english', $1)` against `search_tsv`, sort `ts_rank` DESC then `station_id ASC`. Default (no search): `station_id ASC` |

Response — **three required top-level keys**, read as `data?.stations ?? []`, `data?.stats`, `data?.pagination`:

```json
{
  "stats": {"activeStations": 19, "totalTx": 2701, "lastRecordWrite": "2026-04-17T15:42:19.123Z", "totalDataPoints": 1690722},
  "stations": [{"stationId": 12345, "name": "…", "location": "…", "status": "online",
                "lastReading": "2026-04-17T15:40:00.000Z", "lastTemp": 18.3,
                "lastConditions": "clear", "txRecords": 2701, "lastBlockHeight": 879412}],
  "pagination": {"page": 1, "limit": 50, "total": 19, "totalPages": 1}
}
```

| Field | Constraint |
|---|---|
| `stats.activeStations`, `stats.totalTx` | `.toLocaleString()` → **non-null NUMBER** |
| `stats.totalDataPoints` | `formatLargeNumber(n)` = `n.toLocaleString()` → **non-null NUMBER**. `= total_records × 33` |
| `stats.lastRecordWrite` | `formatDateTime(ts)`: `if (!ts) return '—'`. **Nullable**; any `new Date()`-parseable string |
| `station.stationId` | number; React `key` and `navigate('/station/'+id)` |
| `station.name` | rendered raw; the backend substitutes `Station <id>` when blank |
| `station.location` | `\|\| '—'` — nullable/blank tolerated |
| `station.status` | **STRICT compare against the literal `'online'`.** Anything else renders as Offline; only `'online'`/`'offline'` are meaningful. **Derived:** `online` iff `is_active AND last_reading >= now() − 3 × POLL_RATE` (15 min) |
| `station.lastReading` | `formatDateTime` — nullable |
| `station.lastTemp` | **crash list item 2** — present and explicitly `null` |
| `station.lastConditions` | `\|\| '—'` — blank tolerated |
| `station.txRecords` | **crash list item 1** — required non-null number |
| `station.lastBlockHeight` | **Not read by the Dashboard at all** (only by `StationRecords`, and there `??`-chained). Nullable. Incidental in this response; kept for symmetry |
| `pagination` | `total.toLocaleString()`, `total !== 1` (pluralisation), `page`, `totalPages`. The footer only renders when `totalPages > 1` |

**Stats semantics.** `totalTx` = the honest count of **distinct on-chain transactions** and `totalDataPoints` = **completed records × 33**, fixing the two HEAD bugs of §2.2. `activeStations` is a live `count(*) FROM stations WHERE is_active`, so it can never be stuck at 0. The magnitude of the `totalTx` tile drops ~19× relative to the TypeScript; that is the point, and the database restarts at zero anyway (§14, §18.3).

Note `pagination.total` from the *records* call takes precedence over `station.txRecords` for the "Total Records" tile on the station page, so a stale `txRecords` counter is invisible there but **is** visible on the Dashboard — which is why §10.2 bumps it inside the same Postgres transaction as `Complete`, and why `weather stats-recompute` exists.

### 13.4 `GET /api/stations/{stationId}` → 200

The client parses the URL param itself (`const stationId = stationIdParam ? parseInt(stationIdParam, 10) : undefined`) and the query is `enabled: !!stationId`, so stationId 0 is never fetched.

Response is a **bare `StationSummary` object** — the same nine fields as one array element of `/api/stations`.`stations`, **not wrapped**:

```json
{"stationId":12345,"name":"…","location":"…","status":"online","lastReading":"…Z",
 "lastTemp":18.3,"lastConditions":"clear","txRecords":2701,"lastBlockHeight":879412}
```

Everything here is read through optional chaining / nullish coalescing (`station?.txRecords?.toLocaleString() ?? '—'`, `station?.lastBlockHeight ?? records.find(...)?.blockchain.blockHeight ?? null`), so this endpoint is far more forgiving than the list. Emit the same non-null discipline anyway, so one DTO serves both.

**404 handling:** `if (response.status === 404) throw new Error('Station not found')`. Any other non-2xx becomes `Failed to fetch station: ${response.statusText}`, so emit a real HTTP reason phrase or the message reads oddly (cosmetic only). `Atoi` failure or an out-of-`int32` value → **400**; unknown id → **404**.

### 13.5 `GET /api/weather/{id}` → 200 — the one endpoint that can crash the page

`{id}` is the opaque `record.id` string echoed back from the list. The frontend never parses it — it only interpolates it into the URL and renders it as text — so **uuidv7 text is fine**.

Response: a **bare `WeatherRecord`** (no envelope), the same shape as a list item **plus a trailing `error`**:

```
{ id, stationId, timestamp, data, blockchain: {txid, outputIndex, blockHeight}, status, createdAt, processedAt, error }
```

Read sites: `record.blockchain.txid ?? null` (feeds `useVerification` and gates the whole Blockchain Record card); `record.blockchain.outputIndex` (rendered raw); `record.blockchain.blockHeight ? .toLocaleString() : verificationResult?.blockHeight ? … : 'Not confirmed yet'`; `record.stationId` (back-link); `record.timestamp`/`record.createdAt` → `new Date(x).toLocaleString()`; `record.processedAt` and `record.error` (rendered only when truthy); `record.status`.

**`data` is the crash surface** — crash list item 4 and the 24-key table of §13.1.

Because a weather transaction never gets a change output (§8.6), `outputIndex` is always a stable `0..N-1` — worth stating because the explorer surfaces it verbatim.

**Do not reproduce the TS 500-vs-404 wart:** an unparseable id makes `findById` throw a `CastError`, which the catch turns into HTTP **500**, not 404, while the frontend only special-cases 404. **Go returns 400 for a syntactically invalid id and 404 for an unknown one; never 500.** 404 body: `{"error":"Weather record not found"}`.

### 13.6 `POST /api/verify` → 200, and `GET /api/proof/{txid}`

#### `POST /api/verify` — a bare top-level map keyed by the verbatim txid

Request (literal, `services/api.ts:85-109`): `{"txids": ["<64 hex>", …]}`. Batch size: up to 20 from `useAutoVerify` (one page of station records, deduped via `[...new Set(...)]`), exactly 1 from `useVerification`.

Response is a **BARE JSON OBJECT at the top level** — no envelope, no array:

```ts
Promise<Record<string, VerifyResult>>   // VerifyResult = { confirmed: boolean; blockHeight: number | null }
```

Two consumers impose two distinct constraints:

| Consumer | Constraint |
|---|---|
| `useVerification.ts:55,:79` — `const { confirmed, blockHeight } = results[txid] ?? { confirmed: false, blockHeight: null }` | **The handler MUST echo the request txid VERBATIM as the map key.** Do not normalise case, do not canonicalise. In practice txids reaching the client are lowercase because the backend generated them, so this is a contract requirement rather than an active bug — but a Go implementation that upper-cased keys would silently report **every** tx as unconfirmed. The `?? {…}` guard means a **missing** key degrades gracefully to unconfirmed. |
| `useAutoVerify.ts:68` — `for (const [txid, { confirmed, blockHeight }] of Object.entries(results))` | **No value may be `null`** — crash list item 5. And `if (confirmed && blockHeight)` means **`blockHeight: 0` is treated as unconfirmed**, so **0 must never be used as a sentinel — use `null`.** |

**Side effect the frontend relies on:** the backend **persists** `blockHeight` for confirmed txids, because `useVerification` patches only the React Query cache and then `invalidateQueries({queryKey:['weather','list']})` — the refreshed list must come back with `blockHeight` already set or the auto-verify loop **re-fires forever** (`attempted` is a per-mount ref, so it does not survive navigation). Persist it in a **Postgres transaction** together with `stations.last_block_height` — which is also what makes `lastBlockHeight` a real field for the first time (§2.2). **Note the Mongo path is broken today** (§5 row 17), so assume no existing `blockHeight` data exists anywhere.

400 semantics to preserve: empty/non-array `txids` → `{"error":"Body must contain a non-empty txids array"}`; any element failing `^[a-fA-F0-9]{64}$` → `{"error":"All txids must be 64-character hex strings"}`. Plus the new cap: more than 100 → 400. The frontend does not read the error body — it throws `Failed to verify transactions: ${response.statusText}` on any non-2xx, which `useAutoVerify` swallows (and un-marks `attempted`, so it retries) and `useVerification` surfaces as a red "Verification error" line.

Upstream: `GET <WOC_BASE>/tx/<txid>`, field name lowercase **`blockheight`**, mapped as `data.blockheight > 0 ? data.blockheight : null`. All the hardening of §6.2 and §6.3 applies.

**Security invariant restated:** the caller chooses only **which existing rows get refreshed**, never the `blockHeight` **value**.

#### `GET /api/proof/{txid}` — kept, deliberately

**It is dead code from the UI's perspective today.** `fetchBeefProof()` has **zero callers**; `services/verify.ts`'s `checkBeefConfirmation` and `verifyWeatherProof` have zero callers; `@bsv/sdk` (resolved 1.10.3) is imported **only** at `services/verify.ts:1` — i.e. only by that dead code. The only live use of that module is `getNetwork()` choosing a WhatsOnChain hostname for an anchor href. **The live confirmation path is 100 % server-side.**

**Decision: keep the endpoint, and serve BRC-95 atomic BEEF.** One line of why: it is the only endpoint that substantiates the product's own proof claim, and reviving it later is a small frontend change — whereas deleting it makes real SPV a backend project again. It is **not** on any required path, and its absence would not break the SPA.

| Case | Status | Body |
|---|---|---|
| Not 64 hex | 400 | `{"error":"Invalid transaction ID format"}` |
| **Txid not present in `weather_records`** | 404 | `{"error":"Transaction not found or not yet mined"}` — **no outbound call is made** |
| Services returned nothing | 404 | `{"error":"BEEF proof not found for transaction"}` |
| Upstream error | 500 | `{"error":"Failed to fetch BEEF proof"}` |
| Success | 200 | `{"txid":"<echo>","beef":"<lowercase hex>"}` |

**Unmined behaviour, pinned:** if a BEEF can be assembled without a merkle path, return **200** with it; only return 404 when no BEEF exists at all. That lets a future client verifier improve as soon as the BUMP appears.

**Atomic BEEF is mandatory, and here is the latent bug it fixes.** The dead client calls `Transaction.fromHexBEEF(beefHex)` with **no txid argument**. In `@bsv/sdk` 1.10.3 that resolves the subject as `txid ?? b.atomicTxid ?? b.txs.slice(-1)[0].txid`. Because the TS backend serialises with `beef.toBinary()` (plain BEEF, `atomicTxid === undefined`), resolution falls through to **the LAST tx in the BEEF's tx list** — which is only the requested tx *by convention of the topological sort*, not by construction. If the BEEF ever carries more than one leaf, the client silently verifies the **wrong transaction**: `tx.merklePath` and `tx.id('hex')` belong to a different tx than the one requested, and `verifyWeatherProof` returns `{verified: true, txid: <wrong txid>}`. **Two fixes, and we do both:** the backend emits `beef.AtomicBytes(txidHash)` (BRC-95: `0x01010101` prefix + 32-byte reversed subject txid), which makes `fromAtomicBEEF`/`fromHexBEEF` resolve deterministically and **error loudly** on mismatch; and §13.9 changes the frontend call to `Transaction.fromHexBEEF(hex, txid)`. **Do not re-implement the ambiguity in Go.**

**Hardening** (this route is an unauthenticated proxy on fully attacker-controlled input): store-gating first (the partial index on `txid` supports it), a bounded LRU of 1 024 entries, positive TTL 10 min, **negative TTL 30 s**, `singleflight` per txid, a per-IP rate limit (`PROOF_RATE_LIMIT_PER_MIN`, default 60) and WoC 429 backoff. Without gating, an attacker iterating unique 64-hex values drives one `services.GetBEEF` per request, **exhausting the shared keyless WoC/ARC rate limit the storage server also depends on**, while growing an unbounded map.

### 13.7 `GET /api/events` — the SSE contract

Client (literal, `hooks/useLiveStats.ts:19-63`):

```ts
const es = new EventSource(`${API_BASE}/api/events`);
es.addEventListener('stats_update', (e: MessageEvent) => {
  const stats: DashboardStats = JSON.parse(e.data);
  setStatus('connected');
  queryClientRef.current.setQueriesData<DashboardResponse>({queryKey:['dashboard']},
    (old) => old ? {...old, stats} : old);
});
es.onopen  = () => setStatus('connected');
es.onerror = () => setStatus('disconnected');
return () => { es.close(); setStatus('disconnected'); };
```

| Requirement | Detail |
|---|---|
| **Named event** | The line MUST be `event: stats_update`. A default/unnamed `message` event is ignored entirely — the Live dot would stay green (`onopen` fires) while stats never update. |
| `data:` payload | A **single-line** JSON object parsed into `DashboardStats` = `{activeStations, totalTx, lastRecordWrite, totalDataPoints}` — the **same four fields** as `/api/stations`.`stats`, because the whole `stats` slice is replaced wholesale. |
| Key order | **Irrelevant.** Parsed by name. |
| Malformed JSON | Swallowed by a bare `catch {}`, so **a heartbeat must NOT be sent as a `stats_update`.** |
| First push | On connect, **immediately write one `stats_update`** from the current stats so the tiles populate without waiting (the TS server does this; keep it). |
| Heartbeat | An SSE **comment** every 30 s: `:ping\n\n`. Keep it — Cloudflare tunnels drop idle streams. |
| Headers | `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`, then flush. The Go handler must call `http.Flusher.Flush()` **per write** and must never buffer. `X-Accel-Buffering` is cosmetic now that nginx is not in the API path, but it costs one line and cloudflared or a future proxy may honour it. |
| Lifetime | The response is never ended; cleanup is on client disconnect. In Go: `select` on `r.Context().Done()`. |
| Concurrency | One connection per Dashboard **mount** (`useEffect` with `[]` deps). Under React StrictMode in dev this mounts twice, i.e. **two connections per tab**. Only `/explorer` opens a stream; the landing page does not. Caps in §6.1. |
| No `WriteTimeout` | The API server sets `WriteTimeout: 0`; the SSE handler sets no write deadline (§6.7). |

**Broadcast triggers:** (a) on connect, (b) immediately after each successful publish batch commits, (c) every sampler tick (60 s) as a floor.

**What fronts this stream must not buffer it.** The reverted nginx config documented the requirement even though the config itself is dead: `proxy_buffering off`, `proxy_cache off`, `chunked_transfer_encoding off`, `proxy_read_timeout 86400s`, and **no** `Connection: upgrade` (the orphaned file sets exactly that header on `/api`, which is actively wrong for SSE). In the two-images-behind-one-Cloudflare-tunnel shape there is no in-pod proxy, but the tunnel, Traefik and the ModSecurity middleware must be **verified** not to buffer or idle-timeout the stream (§15.7, §21.2 step 11).

### 13.8 Status projection, timestamps, numbers, ids

**Status is a hard contract.** `getOnChainStatus()` gates on `record.status !== 'completed' || !record.blockchain.txid → 'Not On-Chain'`, and `VerificationBadge` indexes `statusStyles[status]` over exactly `pending|processing|completed|failed`. An unknown status does **not** crash — `statusStyles[status]` is `undefined`, so the badge renders the raw string with a broken className — but it **will** be shown as "Not On-Chain" forever. **The Go port must project its internal state machine onto exactly those four wire values:**

| Internal | Wire |
|---|---|
| `pending` | `pending` |
| `processing` (claimed, publishing, or awaiting adoption) | `processing` |
| `completed` with `chain_status ∈ {arc-accepted, unmined, mined}` | `completed` |
| `failed` (terminal: `attempts >= 3` permanent, or `chain_status = aborted`) | `failed` |

Chain-level richness (`arc-accepted`, `unmined`, `mined`, `aborted`) lives in `chain_status`, which the SPA never sees. `completed` always implies `txid`, `outputIndex` and `processedAt` are set.

**Timestamps.** All use an `isoMillis` type: UTC, exactly three fractional digits, `Z` suffix — `2006-01-02T15:04:05.000Z`. Go's default `RFC3339Nano` marshal is variable-width, and `timestamp` is compared with `localeCompare` (§13.2), so a fixed shape is required, not merely nice. A space-separated form yields `Invalid Date` in Safari.

**Numbers are numbers.** Never scan a `NUMERIC` into a string, never leave a `json.Number` as a string. `air_density` and `station_pressure` are `float64`; everything else numeric is an integer type. `lastTemp` is `*float64` with `omitempty` **forbidden**.

**Ids** are opaque, path-safe JSON strings (uuidv7 text). They change from Mongo ObjectId hex — see §14.

**React Query caching to be aware of.** `main.tsx` sets `staleTime: 30_000`, `gcTime: 600_000`, `retry: 1` globally, and the weather-detail query overrides `staleTime` to `5 × 60_000`. Prefetch-on-hover fires a real HTTP request per hovered row. So the Go backend sees **bursty read traffic dominated by prefetches**, and a just-written record can be up to 30 s stale in a client that already has the page cached. This is why the rate limit is 600/min (§6.1) and why the SSE stream — not polling — is what makes the tiles live.

### 13.9 Frontend changes in scope — five surgical fixes

The frontend is kept, not frozen. Exactly these changes ship with the port:

| # | Change | Why |
|---|---|---|
| 1 | **`frontend/Dockerfile`: add `ARG VITE_BSV_NETWORK` + `ENV VITE_BSV_NETWORK=$VITE_BSV_NETWORK`** positioned after `COPY . .` and before `RUN npm run build` (the same position `5ac61d5` established for `VITE_API_URL`); pass `VITE_BSV_NETWORK=main` from `build.yml`. Keep `getNetwork()`'s `'test'` fallback (a safe default for dev). | Fixes §2.3 bug 1: every WhatsOnChain link on the live mainnet site currently points at testnet. |
| 2 | **Add `frontend/.dockerignore`** with `node_modules/`, `dist/`, `.env*`, `.DS_Store`. | The repo's single `.dockerignore` sits at the **repo root** and therefore applies only to the backend build; the frontend context is `./frontend` (§6.6). |
| 3 | **Replace `frontend/nginx.conf`** with a **static-only** config for the new image: `listen 8080`, `try_files $uri $uri/ /index.html`, `location = /healthz { access_log off; return 200 "ok\n"; }`, asset cache headers, and **no `location /api`, no `proxy_pass` anywhere.** Add a CI grep that fails the build if `proxy_pass` appears anywhere under `frontend/`. | The file at HEAD is **orphaned dead config** that no Dockerfile copies (§15.1). It is simultaneously the source of a phantom port mismatch and a loaded gun for re-landing the reverted reverse proxy. |
| 4 | **Delete dead components:** `WeatherList.tsx` (imported by nothing; the only caller of `useWeatherRecords` and the only sender of `?status=`), `WeatherCard.tsx` (imported only by `WeatherList`; the sole reader of `data.icon`, which it maps to `openweathermap.org` PNG URLs — an external image fetch that never happens on any live page), and `useWeather.ts::useWeatherRecords`. All three use the pre-redesign indigo/gray palette, confirming both redesign commits skipped them. | They are not API requirements and must not be mistaken for them. `?status=` filtering is exercised by **nothing** in the shipped UI — keep the parameter (it is cheap and the allowlist is the security-relevant part) but do not treat it as load-bearing. |
| 5 | **`services/verify.ts`: change `Transaction.fromHexBEEF(beefHex)` to `Transaction.fromHexBEEF(beefHex, txid)`** in `verifyWeatherProof`/`checkBeefConfirmation`, threading the txid through. Leave the functions dormant. | Removes the latent wrong-transaction bug of §13.6 rather than leaving it armed for whoever revives the path. Pairs with the backend emitting atomic BEEF. |

**Build inputs.** Set `VITE_API_URL` **empty** so the bundle emits **relative** `/api/...` paths, which is exactly what the path-split Ingress needs, and **delete the GitHub repo variable `VITE_API_URL`** (currently the absolute `https://weather-proof-api.bsvblockchain.tech`) so `${{ vars.VITE_API_URL }}` cannot silently re-inject it. Vite precedence is verified against the installed vite 5.4.21: `loadEnv` applies `.env` file entries first and then **overwrites from `process.env` for anything `VITE_`-prefixed**, and an empty env var still enumerates — so `ENV VITE_API_URL=` yields `''` → `API_BASE = ''` → relative paths.

**The frontend was already written for relative same-origin `/api`.** `API_BASE = import.meta.env.VITE_API_URL || ''` in both consumers, and `vite.config.ts` already proxies `/api` → `http://localhost:3001` in dev. **Setting an absolute `VITE_API_URL` was the deviation, not the requirement** — which is what makes the path-split Ingress natural and what deletes CORS, the second hostname, and `CORS_ORIGIN` outright (§6.5).

**Two follow-ups explicitly out of scope:** wiring the landing page's hardcoded stats to live data, and reviving the client-side BEEF verification path. Both are new product work.

---

## 14. Migration and data loss

### 14.1 There is nothing to migrate — the old data is gone, not abandoned

**This is not a choice between migrating and starting fresh. The old dataset is unrecoverable, and no backfill is possible at any price.**

**The evidence.** The MongoDB Atlas cluster named in the SSM parameter `/apps/weather-chain/MONGO_URI` **no longer exists**. Its SRV record does not resolve:

```
$ dig SRV _mongodb._tcp.weatherchain.mezgo0s.mongodb.net
;; ->>HEADER<<- opcode: QUERY, status: NXDOMAIN
;; flags: qr rd ra; QUERY: 1, ANSWER: 0, AUTHORITY: 0, ADDITIONAL: 0
```

**`NXDOMAIN`, zero answers — and DNS itself is healthy** (`dig +short google.com` resolves normally from the same resolver, so this is a deleted cluster, not a network fault). There is no host to connect to, no `mongoexport` to run, and no dump anywhere else in the estate.

**And the loss compounds.** The **txids** are what would let old records be located on chain — and they lived **in that cluster and nowhere else**. So even though the transactions are still on the blockchain, and even though a decoder could in principle read them, **there is no index from which to find them.** The TypeScript passed **no labels** on its weather actions and the Go app points at a **different storage server**, so `ListActions` cannot enumerate them either. Chain data without txids is not recoverable data.

**Consequently this is a clean-slate build:** plain Postgres (pgx/v5, `data jsonb`, `FOR UPDATE SKIP LOCKED` claim), starting empty.

| Consequence | Statement |
|---|---|
| Existing Mongo weather records and their txids | **Gone.** Not deprioritised, not deferred — **unrecoverable**, per the NXDOMAIN evidence above. No backfill script is a deliverable **because no backfill is possible.** |
| `/weather/:id` permalinks | **Change.** Every bookmarked link 404s. Ids move from Mongo ObjectId hex to uuidv7 text. *(Still true, and still the user-visible consequence to communicate — §20 item 4.)* |
| The anchored-reading history the demo is selling | **Restarts at zero.** The dashboard's `totalTx` tile starts at 0, and separately drops ~19× in meaning once it counts transactions rather than records (§13.3). |
| Existing `blockHeight` data | **None exists, and none is retrievable.** Independently of the cluster loss, `POST /api/verify`'s persistence path used a MongoDB multi-document transaction against a standalone `mongo:8.0` with no `--replSet`, so it threw `IllegalOperation` and returned 500 in that deployment shape (§5 row 17) — the field was probably never written at all. |
| SSM | `MONGO_URI` is **retired from the ExternalSecret** in this PR; **the SSM parameter itself is deleted later, separately** (§15.6). It is a string pointing at an NXDOMAIN host, so it protects nothing. `POSTGRES_PASSWORD` is added. |
| Chain-side recovery of the old history | **Unavailable, twice over.** The txids that would locate the records died with the cluster; and the TS actions carried no labels while the Go app uses a different storage server, so `ListActions` from the new wallet returns none of the old history. |
| ~~Insurance: a one-off `mongoexport` snapshot~~ | **REMOVED — it is not an option.** An earlier draft recorded a `mongoexport` of `weatherrecords` as unowned, non-blocking insurance "before the external cluster is decommissioned". The cluster is **already gone**, so there is nothing to export. The row is struck rather than deleted so nobody re-proposes it (see also §20 item 6). |

### 14.2 Why Postgres

The store is a durable queue plus two flat tables. `SKIP LOCKED` is the **replacement for the accidental cross-pod mutex** described in §5.0 — a structural guarantee rather than a convention. The toolbox already depends on pgx/v5, so it is zero new dependency class. `go-message-box-server` is a known-good Go + own-Postgres manifest template to diff against. Everything above the store interfaces is datastore-agnostic, so this is a one-package decision. Postgres also gives real multi-statement transactions, which removes the `POST /api/verify` failure class outright.

### 14.3 Legacy funds and identity reuse

- **The legacy hash-puzzle UTXOs are not currently recoverable by anyone.** The old storage server `https://store-us-1.bsvb.tech` responds 200 on `GET /`, but its BRC-104 endpoint `/.well-known/auth` returns **HTTP 500**; a real Go client using the identity key fails at `failed to make storage available … failed to read response body`. **No sweep tool can be written until that server is repaired.** Recorded as a **deferred, non-blocking** recovery task with that reason. **Do not design a sweep now.**
- Each legacy output has its **own random preimage** (`src/service/setup.ts:19-27` calls `createHashPuzzle()` inside the per-output loop), stored **only** in the old server's `customInstructions` — not in Mongo, not in SSM, not on chain until spent.
- **Reusing `/apps/weather-chain/SERVER_PRIVATE_KEY`** (confirmed in SSM, us-east-2, profile `bsva`, identity pubkey `026d970c3755321676cdea83a285572e10f761943c18ce930b6e719ee42e161efd`) buys **only a stable on-chain identity**. Baskets are per-user **and** per-storage-server: `fuel`, `reserve` and `default` all start **empty** on `go-wallet-us-1.bsvblockchain.tech`. **No balance or history carries across — the §9 deposit is mandatory on first deploy.**

### 14.4 Durability of the new data

| Item | Decision |
|---|---|
| Backups | **Deliverable:** a nightly `pg_dump` CronJob to S3 in the infra PR (`apps/base/weather-proof/postgres-backup-cronjob.yaml`), 14-day retention. |
| `synchronous_commit` | **Left at the default `on`.** Do **not** copy go-wallet-toolbox's `synchronous_commit=off`: that buys 1000-TPS write throughput at the cost of losing recently-committed transactions on an unclean shutdown, and **this app's rows are the only record that a weather observation was ever queued** — a lost commit is a silently dropped observation with no upstream to re-read it from. |
| Read-API downtime | **Accepted and stated:** single-replica Postgres on an RWO PVC with `strategy: Recreate` means the read API is down for the duration of every DB restart, node drain or image bump (tens of seconds). §11.2 is what keeps that from becoming a CrashLoopBackOff. |
| Reconstructibility | `completed` rows **are** reconstructible from chain: the `weather` label on every action (§10.0) plus the decoder recovers `data`, `txid` and `vout`. The procedure is documented in `docs/RUNBOOK.md` ("database restore"); a `weather rebuild-from-chain` subcommand is a future task, not a v1 deliverable. **PVC loss is therefore recoverable, not terminal.** |

---

## 15. Deployment

### 15.1 Why the nginx `/api` reverse proxy is ruled out — record it so nobody retries

`6b40836` ("Switch frontend from serve to nginx with API reverse proxy") was reverted by `c8114d8` **14 h 22 m later, the same day** (07:57:09 → 22:19:07 +0400, 2026-03-13). Both commits were pushed straight to `master` by the same author with **no PR and no issue** (`gh pr list --state all` shows only PRs #1–#5, none of them these; `gh issue list` is empty), and `c8114d8`'s message is the bare `git revert` template. **The author's stated reason does not exist anywhere in the repo or on GitHub.**

**But the deploy record proves the image was built, tagged, evaluated and rejected before it ever reached the cluster:** tag `v0.0.9` == `6b40836`, and the Flux manifest history goes `v0.0.8 → v0.0.10` (bsva-infra-flux commits `6eb152b0`/`4c05916a`, 2026-03-18), **skipping `v0.0.9` entirely.**

Four concrete, independently fatal defects in the reverted diff. **Record them as hazards, not as history** — they are a reading of the diff, not a documented cause:

| # | Defect | Mechanism |
|---|---|---|
| 1 | **Deploy-fatal upstream name.** It changed `proxy_pass http://weather-chain-back:3001` to `proxy_pass http://app:3001` — its own commit body calls that a "Fix … to use correct docker service name (app:3001)". `app` is the **docker-compose** service name (`docker-compose.yaml:25`). | nginx resolves a literal upstream hostname **at config-load time** and aborts with `[emerg] host not found in upstream "app"`. There is no Service named `app` in namespace `weather-proof`, so the front pod CrashLoopBackOffs and takes the **static site** down as well as the API. |
| 2 | **Port drift.** `EXPOSE 3000` / `serve -l 3000` → `EXPOSE 80` / nginx `listen 80`, while the Flux Deployment/Service kept containerPort and port/targetPort **3000**. | Even with a resolvable upstream, Ingress → Service:3000 → nothing listening. Probes fail. |
| 3 | **Build-arg break.** It **deleted** `ARG VITE_API_URL`/`ENV` from `frontend/Dockerfile` while `build.yml` still passes `build-args: VITE_API_URL=…`. Docker only *warns* on an unconsumed build arg. | The bundle silently lost its API base and fell back to relative `/api` — which only works **if the proxy works**, so defects 1 and 3 compound into total failure. |
| 4 | **CORS default flipped** to `https://weather-proof.bsvb.tech`, breaking local dev at `http://localhost:5173` and pinning a legacy `bsvb.tech` host that was about to be retired. | Self-cancelling under a same-origin proxy, but bundled into the change and therefore lost on revert along with the (correct-on-the-merits) removal of the frontend's inert runtime `VITE_*` env. |

Architecturally it also put the **long-lived SSE stream** (`location /api/events`, `proxy_read_timeout 86400s`, `proxy_buffering off`, `chunked_transfer_encoding off`) through the **frontend pod**, so every static-asset rollout would tear down every live client stream, and the SPA pod became a mandatory hop for the API. Its `/api` location also set `proxy_set_header Connection 'upgrade'`, which is **actively wrong for SSE** — plausibly the thing that actually broke first.

**The revert was itself hasty in one way worth knowing:** it left `docker-compose.yaml` mapping `5173:80` against a container serving on 3000, and that stayed broken for a month until `698c944` changed it to `5173:3000`.

**Decision, binding: the deploy shape is TWO IMAGES behind ONE Cloudflare-tunnel Ingress with a path split.** No in-pod upstream name to resolve, no port renegotiation, and the SSE stream is proxied by the Ingress rather than by a hand-rolled nginx location block that needs bespoke buffering tuning. **Do not reintroduce the reverse proxy.** §13.9 item 3 adds a CI grep so it cannot be re-landed silently.

**And the port "mismatch" earlier research reported does not exist.** At HEAD the frontend production stage is:

```dockerfile
FROM node:20-alpine AS production
RUN npm install -g serve
COPY --from=build /app/dist ./dist
EXPOSE 3000
CMD ["serve", "-s", "dist", "-l", "3000"]
```

`serve -s` (= `--single`) provides exactly the SPA fallback all four routes need, `docker-compose.yaml:82` maps `5173:3000`, and **the deployed manifest's containerPort/targetPort 3000 is CORRECT and matches the repo at HEAD.** The "nginx listens on 80" observation came from reading `frontend/nginx.conf`, which **no Dockerfile copies** (`git grep nginx` finds it only in stale docs — `DOCKER_QUICKSTART.md:170`, `CHANGELOG.md:19`, `frontend/README.md:157` — and in the file itself). **An earlier plan to move the Service to `targetPort: 80` and use an nginx image would have BROKEN a currently-correct manifest.** That plan is dropped; the port was never the bug.

### 15.2 Shape

```
weather-proof-us-1.bsvblockchain.tech        (one hostname, one Ingress)
   /api   ->  Service weather-proof-back:3001    (Go backend)
   /      ->  Service weather-proof-front:8080   (nginx, static only)
```

Four hostnames collapse to one, four Ingress objects collapse to one, and CORS disappears because everything is same-origin.

### 15.3 UPDATE `apps/base/weather-proof/` in place — do NOT delete and recreate

An earlier draft proposed deleting `apps/base/weather-proof/` and creating `apps/base/weather-chain/`. **That only made sense under the dropped rename**; now that the app is `weather-proof` everywhere, the "new" directory would be the **identical path**, so delete+add renders as an unreviewable "everything changed" diff.

There is **zero cluster-state difference**: `- ../base/weather-proof` is commented out in `apps/bsva-us-1/kustomization.yaml`, and the apps Kustomization has `prune: true`, so **no weather-proof object exists in `bsva-us-1` today.** The choice is purely about reviewability — and reviewability is what these manifests most need, because they have demonstrably **never been validated**. They contain:

| Defect in the existing manifest | Effect |
|---|---|
| `FUNDING_BACKET_MIN: 10` | Misspelled (BASKET), so the TS app silently used its **200** default, not the 10 written |
| `DOMAIN` | Read **nowhere** in `src/` |
| `PORT: 3001` on the backend | The TS backend reads **`API_PORT`**, so this was inert and only coincidentally matched the 3001 default |
| `VITE_API_URL: http://weather-proof-back:3001` on the **frontend** | A **runtime** env var on a **static** bundle — inert since day one, and it names an in-cluster address a browser could never reach |
| `PORT: "3000"` on the frontend | nginx never read it; `serve -l 3000` was hardcoded in `CMD` |

An in-place diff shows a reviewer exactly which legacy value died. Keep the back/front file naming (two Deployments) rather than the single-app `app-*.yaml` convention of `go-wallet-toolbox`/`go-message-box-server`, but **do** split the Ingress into a new `ingress.yaml`, because there is now one Ingress serving both Services and it belongs to neither file.

**Complete file set — 10 files (4 updated, 6 new), plus 2 recommended:**

| File | State | Contents |
|---|---|---|
| `namespace.yaml` | unchanged | `weather-proof` |
| `external-secrets.yaml` | **updated** | §15.6 |
| `weather-proof-back.yaml` | **updated** | Deployment + Service only; **both Ingresses deleted from it** |
| `weather-proof-front.yaml` | **updated** | Deployment + Service only; **both Ingresses deleted from it** |
| `kustomization.yaml` | **updated** | add the six new resources; keep `namespace: weather-proof`; leave the non-standard top-level `metadata.name` (it matches the sibling apps and is harmless) |
| `ingress.yaml` | **new** | the single path-split Cloudflare-tunnel Ingress (§15.5) |
| `postgres-deployment.yaml` | **new** | §15.4 |
| `postgres-service.yaml` | **new** | ClusterIP `postgres:5432` |
| `postgres-data-persistentvolumeclaim.yaml` | **new** | §15.4, **without** the skip annotations on the first PR |
| `app-configmap.yaml` | **new** | the non-secret backend config of §6/§8.13, so tuning knobs are a ConfigMap edit rather than Deployment env churn |
| `pdb.yaml` | recommended | PodDisruptionBudget `maxUnavailable: 1` — documents single-replica intent for node drains |
| `postgres-backup-cronjob.yaml` | recommended | nightly `pg_dump` → S3, 14-day retention (§14.4) |

Outside the directory, **as the last change to the manifests**: uncomment `- ../base/weather-proof` in `apps/bsva-us-1/kustomization.yaml`. Note this is deliberately **not** the last *deploy* step — it is §21.2 **step 8**, and it must precede the PVC/ExternalSecret verification, because until it lands `prune: true` means none of those objects exist to verify.

**`weather-proof-back.yaml` — every value that changes:**

| Field | From | To |
|---|---|---|
| `image` | `ghcr.io/bsv-blockchain-demos/weather-proof-back:v0.0.12` | the first Go tag, **pinned** (e.g. `:v1.0.0`). **Never `:latest`** — `go-message-box-server`'s `:latest` is the anti-pattern; `go-wallet-toolbox` pins `:v0.184.3`, follow that |
| `replicas` | 1 | **1 (KEEP — load-bearing, §5.0 / §5 row 4)** |
| `strategy` | `rollingUpdate{maxSurge:0,maxUnavailable:1}` | **`type: Recreate`.** Honest framing: this is a **clarity** change, not a bug fix — at `replicas: 1`, `maxSurge 0` + `maxUnavailable 1` already terminates the old pod before starting the new one, so the single-writer invariant already held. `Recreate` makes the intent unmistakable to whoever edits it next |
| `terminationGracePeriodSeconds` | *absent* → k8s default 30 | **90** (§16.1). At 30 s a SIGKILL can land mid-`CreateAction` |
| env `DOMAIN` | present | **DELETE** — read nowhere |
| env `FUNDING_OUTPUT_AMOUNT`, `FUNDING_BACKET_MIN`, `FUNDING_BATCH_SIZE` | present | **DELETE** — funding is server-side now (§5 row 10) |
| env `CORS_ORIGIN` | `https://weather-proof.bsvblockchain.tech` | **DELETE** — same-origin (§6.5) |
| env `PORT: 3001` | inert | **rename to `API_PORT: "3001"`** so it is actually read |
| env `WALLET_STORAGE_URL` | `https://store-us-1.bsvb.tech` | **`https://go-wallet-us-1.bsvblockchain.tech`** (the old host's `/.well-known/auth` returns 500) |
| env `WEATHER_OUTPUTS_PER_TX` | `5` | **`"21"`** (§8.9). **Name kept**, not renamed — it is the name in the TS, the manifest and the docs, and renaming buys nothing while breaking traceability |
| env `MONITOR_INTERVAL: 60` | present | **rename to `FUEL_INTERVAL: "60"`** (§8.13) |
| env `BSV_NETWORK`, `POLL_RATE`, `PROCESSOR_INTERVAL` | `main`, `300`, `3` | **KEEP** |
| env **added** (via `app-configmap.yaml`) | — | `OPS_PORT: "9090"`, `DENOMINATION_SATOSHIS: "50"`, `FUEL_TARGET_POOL_SIZE: "1000"`, `FANOUT_OUTPUTS_PER_TX: "100"`, `FUEL_FANOUT_MAX_TXS_PER_ROUND: "12"`, `WEATHER_MAX_SCRIPT_BYTES: "297"`, `PROCESSING_LEASE: "5m"`, `RECONCILE_INTERVAL: "5m"`, `PROOF_RATE_LIMIT_PER_MIN: "60"`, `TRUSTED_PROXY_CIDRS` (**value below — never ship it blank**), `LOG_LEVEL: "info"`, and the Postgres DSN pieces `PG_HOST: postgres`, `PG_PORT: "5432"`, `PG_USER: weather`, `PG_DATABASE: weather`, `PG_SSLMODE: disable` |
| **added** | — | `initContainer: wait-for-postgres`; resources; probes; `securityContext`; `containerPort: 9090` (documentation only) |
| `envFrom` secretRef `weather-proof-secrets` | present | **KEEP** |
| **Service** | `weather-proof-back:3001` → targetPort 3001 | unchanged. **Deliberately do NOT add port 9090**, so no Ingress path can ever reach `/api/ops` (§11.2) |

**`TRUSTED_PROXY_CIDRS` — the shipped value, because an empty list silently disables per-IP rate limiting (§6.1).** The **cloudflared tunnel is the only ingress path** to this hostname (§15.5), so the correct and smallest list is the cluster **pod CIDR** — every request reaches the backend from a cloudflared or Traefik pod, never from a Cloudflare edge address directly:

```yaml
# app-configmap.yaml — see §6.1. An EMPTY value fails closed: forwarding headers are
# ignored and the limiter degrades to one bucket per proxy pod (WARN at boot).
# Value = the bsva-us-1 pod CIDR. Confirm before merge with:
#   kubectl cluster-info dump | grep -m1 cluster-cidr
# (EKS default for this cluster shape is the VPC subnet range, NOT 10.244.0.0/16 —
#  do not copy that from a kubeadm tutorial.)
TRUSTED_PROXY_CIDRS: "10.0.0.0/8"
```

Ship the pod/VPC CIDR only. **Do not enumerate the ~22 Cloudflare prefixes here** — they would be dead weight while cloudflared is the sole path (a Cloudflare edge IP is never this pod's peer), and a stale list is worse than no list. The enumerated Cloudflare ranges become necessary **only** if the tunnel is ever replaced by a public LoadBalancer that Cloudflare proxies directly to; that is the one change that must also add them. In that case: source them from `https://www.cloudflare.com/ips-v4` and `…/ips-v6` (never hand-typed), **owner: infra**, refresh **quarterly** and on any Cloudflare change notice, tracked as a line in `docs/RUNBOOK.md` next to the alarm definitions. §20 records it as an open item until the CIDR is confirmed against the live cluster.

**initContainer** (copy `go-message-box-server`, which also does the nslookup):

```yaml
initContainers:
  - name: wait-for-postgres
    image: busybox:1.36
    command: ['sh','-c','until nslookup postgres && nc -z postgres 5432; do echo waiting for postgres; sleep 2; done;']
```

**Resources (back):** requests `cpu 200m` / `memory 256Mi`; limits `cpu "1"` / `memory 1Gi`. `go-wallet-toolbox`'s 1–4 CPU / 1–4 Gi is sized for 1000 TPS and is wildly oversized here; `go-message-box-server`'s 500m/256Mi limit is too tight for BEEF assembly.

**Probes:**

```yaml
livenessProbe:                       # process-only, NO DB ping, NO wallet check (§11.2)
  httpGet: {path: /api/health, port: 3001}
  initialDelaySeconds: 20
  periodSeconds: 30
  timeoutSeconds: 5
  failureThreshold: 5
readinessProbe:                      # fast pgxpool.Ping; 503 when Postgres is unreachable
  httpGet: {path: /api/ready, port: 3001}
  initialDelaySeconds: 10
  periodSeconds: 15
  timeoutSeconds: 5
```

The rationale of §11.2 must be a **comment in the manifest**, not only in this document.

### 15.4 Postgres, and the PVC skip-annotation trap

**`postgres-deployment.yaml`** — base it on `go-wallet-toolbox`'s (it has probes and a `secretKeyRef` password; `go-message-box-server` has neither and **hardcodes `POSTGRES_PASSWORD: messagebox` in plaintext in git** — do not copy that), then scale it down:

| Setting | Value |
|---|---|
| image | `postgres:17-alpine` |
| `strategy` | `type: Recreate` (mandatory with a ReadWriteOnce PVC) |
| `POSTGRES_USER` / `POSTGRES_DB` | `weather` / `weather` |
| `POSTGRES_PASSWORD` | `secretKeyRef {name: postgres-secrets, key: POSTGRES_PASSWORD}` |
| `PGDATA` | `/var/lib/postgresql/data/pgdata` — **the subdirectory matters**: pointing PGDATA at the mount root breaks initdb on a volume with `lost+found` |
| liveness + readiness | both `exec: [pg_isready, -U, weather, -d, weather]` with go-wallet-toolbox's timings |
| resources | requests `cpu 250m` / `memory 512Mi`; limits `cpu "1"` / `memory 2Gi` (vs go-wallet-toolbox's 2–4 CPU / 4–8 Gi) |
| server args | `max_connections=100`, `shared_buffers=256MB`, `effective_cache_size=1GB`, `work_mem=8MB`, `maintenance_work_mem=128MB` |
| `synchronous_commit` | **default `on` — do NOT copy `off`** (§14.4) |

**The PVC skip-annotation trap.** `go-wallet-toolbox`'s PVC carries `kustomize.toolkit.fluxcd.io/skip: "true"` and `kustomize.toolkit.fluxcd.io/reconcile: disabled`. `reconcile: disabled` is the load-bearing one — a documented Flux annotation that makes kustomize-controller skip the resource entirely, so **it is never created OR updated**; `skip: "true"` is not a documented Flux annotation and appears to be a no-op belief. Both were **copied** from `apps/base/wab/db-data-persistentvolumeclaim.yaml`, where they were legitimately added (commit `5a30e304`, "Don't reconcile wab DB anymore") to stop Flux churning an **already-live** PVC after a history of storage-size edits (1Gi→10Gi→1Gi→10Gi, gp3→gp3-retain). Commit `db34db31` then created go-wallet-toolbox's PVC by copying that file into a **brand-new namespace, annotations included** — which is the trap: **Flux will never create it, so the Postgres pod sits `Pending` on an unbound claim forever and the failure is silent** (Flux reports the Kustomization as reconciled).

**Decision: ship the PVC WITHOUT the annotations on the first PR**, exactly like `go-message-box-server`'s clean PVC, and let Flux create it once. **Then add `reconcile: disabled` in a small follow-up commit** after `kubectl get pvc -n weather-proof postgres-data` shows `Bound`. Two-phase, no manual `kubectl apply`, no Pending-forever window. (If anyone insists on shipping the annotations in the first PR, the checklist **must** contain an explicit `kubectl apply -n weather-proof -f apps/base/weather-proof/postgres-data-persistentvolumeclaim.yaml` **before** uncommenting the include line — and note a hand-applied PVC carries no Flux inventory labels, so `prune: true` will not clean it up later.)

Spec: `accessModes: [ReadWriteOnce]`, `storageClassName: gp3-retain`, `storage: 10Gi` (matching `go-message-box-server`; go-wallet-toolbox's 20 Gi is sized for 1000-TPS storage rows, not ~5 500 jsonb rows/day).

### 15.5 The frontend Deployment and the single Ingress

**`weather-proof-front.yaml`:**

| Field | Value | Why |
|---|---|---|
| image | new pinned tag, built from `nginxinc/nginx-unprivileged:1.27-alpine` | Already listens 8080 and writes its temp dirs under `/tmp`; mount an `emptyDir` at `/tmp`. Replaces a full `node:20-alpine` + global `serve` install shipped purely to serve a handful of static files |
| **port** | **8080** everywhere: `listen 8080`, containerPort 8080, Service port/targetPort 8080 | 8080 is **unprivileged**, so the pod runs `runAsNonRoot: true` / `runAsUser: 101` / `readOnlyRootFilesystem: true` with **no `NET_BIND_SERVICE`**. And it is distinguishable from **both** legacy values — 3000 (`serve`) and 80 (the reverted v0.0.9 nginx) — so the number itself signals "this is the new image". **Keeping 3000 was considered and rejected**: it makes an nginx image masquerade as the old `serve` port and preserves exactly the ambiguity that produced the phantom mismatch |
| env | **none at all** | Delete both `PORT: "3000"` (nginx never read it) and `VITE_API_URL: http://weather-proof-back:3001` (runtime env on a static bundle is inert, and it names an address a browser could never reach). Everything the frontend needs is baked at build time |
| `replicas` | **2** | Unlike the backend, the static server is stateless with no keeper and no single-writer invariant, so two replicas with the default RollingUpdate give genuinely zero-downtime asset rollouts and survive a single node drain. **This asymmetry (back = 1/Recreate, front = 2/RollingUpdate) must be commented in both files so nobody "harmonises" them** |
| resources | requests `cpu 10m` / `memory 32Mi`; limits `cpu 200m` / `memory 128Mi` | — |
| probes | both liveness and readiness `httpGet /healthz :8080` | The same path is fine here precisely because nginx has no dependency that could make it unready-but-alive, and it avoids serving the full `index.html` on every probe |
| `terminationGracePeriodSeconds` | default 30 | No long-lived streams terminate here — SSE lands on the backend |

**`ingress.yaml`** — one Ingress object named `weather-proof`, host `weather-proof-${app_suffix}.bsvblockchain.tech` (resolves to `weather-proof-us-1.bsvblockchain.tech`; substitution is guaranteed because `clusters/bsva-us-1/apps.yaml` declares `postBuild.substituteFrom: [ConfigMap cluster-vars]`, and `clusters/bsva-us-1/vars/configmap.yaml` supplies `app_suffix: us-1` and `cloudflare_tunnel_uuid`):

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: weather-proof
  labels:
    kubernetes.io/ingress-class: traefik-public
  annotations:
    external-dns.alpha.kubernetes.io/target: "${cloudflare_tunnel_uuid}.cfargotunnel.com"
    external-dns.alpha.kubernetes.io/cloudflare-proxied: "true"
spec:
  ingressClassName: traefik-public
  rules:
    - host: weather-proof-${app_suffix}.bsvblockchain.tech
      http:
        paths:
          - path: /api
            pathType: Prefix
            backend: {service: {name: weather-proof-back,  port: {number: 3001}}}
          - path: /
            pathType: Prefix
            backend: {service: {name: weather-proof-front, port: {number: 8080}}}
```

Exactly those two annotations and nothing else. **No `tls:` block, no cert-manager annotation, no `traefik.ingress.kubernetes.io/router.tls`, no `router.middlewares` redirect** — Cloudflare terminates TLS at the edge and the tunnel reaches Traefik internally. Copy the shape of `apps/base/go-wallet-toolbox/ingress.yaml` verbatim.

- **Ordering safety:** Traefik derives router priority from rule length, so the longer `/api` PathPrefix always wins over `/` regardless of YAML order. Keep `/api` first for human readability.
- **No strip-prefix middleware.** The backend's routes are literally `/api/weather`, `/api/stations`, `/api/verify`, `/api/events`, `/api/proof`, `/api/health`, `/api/ready`, so the prefix must pass through intact.
- `pathType: Prefix` on `/api` matches `/api` and `/api/...` but not `/apiXYZ`, which is what we want.
- Precedent for a multi-path Ingress in this repo: `apps/base/float/float.yaml` and `apps/base/faucet/_base`.

**Drop all four legacy Ingresses:**

| Ingress | Why it goes |
|---|---|
| `weather-proof-back`, `weather-proof-front` | cert-manager/Let's Encrypt on `weather-proof-api.bsvb.tech` and `weather-proof.bsvb.tech`, with `cert-manager.io/cluster-issuer: letsencrypt`, `router.tls: "true"` and the `traefik-public-redirect-to-bsvblockchain@kubernetescrd` middleware. Commit `cb1174e1` ("Migrate weather-proof to CF only") already established Cloudflare-only intent yet left these behind. Once traffic is a Cloudflare tunnel the **HTTP-01 ACME solver is unreachable**, so cert-manager produces perpetually-failing Order/Challenge objects plus rate-limit pressure on the shared `letsencrypt` ClusterIssuer. The redirect-to-bsvblockchain middleware is meaningless when the only hostname **is** on bsvblockchain.tech |
| `weather-proof-back-cf`, `weather-proof-front-cf` | Already correct in style (CF-tunnel annotations, no TLS block), but they are **two hostnames** — and the whole point of the path split is that there is **one origin**, which is what eliminates CORS. **Superseded, not wrong** |

Net effect: **4 Ingresses → 1; 4 hostnames → 1**; two cert-manager Certificates/Secrets (`weather-proof-back-tls`, `weather-proof-tls`) never requested. Note the hostname gains the `-us-1` suffix (matching `go-wallet-us-1`) — **flag this to whoever owns any external link or bookmark to `weather-proof.bsvblockchain.tech`.**

### 15.6 SSM parameters and ExternalSecrets

| Path | State |
|---|---|
| `/apps/weather-chain/SERVER_PRIVATE_KEY` | **exists** (last modified 2026-03-09), reused (identity pubkey `026d970c…161efd`) |
| `/apps/weather-chain/TEMPEST_API_KEY` | **exists**, reused |
| `/apps/weather-chain/POSTGRES_PASSWORD` | **must be created by a human before the include line is uncommented** (§0 P3) — otherwise the ExternalSecret never syncs and both the app and Postgres start with an empty password |
| `/apps/weather-chain/MONGO_URI` | **Removed from the ExternalSecret in this PR.** The SSM parameter itself is deleted as a separate, later, deliberate step once the new app has been healthy for a while — **not** because it protects anything: the cluster it names returns **NXDOMAIN** and the dataset is unrecoverable (§14.1), so it is a dead string. Keeping it out of the cutover PR is hygiene, not insurance |

**The prefix mismatch, stated and left alone.** The parameter prefix is `/apps/weather-chain/*` while everything else is `weather-proof` (repo, namespace, ExternalSecret, target Secret, images, hostname). **The justification for not migrating is not laziness:** `SERVER_PRIVATE_KEY` is a **live mainnet identity key**, and copying it to `/apps/weather-proof/SERVER_PRIVATE_KEY` doubles the number of places that key exists and creates an indefinite window in which two paths hold the same secret with nothing recording which is authoritative. **A one-line comment in `external-secrets.yaml`** — "SSM prefix intentionally remains `/apps/weather-chain/` — pre-rename, not migrated; see PR #N" — costs nothing and prevents someone "tidying" it later.

**`external-secrets.yaml` becomes TWO ExternalSecrets**, mirroring `go-wallet-toolbox`:

| Object | Contents |
|---|---|
| ServiceAccount `external-secrets-sa` + SecretStore `aws-backend` | **Unchanged** — the IRSA annotation `eks.amazonaws.com/role-arn: arn:aws:iam::891377253213:role/eks-external-secrets-role-${cluster_name}`, ParameterStore, `region: ${cluster_region}`, jwt auth via that SA. Already correct and identical across the three sibling apps |
| ExternalSecret `weather-proof-secrets` | **drop** `MONGO_URI`; keep `SERVER_PRIVATE_KEY`, `TEMPEST_API_KEY`; **add** `POSTGRES_PASSWORD` (the app needs it to build its DSN) |
| ExternalSecret `postgres-secrets` (**new**) | target Secret `postgres-secrets`, single key `POSTGRES_PASSWORD` from the same SSM path |

The split is not ceremony: the Postgres Deployment consumes its password via `secretKeyRef`, and if it instead used `envFrom` on the app's bundle, **`SERVER_PRIVATE_KEY` (a live mainnet key) would land in the Postgres container's environment for no reason.**

Keep `creationPolicy: Owner`, `deletionPolicy: Retain`, `refreshInterval: 1h` on both. **Do not** temporarily lower `refreshInterval` to `1m` for the first sync (that churns git twice); instead force the first reconcile with:

```
kubectl annotate externalsecret -n weather-proof weather-proof-secrets postgres-secrets force-sync=$(date +%s) --overwrite
```

### 15.7 UNVERIFIED deploy risk: SSE and `POST /api/verify` pass through a cluster-wide ModSecurity WAF

**This can break the app on day one and has never been exercised.** ModSecurity is applied **globally, not per-app**: `infrastructure/controllers/traefik.yaml:58-62` attaches `traefik-public-modsecurity@kubernetescrd` as a middleware on the **`websecure` entrypoint itself**, so every request to any `traefik-public` Ingress is inspected (`owasp/modsecurity-crs:4-nginx`, `PARANOIA=1`, `MODSEC_RULE_ENGINE=On`, maxBodySize 10 MB). weather-proof has **never actually been live in this cluster** (its include line is commented), so its traffic has never met that middleware.

Two specific exposures:

1. **`GET /api/events` is a long-lived SSE stream** proxied through `acouvreur/traefik-modsecurity-plugin` v1.3.0. The plugin inspects requests and then proxies, so responses *should* stream — but this is **unproven on this cluster**, and a buffering plugin would leave the live-stats bar permanently "connecting" with no error anywhere.
2. **`POST /api/verify` sends a JSON array of 64-hex txids.** CRS integer/injection rules have already produced false positives on BSV payloads here (rule 942220 excluded for `sequenceNumber`; rule 930120 removed outright for dotted JSON arg names).

**Mitigation:** test both **immediately** after enabling (§21.2 step 11). If either trips, add a narrow **path-scoped** exclusion (preferred over host-scoped, since the app is unauthenticated and losing body inspection entirely is a real reduction in defence-in-depth), following the existing pattern in `infrastructure/controllers/modsecurity.yaml`:

```
SecRule REQUEST_HEADERS:X-Forwarded-Host "@rx ^weather-proof-[a-z]+-[0-9]+\.bsvblockchain\.tech$" \
  "id:1000006,phase:1,pass,nolog,ctl:requestBodyAccess=Off"
```

…and **bump the `configmap-hash` pod annotation to force a rollout** (rules 1000004/1000005 do exactly this for `store-*`/`go-wallet-*`).

### 15.8 CI

**Rewrite `.github/workflows/build.yml`. It is structurally fine and already least-privilege thanks to `5cfea93` — do not regress it.**

Keep verbatim: top-level `permissions: contents: read`; the `get_tag` job with no permissions block (inherits read); `build-and-push` with `permissions: {contents: read, packages: write}`. Triggers stay `push: {tags: [v*], branches: [master]}` + `pull_request: {branches: [master]}` + `workflow_dispatch`. The PR path stays push-less via `push: ${{ github.event_name != 'pull_request' }}` — which is what actually prevents fork PRs pushing images.

Changes:

| # | Change |
|---|---|
| 1 | Fix the duplicated step name — the second "Build and push frontend Docker image" builds the **backend** |
| 2 | Front build args become `VITE_API_URL=` (empty) and `VITE_BSV_NETWORK=main`; **delete the GitHub repo variable `VITE_API_URL`** |
| 3 | **Split the front job into build-with-`load: true` → verify → push**, so the verification **gates** the push instead of running after it |
| 4 | Bump `docker/build-push-action@v5` → `@v6`; pin every action to a 40-char SHA |
| 5 | Back build context stays `.` with `./Dockerfile`; front stays `./frontend` with `./frontend/Dockerfile` — **do not move them** (§4) |
| 6 | Switch both Dockerfiles to `npm ci` against committed lockfiles (§6.6) |

**The bundle-verification step** — runs between build and push, so a failure blocks publication:

```bash
cid=$(docker create weather-proof-front:verify)
docker cp "$cid":/usr/share/nginx/html/. /tmp/bundle/
docker rm "$cid"

# NEGATIVE assertions — fail if ANY match
grep -rIEn 'localhost|127\.0\.0\.1|:3001|:5173'                 /tmp/bundle && exit 1
grep -rIEn 'weather-proof-api|weather-proof-back|\.bsvb\.tech'  /tmp/bundle && exit 1

# POSITIVE assertion — an empty/broken build must not pass by containing nothing
grep -rIq '/api/weather' /tmp/bundle || exit 1

# Build marker: DO NOT try to grep for the network string — esbuild constant-folds
# `network === 'main' ? 'main' : 'test'` and the result is not reliably greppable.
jq -e '.viteBsvNetwork == "main" and .viteApiUrl == ""' /tmp/bundle/build-info.json

# The reverse proxy must never come back (§15.1)
grep -rn 'proxy_pass' frontend/ && exit 1
```

The second negative grep catches the legacy absolute host and the retired `bsvb.tech` domain — **which is what is actually baked in today** and what a localhost-only grep would miss. The `/api/weather` positive assertion works because the template literal `${API_BASE}/api/weather?` minifies to a surviving `"/api/weather?"` string literal. The build marker comes from the front Dockerfile's final stage:

```dockerfile
RUN printf '{"viteApiUrl":"%s","viteBsvNetwork":"%s","commit":"%s"}\n' \
      "$VITE_API_URL" "$VITE_BSV_NETWORK" "$GIT_SHA" > /usr/share/nginx/html/build-info.json
```

…which doubles as a permanently useful production debugging artefact at `https://weather-proof-us-1.bsvblockchain.tech/build-info.json`.

**New `.github/workflows/go.yml`:** top-level `permissions: contents: read`, no job-level grants. `go vet ./...`, `go build ./...`, `go test -race ./...` (with a `postgres:17-alpine` service for the claim-concurrency test), `golangci-lint run`, `govulncheck ./...`, `go mod verify`, a `go mod tidy` no-diff check, `gitleaks`, and the **golden-file drift check** of §7.4 — `go test ./internal/weather -run TestGolden -update` followed by `git diff --exit-code internal/weather/testdata/golden/`, so an encoder change that was not accompanied by a reviewed golden update fails CI. *(This replaces the deleted parity job, which ran `make parity-regen` against a pinned `@bsv/sdk` oracle and diffed `vectors.json`; there is no `node`, `tsc` or `npm` step in `go.yml` at all — §7.0.)* Trigger on the same push/PR branches as `build.yml`. **Plus: add `go` to CodeQL's language list** (§6.8) — without it the Go backend is scanned by nothing.

---

## 16. Lifecycle and shutdown

### 16.1 Ownership and deadline table

`terminationGracePeriodSeconds: 90`. The budget below sums to **≤ 80 s**, leaving 10 s of slack before SIGKILL.

| Step | Owner | Deadline | Notes |
|---|---|---|---|
| 0. SIGTERM → cancel root ctx | `cmd/weather/main.go` | — | `signal.NotifyContext`. All tickers observe `ctx.Done` immediately. |
| 1. `http.Server.Shutdown(ctx5s)` on **both** servers | `internal/api` | **5 s** | Stops accepting; drains in-flight reads. SSE handlers return on `ctx.Done`. |
| 2. Wait for poller, processor, keeper supervisor, reconciler, sampler, SSE hub | `errgroup` in `main.go` under one bounded deadline | **70 s** | Must exceed the longest in-flight wallet RPC (`CreateAction` 60 s, `FanOutFuel` 45 s). On expiry, **log which goroutine failed to exit** (each member sets a done-flag) and proceed. |
| 3. `pgxpool.Close()` | `main.go` | **5 s** | After all users are gone. |
| 4. `wallet.Close()` | `main.go` | immediate | A **documented no-op** for the storage client — it neither waits for nor interrupts an in-flight RPC. **The per-call ctx timeouts of §3.2 are what actually bound RPCs**, which is exactly why step 2's deadline must exceed the longest of them. |
| 5. Cancel the `pkg/services` HTTP client behind `/api/proof`, in-flight Tempest and WoC requests, and the BEEF cache | their owning components, on `ctx.Done` | within steps 1–2 | All derive from the root ctx. |

**Non-obvious constraint:** the Tempest HTTP client has its own 15 s timeout (`FETCH_TIMEOUT_MS = 15_000`) and the TypeScript iterates every station **serially**, so a full poll can legitimately take minutes. **The Go poller must honour `ctx.Done` between stations, not only between polls**, or it will blow the 70 s budget on its own (bounded concurrency plus per-station ctx checks).

**The keeper is an errgroup member**, not a bare `go keeper.Run(ctx)`. Its `Run` wrapper returns `nil` on `ctx.Done`. **Nothing may be started with a bare `go`.**

**Test:** assert `FanOutFuel`'s per-call timeout (45 s) and `CreateAction`'s (60 s) are both **strictly less** than the step-2 wait budget (70 s), and that the sum of steps 1–3 is **strictly less** than `terminationGracePeriodSeconds`.

An in-flight publish at shutdown is cancelled → classified **Unknown** (§10.3) → the classifier marks the row `pending` with `adopt_required = true`, `claim_ref` preserved, **before the goroutine returns**; if the process dies before that write lands, the reaper does the same after `PROCESSING_LEASE`. Either way the next tick's (or next boot's) adopt-check resolves it **without a duplicate broadcast**. The grace period and the adoption mechanism are the same fix.

### 16.2 Keeper supervision

```
for each 60s tick (and immediately at start):
    if roundInFlight already set:  WARN "keeper round still in flight" (with age); skip
    ctx2, cancel := context.WithTimeout(ctx, 120s)
    record lastRoundStart
    err := keeper.RunOnce(ctx2)
    if err != nil: WARN, count it, continue        (never fatal)
    record lastRoundSuccess; if minted > 0 record lastKeeperMintAt
    catch-up: while minted > 0 && inventory+minted < lowWater && rounds < 5:
                  sleep 2s; run another round
    if now - lastRoundSuccess > 3 * interval:  WARN "keeper rounds not completing"
```

A stuck round is otherwise **pure silence**: `RunOnce` returns instantly when `roundInFlight` is set, indistinguishable from a healthy no-op. At target 1000 with `FanoutMaxTxsPerRound: 12`, a cold start 0 → 1000 (10 leaves) completes in **one** round, so the catch-up path is margin rather than a load-bearing mechanism — an improvement over D=40's two-round cold start.

---

## 17. Testing strategy

### 17.1 Encoder determinism and the golden files (the frozen contract)

Covered in §7.4. The contract is **self-referential by design**: the goldens are generated from this Go encoder, they contain **no wall-clock timestamp and no git revision** (both were tried and both make regenerate-and-diff fail), regeneration is behind an explicit `go test ./internal/weather -run TestGolden -update`, plain `go test` verifies, and `go.yml` adds `git diff --exit-code internal/weather/testdata/golden/` so a drift cannot be laundered by regenerating the file.

**What this contract is and is not.** It is a **change detector** — it makes an accidental edit to field order, number encoding or the rounding rule fail loudly. It is **not** a correctness oracle against an external reference, because there is none: nothing outside this backend decodes the script (§7.0). Correctness comes from the **round-trip** and the **297-byte cap**; the goldens catch the symmetric encoder+decoder edit that a round-trip cannot.

*Removed with byte-parity (§7.0): the pinned `@bsv/sdk` `1.10.3` oracle, the `parity/ts/` lockfile, `make parity` / `make parity-regen`, and the vectors-generated-from-TypeScript job.*

### 17.2 Required tests

| # | Area | Test |
|---|---|---|
| 1 | **Encoder goldens + determinism** | The golden set of §7.4 item 1 (36 B floor, 99 B sample, 211 B extreme, an all-negative record, a 297 B on-cap record) compared against the committed files; the `appendScriptNum` table; the push-length boundary table; the negative cases of §7.5. **Plus determinism:** encoding the same record 100 times yields identical bytes, and the field order emitted equals `FieldSchema`'s slice order (so a map can never creep into the encode path). *(These are **self-generated** goldens — asserted against the committed Go output, never against TypeScript.)* |
| 2 | Encoder invariant | No field can ever emit `0x6a` in an opcode position (push ops cap at `0x4b`, below `OP_1NEGATE`). |
| 3 | **Encoder cap boundary** | A synthetic record encoding to **exactly 297 B is accepted**; one encoding to **298 B is rejected** with `ErrScriptTooLarge` **by the encoder**; the publisher refuses it **before** `CreateAction`; the 211 B fixture is ≤ 297 B. This pins the money boundary of §7.6 from both sides. |
| 4 | **Round-trip + decoder negatives** | `Decode(Encode(x))` deep-equals `x` for every golden record **and** for randomly generated in-range records (property test). Negatives: version ≠ 1; **fewer than 36 chunks**; trailing chunks tolerated; a non-minimal push length; `0x6a` in a field position. **One layout only** — *the prefix-less 34-chunk legacy fixture is removed with the legacy layout (§7.7)*. |
| 5 | Float helpers | `EncodeFloat`/`DecodeFloat`/`ValidateFloatPrecision` tables including non-default scales (100, 1e9) and the epsilon cases, **plus the §7.3 rounding-rule table** — `-1.2345675`, `-0.0000005`, `-1.5e-6`, `-2.5e-6` asserted against the documented half-away-from-zero rule (**not** against ECMAScript's `Math.round`). |
| 6 | *(removed)* | **The human-gated real on-chain fixture test is deleted.** It required pulling a historical weather output script off WhatsOnChain and asserting the Go encoder re-produced it byte-identically — a byte-parity assertion (§7.0), against a transaction that **cannot be located** now that the txid store is gone (§14.1). Nothing replaces it at this number; test 22 already checks a **freshly published** transaction against reality, which is the part that had operational value. |
| 7 | HTTP golden files | One golden response file **per endpoint**, byte-compared, driven by a fake store: stations list, station detail, weather list, weather detail, verify, health, proof. Assert **every** §13.1 crash-list item: `txRecords` non-null; `lastTemp` **present and explicitly null** (a test that fails if `omitempty` is ever added); `blockchain` non-nil with all three keys; all 33 `data` keys present with correct JSON types; `totalPages == 0` when `total == 0`; clamped `page` echo; **`items` sorted `created_at DESC`**; `timestamp` matching `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`; a bare `?` query string accepted; the trailing-slash route; the `stationId` filter; an unknown `status` → 400. **Plus the §6.0 control-19 headers** (`X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`) present on every one of these responses. |
| 8 | **Verify contract** | Response is a **bare top-level map**; **every request txid is echoed VERBATIM as a key**, including a mixed-case input; **no value is `null`**; `blockHeight` is `null`, never `0`, when unconfirmed; a 101-txid body → 400; a 65 KiB body → 413; outbound concurrency never exceeds 8; a confirmed txid **persists** `block_height` on the row **and** `stations.last_block_height` in one transaction. |
| 9 | **SSE contract** | The response contains the literal line `event: stats_update`; `data:` is a **single line** of valid JSON with the four stats keys; **one event is written immediately on connect**; a `:ping` comment appears within 31 s and is **not** a `stats_update`; `Flush` is called per write; the handler returns on `ctx.Done`; the 13th concurrent connection from one IP is refused; the global cap returns 503. **And the §6.0 control-20 asymmetry:** a non-SSE handler gets a per-response write deadline while the SSE handler gets **none**, asserted directly rather than inferred from the absence of a timeout. |
| 10 | **Claim concurrency** | Postgres-backed: N goroutines call `ClaimPending` simultaneously; assert **zero double-claims**. Plus a reaper test (lease expiry → `pending` + `adopt_required`). Wired to a CI job with `services: postgres:17-alpine`. **This is the headline fix, it is currently untested, and per §5.0 it is the sole replacement for an accidental protection.** |
| 11 | Adoption | A fake `ActionCreator` that "times out" after committing; assert the next tick adopts by label and does **not** create a second action; assert vout recovery by script matching including at a non-zero index. **Assert an `aborted` labelled action is NOT adopted** — a requeued row whose prior `claim_ref` matches an `aborted`/`failed` action must fall through to a fresh batch rather than be marked `completed` on a dead txid. |
| 12 | Error classification | A table over every classifier input; assert a funding error (`ErrNotEnoughFunds`) **never advances `attempts`** and opens the breaker; assert an encode error advances only its own row; **assert the `WERR_REVIEW_ACTIONS` + `reviewActionResults[].status=="doubleSpend"` shape is classified `ClassDoubleSpend` and retried up to 5 times without advancing `attempts`** (§5 row 9). |
| 13 | Rate limiting | **Peer gate, both directions:** from a peer INSIDE `TRUSTED_PROXY_CIDRS`, the key is `CF-Connecting-IP` when present, the rightmost-untrusted XFF hop otherwise, and `RemoteAddr` only as a last resort; from a peer OUTSIDE the list, `CF-Connecting-IP` and `X-Forwarded-For` are **ignored** and the key is `RemoteAddr` — so 1 000 requests bearing 1 000 distinct `CF-Connecting-IP` values from one untrusted peer produce **one** key and get 429ed. Assert the empty-list case trusts nothing and emits the boot WARN. Plus: a spoofed `X-Forwarded-For` cannot mint unlimited keys (the map is bounded); `/api/health` and `/api/ready` are **never** limited; an `/api/events` request does **not** decrement the general bucket; `RateLimit-*` and `Retry-After` headers on a 429. |
| 14 | Input validation | Every row of §6.4's inventory: no NaN-equivalent path; `?limit=abc` yields the default, not null; `?search=0` returns 200 (not the TS 500); a malformed `/api/weather/{id}` → 400/404, never 500; a station id overflowing `int32` → 400 not 500; `websearch_to_tsquery` never errors on adversarial input (fuzz it). |
| 15 | Error leakage | A `*pgconn.PgError` never appears in any response body; every 500 carries a `request_id`; no configured secret appears in any log or error string. |
| 16 | Tempest mapper | `httpmock` table over the **documented Go coercion rules** (§7.5): the `0`/`""`/`false` fallbacks for **absent** fields, and the **reject-on-unparseable** rule — including that a **fractional value in an integer field is rejected, not truncated** (the deliberate divergence from JS `parseInt`, which was only ever mimicked for parity). (The whole TS service layer was untested.) |
| 17 | Config | **One case per numbered rule of §8.15, cited by number**, each asserting a rejecting input *and* an accepting input: rules **1–7** (secrets, key/network, storage URL, distinct ports, DSN), the mirrored-knob rules **8** (`MaxOneClaimScriptBytes(D) ≥ 36`; asserted at D=23 reject / D=24 accept, **not** against a hardcoded `D ≥ 24`), **9** (`FanoutOutputsPerTx == 100`), **10** (`WEATHER_MAX_SCRIPT_BYTES ≤ MaxOneClaimScriptBytes(D)`, plus the `WEATHER_ALLOW_MULTICLAIM_SCRIPTS` downgrade), **11** (`WEATHER_OUTPUTS_PER_TX ∈ [1,25]`), **12** (the pool-floor predicate, asserted at the shipped values to need 475 against a low water of 600, and to **reject** when `FUEL_TARGET_POOL_SIZE` is lowered to 700), **13** (the one-round cold-start predicate, `12 ≥ 10`; rejects at 9), **14** (lease > timeout), **15–19** (intervals, water band, CIDR parsing + the empty-list WARN, proof limiter, log level), and **20** (`Validate()` returns **all** failures, not the first, and no error string contains any secret). Plus the **reduced rule set** — `Validate()` for `deposit-address`/`deposit`/`requeue`/`stats-recompute` succeeds with `TEMPEST_API_KEY`, both ports and every fuel knob unset (rule 3 and the `S`-only rows of the §8.15 inventory). |
| 18 | Fuel math | `MaxOneClaimScriptBytes` reproduces §8.3 and is asserted over **D ∈ {17, 20, 24, 28, 30, 40, 50, 55, 56, 60}** — the set containing every denomination where the tempting `10d − 201` closed form is wrong (§8.4); a D ∈ {20,30,40,50,60} sample cannot catch it. Plus a table test asserting `ClaimsRequired` reproduces the claim count of **every row of §8.5 and §8.8**. |
| 19 | Keeper overrides | Each of the three overrides differs from what `FromThroughput` would have inherited; `Denomination` and `FanoutOutputsPerTx` equal the mirrored server values; the pool gauge is `TotalOutputs`, **not** a `len()` (§5 row 5); a de-duplicated top-up does **not** double-apply the winner's accounting and does not log a mint it did not perform (§5 row 4). |
| 20 | Deposit | Vout resolution at a non-zero index; failure when absent; a per-deposit suffix is generated (not a constant). |
| 21 | Shutdown budget | Per-call timeouts < wait budget < grace period; the poller honours `ctx.Done` **between stations**. |
| 22 | **Integration (or a sampled runtime check)** | Fetch a published transaction and assert the weather script sits at the **expected vout**, and that the transaction has **exactly N outputs with no change output** (§8.6). With `ReturnTXIDOnly: true` the `CreateAction` result carries no outputs, so the persisted vout is otherwise never checked against reality — a future server-side output reordering (e.g. a commission output moving ahead of provided outputs) would corrupt every record silently. |
| 23 | Stats | `Complete` bumps `total_tx` by **1 per transaction** (not per record) and `total_records` by the batch size, in one transaction; `activeStations` reflects `stations.is_active` and can never be stuck at 0; `weather stats-recompute` reproduces the incremental counters from scratch. |

### 17.3 CI

The repo has **one** workflow today (`build.yml`) and no Go CI. Two workflows after this port: the rewritten `build.yml` and the new `go.yml` (§15.8), plus CodeQL extended to `go`.

---

## 18. Accepted risks

Named explicitly rather than hidden. **None of these is softened.**

### 18.1 Residual duplicate-publish window

The adopt-by-label mechanism (§10.2) closes the crash/timeout duplicate path in every case where `ListActions` can see the committed action. It does **not** close: a label-index defect on the server, a `ListActions` call answered from a different `userID` scope, or an operator running two instances against one database (prevented by `replicas: 1` + `Recreate`, **not by code**). `ListActions` failures are classified Infra and open the breaker rather than triggering a republish, so the window is narrow. **Accepted.**

**And note the sharper edge introduced by server-side funding:** with the funding-UTXO collision gone, two overlapping pods would both **succeed** rather than one failing (§5.0). The atomic claim plus single-replica deployment are the only things standing between this app and duplicate on-chain records. **This is the highest-consequence accepted risk in the document.**

### 18.2 Delegated spending authority (CVE-2026-56744 class)

Unfixed on `go-wallet-toolbox` main; the storage server signs storage-supplied output scripts verbatim (§12.1). The key stays in this process, but its spending authority is **effectively delegated**: a compromised or buggy storage server can redirect the entire standing float and every claimed fuel/chunk input, on mainnet, under a reused funded identity. The mitigation is **exposure sizing** (≈ 1 580 000 sat maximum blast radius, §12.2), **not elimination**. **Accepted, with the upstream assembler fix recorded as the real remedy.**

### 18.3 Unrecoverable records and changed permalinks

**This is a data loss that has already happened, not a trade this port is making.** The MongoDB Atlas cluster holding the weather records **and their txids** no longer resolves (`NXDOMAIN`, §14.1), so: **every bookmarked `/weather/:id` 404s**; the anchored-reading history **restarts at zero**; and the old records cannot be recovered from chain either, because the txids that would locate them died with the cluster and the TS actions carried no labels. Relatedly, the Go decoder reads **only** the layout the Go encoder writes (§7.7) — historical on-chain layouts are unsupported, which costs nothing, since they are unlocatable regardless. Separately, the dashboard's `totalTx` tile becomes an honest transaction count and therefore reads ~19× smaller than the number the TypeScript displayed (§13.3). **Accepted, because no alternative exists** (§14.1).

### 18.4 Unreachable legacy funds

The legacy hash-puzzle UTXOs on `store-us-1.bsvb.tech` cannot be swept while that server's `/.well-known/auth` returns 500 — **by anyone**, because each output's preimage lives only in that server's `customInstructions`. **Accepted as deferred**; no sweep is designed.

### 18.5 Read-API downtime on DB restarts

Single-replica Postgres on an RWO PVC with `strategy: Recreate` means the read API is unavailable for the duration of every DB restart, node drain or image bump (tens of seconds). §11.2's liveness/readiness split is what keeps that from escalating into a CrashLoopBackOff that also stops weather collection. **Accepted** for a demo.

### 18.6 A 298–331 B record is refused where TS would have published

The 297 B cap is new relative to TS, which had no size cap. A record between 298 and 331 B is marked `failed` rather than published at 2 claims. The worst measured real record is 169 B and the repo's own extreme fixture is 211 B, so the margin is 86–128 B; `WEATHER_ALLOW_MULTICLAIM_SCRIPTS` is the escape hatch. **Accepted** (§7.6).

### 18.7 Fuel-pool gauge is an upper bound

`ListOutputs(fuel).TotalOutputs` counts `sending`-status fuel that the funder (tiers `[mined, unproven]`) cannot yet claim. Immediately after a mint the keeper can see a full pool while `CreateAction` still fails with not-enough-funds. Absorbed by pool size (1000 gives 272.7 s of burst runway and 7.6 h of steady runway); logged as an upper bound; **repeated `ErrNotEnoughFunds` while the count looks healthy is the signature of this condition, not of an empty pool.** **Accepted** — the shared `spend_policy` is not widened for one tenant.

### 18.8 Raised rate limits are a deliberate availability-over-strictness trade

The general `/api` limit rises from 100/min to 600/min per real client IP (§6.1). Combined with correct per-IP keying this is **strictly stricter in practice** than the shipped configuration (which is one global bucket for the whole internet), but a single IP can now issue 6× more requests than the literal TypeScript number. The alternative — 100/min per IP against a frontend that fires two prefetches per hovered row across 50 rows — makes the dashboard unusable for one engaged user. Alarm 10 watches the 429 rate so the number can be tuned with evidence. **Accepted.**

### 18.9 The ModSecurity WAF interaction is unverified

SSE through the traefik-modsecurity plugin, and `POST /api/verify`'s hex-array body against CRS PARANOIA=1, have **never been exercised for this app** because it has never been live in this cluster (§15.7). This is the **most likely day-one surprise**. Mitigation is a scoped exclusion following an existing pattern, applied reactively at step 11 of the checklist. **Accepted as a known unknown with a named response.**

### 18.10 The fuel tables say nothing about lock contention

The D=50 arithmetic was produced by driving the collector, not the SQL layer (§8). It is sound for claim counts and fees, but it does **not** model `FOR UPDATE SKIP LOCKED` contention, lock-wait, or partial-pool behaviour. At 288 tx/day that is a safe omission; if this app ever runs at real throughput the contention question must be re-opened. **Accepted.**

---

## 19. Documentation deliverables

| File | Fate |
|---|---|
| `ENCODING.md` | The **Field Order section is factually wrong** — it contradicts `src/format/schema.ts`, which is alphabetical. **Corrected to alphabetical**, with a warning that `internal/weather/schema.go` is now the only normative source. **This is the single most dangerous artifact for anyone touching the encoder later.** **Rewritten to document the format as internal** (§7.0): valid script, fixed order, 297 B cap, round-trip, Go rounding rule — and to state that byte-parity with the old TypeScript is not a requirement. *(It is no longer "the human-readable half of the oracle"; there is no oracle — §7.0.)* |
| `SPEC.md` | **Rewritten.** ~60 % of it is the funding spec that no longer exists. Kept in place at the repo root; **not** relocated to `parity/ts/`, which is not created (§5 row 20). |
| `PLAN.md` | Phases 4/5/6 and the economics sections **deleted**. |
| `VALIDATE.md` | §2, §6 and objectives O4/O6 **deleted**; **§4.2's documented state machine finally becomes true** (terminal `failed` is reachable for the first time — §5 row 15). |
| `README.md` (root) | Funding section removed; the stale "111 tests / 98 % coverage" claims removed. |
| `frontend/README.md` | **Line 157 ("The included Dockerfile builds and serves via nginx") is false at HEAD and is exactly the trap that produced the phantom port mismatch.** Corrected. |
| `CONTAINERIZATION_COMPLETE.md`, `DOCKER.md`, `DOCKER_SUMMARY.md`, `DOCKER_QUICKSTART.md`, `QUICKSTART.md`, `VALIDATE.md`, `IMPLEMENTATION_SUMMARY.md`, `PLAN.md` | **Eight stale Node-deployment docs that will actively mislead.** Delete, or move to `docs/legacy/` if anyone objects. Note `QUICKSTART.md:41` contains a committed private key (§2.1), `DOCKER.md` hardcodes `MONGO_INITDB_ROOT_PASSWORD: password`, and `DOCKER_QUICKSTART.md:170` is one of the sources of the nginx phantom. |
| `docs/RUNBOOK.md` | **New, ships in the same PR.** Six procedures, each with the exact command, the expected log lines, and which alarm should have fired: **(1) wallet out of money** (`weather deposit-address` → `weather deposit`), **(2) storage server down** (expected degraded state, what keeps working), **(3) fuel keeper silent** (log keys to grep, `weather preflight`), **(4) rows stuck in `failed`** (`weather requeue`), **(5) database restore** (`pg_restore` from the CronJob's S3 bucket, plus the chain-reconstruction procedure of §14.4), **(6) the dashboard tiles look wrong** (`weather stats-recompute`). Plus the one-line CORS re-add path (§6.5) and a link to the upstream assembler issue (§12.3). |
| `docs/alerts/weather-proof.md` | **New** — the eleven alarm definitions of §11.3, their destination and owner. |
| `docs/specs/prove-data-lock.md` | **Dropped.** It was carried forward from a file that does not exist at `HEAD` (§5.2). |

**Coupling note:** `src/notification/*` is deleted **only because** the §11.3 monitors ship alongside it. If those monitors slip, a minimal in-process WARN→webhook path stays in the Go port (§5 row 11).

---

## 20. Open logistics items

These are logistics, not design decisions. Each has a named decision point before the deploy PR.

| # | Item | Decide by |
|---|---|---|
| 1 | **SSM `POSTGRES_PASSWORD` and the IRSA policy scope.** Requires a human with AWS SSM access in us-east-2, profile `bsva`, account **891377253213**. The IAM policy may grant `ssm:GetParameter` on an **enumerated** ARN list rather than the `/apps/weather-chain/*` prefix, in which case it needs updating — and the failure mode is a silently stuck ExternalSecret, not an error. *(Unverifiable from the research environment: those AWS credentials were account 381492298518.)* | **before P4** (§0 P3) |
| 2 | **Storage-server restart window.** The D=50 change requires **recreating** the go-wallet-toolbox pod (subPath mounts never refresh). Pick a window; the server currently has zero transactions, so the blast radius is nil. | before P1 merges |
| 3 | **Cloudflare rate-limiting plan tier.** The edge rule recommendation (§6.1) assumes a single free-plan Rate Limiting Rule. Confirm the tier before committing to thresholds. The in-process limiter is required regardless. | with the deploy PR |
| 3b | **Confirm the `bsva-us-1` pod CIDR** for `TRUSTED_PROXY_CIDRS` (§15.3) against the live cluster rather than shipping the `10.0.0.0/8` placeholder on faith. **This is not cosmetic:** an empty or wrong list makes `clientIP` ignore every forwarding header and the `/api` limiter degrades to one bucket per proxy pod — §6.1 defect 1, the exact bug the port exists to fix. It fails closed and WARNs, so it is safe but must not ship unverified. | **with the deploy PR** (before §21.2 step 8) |
| 4 | **Hostname change.** `weather-proof.bsvblockchain.tech` → `weather-proof-us-1.bsvblockchain.tech`. Confirm the four legacy CNAMEs (created by external-dns from the now-deleted Ingresses) can be released, and notify anyone holding a bookmark. | with the deploy PR |
| 5 | **The true station count behind the Tempest token.** The `'19+'` figure is a hardcoded marketing string (§13.0). It feeds the fuel sizing (§8.12's K column). Sizing is safe for K ≤ 20. | opportunistic |
| 6 | ~~**Old Mongo snapshot** (`mongoexport` of `weatherrecords`)~~ **— REMOVED, not deferred.** The Atlas cluster is already gone: `dig SRV _mongodb._tcp.weatherchain.mezgo0s.mongodb.net` returns **NXDOMAIN** while DNS is otherwise healthy (§14.1). There is no host to export from. Struck rather than deleted so it is not re-proposed as cheap insurance. | **n/a — impossible** |
| 7 | **File the throughput-dashboard denomination bug** against `go-wallet-toolbox`: `cmd/throughput_dashboard/internal/config/config.go:106` hardcodes `DenominationSatoshis = 30` while its own local `infra-config-docker-throughput-mainnet.yaml` derives 20, and `DemoDenomination` short-circuits on the explicit value so no env var or ConfigMap key can override it. **Unrelated to this port** (§0.2) but discovered by it. | opportunistic |

---

## 21. Implementation phases and deploy checklist

### 21.1 Build order

Four independently mergeable units across two repos — the `bsva-infra-flux` denomination PR (§0.3), the Go port itself, the `bsva-infra-flux` manifest set (§15.3), and docs/alarms/CI.

| # | Phase | Exit criterion | Blocked by |
|---|---|---|---|
| 1 | **Go module bootstrap.** There is no Go module in this repo today. Create `go.mod` at the repo root (module `github.com/bsv-blockchain-demos/weather-proof`, `go 1.26.3`) with `go.sum` committed, `.golangci.json` (§4.1), the `Makefile` (`build test lint up down migrate`) and the `go.yml` skeleton (§15.8). *(This replaces the deleted "golden parity vectors" phase — see §7.0. It has no TypeScript step, no `@bsv/sdk` pin and no ordering constraint against any TS change.)* | `go build ./... && go test ./... && golangci-lint run` green on an empty tree | **Nothing — start today.** Blocks everything, because nothing else compiles without it. |
| 2 | **`internal/weather`** — types, schema, scriptnum, float, encoder, decoder, and the §7.4 goldens committed. | tests 1–5 green (test 6 no longer exists) | phase 1 |
| 3 | **`internal/store`** + `migrations.sql` + the atomic claim, reaper, stations, stats and adopt-support queries. | test 10 green on the CI Postgres service | phase 2 |
| 4 | **`internal/api`** + DTOs + middleware + rate limiting + SSE + verify + proof. | tests 7, 8, 9, 13, 14, 15 green | phase 3 |
| 5 | **`internal/walletconn` + `internal/fuel` + `internal/fuelmath` + `internal/publisher` + `internal/pipeline`.** | tests 11, 12, 17, 18, 19 green | phases 2–4, **and P1/P2 merged** (§0) — the preflight and any live fan-out are meaningless against a server still at D=20 |
| 6 | **`cmd/weather` CLI + `internal/obs` + the frontend fixes (§13.9) + docs + alarms + the manifest set + CI.** | §21.2 complete | phase 5 |

**Phases 1–4 are independent of the denomination prerequisite** and proceed in parallel with the P1/P2 window; only phase 5 onward requires it. Phases 1 and 2 go first because everything else compiles against them — **not** because of any byte-parity critical path; that path is deleted (§7.0), and with it the old constraint that the encoder work had to land before any TypeScript change. **No TypeScript is deleted as part of phases 1–2**, and none needs to be (§5 row 20).

### 21.2 Deploy checklist, in dependency order

Every one of these fails **invisibly** if taken out of order.

| # | Step | Owner | Failure if skipped |
|---|---|---|---|
| 1 | Create SSM `/apps/weather-chain/POSTGRES_PASSWORD` (SecureString) and confirm `SERVER_PRIVATE_KEY` and `TEMPEST_API_KEY` are present | infra | Missing parameter → the pod sits in `CreateContainerConfigError` with no useful signal |
| 2 | **Verify the IRSA policy** covers the new key (prefix vs enumerated ARNs) | infra | A silently stuck ExternalSecret, not an error |
| 3 | **Merge the denomination PR** (`denomination_satoshis: 50`, `expected_tx_size_bytes: 500`, `expected_output_satoshis: 0`) | infra | Every weather tx cascades 4–26 claims; 2.60× cost |
| 4 | **Recreate the storage-server pod** (`kubectl rollout restart deployment/app -n go-wallet-toolbox`) and confirm the resolved denomination in its logs | infra | **The merged value has no effect at all** — `infra-config.yaml` is a subPath mount and never refreshes |
| 5 | **Fix the frontend build inputs FIRST:** add `frontend/.dockerignore`, add `ARG VITE_BSV_NETWORK` to `frontend/Dockerfile`, delete the repo variable `VITE_API_URL`, and set the build args to `VITE_API_URL=` / `VITE_BSV_NETWORK=main` | repo owner | `VITE_*` are baked at **image build time**. Doing this after the publish step means the published image still carries the legacy absolute host and links mainnet txids to testnet |
| 6 | Publish `weather-proof-back` and `weather-proof-front` on a **pinned tag**, with the front image gated by the bundle-verification step of §15.8 | repo owner | A published front image that points at localhost or the retired `bsvb.tech` host |
| 7 | Merge the weather-proof manifest PR **with the include line still commented** | infra | — |
| 8 | **Uncomment `- ../base/weather-proof`** in `apps/bsva-us-1/kustomization.yaml`. **This is the step that makes weather-proof exist in the cluster** — until it lands, `prune: true` means there is no namespace, no PVC and no ExternalSecret, so there is nothing for step 9 to inspect or force-sync. Bringing the app up before the deposit is safe: §3.2's boot order plus §8.14's `default`-balance gate mean it boots `degraded: "unfunded"`, serves every read endpoint, and **never crashloops on an empty wallet** | infra | Leaving it commented is worse than enabling early: step 9 has nothing to verify, and step 10 has no pod to exec into and no Postgres for the `deposits` table |
| 9 | **Now that the objects exist:** confirm `kubectl get pvc -n weather-proof postgres-data` is **Bound** and both ExternalSecrets report **Ready** (force the first sync with the `force-sync` annotation of §15.6) | infra | With the default 1 h refresh the pod waits an hour in `CreateContainerConfigError`; an annotated PVC would sit `Pending` forever with Flux reporting success |
| 10 | **Operator deposit:** `kubectl exec deploy/weather-proof-back -n weather-proof -- weather deposit-address`, send **1 500 000 sat**, then `… weather deposit --txid <id>`, confirm the `default` balance. Both subcommands need Postgres (they persist to and read `deposits`) and run under the **reduced rule set of §8.15** (`D` column) | operator | The app starts with zero fuel and dies **silently** — `ensureChunks` logs a WARN and returns `nil` |
| 11 | **Immediately test the WAF interaction (§15.7):** open `/explorer` and confirm the Live dot goes green **and the stat tiles update**; then trigger `POST /api/verify` from a station page | operator | SSE buffered by the ModSecurity plugin → the stats bar is permanently "connecting" with no error anywhere. **This is the most likely day-one surprise** |
| 12 | Create the eleven Coralogix alarms of §11.3 | operator | The two most dangerous states are non-erroring and would go unnoticed |
| 13 | Watch the first heartbeat: `walletConnected:true`, `degraded:""`, `fuelPoolOutputs` climbing 0 → 1000 in **one** round | operator | — |
| 14 | Watch the first publish: one `CreateAction`, **`claims=11` expected at K=20/S=169** (or 8 at S=121), a `completed` row with a real txid, `outputIndex: 0`, and **no change output on chain** | operator | — |
| 15 | Confirm `weather-proof-us-1.bsvblockchain.tech` serves the SPA at `/`, `/explorer` and `/station/<id>` deep links resolve (SPA fallback), `/api/stations` returns items, and `/build-info.json` reports `viteBsvNetwork: "main"` | operator | — |
| 16 | **Follow-up commit:** add `kustomize.toolkit.fluxcd.io/reconcile: disabled` to the now-Bound PVC (§15.4) | infra | Flux churn on future storage edits |
| 17 | **Later, separately:** delete `/apps/weather-chain/MONGO_URI` from SSM once the new app has been healthy for a while | infra | **None — the parameter is already inert.** It points at a cluster whose SRV record returns NXDOMAIN (§14.1), so it is a dead string, not a pointer to a recoverable dataset. Deleting it loses nothing; the only reason to wait is to avoid touching SSM during the cutover |

---

## 22. Requirements traceability

| ID | Theme | Section |
|---|---|---|
| R1 | Operator deposit path | §5 row 7, §9, §21.2 step 10 |
| R2 | Exactly-once publishing, and the atomic claim as **mandatory** | §5.0, §5 row 1, §10.1–§10.4, §18.1 |
| R3 | Error classification, breaker, requeue, poison isolation | §5 row 15, §10.3–§10.5, §10.7 |
| R4 | Trust-boundary correction (CVE-2026-56744 class) | §12.1–§12.3, §8.12, §18.2 |
| R5 | Two-shape fuel preflight | §8.14, §3.2 |
| R6 | Graceful shutdown | §16.1 |
| R7 | Infra manifest set, updated in place | §15.3–§15.6 |
| R8 | Ordered first-deploy checklist | §21.2, §20 |
| R9 | Denomination cross-repo prerequisite (D=50), and the deleted frontier premise | §0, §8, §21.2 steps 3–4 |
| R10 | **Security parity — every TS control and its Go equivalent** | **§6** (rate limiting §6.1, SSRF §6.2, amplification §6.3, injection + input validation §6.4, CORS §6.5, secrets/images/supply chain §6.6, body limits + timeouts §6.7, workflow permissions + scanning §6.8) |
| R11 | Broadcast outcome reconciliation, `block_height` written for the first time | §10.6, §2.2 |
| R12 | Keeper supervision, single-flight, single instance | §5 row 4, §16.2, §15.3 |
| R13 | Observability and alerting | §11 |
| R14 | Boot dependency ordering | §3.2 |
| R15 | Errgroup error policy | §10.9 |
| R16 | Encoding error semantics | §7.5 |
| R17 | Duplicate readings | §10.8, §4.2 |
| R18 | Configuration inventory and mirrored knobs | **§8.15 (the inventory table + the 20 numbered rules)**, §8.13, §15.3, §6, §17.2 test 17 |
| R19 | Secret redaction | §12.5, §6.6 |
| R20 | `/api/proof` decision and hardening; atomic BEEF | §13.6 |
| R21 | **The API the redesigned frontend needs** (5 endpoints + SSE + probes), required vs incidental | §13 |
| R22 | The two live shipped-image bugs (testnet links, global rate bucket) | §2.3, §6.1, §13.9 |
| R23 | **Encoder determinism, the 297 B cap, the round-trip, and the self-generated goldens** — and the explicit release of byte-parity | **§7.0** (why parity is not required, and what was dropped), §7.2–§7.4, §7.6, §7.7, §17.1, §17.2 tests 1–5, §5 row 20 |
| R24 | Test contract coverage | §17.2 |
| R25 | Quantitative derivation at D=50 (windows, sawtooth, cascade, pool, chunks, burn) | §8.1–§8.12 |
| R26 | **Unrecoverable old data** (NXDOMAIN Atlas cluster, txids lost with it, no backfill possible), permalinks, honest stats semantics | §14.1, §2.2, §13.3, §18.3, §20 item 6 |
| R27 | Legacy funds and identity reuse | §14.3, §18.4 |
| R28 | Documentation, runbook, alarms | §19 |
| R29 | Durability (`synchronous_commit`, backups, reconstructibility) | §14.4, §18.5 |
| R30 | **Evidence-based deletion, including `ea0c654` / `11ecfd5` / `cc5baef`** | **§5** (esp. §5.0 and rows 1, 4, 5, 9, 11, 15), §5.2 |
| R31 | The nginx reverse proxy: why it was reverted, and that it is ruled out | §15.1, §13.9 item 3 |
| R32 | Naming (`weather-proof` everywhere) and the `/apps/weather-chain/*` SSM wart | header, §15.6 |
| R33 | Deploy shape: two images, one Cloudflare-tunnel Ingress, path split, port 8080 | §15.2, §15.5 |
| R34 | WAF interaction as a named day-one risk | §15.7, §18.9, §21.2 step 11 |
