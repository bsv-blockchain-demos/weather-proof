# HTTP API — as shipped by Plan B2

This document describes the endpoints **as built**, not as originally planned. Where the plan or the design spec
says something different, this file is the record of what the code does; the two known corrections are listed at
the end.

Everything here is served by `internal/api` against `store.Store` (B1). There are **two listeners**:

| Listener | Env var | Handler | Reachability |
|---|---|---|---|
| API | `API_PORT` | `api.NewRouter(Deps)` | public, behind the path-split Ingress on `/api` |
| Ops | `OPS_PORT` | `api.NewOpsRouter(Deps)` | cluster-internal only |

`/api/ops` is on the **ops** listener and nowhere else. That is not a registration-order convenience: §15.5
path-splits a single Ingress on the `/api` prefix, so anything registered on the public port under `/api` is
world-reachable at the public hostname.

Both servers must be started through `api.ListenAndServe`, which refuses a server whose `BaseContext` is nil.
Call `api.AttachBaseContext(ctx, srv)` first — without it, `api.Shutdown` cannot end a live SSE stream and every
deploy burns the whole 5 s shutdown budget silently.

## Endpoint inventory

| Method + path | Listener | Limiter scope | Response |
|---|---|---|---|
| `GET /api/weather` (and `/api/weather/`) | API | general | `{"items":[…],"pagination":{…}}` (spec §13.2) |
| `GET /api/weather/{id}` | API | general | bare `weatherDetail` — the list item plus `error` (spec §13.5) |
| `GET /api/stations` (and `/api/stations/`) | API | general | `{"stats":{…},"stations":[…],"pagination":{…}}` (spec §13.3) |
| `GET /api/stations/{stationId}` | API | general | bare `stationSummary`, the same nine fields, unwrapped (spec §13.4) |
| `GET /api/events` | API | sse | `text/event-stream`, see below (spec §13.7) |
| `POST /api/verify` | API | verify | **501** `{"error":"not implemented"}` until B3 |
| `GET /api/proof/{txid}` | API | proof | **501** `{"error":"not implemented"}` until B3 |
| `GET /api/health` | API | **none** | `{"status":"ok","timestamp":"…"}` — always 200 |
| `GET /api/ready` | API | **none** | `{"status":"ok"}` / 503 `{"status":"unavailable"}` |
| `GET /api/ops` | **Ops** | none (port-separated) | see "The ops snapshot" below |
| anything else | either | none | 404 `{"error":"not found"}` |

Notes that are contract, not detail:

- **A bare trailing `?` is a normal request.** Both frontend call sites hardcode it in a template literal.
- **Both list paths are registered twice**, as the exact pattern and as its `{$}` twin, because Go 1.22's
  `ServeMux` does not match a trailing slash against an exact pattern. There are no redirects.
- **Any method other than the one registered gets a JSON 405** with an `Allow` header naming that one method.
  `HEAD` is 405'd like anything else; nothing in the spec requires it.
- **Item order is `created_at DESC, id DESC`**, provided by the store. No handler re-sorts.
- `totalPages` is **0** when `total` is 0, so the SPA's Next button disables.
- All timestamps are `2006-01-02T15:04:05.000Z` — UTC, exactly three fractional digits.
- **No field anywhere carries `omitempty`.** `station.lastTemp` in particular is present and explicitly `null`
  when unknown; an omitted key is `undefined` in the SPA, `undefined !== null` is true, and the UI renders the
  literal string `"undefined°C"`.

### Query parameters

