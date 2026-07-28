# Encoder Byte-Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Freeze the existing TypeScript weather-record encoder as a pinned, reproducible parity oracle, and build a Go `internal/weather` package that reproduces its bytes exactly for every committed golden vector and decodes all three on-chain record layouts losslessly.

**Architecture:** The TypeScript encoder moves (never gets deleted) to `parity/ts/`, gets its own `package.json` with `@bsv/sdk` pinned to the exact version `1.10.3` and a committed lockfile, and gains one generator script that writes `internal/weather/testdata/vectors.json` — the frozen byte contract. A new Go module at the repository root then implements `internal/weather` (types, schema, script numbers, floats, encoder, decoder) driven test-first off that JSON file. `make parity` regenerates the vectors into a temp file and diffs, so the contract can never drift silently.

**Tech Stack:** TypeScript 5.3.3 + ts-node + jest 29 (oracle only, frozen); `@bsv/sdk` 1.10.3 (npm, exact pin); Go 1.26.3; `github.com/bsv-blockchain/go-sdk` v1.3.2 (`script` package); golangci-lint v2.12.2; GNU make.

## Global Constraints

- `@bsv/sdk` is pinned to the **exact** version `1.10.3` in `parity/ts/package.json` — **no caret**. The current root `package.json` says `^1.7.0` while `node_modules` holds `1.10.3`, so a fresh install today already resolves a different minor than the range implies.
- The lockfile integrity hash the vectors must record is `sha512-3olJE3s3pttwVFd/OYuTeXmllFwNTYPwu5EdD9NV8am70eJjTBPq4JTq4Q1vZG/pFZm3A4a99WMzdq8f6Wmu7w==`.
- Go version: `go 1.26.3` in `go.mod`, with no separate `toolchain` line (matching `go-wallet-toolbox`). `GOTOOLCHAIN=auto` (the default) auto-downloads 1.26.3 when the local default is older — `go version` run from **inside** the module prints `go1.26.3`, run from `/tmp` it may print something older. That is expected.
- Go module path: `github.com/bsv-blockchain-demos/weather-proof`.
- Go code lives at the repository **ROOT**, alongside `frontend/`. Not in a `backend/` subdirectory. `frontend/` contains no `.go` files so `go build ./...` skips it, and `build.yml`'s `.` build context does not change.
- Go SDK: `github.com/bsv-blockchain/go-sdk v1.3.2`.
- Lint: `.golangci.json` is the `go-wallet-toolbox` config with **four documented edits**: the `gci` section prefix, `goimports.local-prefixes`, the `revive` settings dropped (revive is in the disable list and its separate config file does not exist here), and `run.build-tags: ["mage"]` dropped (this module has no build tags). Task 5a writes the resulting file out literally. It is schema `"version": "2"`, `run.tests: true` (test files are linted too), `gofmt`+`gofumpt` formatters, `exhaustive` with `default-signifies-exhaustive: false`, `govet` with `shadow` enabled, `misspell` with `locale: US` and a fixed `ignore-rules` list, and `nolintlint` with `allow-unused: false`, `require-explanation: true`, `require-specific: true`.
- `nolintlint` with `allow-unused: false` makes an **unnecessary** `//nolint` a lint failure in its own right. Every `//nolint` in this plan was checked against golangci-lint v2.12.2 with this exact config, and only the two `G304` directives in `onchain_test.go` suppress a diagnostic that actually fires. `gosec` v2.12.2 does range analysis and does **not** flag `uint64(-n)`, `uint64(n)`, `byte(mag&0xff)`, `script.Op1 + byte(n) - 1` or `int64(mag)`; `unconvert` ignores float conversions by default and does **not** flag `float64(v * scale)`. Those five reasons are therefore written as ordinary comments, not directives. Do not "helpfully" add a `//nolint` anywhere: it will fail `make go-lint`.
- `misspell` is enabled with `locale: US` and its `ignore-rules` list contains `marshalling` but **not** `marshalled` or `synthesised`. Because `run.tests: true`, test files are linted: use `marshaled` and `synthesized` in test messages.
- **No TypeScript may be deleted in this plan.** Every relocation is `git mv`, as its own commit, so `git log --follow` keeps the encoder's provenance. The TypeScript encoder must still compile and its 111 jest tests must still pass at the end of Phase 1. Deleting `src/format/*` makes the golden vectors unregenerable forever.
- **Vectors are asserted byte-exact, not round-tripped.** A symmetric encoder-and-decoder error round-trips perfectly while writing the wrong bytes on chain. Every record vector is compared as a hex string against the value the TypeScript produced.
- **The 33-field schema order IS the wire format.** The CURRENT order is strict alphabetical, `air_density` first and `wind_gust` last, ported from `parity/ts/src/format/schema.ts` for everything this plan encodes. `ENCODING.md`'s "Field Order" section (time-first, category-grouped) is **not an error**: it is the SUPERSEDED wire order, and it is the only surviving documentation that the superseded order existed. Never port the current order from it; never delete it either.
- Version 1 is emitted as a **single opcode** (`OP_1` = `0x51`), not a data push. At version 17 it becomes a data push (`0111`) and silently changes the record prefix from 3 bytes to 4.
- **There are THREE on-chain layouts, not two, and the version byte does not distinguish them.** Verified from git: `git show -s --format=%ci e2ae463` is `2026-01-27 11:11:45 -0600` and `c44b7ae` is `2026-01-27 11:14:41 -0600`, and `git merge-base --is-ancestor e2ae463 c44b7ae` exits 0 — so the alphabetical reorder landed **three minutes before** the `OP_FALSE OP_RETURN` prefix, and committed code produced three distinct wire formats in sequence:

  | layout | commit range | prefix | field order | chunks |
  |---|---|---|---|---|
  | **A** | `34b5b81`..`e2ae463^` | none | **time-first, category-grouped** (`FieldSchemaV0`) | 34 |
  | **B** | `e2ae463`..`c44b7ae^` | none | strict alphabetical | 34 |
  | **C** | `c44b7ae`..`HEAD` | `006a` | strict alphabetical | 36 |

- Chunk-count basis is **36** for layout C and **34** for layouts A and B. A guard written on 34 accepts a prefixed script truncated by two whole fields.
- **A and B have no format-only discriminator** — both are 34 chunks and both start `OP_1`. A layout-A record read under the alphabetical order decodes to SILENT GARBAGE with no error: schema index 0 is `air_density` (float, `/1e6`) but the chunk holds `time`, so a real record decodes as `air_density = 1769529302/1e6 = 1769.529302`, and index 3 is `conditions` (string) but the chunk holds the integer `dew_point`, so `string(c.Data)` returns raw little-endian bytes. All 33 fields are wrong and every byte-parity test still passes, because the golden vectors are generated from HEAD's schema only. Task 11 therefore discriminates on the **value** of the first field: in layout B index 0 is `air_density × 1e6` (under 1e9 for any physically possible reading — the repository's own adversarial `extreme` fixture sits at 999999999, one below the threshold), in layout A it is `time`, a Unix epoch (over 1.7e9 since 2023). The threshold is `1_000_000_000`.

### Measured reference values (do not re-derive)

The three fixture scripts, produced by executing `dist/format/encoder.js` against `@bsv/sdk@1.10.3` on Node v24.15.0:

| fixture | bytes | chunks | hex |
|---|---|---|---|
| `minimal` | 36 | 36 | `006a51000000000000000000000000000000000000000000000000000000000000000000` |
| `sample` | 99 | 36 | `006a510310af13018903d7090105436c656172520191018d09636c6561722d6461795151000001200a3330202d203334206b6d046d50f86800000000000766616c6c696e67013102fb03023702042009653a04d6df7869520189018b52021801015754` |
| `extreme` | 211 | 36 | `006a5104ffc99a3b01e4033f420f3145787472656d6520636f6e646974696f6e732077697468207370656369616c2063686172733a2021402324255e262a2829013201b201f81665787472656d652d776561746865722de29aa1efb88f510002e703020f2702e7032256657279206661722061776179207769746820756e69636f64653a20e6b58be8af9504ffffff7f02e70302e70302a00502a00501640f72617069646c792066616c6c696e67016402d007020f2704d202964904ffffff7f0114013c013202c800026701034e4e45022c01` |

The layout-B prefix-less form of each is the same hex with the leading `006a` removed; all three round-trip through the TypeScript decoder with zero field differences.

The layout-A form of each is a **pure permutation** of the same 33 field chunks into the pre-`e2ae463` order, with `OP_1` and no prefix. It needs no oracle to derive, because the encoder emits each field independently: reordering the chunks is exactly what the pre-`e2ae463` encoder did. These three hexes were computed by parsing the chunks of the hexes above, mapping them onto the alphabetical schema, and re-emitting them in the `FieldSchemaV0` order transcribed from `git show e2ae463^:src/format/schema.ts`:

| fixture | bytes | chunks | layout-A hex |
|---|---|---|---|
| `minimal_v0` | 34 | 34 | `51000000000000000000000000000000000000000000000000000000000000000000` |
| `sample_v0` | 97 | 34 | `5104d6df78690189018d0191018b018952042009653a02fb030766616c6c696e6701310310af135254021801015700000000005151000001200a3330202d203334206b6d046d50f86803d709010237025205436c65617209636c6561722d646179` |
| `extreme_v0` | 209 | 34 | `5104ffffff7f01e401f801b20132013c013204d202964902d0070f72617069646c792066616c6c696e67016404ffc99a3b02c800022c01026701034e4e45016402e70302e70302a00502a005510002e703020f2702e7032256657279206661722061776179207769746820756e69636f64653a20e6b58be8af9504ffffff7f033f420f020f2701143145787472656d6520636f6e646974696f6e732077697468207370656369616c2063686172733a2021402324255e262a28291665787472656d652d776561746865722de29aa1efb88f` |

Note `minimal_v0` is byte-identical to `minimal`'s layout-B form: every field of the all-zero record encodes as `0x00`, so the two orders coincide, and decoding it under either order yields the same all-zero record. The discriminator therefore classifies it as layout B, harmlessly.

The pre-`e2ae463` field order (`FieldSchemaV0`), transcribed from `git show e2ae463^:src/format/schema.ts`:

```
 0 time                                  integer
 1 air_temperature                       integer
 2 feels_like                            integer
 3 dew_point                             integer
 4 wet_bulb_temperature                  integer
 5 wet_bulb_globe_temperature            integer
 6 delta_t                               integer
 7 station_pressure                        float
 8 sea_level_pressure                    integer
 9 pressure_trend                         string
10 relative_humidity                     integer
11 air_density                             float
12 wind_avg                              integer
13 wind_gust                             integer
14 wind_direction                        integer
15 wind_direction_cardinal                string
16 precip_probability                    integer
17 precip_accum_local_day                integer
18 precip_accum_local_yesterday          integer
19 precip_minutes_local_day              integer
20 precip_minutes_local_yesterday        integer
21 is_precip_local_day_rain_check        boolean
22 is_precip_local_yesterday_rain_check  boolean
23 lightning_strike_count_last_1hr       integer
24 lightning_strike_count_last_3hr       integer
25 lightning_strike_last_distance        integer
26 lightning_strike_last_distance_msg     string
27 lightning_strike_last_epoch           integer
28 brightness                            integer
29 solar_radiation                       integer
30 uv                                    integer
31 conditions                             string
32 icon                                   string
```

It holds exactly the same 33 names as the alphabetical order, with the same type for every name.

Per-field breakdown of the 99-byte `sample`, measured by serializing each field in isolation and confirming the concatenation equals the full script:

```
      header                              006a51
 0 air_density                     float    0310af13
 1 air_temperature               integer    0189
 2 brightness                    integer    03d70901
 3 conditions                     string    05436c656172
 4 delta_t                       integer    52
 5 dew_point                     integer    0191
 6 feels_like                    integer    018d
 7 icon                           string    09636c6561722d646179
 8 is_precip_local_day_rain_check        boolean  51
 9 is_precip_local_yesterday_rain_check  boolean  51
10 lightning_strike_count_last_1hr       integer  00
11 lightning_strike_count_last_3hr       integer  00
12 lightning_strike_last_distance        integer  0120
13 lightning_strike_last_distance_msg     string  0a3330202d203334206b6d
14 lightning_strike_last_epoch           integer  046d50f868
15 precip_accum_local_day                integer  00
16 precip_accum_local_yesterday          integer  00
17 precip_minutes_local_day              integer  00
18 precip_minutes_local_yesterday        integer  00
19 precip_probability                    integer  00
20 pressure_trend                         string  0766616c6c696e67
21 relative_humidity                     integer  0131
22 sea_level_pressure                    integer  02fb03
23 solar_radiation                       integer  023702
24 station_pressure                        float  042009653a
25 time                                  integer  04d6df7869
26 uv                                    integer  52
27 wet_bulb_globe_temperature            integer  0189
28 wet_bulb_temperature                  integer  018b
29 wind_avg                              integer  52
30 wind_direction                        integer  021801
31 wind_direction_cardinal                string  0157
32 wind_gust                             integer  54
```

---

## File Structure

Every file this plan creates or modifies, and its single responsibility.

### Phase 1 — the pinned oracle and the golden vectors

| Path | Action | Single responsibility |
|---|---|---|
| `.gitignore` | Modify | Un-ignore the two lockfiles that pin reproducible builds; add the Go section. |
| `parity/ts/package.json` | Create | The oracle's own dependency set: `@bsv/sdk` at exactly `1.10.3` plus jest/ts-node. Nothing else. |
| `parity/ts/package-lock.json` | Create (generated, committed) | The actual pin. Its `integrity` hash is the provenance the vectors record. |
| `frontend/package-lock.json` | Add to git (already exists on disk) | Spec-mandated un-ignore; keeps the frontend image reproducible. Does not affect the vectors. |
| `parity/ts/src/format/{constants,decoder,encoder,schema,types}.ts` | `git mv` from `src/format/` | The frozen oracle. Content unchanged, forever. |
| `parity/ts/src/utils/float-encoder.ts` | `git mv` from `src/utils/` | The frozen `Math.round(value * scale)` float path. |
| `parity/ts/src/index.ts` | `git mv` from `src/index.ts` | The oracle's barrel; `integration.test.ts` imports it. |
| `parity/ts/tests/*.test.ts` (6 files) | `git mv` from `tests/` | The 111 jest tests that prove the oracle still works. |
| `parity/ts/tests/fixtures/weather-samples.ts` | `git mv` from `tests/fixtures/` | The three record fixtures the vectors are generated from. |
| `parity/ts/tsconfig.json` | Create | Compiles the oracle standalone with `strict: true` and **no `rootDir`**. |
| `parity/ts/jest.config.js` | `git mv` from root, then edit | Runs the oracle's 111 tests from `parity/ts/`. |
| `parity/ts/check-vectors.py` | Create | The committed check on the generated vectors: every measured value asserted, run before and after the generator exists. |
| `parity/ts/gen-vectors.ts` | Create | The ONLY writer of `internal/weather/testdata/vectors.json`. Fully deterministic. |
| `tsconfig.json` (root) | Modify | Drop `rootDir`, include `parity/ts/src` so the running app still compiles against the relocated oracle. |
| `package.json` (root) | Modify | Repoint `main`/`types`/`start`/`test*`/`lint` at the new layout. |
| `Dockerfile` | Modify | `COPY parity ./parity` instead of the vanished `tests`; CMD becomes `dist/src/app.js`. |
| `.dockerignore` | Modify | Ignore `node_modules` at any depth so `parity/ts/node_modules` never enters an image. |
| `src/{db/models/weather-record,service/tempest,service/transaction,scripts/locking-scripts}.ts` | Modify (import paths only) | Point at the relocated oracle. No logic changes. |
| `Makefile` | Modify | Add `parity`, `parity-regen`, `parity-test`; later `go-build`, `go-test`, `go-lint`, `check`. |
| `internal/weather/testdata/vectors.json` | Create (generated, committed) | **The frozen byte contract.** Never hand-edited. |
| `ENCODING.md` | Modify | Correction banner: the field-order section documents the SUPERSEDED order (keep it, do not trust it for new work) and the "97 bytes" figure is the OP_RETURN payload, not the script. |

### Phase 2 — the Go package

| Path | Action | Single responsibility |
|---|---|---|
| `go.mod`, `go.sum` | Create | Module identity, Go version, the one dependency. |
| `.golangci.json` | Create | Toolbox lint config verbatim, with the local prefix changed. |
| `internal/weather/types.go` | Create | `Version`, `FloatScale`, `FloatEpsilon`, `DataFieldsPerRecord`, `ChunksPrefixed`, `ChunksLegacy`, `FieldType`, `FieldDefinition`, `WeatherData`. |
| `internal/weather/types_test.go` | Create | Constants, `FieldType.String()`, and the 33 json tags being complete and alphabetical. |
| `internal/weather/vectors_test.go` | Create | The golden-file loader and the integrity gate every other parity test depends on. |
| `internal/weather/schema.go` | Create | `FieldSchema` (the current wire order), `FieldSchemaV0` (the superseded layout-A order), and the `fieldPtrs`/`fieldPtrsV0` schema-to-struct bridges. |
| `internal/weather/schema_test.go` | Create | Order, count, alphabetical-ness, type tally, parity with the vectors' schema, the `fieldPtrs` type/index correspondence, and that `FieldSchemaV0` is a permutation of `FieldSchema` with identical types. |
| `internal/weather/scriptnum.go` | Create | `MaxSafeInteger`, `ErrNumberOutOfRange`, `scriptNumBytes`, `appendScriptNum` — the `writeBn` reproduction. |
| `internal/weather/scriptnum_test.go` | Create | The 40-entry `writeNumber` table, the opcode branches, sign extension, out-of-range, and `AppendPushData` vs `writeBin`. |
| `internal/weather/float.go` | Create | `jsRound`, `EncodeFloat`, `DecodeFloat`, `ValidateFloatPrecision`, `RequireIntegral`, `ErrNonFinite`, `ErrNonIntegral`. |
| `internal/weather/float_test.go` | Create | The 35-entry float table, the `math.Round` divergences, non-default scales, epsilon, and non-finite rejection. |
| `internal/weather/encoder.go` | Create | `Encode`, `EncodeHex`, `appendField`, `MaxPushDataLen`, `ErrStringTooLong`, `ErrSchemaMismatch`. |
| `internal/weather/encoder_test.go` | Create | The three fixtures byte-exact, the 21 strings, the no-`0x6a` invariant, the size cap, and the typed-error negatives. |
| `internal/weather/decoder.go` | Create | `Decode`, `DecodeHex`, `IsValidScript`, `payloadStart`, `legacyUsesV0Order`, `readField`, `scriptNumFromBytes`, `scriptNumFromChunk`, `ErrUnsupportedVersion`, `ErrMalformedScript`. |
| `internal/weather/decoder_test.go` | Create | All three layouts, round-trips, the A/B discriminator at its boundary, version rejection, truncation rejection, trailing tolerance. |
| `scripts/fetch-onchain-fixture.sh` | Create (Task 12, human-gated) | Pulls a published weather output script into `testdata/onchain/`, either by txid or by walking an address's history for the EARLIEST one. |
| `internal/weather/testdata/onchain/*.hex`, `*.json` | Create (fetched, committed, Task 12) | Real on-chain records, proving the Go decoder parses production data. |
| `internal/weather/onchain_test.go` | Create | Decodes every committed on-chain fixture and re-encodes it byte-identically; skips when the directory is empty unless `WEATHER_ONCHAIN_FIXTURES=1`. |

**Task 11 is the autonomous completion point of this plan.** Tasks 1-11 need no input that is not in this document, and `make check` is green at the end of Task 11. Task 12 needs one input nobody has yet — a real txid, or the operator's publishing address — so it is human-gated and sits deliberately outside the automated sequence. Do not block Tasks 1-11 on it, and do not satisfy it with a synthesized script.

---

## Task 1: Pin `@bsv/sdk` exactly and commit the lockfiles

**Files:**
- Modify: `/Users/personal/git/demos/weather-chain/.gitignore` (lines 1-4 today are `# Dependencies`, `node_modules/`, `package-lock.json`, `yarn.lock`; append a new block at the end of the file)
- Create: `/Users/personal/git/demos/weather-chain/parity/ts/package.json`
- Create: `/Users/personal/git/demos/weather-chain/parity/ts/package-lock.json` (generated by `npm install`)
- Add to git: `/Users/personal/git/demos/weather-chain/frontend/package-lock.json` (already on disk, 146,280 bytes, currently ignored)
- Test: `/Users/personal/git/demos/weather-chain/parity/ts/check-pin.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: `parity/ts/package.json` with `dependencies["@bsv/sdk"] == "1.10.3"`; `parity/ts/package-lock.json` with `packages["node_modules/@bsv/sdk"].integrity == "sha512-3olJE3s3pttwVFd/OYuTeXmllFwNTYPwu5EdD9NV8am70eJjTBPq4JTq4Q1vZG/pFZm3A4a99WMzdq8f6Wmu7w=="`. Task 3's generator reads that `integrity` value. Task 3's `npm scripts` entry `gen` is defined here.

### Steps

- [ ] **Write the failing check.** Create `/Users/personal/git/demos/weather-chain/parity/ts/check-pin.sh` with exactly this content:

```bash
#!/usr/bin/env bash
# Verifies the parity oracle's dependency pin is exact, committed and un-ignored.
# This is a contract check, not a build step: if any assertion here fails, the
# golden vectors in internal/weather/testdata/vectors.json are generated from a
# moving target and are worthless.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

fail=0
check() {
  if [ "$1" = "0" ]; then
    printf 'ok   %s\n' "$2"
  else
    printf 'FAIL %s\n' "$2"
    fail=1
  fi
}

# 1. The lockfile must exist.
[ -f parity/ts/package-lock.json ]
check $? "parity/ts/package-lock.json exists"

# 2. It must NOT be ignored. git check-ignore exits 0 when a path IS ignored,
#    so we invert: exit 1 (not ignored) is what we want.
git check-ignore -q parity/ts/package-lock.json
[ $? -ne 0 ]
check $? "parity/ts/package-lock.json is NOT gitignored"

# 3. It must be tracked by git.
[ -n "$(git ls-files parity/ts/package-lock.json)" ]
check $? "parity/ts/package-lock.json is tracked by git"

# 4. package.json must pin the exact version with no range operator.
declared="$(node -e 'process.stdout.write(require("./parity/ts/package.json").dependencies["@bsv/sdk"])')"
[ "$declared" = "1.10.3" ]
check $? "package.json declares @bsv/sdk == 1.10.3 (got '$declared')"

# 5. The lockfile must resolve exactly 1.10.3.
locked="$(node -e 'const l=require("./parity/ts/package-lock.json");process.stdout.write(l.packages["node_modules/@bsv/sdk"].version)')"
[ "$locked" = "1.10.3" ]
check $? "lockfile resolves @bsv/sdk 1.10.3 (got '$locked')"

# 6. The integrity hash must be the measured one.
want="sha512-3olJE3s3pttwVFd/OYuTeXmllFwNTYPwu5EdD9NV8am70eJjTBPq4JTq4Q1vZG/pFZm3A4a99WMzdq8f6Wmu7w=="
got="$(node -e 'const l=require("./parity/ts/package-lock.json");process.stdout.write(l.packages["node_modules/@bsv/sdk"].integrity)')"
[ "$got" = "$want" ]
check $? "lockfile integrity matches the measured hash"

# 7. The frontend lockfile must also be un-ignored and tracked.
git check-ignore -q frontend/package-lock.json
[ $? -ne 0 ]
check $? "frontend/package-lock.json is NOT gitignored"

[ -n "$(git ls-files frontend/package-lock.json)" ]
check $? "frontend/package-lock.json is tracked by git"

exit "$fail"
```

- [ ] **Make it executable and run it, and see it fail.** Run:

```bash
chmod +x /Users/personal/git/demos/weather-chain/parity/ts/check-pin.sh
/Users/personal/git/demos/weather-chain/parity/ts/check-pin.sh
```

Expected output (the first lines; the `node -e` calls also print a module-not-found error to stderr, which is fine):

```
FAIL parity/ts/package-lock.json exists
FAIL parity/ts/package-lock.json is NOT gitignored
FAIL parity/ts/package-lock.json is tracked by git
```

Exit code 1.

- [ ] **Un-ignore the two lockfiles.** Append this block to the end of `/Users/personal/git/demos/weather-chain/.gitignore`:

```gitignore
# The parity oracle's lockfile is the pin that keeps the golden vectors
# reproducible. `package-lock.json` is ignored globally on line 3, so these two
# negations are what make the pins committable. Do not remove them.
!parity/ts/package-lock.json
!frontend/package-lock.json
```

- [ ] **Create the oracle's package.json.** Create `/Users/personal/git/demos/weather-chain/parity/ts/package.json` with exactly this content. Note `@bsv/sdk` has **no caret**: that is the whole point of this task.

```json
{
  "name": "weatherproof-parity-oracle",
  "version": "0.0.0",
  "private": true,
  "description": "FROZEN TypeScript encoder, retained solely as the byte-parity oracle for internal/weather. Nothing in the running system imports this. Deleting it makes internal/weather/testdata/vectors.json unregenerable.",
  "license": "Open BSV License",
  "scripts": {
    "gen": "ts-node --project tsconfig.json gen-vectors.ts",
    "test": "jest",
    "test:watch": "jest --watch",
    "test:coverage": "jest --coverage",
    "typecheck": "tsc --noEmit --project tsconfig.json"
  },
  "dependencies": {
    "@bsv/sdk": "1.10.3"
  },
  "devDependencies": {
    "@types/jest": "29.5.12",
    "@types/node": "20.11.16",
    "jest": "29.7.0",
    "ts-jest": "29.1.2",
    "ts-node": "10.9.2",
    "typescript": "5.3.3"
  }
}
```

- [ ] **Install and generate the lockfile.** Run:

```bash
cd /Users/personal/git/demos/weather-chain/parity/ts && npm install
```

This needs network access. Expected: npm reports roughly `added 300 packages` and creates both `node_modules/` and `package-lock.json`.

- [ ] **Run the check and see it pass.** Run:

```bash
/Users/personal/git/demos/weather-chain/parity/ts/check-pin.sh
```

Expected output — the first six lines pass, the last two still fail because nothing is committed yet:

```
ok   parity/ts/package-lock.json exists
ok   parity/ts/package-lock.json is NOT gitignored
FAIL parity/ts/package-lock.json is tracked by git
ok   package.json declares @bsv/sdk == 1.10.3 (got '1.10.3')
ok   lockfile resolves @bsv/sdk 1.10.3 (got '1.10.3')
ok   lockfile integrity matches the measured hash
ok   frontend/package-lock.json is NOT gitignored
FAIL frontend/package-lock.json is tracked by git
```

If line 6 fails, **stop**: npm resolved a different tarball than the one the vectors were measured against, and nothing downstream is trustworthy.

- [ ] **Stage everything and re-run the check.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add .gitignore parity/ts/package.json parity/ts/package-lock.json parity/ts/check-pin.sh frontend/package-lock.json && \
  ./parity/ts/check-pin.sh
```

Expected: all eight lines `ok`, exit code 0.

- [ ] **Confirm no stray files were staged.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git status --porcelain
```

Expected: exactly five `A` lines (`.gitignore` shows as `M`), and **no** entry under `parity/ts/node_modules/`. If `node_modules` appears, the `.gitignore` `node_modules/` rule is not matching at depth — add `**/node_modules/` before continuing.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git commit -m "parity: pin @bsv/sdk to exactly 1.10.3 and commit the lockfiles

The golden encoder vectors are only a contract if the dependency that
produced them cannot move. The root package.json declares ^1.7.0 while
node_modules holds 1.10.3, and package-lock.json was gitignored, so a
fresh install already resolved a different minor than the range implied.

parity/ts/package.json pins the exact version with no caret and its
lockfile is now tracked; .gitignore negates the global package-lock.json
rule for it and for frontend/. check-pin.sh asserts all of it."
```

---

## Task 2: Relocate the oracle to `parity/ts/` with both TypeScript builds still green

Task 2 is split into **four independently committable sub-tasks**, 2a to 2d, because a failure in the eleventh step of one giant task leaves nothing to fall back to. `check-oracle.sh` is written once in 2a and its checks come green in groups: 2a satisfies checks 1-2, 2b checks 3-4, 2c check 5. Run the whole script at the end of every sub-task; only the checks named in that sub-task are expected to pass.

**Files (all four sub-tasks):**
- `git mv`: `src/format/constants.ts`, `src/format/decoder.ts`, `src/format/encoder.ts`, `src/format/schema.ts`, `src/format/types.ts` → `parity/ts/src/format/`
- `git mv`: `src/utils/float-encoder.ts` → `parity/ts/src/utils/float-encoder.ts`
- `git mv`: `src/index.ts` → `parity/ts/src/index.ts`
- `git mv`: `tests/decoder.test.ts`, `tests/edge-cases.test.ts`, `tests/encoder.test.ts`, `tests/float-encoder.test.ts`, `tests/integration.test.ts`, `tests/roundtrip.test.ts` → `parity/ts/tests/`
- `git mv`: `tests/fixtures/weather-samples.ts` → `parity/ts/tests/fixtures/weather-samples.ts`
- `git mv`: `jest.config.js` → `parity/ts/jest.config.js`, then rewrite its contents
- Create: `/Users/personal/git/demos/weather-chain/parity/ts/tsconfig.json`
- Modify: `/Users/personal/git/demos/weather-chain/tsconfig.json` (whole file rewritten; it is 17 lines today)
- Modify: `/Users/personal/git/demos/weather-chain/package.json` (the `main`, `types` and `scripts` keys)
- Modify: `/Users/personal/git/demos/weather-chain/Dockerfile` (line 18 `COPY tests ./tests`, line 57 `CMD ["node", "dist/app.js"]`)
- Modify: `/Users/personal/git/demos/weather-chain/.dockerignore` (line 2 `node_modules/`)
- Modify: `/Users/personal/git/demos/weather-chain/src/db/models/weather-record.ts` (line 2)
- Modify: `/Users/personal/git/demos/weather-chain/src/service/tempest.ts` (line 2)
- Modify: `/Users/personal/git/demos/weather-chain/src/service/transaction.ts` (line 2)
- Modify: `/Users/personal/git/demos/weather-chain/src/scripts/locking-scripts.ts` (lines 2, 3 and 25)

**Interfaces:**
- Consumes: `parity/ts/package.json` and `parity/ts/package-lock.json` from Task 1, and `parity/ts/node_modules/` installed there.
- Produces: the oracle importable as `./src/format/encoder`, `./src/format/decoder`, `./src/format/schema`, `./src/format/constants`, `./src/utils/float-encoder` and `./tests/fixtures/weather-samples` **relative to `parity/ts/`** — Task 3's `gen-vectors.ts` imports exactly those paths. Also produces `parity/ts/tsconfig.json`, which Task 3's `ts-node --project tsconfig.json` uses.

**Why this task is delicate:** a bare `git mv` breaks two things that a reviewer would rightly reject. Both were measured, not guessed.

1. Five files in the running `src/` tree import the oracle. After the move, `npx tsc --noEmit` reports exactly five `TS2307: Cannot find module` errors. Repointing them at `../../parity/ts/src/format/types` instead reports `TS6059: File ... is not under 'rootDir'` — because the root `tsconfig.json` sets `"rootDir": "./src"`. **Dropping `rootDir` and adding `parity/ts/src` to `include` compiles cleanly (exit 0)**, at the cost of the emit layout moving from `dist/app.js` to `dist/src/app.js`.
2. `Dockerfile` line 18 is `COPY tests ./tests`. After the move there is no `tests/` directory and the Docker build fails outright. `.github/workflows/build.yml` builds that image on every pull request to `master`.

The intermediate commits of 2a and 2b leave the ROOT TypeScript build red (`TS2307`, the consumers still point at `src/format/`). That is deliberate: four small commits whose last one is green beats one commit that cannot be bisected. 2c is the commit that makes `npx tsc --noEmit` exit 0 again, and nothing between 2a and 2c is pushed as a working state.

### Task 2a steps — move the sources

- [ ] **Write the failing check.** Create `/Users/personal/git/demos/weather-chain/parity/ts/check-oracle.sh` with exactly this content:

```bash
#!/usr/bin/env bash
# Verifies the relocated oracle compiles standalone, its 111 tests pass, and the
# running TypeScript app still compiles against the new location.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

fail=0
check() {
  if [ "$1" = "0" ]; then
    printf 'ok   %s\n' "$2"
  else
    printf 'FAIL %s\n' "$2"
    fail=1
  fi
}

# 1. The oracle sources must live under parity/ts and nowhere else.
for f in src/format/constants.ts src/format/decoder.ts src/format/encoder.ts \
         src/format/schema.ts src/format/types.ts src/utils/float-encoder.ts src/index.ts; do
  [ ! -e "$f" ]
  check $? "$f no longer exists at the old location"
done

for f in parity/ts/src/format/constants.ts parity/ts/src/format/decoder.ts \
         parity/ts/src/format/encoder.ts parity/ts/src/format/schema.ts \
         parity/ts/src/format/types.ts parity/ts/src/utils/float-encoder.ts \
         parity/ts/src/index.ts parity/ts/tests/fixtures/weather-samples.ts \
         parity/ts/tsconfig.json parity/ts/jest.config.js; do
  [ -f "$f" ]
  check $? "$f exists"
done

# 2. git must record the moves as renames, so `git log --follow` keeps provenance.
renames="$(git diff --cached -M --name-status | grep -c '^R')"
[ "$renames" -ge 13 ]
check $? "git staged at least 13 renames (got $renames)"

# 3. The oracle compiles standalone with strict mode.
( cd parity/ts && npx tsc --noEmit --project tsconfig.json )
check $? "parity/ts compiles standalone (tsc --noEmit)"

# 4. All 111 oracle tests pass.
( cd parity/ts && npx jest --silent >/tmp/parity-jest.log 2>&1 )
check $? "parity/ts jest suite passes"
grep -q "Tests:       111 passed, 111 total" /tmp/parity-jest.log
check $? "jest reports exactly 111 passing tests"

# 5. The running TypeScript app still compiles.
npx tsc --noEmit
check $? "root tsc --noEmit exits 0"

exit "$fail"
```

- [ ] **Make it executable and run it, and see it fail.** Run:

```bash
chmod +x /Users/personal/git/demos/weather-chain/parity/ts/check-oracle.sh
/Users/personal/git/demos/weather-chain/parity/ts/check-oracle.sh
```

Expected: the first seven lines all read `FAIL src/format/constants.ts no longer exists at the old location` and so on, because nothing has moved yet. Exit code 1.

- [ ] **Move the oracle sources.** Run, from the repository root:

```bash
cd /Users/personal/git/demos/weather-chain && \
  mkdir -p parity/ts/src/format parity/ts/src/utils parity/ts/tests/fixtures && \
  git mv src/format/constants.ts parity/ts/src/format/constants.ts && \
  git mv src/format/decoder.ts   parity/ts/src/format/decoder.ts && \
  git mv src/format/encoder.ts   parity/ts/src/format/encoder.ts && \
  git mv src/format/schema.ts    parity/ts/src/format/schema.ts && \
  git mv src/format/types.ts     parity/ts/src/format/types.ts && \
  git mv src/utils/float-encoder.ts parity/ts/src/utils/float-encoder.ts && \
  git mv src/index.ts            parity/ts/src/index.ts && \
  git mv tests/decoder.test.ts        parity/ts/tests/decoder.test.ts && \
  git mv tests/edge-cases.test.ts     parity/ts/tests/edge-cases.test.ts && \
  git mv tests/encoder.test.ts        parity/ts/tests/encoder.test.ts && \
  git mv tests/float-encoder.test.ts  parity/ts/tests/float-encoder.test.ts && \
  git mv tests/integration.test.ts    parity/ts/tests/integration.test.ts && \
  git mv tests/roundtrip.test.ts      parity/ts/tests/roundtrip.test.ts && \
  git mv tests/fixtures/weather-samples.ts parity/ts/tests/fixtures/weather-samples.ts && \
  git mv jest.config.js parity/ts/jest.config.js && \
  rmdir src/format src/utils tests/fixtures tests
```

No file contents change in this step. `tests/fixtures/weather-samples.ts` imports `'../../src/format/types'`, which after the move resolves to `parity/ts/src/format/types` unchanged — no edit needed.

- [ ] **Re-run the check and see checks 1-2 pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && ./parity/ts/check-oracle.sh; echo "exit $?"
```

Expected: all seven `no longer exists at the old location` lines are `ok`; the existence loop is `ok` for all nine moved files and `FAIL parity/ts/tsconfig.json exists`, because that file is created in 2b; `ok   git staged at least 13 renames (got 15)`. Checks 3, 4 and 5 all report `FAIL` — the exact stderr they print does not matter yet, since 2b provides the tsconfig they need and 2c repoints the consumers. Exit 1.

- [ ] **Confirm the moves are recorded as renames, not delete-plus-add.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git diff --cached -M --name-status | grep -c '^R'
```

Expected: `15`. If it prints a smaller number, a file's content changed during the move and `git log --follow` will lose the encoder's provenance — `git checkout` the affected file back and redo the `git mv` without editing.

- [ ] **Commit the move on its own.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git commit -m "parity: git mv the TypeScript encoder oracle to parity/ts/

The encoder is the highest-risk part of the Go port: a subtle mistake
writes wrong bytes to mainnet forever and no round-trip test catches it.
The TypeScript that produces those bytes therefore survives the port as a
frozen oracle, moved rather than copied so git log --follow keeps its
provenance.

This commit is the move and nothing else: 15 renames, zero content
changes. The root build is deliberately red until 2c repoints the four
surviving src/ consumers."
```

### Task 2b steps — the oracle's own build config

- [ ] **Create the oracle's tsconfig.** Create `/Users/personal/git/demos/weather-chain/parity/ts/tsconfig.json` with exactly this content. `strict: true` is mandatory: with `strict: false` the oracle does not compile at all — `decoder.ts` lines 65, 70, 74 and 78 fail with `TS2322: Type 'any' is not assignable to type 'never'` on the `Partial<WeatherData>` indexed assignments. There is deliberately **no `rootDir`**, because the tests import fixtures that a `rootDir` of `./src` would exclude.

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "module": "commonjs",
    "lib": ["ES2020"],
    "outDir": "./dist",
    "declaration": true,
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "resolveJsonModule": true,
    "moduleResolution": "node",
    "types": ["node", "jest"]
  },
  "include": ["src/**/*", "tests/**/*", "gen-vectors.ts"]
}
```

- [ ] **Rewrite the moved jest config.** Replace the entire contents of `/Users/personal/git/demos/weather-chain/parity/ts/jest.config.js` with exactly this. The `rootDir: __dirname` is what lets it be invoked either from `parity/ts/` or from the repository root with `--config`.

```javascript
// Jest config for the FROZEN parity oracle. Moved here from the repository root
// by the encoder byte-parity plan; the coverage thresholds and the src/ globs of
// the original are gone because src/ is no longer this project's source root.
module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  rootDir: __dirname,
  roots: ['<rootDir>/tests'],
  testMatch: ['**/*.test.ts'],
  globals: {
    'ts-jest': { tsconfig: '<rootDir>/tsconfig.json' },
  },
  collectCoverageFrom: ['<rootDir>/src/**/*.ts', '!<rootDir>/src/**/*.d.ts'],
};
```

- [ ] **Run the check and see checks 3-4 pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git add -A && ./parity/ts/check-oracle.sh; echo "exit $?"
```

Expected: everything `ok` except the last line, `FAIL root tsc --noEmit exits 0` — 2c fixes that. In particular these three must be `ok` now:

```
ok   parity/ts/tsconfig.json exists
ok   parity/ts compiles standalone (tsc --noEmit)
ok   parity/ts jest suite passes
ok   jest reports exactly 111 passing tests
```

Exit 1. If `parity/ts compiles standalone` fails with `TS2322: Type 'any' is not assignable to type 'never'` on `decoder.ts` lines 65, 70, 74 and 78, `strict` is not `true` in the tsconfig just written.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add parity/ts/tsconfig.json parity/ts/jest.config.js && \
  git commit -m "parity: build config for the relocated oracle

parity/ts/tsconfig.json compiles the oracle standalone. strict: true is
mandatory, not stylistic: with strict: false decoder.ts does not compile at
all, because the Partial<WeatherData> indexed assignments become
'any is not assignable to never'. There is deliberately no rootDir, because
the tests import fixtures a rootDir of ./src would exclude.

The moved jest.config.js gets rootDir: __dirname so it runs from either
parity/ts/ or the repository root. tsc --noEmit exits 0 and jest reports
111 passed."
```

### Task 2c steps — repoint the running app

- [ ] **Rewrite the root tsconfig.** Replace the entire contents of `/Users/personal/git/demos/weather-chain/tsconfig.json` with exactly this. The `rootDir` removal is the measured fix for `TS6059`; `parity/ts/tests` is excluded so the app build does not pull in jest types.

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "module": "commonjs",
    "lib": ["ES2020"],
    "declaration": true,
    "outDir": "./dist",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "resolveJsonModule": true,
    "moduleResolution": "node"
  },
  "include": ["src/**/*", "parity/ts/src/**/*"],
  "exclude": ["node_modules", "dist", "parity/ts/dist", "parity/ts/tests"]
}
```

- [ ] **Repoint the four consumer imports.** Make exactly these four edits.

In `/Users/personal/git/demos/weather-chain/src/db/models/weather-record.ts`, line 2, replace:

```typescript
import { WeatherData } from '../../format/types';
```

with:

```typescript
import { WeatherData } from '../../../parity/ts/src/format/types';
```

In `/Users/personal/git/demos/weather-chain/src/service/tempest.ts`, line 2, replace:

```typescript
import { WeatherData } from '../format/types';
```

with:

```typescript
import { WeatherData } from '../../parity/ts/src/format/types';
```

In `/Users/personal/git/demos/weather-chain/src/service/transaction.ts`, line 2, replace:

```typescript
import { WeatherDataEncoder } from '../format/encoder';
```

with:

```typescript
import { WeatherDataEncoder } from '../../parity/ts/src/format/encoder';
```

In `/Users/personal/git/demos/weather-chain/src/scripts/locking-scripts.ts`, replace lines 2 and 3:

```typescript
import { WeatherData } from '../format/types';
import { WeatherDataEncoder } from '../format/encoder';
```

with:

```typescript
import { WeatherData } from '../../parity/ts/src/format/types';
import { WeatherDataEncoder } from '../../parity/ts/src/format/encoder';
```

and line 25:

```typescript
  const { WeatherDataDecoder } = require('../format/decoder');
```

with:

```typescript
  const { WeatherDataDecoder } = require('../../parity/ts/src/format/decoder');
```

- [ ] **Stage everything and run the check, and see it pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git add -A && ./parity/ts/check-oracle.sh
```

Expected output, all lines `ok`, exit code 0. The two slowest checks print nothing until they finish; jest takes about 4 seconds:

```
ok   src/format/constants.ts no longer exists at the old location
ok   src/format/decoder.ts no longer exists at the old location
ok   src/format/encoder.ts no longer exists at the old location
ok   src/format/schema.ts no longer exists at the old location
ok   src/format/types.ts no longer exists at the old location
ok   src/utils/float-encoder.ts no longer exists at the old location
ok   src/index.ts no longer exists at the old location
ok   parity/ts/src/format/constants.ts exists
ok   parity/ts/src/format/decoder.ts exists
ok   parity/ts/src/format/encoder.ts exists
ok   parity/ts/src/format/schema.ts exists
ok   parity/ts/src/format/types.ts exists
ok   parity/ts/src/utils/float-encoder.ts exists
ok   parity/ts/src/index.ts exists
ok   parity/ts/tests/fixtures/weather-samples.ts exists
ok   parity/ts/tsconfig.json exists
ok   parity/ts/jest.config.js exists
ok   git staged at least 13 renames (got 15)
ok   parity/ts compiles standalone (tsc --noEmit)
ok   parity/ts jest suite passes
ok   jest reports exactly 111 passing tests
ok   root tsc --noEmit exits 0
```

If `root tsc --noEmit exits 0` fails with `TS6059`, the root `tsconfig.json` still has `"rootDir": "./src"` — remove it. If it fails with `TS2307`, one of the four consumer imports was missed.

- [ ] **Verify the emitted layout matches the CMD that 2d will set.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && rm -rf dist && npx tsc && ls dist && test -f dist/src/app.js && echo "CMD path OK"
```

Expected: `dist` contains `parity` and `src`, and the last line prints `CMD path OK`. Dropping `rootDir` is what moved the entrypoint from `dist/app.js` to `dist/src/app.js`; 2d updates the Dockerfile `CMD` to match.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add tsconfig.json src/db/models/weather-record.ts src/service/tempest.ts \
    src/service/transaction.ts src/scripts/locking-scripts.ts && \
  git commit -m "parity: point the running app at the relocated oracle

Five imports in the running src/ tree referenced the encoder. Repointing
them alone is not enough: importing across the root tsconfig's
rootDir: ./src is TS6059, not TS2307, so rootDir is dropped and
parity/ts/src is added to include. Measured: npx tsc --noEmit exits 0.

The cost is that the emit layout moves from dist/app.js to dist/src/app.js,
which 2d reflects in the Dockerfile CMD.

check-oracle.sh is now fully green: 111 jest tests, both typechecks."
```

### Task 2d steps — packaging

- [ ] **Update the root package.json.** In `/Users/personal/git/demos/weather-chain/package.json`, replace the `main`, `types` and `scripts` block:

```json
  "main": "dist/index.js",
  "types": "dist/index.d.ts",
  "scripts": {
    "build": "tsc",
    "start": "tsc && node dist/app.js",
    "dev": "ts-node src/app.ts",
    "setup": "ts-node src/scripts/setup-funding.ts",
    "test": "jest",
    "test:watch": "jest --watch",
    "test:coverage": "jest --coverage",
    "lint": "eslint src tests --ext .ts",
    "format": "prettier --write \"src/**/*.ts\" \"tests/**/*.ts\""
  },
```

with:

```json
  "main": "dist/parity/ts/src/index.js",
  "types": "dist/parity/ts/src/index.d.ts",
  "scripts": {
    "build": "tsc",
    "start": "tsc && node dist/src/app.js",
    "dev": "ts-node src/app.ts",
    "setup": "ts-node src/scripts/setup-funding.ts",
    "test": "npm --prefix parity/ts test",
    "test:watch": "npm --prefix parity/ts run test:watch",
    "test:coverage": "npm --prefix parity/ts run test:coverage",
    "lint": "eslint src --ext .ts",
    "format": "prettier --write \"src/**/*.ts\""
  },
```

Two notes for whoever reviews this. `npm run lint` was already non-functional before this change — the repository has no eslint configuration file at all — so repointing it is tidying, not a fix. And `make test` (`docker-compose run --rm app npm test`) was also already broken, because the production image stage installs with `--omit=dev` and therefore has no jest; this change does not make it worse.

- [ ] **Update the Dockerfile.** In `/Users/personal/git/demos/weather-chain/Dockerfile`, replace line 18:

```dockerfile
COPY tests ./tests
```

with:

```dockerfile
# The frozen parity oracle. tsconfig.json includes parity/ts/src, so the build
# needs it present. tests/ moved into it and no longer exists at the root.
COPY parity ./parity
```

and replace the last line:

```dockerfile
CMD ["node", "dist/app.js"]
```

with:

```dockerfile
CMD ["node", "dist/src/app.js"]
```

- [ ] **Update .dockerignore.** In `/Users/personal/git/demos/weather-chain/.dockerignore`, replace line 2:

```
node_modules/
```

with:

```
node_modules/
**/node_modules/
```

Docker's ignore patterns are not recursive by default, so without the second line `parity/ts/node_modules/` would be copied into the build context.

- [ ] **Verify the Docker build context and re-run the full check.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  grep -n 'COPY parity' Dockerfile && \
  grep -n 'dist/src/app.js' Dockerfile && \
  grep -n '\*\*/node_modules/' .dockerignore && \
  grep -n '"main"\|"start"\|"test"' package.json && \
  ./parity/ts/check-oracle.sh
```

Expected: one match for each `grep`, then all 22 `ok` lines from `check-oracle.sh` and exit 0. Nothing in 2d can break the typechecks; the greps are the assertion that all four packaging edits actually landed.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add package.json Dockerfile .dockerignore && \
  git commit -m "parity: repackage for the parity/ts layout

Dockerfile line 18 was COPY tests ./tests and build.yml builds that image
on every pull request to master, so after the move the Docker build failed
outright. It now copies parity/, which tsconfig.json includes, and CMD
follows the emit layout to dist/src/app.js.

.dockerignore gains **/node_modules/ because Docker ignore patterns are not
recursive: without it parity/ts/node_modules/ enters the build context.

package.json main/types/test* point at the new layout. Two notes for the
reviewer: npm run lint was already non-functional (there is no eslint config
in this repository at all) and make test was already broken (the production
image installs with --omit=dev so it has no jest). Neither is made worse."
```

---

## Task 3: Generate and commit `internal/weather/testdata/vectors.json`

**Files:**
- Test: `/Users/personal/git/demos/weather-chain/parity/ts/check-vectors.py`
- Create: `/Users/personal/git/demos/weather-chain/parity/ts/gen-vectors.ts`
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/testdata/vectors.json` (generated by the above, then committed)

**Interfaces:**
- Consumes: from Task 2, the oracle at `parity/ts/src/format/{constants,decoder,encoder,schema}.ts`, `parity/ts/src/utils/float-encoder.ts` and `parity/ts/tests/fixtures/weather-samples.ts`, plus `parity/ts/tsconfig.json`. From Task 1, `parity/ts/package-lock.json` (read for the integrity hash).
- Produces: `internal/weather/testdata/vectors.json` with exactly these top-level keys in this order: `_comment`, `sdkVersion`, `sdkIntegrity`, `oracleDigest`, `version`, `floatScale`, `fieldCount`, `schema`, `records`, `legacy`, `writeNumber`, `writeBin`, `floats`, `strings`. Task 6's Go `testVectors` struct mirrors that shape exactly, with `json.DisallowUnknownFields`, so adding a key here without updating that struct fails the Go tests.

**Determinism is the whole point.** Two things that look like provenance would make `make parity` permanently red, and both were measured:

- A wall-clock `generatedAt`. Two runs one second apart differed: `< "generatedAt": "2026-07-28T21:29:39.429Z"` versus `> "generatedAt": "2026-07-28T21:30:07.783Z"`.
- `git rev-parse HEAD`. It is stable within a single run but changes on the very next commit, so `make parity` would fail on every commit after the one that generated the file. It is also unstable between a full clone and a shallow CI clone.

The replacement is a sha256 digest over the oracle's own source files, which is deterministic, git-independent, and changes exactly when the bytes-producing code changes. Its measured value for the current oracle is `ffe3653dc800ec0190060635cc1ae2186f79257624ad81f5a2ca1a3ace07311a`, confirmed by hashing the seven files in the working tree.

### Steps

- [ ] **Write the failing check.** The generator produces the single most load-bearing artifact in this plan, so the assertions on it are written FIRST and COMMITTED, exactly like `check-pin.sh` in Task 1 and `check-oracle.sh` in Task 2. A throwaway heredoc run only after the generator already works never has its failure mode observed and guards nothing afterwards. Create `/Users/personal/git/demos/weather-chain/parity/ts/check-vectors.py` with exactly this content:

```python
#!/usr/bin/env python3
"""Asserts every measured value in internal/weather/testdata/vectors.json.

Run from the repository root: python3 parity/ts/check-vectors.py

This is the contract check on the golden byte contract. `make parity` proves the
vectors are REPRODUCIBLE; this proves they are the values that were actually
measured against @bsv/sdk 1.10.3 on Node v24.15.0. A reproducible file full of
wrong bytes is still wrong.
"""
import json

d = json.load(open('internal/weather/testdata/vectors.json'))
assert d['sdkVersion'] == '1.10.3', d['sdkVersion']
assert d['sdkIntegrity'].startswith('sha512-3olJE3s3pttwVFd/O'), d['sdkIntegrity']
assert d['oracleDigest'] == 'ffe3653dc800ec0190060635cc1ae2186f79257624ad81f5a2ca1a3ace07311a'
assert d['version'] == 1 and d['floatScale'] == 1000000 and d['fieldCount'] == 33
assert len(d['schema']) == 33
assert d['schema'][0] == {'name': 'air_density', 'type': 'float'}
assert d['schema'][32] == {'name': 'wind_gust', 'type': 'integer'}
by = {r['name']: r for r in d['records']}
assert by['minimal']['bytes'] == 36 and by['minimal']['chunks'] == 36
assert by['sample']['bytes'] == 99 and by['sample']['chunks'] == 36
assert by['extreme']['bytes'] == 211 and by['extreme']['chunks'] == 36
assert all(r['roundTrips'] for r in d['records'])
assert by['minimal']['hex'] == '006a51' + '00' * 33
assert by['sample']['hex'].startswith('006a510310af13018903d709')
assert all(l['chunks'] == 34 for l in d['legacy'])
assert all(l['hex'] == by[l['name'].replace('_legacy_prefixless', '')]['hex'][4:] for l in d['legacy'])
wn = {e['value']: e for e in d['writeNumber']}
assert len(wn) == 40
assert wn['0']['hex'] == '00' and wn['1']['hex'] == '51' and wn['16']['hex'] == '60'
assert wn['17']['hex'] == '0111' and wn['-1']['hex'] == '4f' and wn['-2']['hex'] == '0182'
assert wn['128']['hex'] == '028000' and wn['-128']['hex'] == '028080'
assert wn['2147483647']['hex'] == '04ffffff7f'
assert wn['9007199254740991']['hex'] == '07ffffffffffff1f'
assert wn['9007199254740992']['error'] == 'The number is larger than 2 ^ 53 (unsafe)'
assert wn['-9007199254740992']['error'] == 'The number is larger than 2 ^ 53 (unsafe)'
wb = {e['len']: e['hex'] for e in d['writeBin']}
assert wb[0] == '00' and wb[1] == '0141'
assert wb[75].startswith('4b') and wb[76].startswith('4c4c')
assert wb[255].startswith('4cff') and wb[256].startswith('4d0001') and wb[500].startswith('4df401')
fl = {str(e['input']): e for e in d['floats']}
assert len(d['floats']) == 35
assert fl['1.29']['scaled'] == '1290000' and fl['1.29']['hex'] == '0310af13'
assert fl['-1.2345675']['scaled'] == '-1234567' and fl['-1.2345675']['hex'] == '0387d692'
assert fl['-5e-07']['scaled'] == '0' and fl['-5e-07']['hex'] == '00'
assert fl['-1.5e-06']['scaled'] == '-1' and fl['-1.5e-06']['hex'] == '4f'
assert fl['-2.5e-06']['scaled'] == '-2' and fl['-2.5e-06']['hex'] == '0182'
st = {e['str']: e for e in d['strings']}
assert len(d['strings']) == 21
assert st['']['hex'] == '00' and st['W']['hex'] == '0157'
assert st['Clear']['hex'] == '05436c656172'
assert st['extreme-weather-⚡️']['utf8Len'] == 22
assert st['测试' * 50]['utf8Len'] == 300
print('all measured values confirmed')
```

- [ ] **Run it and see it fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && python3 parity/ts/check-vectors.py; echo "exit $?"
```

Expected — the golden file does not exist yet, so the very first statement raises:

```
FileNotFoundError: [Errno 2] No such file or directory: 'internal/weather/testdata/vectors.json'
exit 1
```

- [ ] **Commit the check before the generator exists.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add parity/ts/check-vectors.py && \
  git commit -m "parity: assert the measured values of the golden vectors

Written and committed before gen-vectors.ts, so its failure mode is
observed rather than assumed: with no vectors.json it raises
FileNotFoundError and exits 1.

make parity proves the vectors are reproducible. This proves they are the
values actually measured against @bsv/sdk 1.10.3 on Node v24.15.0 — a
reproducible file full of wrong bytes is still wrong."
```

- [ ] **Write the generator.** Create `/Users/personal/git/demos/weather-chain/parity/ts/gen-vectors.ts` with exactly this content:

```typescript
/**
 * Golden-vector generator: the ONLY writer of internal/weather/testdata/vectors.json.
 *
 * Run it with `make parity-regen`. `make parity` runs it into a temporary file and
 * diffs against the committed vectors, failing the build on any difference.
 *
 * MUST BE DETERMINISTIC. Two runs of this script, one second apart, must produce
 * byte-identical output. That rules out:
 *   - a wall-clock `generatedAt` field, and
 *   - `git rev-parse HEAD`, which changes on the very next commit and would make
 *     `make parity` permanently red.
 * Provenance is instead the @bsv/sdk version, the lockfile integrity hash (a
 * stronger pin than the version string) and a sha256 digest of the oracle sources
 * themselves. The generation date lives in `git log`.
 */
import { createHash } from 'crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'fs';
import * as path from 'path';

import { Script } from '@bsv/sdk';

import { FLOAT_SCALE, VERSION } from './src/format/constants';
import { WeatherDataDecoder } from './src/format/decoder';
import { WeatherDataEncoder } from './src/format/encoder';
import { FIELD_SCHEMA } from './src/format/schema';
import { encodeFloat } from './src/utils/float-encoder';
import {
  extremeWeatherData,
  minimalWeatherData,
  sampleWeatherData,
} from './tests/fixtures/weather-samples';

const REPO = path.resolve(__dirname, '../..');
const OUT = path.join(REPO, 'internal/weather/testdata/vectors.json');

// `require('@bsv/sdk/package.json')` throws ERR_PACKAGE_PATH_NOT_EXPORTED: the
// SDK's `exports` map does not expose ./package.json. Resolve the installed
// entrypoint and walk up to the manifest instead.
const sdkPkgPath = path.join(path.dirname(require.resolve('@bsv/sdk')), '../../package.json');
const sdkVersion = JSON.parse(readFileSync(sdkPkgPath, 'utf8')).version as string;

// The lockfile integrity hash pins the exact tarball, not just the version.
const lock = JSON.parse(readFileSync(path.join(__dirname, 'package-lock.json'), 'utf8'));
const sdkIntegrity: string = lock.packages?.['node_modules/@bsv/sdk']?.integrity ?? 'MISSING';

// A content digest of the oracle itself. Deterministic, git-independent, and it
// changes exactly when the bytes-producing code changes.
const ORACLE_FILES = [
  'src/format/constants.ts',
  'src/format/decoder.ts',
  'src/format/encoder.ts',
  'src/format/schema.ts',
  'src/format/types.ts',
  'src/utils/float-encoder.ts',
  'tests/fixtures/weather-samples.ts',
];
const digest = createHash('sha256');
for (const rel of ORACLE_FILES) {
  digest.update(rel);
  digest.update('\0');
  digest.update(readFileSync(path.join(__dirname, rel)));
}
const oracleDigest = digest.digest('hex');

const enc = new WeatherDataEncoder();
const dec = new WeatherDataDecoder();

/** Captures either the hex or the EXACT thrown message, so Go can assert both. */
function attempt(fn: () => string): { hex?: string; error?: string } {
  try {
    return { hex: fn() };
  } catch (e) {
    return { error: (e as Error).message };
  }
}

const records = [
  { name: 'minimal', data: minimalWeatherData },
  { name: 'sample', data: sampleWeatherData },
  { name: 'extreme', data: extremeWeatherData },
].map(({ name, data }) => {
  const script = enc.encode(data);
  const hex = script.toHex();
  return {
    name,
    data,
    hex,
    bytes: script.toBinary().length,
    chunks: script.chunks.length,
    roundTrips: JSON.stringify(dec.decode(Script.fromHex(hex))) === JSON.stringify(data),
  };
});

// Records written before commit c44b7ae have no `006a` prefix and parse to 34
// chunks. That layout is on chain and the Go decoder must accept it.
//
// NOTE: this is layout B only — prefix-less, but in the CURRENT alphabetical
// field order. Layout A (prefix-less AND in the pre-e2ae463 time-first order,
// which shipped three minutes earlier) cannot be generated from this oracle,
// because HEAD's FIELD_SCHEMA is alphabetical. Its vectors are permutations of
// these chunks, pinned as v0Hexes in internal/weather/decoder_test.go.
const legacy = records.map((r) => ({
  name: `${r.name}_legacy_prefixless`,
  hex: r.hex.slice(4),
  chunks: Script.fromHex(r.hex.slice(4)).chunks.length,
  data: r.data,
}));

const writeNumber = [
  0, 1, 2, 15, 16, 17, -1, -2, -16, -17, 75, 76, 127, 128, -127, -128,
  255, 256, -255, -256, 32767, 32768, -32768, 65535, 65536,
  8388607, 8388608, -8388608, 16777215, 16777216,
  2147483647, 2147483648, -2147483647, -2147483648,
  4294967295, 4294967296, 9007199254740991, -9007199254740991,
  9007199254740992, -9007199254740992, // the last two MUST throw
].map((value) => ({
  value: String(value),
  ...attempt(() => {
    const s = new Script();
    s.writeNumber(value);
    return s.toHex();
  }),
}));

const writeBin = [0, 1, 2, 74, 75, 76, 77, 254, 255, 256, 257, 500].map((len) => {
  const s = new Script();
  s.writeBin(new Array<number>(len).fill(0x41));
  return { len, hex: s.toHex() };
});

// The float path. `scaled` is the ECMAScript Math.round result; Go's math.Round
// DISAGREES on every exactly representable negative half.
const floats = [
  0, 1.29, 979.7, 0.5, -1.29, -979.7, 0.000001, 0.0000005, -0.0000005,
  1.5e-6, -1.5e-6, 2.5e-6, -2.5e-6, 1.005, 2.675, 1.2345674, 1.2345675,
  -1.2345675, 12345.678901, 999.999999, 1234.56789, 99999.999999,
  -123.456789, -987.654321, 1.123456789, 0.1, 0.2, 0.3,
  2.5e-7, -2.5e-7, 1e-7, -1e-7, 4.35, -4.35, 9.007199254740991,
].map((input) => {
  const scaled = encodeFloat(input, FLOAT_SCALE);
  return {
    input,
    scaled: String(scaled),
    ...attempt(() => {
      const s = new Script();
      s.writeNumber(scaled);
      return s.toHex();
    }),
  };
});

const strings = [
  '', 'W', 'Clear', 'clear-day', 'falling', '30 - 34 km', 'rapidly falling',
  'NNE', '测试', 'Test 测试 ⚡️', '⚡️☀️🌧️❄️', 'Thunderstorm ⛈️',
  'Clear 晴天 晴れ Солнечно', 'extreme-weather-⚡️',
  '!@#$%^&*()_+-=[]{}|;:\'",.<>?/\\`~',
  'A'.repeat(75), 'A'.repeat(76), 'B'.repeat(255), 'B'.repeat(256),
  'X'.repeat(500), '测试'.repeat(50),
].map((str) => {
  const bytes = Buffer.from(str, 'utf8');
  const s = new Script();
  s.writeBin(Array.from(bytes));
  return { str, utf8Len: bytes.length, hex: s.toHex() };
});

mkdirSync(path.dirname(OUT), { recursive: true });
writeFileSync(
  OUT,
  JSON.stringify(
    {
      _comment:
        'GENERATED by parity/ts/gen-vectors.ts via `make parity-regen`. DO NOT EDIT BY HAND. ' +
        'These bytes are the frozen on-chain contract; changing one invalidates published data.',
      sdkVersion,
      sdkIntegrity,
      oracleDigest,
      version: VERSION,
      floatScale: FLOAT_SCALE,
      fieldCount: FIELD_SCHEMA.length,
      schema: FIELD_SCHEMA.map((f) => ({ name: f.name, type: f.type })),
      records,
      legacy,
      writeNumber,
      writeBin,
      floats,
      strings,
    },
    null,
    2,
  ) + '\n',
);

console.log(`wrote ${OUT}`);
console.log(`  sdk=${sdkVersion} oracle=${oracleDigest.slice(0, 12)}`);
console.log(
  `  records=${records.length} writeNumber=${writeNumber.length} ` +
    `writeBin=${writeBin.length} floats=${floats.length} strings=${strings.length}`,
);
```

- [ ] **Typecheck the generator, and see it compile.** Run:

```bash
cd /Users/personal/git/demos/weather-chain/parity/ts && npx tsc --noEmit --project tsconfig.json
```

Expected: no output, exit code 0. If it reports `Cannot find name 'require'` or `Cannot find name 'Buffer'`, the `"types": ["node", "jest"]` entry is missing from `parity/ts/tsconfig.json`.

- [ ] **Run the generator.** Run:

```bash
cd /Users/personal/git/demos/weather-chain/parity/ts && npx ts-node --project tsconfig.json gen-vectors.ts
```

Expected output, exactly:

```
wrote /Users/personal/git/demos/weather-chain/internal/weather/testdata/vectors.json
  sdk=1.10.3 oracle=ffe3653dc800
  records=3 writeNumber=40 writeBin=12 floats=35 strings=21
```

If `oracle=` is anything other than `ffe3653dc800`, one of the seven `ORACLE_FILES` was modified during the move. Content must be byte-identical to what `git mv` carried over — run `git diff HEAD~1 -M --stat` and confirm every oracle file shows zero changed lines.

- [ ] **Run the committed check and see it pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && python3 parity/ts/check-vectors.py
```

Expected output, exactly: `all measured values confirmed`. An `AssertionError` here names the exact key that does not match what was measured — do not adjust the check to fit the output; find out why the bytes moved.

- [ ] **Prove determinism.** Run the generator twice and diff:

```bash
cd /Users/personal/git/demos/weather-chain && \
  cp internal/weather/testdata/vectors.json /tmp/vectors-run1.json && \
  ( cd parity/ts && npx ts-node --project tsconfig.json gen-vectors.ts >/dev/null ) && \
  diff -u /tmp/vectors-run1.json internal/weather/testdata/vectors.json && \
  echo "DETERMINISTIC: byte-identical across runs"
```

Expected: `DETERMINISTIC: byte-identical across runs`. If `diff` prints anything, a non-deterministic field crept in and `make parity` can never pass — fix it before committing.

- [ ] **Confirm the file size and shape.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && wc -c internal/weather/testdata/vectors.json && head -8 internal/weather/testdata/vectors.json
```

Expected: `28639` bytes, and the first eight lines are the `_comment`, `sdkVersion`, `sdkIntegrity`, `oracleDigest`, `version`, `floatScale`, `fieldCount` keys inside the opening brace.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add parity/ts/check-vectors.py parity/ts/gen-vectors.ts internal/weather/testdata/vectors.json && \
  git commit -m "parity: generate the golden encoder vectors from the pinned oracle

internal/weather/testdata/vectors.json is the frozen byte contract for the
Go port: three fixture scripts (36/99/211 bytes) asserted byte-exact, the
40-entry writeNumber table including the two values that must throw, the
12 writeBin length boundaries, 35 float vectors covering every case where
Go's math.Round disagrees with ECMAScript, and 21 string vectors.

The generator is deterministic by construction. A wall-clock generatedAt
field and git rev-parse HEAD were both tried and both make regenerate-and-
diff permanently red; provenance is the SDK version, the lockfile integrity
hash and a sha256 digest of the oracle sources (ffe3653dc800...).

These vectors exist BEFORE any TypeScript is removed. Once src/format is
gone they can never be regenerated."
```

---

## Task 4: Wire `make parity`, `make parity-regen` and `make parity-test`

**Files:**
- Modify: `/Users/personal/git/demos/weather-chain/Makefile` (the existing file is Docker-oriented, 100 lines, and its line 3 is `.PHONY: help build up down logs restart clean setup test shell mongo-shell status`; append a new section at the end)

**Interfaces:**
- Consumes: `parity/ts/gen-vectors.ts` and `internal/weather/testdata/vectors.json` from Task 3; `parity/ts/tsconfig.json` and `parity/ts/jest.config.js` from Task 2; `parity/ts/package-lock.json` from Task 1.
- Produces: make targets `parity`, `parity-regen`, `parity-test`, and the variables `VECTORS` (`internal/weather/testdata/vectors.json`) and `NPM_INSTALL`. Task 5a extends the same section with `go-build`, `go-test`, `go-lint` and `check`.

**The existing Makefile already defines a `test` target** (`docker-compose run --rm app npm test`). Do not touch it. The Go targets added in Task 5a are deliberately named `go-test` and `go-build` to avoid the collision; reconciling the Docker targets belongs to a later plan.

**One correction to be aware of:** each line of a make recipe runs in its own shell, so a `cd parity/ts` on one line does not persist to the next. Inside the multi-line `parity` recipe the `cd` is wrapped in a subshell `( ... )` precisely so that the `diff` afterwards still resolves `$(VECTORS)` relative to the repository root. Without the subshell the diff silently looks for the file in the wrong directory.

### Steps

- [ ] **Write the failing check.** Run this now, before editing the Makefile, and see it do nothing:

```bash
cd /Users/personal/git/demos/weather-chain && make parity; echo "exit $?"
```

Expected output — measured against the pristine Makefile, and it is **not** `No rule to make target`:

```
make: Nothing to be done for `parity'.
exit 0
```

Tasks 1-3 created a `parity/` **directory**, and GNU make sees a target name that already exists on disk with no rule attached, so it reports the target as already satisfied and exits 0. That is exactly why the `.PHONY: parity parity-regen parity-test` line in the next step is mandatory rather than decorative: without it, `make parity` would keep silently succeeding without ever running the recipe, and the byte contract would never be verified.

- [ ] **Append the parity section to the Makefile.** Add this to the end of `/Users/personal/git/demos/weather-chain/Makefile`:

```makefile

# ---------------------------------------------------------------------------
# Encoder byte parity. The contract between the frozen TypeScript oracle in
# parity/ts/ and the Go package in internal/weather/.
# ---------------------------------------------------------------------------

.PHONY: parity parity-regen parity-test

VECTORS := internal/weather/testdata/vectors.json

# NPM_INSTALL is overridable so `make parity` can run offline once parity/ts has
# been installed once: `NPM_INSTALL=true make parity`.
NPM_INSTALL ?= npm ci --prefer-offline --silent