| Endpoint | Parameter | Default | Clamp / rule |
|---|---|---|---|
| `/api/weather` | `page` | 1 | ≥ 1, and capped so `page × limit ≤ 100 000` |
| `/api/weather` | `limit` | 20 | 1 – 100 |
| `/api/weather` | `stationId` | absent | must parse as an integer, else **400** `{"error":"invalid stationId"}` |
| `/api/weather` | `status` | absent | one of `pending`, `processing`, `completed`, `failed`, else **400** `{"error":"invalid status"}` |
| `/api/stations` | `page` | 1 | as above |
| `/api/stations` | `limit` | 50 | 1 – 200 |
| `/api/stations` | `search` | absent | trimmed; fails `store.ValidText` → **400** `{"error":"invalid search"}`; a value parsing as an integer is an exact `station_id` lookup |

An absent, empty or unparseable `page`/`limit` falls back to the default rather than erroring — parse and clamp
are separate steps and there is no NaN-equivalent path. A path segment failing `store.ValidText` is
**400** `{"error":"invalid id"}`, not 404: "not found" is the wrong answer to a malformed request. Record ids are
**opaque** — `uuid.Parse` is deliberately not applied (see the plan's open question 4).

`station.status` is the derived literal `online` or `offline` only: online iff the station is active **and** its
last reading is within `3 × pollRate` of now. `chain_status` (`arc-accepted`, `unmined`, `mined`, `aborted`) is
projected onto the four wire statuses and **never** appears in any response body.

## Rate limiting

Four **disjoint** scopes. Each is its own `*ratelimit.Limiter`, applied per route at registration, so a request
consumes exactly one bucket. The window is a fixed one minute; the key map is bounded at 10 000 entries with
eviction.

| Scope | Routes | Configured | `RateLimit-Limit` advertised and enforced |
|---|---|---|---|
| general | the four read endpoints and their `{$}` twins | 600/min + burst 120 | **720** |
| verify | `POST /api/verify` | 60/min | 60 |
| sse | `GET /api/events` | 30 new streams/min | 30 |
| proof | `GET /api/proof/{txid}` | `PROOF_RATE_LIMIT_PER_MIN`, default 60, floored at 1 | that value |

**720, not 600.** Burst is permanent capacity rather than a start-of-window allowance, so `Decision.Limit` is
`limit + burst` and the **721st** request in a window is the first refusal. This was ruled deliberately; the
plan and spec have been corrected to match.

Beyond the per-minute scopes, `/api/events` also enforces **12 concurrent streams per IP** (429) and a **global
cap of 500 concurrent streams** (503). Those two are counters in the hub, not limiter buckets, and they refuse
rather than evict — an established stream is never dropped to admit a new one.

`/api/health` and `/api/ready` are registered **before** any limiter and are neither refused nor counted: a
liveness probe that can be rate-limited turns a traffic spike into a pod restart. The catch-all 404 is
unlimited too, deliberately — a limiter there would let an unrouted path drain a real client's bucket.

### Client-IP resolution

No forwarding header is read until the immediate TCP peer is proven to be inside `TRUSTED_PROXY_CIDRS`.

- Untrusted peer → key on the peer address, consult **no** header.
- Trusted peer → `CF-Connecting-IP`, else `X-Forwarded-For` walked from the **right** taking the first hop not
  in the trusted set, else `RemoteAddr`.
- IPv6 keys are canonicalized to a `/64`; every prefix check is preceded by `.Unmap()`.

An empty `TRUSTED_PROXY_CIDRS` is legal, means trust nothing, and emits exactly one boot WARN. A malformed
entry is a startup error.

### The 429 header contract

Every refusal carries all four:

```
RateLimit-Limit: 720
RateLimit-Remaining: 0
RateLimit-Reset: 37
Retry-After: 37
```

`RateLimit-Reset` and `Retry-After` are whole seconds, rounded **up**, so neither is ever `0` on a refusal. The
three `RateLimit-*` headers also appear on every **allowed** response through a limited route; `Retry-After`
does not, because it means "you were refused, wait this long".

A wrong-method request to a limited path **still costs a slot** — the bucket is charged before the method check,
so a free 405 is not an unmetered way to probe the API.

Three 429 bodies exist and they are deliberately distinct, so a log line says which control fired:

| Body message | Meaning |
|---|---|
| `Too many requests, please try again later` | a per-minute limiter scope |
| `Too many concurrent streams, please try again later` | the 12-per-IP SSE cap |
| `Stream capacity reached, please try again later` | the 500-stream global cap (**503**, not 429) |

## Error bodies

Exactly two shapes, and no third:

```jsonc
// every 4xx — exactly ONE key, always
{"error": "Weather record not found"}

// every 5xx — the fixed message plus a correlation id
{"error": "internal server error", "request_id": "0197f0…"}
```

`clientErrorDTO` structurally cannot carry a second key, and `serverErrorDTO.RequestID` is a plain `string`, so
a 500 can never ship without an id. The 400/404 messages the spec pins verbatim are `Weather record not found`,
`Station not found`, `invalid id`, `invalid stationId`, `invalid status`, `invalid search`, `not found` and
`method not allowed`.

**No error is ever formatted into a body.** `store.ErrNotFound` → 404, `store.ErrConflict` → 409, and
**everything else** → the opaque 500 above, with the real error logged through `slog` at error level with the
request id. A `*pgconn.PgError`'s `Error()` carries the SQLSTATE, and the struct carries `Detail`, `Hint`,
`ConstraintName`, `ColumnName` and `TableName`; none of it may reach a client.

`X-Request-Id` is on every response. An **inbound** `X-Request-Id` is ignored entirely: a caller-chosen id lets
an attacker poison log correlation.

## Security headers, timeouts, CORS

All three of these are on **every** response including 404, 405, 429, 500, 503 and the SSE stream:

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
```

No CSP (that is the frontend nginx's, §6.6) and no HSTS (TLS terminates at Cloudflare) — both omissions are
deliberate. **CORS is deleted, not configured:** no middleware, no `CORS_ORIGIN` env var, never a reflected
`Origin`, and `Access-Control-Allow-Credentials` is never set. Both dev and prod are same-origin.

| Setting | API server | Ops server |
|---|---|---|
| `ReadHeaderTimeout` | 10 s | 10 s |
| `ReadTimeout` | 15 s | 15 s |
| `WriteTimeout` | **0** | 15 s |
| `IdleTimeout` | 120 s | 120 s |
| `MaxHeaderBytes` | 16 KiB | 16 KiB |

The API server's `WriteTimeout` is 0 because `GET /api/events` never finishes writing; any finite value there
kills every stream at that mark with no error a client can see. The compensating control is a **per-response**
30 s write deadline applied to every handler **except** the SSE one.

## `GET /api/events` — the SSE wire format

```
event: stats_update
data: {"activeStations":3,"totalTx":41,"lastRecordWrite":"2026-04-17T15:40:00.000Z","totalDataPoints":1353}

```

- Response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`,
  `X-Accel-Buffering: no`. The header block is flushed immediately so `EventSource` fires `onopen`.
- The event is **named** `stats_update`. A default/unnamed `message` event is ignored by the client, which would
  leave the Live dot green while the tiles never update.
- Note the single space after each `:`, the single newline between the two field lines, and the **blank line**
  that dispatches the frame.
- The `data` payload is the same four-key `statsDTO` the dashboard reads, because the SPA replaces the whole
  stats slice wholesale.
- **One frame is pushed immediately on connect**, before any broadcast, so a client is never blank while
  waiting for the next tick.
- The keep-alive is an SSE **comment**, `:ping\n\n`, every 30 s — never a `stats_update`. Cloudflare tunnels
  drop idle streams, so it is not decoration. A heartbeat shaped like a `stats_update` would be swallowed by
  the client's bare `catch {}` and the breakage would be invisible.
- A slow client's buffer is depth 1 and a full buffer **drops** the pending update rather than stalling the
  publisher. Dropping is safe here because every payload replaces the whole state.

## The ops snapshot

```json
{
  "pendingRows": 11,
  "processingRows": 22,
  "failedRows": 33,
  "stillUnminedOlderThan1h": 44,
  "minedCount": 55,
  "abortedCount": 0,
  "activeStations": 19,
  "totalTx": 2701,
  "sseClients": 3,
  "stale": ["abortedCount"]
}
```

### Why `stale` exists

`abortedCount` is **structurally always zero**: the frozen `store.RecordStore` has no method that can write
`chain_status = 'aborted'` (B1's handover, item 7). Dropping the key would break a dashboard silently, and
emitting a permanently-zero number as if it were live is worse than omitting it — a zero reads as an
affirmative all-clear to both an operator and an alarm author.

So the field is emitted **and** named in `stale`, an array of field names whose values are structurally
unreachable. A zero in a `stale` field means "not implemented", not "nothing happened".

`stale` is **derived from the snapshot at request time**, not a static list: `"abortedCount"` is present if and
only if `snap.AbortedCount == 0`. The moment Plan C widens `RecordStore`, wires the write path, and a snapshot
observes a nonzero count, the marker removes itself with no code change — and `TestOpsStaleListNamesAbortedCount`
fails when the gap closes, forcing the entry out. The key is never absent and never `null`: an all-live
snapshot emits `"stale": []`.

The fuel and wallet fields of the §11.1 heartbeat are **absent**, not zero: B2 owns no wallet and no keeper, and
emitting zeros for them would be the same lie `stale` exists to prevent. Plan C adds them with the sampler that
can populate them.

## Known caveats

1. **`abortedCount` is not live** — see above. Tracked as B1 handover item 7; the widening is Plan C's.
2. **`POST /api/verify` and `GET /api/proof/{txid}` answer 501 while their limiter scopes are already live.**
   B3 **replaces** those two handlers at their existing registration sites in `NewRouter`. It must not register
   the paths again: a second registration either panics on a `ServeMux` conflict or leaves the limited copy
   shadowed, and either way one copy ships unlimited. `MaxBytesReader` → 413 is B3's; the placeholder reads no
   body. `markSSEExempt` is for the stream only — both of these want the 30 s write deadline.
3. **Station search diverges between the fake store and Postgres.** Postgres stems (`runs` matches `Running`),
   treats a multi-word query as an implicit AND and parses `websearch_to_tsquery` operators; the fake does a
   literal single-token comparison. **Prefixes match in neither**, so search-as-you-type does not work today at
   all. Only the numeric shortcut and single-token exact case-insensitive matching are guaranteed identical.
4. **`air_temperature` is an integer** in the frozen wire schema, and `lastTemp` is fed from it. The spec's
   `"lastTemp": 18.3` example is unachievable without a wire-format version bump.
5. **`gosec` G112 does not gate `ReadHeaderTimeout`.** It fires only when a server literal has neither
   `ReadHeaderTimeout` nor `ReadTimeout`, and both servers set `ReadTimeout`.
   `TestReadHeaderTimeoutIsSetOnBothServers` is the only gate on that field.
6. **`serve` cannot boot until `internal/fuelmath` exists.** `config.Validate` fails closed for the `serve` and
   `preflight` subcommands, naming the four rules (8, 10, 12, 13) it cannot evaluate. That is deliberate;
   skipping them with a WARN is what spec rule 8 exists to prevent.

## Two corrections to the plan and the spec

Both were measured, both are now fixed in `docs/superpowers/plans/2026-07-30-read-api.md` and
`docs/superpowers/specs/2026-07-27-weather-proof-go-port-design.md`:

1. **`gosec` G112 does not flag a missing `ReadHeaderTimeout` on its own** (caveat 5 above). Both documents
   previously asserted the opposite, so anything that leaned on the linter for that field was uncovered.
2. **The general scope advertises and enforces `RateLimit-Limit: 720`, not 600**, because `Decision.Limit` is
   `limit + burst`.