# parity VERIFIES by default: regenerate into a temp file and diff. Any
# difference fails the build. The committed file is restored on failure so a red
# build never leaves a mutated vector file behind.
parity:
	@cd parity/ts && $(NPM_INSTALL)
	@tmp=$$(mktemp -d); \
	cp $(VECTORS) $$tmp/committed.json; \
	( cd parity/ts && npx ts-node --project tsconfig.json gen-vectors.ts >/dev/null ); \
	if ! diff -u $$tmp/committed.json $(VECTORS); then \
		cp $$tmp/committed.json $(VECTORS); \
		rm -rf $$tmp; \
		echo "PARITY FAILED: the TypeScript oracle no longer reproduces the committed vectors."; \
		echo "The committed file has been restored. Review the diff above before regenerating."; \
		exit 1; \
	fi; \
	rm -rf $$tmp; \
	echo "parity OK: $(VECTORS) reproduces byte-for-byte"

# Regenerate deliberately. Review the diff by hand: it changes the on-chain contract.
parity-regen:
	@cd parity/ts && $(NPM_INSTALL)
	@cd parity/ts && npx ts-node --project tsconfig.json gen-vectors.ts
	@git --no-pager diff --stat -- $(VECTORS)
	@echo "REVIEW THE DIFF. Changed bytes mean already-published records no longer decode."

# The oracle's own test suite: a strict typecheck plus 111 jest tests.
parity-test:
	@cd parity/ts && $(NPM_INSTALL)
	@cd parity/ts && npx tsc --noEmit --project tsconfig.json
	@cd parity/ts && npx jest
```

- [ ] **Run `make parity` and see it pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make parity
```

Expected output (the `npm ci` line prints nothing because of `--silent`):

```
parity OK: internal/weather/testdata/vectors.json reproduces byte-for-byte
```

- [ ] **Prove the target actually detects drift.** Corrupt one byte of the committed file, run `make parity`, and confirm it fails and restores:

```bash
cd /Users/personal/git/demos/weather-chain && \
  python3 -c "
p='internal/weather/testdata/vectors.json'
s=open(p).read().replace('\"0310af13\"','\"0310af99\"',1)
open(p,'w').write(s)
print('corrupted one byte of the air_density vector')
" && \
  make parity; echo "make exited $?"; \
  git checkout -- internal/weather/testdata/vectors.json && \
  echo "restored from git"
```

Expected: the diff shows `-      "hex": "0310af99"` / `+      "hex": "0310af13"`, then `PARITY FAILED: ...`, then `make exited 2`, then `restored from git`. If `make parity` reports OK on a corrupted file, the target is not actually diffing — check that the `( cd parity/ts && ... )` subshell parentheses are present.

- [ ] **Run `make parity-test` and see 111 tests pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make parity-test 2>&1 | tail -6
```

Expected tail:

```
Test Suites: 6 passed, 6 total
Tests:       111 passed, 111 total
Snapshots:   0 total
Time:        3.711 s
Ran all test suites.
```

The `Time:` figure varies run to run; every other line must match exactly.

- [ ] **Confirm `make parity` left the tree clean.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git status --porcelain
```

Expected: only `M Makefile`. If `internal/weather/testdata/vectors.json` shows as modified, the restore path in the recipe is wrong.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && git add Makefile && git commit -m "parity: make parity verifies, make parity-regen regenerates

A golden vector that can be silently regenerated is not a contract, so
verification is the default: parity regenerates into a temp file, diffs,
and restores the committed file before failing. Regeneration is behind an
explicit parity-regen that prints the diffstat and a warning, because a
changed byte means already-published records no longer decode.

The Go targets are named go-build/go-test/go-lint because this Makefile
already has a Docker-oriented 'test' target that must keep working."
```

---

## Task 5a: Bootstrap the Go module, lint config and Makefile targets

Pure setup: no failing test, because there is no behaviour yet. It is its own task and its own commit precisely so that the test-first pair in 5b is not buried behind a module bootstrap, a lint-config write and a multi-minute network build.

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/go.mod`
- Create: `/Users/personal/git/demos/weather-chain/go.sum` (generated by `go get`)
- Create: `/Users/personal/git/demos/weather-chain/.golangci.json` (the toolbox config with four documented edits, written out literally)
- Modify: `/Users/personal/git/demos/weather-chain/.gitignore` (append a Go section)
- Modify: `/Users/personal/git/demos/weather-chain/Makefile` (append `go-build`, `go-test`, `go-lint`, `check` to the parity section added in Task 4)

**Interfaces:**
- Consumes: the `parity` and `parity-test` targets from Task 4, which `check` chains.
- Produces: the module identity `github.com/bsv-blockchain-demos/weather-proof`, the pinned linter, and the make targets `go-build`, `go-test`, `go-lint`, `check`. Task 5b is the first task with Go code.

### Task 5a steps

- [ ] **Create the module.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  printf 'module github.com/bsv-blockchain-demos/weather-proof\n\ngo 1.26.3\n' > go.mod && \
  go get github.com/bsv-blockchain/go-sdk@v1.3.2 && \
  cat go.mod
```

Expected `go.mod` afterwards:

```
module github.com/bsv-blockchain-demos/weather-proof

go 1.26.3

require github.com/bsv-blockchain/go-sdk v1.3.2

require (
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/crypto v0.54.0 // indirect
)
```

**Do not run `go mod tidy` in this task** — nothing imports the SDK yet, so tidy would strip the requirement. Task 8 is the first task that imports it, and tidy is safe from then on.

- [ ] **Confirm the toolchain resolves to 1.26.3.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go version
```

Expected: `go version go1.26.3 darwin/arm64` (or your platform). The same command run from `/tmp` may print an older version — that is `GOTOOLCHAIN=auto` doing its job, not a problem.

- [ ] **Write the lint config.** Create `/Users/personal/git/demos/weather-chain/.golangci.json` with exactly this content. It is `/Users/personal/git/go/go-wallet-toolbox/.golangci.json` with four edits, all four visible below: the `gci` prefix and `goimports.local-prefixes` say `github.com/bsv-blockchain-demos/weather-proof`; the `revive` settings block is gone (revive is in the `disable` list and its separate config file does not exist in this repository); and `run.build-tags: ["mage"]` is gone (this module has no build tags). It is written out literally rather than generated by a script that reads an absolute path into another repository on one machine, which would fail in CI, in a worktree, and for everyone else.

```json
{
    "formatters": {
        "enable": [
            "gofmt",
            "gofumpt"
        ],
        "exclusions": {
            "generated": "lax",
            "paths": [
                ".*\\.my\\.go$",
                "lib/bad.go",
                ".make",
                ".vscode",
                "dist",
                "third_party$",
                "builtin$"
            ]
        },
        "settings": {
            "gci": {
                "sections": [
                    "standard",
                    "default",
                    "prefix(github.com/bsv-blockchain-demos/weather-proof)"
                ]
            },
            "gofmt": {
                "simplify": true
            },
            "gofumpt": {
                "extra-rules": false
            },
            "goimports": {
                "local-prefixes": [
                    "github.com/bsv-blockchain-demos/weather-proof"
                ]
            }
        }
    },
    "issues": {
        "uniq-by-line": true
    },
    "linters": {
        "disable": [
            "containedctx",
            "contextcheck",
            "err113",
            "forbidigo",
            "funcorder",
            "gochecknoglobals",
            "gochecknoinits",
            "gocognit",
            "gocritic",
            "gocyclo",
            "godot",
            "godox",
            "gomoddirectives",
            "inamedparam",
            "nakedret",
            "nestif",
            "nilerr",
            "nilnil",
            "predeclared",
            "revive",
            "wsl_v5"
        ],
        "enable": [
            "asasalint",
            "arangolint",
            "asciicheck",
            "bidichk",
            "bodyclose",
            "copyloopvar",
            "dogsled",
            "durationcheck",
            "embeddedstructfieldcheck",
            "errcheck",
            "errchkjson",
            "errname",
            "errorlint",
            "exhaustive",
            "gocheckcompilerdirectives",
            "gochecksumtype",
            "goconst",
            "goheader",
            "gosec",
            "gosmopolitan",
            "govet",
            "ineffassign",
            "loggercheck",
            "makezero",
            "mirror",
            "misspell",
            "musttag",
            "nilnesserr",
            "noctx",
            "nolintlint",
            "nosprintfhostport",
            "prealloc",
            "protogetter",
            "reassign",
            "recvcheck",
            "rowserrcheck",
            "spancheck",
            "sqlclosecheck",
            "staticcheck",
            "testifylint",
            "unconvert",
            "unparam",
            "unused",
            "wastedassign",
            "zerologlint"
        ],
        "settings": {
            "dogsled": {
                "max-blank-identifiers": 2
            },
            "dupl": {
                "threshold": 100
            },
            "exhaustive": {
                "default-signifies-exhaustive": false
            },
            "funcorder": {
                "constructor-after-struct": true
            },
            "funlen": {
                "lines": 60,
                "statements": 40
            },
            "gocognit": {
                "min-complexity": 10
            },
            "goconst": {
                "min-len": 3,
                "min-occurrences": 10
            },
            "gocyclo": {
                "min-complexity": 10
            },
            "godox": {
                "keywords": [
                    "NOTE",
                    "OPTIMIZE",
                    "HACK",
                    "ATTN",
                    "ATTENTION"
                ]
            },
            "govet": {
                "enable": [
                    "atomicalign",
                    "shadow"
                ],
                "settings": {
                    "printf": {
                        "funcs": [
                            "(github.com/golangci/golangci-lint/pkg/logutils.Log).Infof",
                            "(github.com/golangci/golangci-lint/pkg/logutils.Log).Warnf",
                            "(github.com/golangci/golangci-lint/pkg/logutils.Log).Errorf",
                            "(github.com/golangci/golangci-lint/pkg/logutils.Log).Fatalf"
                        ]
                    }
                }
            },
            "lll": {
                "line-length": 120,
                "tab-width": 1
            },
            "misspell": {
                "ignore-rules": [
                    "bsv",
                    "bitcoin",
                    "analyse",
                    "analysed",
                    "analyses",
                    "authorise",
                    "authorised",
                    "behaviour",
                    "cancelled",
                    "cancelling",
                    "cancellation",
                    "capitalise",
                    "catalogue",
                    "categorise",
                    "centre",
                    "colour",
                    "colours",
                    "customise",
                    "customised",
                    "defence",
                    "dialogue",
                    "favour",
                    "favourite",
                    "fibre",
                    "finalise",
                    "finalised",
                    "flavour",
                    "fulfil",
                    "fulfilment",
                    "grey",
                    "honour",
                    "initialise",
                    "initialised",
                    "labelled",
                    "labelling",
                    "labour",
                    "licence",
                    "litre",
                    "marshalling",
                    "maximise",
                    "metre",
                    "minimise",
                    "modelled",
                    "modelling",
                    "neighbour",
                    "normalise",
                    "normalised",
                    "offence",
                    "optimise",
                    "optimised",
                    "organisation",
                    "organise",
                    "organised",
                    "prioritise",
                    "prioritised",
                    "recognise",
                    "recognised",
                    "serialise",
                    "serialised",
                    "serialisation",
                    "signalling",
                    "summarise",
                    "synchronise",
                    "synchronised",
                    "travelled",
                    "travelling",
                    "whilst"
                ],
                "locale": "US"
            },
            "nakedret": {
                "max-func-lines": 30
            },
            "nestif": {
                "min-complexity": 4
            },
            "nolintlint": {
                "allow-unused": false,
                "require-explanation": true,
                "require-specific": true
            },
            "prealloc": {
                "for-loops": false,
                "range-loops": true,
                "simple": true
            },
            "unparam": {
                "check-exported": false
            },
            "wsl": {
                "allow-assign-and-call": true,
                "allow-cuddle-declarations": true,
                "allow-multiline-assign": true,
                "strict-append": true
            }
        }
    },
    "output": {
        "formats": {
            "text": {
                "path": "stdout",
                "print-issued-lines": true,
                "print-linter-name": true
            }
        }
    },
    "run": {
        "allow-parallel-runners": true,
        "concurrency": 8,
        "issues-exit-code": 1,
        "tests": true
    },
    "severity": {
        "default": "warning",
        "rules": [
            {
                "linters": [
                    "dupl",
                    "misspell",
                    "makezero"
                ],
                "severity": "info"
            }
        ]
    },
    "version": "2"
}
```

- [ ] **Verify the lint config.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && python3 -c "
import json
c = json.load(open('.golangci.json'))
assert c['version'] == '2'
assert c['run']['tests'] is True
assert 'build-tags' not in c['run']
assert 'revive' not in c['linters']['settings']
assert c['linters']['settings']['exhaustive']['default-signifies-exhaustive'] is False
assert c['linters']['settings']['nolintlint'] == {'allow-unused': False, 'require-explanation': True, 'require-specific': True}
assert c['linters']['settings']['misspell']['locale'] == 'US'
assert 'shadow' in c['linters']['settings']['govet']['enable']
assert c['formatters']['settings']['gci']['sections'][2] == 'prefix(github.com/bsv-blockchain-demos/weather-proof)'
assert c['formatters']['settings']['goimports']['local-prefixes'] == ['github.com/bsv-blockchain-demos/weather-proof']
assert 'go-wallet-toolbox' not in open('.golangci.json').read()
print('lint config verified')
"
```

Expected final line: `lint config verified`.

- [ ] **Install the pinned linter.** This is the longest-running action in the task: it is a network build measured in minutes, not seconds. Run:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 && golangci-lint --version
```

Expected: a line containing `golangci-lint has version 2.12.2`. `v2.12.2` is the version `go-wallet-toolbox` pins in `.github/env/10-pre-commit.env`, and it is the version every `//nolint` decision in this plan was checked against.

- [ ] **Append the Go section to .gitignore.** Add this to the end of `/Users/personal/git/demos/weather-chain/.gitignore`:

```gitignore
# Go
/weather
*.test
*.out
```

- [ ] **Append the Go targets to the Makefile.** Add this at the end of `/Users/personal/git/demos/weather-chain/Makefile`, directly after the `parity-test` recipe added in Task 4:

```makefile

.PHONY: go-build go-test go-lint check

# Named go-* because this Makefile already has Docker-oriented `build` and `test`
# targets that must keep working. Reconciling them belongs to a later plan.
go-build:
	go build ./...

go-test:
	go test ./... -count=1

go-lint:
	golangci-lint run

# The full gate: the byte contract, the oracle's own tests, then the Go side.
check: parity parity-test go-build go-test go-lint
```

- [ ] **Verify the bootstrap without running the Go toolchain over an empty module.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  go env GOMOD && \
  grep -n 'go-sdk' go.mod && \
  test -f go.sum && echo "go.sum present" && \
  make -n go-build && make -n go-test && make -n go-lint && \
  grep -c 'go-build\|go-test\|go-lint\|^check:' Makefile
```

Expected: the absolute path of `go.mod`, the `require github.com/bsv-blockchain/go-sdk v1.3.2` line, `go.sum present`, then the three recipe bodies echoed by `make -n` (`go build ./...`, `go test ./... -count=1`, `golangci-lint run`), then a count of at least `6`. `make -n` prints recipes without executing them, which is deliberate: `go build ./...` and `golangci-lint run` over a module with no Go files have no useful exit status to assert. Task 5b runs them for real.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add go.mod go.sum .golangci.json .gitignore Makefile && \
  git commit -m "weather: bootstrap the Go module, lint config and make targets

Module github.com/bsv-blockchain-demos/weather-proof at the repository
root, go 1.26.3, one dependency (go-sdk v1.3.2), no toolchain line.

.golangci.json is the go-wallet-toolbox config with four edits, written out
literally rather than generated: the gci prefix, goimports local-prefixes,
the revive settings dropped (revive is disabled and its config file does not
exist here) and the mage build tag dropped. Generating it from an absolute
path into another repository would have worked on exactly one machine.

nolintlint runs with allow-unused: false, so an unnecessary //nolint is
itself a lint failure. Every directive in this port was checked against
golangci-lint v2.12.2 with this exact config.

Makefile gains go-build/go-test/go-lint and a check target that runs the
byte contract first. No Go code yet: that is 5b."
```

---

## Task 5b: `internal/weather/types.go` — the record types

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/types.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/types_test.go`

**Interfaces:**
- Consumes: the module and lint config from Task 5a.
- Produces, all in `package weather`:
  - `const Version = 1` (untyped)
  - `const FloatScale = 1_000_000` (untyped), `const FloatEpsilon = 1e-6` (untyped)
  - `const DataFieldsPerRecord = 33`, `const ChunksPrefixed = 36`, `const ChunksLegacy = 34`
  - `type FieldType uint8` with `FieldInteger`, `FieldFloat`, `FieldString`, `FieldBoolean` (in that iota order) and `func (t FieldType) String() string` returning `"integer"`, `"float"`, `"string"`, `"boolean"`, `"unknown"`
  - `type FieldDefinition struct { Name string; Type FieldType; Required bool }`
  - `type WeatherData struct { ... }` — 33 exported fields, alphabetical by json tag; `AirDensity float64`, `StationPressure float64`, `IsPrecipLocalDayRainCheck bool`, `IsPrecipLocalYesterdayRainCheck bool`, `Conditions`/`Icon`/`LightningStrikeLastDistanceMsg`/`PressureTrend`/`WindDirectionCardinal` all `string`, and the remaining 24 `int64`
  - test helper `func jsonTags(t *testing.T) []string` — Task 7's `schema_test.go` calls it

### Task 5b steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/types_test.go` with exactly this content:

```go
package weather

import (
	"reflect"
	"sort"
	"testing"
)

func TestConstants(t *testing.T) {
	if Version != 1 {
		t.Errorf("Version = %d, want 1", Version)
	}

	if FloatScale != 1000000 {
		t.Errorf("FloatScale = %d, want 1000000", FloatScale)
	}

	if FloatEpsilon != 1e-6 {
		t.Errorf("FloatEpsilon = %v, want 1e-6", FloatEpsilon)
	}

	if DataFieldsPerRecord != 33 {
		t.Errorf("DataFieldsPerRecord = %d, want 33", DataFieldsPerRecord)
	}

	if ChunksPrefixed != 36 {
		t.Errorf("ChunksPrefixed = %d, want 36 (OP_FALSE, OP_RETURN, OP_1, 33 fields)", ChunksPrefixed)
	}

	if ChunksLegacy != 34 {
		t.Errorf("ChunksLegacy = %d, want 34 (OP_1, 33 fields)", ChunksLegacy)
	}
}

func TestFieldTypeString(t *testing.T) {
	cases := map[FieldType]string{
		FieldInteger: "integer",
		FieldFloat:   "float",
		FieldString:  "string",
		FieldBoolean: "boolean",
	}
	for ft, want := range cases {
		if got := ft.String(); got != want {
			t.Errorf("FieldType(%d).String() = %q, want %q", ft, got, want)
		}
	}
}

// jsonTags returns the json tag of every WeatherData field, in declaration order.
func jsonTags(t *testing.T) []string {
	t.Helper()

	rt := reflect.TypeOf(WeatherData{})

	tags := make([]string, 0, rt.NumField())
	for i := range rt.NumField() {
		tag, ok := rt.Field(i).Tag.Lookup("json")
		if !ok {
			t.Fatalf("WeatherData.%s has no json tag", rt.Field(i).Name)
		}
		tags = append(tags, tag)
	}

	return tags
}

func TestWeatherDataJSONTagsAreAlphabeticalAndComplete(t *testing.T) {
	tags := jsonTags(t)

	if len(tags) != DataFieldsPerRecord {
		t.Fatalf("WeatherData has %d fields, want %d", len(tags), DataFieldsPerRecord)
	}

	sorted := make([]string, len(tags))
	copy(sorted, tags)
	sort.Strings(sorted)

	for i := range tags {
		if tags[i] != sorted[i] {
			t.Errorf("json tag %d is %q but alphabetical order wants %q: declaration order is the wire order",
				i, tags[i], sorted[i])
		}
	}

	if len(tags) > 0 {
		if tags[0] != "air_density" {
			t.Errorf("first json tag = %q, want air_density", tags[0])
		}

		if tags[len(tags)-1] != "wind_gust" {
			t.Errorf("last json tag = %q, want wind_gust", tags[len(tags)-1])
		}
	}
}
```

- [ ] **Run it and see it fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -10
```

Expected: one compilation failure per undefined identifier, then the build-failed line:

```
internal/weather/types_test.go:10:5: undefined: Version
internal/weather/types_test.go:14:5: undefined: FloatScale
internal/weather/types_test.go:18:5: undefined: FloatEpsilon
internal/weather/types_test.go:22:5: undefined: DataFieldsPerRecord
internal/weather/types_test.go:26:5: undefined: ChunksPrefixed
internal/weather/types_test.go:30:5: undefined: ChunksLegacy
internal/weather/types_test.go:36:14: undefined: FieldType
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [build failed]
```

Go truncates the list after ten errors, so the exact set shown depends on the
order it reports them; what matters is that every identifier is undefined and
the package does not build.

- [ ] **Write the implementation.** Create `/Users/personal/git/demos/weather-chain/internal/weather/types.go` with exactly this content:

```go
// Package weather encodes and decodes weather records as BSV locking scripts.
//
// The byte layout produced here is a FROZEN CONTRACT: records already published
// on chain must stay decodable, and the browser frontend decodes new records
// with the TypeScript implementation retained at parity/ts/. Any change to the
// bytes this package emits breaks published data. The golden vectors in
// testdata/vectors.json are the contract; `make parity` regenerates them from
// the pinned TypeScript oracle and fails on any difference.
package weather

// Version is the record schema version.
//
// WARNING: the version is emitted as a SINGLE OPCODE (OP_1 = 0x51), not a data
// push. At version 17 and above it stops being a 1-byte opcode and becomes a
// data push (17 encodes as 0111), which silently changes the record prefix from
// 3 bytes to 4 and changes the chunk-count basis from 36 to 37. Any bump needs a
// coordinated change in BOTH decoders: this package and parity/ts/src/format.
const Version = 1

const (
	// FloatScale is the fixed-point scale for the two float fields: 6 decimals.
	FloatScale = 1_000_000

	// FloatEpsilon is the comparison tolerance for a decoded float.
	FloatEpsilon = 1e-6

	// DataFieldsPerRecord is the number of schema fields in every record.
	DataFieldsPerRecord = 33

	// ChunksPrefixed is the chunk count of the current layout:
	// OP_FALSE, OP_RETURN, OP_1, then 33 field pushes.
	//
	// script.DecodeOptionsParseOpReturn steps PAST the 0x6a byte but still
	// appends the OP_RETURN chunk, so the basis is 36 and not 34. A guard
	// written on a 34 basis accepts a script truncated by two fields.
	ChunksPrefixed = 36

	// ChunksLegacy is the chunk count of BOTH pre-c44b7ae layouts, which have no
	// OP_FALSE OP_RETURN prefix: OP_1 then 33 field pushes.
	//
	// Two distinct wire formats share this count, because commit e2ae463
	// reordered the schema three minutes before c44b7ae added the prefix:
	// layout A (before e2ae463) is FieldSchemaV0 order, layout B (e2ae463 to
	// c44b7ae) is FieldSchema order. Nothing in the byte layout tells them
	// apart; decoder.go discriminates on the value of the first field.
	ChunksLegacy = 34
)

// FieldType is the wire type of a schema field.
type FieldType uint8

const (
	// FieldInteger is emitted with appendScriptNum.
	FieldInteger FieldType = iota
	// FieldFloat is scaled by FloatScale, rounded with jsRound, then emitted
	// with appendScriptNum.
	FieldFloat
	// FieldString is emitted as a UTF-8 data push.
	FieldString
	// FieldBoolean is emitted as appendScriptNum(0) or appendScriptNum(1).
	FieldBoolean
)

// String implements fmt.Stringer using the TypeScript spelling of each type, so
// a Go value can be compared directly against the `type` strings in
// testdata/vectors.json.
func (t FieldType) String() string {
	switch t {
	case FieldInteger:
		return "integer"
	case FieldFloat:
		return "float"
	case FieldString:
		return "string"
	case FieldBoolean:
		return "boolean"
	default:
		return "unknown"
	}
}

// FieldDefinition mirrors FieldDefinition in parity/ts/src/format/types.ts.
type FieldDefinition struct {
	Name     string
	Type     FieldType
	Required bool
}

// WeatherData is one weather reading.
//
// The JSON tags are the Tempest field names and are also the keys used by the
// golden vectors, so they must never be renamed. The declaration order is the
// same strict alphabetical order as FieldSchema, which is the wire order.
type WeatherData struct {
	AirDensity                      float64 `json:"air_density"`
	AirTemperature                  int64   `json:"air_temperature"`
	Brightness                      int64   `json:"brightness"`
	Conditions                      string  `json:"conditions"`
	DeltaT                          int64   `json:"delta_t"`
	DewPoint                        int64   `json:"dew_point"`
	FeelsLike                       int64   `json:"feels_like"`
	Icon                            string  `json:"icon"`
	IsPrecipLocalDayRainCheck       bool    `json:"is_precip_local_day_rain_check"`
	IsPrecipLocalYesterdayRainCheck bool    `json:"is_precip_local_yesterday_rain_check"`
	LightningStrikeCountLast1hr     int64   `json:"lightning_strike_count_last_1hr"`
	LightningStrikeCountLast3hr     int64   `json:"lightning_strike_count_last_3hr"`
	LightningStrikeLastDistance     int64   `json:"lightning_strike_last_distance"`
	LightningStrikeLastDistanceMsg  string  `json:"lightning_strike_last_distance_msg"`
	LightningStrikeLastEpoch        int64   `json:"lightning_strike_last_epoch"`
	PrecipAccumLocalDay             int64   `json:"precip_accum_local_day"`
	PrecipAccumLocalYesterday       int64   `json:"precip_accum_local_yesterday"`
	PrecipMinutesLocalDay           int64   `json:"precip_minutes_local_day"`
	PrecipMinutesLocalYesterday     int64   `json:"precip_minutes_local_yesterday"`
	PrecipProbability               int64   `json:"precip_probability"`
	PressureTrend                   string  `json:"pressure_trend"`
	RelativeHumidity                int64   `json:"relative_humidity"`
	SeaLevelPressure                int64   `json:"sea_level_pressure"`
	SolarRadiation                  int64   `json:"solar_radiation"`
	StationPressure                 float64 `json:"station_pressure"`
	Time                            int64   `json:"time"`
	UV                              int64   `json:"uv"`
	WetBulbGlobeTemperature         int64   `json:"wet_bulb_globe_temperature"`
	WetBulbTemperature              int64   `json:"wet_bulb_temperature"`
	WindAvg                         int64   `json:"wind_avg"`
	WindDirection                   int64   `json:"wind_direction"`
	WindDirectionCardinal           string  `json:"wind_direction_cardinal"`
	WindGust                        int64   `json:"wind_gust"`
}
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofmt -l internal/ && go test ./internal/weather/... -count=1 -v 2>&1 | tail -10
```

Expected: `gofmt -l` prints nothing, and:

```
=== RUN   TestConstants
--- PASS: TestConstants (0.00s)
=== RUN   TestFieldTypeString
--- PASS: TestFieldTypeString (0.00s)
=== RUN   TestWeatherDataJSONTagsAreAlphabeticalAndComplete
--- PASS: TestWeatherDataJSONTagsAreAlphabeticalAndComplete (0.00s)
PASS
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.2s
```

- [ ] **Run the Go gate and see it clean.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make go-build && make go-test && make go-lint && echo "GO GATE CLEAN"
```

Expected: `GO GATE CLEAN`. If `golangci-lint` reports `gofumpt` differences, run `gofumpt -w internal/` (install with `go install mvdan.cc/gofumpt@latest`) and re-run.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/types.go internal/weather/types_test.go && \
  git commit -m "weather: the record types and constants

internal/weather/types.go carries the constants and the 33-field
WeatherData whose json tags are the wire field names in strict
alphabetical order — the declaration order IS the wire order, and the test
asserts it by sorting the tags rather than trusting the author.

The Version comment records why 17 is a breaking change: the version is a
single opcode, not a data push, so at 17 the record prefix silently grows
from 3 bytes to 4 and the chunk basis from 36 to 37.

ChunksPrefixed is 36 and not 34 because DecodeOptionsParseOpReturn steps
past the 0x6a byte but still appends the OP_RETURN chunk."
```

---

## Task 6: The golden-vector loader and integrity gate

**Files:**
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/vectors_test.go`
- Depends on the already-committed `/Users/personal/git/demos/weather-chain/internal/weather/testdata/vectors.json`

**Interfaces:**
- Consumes: `Version`, `FloatScale`, `DataFieldsPerRecord` from Task 5b; `internal/weather/testdata/vectors.json` from Task 3.
- Produces, all in `package weather` test scope:
  - `const vectorsPath = "testdata/vectors.json"`
  - `const expectedSDKVersion = "1.10.3"`
  - `type testVectors struct` with fields `Comment`, `SDKVersion`, `SDKIntegrity`, `OracleDigest`, `Version int64`, `FloatScale int64`, `FieldCount int`, `Schema []struct{Name, Type string}`, `Records []struct{Name string; Data json.RawMessage; Hex string; Bytes, Chunks int; RoundTrips bool}`, `Legacy []struct{Name, Hex string; Chunks int; Data json.RawMessage}`, `WriteNumber []struct{Value, Hex, Error string}`, `WriteBin []struct{Len int; Hex string}`, `Floats []struct{Input float64; Scaled, Hex, Error string}`, `Strings []struct{Str string; UTF8Len int; Hex string}`
  - `func loadVectors(t *testing.T) *testVectors` — used by Tasks 7, 8, 9, 10 and 11
- The decoder uses `json.DisallowUnknownFields`, so if Task 3's generator ever adds a key that this struct lacks, every parity test fails loudly rather than silently ignoring new coverage.

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/vectors_test.go` with exactly this content:

```go
package weather

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// vectorsPath is the golden parity contract. It is written ONLY by
// parity/ts/gen-vectors.ts via `make parity-regen`; never edit it by hand.
const vectorsPath = "testdata/vectors.json"

// expectedSDKVersion is the @bsv/sdk version the vectors were generated from.
// parity/ts/package.json pins it exactly, with no caret.
const expectedSDKVersion = "1.10.3"

// testVectors mirrors the JSON written by parity/ts/gen-vectors.ts.
type testVectors struct {
	Comment      string `json:"_comment"`
	SDKVersion   string `json:"sdkVersion"`
	SDKIntegrity string `json:"sdkIntegrity"`
	OracleDigest string `json:"oracleDigest"`
	Version      int64  `json:"version"`
	FloatScale   int64  `json:"floatScale"`
	FieldCount   int    `json:"fieldCount"`

	Schema []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"schema"`

	Records []struct {
		Name       string          `json:"name"`
		Data       json.RawMessage `json:"data"`
		Hex        string          `json:"hex"`
		Bytes      int             `json:"bytes"`
		Chunks     int             `json:"chunks"`
		RoundTrips bool            `json:"roundTrips"`
	} `json:"records"`

	Legacy []struct {
		Name   string          `json:"name"`
		Hex    string          `json:"hex"`
		Chunks int             `json:"chunks"`
		Data   json.RawMessage `json:"data"`
	} `json:"legacy"`

	WriteNumber []struct {
		Value string `json:"value"`
		Hex   string `json:"hex"`
		Error string `json:"error"`
	} `json:"writeNumber"`

	WriteBin []struct {
		Len int    `json:"len"`
		Hex string `json:"hex"`
	} `json:"writeBin"`

	Floats []struct {
		Input  float64 `json:"input"`
		Scaled string  `json:"scaled"`
		Hex    string  `json:"hex"`
		Error  string  `json:"error"`
	} `json:"floats"`

	Strings []struct {
		Str     string `json:"str"`
		UTF8Len int    `json:"utf8Len"`
		Hex     string `json:"hex"`
	} `json:"strings"`
}

// loadVectors reads and validates the golden file. Every parity test starts here,
// so a missing or truncated file fails every test loudly instead of silently
// checking nothing.
func loadVectors(t *testing.T) *testVectors {
	t.Helper()

	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v\nRun `make parity-regen` from the repository root.", vectorsPath, err)
	}

	var v testVectors

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&v); err != nil {
		t.Fatalf("cannot parse %s: %v", vectorsPath, err)
	}

	return &v
}

func TestVectorsFileIntegrity(t *testing.T) {
	v := loadVectors(t)

	if v.SDKVersion != expectedSDKVersion {
		t.Errorf("sdkVersion = %q, want %q: the oracle was generated from a different @bsv/sdk",
			v.SDKVersion, expectedSDKVersion)
	}

	if len(v.SDKIntegrity) == 0 || v.SDKIntegrity == "MISSING" {
		t.Errorf("sdkIntegrity = %q: the lockfile integrity hash is the strongest pin and must be recorded",
			v.SDKIntegrity)
	}

	if len(v.OracleDigest) != 64 {
		t.Errorf("oracleDigest = %q (%d chars), want a 64-character sha256 hex digest",
			v.OracleDigest, len(v.OracleDigest))
	}

	if v.Version != Version {
		t.Errorf("vectors version = %d, Go Version = %d", v.Version, Version)
	}

	if v.FloatScale != FloatScale {
		t.Errorf("vectors floatScale = %d, Go FloatScale = %d", v.FloatScale, FloatScale)
	}

	if v.FieldCount != DataFieldsPerRecord {
		t.Errorf("vectors fieldCount = %d, Go DataFieldsPerRecord = %d", v.FieldCount, DataFieldsPerRecord)
	}

	if len(v.Schema) != DataFieldsPerRecord {
		t.Errorf("len(schema) = %d, want %d", len(v.Schema), DataFieldsPerRecord)
	}

	counts := map[string]int{
		"records":     len(v.Records),
		"legacy":      len(v.Legacy),
		"writeNumber": len(v.WriteNumber),
		"writeBin":    len(v.WriteBin),
		"floats":      len(v.Floats),
		"strings":     len(v.Strings),
	}
	want := map[string]int{
		"records": 3, "legacy": 3, "writeNumber": 40,
		"writeBin": 12, "floats": 35, "strings": 21,
	}

	for name, wantN := range want {
		if counts[name] != wantN {
			t.Errorf("%s has %d entries, want %d: the golden file lost coverage", name, counts[name], wantN)
		}
	}

	t.Logf("vectors OK: sdk=%s oracle=%s records=%d writeNumber=%d writeBin=%d floats=%d strings=%d",
		v.SDKVersion, v.OracleDigest[:12], counts["records"], counts["writeNumber"],
		counts["writeBin"], counts["floats"], counts["strings"])
}
```

- [ ] **Prove the test really fails when the file is missing.** Temporarily move the golden file away, run, and see the loud failure:

```bash
cd /Users/personal/git/demos/weather-chain && \
  mv internal/weather/testdata/vectors.json /tmp/vectors-hidden.json && \
  go test ./internal/weather/... -run TestVectorsFileIntegrity 2>&1 | head -5; \
  mv /tmp/vectors-hidden.json internal/weather/testdata/vectors.json && \
  echo "golden file restored"
```

Expected:

```
--- FAIL: TestVectorsFileIntegrity (0.00s)
    vectors_test.go:82: cannot read testdata/vectors.json: open testdata/vectors.json: no such file or directory
        Run `make parity-regen` from the repository root.
```

followed by `golden file restored`.

- [ ] **Prove the test really fails on an unknown key.** Add a key the struct does not know about, run, restore:

```bash
cd /Users/personal/git/demos/weather-chain && \
  python3 -c "
import json
p='internal/weather/testdata/vectors.json'
s=open(p).read().replace('{\n  \"_comment\"','{\n  \"surpriseKey\": 1,\n  \"_comment\"',1)
open(p,'w').write(s)
" && \
  go test ./internal/weather/... -run TestVectorsFileIntegrity 2>&1 | head -3; \
  git checkout -- internal/weather/testdata/vectors.json && echo "golden file restored"
```

Expected: `cannot parse testdata/vectors.json: json: unknown field "surpriseKey"`, then `golden file restored`.

- [ ] **Run it and see it pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... -run TestVectorsFileIntegrity -count=1 -v
```

Expected:

```
=== RUN   TestVectorsFileIntegrity
    vectors_test.go:150: vectors OK: sdk=1.10.3 oracle=ffe3653dc800 records=3 writeNumber=40 writeBin=12 floats=35 strings=21
--- PASS: TestVectorsFileIntegrity (0.00s)
PASS
```

- [ ] **Confirm lint is clean and the tree is clean.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofmt -l internal/ && make go-lint && git status --porcelain
```

Expected: `gofmt -l` prints nothing, `golangci-lint` prints nothing, and `git status --porcelain` shows only `?? internal/weather/vectors_test.go`.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/vectors_test.go && \
  git commit -m "weather: load and gate the golden parity vectors

Every parity test in this package starts at loadVectors, so a missing or
truncated golden file fails everything loudly instead of silently checking
nothing. DisallowUnknownFields means a key the generator adds without a
matching Go field is a test failure, not lost coverage.

TestVectorsFileIntegrity pins the provenance (sdk 1.10.3, a 64-char oracle
digest, a non-empty lockfile integrity hash) and the entry counts, so a
generator change that quietly drops a table cannot pass."
```

---

## Task 7: `internal/weather/schema.go` — the 33-field wire order, current and superseded

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/schema.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/schema_test.go`
- Modify: `/Users/personal/git/demos/weather-chain/ENCODING.md` (insert a version notice immediately after the top-level heading; the existing "Field Order" section is KEPT INTACT, because it documents the superseded layout-A order)

**Interfaces:**
- Consumes: `FieldDefinition`, `FieldType`, `FieldInteger`, `FieldFloat`, `FieldString`, `FieldBoolean`, `WeatherData`, `DataFieldsPerRecord` from Task 5b; `loadVectors` from Task 6; `jsonTags` from Task 5b's test file.
- Produces:
  - `var FieldSchema = []FieldDefinition{...}` — 33 entries, index 0 `air_density`/`FieldFloat`, index 32 `wind_gust`/`FieldInteger`, every entry `Required: true`
  - `var FieldSchemaV0 = []FieldDefinition{...}` — the same 33 entries in the SUPERSEDED layout-A order, index 0 `time`/`FieldInteger`, index 32 `icon`/`FieldString`. Task 11's decoder consumes it; nothing encodes with it, ever.
  - `func (d *WeatherData) fieldPtrs() []any` — 33 pointers, `ptrs[i]` corresponding to `FieldSchema[i]`, concrete types `*int64` / `*float64` / `*string` / `*bool` matching the schema type. Tasks 10 and 11 both consume it: the encoder reads through the pointers, the decoder writes through them.
  - `func (d *WeatherData) fieldPtrsV0() []any` — the same pointers in `FieldSchemaV0` order, derived from `fieldPtrs` rather than hand-written a second time. Task 11's decoder consumes it.

`fieldPtrs` lives in `schema.go` rather than `types.go` because it is the schema-to-struct bridge, and because it is the file where a schema edit must be mirrored.

**Why a second schema exists.** `git merge-base --is-ancestor e2ae463 c44b7ae` exits 0 and the two commits are three minutes apart, so the alphabetical reorder shipped BEFORE the `OP_FALSE OP_RETURN` prefix. Records written between `34b5b81` and `e2ae463^` are prefix-less AND in the time-first order. They have the same 34-chunk shape and the same leading `OP_1` as the prefix-less alphabetical records, so a decoder that assumes one order reads the other as silent garbage. `FieldSchemaV0` exists so that Task 11 can read them correctly instead. It is transcribed from `git show e2ae463^:src/format/schema.ts`, which is the authority — run that command and diff it against the list below before trusting either.

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/schema_test.go` with exactly this content:

```go
package weather

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// wantFieldNames is the wire order, transcribed independently from
// parity/ts/src/format/schema.ts. It is deliberately duplicated rather than
// derived from FieldSchema: a test that reads the value it is checking proves
// nothing.
var wantFieldNames = []string{
	"air_density",
	"air_temperature",
	"brightness",
	"conditions",
	"delta_t",
	"dew_point",
	"feels_like",
	"icon",
	"is_precip_local_day_rain_check",
	"is_precip_local_yesterday_rain_check",
	"lightning_strike_count_last_1hr",
	"lightning_strike_count_last_3hr",
	"lightning_strike_last_distance",
	"lightning_strike_last_distance_msg",
	"lightning_strike_last_epoch",
	"precip_accum_local_day",
	"precip_accum_local_yesterday",
	"precip_minutes_local_day",
	"precip_minutes_local_yesterday",
	"precip_probability",
	"pressure_trend",
	"relative_humidity",
	"sea_level_pressure",
	"solar_radiation",
	"station_pressure",
	"time",
	"uv",
	"wet_bulb_globe_temperature",
	"wet_bulb_temperature",
	"wind_avg",
	"wind_direction",
	"wind_direction_cardinal",
	"wind_gust",
}

func TestFieldSchemaOrderAndCount(t *testing.T) {
	if len(FieldSchema) != DataFieldsPerRecord {
		t.Fatalf("len(FieldSchema) = %d, want %d", len(FieldSchema), DataFieldsPerRecord)
	}

	if len(wantFieldNames) != DataFieldsPerRecord {
		t.Fatalf("the test's own list has %d names, want %d", len(wantFieldNames), DataFieldsPerRecord)
	}

	for i, want := range wantFieldNames {
		if FieldSchema[i].Name != want {
			t.Errorf("FieldSchema[%d].Name = %q, want %q: the order IS the wire format",
				i, FieldSchema[i].Name, want)
		}

		if !FieldSchema[i].Required {
			t.Errorf("FieldSchema[%d] (%s).Required = false, want true", i, FieldSchema[i].Name)
		}
	}
}

func TestFieldSchemaIsStrictlyAlphabetical(t *testing.T) {
	names := make([]string, 0, len(FieldSchema))
	for _, f := range FieldSchema {
		names = append(names, f.Name)
	}

	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)

	for i := range names {
		if names[i] != sorted[i] {
			t.Fatalf("FieldSchema[%d] = %q but alphabetical order wants %q: port the CURRENT order from schema.ts. ENCODING.md's time-first order is the SUPERSEDED one and lives in FieldSchemaV0",
				i, names[i], sorted[i])
		}
	}
}

// wantFieldNamesV0 is the SUPERSEDED layout-A wire order, transcribed
// independently from `git show e2ae463^:src/format/schema.ts`. Commit e2ae463
// ("update ordering and readme", 2026-01-27 11:11:45 -0600) replaced it with
// strict alphabetical order three minutes before c44b7ae added the OP_RETURN
// prefix, so records in this order are prefix-less AND time-first.
var wantFieldNamesV0 = []string{
	"time",
	"air_temperature",
	"feels_like",
	"dew_point",
	"wet_bulb_temperature",
	"wet_bulb_globe_temperature",
	"delta_t",
	"station_pressure",
	"sea_level_pressure",
	"pressure_trend",
	"relative_humidity",
	"air_density",
	"wind_avg",
	"wind_gust",
	"wind_direction",
	"wind_direction_cardinal",
	"precip_probability",
	"precip_accum_local_day",
	"precip_accum_local_yesterday",
	"precip_minutes_local_day",
	"precip_minutes_local_yesterday",
	"is_precip_local_day_rain_check",
	"is_precip_local_yesterday_rain_check",
	"lightning_strike_count_last_1hr",
	"lightning_strike_count_last_3hr",
	"lightning_strike_last_distance",
	"lightning_strike_last_distance_msg",
	"lightning_strike_last_epoch",
	"brightness",
	"solar_radiation",
	"uv",
	"conditions",
	"icon",
}

// TestFieldSchemaV0IsAPermutationWithIdenticalTypes is the safety property that
// makes the layout-A decode path sound: V0 must hold exactly the same 33 names
// as the current schema, each with the same type. If it drifts, a layout-A
// record decodes into the wrong struct field instead of being read correctly.
func TestFieldSchemaV0IsAPermutationWithIdenticalTypes(t *testing.T) {
	if len(FieldSchemaV0) != DataFieldsPerRecord {
		t.Fatalf("len(FieldSchemaV0) = %d, want %d", len(FieldSchemaV0), DataFieldsPerRecord)
	}

	if len(wantFieldNamesV0) != DataFieldsPerRecord {
		t.Fatalf("the test's own V0 list has %d names, want %d", len(wantFieldNamesV0), DataFieldsPerRecord)
	}

	for i, want := range wantFieldNamesV0 {
		if FieldSchemaV0[i].Name != want {
			t.Errorf("FieldSchemaV0[%d].Name = %q, want %q: this order IS the layout-A wire format",
				i, FieldSchemaV0[i].Name, want)
		}
	}

	typeOf := make(map[string]FieldType, len(FieldSchema))
	for _, f := range FieldSchema {
		typeOf[f.Name] = f.Type
	}

	for i, f := range FieldSchemaV0 {
		got, ok := typeOf[f.Name]
		if !ok {
			t.Errorf("FieldSchemaV0[%d] names %q, which is not in FieldSchema", i, f.Name)

			continue
		}

		if got != f.Type {
			t.Errorf("field %q is %s in FieldSchemaV0 but %s in FieldSchema", f.Name, f.Type, got)
		}

		if !f.Required {
			t.Errorf("FieldSchemaV0[%d] (%s).Required = false, want true", i, f.Name)
		}
	}

	// A permutation, not merely a subset: sorting V0 must reproduce the current
	// order exactly, which also catches a duplicated name.
	sortedV0 := make([]string, 0, len(FieldSchemaV0))
	for _, f := range FieldSchemaV0 {
		sortedV0 = append(sortedV0, f.Name)
	}

	sort.Strings(sortedV0)

	for i, f := range FieldSchema {
		if sortedV0[i] != f.Name {
			t.Fatalf("sorted FieldSchemaV0[%d] = %q, want %q: V0 is not a permutation of FieldSchema",
				i, sortedV0[i], f.Name)
		}
	}
}

func TestFieldPtrsV0MatchSchemaV0Types(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrsV0()

	if len(ptrs) != len(FieldSchemaV0) {
		t.Fatalf("fieldPtrsV0 returned %d pointers for %d schema fields", len(ptrs), len(FieldSchemaV0))
	}

	wantType := map[FieldType]string{
		FieldInteger: "*int64",
		FieldFloat:   "*float64",
		FieldString:  "*string",
		FieldBoolean: "*bool",
	}

	// The V0 pointers must be the SAME pointers as the alphabetical ones, just
	// reordered, so writing through either list fills the same struct.
	alpha := d.fieldPtrs()
	alphaIndex := make(map[string]int, len(FieldSchema))

	for i, f := range FieldSchema {
		alphaIndex[f.Name] = i
	}

	for i, f := range FieldSchemaV0 {
		got := reflect.TypeOf(ptrs[i]).String()
		if got != wantType[f.Type] {
			t.Errorf("fieldPtrsV0[%d] (%s) is %s, want %s for schema type %s",
				i, f.Name, got, wantType[f.Type], f.Type)

			continue
		}

		if ptrs[i] != alpha[alphaIndex[f.Name]] {
			t.Errorf("fieldPtrsV0[%d] (%s) does not point at the same field as fieldPtrs[%d]",
				i, f.Name, alphaIndex[f.Name])
		}
	}
}

func TestFieldSchemaTypeTally(t *testing.T) {
	tally := map[FieldType]int{}
	for _, f := range FieldSchema {
		tally[f.Type]++
	}

	want := map[FieldType]int{
		FieldInteger: 24,
		FieldFloat:   2,
		FieldString:  5,
		FieldBoolean: 2,
	}
	for ft, n := range want {
		if tally[ft] != n {
			t.Errorf("%s field count = %d, want %d", ft, tally[ft], n)
		}
	}
}

// TestFieldSchemaMatchesVectors is the parity gate: the Go schema must agree with
// the schema the TypeScript oracle recorded, index by index. Any reordering of
// either side fails here.
func TestFieldSchemaMatchesVectors(t *testing.T) {
	v := loadVectors(t)

	if len(FieldSchema) != len(v.Schema) {
		t.Fatalf("Go schema has %d fields, vectors have %d", len(FieldSchema), len(v.Schema))
	}

	for i, want := range v.Schema {
		got := FieldSchema[i]
		if got.Name != want.Name || got.Type.String() != want.Type {
			t.Errorf("schema[%d]: Go = %s/%s, vectors = %s/%s",
				i, got.Name, got.Type, want.Name, want.Type)
		}
	}

	t.Logf("schema parity OK: %d fields, %s .. %s",
		len(FieldSchema), FieldSchema[0].Name, FieldSchema[len(FieldSchema)-1].Name)
}

// TestSchemaNamesMatchJSONTags ties the schema to the struct: every schema name
// must be the json tag at the same index of WeatherData.
func TestSchemaNamesMatchJSONTags(t *testing.T) {
	tags := jsonTags(t)

	if len(tags) != len(FieldSchema) {
		t.Fatalf("WeatherData has %d fields, FieldSchema has %d", len(tags), len(FieldSchema))
	}

	for i, f := range FieldSchema {
		if tags[i] != f.Name {
			t.Errorf("index %d: WeatherData json tag %q != FieldSchema name %q", i, tags[i], f.Name)
		}
	}
}

func TestFieldPtrsMatchSchemaTypes(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	if len(ptrs) != len(FieldSchema) {
		t.Fatalf("fieldPtrs returned %d pointers for %d schema fields", len(ptrs), len(FieldSchema))
	}

	wantType := map[FieldType]string{
		FieldInteger: "*int64",
		FieldFloat:   "*float64",
		FieldString:  "*string",
		FieldBoolean: "*bool",
	}

	for i, f := range FieldSchema {
		got := reflect.TypeOf(ptrs[i]).String()
		if got != wantType[f.Type] {
			t.Errorf("fieldPtrs[%d] (%s) is %s, want %s for schema type %s",
				i, f.Name, got, wantType[f.Type], f.Type)
		}
	}
}

// TestFieldPtrsAreDistinctAndInStructOrder writes a unique marker through every
// pointer and reads it back through JSON, so a copy-paste slip in fieldPtrs (the
// same struct field listed twice) cannot survive.
func TestFieldPtrsAreDistinctAndInStructOrder(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	for i, f := range FieldSchema {
		switch f.Type {
		case FieldInteger:
			*(ptrs[i].(*int64)) = int64(i) + 1
		case FieldFloat:
			*(ptrs[i].(*float64)) = float64(i) + 1
		case FieldString:
			*(ptrs[i].(*string)) = f.Name
		case FieldBoolean:
			*(ptrs[i].(*bool)) = true
		default:
			t.Fatalf("field %d has unknown type %d", i, f.Type)
		}
	}

	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got) != DataFieldsPerRecord {
		t.Fatalf("marshaled %d keys, want %d", len(got), DataFieldsPerRecord)
	}

	for i, f := range FieldSchema {
		v, ok := got[f.Name]
		if !ok {
			t.Errorf("field %d (%s) missing from marshaled JSON", i, f.Name)

			continue
		}

		switch f.Type {
		case FieldInteger, FieldFloat:
			n, isNum := v.(float64)
			if !isNum || n != float64(i)+1 {
				t.Errorf("field %d (%s) = %v, want %v: fieldPtrs index does not match the schema index",
					i, f.Name, v, float64(i)+1)
			}
		case FieldString:
			if v != f.Name {
				t.Errorf("field %d (%s) = %v, want %q", i, f.Name, v, f.Name)
			}
		case FieldBoolean:
			if v != true {
				t.Errorf("field %d (%s) = %v, want true", i, f.Name, v)
			}
		default:
			t.Errorf("field %d has unknown type %d", i, f.Type)
		}
	}
}
```

- [ ] **Run it and see it fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -5
```

Expected:

```
internal/weather/schema_test.go:52:9: undefined: FieldSchema
internal/weather/schema_test.go:153:12: d.fieldPtrs undefined (type *WeatherData has no field or method fieldPtrs)
internal/weather/schema_test.go:222:9: undefined: FieldSchemaV0
internal/weather/schema_test.go:301:12: d.fieldPtrsV0 undefined (type *WeatherData has no field or method fieldPtrsV0)
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [build failed]
```

Line numbers depend on how the compiler orders its report; what matters is that all four identifiers are undefined and the package does not build.

- [ ] **Write the implementation.** Create `/Users/personal/git/demos/weather-chain/internal/weather/schema.go` with exactly this content:

```go
package weather

// FieldSchema is THE WIRE FORMAT.
//
// The order below is the on-chain field layout and must never change: it is
// strict alphabetical order, air_density first and wind_gust last, ported from
// parity/ts/src/format/schema.ts.
//
// Port the CURRENT order from schema.ts ONLY. ENCODING.md's "Field Order"
// section lists a time-first, category-grouped order; that order is not an
// error, it is the SUPERSEDED wire format, and it lives here as FieldSchemaV0.
// The current order is proved by the real sample script, which begins
// 006a51 0310af13, and 0310af13 is air_density (1.29 * 1e6 = 1290000 =
// 0x13AF10, little-endian 10 af 13).
//
// All 33 entries are required in the TypeScript source.
var FieldSchema = []FieldDefinition{
	{Name: "air_density", Type: FieldFloat, Required: true},                            //  0
	{Name: "air_temperature", Type: FieldInteger, Required: true},                      //  1
	{Name: "brightness", Type: FieldInteger, Required: true},                           //  2
	{Name: "conditions", Type: FieldString, Required: true},                            //  3
	{Name: "delta_t", Type: FieldInteger, Required: true},                              //  4
	{Name: "dew_point", Type: FieldInteger, Required: true},                            //  5
	{Name: "feels_like", Type: FieldInteger, Required: true},                           //  6
	{Name: "icon", Type: FieldString, Required: true},                                  //  7
	{Name: "is_precip_local_day_rain_check", Type: FieldBoolean, Required: true},       //  8
	{Name: "is_precip_local_yesterday_rain_check", Type: FieldBoolean, Required: true}, //  9
	{Name: "lightning_strike_count_last_1hr", Type: FieldInteger, Required: true},      // 10
	{Name: "lightning_strike_count_last_3hr", Type: FieldInteger, Required: true},      // 11
	{Name: "lightning_strike_last_distance", Type: FieldInteger, Required: true},       // 12
	{Name: "lightning_strike_last_distance_msg", Type: FieldString, Required: true},    // 13
	{Name: "lightning_strike_last_epoch", Type: FieldInteger, Required: true},          // 14
	{Name: "precip_accum_local_day", Type: FieldInteger, Required: true},               // 15
	{Name: "precip_accum_local_yesterday", Type: FieldInteger, Required: true},         // 16
	{Name: "precip_minutes_local_day", Type: FieldInteger, Required: true},             // 17
	{Name: "precip_minutes_local_yesterday", Type: FieldInteger, Required: true},       // 18
	{Name: "precip_probability", Type: FieldInteger, Required: true},                   // 19
	{Name: "pressure_trend", Type: FieldString, Required: true},                        // 20
	{Name: "relative_humidity", Type: FieldInteger, Required: true},                    // 21
	{Name: "sea_level_pressure", Type: FieldInteger, Required: true},                   // 22
	{Name: "solar_radiation", Type: FieldInteger, Required: true},                      // 23
	{Name: "station_pressure", Type: FieldFloat, Required: true},                       // 24
	{Name: "time", Type: FieldInteger, Required: true},                                 // 25
	{Name: "uv", Type: FieldInteger, Required: true},                                   // 26
	{Name: "wet_bulb_globe_temperature", Type: FieldInteger, Required: true},           // 27
	{Name: "wet_bulb_temperature", Type: FieldInteger, Required: true},                 // 28
	{Name: "wind_avg", Type: FieldInteger, Required: true},                             // 29
	{Name: "wind_direction", Type: FieldInteger, Required: true},                       // 30
	{Name: "wind_direction_cardinal", Type: FieldString, Required: true},               // 31
	{Name: "wind_gust", Type: FieldInteger, Required: true},                            // 32
}

// FieldSchemaV0 is the SUPERSEDED wire format: layout A.
//
// Commit e2ae463 ("update ordering and readme", 2026-01-27 11:11:45 -0600)
// replaced this time-first, category-grouped order with strict alphabetical
// order. Commit c44b7ae added the OP_FALSE OP_RETURN prefix 2026-01-27
// 11:14:41 -0600 — THREE MINUTES LATER — and
// `git merge-base --is-ancestor e2ae463 c44b7ae` exits 0. So three wire formats
// were published in sequence, not two:
//
//	A  34b5b81..e2ae463^  no prefix, THIS order          34 chunks
//	B  e2ae463..c44b7ae^  no prefix, FieldSchema order   34 chunks
//	C  c44b7ae..HEAD      006a prefix, FieldSchema order 36 chunks
//
// A and B are indistinguishable by shape: both are 34 chunks starting OP_1.
// Read a layout-A record under FieldSchema and every one of the 33 fields is
// wrong with no error raised — index 0 becomes air_density = time/1e6, index 3
// becomes conditions = the raw little-endian bytes of dew_point. decoder.go
// therefore discriminates on the VALUE of the first field; see
// legacyUsesV0Order.
//
// Transcribed from `git show e2ae463^:src/format/schema.ts`. NOTHING ENCODES
// WITH THIS. It exists only so already-published layout-A records stay
// readable, and it holds exactly the same 33 names and types as FieldSchema.
var FieldSchemaV0 = []FieldDefinition{
	{Name: "time", Type: FieldInteger, Required: true},                                 //  0
	{Name: "air_temperature", Type: FieldInteger, Required: true},                      //  1
	{Name: "feels_like", Type: FieldInteger, Required: true},                           //  2
	{Name: "dew_point", Type: FieldInteger, Required: true},                            //  3
	{Name: "wet_bulb_temperature", Type: FieldInteger, Required: true},                 //  4
	{Name: "wet_bulb_globe_temperature", Type: FieldInteger, Required: true},           //  5
	{Name: "delta_t", Type: FieldInteger, Required: true},                              //  6
	{Name: "station_pressure", Type: FieldFloat, Required: true},                       //  7
	{Name: "sea_level_pressure", Type: FieldInteger, Required: true},                   //  8
	{Name: "pressure_trend", Type: FieldString, Required: true},                        //  9
	{Name: "relative_humidity", Type: FieldInteger, Required: true},                    // 10
	{Name: "air_density", Type: FieldFloat, Required: true},                            // 11
	{Name: "wind_avg", Type: FieldInteger, Required: true},                             // 12
	{Name: "wind_gust", Type: FieldInteger, Required: true},                            // 13
	{Name: "wind_direction", Type: FieldInteger, Required: true},                       // 14
	{Name: "wind_direction_cardinal", Type: FieldString, Required: true},               // 15
	{Name: "precip_probability", Type: FieldInteger, Required: true},                   // 16
	{Name: "precip_accum_local_day", Type: FieldInteger, Required: true},               // 17
	{Name: "precip_accum_local_yesterday", Type: FieldInteger, Required: true},         // 18
	{Name: "precip_minutes_local_day", Type: FieldInteger, Required: true},             // 19
	{Name: "precip_minutes_local_yesterday", Type: FieldInteger, Required: true},       // 20
	{Name: "is_precip_local_day_rain_check", Type: FieldBoolean, Required: true},       // 21
	{Name: "is_precip_local_yesterday_rain_check", Type: FieldBoolean, Required: true}, // 22
	{Name: "lightning_strike_count_last_1hr", Type: FieldInteger, Required: true},      // 23
	{Name: "lightning_strike_count_last_3hr", Type: FieldInteger, Required: true},      // 24
	{Name: "lightning_strike_last_distance", Type: FieldInteger, Required: true},       // 25
	{Name: "lightning_strike_last_distance_msg", Type: FieldString, Required: true},    // 26
	{Name: "lightning_strike_last_epoch", Type: FieldInteger, Required: true},          // 27
	{Name: "brightness", Type: FieldInteger, Required: true},                           // 28
	{Name: "solar_radiation", Type: FieldInteger, Required: true},                      // 29
	{Name: "uv", Type: FieldInteger, Required: true},                                   // 30
	{Name: "conditions", Type: FieldString, Required: true},                            // 31
	{Name: "icon", Type: FieldString, Required: true},                                  // 32
}

// fieldPtrs returns pointers to the 33 wire fields of d in FieldSchema order.
// Index i of the result corresponds to index i of FieldSchema, and the concrete
// pointer type matches FieldSchema[i].Type:
//
//	FieldInteger -> *int64    FieldFloat   -> *float64
//	FieldString  -> *string   FieldBoolean -> *bool
//
// One list serves both the encoder (reads through the pointers) and the decoder
// (writes through them), so the two can never drift apart.
func (d *WeatherData) fieldPtrs() []any {
	return []any{
		&d.AirDensity,                      //  0 air_density                          float
		&d.AirTemperature,                  //  1 air_temperature                      integer
		&d.Brightness,                      //  2 brightness                           integer
		&d.Conditions,                      //  3 conditions                           string
		&d.DeltaT,                          //  4 delta_t                              integer
		&d.DewPoint,                        //  5 dew_point                            integer
		&d.FeelsLike,                       //  6 feels_like                           integer
		&d.Icon,                            //  7 icon                                 string
		&d.IsPrecipLocalDayRainCheck,       //  8 is_precip_local_day_rain_check       boolean
		&d.IsPrecipLocalYesterdayRainCheck, //  9 is_precip_local_yesterday_rain_check boolean
		&d.LightningStrikeCountLast1hr,     // 10 lightning_strike_count_last_1hr      integer
		&d.LightningStrikeCountLast3hr,     // 11 lightning_strike_count_last_3hr      integer
		&d.LightningStrikeLastDistance,     // 12 lightning_strike_last_distance       integer
		&d.LightningStrikeLastDistanceMsg,  // 13 lightning_strike_last_distance_msg   string
		&d.LightningStrikeLastEpoch,        // 14 lightning_strike_last_epoch          integer
		&d.PrecipAccumLocalDay,             // 15 precip_accum_local_day               integer
		&d.PrecipAccumLocalYesterday,       // 16 precip_accum_local_yesterday         integer
		&d.PrecipMinutesLocalDay,           // 17 precip_minutes_local_day             integer
		&d.PrecipMinutesLocalYesterday,     // 18 precip_minutes_local_yesterday       integer
		&d.PrecipProbability,               // 19 precip_probability                   integer
		&d.PressureTrend,                   // 20 pressure_trend                       string
		&d.RelativeHumidity,                // 21 relative_humidity                    integer
		&d.SeaLevelPressure,                // 22 sea_level_pressure                   integer
		&d.SolarRadiation,                  // 23 solar_radiation                      integer
		&d.StationPressure,                 // 24 station_pressure                     float
		&d.Time,                            // 25 time                                 integer
		&d.UV,                              // 26 uv                                   integer
		&d.WetBulbGlobeTemperature,         // 27 wet_bulb_globe_temperature           integer
		&d.WetBulbTemperature,              // 28 wet_bulb_temperature                 integer
		&d.WindAvg,                         // 29 wind_avg                             integer
		&d.WindDirection,                   // 30 wind_direction                       integer
		&d.WindDirectionCardinal,           // 31 wind_direction_cardinal              string
		&d.WindGust,                        // 32 wind_gust                            integer
	}
}

// fieldPtrsV0 returns the same 33 pointers in FieldSchemaV0 order, for decoding
// a layout-A record.
//
// It is DERIVED from fieldPtrs rather than written out a second time. The two
// orders hold exactly the same 33 fields, so a second literal list would be a
// second place to make a copy-paste mistake — and a mistake here writes a
// decoded value into the wrong struct field, which no byte-parity vector can
// catch. If a name in FieldSchemaV0 is absent from FieldSchema the slot is nil,
// and readField rejects it with ErrSchemaMismatch rather than panicking;
// TestFieldSchemaV0IsAPermutationWithIdenticalTypes makes that unreachable.
func (d *WeatherData) fieldPtrsV0() []any {
	ptrs := d.fieldPtrs()

	byName := make(map[string]any, len(FieldSchema))
	for i, f := range FieldSchema {
		byName[f.Name] = ptrs[i]
	}

	out := make([]any, 0, len(FieldSchemaV0))
	for _, f := range FieldSchemaV0 {
		out = append(out, byName[f.Name])
	}

	return out
}
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofmt -w internal/ && go test ./internal/weather/... -count=1 -v -run 'TestFieldSchema|TestSchemaNames|TestFieldPtrs' 2>&1 | tail -20
```

Expected:

```
=== RUN   TestFieldSchemaOrderAndCount
--- PASS: TestFieldSchemaOrderAndCount (0.00s)
=== RUN   TestFieldSchemaIsStrictlyAlphabetical
--- PASS: TestFieldSchemaIsStrictlyAlphabetical (0.00s)
=== RUN   TestFieldSchemaTypeTally
--- PASS: TestFieldSchemaTypeTally (0.00s)
=== RUN   TestFieldSchemaMatchesVectors
    schema_test.go:266: schema parity OK: 33 fields, air_density .. wind_gust
--- PASS: TestFieldSchemaMatchesVectors (0.00s)
=== RUN   TestSchemaNamesMatchJSONTags
--- PASS: TestSchemaNamesMatchJSONTags (0.00s)
=== RUN   TestFieldPtrsMatchSchemaTypes
--- PASS: TestFieldPtrsMatchSchemaTypes (0.00s)
=== RUN   TestFieldPtrsAreDistinctAndInStructOrder
--- PASS: TestFieldPtrsAreDistinctAndInStructOrder (0.00s)
=== RUN   TestFieldSchemaV0IsAPermutationWithIdenticalTypes
--- PASS: TestFieldSchemaV0IsAPermutationWithIdenticalTypes (0.00s)
=== RUN   TestFieldPtrsV0MatchSchemaV0Types
--- PASS: TestFieldPtrsV0MatchSchemaV0Types (0.00s)
PASS
```

- [ ] **Confirm `FieldSchemaV0` against git, not against this plan.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git show e2ae463^:src/format/schema.ts | grep -o "name: '[a-z0-9_]*'" | sed "s/name: '//;s/'//" > /tmp/v0-from-git.txt && \
  grep -o '{Name: "[a-z0-9_]*"' internal/weather/schema.go | sed 's/{Name: "//;s/"//' | tail -33 > /tmp/v0-from-go.txt && \
  diff -u /tmp/v0-from-git.txt /tmp/v0-from-go.txt && \
  echo "FieldSchemaV0 matches e2ae463^:src/format/schema.ts"
```

Expected: `diff` prints nothing and the last line appears. This is the check that matters: the V0 order is authoritative in git history, not in this document. `tail -33` selects the second `[]FieldDefinition` literal in the file, which is `FieldSchemaV0`.

- [ ] **Annotate ENCODING.md.** Insert this block into `/Users/personal/git/demos/weather-chain/ENCODING.md` immediately after its first top-level heading line, before any other prose. **Do not edit or delete the "Field Order" section itself.** It is not a mistake: it documents the pre-`e2ae463` wire order, and it is the only surviving documentation that layout A ever existed. Deleting it destroys the evidence that `internal/weather/schema.go`'s `FieldSchemaV0` is based on.

```markdown
> **VERSION NOTICE — read this before trusting anything below.** Two statements
> in this document no longer describe the code. Both were checked by running the
> real encoder and by reading git history.
>
> 1. **Field order.** The "Field Order" section below (time-first,
>    category-grouped) documents the **pre-`e2ae463` wire order**, superseded on
>    2026-01-27 by commit `e2ae463` ("update ordering and readme"). It is kept
>    deliberately: records published before that commit are on chain in this
>    order, and `internal/weather/schema.go` carries it as `FieldSchemaV0` so
>    they stay decodable. The **current** wire order is **strict alphabetical**,
>    `air_density` first and `wind_gust` last, defined by
>    `parity/ts/src/format/schema.ts` and mirrored as `FieldSchema`. Proof: the
>    sample script begins `006a51` `0310af13`, and `0310af13` is `air_density`
>    (1.29 × 1e6 = 1290000 = 0x13AF10, little-endian `10 af 13`). Port the
>    current order from `schema.ts`, never from this document — and do not
>    delete the superseded order from this document either.
> 2. **"Example data: 97 bytes".** 97 is the length of the **OP_RETURN payload**.
>    The **locking script** is **99 bytes**. A test asserting 97, or a size cap
>    computed from 97, is 2 bytes short. The neighbouring "Chunks: 34" is
>    likewise the pre-`c44b7ae` count; the current prefixed layout parses to 36.
```

- [ ] **Run the whole suite and the linter.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make go-test && make go-lint && echo "GO GATE CLEAN"
```

Expected: `ok github.com/bsv-blockchain-demos/weather-proof/internal/weather`, no lint output, then `GO GATE CLEAN`.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/schema.go internal/weather/schema_test.go ENCODING.md && \
  git commit -m "weather: the 33-field schema, which IS the wire format

FieldSchema is strict alphabetical order ported from schema.ts. The test
transcribes the 33 names independently rather than deriving them from the
value under test, asserts the order matches the TypeScript oracle's schema
index by index, and pins the 24/2/5/2 type tally.

FieldSchemaV0 is the SUPERSEDED order, and it is here because there are
three on-chain layouts, not two: e2ae463 reordered the schema at 11:11:45
and c44b7ae added the OP_RETURN prefix at 11:14:41 the same day, three
minutes later. Records from before the reorder are prefix-less AND
time-first, share the 34-chunk shape of the prefix-less alphabetical
records, and would otherwise decode to silent garbage with no error --- 33
wrong fields, air_density read from the time chunk. It is transcribed from
git show e2ae463^:src/format/schema.ts and a test diffs it back against
that command. Nothing encodes with it.

fieldPtrs is the schema-to-struct bridge: one ordered list of pointers that
the encoder reads through and the decoder writes through, so the two halves
cannot drift. fieldPtrsV0 is derived from it rather than written twice.

ENCODING.md gets a version notice, not a correction: its field-order
section is the superseded wire format and the only record that layout
existed, so it stays. Its '97 bytes' is the OP_RETURN payload, not the
99-byte script."
```

---

## Task 8: `internal/weather/scriptnum.go` — reproduce `Script.writeBn` exactly

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum_test.go`

**Interfaces:**
- Consumes: `loadVectors` from Task 6.
- Produces:
  - `const MaxSafeInteger int64 = 9007199254740991`
  - `var ErrNumberOutOfRange = errors.New("number outside the JavaScript safe-integer range")`
  - `func scriptNumBytes(n int64) []byte` — sign-magnitude little-endian, empty for zero
  - `func appendScriptNum(s *script.Script, n int64) error` — Tasks 9, 10 and 11 all call it
  - test helpers `func hexOfScriptNum(t *testing.T, n int64) string`, `func parseInt64(t *testing.T, s string) int64`, `func truncate(s string, n int) string` — Tasks 9, 10 and 11 use them
- Imports `github.com/bsv-blockchain/go-sdk/script`; `go mod tidy` becomes safe from this task onward.

**Why this must be hand-rolled.** There is no byte-compatible script-number encoder in the Go SDK, and each near-miss was measured:

- `script.AppendBigInt(bInt big.Int)` is literally `AppendPushData(bInt.Bytes())`: big-endian magnitude, no sign byte, no little-endian reversal, no small-int opcodes. `AppendBigInt(-1290000)` gives `0313af10`; TypeScript gives `0310af93`. Mutually undecodable, not a near-miss.
- `interpreter.ScriptNumber.Bytes()` produces the right *bytes* but `AppendPushData(sn.Bytes())` cannot produce the three single-opcode branches, so it diverges on exactly 17 values — `0` excepted, that is `1..16` and `-1`. `1` gives `0101` where TypeScript gives `51`. Those are the most common values in the dataset (booleans, `uv`, zero counters).
- `interpreter.ScriptNumber.Bytes()` also **mutates its receiver**: two consecutive calls on `-1290000` returned `10af93` then `10af13`.

`(*script.Script).AppendPushData` **is** byte-identical to `@bsv/sdk`'s `writeBin` at every length, including length 0 — verified over 22 length cases — so the data push itself is delegated.

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum_test.go` with exactly this content:

```go
package weather

import (
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// hexOfScriptNum encodes n on a fresh script and returns its lowercase hex.
func hexOfScriptNum(t *testing.T, n int64) string {
	t.Helper()

	s := &script.Script{}
	if err := appendScriptNum(s, n); err != nil {
		t.Fatalf("appendScriptNum(%d): unexpected error %v", n, err)
	}

	return s.String()
}

// parseInt64 fails the test rather than returning an error, because every value
// in the golden file is generated and must parse.
func parseInt64(t *testing.T, s string) int64 {
	t.Helper()

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("golden file holds a non-int64 value %q: %v", s, err)
	}

	return n
}

// truncate shortens a hex string for a readable failure message.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}

func TestMaxSafeIntegerConstant(t *testing.T) {
	if MaxSafeInteger != 9007199254740991 {
		t.Errorf("MaxSafeInteger = %d, want 9007199254740991 (2^53 - 1)", MaxSafeInteger)
	}

	if MaxSafeInteger != (1<<53)-1 {
		t.Errorf("MaxSafeInteger = %d, want (1<<53)-1", MaxSafeInteger)
	}
}

// TestAppendScriptNumVectors drives every writeNumber entry in the golden file,
// including the two entries that must error.
func TestAppendScriptNumVectors(t *testing.T) {
	v := loadVectors(t)

	var okCases, errCases int

	for _, c := range v.WriteNumber {
		n := parseInt64(t, c.Value)

		s := &script.Script{}
		err := appendScriptNum(s, n)

		if c.Error != "" {
			errCases++

			if err == nil {
				t.Errorf("value %d: want an error (TypeScript throws %q), got hex %s", n, c.Error, s.String())

				continue
			}

			if !errors.Is(err, ErrNumberOutOfRange) {
				t.Errorf("value %d: error is %v, want ErrNumberOutOfRange", n, err)
			}

			continue
		}

		if err != nil {
			t.Errorf("value %d: unexpected error %v", n, err)

			continue
		}

		if got := s.String(); got != c.Hex {
			t.Errorf("value %d: got %s, want %s", n, got, c.Hex)

			continue
		}

		okCases++
	}

	if okCases != 38 || errCases != 2 {
		t.Errorf("drove %d hex cases and %d error cases, want 38 and 2", okCases, errCases)
	}

	t.Logf("writeNumber parity OK: %d hex vectors, %d error vectors", okCases, errCases)
}

// TestAppendScriptNumOpcodeBranches pins the three single-opcode branches of
// writeBn, which no helper in the Go SDK reproduces. Getting these wrong still
// decodes, which is why they are asserted explicitly and not only via the table.
func TestAppendScriptNumOpcodeBranches(t *testing.T) {
	cases := []struct {
		n    int64
		want string
		why  string
	}{
		{0, "00", "OP_0"},
		{-1, "4f", "OP_1NEGATE"},
		{1, "51", "OP_1"},
		{2, "52", "OP_2"},
		{15, "5f", "OP_15"},
		{16, "60", "OP_16"},
		{17, "0111", "first value past the opcode range: a 1-byte push"},
		{-2, "0182", "negative values other than -1 are always pushes"},
	}

	for _, c := range cases {
		if got := hexOfScriptNum(t, c.n); got != c.want {
			t.Errorf("appendScriptNum(%d) = %s, want %s (%s)", c.n, got, c.want, c.why)
		}
	}
}

// TestAppendScriptNumSignExtension pins the toSm('little') rule that appends an
// extra byte when the magnitude's top byte already has bit 7 set. Getting it
// wrong changes the byte LENGTH, so these are the boundaries that matter.
func TestAppendScriptNumSignExtension(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{127, "017f"},
		{128, "028000"},
		{-127, "01ff"},
		{-128, "028080"},
		{255, "02ff00"},
		{-255, "02ff80"},
		{256, "020001"},
		{-256, "020081"},
		{32767, "02ff7f"},
		{32768, "03008000"},
		{-32768, "03008080"},
		{2147483647, "04ffffff7f"},
		{2147483648, "050000008000"},
		{-2147483647, "04ffffffff"},
		{-2147483648, "050000008080"},
		{9007199254740991, "07ffffffffffff1f"},
		{-9007199254740991, "07ffffffffffff9f"},
		{1290000, "0310af13"},
		{-1290000, "0310af93"},
		{9007199, "045f708900"},
	}

	for _, c := range cases {
		if got := hexOfScriptNum(t, c.n); got != c.want {
			t.Errorf("appendScriptNum(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

func TestAppendScriptNumRejectsOutOfRange(t *testing.T) {
	cases := []int64{
		9007199254740992,
		-9007199254740992,
		math.MaxInt64,
		math.MinInt64,
	}

	for _, n := range cases {
		s := &script.Script{}

		err := appendScriptNum(s, n)
		if err == nil {
			t.Errorf("appendScriptNum(%d) = nil error, want ErrNumberOutOfRange", n)

			continue
		}

		if !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("appendScriptNum(%d) error = %v, want ErrNumberOutOfRange", n, err)
		}

		if len(*s) != 0 {
			t.Errorf("appendScriptNum(%d) wrote %d bytes on the error path, want 0", n, len(*s))
		}
	}
}

func TestScriptNumBytesZeroIsEmpty(t *testing.T) {
	if got := scriptNumBytes(0); len(got) != 0 {
		t.Errorf("scriptNumBytes(0) = %v, want an empty slice (writeBn never reaches this path for zero)", got)
	}
}

// TestAppendPushDataMatchesWriteBin proves the Go SDK's AppendPushData is
// byte-identical to @bsv/sdk's writeBin at every length boundary, which is why
// this package delegates the data push instead of hand-rolling it.
func TestAppendPushDataMatchesWriteBin(t *testing.T) {
	v := loadVectors(t)

	for _, c := range v.WriteBin {
		payload := make([]byte, c.Len)
		for i := range payload {
			payload[i] = 'A' // the generator fills with 0x41
		}

		s := &script.Script{}
		if err := s.AppendPushData(payload); err != nil {
			t.Fatalf("AppendPushData(%d bytes): %v", c.Len, err)
		}

		if got := s.String(); got != c.Hex {
			t.Errorf("writeBin length %d: got %s..., want %s...",
				c.Len, truncate(got, 12), truncate(c.Hex, 12))
		}
	}

	t.Logf("writeBin parity OK: %d length-boundary vectors", len(v.WriteBin))
}
```

- [ ] **Run it and see it fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -5
```

Expected:

```
internal/weather/scriptnum_test.go:17:16: undefined: appendScriptNum
internal/weather/scriptnum_test.go:48:5: undefined: MaxSafeInteger
internal/weather/scriptnum_test.go:82:23: undefined: ErrNumberOutOfRange
internal/weather/scriptnum_test.go:196:14: undefined: scriptNumBytes
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [build failed]
```

- [ ] **Write the implementation.** Create `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum.go` with exactly this content:

```go
package weather

import (
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
)

// MaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER, 2^53 - 1.
//
// @bsv/sdk routes every Script.writeNumber call through the BigNumber
// constructor, which asserts BigInt(Math.abs(n)) <= (1n << 53n) - 1n and throws
// "The number is larger than 2 ^ 53 (unsafe)" otherwise. Go's int64 would
// silently accept values up to 9.2e18 and emit an 8-byte push the TypeScript
// encoder could never produce, so the limit is enforced here instead.
const MaxSafeInteger int64 = 9007199254740991

// ErrNumberOutOfRange reports a value outside the JavaScript safe-integer range.
var ErrNumberOutOfRange = errors.New("number outside the JavaScript safe-integer range")

// scriptNumBytes reproduces BigNumber.toSm('little') from @bsv/sdk for the
// data-push branch of writeBn.
//
// The algorithm, in order:
//  1. zero produces no bytes (writeBn never reaches this path for zero);
//  2. build the minimal magnitude, little-endian;
//  3. if the most-significant byte already has bit 7 set, APPEND a sign byte
//     (0x80 negative, 0x00 positive) so the sign bit is not misread;
//  4. otherwise, for a negative value, OR 0x80 into the most-significant byte.
//
// Measured against Script.writeNumber over 10,631 integers: 0 mismatches.
//
// Precondition: -MaxSafeInteger <= n <= MaxSafeInteger. appendScriptNum enforces
// it before calling, which is what makes the negation below overflow-free.
func scriptNumBytes(n int64) []byte {
	if n == 0 {
		return []byte{}
	}

	neg := n < 0

	var mag uint64
	if neg {
		// appendScriptNum rejects |n| > MaxSafeInteger before calling, so -n is
		// in [1, 2^53-1] here and the conversion to uint64 is exact. Do NOT add
		// a //nolint:gosec: gosec v2.12.2 does range analysis, does not flag
		// this, and nolintlint runs with allow-unused: false.
		mag = uint64(-n)
	} else {
		// n is in [1, 2^53-1] on this branch, so the conversion is exact.
		mag = uint64(n)
	}

	b := make([]byte, 0, 9) // at most 8 magnitude bytes plus one appended sign byte
	for mag > 0 {
		// The 0xff mask bounds the value to 0..255, so the byte conversion is
		// lossless and needs no suppression.
		b = append(b, byte(mag&0xff))
		mag >>= 8
	}

	top := len(b) - 1
	switch {
	case b[top]&0x80 != 0 && neg:
		b = append(b, 0x80)
	case b[top]&0x80 != 0:
		b = append(b, 0x00)
	case neg:
		b[top] |= 0x80
	}

	return b
}

// appendScriptNum reproduces Script.writeNumber -> writeBn from @bsv/sdk.
//
// writeBn has four branches and the ORDER matters, because three of them emit a
// single opcode that AppendPushData cannot produce:
//
//	n == 0            -> OP_0       (0x00)
//	n == -1           -> OP_1NEGATE (0x4f)
//	1 <= n <= 16      -> OP_1..OP_16 (0x51..0x60)
//	otherwise         -> a minimal data push of scriptNumBytes(n)
//
// Do NOT reach for a helper from the SDK here. script.AppendBigInt is
// big-endian magnitude with no sign byte and no little-endian reversal
// (AppendBigInt(-1290000) gives 0313af10 where TypeScript gives 0310af93), and
// interpreter.ScriptNumber both misses the three opcode branches and mutates its
// own receiver on every Bytes() call for negative values.
func appendScriptNum(s *script.Script, n int64) error {
	if n > MaxSafeInteger || n < -MaxSafeInteger {
		return fmt.Errorf("%w: %d", ErrNumberOutOfRange, n)
	}

	switch {
	case n == 0:
		return s.AppendOpcodes(script.Op0)
	case n == -1:
		return s.AppendOpcodes(script.Op1NEGATE)
	case n >= 1 && n <= 16:
		// This case guards 1 <= n <= 16, so byte(n) is exact and
		// Op1+byte(n)-1 stays within 0x51..0x60.
		return s.AppendOpcodes(script.Op1 + byte(n) - 1)
	default:
		return s.AppendPushData(scriptNumBytes(n))
	}
}
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofmt -w internal/ && go test ./internal/weather/... -count=1 -v -run 'ScriptNum|PushData|MaxSafe' 2>&1 | tail -16
```

Expected:

```
=== RUN   TestMaxSafeIntegerConstant
--- PASS: TestMaxSafeIntegerConstant (0.00s)
=== RUN   TestAppendScriptNumVectors
    scriptnum_test.go:104: writeNumber parity OK: 38 hex vectors, 2 error vectors
--- PASS: TestAppendScriptNumVectors (0.00s)
=== RUN   TestAppendScriptNumOpcodeBranches
--- PASS: TestAppendScriptNumOpcodeBranches (0.00s)
=== RUN   TestAppendScriptNumSignExtension
--- PASS: TestAppendScriptNumSignExtension (0.00s)
=== RUN   TestAppendScriptNumRejectsOutOfRange
--- PASS: TestAppendScriptNumRejectsOutOfRange (0.00s)
=== RUN   TestScriptNumBytesZeroIsEmpty
--- PASS: TestScriptNumBytesZeroIsEmpty (0.00s)
=== RUN   TestAppendPushDataMatchesWriteBin
    scriptnum_test.go:227: writeBin parity OK: 12 length-boundary vectors
--- PASS: TestAppendPushDataMatchesWriteBin (0.00s)
PASS
```

- [ ] **Tidy the module now that the SDK is actually imported.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go mod tidy && git diff --stat go.mod go.sum
```

Expected: `go.sum` gains the transitive test dependencies of the SDK (`davecgh/go-spew`, `pmezard/go-difflib`, `stretchr/testify`, `gopkg.in/yaml.v3`) and `go.mod` keeps `require github.com/bsv-blockchain/go-sdk v1.3.2`. If the `require` line disappears, `scriptnum.go` was not saved.

- [ ] **Run the whole suite and the linter.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make go-test && make go-lint && echo "GO GATE CLEAN"
```

Expected: `GO GATE CLEAN`. There is deliberately **no `//nolint` in `scriptnum.go`**, and adding one breaks the build: `nolintlint` runs with `allow-unused: false`, and gosec v2.12.2 does range analysis, so it does not flag `uint64(-n)`, `uint64(n)`, `byte(mag&0xff)` or `script.Op1 + byte(n) - 1`. A `//nolint:gosec` on any of them is reported as `directive ... is unused for linter "gosec" (nolintlint)` and `make go-lint` exits 2. The reasoning for each conversion stays as an ordinary comment.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/scriptnum.go internal/weather/scriptnum_test.go go.mod go.sum && \
  git commit -m "weather: reproduce @bsv/sdk Script.writeBn byte for byte

There is no byte-compatible script-number encoder in the Go SDK.
AppendBigInt is big-endian magnitude with no sign byte (-1290000 gives
0313af10 where TypeScript gives 0310af93). interpreter.ScriptNumber gets
the bytes right but cannot emit the three single-opcode branches, so it
diverges on exactly 17 values (1..16 and -1, the most common values in the
dataset), and its Bytes() method mutates its own receiver on negatives.

scriptNumBytes plus appendScriptNum was measured at 0 mismatches over
10,631 integers and reproduces all three fixture scripts exactly. Only the
data push is delegated: AppendPushData IS byte-identical to writeBin at
every length, including zero.

The 2^53-1 gate lives here because it lives in the BigNumber constructor on
the TypeScript side; an int64 would silently accept 9.2e18."
```

---

## Task 9: `internal/weather/float.go` — ECMAScript `Math.round`, FMA-safe

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/float.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/float_test.go`

**Interfaces:**
- Consumes: `FloatScale`, `FloatEpsilon` from Task 5b; `MaxSafeInteger`, `ErrNumberOutOfRange`, `appendScriptNum` from Task 8; `loadVectors` from Task 6; `parseInt64` and `hexOfScriptNum` from Task 8's test file.
- Produces:
  - `var ErrNonFinite = errors.New("value is not finite")`
  - `var ErrNonIntegral = errors.New("value is not an integer")`
  - `func jsRound(x float64) float64`
  - `func EncodeFloat(v, scale float64) (int64, error)` — Task 10's encoder calls it as `EncodeFloat(*p, FloatScale)`
  - `func DecodeFloat(scaled int64, scale float64) float64` — Task 11's decoder calls it as `DecodeFloat(n, FloatScale)`
  - `func ValidateFloatPrecision(original, decoded, epsilon float64) bool`
  - `func RequireIntegral(v float64) error`

**This is the single highest-risk function in the port.** ECMAScript `Math.round` rounds a half **toward positive infinity**; Go's `math.Round` rounds a half **away from zero**. They disagree on every exactly-representable negative half, and the divergence is silent — the record still decodes, just to a different number. Measured over a corpus of 8,035 doubles:

| implementation | mismatches against ECMAScript |
|---|---|
| `math.Round(x)` | **2,013 of 8,035 (25%)** |
| `math.Floor(x + 0.5)` | 5 of 8,035 |
| `math.Floor(x)` + fractional compare | **0 of 8,035** |

The `Floor(x+0.5)` shortcut fails because the addition loses precision: `0.49999999999999994` becomes `1` instead of `0`, and `4503599627370497` becomes `4503599627370498`.

**`RequireIntegral` has no caller in this plan** and that is deliberate: `WeatherData`'s integer fields are `int64`, so a non-integral integer is unrepresentable here. It is the check `internal/tempest/mapper.go` needs in a later plan when it coerces a raw JSON number, and it lives here so both halves of the TypeScript `BigNumber` constructor assertion have one implementation. It has its own test, so it is not dead weight.

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/float_test.go` with exactly this content:

```go
package weather

import (
	"errors"
	"math"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// TestEncodeFloatVectors drives every float entry in the golden file end to end:
// EncodeFloat must produce the same scaled integer the TypeScript recorded, and
// appendScriptNum must then produce the same hex.
func TestEncodeFloatVectors(t *testing.T) {
	v := loadVectors(t)

	for _, c := range v.Floats {
		if c.Error != "" {
			t.Errorf("input %v: the golden file records a throw %q, which this table does not expect", c.Input, c.Error)

			continue
		}

		wantScaled := parseInt64(t, c.Scaled)

		got, err := EncodeFloat(c.Input, FloatScale)
		if err != nil {
			t.Errorf("input %v: unexpected error %v", c.Input, err)

			continue
		}

		if got != wantScaled {
			t.Errorf("input %v: EncodeFloat gave %d, TypeScript Math.round gave %d", c.Input, got, wantScaled)

			continue
		}

		s := &script.Script{}
		if err := appendScriptNum(s, got); err != nil {
			t.Errorf("input %v: appendScriptNum(%d): %v", c.Input, got, err)

			continue
		}

		if hexGot := s.String(); hexGot != c.Hex {
			t.Errorf("input %v: got %s, want %s", c.Input, hexGot, c.Hex)
		}
	}

	t.Logf("float parity OK: %d vectors through jsRound + writeNumber", len(v.Floats))
}

// TestJSRoundDivergesFromMathRound pins the four measured cases where Go's
// math.Round would write different bytes to chain. Over a corpus of 8,035
// doubles, math.Round mismatched ECMAScript on 2,013 of them (25%).
func TestJSRoundDivergesFromMathRound(t *testing.T) {
	cases := []struct {
		product   float64 // the value of v * 1e6
		wantJS    float64 // ECMAScript Math.round
		wantGoBad float64 // what math.Round gives, for contrast
	}{
		{-1234567.5, -1234567, -1234568},
		{-0.5, 0, -1},
		{-1.5, -1, -2},
		{-2.5, -2, -3},
	}

	for _, c := range cases {
		if got := jsRound(c.product); got != c.wantJS {
			t.Errorf("jsRound(%v) = %v, want %v (ECMAScript rounds a half toward +Inf)",
				c.product, got, c.wantJS)
		}

		if got := math.Round(c.product); got != c.wantGoBad {
			t.Errorf("sanity check failed: math.Round(%v) = %v, expected the wrong value %v",
				c.product, got, c.wantGoBad)
		}
	}
}

// TestJSRoundBeatsFloorPlusHalf pins the two measured cases where the tempting
// math.Floor(x+0.5) shortcut is wrong because the addition loses precision.
func TestJSRoundBeatsFloorPlusHalf(t *testing.T) {
	cases := []struct {
		x      float64
		wantJS float64
	}{
		{0.49999999999999994, 0},
		{4503599627370497, 4503599627370497},
	}

	for _, c := range cases {
		if got := jsRound(c.x); got != c.wantJS {
			t.Errorf("jsRound(%v) = %v, want %v", c.x, got, c.wantJS)
		}

		if bad := math.Floor(c.x + 0.5); bad == c.wantJS {
			t.Errorf("sanity check failed: math.Floor(%v+0.5) = %v, expected it to be WRONG", c.x, bad)
		}
	}
}

// TestJSRoundNegativeZero documents that jsRound returns +0 where JavaScript
// returns -0, and that it does not matter because both sides encode 0x00.
func TestJSRoundNegativeZero(t *testing.T) {
	got := jsRound(-0.5)
	if got != 0 {
		t.Fatalf("jsRound(-0.5) = %v, want 0", got)
	}

	if int64(got) != 0 {
		t.Fatalf("int64(jsRound(-0.5)) = %d, want 0", int64(got))
	}

	n, err := EncodeFloat(-0.0000005, FloatScale)
	if err != nil {
		t.Fatalf("EncodeFloat(-0.0000005): %v", err)
	}

	if n != 0 {
		t.Fatalf("EncodeFloat(-0.0000005) = %d, want 0 (JavaScript gives -0, which encodes as OP_0)", n)
	}

	if got := hexOfScriptNum(t, n); got != "00" {
		t.Fatalf("-0.0000005 encodes as %s, want 00", got)
	}
}

func TestEncodeFloatRejectsNonFinite(t *testing.T) {
	cases := []struct {
		name string
		v    float64
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}

	for _, c := range cases {
		if _, err := EncodeFloat(c.v, FloatScale); !errors.Is(err, ErrNonFinite) {
			t.Errorf("EncodeFloat(%s) error = %v, want ErrNonFinite: int64(NaN) is implementation-defined in Go and no golden vector can catch it",
				c.name, err)
		}
	}
}

func TestEncodeFloatRejectsOutOfRange(t *testing.T) {
	// 1e15 * 1e6 = 1e21, far past 2^53-1. TypeScript throws here too.
	for _, v := range []float64{1e15, 1e16, -1e15, -1e16} {
		if _, err := EncodeFloat(v, FloatScale); !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("EncodeFloat(%v) error = %v, want ErrNumberOutOfRange", v, err)
		}
	}

	// A product that overflows to infinity is caught one step earlier, by the
	// non-finite guard on the product rather than the range guard on the result.
	if _, err := EncodeFloat(math.MaxFloat64, FloatScale); !errors.Is(err, ErrNonFinite) {
		t.Errorf("EncodeFloat(MaxFloat64) error = %v, want ErrNonFinite", err)
	}

	// 2^53-1 exactly is fine on both sides.
	if _, err := EncodeFloat(9007199254740991, 1); err != nil {
		t.Errorf("EncodeFloat(2^53-1, 1) = %v, want nil: the boundary value itself is legal", err)
	}
}

// TestEncodeDecodeFloatNonDefaultScales covers the two non-default scales the
// TypeScript suite exercises (2 decimals and 9 decimals).
func TestEncodeDecodeFloatNonDefaultScales(t *testing.T) {
	cases := []struct {
		v      float64
		scale  float64
		scaled int64
	}{
		{1.23, 100, 123},
		{1.123456789, 1e9, 1123456789},
		{1.29, FloatScale, 1290000},
		{979.7, FloatScale, 979700000},
		{0.5, FloatScale, 500000},
		{-1.29, FloatScale, -1290000},
		{-979.7, FloatScale, -979700000},
		{0.000001, FloatScale, 1},
		{12345.678901, FloatScale, 12345678901},
	}

	for _, c := range cases {
		got, err := EncodeFloat(c.v, c.scale)
		if err != nil {
			t.Errorf("EncodeFloat(%v, %v): %v", c.v, c.scale, err)

			continue
		}

		if got != c.scaled {
			t.Errorf("EncodeFloat(%v, %v) = %d, want %d", c.v, c.scale, got, c.scaled)

			continue
		}

		if back := DecodeFloat(c.scaled, c.scale); back != c.v {
			// Exact equality holds for every case in this table because each
			// scaled/scale quotient rounds back to the same double.
			t.Errorf("DecodeFloat(%d, %v) = %v, want exactly %v", c.scaled, c.scale, back, c.v)
		}
	}
}

// TestEncodeFloatRoundingDirection pins the two cases the TypeScript suite calls
// out by name: 1.2345674 rounds down, 1.2345675 rounds up. These do NOT survive
// an exact round trip, so only the scaled integer is asserted.
func TestEncodeFloatRoundingDirection(t *testing.T) {
	cases := []struct {
		v      float64
		scaled int64
	}{
		{1.2345674, 1234567},
		{1.2345675, 1234568},
		{-1.2345675, -1234567},
	}

	for _, c := range cases {
		got, err := EncodeFloat(c.v, FloatScale)
		if err != nil {
			t.Errorf("EncodeFloat(%v): %v", c.v, err)

			continue
		}

		if got != c.scaled {
			t.Errorf("EncodeFloat(%v) = %d, want %d", c.v, got, c.scaled)
		}
	}
}

// TestDecodeFloatPrecisionLoss pins the documented lossy case: nine decimal
// places do not survive a six-decimal scale.
func TestDecodeFloatPrecisionLoss(t *testing.T) {
	const original = 1.123456789

	scaled, err := EncodeFloat(original, FloatScale)
	if err != nil {
		t.Fatalf("EncodeFloat: %v", err)
	}

	if scaled != 1123457 {
		t.Fatalf("EncodeFloat(%v) = %d, want 1123457", original, scaled)
	}

	decoded := DecodeFloat(scaled, FloatScale)
	if decoded == original {
		t.Fatalf("DecodeFloat gave back %v exactly; precision loss was expected", decoded)
	}

	if !ValidateFloatPrecision(original, decoded, FloatEpsilon) {
		t.Fatalf("ValidateFloatPrecision(%v, %v, %v) = false, want true", original, decoded, FloatEpsilon)
	}
}

func TestValidateFloatPrecision(t *testing.T) {
	cases := []struct {
		original, decoded float64
		want              bool
	}{
		{1.29, 1.29, true},
		{1.29, 1.2900001, true},
		{1.29, 1.2899999, true},
		{1.29, 1.30, false},
		{1.29, 1.28, false},
		{-1.29, -1.29, true},
		{-1.29, -1.2900001, true},
		{-1.29, -1.30, false},
		{0, 0, true},
		{0, 0.0000001, true},
		// The comparison is strictly less than, so a difference of exactly
		// epsilon is NOT within tolerance. This mirrors the TypeScript.
		{0, FloatEpsilon, false},
	}

	for _, c := range cases {
		if got := ValidateFloatPrecision(c.original, c.decoded, FloatEpsilon); got != c.want {
			t.Errorf("ValidateFloatPrecision(%v, %v, %v) = %v, want %v",
				c.original, c.decoded, FloatEpsilon, got, c.want)
		}
	}
}

func TestRequireIntegral(t *testing.T) {
	if err := RequireIntegral(42); err != nil {
		t.Errorf("RequireIntegral(42) = %v, want nil", err)
	}

	if err := RequireIntegral(-42); err != nil {
		t.Errorf("RequireIntegral(-42) = %v, want nil", err)
	}

	if err := RequireIntegral(0); err != nil {
		t.Errorf("RequireIntegral(0) = %v, want nil", err)
	}

	if err := RequireIntegral(1.5); !errors.Is(err, ErrNonIntegral) {
		t.Errorf("RequireIntegral(1.5) = %v, want ErrNonIntegral", err)
	}

	if err := RequireIntegral(math.NaN()); !errors.Is(err, ErrNonFinite) {
		t.Errorf("RequireIntegral(NaN) = %v, want ErrNonFinite", err)
	}

	if err := RequireIntegral(math.Inf(1)); !errors.Is(err, ErrNonFinite) {
		t.Errorf("RequireIntegral(+Inf) = %v, want ErrNonFinite", err)
	}
}
```

- [ ] **Run it and see it fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -6
```

Expected:

```
internal/weather/float_test.go:27:14: undefined: EncodeFloat
internal/weather/float_test.go:64:20: undefined: jsRound
internal/weather/float_test.go:143:33: undefined: ErrNonFinite
internal/weather/float_test.go:206:15: undefined: DecodeFloat
internal/weather/float_test.go:253:7: undefined: ValidateFloatPrecision
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [build failed]
```

- [ ] **Write the implementation.** Create `/Users/personal/git/demos/weather-chain/internal/weather/float.go` with exactly this content:

```go
package weather

import (
	"errors"
	"fmt"
	"math"
)

var (
	// ErrNonFinite reports a NaN or infinite input. TypeScript throws on this
	// path (Math.round(NaN) is NaN, and BigInt(NaN) raises a RangeError). Go's
	// int64(NaN) is implementation-defined, so one junk reading would otherwise
	// commit arbitrary bytes to chain with no error and no golden vector able to
	// catch it.
	ErrNonFinite = errors.New("value is not finite")

	// ErrNonIntegral reports a non-integral value where an integer is required.
	// It mirrors the second BigNumber constructor assertion, "Number must be an
	// integer for BigNumber conversion". WeatherData's integer fields are int64
	// so this cannot arise inside this package; RequireIntegral exists for
	// internal/tempest/mapper.go, which validates raw API numbers.
	ErrNonIntegral = errors.New("value is not an integer")
)

// jsRound reproduces ECMAScript Math.round, which rounds a half TOWARD
// POSITIVE INFINITY.
//
// Go's math.Round rounds a half AWAY FROM ZERO and therefore disagrees on every
// exactly representable negative half: measured over a corpus of 8,035 doubles,
// math.Round produced 2,013 mismatches (25%). math.Floor(x+0.5) is also wrong,
// because the addition loses precision: it produced 5 mismatches on the same
// corpus (0.49999999999999994 rounds to 1 instead of 0, and 4503599627370497
// becomes 4503599627370498). This form produced 0 mismatches.
//
// It is correct because math.Floor is exact and the comparison x-f >= 0.5 is
// exact: for |x| >= 2^52 the value is already integral, so x-f is exactly 0.
//
// It returns +0 where JavaScript returns -0, for example at x = -0.5. That is
// harmless: int64(+0) and int64(-0) are both 0, and TypeScript's own
// new BigNumber(-0) takes the sign = 0 path because -0 < 0 is false, so both
// sides encode 0x00. Never branch on math.Signbit of the result.
func jsRound(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}

	f := math.Floor(x)
	if x-f >= 0.5 {
		return f + 1
	}

	return f
}

// EncodeFloat scales v by scale and rounds with ECMAScript semantics, mirroring
// encodeFloat in parity/ts/src/utils/float-encoder.ts, whose body is exactly
// Math.round(value * scale).
func EncodeFloat(v, scale float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: value %v", ErrNonFinite, v)
	}

	if math.IsNaN(scale) || math.IsInf(scale, 0) {
		return 0, fmt.Errorf("%w: scale %v", ErrNonFinite, scale)
	}

	// The explicit float64 conversion below is deliberate and load-bearing. Go
	// permits the compiler to fuse v*scale into a single FMA instruction on
	// arm64, ppc64 and s390x; an FMA keeps extra precision in the product, which
	// can move it across a .5 boundary and change the rounded result, and
	// therefore the bytes written on chain. The Go specification guarantees that
	// an explicit floating-point conversion rounds to the target precision,
	// which forbids the fusion. DO NOT DELETE THE CONVERSION.
	//
	// It needs no //nolint: unconvert ignores float conversions by default, so a
	// //nolint:unconvert here would itself fail nolintlint (allow-unused: false).
	prod := float64(v * scale)

	if math.IsNaN(prod) || math.IsInf(prod, 0) {
		return 0, fmt.Errorf("%w: %v * %v produced %v", ErrNonFinite, v, scale, prod)
	}

	r := jsRound(prod)
	if r > float64(MaxSafeInteger) || r < -float64(MaxSafeInteger) {
		return 0, fmt.Errorf("%w: %v * %v rounds to %v", ErrNumberOutOfRange, v, scale, r)
	}

	return int64(r), nil
}

// DecodeFloat is the inverse of EncodeFloat, mirroring decodeFloat in
// parity/ts/src/utils/float-encoder.ts (scaled / scale).
func DecodeFloat(scaled int64, scale float64) float64 {
	return float64(scaled) / scale
}

// ValidateFloatPrecision reports whether decoded is within epsilon of original,
// mirroring validateFloatPrecision in parity/ts/src/utils/float-encoder.ts. The
// comparison is strictly less than, exactly as the TypeScript is.
func ValidateFloatPrecision(original, decoded, epsilon float64) bool {
	return math.Abs(original-decoded) < epsilon
}

// RequireIntegral reports ErrNonFinite for NaN or infinity and ErrNonIntegral
// for a finite value with a fractional part.
//
// Nothing in this package calls it: WeatherData's integer fields are int64, so a
// non-integral integer is unrepresentable. It is the check
// internal/tempest/mapper.go needs when it coerces a raw JSON number that must
// land in an integer field, and it lives here so that both halves of the
// TypeScript BigNumber assertion have one implementation.
func RequireIntegral(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("%w: value %v", ErrNonFinite, v)
	}

	if v != math.Trunc(v) {
		return fmt.Errorf("%w: value %v", ErrNonIntegral, v)
	}

	return nil
}
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofmt -w internal/ && go test ./internal/weather/... -count=1 -v -run 'Float|JSRound|Integral' 2>&1 | tail -24
```

Expected: eleven `--- PASS` lines including `float parity OK: 35 vectors through jsRound + writeNumber`, ending in `PASS`.

- [ ] **Prove the FMA hazard is actually covered on a second architecture.** Run the suite for `amd64` as well as the native architecture:

```bash
cd /Users/personal/git/demos/weather-chain && GOARCH=amd64 go test ./internal/weather/... -count=1 2>&1 | tail -2
```

Expected: `ok github.com/bsv-blockchain-demos/weather-proof/internal/weather`. On an Apple Silicon machine this runs the `amd64` test binary under Rosetta 2 and takes a few seconds longer. FMA contraction is the class of bug that passes on one architecture and fails on another, so this is not optional.

- [ ] **Run the whole suite and the linter.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make go-test && make go-lint && echo "GO GATE CLEAN"
```

Expected: `GO GATE CLEAN`. Note what is **not** needed here: `unconvert` ignores floating-point conversions by default, so `float64(v * scale)` is not reported and must NOT carry a `//nolint:unconvert` — with `allow-unused: false` an unnecessary directive is itself a lint failure. What is load-bearing is the **conversion**, not any comment: deleting `float64(...)` lets the compiler contract the multiply into an FMA on arm64 and changes the bytes written on chain. A comment cannot affect code generation; the conversion can.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/float.go internal/weather/float_test.go && \
  git commit -m "weather: ECMAScript Math.round semantics for the float fields

ECMAScript rounds a half toward +Infinity; Go's math.Round rounds away from
zero. Over a corpus of 8,035 doubles math.Round mismatched on 2,013 (25%),
and every mismatch is silent: the record still decodes, to a different
number. math.Floor(x+0.5) is also wrong (5 mismatches) because the addition
loses precision. The Floor-plus-fractional-compare form measured 0.

EncodeFloat forces a rounded float64 intermediate with an explicit
conversion, which the Go spec guarantees blocks FMA contraction. Without it
arm64 may fuse the multiply and change the rounded result. The suite is run
for both arm64 and amd64 for exactly that reason.

Non-finite input is rejected with a typed error before rounding, because
int64(NaN) is implementation-defined in Go and no golden vector can catch
arbitrary bytes going on chain."
```

---

## Task 10: `internal/weather/encoder.go` — the three fixtures, byte-exact

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/encoder.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/encoder_test.go`

**Interfaces:**
- Consumes: `WeatherData`, `FieldDefinition`, `FieldType` and the four `Field*` constants, `Version`, `FloatScale`, `DataFieldsPerRecord` from Task 5b; `FieldSchema` and `(*WeatherData).fieldPtrs()` from Task 7; `appendScriptNum` and `ErrNumberOutOfRange` from Task 8; `EncodeFloat` and `ErrNonFinite` from Task 9; `loadVectors` from Task 6; `truncate` from Task 8's test file.
- Produces:
  - `const MaxPushDataLen = 65535`
  - `var ErrStringTooLong = errors.New("string exceeds the maximum pushdata length")`
  - `var ErrSchemaMismatch = errors.New("schema and WeatherData field list disagree")` — Task 11's decoder also returns it
  - `func Encode(d *WeatherData) (*script.Script, error)`
  - `func EncodeHex(d *WeatherData) (string, error)`
  - `func appendField(s *script.Script, f FieldDefinition, ptr any) error`
  - test helper `func recordFromVector(t *testing.T, raw json.RawMessage) *WeatherData` — Task 11's tests use it
  - test constant `const maxScriptBytesForTest = 297`

**Two deliberate divergences from the TypeScript, both documented in code so nobody "fixes" them into a parity break:**

1. `Encode` takes `*WeatherData`, where the spec writes `Encode(WeatherData)`. A pointer is required because `fieldPtrs` must return pointers into the caller's storage for the decoder to write through the same list. One list, two directions.
2. Above 65535 bytes this returns `ErrStringTooLong` where `@bsv/sdk`'s `writeBin` would emit `OP_PUSHDATA4`. Unreachable in practice under the publisher's script-size cap, but it is a real divergence.

**The script-size cap is 297 bytes** (`WEATHER_MAX_SCRIPT_BYTES`), the top of the contiguous one-claim window at a 50-satoshi denomination. It is duplicated as a test constant here rather than exported, because the running value is a validated config knob owned by `internal/config` in a later plan; the publisher's pre-`CreateAction` rejection of an oversized record belongs to that plan too. What belongs here is proving the repository's own worst fixture leaves headroom: 211 bytes, 86 bytes clear.

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/encoder_test.go` with exactly this content:

```go
package weather

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// maxScriptBytesForTest is WEATHER_MAX_SCRIPT_BYTES, the publisher's cap: the top
// of the contiguous one-claim script-size window at a 50-satoshi denomination.
// It is duplicated here rather than exported, because the running value is a
// validated config knob owned by internal/config in a later plan. This test only
// asserts the encoder's own output fits under it.
const maxScriptBytesForTest = 297

// recordFromVector unmarshals a golden record's `data` object into WeatherData
// and fails if the JSON carries a key the struct does not have, or is missing one
// the schema requires.
func recordFromVector(t *testing.T, raw json.RawMessage) *WeatherData {
	t.Helper()

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("record data is not a JSON object: %v", err)
	}

	if len(keys) != DataFieldsPerRecord {
		t.Fatalf("record data has %d keys, want %d", len(keys), DataFieldsPerRecord)
	}

	for _, f := range FieldSchema {
		if _, ok := keys[f.Name]; !ok {
			t.Fatalf("record data is missing schema field %q", f.Name)
		}
	}

	var d WeatherData

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&d); err != nil {
		t.Fatalf("record data does not fit WeatherData: %v", err)
	}

	return &d
}

// TestEncodeRecordVectorsAreByteExact is the headline gate: the Go encoder must
// reproduce the TypeScript bytes exactly for all three fixtures. This is asserted
// byte-for-byte, NOT by round-tripping: a symmetric encoder and decoder error
// round-trips perfectly while writing the wrong bytes on chain.
func TestEncodeRecordVectorsAreByteExact(t *testing.T) {
	v := loadVectors(t)

	for _, r := range v.Records {
		d := recordFromVector(t, r.Data)

		s, err := Encode(d)
		if err != nil {
			t.Errorf("record %s: Encode: %v", r.Name, err)

			continue
		}

		got := s.String()
		if got != r.Hex {
			t.Errorf("record %s BYTE MISMATCH\n got: %s\nwant: %s", r.Name, got, r.Hex)

			continue
		}

		if len(*s) != r.Bytes {
			t.Errorf("record %s: script is %d bytes, want %d", r.Name, len(*s), r.Bytes)
		}

		hexStr, err := EncodeHex(d)
		if err != nil {
			t.Errorf("record %s: EncodeHex: %v", r.Name, err)

			continue
		}

		if hexStr != r.Hex {
			t.Errorf("record %s: EncodeHex disagrees with Encode().String()", r.Name)
		}

		ops, err := script.DecodeScript(*s, script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Errorf("record %s: DecodeScript: %v", r.Name, err)

			continue
		}

		if len(ops) != r.Chunks {
			t.Errorf("record %s: %d chunks, want %d", r.Name, len(ops), r.Chunks)
		}

		t.Logf("record %-8s BYTE-EXACT  bytes=%d chunks=%d", r.Name, len(*s), len(ops))
	}
}

// TestEncodeVersionAndPrefixAreOpcodes pins the three-byte header. The prefix is
// two opcodes and the version is a third opcode, never a data push.
func TestEncodeVersionAndPrefixAreOpcodes(t *testing.T) {
	s, err := Encode(&WeatherData{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	raw := *s
	if len(raw) < 3 {
		t.Fatalf("script is %d bytes, want at least 3", len(raw))
	}

	if raw[0] != 0x00 || raw[1] != 0x6a || raw[2] != 0x51 {
		t.Fatalf("header = %02x %02x %02x, want 00 6a 51 (OP_FALSE OP_RETURN OP_1)", raw[0], raw[1], raw[2])
	}

	// The all-zero record is 3 header bytes plus 33 single-byte fields.
	if len(raw) != 36 {
		t.Fatalf("all-zero record is %d bytes, want 36", len(raw))
	}
}

// TestEncodeStringVectors drives the 21 string vectors through the encoder's own
// string path by putting each one in the `conditions` field, so the assertion
// covers the real call site and not just AppendPushData in isolation.
func TestEncodeStringVectors(t *testing.T) {
	v := loadVectors(t)

	for _, c := range v.Strings {
		if got := len([]byte(c.Str)); got != c.UTF8Len {
			t.Errorf("Go UTF-8 length of %q is %d, TypeScript recorded %d",
				truncate(c.Str, 24), got, c.UTF8Len)
		}

		d := &WeatherData{Conditions: c.Str}

		s, err := Encode(d)
		if err != nil {
			t.Errorf("utf8Len %d: Encode: %v", c.UTF8Len, err)

			continue
		}

		// conditions is schema index 3, so its push starts after the 3 header
		// bytes plus 3 single-byte fields (air_density, air_temperature,
		// brightness all encode as one byte when zero).
		full := s.String()

		const prefixHex = "006a51" + "00" + "00" + "00"
		if !strings.HasPrefix(full, prefixHex) {
			t.Fatalf("utf8Len %d: unexpected leading bytes %s", c.UTF8Len, truncate(full, 12))
		}

		fieldHex := full[len(prefixHex) : len(prefixHex)+len(c.Hex)]
		if fieldHex != c.Hex {
			t.Errorf("string (utf8Len %d) encoded as %s..., want %s...",
				c.UTF8Len, truncate(fieldHex, 16), truncate(c.Hex, 16))
		}
	}

	t.Logf("string parity OK: %d vectors through the encoder's string path", len(v.Strings))
}

// TestEncodeNoFieldEmitsOpReturnOpcode is spec test 2. No field may emit 0x6a in
// an opcode position: data pushes cap at OP_PUSHDATA4 (0x4e) and script numbers
// cap at OP_16 (0x60), all below OP_RETURN. If a field could emit 0x6a, a decoder
// scanning for the marker would mis-split the record.
func TestEncodeNoFieldEmitsOpReturnOpcode(t *testing.T) {
	v := loadVectors(t)

	seen := map[byte]bool{}

	record := func(hexPairs string) {
		if len(hexPairs) < 2 {
			return
		}

		b, err := hex.DecodeString(hexPairs[:2])
		if err != nil {
			t.Fatalf("bad hex %q: %v", hexPairs[:2], err)
		}

		seen[b[0]] = true
	}

	for _, c := range v.WriteNumber {
		if c.Hex != "" {
			record(c.Hex)
		}
	}

	for _, c := range v.WriteBin {
		record(c.Hex)
	}

	for _, c := range v.Floats {
		if c.Hex != "" {
			record(c.Hex)
		}
	}

	for _, c := range v.Strings {
		record(c.Hex)
	}

	if seen[0x6a] {
		t.Fatal("a field emitted 0x6a in an opcode position: the OP_RETURN marker is no longer unambiguous")
	}

	var maxOp byte
	for op := range seen {
		if op > maxOp {
			maxOp = op
		}
	}

	if maxOp >= 0x6a {
		t.Fatalf("highest leading opcode is 0x%02x, want below 0x6a", maxOp)
	}

	t.Logf("opcode invariant OK: %d distinct leading opcodes, highest 0x%02x", len(seen), maxOp)
}

// TestEncodeRecordsFitTheScriptCap is the encoder half of spec test 3. The
// publisher's pre-CreateAction rejection of an oversized record belongs to the
// publisher plan; what belongs here is that the repository's own worst fixture
// leaves headroom.
func TestEncodeRecordsFitTheScriptCap(t *testing.T) {
	v := loadVectors(t)

	for _, r := range v.Records {
		d := recordFromVector(t, r.Data)

		s, err := Encode(d)
		if err != nil {
			t.Fatalf("record %s: %v", r.Name, err)
		}

		if len(*s) > maxScriptBytesForTest {
			t.Errorf("record %s is %d bytes, over the %d-byte cap", r.Name, len(*s), maxScriptBytesForTest)
		}
	}

	// The extreme fixture is the documented worst case at 211 bytes, leaving
	// 86 bytes of headroom under the 297-byte cap.
	extreme := 0

	for _, r := range v.Records {
		if r.Name == "extreme" {
			extreme = r.Bytes
		}
	}

	if extreme != 211 {
		t.Fatalf("the extreme fixture is %d bytes, want 211", extreme)
	}

	if headroom := maxScriptBytesForTest - extreme; headroom != 86 {
		t.Fatalf("headroom is %d bytes, want 86", headroom)
	}
}

func TestEncodeRejectsNilRecord(t *testing.T) {
	if _, err := Encode(nil); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("Encode(nil) error = %v, want ErrSchemaMismatch", err)
	}

	if _, err := EncodeHex(nil); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("EncodeHex(nil) error = %v, want ErrSchemaMismatch", err)
	}
}

func TestEncodeRejectsNonFiniteFloatField(t *testing.T) {
	cases := []struct {
		name string
		d    *WeatherData
	}{
		{"air_density NaN", &WeatherData{AirDensity: math.NaN()}},
		{"air_density +Inf", &WeatherData{AirDensity: math.Inf(1)}},
		{"station_pressure -Inf", &WeatherData{StationPressure: math.Inf(-1)}},
	}

	for _, c := range cases {
		_, err := Encode(c.d)
		if !errors.Is(err, ErrNonFinite) {
			t.Errorf("%s: Encode error = %v, want ErrNonFinite", c.name, err)
		}
	}
}

func TestEncodeRejectsOutOfRangeIntegerField(t *testing.T) {
	d := &WeatherData{Time: MaxSafeInteger + 1}

	_, err := Encode(d)
	if !errors.Is(err, ErrNumberOutOfRange) {
		t.Errorf("Encode with time = 2^53 error = %v, want ErrNumberOutOfRange", err)
	}

	if !strings.Contains(err.Error(), "time") {
		t.Errorf("error %q does not name the offending field", err)
	}

	// The boundary value itself is legal on both sides.
	if _, err := Encode(&WeatherData{Time: MaxSafeInteger}); err != nil {
		t.Errorf("Encode with time = 2^53-1: %v, want nil", err)
	}
}

func TestEncodeRejectsOverlongString(t *testing.T) {
	d := &WeatherData{Conditions: strings.Repeat("A", MaxPushDataLen+1)}

	_, err := Encode(d)
	if !errors.Is(err, ErrStringTooLong) {
		t.Errorf("Encode with a 65536-byte string error = %v, want ErrStringTooLong", err)
	}

	// One byte under the limit still encodes, using OP_PUSHDATA2.
	if _, err := Encode(&WeatherData{Conditions: strings.Repeat("A", MaxPushDataLen)}); err != nil {
		t.Errorf("Encode with a 65535-byte string: %v, want nil", err)
	}
}
```

- [ ] **Run it and see it fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -6
```

Expected:

```
internal/weather/encoder_test.go:67:13: undefined: Encode
internal/weather/encoder_test.go:87:20: undefined: EncodeHex
internal/weather/encoder_test.go:279:31: undefined: ErrSchemaMismatch
internal/weather/encoder_test.go:329:57: undefined: MaxPushDataLen
internal/weather/encoder_test.go:332:24: undefined: ErrStringTooLong
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [build failed]
```

- [ ] **Write the implementation.** Create `/Users/personal/git/demos/weather-chain/internal/weather/encoder.go` with exactly this content:

```go
package weather

import (
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
)

// MaxPushDataLen is the largest string this encoder will push.
//
// @bsv/sdk's writeBin would switch to OP_PUSHDATA4 above this length. This
// package returns ErrStringTooLong instead: it is a deliberate, documented
// divergence from the TypeScript, unreachable in practice because the publisher
// caps a weather script far below 64 KiB. Do not "fix" it into a PUSHDATA4
// branch; that would be a parity break with no vector to catch it.
const MaxPushDataLen = 65535

var (
	// ErrStringTooLong reports a string longer than MaxPushDataLen bytes.
	ErrStringTooLong = errors.New("string exceeds the maximum pushdata length")

	// ErrSchemaMismatch reports that FieldSchema and WeatherData.fieldPtrs
	// disagree, which is a programming error in this package rather than bad
	// input.
	ErrSchemaMismatch = errors.New("schema and WeatherData field list disagree")
)

// Encode builds the locking script for one weather record.
//
// Layout, and it is frozen:
//
//	OP_FALSE OP_RETURN   00 6a   two opcodes, added by commit c44b7ae
//	version              51      OP_1, an OPCODE and not a data push
//	33 fields            ...     FieldSchema order, strict alphabetical
//
// Measured script sizes for the three repository fixtures: 36 bytes for the
// all-zero minimal record, 99 for the real Tempest sample, 211 for the extreme
// record. The OP_FALSE OP_RETURN prefix makes the output provably unspendable,
// so a weather output never adds a UTXO to any basket.
//
// The parameter is a pointer because fieldPtrs must hand out pointers into the
// caller's storage: one ordered list serves both this function, which reads
// through it, and Decode, which writes through it.
func Encode(d *WeatherData) (*script.Script, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: nil WeatherData", ErrSchemaMismatch)
	}

	s := &script.Script{}
	if err := s.AppendOpcodes(script.OpFALSE, script.OpRETURN); err != nil {
		return nil, fmt.Errorf("append OP_FALSE OP_RETURN: %w", err)
	}

	if err := appendScriptNum(s, Version); err != nil {
		return nil, fmt.Errorf("append version: %w", err)
	}

	ptrs := d.fieldPtrs()
	if len(ptrs) != len(FieldSchema) {
		return nil, fmt.Errorf("%w: %d field pointers for %d schema fields",
			ErrSchemaMismatch, len(ptrs), len(FieldSchema))
	}

	for i, f := range FieldSchema {
		if err := appendField(s, f, ptrs[i]); err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i, f.Name, err)
		}
	}

	return s, nil
}

// EncodeHex is Encode followed by lowercase hex serialization, mirroring the
// TypeScript encodeToHex.
func EncodeHex(d *WeatherData) (string, error) {
	s, err := Encode(d)
	if err != nil {
		return "", err
	}

	return s.String(), nil
}

// appendField emits one schema field. ptr must be the pointer fieldPtrs returned
// for the same index; its concrete type is checked against f.Type so a schema
// edit that forgets fieldPtrs fails loudly instead of writing wrong bytes.
func appendField(s *script.Script, f FieldDefinition, ptr any) error {
	switch f.Type {
	case FieldInteger:
		p, ok := ptr.(*int64)
		if !ok {
			return fmt.Errorf("%w: integer field is %T, want *int64", ErrSchemaMismatch, ptr)
		}

		return appendScriptNum(s, *p)

	case FieldFloat:
		p, ok := ptr.(*float64)
		if !ok {
			return fmt.Errorf("%w: float field is %T, want *float64", ErrSchemaMismatch, ptr)
		}

		n, err := EncodeFloat(*p, FloatScale)
		if err != nil {
			return err
		}

		return appendScriptNum(s, n)

	case FieldString:
		p, ok := ptr.(*string)
		if !ok {
			return fmt.Errorf("%w: string field is %T, want *string", ErrSchemaMismatch, ptr)
		}

		b := []byte(*p) // Go strings are already UTF-8, matching Buffer.from(v, 'utf8')
		if len(b) > MaxPushDataLen {
			return fmt.Errorf("%w: %d bytes exceeds %d", ErrStringTooLong, len(b), MaxPushDataLen)
		}

		return s.AppendPushData(b)

	case FieldBoolean:
		p, ok := ptr.(*bool)
		if !ok {
			return fmt.Errorf("%w: boolean field is %T, want *bool", ErrSchemaMismatch, ptr)
		}

		n := int64(0)
		if *p {
			n = 1
		}

		return appendScriptNum(s, n)

	default:
		return fmt.Errorf("%w: unknown field type %d", ErrSchemaMismatch, f.Type)
	}
}
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofmt -w internal/ && go test ./internal/weather/... -count=1 -v -run 'TestEncodeRecord|TestEncodeVersion|TestEncodeString|TestEncodeNoField|TestEncodeRejects' 2>&1 | tail -24
```

Expected, including the three headline lines:

```
=== RUN   TestEncodeRecordVectorsAreByteExact
    encoder_test.go:105: record minimal  BYTE-EXACT  bytes=36 chunks=36
    encoder_test.go:105: record sample   BYTE-EXACT  bytes=99 chunks=36
    encoder_test.go:105: record extreme  BYTE-EXACT  bytes=211 chunks=36
--- PASS: TestEncodeRecordVectorsAreByteExact (0.00s)
=== RUN   TestEncodeVersionAndPrefixAreOpcodes
--- PASS: TestEncodeVersionAndPrefixAreOpcodes (0.00s)
=== RUN   TestEncodeStringVectors
    encoder_test.go:170: string parity OK: 21 vectors through the encoder's string path
--- PASS: TestEncodeStringVectors (0.00s)
=== RUN   TestEncodeNoFieldEmitsOpReturnOpcode
    encoder_test.go:230: opcode invariant OK: 27 distinct leading opcodes, highest 0x60
--- PASS: TestEncodeNoFieldEmitsOpReturnOpcode (0.00s)
=== RUN   TestEncodeRejectsNilRecord
--- PASS: TestEncodeRejectsNilRecord (0.00s)
=== RUN   TestEncodeRejectsNonFiniteFloatField
--- PASS: TestEncodeRejectsNonFiniteFloatField (0.00s)
=== RUN   TestEncodeRejectsOutOfRangeIntegerField
--- PASS: TestEncodeRejectsOutOfRangeIntegerField (0.00s)
=== RUN   TestEncodeRejectsOverlongString
--- PASS: TestEncodeRejectsOverlongString (0.00s)
PASS
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.3s
```

If a record mismatches, diff the two hex strings against the per-field breakdown in this plan's "Measured reference values" section to find which field diverged.

- [ ] **Run the whole suite on both architectures and lint.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  make go-test && GOARCH=amd64 go test ./internal/weather/... -count=1 && make go-lint && echo "GO GATE CLEAN"
```

Expected: two `ok` lines then `GO GATE CLEAN`. The `switch f.Type` in `appendField` enumerates all four `FieldType` values and still has a `default`; `exhaustive` is configured with `default-signifies-exhaustive: false`, so all four cases must be listed explicitly — do not collapse them.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/encoder.go internal/weather/encoder_test.go && \
  git commit -m "weather: encode records byte-identically to the TypeScript

All three repository fixtures reproduce exactly: 36 bytes for the all-zero
record, 99 for the real Tempest sample, 211 for the extreme record. The
assertion is a hex comparison, not a round trip, because a symmetric
encoder-and-decoder error round-trips perfectly while writing the wrong
bytes on chain.

Also asserted: the 21 string vectors through the encoder's own string path
(Go strings are already UTF-8, so Buffer.from(v,'utf8') needs no
translation), the invariant that no field can emit 0x6a in an opcode
position (the highest is OP_16 at 0x60), that the 211-byte worst case
leaves 86 bytes under the 297-byte cap, and typed errors for nil records,
non-finite floats, out-of-range integers and overlong strings.

Above 65535 bytes this returns ErrStringTooLong where the TypeScript would
emit OP_PUSHDATA4. That divergence is deliberate and documented in code."
```

---

## Task 11: `internal/weather/decoder.go` — all three on-chain layouts, losslessly

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/decoder.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/decoder_test.go`

**Interfaces:**
- Consumes: `WeatherData`, `FieldDefinition`, `FieldType` and the four `Field*` constants, `Version`, `FloatScale`, `ChunksPrefixed`, `ChunksLegacy`, `DataFieldsPerRecord` from Task 5b; `FieldSchema`, `FieldSchemaV0`, `fieldPtrs` and `fieldPtrsV0` from Task 7; `MaxSafeInteger` and `ErrNumberOutOfRange` from Task 8; `DecodeFloat` from Task 9; `ErrSchemaMismatch`, `Encode` and `EncodeHex` from Task 10; `loadVectors` from Task 6; `recordFromVector` from Task 10's test file; `truncate` from Task 8's test file.
- Produces:
  - `var ErrUnsupportedVersion = errors.New("unsupported record version")`
  - `var ErrMalformedScript = errors.New("malformed weather script")`
  - `const legacyV0TimeThreshold int64 = 1_000_000_000`
  - `func Decode(raw []byte) (*WeatherData, error)`
  - `func DecodeHex(h string) (*WeatherData, error)`
  - `func IsValidScript(raw []byte) bool`
  - `func payloadStart(ops []*script.ScriptChunk) (int, error)`
  - `func legacyUsesV0Order(ops []*script.ScriptChunk, versionIndex int) (bool, error)`
  - `func readField(c *script.ScriptChunk, f FieldDefinition, ptr any) error`
  - `func scriptNumFromBytes(b []byte) (int64, error)`
  - `func scriptNumFromChunk(c *script.ScriptChunk) (int64, error)`
  - test helpers `func mustHex(t *testing.T, s string) []byte`, `func diffFields(t *testing.T, label string, want, got *WeatherData)` — Task 12's test uses `mustHex`
- `scriptNumFromBytes` lives here rather than in `scriptnum.go` because it returns `ErrMalformedScript`, which is declared in this file. Keeping them together means Task 8 compiles standalone.

**Four traps, all measured:**

1. `script.DecodeScript(raw)` with default options stops at OP_RETURN and returns **2 chunks**, where `chunks[1].Data` is **98 bytes starting `0x6a`** — it includes the OP_RETURN byte. TypeScript's `Script.fromHex(hex).chunks[1].data` is **97 bytes starting `0x51`** — it excludes it. Porting the TypeScript re-parse branch naively prepends a stray `0x6a` and mis-parses every record read back off chain. Always pass `script.DecodeOptionsParseOpReturn`.
2. With that option the count is **36, not 34**: the SDK advances past the `0x6a` byte but still appends the OP_RETURN chunk, because the append at the bottom of its loop runs unconditionally. A guard written on a 34 basis accepts a prefixed script truncated by two whole fields.
3. The **prefix-less layout is real**. Commit `c44b7ae` ("prefix it with an op return for use in markers for now") added `OP_FALSE OP_RETURN`; it is an ancestor of HEAD, and the commits before it wrote `<version> <33 fields>` with no prefix. That layout parses to 34 chunks and round-trips through the TypeScript decoder with zero field differences. It is on chain and must stay readable.
4. **There are two different prefix-less layouts and the bytes cannot tell them apart.** `e2ae463` reordered the schema from time-first to alphabetical at `2026-01-27 11:11:45 -0600`; `c44b7ae` added the prefix at `11:14:41` the same day; `git merge-base --is-ancestor e2ae463 c44b7ae` exits 0. So layout A (prefix-less, time-first) and layout B (prefix-less, alphabetical) both parse to 34 chunks and both start `OP_1`. Reading an A record under `FieldSchema` produces **no error and 33 wrong fields**: index 0 is `air_density` but holds `time`, so a real record decodes as `air_density = 1769529302/1e6 = 1769.529302`, and index 3 is `conditions` but holds the integer `dew_point`, so `string(c.Data)` returns raw little-endian bytes. Every byte-parity test in this plan still passes, because the golden vectors come from HEAD's schema only.

   `Decode` therefore discriminates on the **value** of the first field. In layout B index 0 is `air_density × 1e6`; in layout A it is `time`, a Unix epoch. The threshold is `legacyV0TimeThreshold = 1_000_000_000`, and the repository's own adversarial `extreme` fixture pins the boundary from below: its `air_density` scales to **999999999**, exactly one under the threshold, while its layout-A first field is `time = 2147483647`. Any physically possible air density (about 0.9 to 1.5 kg/m³, so 0.9e6 to 1.5e6 scaled) is three orders of magnitude clear, and any Unix epoch since 2001 is above it. The discriminator is only consulted for **prefix-less** scripts: layout C is identified by its `006a` prefix and never guesses.

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/decoder_test.go` with exactly this content:

```go
package weather

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// mustHex decodes a hex string from the golden file, which is always valid.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex in golden file: %v", err)
	}

	return b
}

// TestDecodeRecordVectorsRoundTrip decodes each golden record and asserts every
// one of the 33 fields matches the original fixture, then re-encodes and asserts
// the bytes are unchanged.
func TestDecodeRecordVectorsRoundTrip(t *testing.T) {
	v := loadVectors(t)

	for _, r := range v.Records {
		if !r.RoundTrips {
			t.Errorf("record %s: the TypeScript oracle recorded roundTrips=false", r.Name)
		}

		want := recordFromVector(t, r.Data)

		got, err := DecodeHex(r.Hex)
		if err != nil {
			t.Errorf("record %s: DecodeHex: %v", r.Name, err)

			continue
		}

		if !reflect.DeepEqual(got, want) {
			diffFields(t, r.Name, want, got)

			continue
		}

		reencoded, err := EncodeHex(got)
		if err != nil {
			t.Errorf("record %s: re-encode: %v", r.Name, err)

			continue
		}

		if reencoded != r.Hex {
			t.Errorf("record %s: re-encoded bytes differ\n got: %s\nwant: %s", r.Name, reencoded, r.Hex)
		}

		t.Logf("record %-8s round-trip OK (%d fields)", r.Name, DataFieldsPerRecord)
	}
}

// v0Hexes are the layout-A forms of the three fixtures: the same 33 field
// chunks, in the pre-e2ae463 FieldSchemaV0 order, with OP_1 and no prefix.
//
// They are a pure PERMUTATION of the golden `legacy` hexes, which is exactly
// what the pre-e2ae463 encoder produced, because each field is emitted
// independently of the others. Derived from the committed vectors with:
//
//	python3 - <<'PY'
//	# parse the golden hex into chunks, map them onto the alphabetical schema,
//	# re-emit in FieldSchemaV0 order.
//	PY
//
// minimal_v0 is byte-identical to minimal's layout-B form: every field of the
// all-zero record is 0x00, so the two orders coincide.
var v0Hexes = map[string]string{
	"minimal": "51000000000000000000000000000000000000000000000000000000000000000000",
	"sample": "5104d6df78690189018d0191018b018952042009653a02fb030766616c6c696e67013103" +
		"10af135254021801015700000000005151000001200a3330202d203334206b6d046d50f868" +
		"03d709010237025205436c65617209636c6561722d646179",
	"extreme": "5104ffffff7f01e401f801b20132013c013204d202964902d0070f72617069646c792066" +
		"616c6c696e67016404ffc99a3b02c800022c01026701034e4e45016402e70302e70302a005" +
		"02a005510002e703020f2702e7032256657279206661722061776179207769746820756e69" +
		"636f64653a20e6b58be8af9504ffffff7f033f420f020f2701143145787472656d6520636f" +
		"6e646974696f6e732077697468207370656369616c2063686172733a2021402324255e262a" +
		"28291665787472656d652d776561746865722de29aa1efb88f",
}

// TestDecodeLegacyV0OrderVectors covers layout A: prefix-less AND in the
// pre-e2ae463 time-first field order. It shares its 34-chunk shape and its
// leading OP_1 with layout B, so nothing in the bytes distinguishes them and
// Decode has to discriminate on the value of the first field.
//
// Without that discrimination this test fails on all 33 fields of sample and
// extreme while every other test in the package still passes, which is precisely
// how the bug would reach production.
func TestDecodeLegacyV0OrderVectors(t *testing.T) {
	v := loadVectors(t)

	for _, r := range v.Records {
		v0hex, ok := v0Hexes[r.Name]
		if !ok {
			t.Errorf("no layout-A hex pinned for record %s", r.Name)

			continue
		}

		raw := mustHex(t, v0hex)

		ops, err := script.DecodeScript(raw, script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Errorf("v0 %s: DecodeScript: %v", r.Name, err)

			continue
		}

		if len(ops) != ChunksLegacy {
			t.Errorf("v0 %s: parsed %d chunks, want %d", r.Name, len(ops), ChunksLegacy)

			continue
		}

		if ops[0].Op != script.Op1 {
			t.Errorf("v0 %s: first opcode is 0x%02x, want 0x51", r.Name, ops[0].Op)
		}

		// The layout-A hex must be a permutation of the layout-B hex: same
		// multiset of bytes, same length, different order (except for minimal,
		// where the two orders coincide).
		if len(raw) != len(mustHex(t, r.Hex))-2 {
			t.Errorf("v0 %s is %d bytes, want %d (the prefixed hex minus 006a)",
				r.Name, len(raw), len(mustHex(t, r.Hex))-2)
		}

		want := recordFromVector(t, r.Data)

		got, err := DecodeHex(v0hex)
		if err != nil {
			t.Errorf("v0 %s: DecodeHex: %v", r.Name, err)

			continue
		}

		if !reflect.DeepEqual(got, want) {
			diffFields(t, r.Name+" (layout A)", want, got)

			continue
		}

		// Re-encoding produces the CURRENT layout. Nothing rewrites old records;
		// this only proves the layout-A decode was lossless.
		reencoded, err := EncodeHex(got)
		if err != nil {
			t.Errorf("v0 %s: re-encode: %v", r.Name, err)

			continue
		}

		if reencoded != r.Hex {
			t.Errorf("v0 %s: re-encode gave %s, want the current-layout hex %s",
				r.Name, truncate(reencoded, 24), truncate(r.Hex, 24))
		}

		t.Logf("layout A %-8s round-trip OK (34 chunks, FieldSchemaV0 order)", r.Name)
	}
}

// TestDecodeLegacyV0IsNotMisreadAsAlphabetical names the exact silent corruption
// the discriminator prevents. Under FieldSchema, the sample's layout-A script
// decodes air_density from the `time` chunk: 1769529302/1e6 = 1769.529302, with
// no error raised.
func TestDecodeLegacyV0IsNotMisreadAsAlphabetical(t *testing.T) {
	got, err := DecodeHex(v0Hexes["sample"])
	if err != nil {
		t.Fatalf("DecodeHex: %v", err)
	}

	if got.AirDensity == 1769.529302 {
		t.Fatal("air_density was read from the time chunk: the layout-A record was decoded under FieldSchema")
	}

	if got.AirDensity != 1.29 {
		t.Errorf("air_density = %v, want 1.29", got.AirDensity)
	}

	if got.Time != 1769529302 {
		t.Errorf("time = %d, want 1769529302", got.Time)
	}

	if got.Conditions != "Clear" {
		t.Errorf("conditions = %q, want \"Clear\": under FieldSchema this index holds the integer dew_point", got.Conditions)
	}
}

// TestLegacyOrderDiscriminatorBoundary pins the A/B discriminator at the exact
// value that makes it non-obvious. The extreme fixture's air_density scales to
// 999999999, ONE below the threshold, so a threshold set any lower would misread
// it as layout A and corrupt all 33 fields.
func TestLegacyOrderDiscriminatorBoundary(t *testing.T) {
	if legacyV0TimeThreshold != 1_000_000_000 {
		t.Errorf("legacyV0TimeThreshold = %d, want 1000000000", legacyV0TimeThreshold)
	}

	v := loadVectors(t)

	cases := []struct {
		label  string
		hex    string
		wantV0 bool
	}{
		{"sample layout B", "", false},
		{"extreme layout B", "", false},
		{"sample layout A", v0Hexes["sample"], true},
		{"extreme layout A", v0Hexes["extreme"], true},
	}

	for _, l := range v.Legacy {
		switch l.Name {
		case "sample_legacy_prefixless":
			cases[0].hex = l.Hex
		case "extreme_legacy_prefixless":
			cases[1].hex = l.Hex
		}
	}

	for _, c := range cases {
		if c.hex == "" {
			t.Fatalf("%s: the golden file is missing the hex for this case", c.label)
		}

		ops, err := script.DecodeScript(mustHex(t, c.hex), script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Errorf("%s: DecodeScript: %v", c.label, err)

			continue
		}

		gotV0, err := legacyUsesV0Order(ops, 0)
		if err != nil {
			t.Errorf("%s: legacyUsesV0Order: %v", c.label, err)

			continue
		}

		if gotV0 != c.wantV0 {
			t.Errorf("%s: legacyUsesV0Order = %v, want %v", c.label, gotV0, c.wantV0)
		}
	}

	// The measured boundary values themselves.
	extremeB, err := scriptNumFromChunk(mustChunk(t, cases[1].hex, 1))
	if err != nil {
		t.Fatalf("extreme layout B first field: %v", err)
	}

	if extremeB != 999999999 {
		t.Errorf("extreme layout B first field = %d, want 999999999 (one below the threshold)", extremeB)
	}

	extremeA, err := scriptNumFromChunk(mustChunk(t, v0Hexes["extreme"], 1))
	if err != nil {
		t.Fatalf("extreme layout A first field: %v", err)
	}

	if extremeA != 2147483647 {
		t.Errorf("extreme layout A first field = %d, want 2147483647 (the time field)", extremeA)
	}
}

// mustChunk parses a script hex and returns chunk i.
func mustChunk(t *testing.T, hexStr string, i int) *script.ScriptChunk {
	t.Helper()

	ops, err := script.DecodeScript(mustHex(t, hexStr), script.DecodeOptionsParseOpReturn)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}

	if i >= len(ops) {
		t.Fatalf("chunk %d requested, script has %d", i, len(ops))
	}

	return ops[i]
}

// TestDecodeLegacyPrefixlessVectors covers layout B, which has no
// OP_FALSE OP_RETURN prefix and 34 chunks. Records in that layout exist on chain
// and must stay readable.
func TestDecodeLegacyPrefixlessVectors(t *testing.T) {
	v := loadVectors(t)

	for _, l := range v.Legacy {
		if l.Chunks != ChunksLegacy {
			t.Errorf("legacy %s: the oracle recorded %d chunks, want %d", l.Name, l.Chunks, ChunksLegacy)
		}

		raw := mustHex(t, l.Hex)

		ops, err := script.DecodeScript(raw, script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Errorf("legacy %s: DecodeScript: %v", l.Name, err)

			continue
		}

		if len(ops) != ChunksLegacy {
			t.Errorf("legacy %s: Go parsed %d chunks, want %d", l.Name, len(ops), ChunksLegacy)
		}

		if ops[0].Op != script.Op1 {
			t.Errorf("legacy %s: first opcode is 0x%02x, want 0x51 (the version, with no prefix)", l.Name, ops[0].Op)
		}

		want := recordFromVector(t, l.Data)

		got, err := DecodeHex(l.Hex)
		if err != nil {
			t.Errorf("legacy %s: DecodeHex: %v", l.Name, err)

			continue
		}

		if !reflect.DeepEqual(got, want) {
			diffFields(t, l.Name, want, got)

			continue
		}

		// Re-encoding a legacy record produces the CURRENT layout, which is the
		// legacy hex with 006a prepended. Nothing rewrites old records; this only
		// proves the decode was lossless.
		reencoded, err := EncodeHex(got)
		if err != nil {
			t.Errorf("legacy %s: re-encode: %v", l.Name, err)

			continue
		}

		if reencoded != "006a"+l.Hex {
			t.Errorf("legacy %s: re-encode gave %s, want 006a + the legacy hex", l.Name, truncate(reencoded, 24))
		}

		t.Logf("legacy %-28s round-trip OK (34-chunk basis)", l.Name)
	}
}

// TestDecodeChunkBasisIsThirtySix proves that the prefixed layout parses to 36
// chunks and that the SDK's OP_RETURN chunk really is retained. A guard written
// on a 34 basis would accept a script truncated by two fields.
func TestDecodeChunkBasisIsThirtySix(t *testing.T) {
	v := loadVectors(t)

	var sampleHex string

	for _, r := range v.Records {
		if r.Name == "sample" {
			sampleHex = r.Hex
		}
	}

	if sampleHex == "" {
		t.Fatal("the golden file has no record named sample")
	}

	raw := mustHex(t, sampleHex)

	ops, err := script.DecodeScript(raw, script.DecodeOptionsParseOpReturn)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}

	if len(ops) != ChunksPrefixed {
		t.Fatalf("parsed %d chunks, want %d", len(ops), ChunksPrefixed)
	}

	if ops[0].Op != script.Op0 || ops[1].Op != script.OpRETURN || ops[2].Op != script.Op1 {
		t.Fatalf("leading opcodes are 0x%02x 0x%02x 0x%02x, want 0x00 0x6a 0x51",
			ops[0].Op, ops[1].Op, ops[2].Op)
	}

	// The default option set stops at OP_RETURN and folds the remainder into one
	// chunk whose Data INCLUDES the 0x6a byte. TypeScript excludes it. This is the
	// asymmetry that makes DecodeOptionsParseOpReturn mandatory.
	defaultOps, err := script.DecodeScript(raw)
	if err != nil {
		t.Fatalf("DecodeScript (default): %v", err)
	}

	if len(defaultOps) != 2 {
		t.Fatalf("default DecodeScript gave %d chunks, want 2", len(defaultOps))
	}

	if len(defaultOps[1].Data) != len(raw)-1 || defaultOps[1].Data[0] != 0x6a {
		t.Fatalf("default DecodeScript chunk 1 Data is %d bytes starting 0x%02x, want %d starting 0x6a",
			len(defaultOps[1].Data), defaultOps[1].Data[0], len(raw)-1)
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	v := loadVectors(t)

	var minimalHex string

	for _, r := range v.Records {
		if r.Name == "minimal" {
			minimalHex = r.Hex
		}
	}

	if minimalHex == "" {
		t.Fatal("the golden file has no record named minimal")
	}

	raw := mustHex(t, minimalHex)

	// Byte 2 is the version opcode. OP_2 (0x52) is version 2.
	bumped := make([]byte, len(raw))
	copy(bumped, raw)
	bumped[2] = 0x52

	_, err := Decode(bumped)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("Decode with version 2 error = %v, want ErrUnsupportedVersion", err)
	}

	if err != nil && !strings.Contains(err.Error(), "got 2") {
		t.Errorf("error %q does not report the version it found", err)
	}

	// Version 0 (OP_0) must be rejected too.
	zeroed := make([]byte, len(raw))
	copy(zeroed, raw)
	zeroed[2] = 0x00

	if _, err := Decode(zeroed); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("Decode with version 0 error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestDecodeRejectsTruncatedScripts(t *testing.T) {
	v := loadVectors(t)

	var minimalHex, legacyHex string

	for _, r := range v.Records {
		if r.Name == "minimal" {
			minimalHex = r.Hex
		}
	}

	for _, l := range v.Legacy {
		if l.Name == "minimal_legacy_prefixless" {
			legacyHex = l.Hex
		}
	}

	if minimalHex == "" || legacyHex == "" {
		t.Fatal("the golden file is missing the minimal record or its legacy form")
	}

	prefixed := mustHex(t, minimalHex)
	legacy := mustHex(t, legacyHex)

	cases := []struct {
		name string
		raw  []byte
	}{
		// 35 chunks in the prefixed layout: one field short. This is the case a
		// 34-chunk guard would wrongly accept.
		{"prefixed minus one field", prefixed[:len(prefixed)-1]},
		{"prefixed minus two fields", prefixed[:len(prefixed)-2]},
		{"legacy minus one field", legacy[:len(legacy)-1]},
		{"header only", prefixed[:3]},
		{"empty", nil},
	}

	for _, c := range cases {
		if _, err := Decode(c.raw); !errors.Is(err, ErrMalformedScript) {
			t.Errorf("%s: Decode error = %v, want ErrMalformedScript", c.name, err)
		}

		if IsValidScript(c.raw) {
			t.Errorf("%s: IsValidScript = true, want false", c.name)
		}
	}

	// Exactly 35 chunks must fail while 36 succeeds: pin the boundary.
	if _, err := Decode(prefixed); err != nil {
		t.Errorf("the untruncated 36-chunk record failed to decode: %v", err)
	}
}

// TestDecodeToleratesTrailingChunks matches the TypeScript guard, which is "at
// least" and not "exactly".
func TestDecodeToleratesTrailingChunks(t *testing.T) {
	v := loadVectors(t)

	for _, r := range v.Records {
		want := recordFromVector(t, r.Data)

		// Append three extra pushes after the record.
		extended := append(mustHex(t, r.Hex), 0x51, 0x52, 0x01, 0x41)

		got, err := Decode(extended)
		if err != nil {
			t.Errorf("record %s with trailing chunks: %v", r.Name, err)

			continue
		}

		if !reflect.DeepEqual(got, want) {
			diffFields(t, r.Name+" (with trailing chunks)", want, got)
		}
	}
}

func TestDecodeHexRejectsBadHex(t *testing.T) {
	if _, err := DecodeHex("zzzz"); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("DecodeHex(\"zzzz\") error = %v, want ErrMalformedScript", err)
	}

	if _, err := DecodeHex("006a5"); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("DecodeHex with odd length error = %v, want ErrMalformedScript", err)
	}
}

func TestScriptNumFromBytesIsTheInverseOfScriptNumBytes(t *testing.T) {
	v := loadVectors(t)

	for _, c := range v.WriteNumber {
		if c.Error != "" {
			continue
		}

		n := parseInt64(t, c.Value)

		got, err := scriptNumFromBytes(scriptNumBytes(n))
		if err != nil {
			t.Errorf("scriptNumFromBytes for %d: %v", n, err)

			continue
		}

		if got != n {
			t.Errorf("scriptNumFromBytes(scriptNumBytes(%d)) = %d", n, got)
		}
	}
}

func TestScriptNumFromBytesRejectsOversized(t *testing.T) {
	// Nine bytes cannot be a script number this encoder produced.
	if _, err := scriptNumFromBytes(make([]byte, 9)); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("scriptNumFromBytes with 9 bytes error = %v, want ErrMalformedScript", err)
	}

	// Eight bytes that decode past 2^53-1 are refused rather than silently
	// truncated, unlike the TypeScript, which loses precision instead.
	huge := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}
	if _, err := scriptNumFromBytes(huge); !errors.Is(err, ErrNumberOutOfRange) {
		t.Errorf("scriptNumFromBytes for 2^63-1 error = %v, want ErrNumberOutOfRange", err)
	}
}

// diffFields reports exactly which fields differ, using the json tags so the
// message names the wire field rather than the Go field.
func diffFields(t *testing.T, label string, want, got *WeatherData) {
	t.Helper()

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}

	var wantMap, gotMap map[string]any
	if err := json.Unmarshal(wantJSON, &wantMap); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}

	if err := json.Unmarshal(gotJSON, &gotMap); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}

	for _, f := range FieldSchema {
		if wantMap[f.Name] != gotMap[f.Name] {
			t.Errorf("%s: field %s = %v, want %v", label, f.Name, gotMap[f.Name], wantMap[f.Name])
		}
	}
}
```

- [ ] **Run it and see it fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -6
```

Expected:

```
internal/weather/decoder_test.go:39:14: undefined: DecodeHex
internal/weather/decoder_test.go:191:11: undefined: Decode
internal/weather/decoder_test.go:192:24: undefined: ErrUnsupportedVersion
internal/weather/decoder_test.go:247:26: undefined: ErrMalformedScript
internal/weather/decoder_test.go:252:6: undefined: IsValidScript
internal/weather/decoder_test.go:264:5: undefined: legacyV0TimeThreshold
internal/weather/decoder_test.go:301:17: undefined: legacyUsesV0Order
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [build failed]
```

Go truncates the list after ten errors, so the exact set shown depends on the order it reports them.

- [ ] **Write the implementation.** Create `/Users/personal/git/demos/weather-chain/internal/weather/decoder.go` with exactly this content:

```go
package weather

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
)

var (
	// ErrUnsupportedVersion reports a record whose version chunk is not Version.
	ErrUnsupportedVersion = errors.New("unsupported record version")

	// ErrMalformedScript reports a script that cannot hold a weather record.
	ErrMalformedScript = errors.New("malformed weather script")
)

// legacyV0TimeThreshold separates the two prefix-less layouts by the VALUE of
// their first field, because nothing in their bytes separates them.
//
// Layout B's first field is air_density x 1e6. Air density is about 0.9 to 1.5
// kg/m3, so 0.9e6 to 1.5e6 scaled; the repository's deliberately extreme fixture
// reaches 999999999, one below this threshold. Layout A's first field is time, a
// Unix epoch, which has been above 1e9 since 2001-09-09 and stays there for
// another 12 years. Every physically possible record falls on the correct side.
const legacyV0TimeThreshold int64 = 1_000_000_000

// Decode reads a weather record out of a raw locking script.
//
// It accepts all THREE on-chain layouts. Two is the wrong answer, and it is the
// dangerous one:
//
//	C  since c44b7ae     OP_FALSE OP_RETURN OP_1 <33 alphabetical>  36 chunks
//	B  e2ae463..c44b7ae^ OP_1 <33 alphabetical>                     34 chunks
//	A  before e2ae463    OP_1 <33 time-first, FieldSchemaV0>        34 chunks
//
// e2ae463 reordered the schema at 2026-01-27 11:11:45 -0600 and c44b7ae added
// the prefix at 11:14:41, three minutes later, so A and B are both real and both
// prefix-less. `git merge-base --is-ancestor e2ae463 c44b7ae` exits 0.
//
// A and B have identical shape: 34 chunks, leading OP_1. Reading an A record
// under FieldSchema raises NO ERROR and gets all 33 fields wrong — air_density
// comes out of the time chunk as 1769.529302 and conditions comes out of
// dew_point as raw little-endian bytes. legacyUsesV0Order therefore picks the
// order from the value of the first field, and only for prefix-less scripts.
//
// The script MUST be parsed with script.DecodeOptionsParseOpReturn. The default
// script.DecodeScript stops at OP_RETURN and hands back a chunk whose Data
// INCLUDES the 0x6a byte, where the TypeScript excludes it: measured on the
// 99-byte sample, Go's default gives Data of length 98 starting 0x6a and
// TypeScript gives 97 starting 0x51. Porting the TypeScript re-parse branch
// naively prepends a stray 0x6a and mis-parses every record read back off chain.
func Decode(raw []byte) (*WeatherData, error) {
	ops, err := script.DecodeScript(raw, script.DecodeOptionsParseOpReturn)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedScript, err)
	}

	index, err := payloadStart(ops)
	if err != nil {
		return nil, err
	}

	version, err := scriptNumFromChunk(ops[index])
	if err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}

	if version != Version {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, version, Version)
	}

	d := &WeatherData{}
	schema := FieldSchema
	ptrs := d.fieldPtrs()

	// index == 0 means there was no OP_FALSE OP_RETURN prefix, so this is layout
	// A or layout B and the bytes cannot say which.
	if index == 0 {
		v0, verr := legacyUsesV0Order(ops, index)
		if verr != nil {
			return nil, verr
		}

		if v0 {
			schema = FieldSchemaV0
			ptrs = d.fieldPtrsV0()
		}
	}

	index++

	if len(ptrs) != len(schema) {
		return nil, fmt.Errorf("%w: %d field pointers for %d schema fields",
			ErrSchemaMismatch, len(ptrs), len(schema))
	}

	for i, f := range schema {
		if err := readField(ops[index+i], f, ptrs[i]); err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i, f.Name, err)
		}
	}

	return d, nil
}

// legacyUsesV0Order reports whether a prefix-less script is in the superseded
// FieldSchemaV0 order (layout A) rather than the alphabetical order (layout B).
//
// versionIndex is the index of the version chunk, so versionIndex+1 is the first
// field. In layout B that field is air_density x 1e6; in layout A it is time.
// See legacyV0TimeThreshold for why the split is sound.
//
// The all-zero record reads 0 here and is classified as layout B. That is
// harmless and not a bug: every field of an all-zero record encodes as 0x00, so
// the two orders produce identical bytes and decode to the identical struct.
func legacyUsesV0Order(ops []*script.ScriptChunk, versionIndex int) (bool, error) {
	first := versionIndex + 1
	if first >= len(ops) {
		return false, fmt.Errorf("%w: no first field to discriminate the legacy field order", ErrMalformedScript)
	}

	n, err := scriptNumFromChunk(ops[first])
	if err != nil {
		return false, fmt.Errorf("read the first field to discriminate the legacy field order: %w", err)
	}

	return n >= legacyV0TimeThreshold, nil
}

// DecodeHex is Decode over a hex string.
func DecodeHex(h string) (*WeatherData, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedScript, err)
	}

	return Decode(raw)
}

// IsValidScript reports whether raw decodes to a weather record.
func IsValidScript(raw []byte) bool {
	_, err := Decode(raw)

	return err == nil
}

// payloadStart returns the index of the version chunk and validates the chunk
// count for whichever layout it detects.
//
// It distinguishes only PREFIXED from PREFIX-LESS. Layouts A and B are both
// prefix-less and both 34 chunks, so they are indistinguishable here and both
// return index 0; Decode then calls legacyUsesV0Order to pick the field order.
//
// The prefixed basis is 36, not 34: DecodeOptionsParseOpReturn advances past the
// 0x6a byte but still appends the OP_RETURN chunk, because the append at the
// bottom of the SDK's loop runs unconditionally. A guard written on a 34 basis
// accepts a prefixed script truncated by two whole fields.
//
// Trailing chunks beyond the record are tolerated on every layout, matching the
// TypeScript, whose guard is also "at least".
func payloadStart(ops []*script.ScriptChunk) (int, error) {
	if len(ops) >= 2 && ops[0].Op == script.Op0 && ops[1].Op == script.OpRETURN {
		if len(ops) < ChunksPrefixed {
			return 0, fmt.Errorf("%w: prefixed layout has %d chunks, want at least %d",
				ErrMalformedScript, len(ops), ChunksPrefixed)
		}

		return 2, nil
	}

	if len(ops) < ChunksLegacy {
		return 0, fmt.Errorf("%w: legacy layout has %d chunks, want at least %d",
			ErrMalformedScript, len(ops), ChunksLegacy)
	}

	return 0, nil
}

// readField reads one schema field into the pointer fieldPtrs returned for the
// same index.
func readField(c *script.ScriptChunk, f FieldDefinition, ptr any) error {
	switch f.Type {
	case FieldInteger:
		p, ok := ptr.(*int64)
		if !ok {
			return fmt.Errorf("%w: integer field is %T, want *int64", ErrSchemaMismatch, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}
		*p = n

		return nil

	case FieldFloat:
		p, ok := ptr.(*float64)
		if !ok {
			return fmt.Errorf("%w: float field is %T, want *float64", ErrSchemaMismatch, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}
		*p = DecodeFloat(n, FloatScale)

		return nil

	case FieldString:
		p, ok := ptr.(*string)
		if !ok {
			return fmt.Errorf("%w: string field is %T, want *string", ErrSchemaMismatch, ptr)
		}
		// An empty push arrives as Op 0x00 with no Data, which is byte-identical
		// to the integer zero. The schema type at this index is what tells the
		// two apart, so never inspect the chunk to decide.
		*p = string(c.Data)

		return nil

	case FieldBoolean:
		p, ok := ptr.(*bool)
		if !ok {
			return fmt.Errorf("%w: boolean field is %T, want *bool", ErrSchemaMismatch, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}
		*p = n == 1

		return nil

	default:
		return fmt.Errorf("%w: unknown field type %d", ErrSchemaMismatch, f.Type)
	}
}

// scriptNumFromBytes is the exact inverse of scriptNumBytes: it reads a
// sign-magnitude little-endian script number.
//
// It is stricter than the TypeScript bytesToNumber, which uses float arithmetic
// and silently loses precision beyond 2^53. This package refuses such a value
// instead, because its own encoder can never produce one.
func scriptNumFromBytes(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, nil
	}

	if len(b) > 8 {
		return 0, fmt.Errorf("%w: script number is %d bytes, maximum 8", ErrMalformedScript, len(b))
	}

	top := len(b) - 1
	neg := b[top]&0x80 != 0

	var mag uint64
	for i := top; i >= 0; i-- {
		v := b[i]
		if i == top && neg {
			v &= 0x7f
		}

		mag = mag<<8 | uint64(v)
	}

	// mag is at most 0x7fffffffffffffff here (8 bytes with bit 7 of the top byte
	// cleared), so it always fits in int64 and the conversion cannot wrap. No
	// //nolint: gosec v2.12.2 does not flag this, and an unused directive is
	// itself a lint failure under allow-unused: false.
	value := int64(mag)
	if neg {
		value = -value
	}

	if value > MaxSafeInteger || value < -MaxSafeInteger {
		return 0, fmt.Errorf("%w: decoded %d", ErrNumberOutOfRange, value)
	}

	return value, nil
}

// scriptNumFromChunk reads a script number from one chunk, mirroring the four
// branches of appendScriptNum.
func scriptNumFromChunk(c *script.ScriptChunk) (int64, error) {
	switch {
	case c.Op == script.Op0:
		return 0, nil
	case c.Op == script.Op1NEGATE:
		return -1, nil
	case c.Op >= script.Op1 && c.Op <= script.Op16:
		return int64(c.Op) - int64(script.Op1) + 1, nil
	default:
		return scriptNumFromBytes(c.Data)
	}
}
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofmt -w internal/ && go test ./internal/weather/... -count=1 -v -run 'TestDecode|TestScriptNumFrom|TestLegacyOrder' 2>&1 | tail -36
```

Expected:

```
=== RUN   TestDecodeRecordVectorsRoundTrip
    decoder_test.go:63: record minimal  round-trip OK (33 fields)
    decoder_test.go:63: record sample   round-trip OK (33 fields)
    decoder_test.go:63: record extreme  round-trip OK (33 fields)
--- PASS: TestDecodeRecordVectorsRoundTrip (0.00s)
=== RUN   TestDecodeLegacyV0OrderVectors
    decoder_test.go:169: layout A minimal  round-trip OK (34 chunks, FieldSchemaV0 order)
    decoder_test.go:169: layout A sample   round-trip OK (34 chunks, FieldSchemaV0 order)
    decoder_test.go:169: layout A extreme  round-trip OK (34 chunks, FieldSchemaV0 order)
--- PASS: TestDecodeLegacyV0OrderVectors (0.00s)
=== RUN   TestDecodeLegacyV0IsNotMisreadAsAlphabetical
--- PASS: TestDecodeLegacyV0IsNotMisreadAsAlphabetical (0.00s)
=== RUN   TestLegacyOrderDiscriminatorBoundary
--- PASS: TestLegacyOrderDiscriminatorBoundary (0.00s)
=== RUN   TestDecodeLegacyPrefixlessVectors
    decoder_test.go:348: legacy minimal_legacy_prefixless    round-trip OK (34-chunk basis)
    decoder_test.go:348: legacy sample_legacy_prefixless     round-trip OK (34-chunk basis)
    decoder_test.go:348: legacy extreme_legacy_prefixless    round-trip OK (34-chunk basis)
--- PASS: TestDecodeLegacyPrefixlessVectors (0.00s)
=== RUN   TestDecodeChunkBasisIsThirtySix
--- PASS: TestDecodeChunkBasisIsThirtySix (0.00s)
=== RUN   TestDecodeRejectsWrongVersion
--- PASS: TestDecodeRejectsWrongVersion (0.00s)
=== RUN   TestDecodeRejectsTruncatedScripts
--- PASS: TestDecodeRejectsTruncatedScripts (0.00s)
=== RUN   TestDecodeToleratesTrailingChunks
--- PASS: TestDecodeToleratesTrailingChunks (0.00s)
=== RUN   TestDecodeHexRejectsBadHex
--- PASS: TestDecodeHexRejectsBadHex (0.00s)
=== RUN   TestScriptNumFromBytesIsTheInverseOfScriptNumBytes
--- PASS: TestScriptNumFromBytesIsTheInverseOfScriptNumBytes (0.00s)
=== RUN   TestScriptNumFromBytesRejectsOversized
--- PASS: TestScriptNumFromBytesRejectsOversized (0.00s)
PASS
```

- [ ] **Run the full gate on both architectures.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  go test ./internal/weather/... -count=1 -v 2>&1 | grep -c '^--- PASS' && \
  GOARCH=amd64 go test ./internal/weather/... -count=1 && \
  make go-lint && echo "GO GATE CLEAN"
```

Expected: `52`, then `ok  github.com/bsv-blockchain-demos/weather-proof/internal/weather`, then `GO GATE CLEAN`. Fifty-two is the total test count for the package after this task: 3 in `types_test.go`, 9 in `schema_test.go`, 1 in `vectors_test.go`, 7 in `scriptnum_test.go`, 11 in `float_test.go`, 9 in `encoder_test.go` and 12 in `decoder_test.go`. If the count is lower, a test file was truncated.

- [ ] **Run the complete project gate.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make check && echo "ALL GREEN"
```

Expected: `parity OK: ...`, then the oracle typecheck, then `Tests: 111 passed, 111 total`, then the Go build, test and lint, then `ALL GREEN`.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/decoder.go internal/weather/decoder_test.go && \
  git commit -m "weather: decode all THREE on-chain record layouts losslessly

Four measured traps are handled. Go's DecodeScript with default options
returns chunks[1].Data INCLUDING the 0x6a byte (98 bytes on the sample)
where TypeScript excludes it (97); a naive port of the TypeScript re-parse
branch prepends a stray 0x6a and mis-parses every record read back off
chain, so DecodeOptionsParseOpReturn is mandatory. With that option the
chunk basis is 36, not 34, because the SDK still appends the OP_RETURN
chunk — a 34-basis guard would accept a script short by two whole fields.
And the prefix-less layout is real, on chain, and decodes at 34 chunks.

The fourth is the one a two-layout reading of the history misses. e2ae463
reordered the schema to alphabetical at 11:11:45 and c44b7ae added the
OP_RETURN prefix at 11:14:41 the same day, so there are two DIFFERENT
prefix-less layouts: time-first (A) and alphabetical (B). Both are 34
chunks starting OP_1, so no byte distinguishes them, and reading an A
record under the alphabetical schema raises no error and returns 33 wrong
fields — air_density decoded from the time chunk as 1769.529302,
conditions decoded from dew_point as raw little-endian bytes. Every
byte-parity vector in this package still passes while that happens, which
is exactly why it needed catching here.

Decode discriminates on the value of the first field: air_density x 1e6 in
layout B versus a Unix epoch in layout A, split at 1e9. The extreme
fixture pins the boundary from one below at 999999999.

All three records round-trip in all three layouts with zero field
differences, and re-encoding reproduces the original bytes.

scriptNumFromBytes refuses a value past 2^53-1 rather than losing precision
the way the TypeScript float arithmetic does. That divergence is deliberate:
this encoder can never produce such a value."
```

---

## Task 12 (HUMAN-GATED): A real on-chain record as a fixture

> **This task is deliberately outside the automated sequence. Task 11 is the autonomous completion point of this plan.** Task 12 needs one input that exists nowhere in this repository — a real txid, or the operator's publishing address — so an agent cannot finish it alone. `make check` is green at the end of Task 11 and stays green through Task 12 whether or not a fixture is ever fetched, because `onchain_test.go` **skips** an empty fixture directory unless `WEATHER_ONCHAIN_FIXTURES=1` demands one. Do not block Tasks 1-11 on this, and never satisfy it with a synthesized script.
>
> **Task 12's completion criterion is: the fetch script runs correctly, and the test fails loudly when the directory is empty and fixtures are demanded.** Committing an actual fixture is the follow-up that a human with the txid performs.

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/scripts/fetch-onchain-fixture.sh`
- Create (only once a real txid is known): `/Users/personal/git/demos/weather-chain/internal/weather/testdata/onchain/$TXID-$VOUT.hex` and `.json`, where both values come from the fetch script and are never invented
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/onchain_test.go`

**Interfaces:**
- Consumes: `Decode`, `EncodeHex` from Tasks 10 and 11; `DataFieldsPerRecord`, `ChunksPrefixed`, `ChunksLegacy` from Task 5b; `mustHex` from Task 11's test file.
- Produces: nothing other tasks consume. This is the last task.

**This is spec §17.2 test 6, and it is the one test the TypeScript suite never had.** Every other assertion in this plan compares Go against TypeScript. This one compares Go against **what is actually on the blockchain** — the only check that catches a shared misunderstanding of the format.

**It also settles the open question left by Task 11.** Layout A (prefix-less, time-first) is supported by `FieldSchemaV0` on the strength of git history alone; nobody has yet checked whether any layout-A record was actually published. That is what `--earliest` is for: it walks an address's history from the oldest transaction and takes the FIRST weather output it finds, which is the record most likely to be layout A. Whatever it returns is evidence — a layout-A fixture proves the `FieldSchemaV0` path is load-bearing, and a layout-B or layout-C earliest record is worth committing as the record that no layout-A output exists on that address.

**Obtaining the one external input.** It cannot be synthesized, and a synthesized fixture would defeat the entire purpose. Two routes, both requiring a human:

1. **The operator's publishing address** (preferred, because it finds the earliest record rather than an arbitrary one). Ask the operator for the address the publisher funds weather outputs from, then run the script's `--earliest` mode, which walks WhatsOnChain address history oldest-first. Verify the address before spending an API call:

   ```bash
   curl -sS --fail "https://api.whatsonchain.com/v1/bsv/main/address/$ADDRESS/history" | python3 -c "
   import json,sys
   h=json.load(sys.stdin)
   print(len(h), 'transactions; oldest listed first:', h[0]['tx_hash'] if h else 'none')
   "
   ```

2. **A txid and vout from the production database**, supplied by the operator, fed to the script's positional form.

There is **no working public read API** to take this from. The hostname an earlier draft of this plan used, `weather-proof-api.bsvblockchain.tech`, does not resolve (`curl` returns HTTP 000); it appears nowhere in this repository except the spec, which instructs that the GitHub variable holding it be deleted. `frontend/.env` points at `http://localhost:3001`. The intended deployed host per the spec is `weather-proof-us-1.bsvblockchain.tech`, which is not deployed yet. Do not put an unresolvable hostname back into this task.

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/onchain_test.go` with exactly this content:

```go
package weather

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// onchainDir holds locking scripts pulled from real published transactions.
//
// Every other parity assertion in this package compares Go against TypeScript.
// These fixtures compare Go against what is actually on the blockchain, which is
// the only check that can catch a misunderstanding both implementations share.
// Populate it with scripts/fetch-onchain-fixture.sh; never hand-write one.
const onchainDir = "testdata/onchain"

// requireOnchainEnv makes an empty fixture directory fatal instead of skipped.
//
// Obtaining a fixture needs an input that lives outside this repository — a real
// txid, or the operator's publishing address — so a fresh checkout cannot
// produce one and `make check` must not be permanently red because of it. Set
// WEATHER_ONCHAIN_FIXTURES=1 in any environment that is supposed to have
// fixtures (a release gate, or a developer who has just fetched one) and the
// skip becomes a hard failure.
const requireOnchainEnv = "WEATHER_ONCHAIN_FIXTURES"

// onchainProvenance mirrors the sidecar JSON the fetch script writes.
type onchainProvenance struct {
	TXID      string `json:"txid"`
	Vout      int    `json:"vout"`
	Network   string `json:"network"`
	Source    string `json:"source"`
	FetchedAt string `json:"fetchedAt"`
	Bytes     int    `json:"bytes"`

	// Earliest records that this fixture was found by walking an address's
	// history oldest-first, so it is the earliest weather output on that address.
	// That is the fixture that can prove or disprove the existence of a layout-A
	// record on chain.
	Earliest bool   `json:"earliest"`
	Address  string `json:"address"`
}

func TestOnChainFixturesDecodeAndReEncode(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(onchainDir, "*.hex"))
	if err != nil {
		t.Fatalf("glob %s: %v", onchainDir, err)
	}

	if len(paths) == 0 {
		msg := fmt.Sprintf("no fixtures in %s.\n"+
			"This test asserts the Go decoder parses a REAL published record, which is the\n"+
			"only assertion here that does not just compare Go against TypeScript.\n"+
			"Fetch one with either form of:\n"+
			"  ./scripts/fetch-onchain-fixture.sh TXID VOUT main\n"+
			"  ./scripts/fetch-onchain-fixture.sh --earliest ADDRESS main\n"+
			"Both need a real value from the operator. Do NOT satisfy this test with a\n"+
			"synthesized script.", onchainDir)

		// Fatal where fixtures are demanded, skipped where they cannot exist yet.
		if os.Getenv(requireOnchainEnv) == "1" {
			t.Fatalf("%s\n(%s=1 makes this fatal instead of skipped)", msg, requireOnchainEnv)
		}

		t.Skipf("%s\nSkipping: set %s=1 to make this a hard failure.", msg, requireOnchainEnv)
	}

	for _, p := range paths {
		//nolint:gosec // G304: the path comes from filepath.Glob over the committed testdata/onchain directory, not from user input.
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)

			continue
		}

		hexStr := strings.TrimSpace(string(raw))

		sidecar := strings.TrimSuffix(p, ".hex") + ".json"

		//nolint:gosec // G304: the path is derived from the filepath.Glob result above, not from user input.
		meta, err := os.ReadFile(sidecar)
		if err != nil {
			t.Errorf("%s: missing provenance sidecar: %v", p, err)

			continue
		}

		// uerr, not err: the outer err is still live below, and govet's shadow
		// analyzer (enabled in .golangci.json) reports a redeclaration here.
		var prov onchainProvenance
		if uerr := json.Unmarshal(meta, &prov); uerr != nil {
			t.Errorf("%s: bad provenance sidecar: %v", sidecar, uerr)

			continue
		}

		if len(prov.TXID) != 64 {
			t.Errorf("%s: txid %q is not 64 hex characters", sidecar, prov.TXID)
		}

		scriptBytes := mustHex(t, hexStr)

		if prov.Bytes != len(scriptBytes) {
			t.Errorf("%s: sidecar says %d bytes, the hex is %d", sidecar, prov.Bytes, len(scriptBytes))
		}

		ops, err := script.DecodeScript(scriptBytes, script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Errorf("%s: DecodeScript: %v", p, err)

			continue
		}

		prefixed := len(ops) >= 2 && ops[0].Op == script.Op0 && ops[1].Op == script.OpRETURN

		wantChunks := ChunksLegacy
		if prefixed {
			wantChunks = ChunksPrefixed
		}

		if len(ops) < wantChunks {
			t.Errorf("%s: %d chunks, want at least %d", p, len(ops), wantChunks)

			continue
		}

		decoded, err := Decode(scriptBytes)
		if err != nil {
			t.Errorf("%s (txid %s vout %d): Decode failed on a REAL on-chain record: %v",
				p, prov.TXID, prov.Vout, err)

			continue
		}

		reencoded, err := EncodeHex(decoded)
		if err != nil {
			t.Errorf("%s: re-encode: %v", p, err)

			continue
		}

		// A legacy record re-encodes into the current layout, which is the same
		// bytes with the 006a prefix added.
		want := hexStr
		if !prefixed {
			want = "006a" + hexStr
		}

		if reencoded != want {
			t.Errorf("%s (txid %s vout %d): re-encode is not byte-identical\n got: %s\nwant: %s",
				p, prov.TXID, prov.Vout, reencoded, want)

			continue
		}

		// Sanity: the record must have real content, not 33 zeros.
		fields, err := json.Marshal(decoded)
		if err != nil {
			t.Errorf("%s: marshal: %v", p, err)

			continue
		}

		var asMap map[string]any
		if err := json.Unmarshal(fields, &asMap); err != nil {
			t.Errorf("%s: unmarshal: %v", p, err)

			continue
		}

		if len(asMap) != DataFieldsPerRecord {
			t.Errorf("%s: decoded %d fields, want %d", p, len(asMap), DataFieldsPerRecord)
		}

		layout := "prefix-less (A or B)"
		if prefixed {
			layout = "prefixed (C)"
		}

		// A prefix-less fixture is the interesting one: report which of the two
		// orders Decode chose, since no byte in the script says.
		if !prefixed {
			v0, verr := legacyUsesV0Order(ops, 0)
			if verr != nil {
				t.Errorf("%s: legacyUsesV0Order: %v", p, verr)

				continue
			}

			if v0 {
				layout = "prefix-less, FieldSchemaV0 order (A) — layout A EXISTS on chain"
			} else {
				layout = "prefix-less, alphabetical order (B)"
			}
		}

		earliest := ""
		if prov.Earliest {
			earliest = " [earliest output on " + prov.Address + "]"
		}

		t.Logf("on-chain %s vout %d (%s): %d bytes, %d chunks, %s, re-encodes byte-identically%s",
			prov.TXID, prov.Vout, prov.Network, len(scriptBytes), len(ops), layout, earliest)
	}
}
```

- [ ] **Run it and see it fail when fixtures are demanded.** This is the step that proves the gate works in both directions. Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  WEATHER_ONCHAIN_FIXTURES=1 go test ./internal/weather/... -run TestOnChainFixtures -count=1 2>&1 | head -12
```

Expected — a hard failure, because the environment claims to have fixtures and has none:

```
--- FAIL: TestOnChainFixturesDecodeAndReEncode (0.00s)
    onchain_test.go:67: no fixtures in testdata/onchain.
        This test asserts the Go decoder parses a REAL published record, which is the
        only assertion here that does not just compare Go against TypeScript.
        Fetch one with either form of:
          ./scripts/fetch-onchain-fixture.sh TXID VOUT main
          ./scripts/fetch-onchain-fixture.sh --earliest ADDRESS main
        Both need a real value from the operator. Do NOT satisfy this test with a
        synthesized script.
        (WEATHER_ONCHAIN_FIXTURES=1 makes this fatal instead of skipped)
```

- [ ] **Run it without the variable and see it skip, not pass silently.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  go test ./internal/weather/... -run TestOnChainFixtures -count=1 -v 2>&1 | grep -E '^(=== RUN|--- SKIP|ok|FAIL)'
```

Expected:

```
=== RUN   TestOnChainFixturesDecodeAndReEncode
--- SKIP: TestOnChainFixturesDecodeAndReEncode (0.00s)
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.3s
```

A `SKIP` line is visible in `-v` output and in CI logs, which a silent pass would not be. This is what keeps `make check` green through Tasks 1-11 while still shouting the moment anyone claims the fixture exists.

- [ ] **Write the fetch script.** Create `/Users/personal/git/demos/weather-chain/scripts/fetch-onchain-fixture.sh` with exactly this content:

```bash
#!/usr/bin/env bash
# Pulls a real published weather output script into
# internal/weather/testdata/onchain/ so the Go decoder can be tested against the
# blockchain rather than only against the TypeScript encoder.
#
# Usage:
#   ./scripts/fetch-onchain-fixture.sh TXID [VOUT] [main|test]
#   ./scripts/fetch-onchain-fixture.sh --earliest ADDRESS [main|test]
#
# --earliest walks the address's transaction history from the OLDEST entry and
# takes the first weather output it finds. That is the fixture worth having: the
# earliest record is the one that can show whether layout A (prefix-less AND in
# the pre-e2ae463 time-first field order) was ever actually published, which
# internal/weather/schema.go currently supports on the strength of git history
# alone.
#
# Both forms need a value that is NOT in this repository. Ask the operator for a
# txid and vout from the production database, or for the publishing address.
# There is no public read API for this: the hostname earlier drafts used,
# weather-proof-api.bsvblockchain.tech, does not resolve.
set -euo pipefail

usage() {
  echo "usage: $0 TXID [VOUT] [main|test]" >&2
  echo "       $0 --earliest ADDRESS [main|test]" >&2
  exit 1
}

[ "$#" -ge 1 ] || usage

mode="txid"
address=""

if [ "$1" = "--earliest" ]; then
  mode="earliest"
  address="${2:?--earliest needs an ADDRESS}"
  network="${3:-main}"
  txid=""
  vout=""
else
  txid="$1"
  vout="${2:-0}"
  network="${3:-main}"

  if [ "${#txid}" -ne 64 ]; then
    echo "error: txid must be 64 hex characters, got ${#txid}" >&2
    exit 1
  fi
fi

case "$network" in
  main|test) ;;
  *) echo "error: network must be 'main' or 'test', got '$network'" >&2; exit 1 ;;
esac

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$REPO_ROOT/internal/weather/testdata/onchain"
API="https://api.whatsonchain.com/v1/bsv/$network"
mkdir -p "$OUT_DIR"

# extract_weather_output TXJSON -> "VOUT HEX" for the first weather-looking
# output, or nothing. A weather output starts 006a51 (layout C) or 51 (A or B).
extract_weather_output() {
  python3 -c "
import json, sys
tx = json.load(sys.stdin)
for i, o in enumerate(tx.get('vout') or []):
    h = ((o.get('scriptPubKey') or {}).get('hex') or '')
    if h.startswith('006a51') or (h.startswith('51') and len(h) >= 68):
        sys.stdout.write('%d %s' % (i, h))
        break
"
}

if [ "$mode" = "earliest" ]; then
  hist_url="$API/address/$address/history"
  echo "walking $hist_url oldest-first"

  # WhatsOnChain returns the history oldest-first; keep that order explicitly.
  hashes="$(curl -sS --fail --max-time 30 "$hist_url" | python3 -c "
import json, sys
for e in json.load(sys.stdin):
    print(e['tx_hash'])
")"

  if [ -z "$hashes" ]; then
    echo "error: address $address has no transaction history on $network" >&2
    exit 1
  fi

  echo "$hashes" | wc -l | xargs echo "history entries:"

  script_hex=""
  while read -r h; do
    [ -n "$h" ] || continue
    echo "  checking $h"

    found="$(curl -sS --fail --max-time 30 "$API/tx/hash/$h" | extract_weather_output)"
    if [ -n "$found" ]; then
      txid="$h"
      vout="${found%% *}"
      script_hex="${found#* }"
      echo "  found a weather output at vout $vout"
      break
    fi

    # WhatsOnChain rate-limits at 3 requests/second on the free tier.
    sleep 1
  done <<< "$hashes"

  if [ -z "$script_hex" ]; then
    echo "error: no weather output found in any transaction of $address" >&2
    exit 1
  fi

  url="$API/tx/hash/$txid"
else
  url="$API/tx/hash/$txid"
  echo "fetching $url"

  tx="$(curl -sS --fail --max-time 30 "$url")"

  script_hex="$(printf '%s' "$tx" | python3 -c "
import json, sys
tx = json.load(sys.stdin)
vout = int(sys.argv[1])
outs = tx.get('vout') or []
if vout >= len(outs):
    sys.exit('transaction has %d outputs, vout %d does not exist' % (len(outs), vout))
h = outs[vout].get('scriptPubKey', {}).get('hex')
if not h:
    sys.exit('output %d has no scriptPubKey.hex' % vout)
sys.stdout.write(h)
" "$vout")"
fi

if [ -z "$script_hex" ]; then
  echo "error: no script hex extracted" >&2
  exit 1
fi

# A weather output starts either 006a51 (layout C) or 51 (layout A or B).
case "$script_hex" in
  006a51*) layout="prefixed (C)" ;;
  51*)     layout="prefix-less (A or B — the decoder decides by value)" ;;
  *) echo "error: output $vout of $txid does not look like a weather record" >&2
     echo "       script starts ${script_hex:0:12}, expected 006a51 or 51" >&2
     exit 1 ;;
esac

bytes=$(( ${#script_hex} / 2 ))
base="$OUT_DIR/$txid-$vout"

printf '%s\n' "$script_hex" > "$base.hex"

earliest_flag="false"
if [ "$mode" = "earliest" ]; then
  earliest_flag="true"
fi

python3 - "$base.json" "$txid" "$vout" "$network" "$url" "$bytes" "$earliest_flag" "$address" <<'PY'
import datetime, json, sys
out, txid, vout, network, url, nbytes, earliest, address = sys.argv[1:9]
json.dump({
    'txid': txid,
    'vout': int(vout),
    'network': network,
    'source': url,
    'fetchedAt': datetime.datetime.now(datetime.timezone.utc)
        .replace(microsecond=0).isoformat().replace('+00:00', 'Z'),
    'bytes': int(nbytes),
    'earliest': earliest == 'true',
    'address': address,
}, open(out, 'w'), indent=2)
open(out, 'a').write('\n')
PY

echo "wrote $base.hex  ($bytes bytes, $layout)"
echo "wrote $base.json"
echo
echo "Now run: WEATHER_ONCHAIN_FIXTURES=1 go test ./internal/weather/... -run TestOnChainFixtures -v"
```

The `fetchedAt` timestamp is fine here: this file is provenance for a fixture and is never regenerated by `make parity`, so it cannot make anything permanently red the way it would inside `vectors.json`.

The `len(h) >= 68` guard in `extract_weather_output` is there because `51` alone is also the start of plenty of non-weather scripts; a weather record is at least 34 bytes, so 68 hex characters, and the `Decode` call in the test is the real check.

- [ ] **Verify the script's argument handling without spending an API call.** Run:

```bash
chmod +x /Users/personal/git/demos/weather-chain/scripts/fetch-onchain-fixture.sh && \
cd /Users/personal/git/demos/weather-chain && \
  ./scripts/fetch-onchain-fixture.sh; echo "no args exit $?"; \
  ./scripts/fetch-onchain-fixture.sh deadbeef; echo "short txid exit $?"; \
  ./scripts/fetch-onchain-fixture.sh 0000000000000000000000000000000000000000000000000000000000000000 0 wrongnet; echo "bad network exit $?"
```

Expected: the usage block then `no args exit 1`; `error: txid must be 64 hex characters, got 8` then `short txid exit 1`; `error: network must be 'main' or 'test', got 'wrongnet'` then `bad network exit 1`. All three guards fire before any network access, so this step is safe to run offline and is the part of Task 12 that can be completed without the operator.

- [ ] **Run the complete gate and confirm Task 12 changes nothing about it.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  make check && \
  GOARCH=amd64 go test ./internal/weather/... -count=1 && \
  echo "ALL GREEN, BOTH ARCHITECTURES"
```

Expected: `parity OK`, `Tests: 111 passed, 111 total`, the Go build/test/lint all clean, an `ok` for amd64, then `ALL GREEN, BOTH ARCHITECTURES`. The on-chain test skips, and the skip is the point: adding a test that cannot yet run must not turn the gate red.

- [ ] **Commit the harness. This is where the automatable part of Task 12 ends.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add scripts/fetch-onchain-fixture.sh internal/weather/onchain_test.go && \
  git commit -m "weather: harness to test the decoder against real published records

Every other assertion in this package compares Go against TypeScript. This
one compares Go against what is actually on the blockchain, which is the
only check that can catch a misunderstanding both implementations share. The
TypeScript suite never had it.

fetch-onchain-fixture.sh pulls a locking script from WhatsOnChain by txid,
or with --earliest walks an address's history oldest-first and takes the
first weather output. The earliest record is the one that can settle whether
layout A was ever published, which schema.go's FieldSchemaV0 currently
supports on git history alone.

No fixture is committed here, because obtaining one needs a txid or the
operator's publishing address and neither is in this repository. The test
therefore SKIPS an empty directory and turns that skip into a hard failure
under WEATHER_ONCHAIN_FIXTURES=1, so make check stays green while the gap
stays visible. It refuses anything that does not start 006a51 or 51, and a
synthesized fixture is explicitly out of bounds: it would assert only that
this package agrees with itself."
```

### Follow-up, once a human supplies the input (not automatable)

These steps need a real txid or the operator's publishing address. Everything above is complete without them.

- [ ] **Fetch a fixture.** With `$ADDRESS` or `$TXID`/`$VOUT` from the operator, run one of:

```bash
cd /Users/personal/git/demos/weather-chain && \
  ./scripts/fetch-onchain-fixture.sh --earliest "$ADDRESS" main

# or, given a specific record:
cd /Users/personal/git/demos/weather-chain && \
  ./scripts/fetch-onchain-fixture.sh "$TXID" "$VOUT" main
```

Expected output shape (the txid, byte count and layout are whatever the chain says):

```
fetching https://api.whatsonchain.com/v1/bsv/main/tx/hash/$TXID
wrote /Users/personal/git/demos/weather-chain/internal/weather/testdata/onchain/$TXID-0.hex  (99 bytes, prefixed (C))
wrote /Users/personal/git/demos/weather-chain/internal/weather/testdata/onchain/$TXID-0.json

Now run: WEATHER_ONCHAIN_FIXTURES=1 go test ./internal/weather/... -run TestOnChainFixtures -v
```

If it exits with `does not look like a weather record`, the vout is wrong — the transaction has several outputs and only some carry weather scripts. List them:

```bash
curl -sS "https://api.whatsonchain.com/v1/bsv/main/tx/hash/$TXID" | \
  python3 -c "
import json,sys
tx=json.load(sys.stdin)
for i,o in enumerate(tx.get('vout') or []):
    h=(o.get('scriptPubKey') or {}).get('hex') or ''
    print(i, len(h)//2, 'bytes', h[:16])
"
```

- [ ] **Run the test with fixtures demanded and see it pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  WEATHER_ONCHAIN_FIXTURES=1 go test ./internal/weather/... -run TestOnChainFixtures -count=1 -v 2>&1 | tail -6
```

Expected shape:

```
=== RUN   TestOnChainFixturesDecodeAndReEncode
    onchain_test.go:208: on-chain $TXID vout 0 (main): 99 bytes, 36 chunks, prefixed (C), re-encodes byte-identically
--- PASS: TestOnChainFixturesDecodeAndReEncode (0.00s)
PASS
```

If the log line instead says `layout A EXISTS on chain`, that is a significant finding, not a problem: it is positive proof that `FieldSchemaV0` and the value-based discriminator in `decoder.go` are load-bearing rather than defensive. Record it in the commit message.

If `re-encode is not byte-identical` fires, **stop and do not work around it.** It means the production encoder and this Go encoder disagree on a real record, which is exactly the failure this whole plan exists to prevent. Diff the two hex strings against the per-field breakdown in this plan's "Measured reference values" section to identify the field.

- [ ] **Commit the fixture with its evidence.** Run, substituting the real values into the message:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/testdata/onchain && \
  WEATHER_ONCHAIN_FIXTURES=1 make check && \
  git commit -m "weather: a real published record as a decoder fixture

Fetched with scripts/fetch-onchain-fixture.sh. The sidecar records the
txid, vout, network, source URL and byte count, and whether it was found by
walking the address history oldest-first.

State which layout it is and, if it is prefix-less, which field order the
decoder chose. An earliest-record fixture in layout B or C is itself the
evidence that no layout-A output exists on that address."
```

---

## Self-review

### 1. Spec coverage

Every encoder-related requirement in spec §7 and §17, and the task that implements it.

| Spec requirement | Where |
|---|---|
| §7.1 layout `OP_FALSE OP_RETURN`, version as `OP_1`, 33 fields in schema order | Task 10 `Encode`; `TestEncodeVersionAndPrefixAreOpcodes` |
| §7.1 measured 99-byte sample script | Task 10 `TestEncodeRecordVectorsAreByteExact` |
| §7.1 per-type emission table (integer / float / string / boolean) | Task 10 `appendField` |
| §7.1 `ErrStringTooLong` above 65535 rather than `OP_PUSHDATA4` | Task 10 `MaxPushDataLen`, `TestEncodeRejectsOverlongString` |
| §7.2 `writeBn` four branches in order | Task 8 `appendScriptNum`, `TestAppendScriptNumOpcodeBranches` |
| §7.2 `scriptNumBytes` sign-extension algorithm | Task 8 `scriptNumBytes`, `TestAppendScriptNumSignExtension` |
| §7.2 "do not use `AppendBigInt`" | Task 8 doc comment on `appendScriptNum` |
| §7.3 `jsRound` ECMAScript semantics | Task 9 `jsRound`, `TestJSRoundDivergesFromMathRound` |
| §7.3 FMA safety via explicit conversion | Task 9 `EncodeFloat`, plus the `GOARCH=amd64` run in Tasks 9, 11 and 12 |
| §7.3 pinned divergence vectors `-1.2345675`, `-0.0000005`, `-1.5e-6`, `-2.5e-6` | Task 9 `TestJSRoundDivergesFromMathRound`; also in the 35-vector golden table |
| §7.4 pin `@bsv/sdk` exactly `1.10.3`, un-ignore and commit the lockfile | Task 1 |
| §7.4 provenance recorded inside `vectors.json` | Task 3 (`sdkVersion`, `sdkIntegrity`, `oracleDigest`; see deviation D1) |
| §7.4 `make parity` verifies by default, regeneration behind `make parity-regen` | Task 4 |
| §7.4 oracle survives via `git mv` to `parity/ts/` | Task 2a |
| §7.4 generate vectors BEFORE any TypeScript deletion | Tasks 1-4 all precede any Go code; nothing in this plan deletes TypeScript |
| §7.4 contents 1: three fixture hexes asserted byte-exact | Task 3 generates, Task 10 asserts |
| §7.4 contents 2: the `writeNumber` table plus the two error cases | Task 3 generates 40 entries, Task 8 asserts 38 hex + 2 errors |
| §7.4 contents 3: `writeBin` boundaries 0, 1, 75, 76, 255, 256, 500 | Task 3 generates 12 lengths (a superset), Task 8 asserts |
| §7.4 contents 4: `jsRound` divergence vectors | Task 3 generates 35 floats, Task 9 asserts |
| §7.4 contents 5: negatives — NaN, ±Inf, non-integral, `\|n\| > 2^53-1` | Task 9 `TestEncodeFloatRejectsNonFinite`, `TestEncodeFloatRejectsOutOfRange`, `TestRequireIntegral`; Task 8 `TestAppendScriptNumRejectsOutOfRange` |
| §7.5 typed errors `ErrNonFinite`, `ErrNonIntegral`, `ErrNumberOutOfRange`, `ErrStringTooLong` | Tasks 8, 9, 10 |
| §7.5 the `internal/tempest/mapper.go` layer | **Out of scope** for Plan A (the mapper is a later plan). `RequireIntegral` in Task 9 is the reusable check it will call. |
| §7.6 the 211-byte fixture is ≤ 297 bytes | Task 10 `TestEncodeRecordsFitTheScriptCap` |
| §7.6 a synthetic 298-byte record is rejected before `CreateAction` | **Out of scope**: the publisher does not exist in Plan A. Noted in Task 10. |
| §7.7 use `DecodeScript(raw, DecodeOptionsParseOpReturn)` | Task 11 `Decode`, `TestDecodeChunkBasisIsThirtySix` |
| §7.7 chunk basis 36, not 34 | Task 11 `payloadStart`, `ChunksPrefixed` |
| §7.7 legacy prefix-less fallback at 34 | Task 11 `payloadStart`, `TestDecodeLegacyPrefixlessVectors` |
| **Beyond the spec:** the SECOND prefix-less layout (pre-`e2ae463`, time-first) | Task 7 `FieldSchemaV0`, Task 11 `legacyUsesV0Order`, `TestDecodeLegacyV0OrderVectors`, `TestDecodeLegacyV0IsNotMisreadAsAlphabetical`, `TestLegacyOrderDiscriminatorBoundary` (gap G7) |
| §7.7 decoder negatives: version ≠ 1, fewer than 36 chunks, trailing tolerated, legacy at 34 | Task 11 `TestDecodeRejectsWrongVersion`, `TestDecodeRejectsTruncatedScripts`, `TestDecodeToleratesTrailingChunks`, `TestDecodeLegacyPrefixlessVectors` |
| §7.8 version-evolution warning in a comment above the constant | Task 5b `types.go` |
| §17.1 CI job running the regeneration and `git diff --exit-code` | **Out of scope**: CI workflow files belong to the CI plan. `make parity` is the target that job will call; `make check` chains it. |
| §17.2 test 1 Encoder: three hexes, `writeNumber`, `writeBin`, `jsRound`, §7.5 negatives | Tasks 8, 9, 10 |
| §17.2 test 2 Encoder invariant: no field emits `0x6a` | Task 10 `TestEncodeNoFieldEmitsOpReturnOpcode` |
| §17.2 test 3 Encoder size | Task 10, encoder half only (publisher half deferred, stated in the task) |
| §17.2 test 4 Decoder negatives | Task 11 |
| §17.2 test 5 Float helpers with non-default scales 100 and 1e9, and epsilon cases | Task 9 `TestEncodeDecodeFloatNonDefaultScales`, `TestValidateFloatPrecision`, `TestDecodeFloatPrecisionLoss` |
| §17.2 test 6 Real on-chain fixture | Task 12 (**human-gated**: the harness, the fetch script and both gate directions are automated; committing a fixture needs a txid or the operator's address) |
| §4.1 package layout: `types.go`, `schema.go`, `scriptnum.go`, `float.go`, `encoder.go`, `decoder.go`, `testdata/vectors.json` | Tasks 5, 7, 8, 9, 10, 11, 3 — all six files, exactly as listed |
| §4.1 module path, Go version, `.golangci.json` with the four documented edits | Task 5a |
| §21.1 phase 1 exit criterion `make parity` green | Task 4 |
| §21.1 phase 2 exit criterion tests 1-6 green | Task 11's final `make check`, which is the autonomous completion point. Test 6 is present and skipping until Task 12's follow-up supplies a fixture; `WEATHER_ONCHAIN_FIXTURES=1 make check` is the gate that demands it. |

**Gaps found and closed while writing this plan** (they are not in the spec, and the plan would have failed CI without them):

- **G1.** Five files in the running `src/` tree import the oracle, so a bare `git mv` breaks `npx tsc`. Measured: five `TS2307` errors, and `TS6059` if the imports are simply repointed while `rootDir: "./src"` remains. Closed in Task 2c by dropping `rootDir` and repointing the four consumer files, with a verification step.
- **G2.** `Dockerfile` line 18 is `COPY tests ./tests`, and `build.yml` builds that image on every pull request to `master`. After the move that directory does not exist and the Docker build fails outright. Closed in Task 2.
- **G3.** The root `jest.config.js` points at `tests/`, and the root `package.json` `test` script runs bare `jest`. Both break after the move. Closed in Task 2b and 2d.
- **G4.** The `sourceCommit: git rev-parse HEAD` field that earlier analysis specified is not deterministic across commits — `make parity` would be red on the very next commit, and would also differ between a full and a shallow CI clone. Closed in Task 3 by replacing it with a content digest of the oracle sources (deviation D1).
- **G5.** Each line of a make recipe runs in its own shell, so the `cd parity/ts` in the `parity` recipe must be wrapped in a subshell or the subsequent `diff` resolves `$(VECTORS)` against the wrong directory. Closed in Task 4.
- **G6.** `ENCODING.md`'s "97 bytes" is the OP_RETURN payload of a 99-byte script, and its field-order section describes an order the code no longer uses; a future engineer will read both. Closed in Task 7 with a version notice that keeps the superseded order rather than deleting it, because that section is the only surviving documentation that layout A existed.
- **G7.** **There are three on-chain layouts, not two.** `git merge-base --is-ancestor e2ae463 c44b7ae` exits 0 and the two commits are three minutes apart, so the alphabetical reorder shipped before the `OP_RETURN` prefix and there are two DIFFERENT prefix-less layouts. Both are 34 chunks starting `OP_1`, so a decoder written for two layouts reads a layout-A record as 33 silently wrong fields — `air_density = 1769.529302` out of the `time` chunk — while every byte-parity vector in the plan still passes, because the vectors come from HEAD's schema only. Closed in Task 7 (`FieldSchemaV0`, transcribed from `git show e2ae463^:src/format/schema.ts` and diffed back against that command) and Task 11 (`legacyUsesV0Order`, discriminating on the value of the first field). Layout-A test vectors are permutations of the golden chunks, so they need no second oracle.
- **G8.** Six `//nolint` directives an earlier draft carried are themselves lint failures under `nolintlint`'s `allow-unused: false`: gosec v2.12.2 does range analysis and does not flag any of the four integer conversions, and `unconvert` ignores float conversions by default. `make go-lint` exited 2 at Task 8 and `make check` failed at Tasks 8-12. Closed by demoting all six to ordinary comments; only the two `G304` directives in `onchain_test.go` suppress a diagnostic that actually fires. `misspell` (locale US, `run.tests: true`) also rejected `marshalled` and `synthesised` in test message strings, failing lint first at Task 7; closed by using the US spellings.
- **G9.** Task 12's only self-contained route to a txid did not exist: `weather-proof-api.bsvblockchain.tech` does not resolve (`curl` returns HTTP 000), appears nowhere in the repository except a spec line that says to delete the variable holding it, and `frontend/.env` points at `localhost:3001`. With a hard `t.Fatalf` on an empty fixture directory, `make check` would have been permanently red and Tasks 1-11 unfinishable. Closed by making Task 12 human-gated with Task 11 as the autonomous completion point, gating the test on `WEATHER_ONCHAIN_FIXTURES=1`, and replacing the dead route with an `--earliest ADDRESS` mode that also answers G7's open question.
- **G10.** `make parity` before the Makefile edit does **not** print `No rule to make target`. Tasks 1-3 create a `parity/` directory, so GNU make treats the target as already satisfied and exits 0 with `Nothing to be done for 'parity'.` — which reads like success. Closed in Task 4 by predicting the real output and stating that this is why `.PHONY: parity` is mandatory.

**Deliberate deviations, each documented in code so nobody "fixes" them into a parity break:**

- **D1.** `vectors.json` carries `oracleDigest` (sha256 over the seven oracle source files) instead of `sourceCommit`. Reason in G4. Measured value: `ffe3653dc800ec0190060635cc1ae2186f79257624ad81f5a2ca1a3ace07311a`.
- **D2.** `Encode` takes `*WeatherData`, not `WeatherData`. `fieldPtrs` must return pointers into the caller's storage so that one ordered list serves both the encoder and the decoder.
- **D3.** Strings above 65535 bytes return `ErrStringTooLong` where TypeScript would emit `OP_PUSHDATA4`. Spec §7.1 prescribes this; it is unreachable under the publisher's cap.
- **D4.** `scriptNumFromBytes` refuses a decoded value past 2^53-1 and refuses more than 8 bytes, where the TypeScript `bytesToNumber` silently loses precision in float arithmetic. This encoder can never produce such a value.
- **D5.** `make parity` does not run `go test`, unlike an earlier draft: that would make the target unusable in Task 4, before the Go package exists. `make check` chains `parity parity-test go-build go-test go-lint` instead.
- **D6.** `WEATHER_MAX_SCRIPT_BYTES = 297` is a test-local constant, not an exported one, because the running value is a validated config knob owned by `internal/config` in a later plan. Two sources of truth would be worse than one duplicated test constant.
- **D7.** The decoder resolves layout A versus layout B with a **value heuristic**, not a format check, because no format check exists: `legacyUsesV0Order` reads the first field and compares it against `legacyV0TimeThreshold = 1_000_000_000`. Every physically possible air density scales to under 1.5e6 and every Unix epoch since 2001 is above 1e9, and the repository's adversarial `extreme` fixture pins the boundary from one below at 999999999. An all-zero record reads 0 and is classified as layout B, which is exact rather than approximate: all-zero encodes identically in both orders. The alternative — refusing prefix-less records outright — would make already-published data unreadable, and guessing alphabetical unconditionally is the silent-corruption bug G7 describes.
- **D8.** `TestOnChainFixturesDecodeAndReEncode` **skips** an empty fixture directory instead of failing, unless `WEATHER_ONCHAIN_FIXTURES=1`. A skip is visible in `-v` output and CI logs, unlike a pass, and this is the only way Tasks 1-11 can reach a green `make check` without an input that exists nowhere in the repository. Reason in G9.
- **D9.** `.golangci.json` is written out literally rather than generated from `/Users/personal/git/go/go-wallet-toolbox/.golangci.json`. The generating script worked on exactly one machine; the file has to work in CI, in a worktree, and for everyone else. Its four differences from the toolbox config are enumerated in the Global Constraints and asserted by the verification step in Task 5a.

### 2. Placeholder scan

Checked and clear:

- No `TBD`, `TODO`, `FIXME`, or `XXX` anywhere in this plan.
- No "add appropriate error handling": every error path is written out, including which typed error and which wrapping verb.
- No "write tests for the above": every task's test file is given in full, test-first, before its implementation.
- No "similar to Task N": `fieldPtrs`, the 33-field lists, `wantFieldNames`, `wantFieldNamesV0`, the sign-extension table, the three fixture hexes and the three layout-A hexes are each written out in full wherever they appear.
- No ellipses inside any code block. Every Go, TypeScript, Python, bash, JSON and make snippet is complete and compiles or runs as written. `.golangci.json` is included literally rather than described.
- No angle-bracket placeholders in any command an agent is expected to run. The two commands that need a human-supplied value (`--earliest "$ADDRESS"` and `"$TXID" "$VOUT"`) live in Task 12's explicitly human-gated follow-up section, below its final commit, and are named shell variables rather than `<txid>`-style holes.
- Every code block has a matching "run it and see it fail" step with the exact expected failure text, and a "run it and see it pass" step with the exact expected output. That includes the golden vectors: `check-vectors.py` is committed and observed failing with `FileNotFoundError` before `gen-vectors.ts` is written.
- The one external dependency, a real txid or the operator's publishing address, is isolated in Task 12 with two concrete acquisition routes and an instruction never to synthesize a substitute. Tasks 1-11 need nothing outside this document, and `make check` is green at the end of Task 11.

### 3. Type consistency

Cross-checked every identifier a later task consumes against the task that defines it. No renames.

| Identifier | Defined | Consumed by |
|---|---|---|
| `Version`, `FloatScale`, `FloatEpsilon`, `DataFieldsPerRecord`, `ChunksPrefixed`, `ChunksLegacy` | Task 5b `types.go` | 6 (`Version`, `FloatScale`, `DataFieldsPerRecord`), 7 (`DataFieldsPerRecord`), 9 (`FloatScale`, `FloatEpsilon`), 10 (`Version`, `FloatScale`, `DataFieldsPerRecord`), 11 (`Version`, `FloatScale`, `ChunksPrefixed`, `ChunksLegacy`, `DataFieldsPerRecord`), 12 (`DataFieldsPerRecord`, `ChunksPrefixed`, `ChunksLegacy`) |
| `FieldType`, `FieldInteger`, `FieldFloat`, `FieldString`, `FieldBoolean`, `(FieldType).String()` | Task 5b `types.go` | 7, 10, 11 |
| `FieldDefinition{Name string; Type FieldType; Required bool}` | Task 5b `types.go` | 7 (builds `FieldSchema`), 10 (`appendField` parameter), 11 (`readField` parameter) |
| `WeatherData` and its 33 exported field names | Task 5b `types.go` | 7 (`fieldPtrs`), 10, 11, 12 |
| `jsonTags(t *testing.T) []string` | Task 5b `types_test.go` | 7 `TestSchemaNamesMatchJSONTags` |
| `FieldSchema []FieldDefinition` | Task 7 `schema.go` | 10, 11, and Task 11's `diffFields` |
| `FieldSchemaV0 []FieldDefinition` | Task 7 `schema.go` | 11 `Decode` (layout A only); 7's own permutation test. Never used by the encoder. |
| `(*WeatherData).fieldPtrs() []any` | Task 7 `schema.go` | 10 `Encode`, 11 `Decode`, 7 `fieldPtrsV0` |
| `(*WeatherData).fieldPtrsV0() []any` | Task 7 `schema.go` | 11 `Decode` (layout A only) |
| `MaxSafeInteger int64` | Task 8 `scriptnum.go` | 9 `EncodeFloat`, 10 `TestEncodeRejectsOutOfRangeIntegerField`, 11 `scriptNumFromBytes` |
| `ErrNumberOutOfRange` | Task 8 `scriptnum.go` | 9 `EncodeFloat`, 10 (test), 11 `scriptNumFromBytes` |
| `scriptNumBytes(int64) []byte` | Task 8 `scriptnum.go` | 11 `TestScriptNumFromBytesIsTheInverseOfScriptNumBytes` |
| `appendScriptNum(*script.Script, int64) error` | Task 8 `scriptnum.go` | 9 (test), 10 `Encode` and `appendField` |
| `hexOfScriptNum(t, int64) string`, `parseInt64(t, string) int64`, `truncate(string, int) string` | Task 8 `scriptnum_test.go` | 9 (`hexOfScriptNum`, `parseInt64`), 10 (`truncate`), 11 (`parseInt64`, `truncate`) |
| `loadVectors(t) *testVectors` and the `testVectors` shape | Task 6 `vectors_test.go` | 7, 8, 9, 10, 11 |
| `ErrNonFinite`, `ErrNonIntegral` | Task 9 `float.go` | 10 `TestEncodeRejectsNonFiniteFloatField` |
| `jsRound(float64) float64` | Task 9 `float.go` | 9 only (unexported, tested directly) |
| `EncodeFloat(v, scale float64) (int64, error)` | Task 9 `float.go` | 10 `appendField` |
| `DecodeFloat(scaled int64, scale float64) float64` | Task 9 `float.go` | 11 `readField` |
| `ValidateFloatPrecision(original, decoded, epsilon float64) bool` | Task 9 `float.go` | 9 only |
| `RequireIntegral(float64) error` | Task 9 `float.go` | 9 only in this plan; `internal/tempest/mapper.go` later |
| `MaxPushDataLen`, `ErrStringTooLong`, `ErrSchemaMismatch` | Task 10 `encoder.go` | 10 (tests), 11 `Decode`/`readField` (`ErrSchemaMismatch`) |
| `Encode(*WeatherData) (*script.Script, error)`, `EncodeHex(*WeatherData) (string, error)` | Task 10 `encoder.go` | 11 (round-trip re-encode), 12 |
| `recordFromVector(t, json.RawMessage) *WeatherData` | Task 10 `encoder_test.go` | 11 |
| `ErrUnsupportedVersion`, `ErrMalformedScript` | Task 11 `decoder.go` | 11 (tests). `ErrMalformedScript` is also returned by `scriptNumFromBytes`, which is why that function lives in `decoder.go` and not `scriptnum.go`. |
| `Decode([]byte) (*WeatherData, error)`, `DecodeHex(string)`, `IsValidScript([]byte) bool` | Task 11 `decoder.go` | 11 (tests), 12 |
| `legacyUsesV0Order([]*script.ScriptChunk, int) (bool, error)`, `legacyV0TimeThreshold int64` | Task 11 `decoder.go` | 11 `Decode` and `TestLegacyOrderDiscriminatorBoundary`, 12 `onchain_test.go` (reports which order a prefix-less fixture used) |
| `mustHex(t, string) []byte` | Task 11 `decoder_test.go` | 12 |
| `mustChunk(t, string, int) *script.ScriptChunk`, `v0Hexes map[string]string` | Task 11 `decoder_test.go` | 11 only |
| `VECTORS`, `NPM_INSTALL` make variables | Task 4 `Makefile` | Task 5a extends the same section |
| `parity/ts/tsconfig.json`, `parity/ts/package-lock.json` | Tasks 2b and 1 | Task 3 `gen-vectors.ts` reads both |
| `parity/ts/check-vectors.py` | Task 3 (committed before the generator) | Task 3's post-generation step; runnable standalone from the repository root forever after |

One ordering constraint worth restating, because breaking it breaks the build between tasks: `scriptNumFromBytes` and `scriptNumFromChunk` belong to `decoder.go` (Task 11), **not** `scriptnum.go` (Task 8), because they return `ErrMalformedScript`, which Task 11 declares. Similarly `fieldPtrs` and `fieldPtrsV0` belong to `schema.go` (Task 7) and not `types.go` (Task 5b), because `unused` would flag them in Task 5b where no code calls them yet.



