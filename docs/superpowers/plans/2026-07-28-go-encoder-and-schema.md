# Go Weather Encoder and Schema Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go package `internal/weather` that deterministically encodes a 33-field weather reading into a valid `OP_FALSE OP_RETURN` locking script under a hard 297-byte cap, and decodes it back losslessly.

**Architecture:** A new Go module at the repository root (the repository is TypeScript today; no Go module exists yet) containing one package, `internal/weather`, bootstrapped and then built in four layers: the record types plus a 33-entry ordered schema slice, the number and float encoding primitives, the encoder, and the decoder. The wire format is **internal to this backend** — nothing outside it decodes the script, so there is no external byte contract and no TypeScript oracle; correctness comes from `Decode(Encode(x)) == x` plus the 297-byte cap, and a self-generated golden file catches the symmetric encoder-plus-decoder edit that a round trip cannot. Every layer is driven test-first and every step is a pure function of the code under test: no wall clock, no randomness, no map iteration.

**Tech Stack:** Go 1.26.3; `github.com/bsv-blockchain/go-sdk` v1.3.2 (the `script` and `script/interpreter` packages — the only dependency); golangci-lint v2.12.2; gofumpt v0.10.0; GNU make. No npm dependency is added and no TypeScript is touched.

## Global Constraints

- Go version: `go 1.26.3` in `go.mod`, with **no separate `toolchain` line** (matching `/Users/personal/git/go/go-wallet-toolbox/go.mod`). `GOTOOLCHAIN=auto`, the default, downloads 1.26.3 when the local default is older; `go version` run from **inside** the module prints `go version go1.26.3 darwin/arm64`, run from `/tmp` it may print something older, and that is expected.
- Go module path: `github.com/bsv-blockchain-demos/weather-proof`. The import path of the one package is therefore `github.com/bsv-blockchain-demos/weather-proof/internal/weather`.
- Go code lives at the repository **ROOT** (`/Users/personal/git/demos/weather-chain`), alongside `frontend/`. Not in a `backend/` subdirectory. `frontend/` contains no `.go` files so `go build ./...` skips it, and the existing `build.yml` build context of `.` does not change.
- Go SDK: `github.com/bsv-blockchain/go-sdk v1.3.2`. This is the version `go-wallet-toolbox` pins, and every signature and measured byte string in this plan was read from `/Users/personal/go/pkg/mod/github.com/bsv-blockchain/go-sdk@v1.3.2/script/`.
- Lint: `.golangci.json` is `/Users/personal/git/go/go-wallet-toolbox/.golangci.json` with **four documented edits** — the `gci` prefix and `goimports.local-prefixes` say `github.com/bsv-blockchain-demos/weather-proof`; the `revive` settings block is dropped (revive is in the `disable` list and its separate `.revive.toml` does not exist in this repository); `run.build-tags: ["mage"]` is dropped (this module has no build tags). Task 1b writes the resulting 298-line file out literally rather than generating it from an absolute path into another repository, which would work on exactly one machine.
- **Lint trap: `nolintlint` runs with `allow-unused: false`, so an UNNECESSARY `//nolint` is itself a lint failure.** This plan contains **zero** `//nolint` directives, and it stays that way. Every reason that looks like it wants one is written as an ordinary comment instead. Do not "helpfully" add a directive: `make go-lint` will fail on it.
- **Lint trap: `misspell` runs with `locale: US`.** Its `ignore-rules` list contains `marshalling` but **not** `marshalled` or `synthesised`. `run.tests: true`, so test files are linted too: write `marshaled`, `synthesized`, `serialized`, `behavior`, `normalized`.
- **Lint trap: `gosec` G304 fires on `os.ReadFile` with a path that is not a constant** — for example a path built with `filepath.Join` from a `filepath.Glob` result — and the only fix is an explained `//nolint`, which `nolintlint` then polices. This plan avoids the whole knot: the golden file is read through the string constant `goldenPath = "testdata/golden/records.json"`, and there is no `filepath.Glob` anywhere.
- **Lint trap: `govet` runs with `shadow` enabled**, so `if err := f(); err != nil` inside a function that already has a live `err` is a failure. Where that happens in this plan the inner variable is given its own name (`mkErr`, `writeErr`, `numErr`).
- **Lint trap: `gosmopolitan` rejects a string literal containing a Han-script rune.** The repository's TypeScript fixture contains `测试`, which would fail. Every non-ASCII string in this plan therefore uses non-Han code points instead (`⚡`, `☔`, `☂`, `☀`, `→`, `ünïcødé`), chosen to keep the same byte lengths — the `extreme` fixture is still exactly 211 bytes. Escaping the Han runes as `\uXXXX` would **not** help: `gosmopolitan` inspects the decoded value, not the source spelling.
- **Lint trap: `prealloc` (range-loops) wants `make([]T, 0, n)` before a range loop that appends.** Four test helpers in Task 5 are written that way for this reason.
- **Lint trap: `gosec` G404 rejects `math/rand`.** Task 5's property test is a deterministic sweep over fixed value tables, not a random generator — which is better for a plan anyway, because a failure is reproducible from the source alone.
- **The 297-byte script cap is MONEY, not style.** 297 bytes is the top of the **contiguous** one-claim fuel window at denomination 50. A script one byte longer silently costs a **second fuel claim for every record published, forever**. It is not 331 (the top of a disjoint island; the 298–321 gap in between already costs two claims, so a 331 cap guarantees nothing) and not 250 (an earlier figure, safe but wasting 47 bytes of usable window, and actively wrong at denomination 40). `Encode` returns `ErrScriptTooLarge` above it, and a test pins 297 accepted / 298 rejected from both sides.
- **NO TypeScript is deleted, moved or edited in this plan.** `package.json`, `tsconfig.json`, `jest.config.js`, `src/`, `tests/` and `frontend/` are untouched. The Go package is additive. There is deliberately **no** `parity/ts/` tree, **no** pinned `@bsv/sdk`, and **no** `make parity` target: byte-parity with the historical TypeScript encoder is not a requirement, because nothing outside this backend ever decoded the script.
- **The golden file is self-generated and must contain no timestamp and no git revision.** It records `formatVersion` and `schemaFieldCount` and nothing else. Both alternatives were tried and both break regenerate-and-diff: a `generatedAt` differs between two runs seconds apart, and `git rev-parse HEAD` is stable within a run but changes on the very next commit, so every later commit would fail the diff, and it differs between a full clone and a shallow CI clone. **A golden file must be a pure function of the code under test.** A test asserts this directly.
- **The 33-field schema order IS the wire format.** Strict alphabetical, `air_density` first and `wind_gust` last, ported from the TypeScript `src/format/schema.ts`. Do **not** port the order from `ENCODING.md`, whose "Field Order" section lists a time-first, category-grouped order that was superseded on chain. The wire format is **not redesigned**: that option was considered and declined.
- **Determinism is a hard requirement.** `FieldSchema` is a slice, never a map. The encode path reads no clock and consumes no randomness. A test encodes the same record 100 times and requires identical bytes, and a second test proves the emitted order equals `FieldSchema`'s slice order.
- **The float rule, chosen and documented:** scaled floats are rounded **half away from zero** with Go's `math.Round`. `-1.5e-6` encodes as `-2`, not `-1`. This is deliberately not JavaScript's `Math.round` (half toward +Infinity, which would give `-1`).
- **The integer rule, chosen and documented:** integers are encoded exactly, with minimal push encoding, and the magnitude is bounded by `MaxScriptInt = 2^53 - 1`. A value outside that range is **rejected** with `ErrNumberOutOfRange`, never truncated or clamped. The bound is not a script limitation — script numbers are arbitrary width after Genesis — it is the range in which a value survives a round trip through JSON, and every weather value arrives as JSON and leaves as JSON.

### Verified `go-sdk` v1.3.2 API — true signatures, read from the module source

Read from `/Users/personal/go/pkg/mod/github.com/bsv-blockchain/go-sdk@v1.3.2/script/{script.go,script_chunk.go,opcodes.go,errors.go}` and `.../script/interpreter/number.go`. None of this is guessed, and every byte string below was produced by running the real functions.

| Symbol | True signature | Note |
|---|---|---|
| `script.Script` | `type Script []byte` | `&script.Script{}` is a usable empty script |
| `(*Script).AppendPushData` | `func (s *Script) AppendPushData(d []byte) error` | Delegates to `EncodePushDatas` → `PushDataPrefix`. Already minimal for every length ≤ 0xFFFF |
| `(*Script).AppendOpcodes` | `func (s *Script) AppendOpcodes(oo ...uint8) error` | **Rejects `0x01..0x4e`** with `ErrInvalidOpcodeType` ("use AppendPushData for push data funcs"), so it cannot be used for pushes. `0x00`, `0x4f`, `0x51..0x60` and `0x6a` all pass |
| `(*Script).Bytes` / `(*Script).String` | `func (s *Script) Bytes() []byte` / `func (s *Script) String() string` | `String()` is lowercase hex |
| `(*Script).IsData` | `func (s *Script) IsData() bool` | True for a script starting `OP_RETURN` or `OP_FALSE OP_RETURN` |
| `script.NewFromBytes` / `NewFromHex` | `func NewFromBytes(b []byte) *Script` / `func NewFromHex(s string) (*Script, error)` | — |
| `script.PushDataPrefix` | `func PushDataPrefix(data []byte) ([]byte, error)` | Measured: `00` for length 0; `0x01..0x4b` + data for 1..75; `4c <len:1>` for 76..255; `4d <len:2 LE>` for 256..65535; `4e <len:4 LE>` above that |
| `script.DecodeScript` | `func DecodeScript(b []byte, options ...DecodeOptions) ([]*ScriptChunk, error)` | See the trap below |
| `script.DecodeOptionsParseOpReturn` | `DecodeOptionsParseOpReturn DecodeOptions = 0` | **Required.** Without it `DecodeScript` sets `op.Data` to the whole remainder **including** the `0x6a` byte and stops |
| `script.ScriptChunk` | `type ScriptChunk struct { Op byte; Data []byte }` | Both fields exported |
| `(*Script).ParseOps` | `func (s *Script) ParseOps() (ops []*ScriptChunk, err error)` | Equivalent to `DecodeScript(..., DecodeOptionsParseOpReturn)` for our shape; this plan uses `DecodeScript` so the option is explicit |
| `script.MinPushSize` | `func MinPushSize(bb []byte) int` | Reference only; not used |
| `interpreter.ScriptNumber` | `type ScriptNumber struct { Val *big.Int; AfterGenesis bool }` | Fields exported; construct directly |
| `(*ScriptNumber).Bytes` | `func (n *ScriptNumber) Bytes() []byte` | Little-endian with a sign bit — exactly the encoding required |
| `(*ScriptNumber).Int64` | `func (n *ScriptNumber) Int64() int64` | Clamps rather than truncating |
| `(*ScriptNumber).Set` | `func (n *ScriptNumber) Set(i int64) *ScriptNumber` | Not used — see gotcha 1 |
| `interpreter.MakeScriptNumber` | `func MakeScriptNumber(bb []byte, scriptNumLen int, requireMinimal, afterGenesis bool) (*ScriptNumber, error)` | The decode direction |
| `interpreter.CheckMinimalDataEncoding` | `func CheckMinimalDataEncoding(v []byte) error` | What `requireMinimal` calls |
| Opcode constants | `script.Op0` / `OpZERO` / `OpFALSE` = `0x00`; `script.OpDATA1` = `0x01` … `OpDATA75` = `0x4b`; `OpPUSHDATA1` = `0x4c`; `OpPUSHDATA2` = `0x4d`; `OpPUSHDATA4` = `0x4e`; `Op1NEGATE` = `0x4f`; `Op1` / `OpONE` = `0x51` … `Op16` = `0x60`; `OpRETURN` = `0x6a`; `OpDUP` = `0x76` | all `byte` |

**Three verified gotchas, each of which would be a silent defect:**

1. **`(*ScriptNumber).Bytes()` MUTATES its receiver on a negative value.** It does `if isNegative { n.Neg() }` and never restores the sign. Measured: a `ScriptNumber` holding `-128` returns `8080` on the first call and **`8000` on the second**, with `Val` left at `+128`. Always construct a fresh `ScriptNumber` per call; never cache one and never `Set()` and reuse it.
2. **`(*Script).AppendBigInt` is the wrong function.** `func (s *Script) AppendBigInt(bInt big.Int) error` is literally `AppendPushData(bInt.Bytes())` — big-endian magnitude with **no sign byte**. Measured: it encodes `-128` as `0180`, i.e. as `+128`. Undecodable. Never use it.
3. **`DecodeScript` without `DecodeOptionsParseOpReturn` swallows the payload.** Measured on `006a5101ff0102`: **5 chunks** with the option (`00`, `6a`, `51`, and the two pushes) and **2 chunks** without (`00`, then one `6a` chunk whose `Data` is `6a5101ff0102`). `(*Script).Chunks()` calls `DecodeScript` with no options, so it is also wrong for this purpose. The option advances past the `0x6a` byte but **still appends the chunk**, which is why the chunk count of a record is **36** and not 34.

### Measured reference values (do not re-derive)

Produced by running the real encoder of this plan against `go-sdk` v1.3.2 on `go1.26.3 darwin/arm64`.

Number encoding (`appendScriptNum`):

```
             0 -> 00                  (OP_0)
            -1 -> 4f                  (OP_1NEGATE)
             1 -> 51                  (OP_1)
             2 -> 52
            16 -> 60                  (OP_16, last opcode branch)
            17 -> 0111                (first data push)
           127 -> 017f
          -127 -> 01ff
           128 -> 028000
          -128 -> 028080
           255 -> 02ff00
          -255 -> 02ff80
           256 -> 020001
          -256 -> 020081
    2147483647 -> 04ffffff7f
   -2147483648 -> 050000008080
    2147483648 -> 050000008000
 9007199254740991 -> 07ffffffffffff1f      (2^53-1)
-9007199254740991 -> 07ffffffffffff9f
```

Script sizes:

| record | bytes | note |
|---|---|---|
| all-zero floor | **36** | `006a51` + 33 × `00` |
| the repository's real Tempest sample | **99** | |
| the repository's `extremeWeatherData` fixture | **211** | 86 bytes of headroom under the cap |
| an all-negative record | **64** | |
| the on-cap synthetic record | **297** | accepted |
| the same with one more string byte | **298** | rejected with `ErrScriptTooLarge` |

Rounding rule (`math.Round`, half away from zero), with the JavaScript column recorded so nobody drifts back toward it:

| input | Go, this rule | ECMAScript `Math.round` |
|---|---|---|
| `-1.2345675` | `-1234568` | `-1234567` |
| `-0.0000005` | `-1` | `0` |
| `-1.5e-6` | `-2` | `-1` |
| `-2.5e-6` | `-3` | `-2` |

---

## File Structure

Every file this plan creates or modifies, and its single responsibility. All paths are absolute from `/Users/personal/git/demos/weather-chain`.

| Path | Action | Single responsibility |
|---|---|---|
| `go.mod` | Create (Task 1a), retidied (Task 3) | Module identity, Go version, the one dependency. |
| `go.sum` | Create (generated, Task 1a), retidied (Task 3) | Dependency hashes: 2 lines from `go get`, 16 after Task 3's `go mod tidy`. |
| `.golangci.json` | Create | The toolbox lint config with the four documented edits. |
| `.gitignore` | Modify | Append a Go section (built binary, test binaries, coverage output). |
| `Makefile` | Modify | Append `go-build`, `go-test`, `go-lint`, `go-golden`, `check`. The existing Docker-oriented `build`/`test` targets keep working untouched. |
| `internal/weather/doc.go` | Create | The package comment: what the format is, and the four properties it must hold. |
| `internal/weather/types.go` | Create | `Version`, `FloatScale`, `FloatEpsilon`, `DataFieldsPerRecord`, `ChunksPerRecord`, `MaxScriptInt`, `FieldType`, `FieldDefinition`, `WeatherData`. |
| `internal/weather/types_test.go` | Create | The constants, `FieldType.String()`, the 33 json tags being complete and alphabetical, and `WeatherData` staying comparable. |
| `internal/weather/schema.go` | Create | `FieldSchema` — the 33-entry ordered slice that IS the wire format — and the `fieldPtrs` schema-to-struct bridge. |
| `internal/weather/schema_test.go` | Create | Order, count, alphabetical-ness, type tally, schema-name/json-tag agreement, and the `fieldPtrs` index and type correspondence. |
| `internal/weather/scriptnum.go` | Create | `ErrNumberOutOfRange`, `ErrNotANumber`, `appendScriptNum`, `scriptNumFromChunk`. |
| `internal/weather/scriptnum_test.go` | Create | The measured number table, all sixteen opcode branches, out-of-range, the `Bytes()` mutation guard, the primitive round trip, and the malformed-chunk rejections. |
| `internal/weather/float.go` | Create | `ErrNonFinite`, `scaleFloat`, `unscaleFloat`, and the documented rounding rule. |
| `internal/weather/float_test.go` | Create | The rounding-rule table with its JavaScript divergences, non-finite rejection, the `MaxScriptInt` boundary, and the precision contract. |
| `internal/weather/encoder.go` | Create | `MaxScriptBytes`, `maxPushDataLen`, `ErrScriptTooLarge`, `ErrStringTooLong`, `ErrUnknownFieldType`, `Encode`, `EncodeHex`, `appendField`. |
| `internal/weather/encoder_test.go` | Create | Shape, the measured sizes, determinism over 100 encodes, emitted order equals schema order, the no-`0x6a` invariant, the 297/298 cap boundary, and the typed-error negatives. |
| `internal/weather/golden_test.go` | Create | The frozen input set (`goldenCases`), the on-cap record, and the `-update` golden mechanism plus its no-timestamp guard. |
| `internal/weather/testdata/golden/records.json` | Create (generated, committed) | **The change detector.** The encoder's own output for five records, frozen. Never hand-edited. |
| `internal/weather/decoder.go` | Create | `ErrMalformedScript`, `ErrUnsupportedVersion`, `Decode`, `DecodeHex`, `IsValidScript`, `readField`. |
| `internal/weather/decoder_test.go` | Create | `equalRecords`, round trips over the golden set and the committed hex, the edge-case table, the deterministic sweep, and every decoder negative. |

---

## Task 1a: Bootstrap the Go module and the package

There is no behavior yet, so the red step is structural rather than a unit test: `go build ./...` cannot even resolve a package pattern in this repository today, because there is no `go.mod`. That is a real, observable failure and this task's job is to turn it green.

Task 1 is split in two so that neither half is a grab bag. This half is only the module identity, the one dependency and the package that will hold everything: five short commands and one 20-line file. The lint config, the two tool installs and the make targets are Task 1b, so a 298-line config file and a multi-minute network build of the linter are not hidden inside the same red-green cycle, and so the test-first cycle in Task 2 is not buried behind either.

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/go.mod`
- Create: `/Users/personal/git/demos/weather-chain/go.sum` (generated by `go get`)
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/doc.go`
- Test: none. The deliverable is verified by the exact `go build ./...` failure below turning into `go build ./...` plus `go test ./...` succeeding.

**Interfaces:**
- Consumes: nothing.
- Produces:
  - the module path `github.com/bsv-blockchain-demos/weather-proof`, requiring `github.com/bsv-blockchain/go-sdk v1.3.2`
  - the package `weather` at `internal/weather`, import path `github.com/bsv-blockchain-demos/weather-proof/internal/weather`, containing only a package comment so far
  - no Go identifiers. Task 2 is the first task with declarations.

### Steps

- [ ] **See the red state.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go env GOMOD; go build ./...
```

Expected, exactly:

```
/dev/null
pattern ./...: directory prefix . does not contain main module or its selected dependencies
```

`go env GOMOD` printing `/dev/null` is Go's way of saying "there is no module here". That is the failure this task fixes.

- [ ] **Create the module and add the one dependency.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  printf 'module github.com/bsv-blockchain-demos/weather-proof\n\ngo 1.26.3\n' > go.mod && \
  go get github.com/bsv-blockchain/go-sdk@v1.3.2 && \
  cat go.mod
```

Expected `go.mod` afterwards, exactly — this is the measured output of the two commands above on go1.26.3, not the post-tidy content:

```
module github.com/bsv-blockchain-demos/weather-proof

go 1.26.3

require github.com/bsv-blockchain/go-sdk v1.3.2 // indirect
```

Two details that look wrong and are not. The `// indirect` marker is correct: no `.go` file imports the SDK yet, so `go get` records the requirement as not-directly-imported. And there is deliberately **no** second `require (...)` block, because `go get` writes only what it resolved — `go.sum` is two lines at this point, with no transitive entries.

Both change in Task 3, which is the first task to import the SDK: its first `go mod tidy` drops the `// indirect` marker from the go-sdk line, adds a second block requiring `github.com/pkg/errors v0.9.1 // indirect` and `golang.org/x/crypto v0.54.0 // indirect`, and takes `go.sum` from 2 lines to 14; its second, after `scriptnum.go` adds the `script/interpreter` import, takes `go.sum` to its final 16 and leaves `go.mod` alone. That diff is expected there and is committed there.

**Do not run `go mod tidy` in this task.** Nothing imports the SDK yet, so tidy would strip the requirement entirely. Task 3 is the first task that imports it, and tidy is safe from then on. Note that the not-yet-imported requirement is harmless here: `go build ./...` and `go test ./...` both succeed with it present.

- [ ] **Confirm the toolchain resolves to 1.26.3.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go version
```

Expected: `go version go1.26.3 darwin/arm64` (or your platform's equivalent). The same command run from `/tmp` may print an older version — that is `GOTOOLCHAIN=auto` doing its job, not a problem.

- [ ] **Create the package.** Create `/Users/personal/git/demos/weather-chain/internal/weather/doc.go` with exactly this content. It is the only file in the package for now, and it exists so `go build ./...` and (from Task 1b onward) `golangci-lint run` have a real package to analyze rather than matching nothing.

```go
// Package weather encodes and decodes weather records as BSV locking scripts.
//
// The format is INTERNAL to this backend. Nothing outside this module decodes
// it: the browser only verifies a merkle proof over the raw transaction, and it
// reads the weather values from the JSON API. There is therefore no external
// byte contract to match, and no TypeScript oracle to agree with.
//
// What the format must still guarantee, and what this package enforces:
//
//   - a VALID script: OP_FALSE OP_RETURN, a version opcode, then 33 field
//     pushes in the fixed order of FieldSchema;
//   - a HARD 297-byte cap (MaxScriptBytes). This is fuel arithmetic, not style:
//     297 bytes is the top of the contiguous one-claim window at denomination
//     50, so one byte more silently costs a second fuel claim per record;
//   - Decode(Encode(x)) == x, because the app's own proof and reconciliation
//     paths read the script back;
//   - determinism: identical input, identical bytes, always. No map iteration
//     in the encode path, no wall clock, no randomness.
package weather
```

- [ ] **See it green.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  go env GOMOD && \
  go build ./... && echo "BUILD OK" && \
  go test ./...
```

Expected: the absolute path of `go.mod` (no longer `/dev/null`), then `BUILD OK`, then exactly:

```
?   	github.com/bsv-blockchain-demos/weather-proof/internal/weather	[no test files]
```

`frontend/` and `node_modules/` contain no `.go` files, so `./...` matches the one package and nothing else.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add go.mod go.sum internal/weather/doc.go && \
  git commit -m "weather: bootstrap the Go module and the package

Module github.com/bsv-blockchain-demos/weather-proof at the repository root,
go 1.26.3, one dependency (go-sdk v1.3.2), no toolchain line.

go.mod records the go-sdk requirement as // indirect and go.sum is two lines,
because no .go file imports the SDK yet. Task 3 is the first task that does, and
its go mod tidy drops the marker and adds the two transitive requires. Running
tidy now would delete the requirement instead.

internal/weather/doc.go carries the package comment and nothing else, so the
build has a real package to analyze. No TypeScript is touched by this commit or
by any other in this plan."
```

---

## Task 1b: The lint config, the pinned tools and the make targets

The red state here is also structural and also real: this repository has no `.golangci.json`, so there is nothing configuring the linter, and the Makefile has no `go-lint` target to invoke it with. Both are observable in one command. This is a separate task from 1a because it contains the only multi-minute action in the plan — a network build of two pinned tools — and because a 298-line config file deserves its own commit rather than riding along with the module bootstrap.

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/.golangci.json`
- Modify: `/Users/personal/git/demos/weather-chain/.gitignore` (append a Go section at the end)
- Modify: `/Users/personal/git/demos/weather-chain/Makefile` (append a Go section at the end)
- Test: none. The deliverable is verified by `gofumpt -l` printing nothing, `golangci-lint run` printing `0 issues.` and `make -n` echoing the four Go recipes.

**Interfaces:**
- Consumes: the module and the `weather` package from Task 1a. No Go identifiers.
- Produces:
  - `.golangci.json`, the toolbox config with the four documented edits
  - `golangci-lint` v2.12.2 and `gofumpt` v0.10.0 on `PATH`
  - make targets `go-build`, `go-test`, `go-lint`, `go-golden`, `check`
  - no Go identifiers.

### Steps

- [ ] **See the red state.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  { test -f .golangci.json && echo "HAS LINT CONFIG" || echo "NO LINT CONFIG"; } && \
  grep -c '^go-lint:' Makefile
```

Expected, exactly:

```
NO LINT CONFIG
0
```

Those are the two gaps this task closes. If `golangci-lint` happens to be installed already from another project, running it here would use its built-in defaults rather than this repository's rules, which is the same gap wearing a disguise.

- [ ] **Write the lint config.** Create `/Users/personal/git/demos/weather-chain/.golangci.json` with exactly this content. It is `/Users/personal/git/go/go-wallet-toolbox/.golangci.json` with four edits, all four visible below: the `gci` prefix and `goimports.local-prefixes` name this module; the `revive` settings block is gone (revive is in the `disable` list and `.revive.toml` does not exist here); `run.build-tags: ["mage"]` is gone (this module has no build tags).

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

- [ ] **Verify the lint config mechanically.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && python3 -c "
import json
c = json.load(open('.golangci.json'))
assert c['version'] == '2'
assert c['run']['tests'] is True
assert 'build-tags' not in c['run']
assert 'revive' not in c['linters']['settings']
assert 'revive' in c['linters']['disable']
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

- [ ] **Install the two pinned tools.** This is the longest action in the task: a network build measured in minutes, not seconds. Run:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 && \
  go install mvdan.cc/gofumpt@v0.10.0 && \
  golangci-lint --version && gofumpt --version
```

Expected: a line containing `golangci-lint has version 2.12.2` and a line `v0.10.0 (go1.26.3)`. Both versions are the ones `go-wallet-toolbox` pins in `.github/env/10-mage-x.env` and `.github/env/10-pre-commit.env`, and they are the versions every lint decision in this plan was checked against. If `golangci-lint` is not on your `PATH` afterwards, it is in `$(go env GOPATH)/bin`.

- [ ] **Append the Go section to `.gitignore`.** Add these four lines at the end of `/Users/personal/git/demos/weather-chain/.gitignore`. Nothing here matches `internal/weather/testdata/`, which must stay tracked.

```gitignore

# Go
/weather
*.test
*.out
```

- [ ] **Append the Go section to the Makefile.** Add this at the end of `/Users/personal/git/demos/weather-chain/Makefile`. The existing Docker-oriented `build` and `test` targets are left exactly as they are, which is why these are named `go-*`.

```makefile

.PHONY: go-build go-test go-lint go-golden check

# Named go-* because this Makefile already has Docker-oriented `build` and
# `test` targets that must keep working. Reconciling them is a later plan.
go-build:
	go build ./...

go-test:
	go test ./... -count=1

go-lint:
	golangci-lint run

# Regenerate the self-generated golden file. Review the diff before committing:
# this target is how a deliberate format change is recorded, and a surprising
# diff here means the encoder changed by accident.
go-golden:
	go test ./internal/weather -run TestGolden -update -count=1

check: go-build go-test go-lint
```

- [ ] **See it green.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  gofumpt -l internal/ && echo "GOFUMPT CLEAN" && \
  golangci-lint run && \
  make -n go-build go-test go-lint go-golden
```

Expected: `GOFUMPT CLEAN`, then `0 issues.`, then the four recipe bodies echoed by `make -n`. `gofumpt -l` printing nothing before `GOFUMPT CLEAN` is the pass condition — it lists files that need formatting. `golangci-lint run` now reads `.golangci.json`, and `make -n go-lint` echoes `golangci-lint run`, which is the `0` from the red step turned into a `1`.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add .golangci.json .gitignore Makefile && \
  git commit -m "weather: the lint config, the pinned tools and the make targets

.golangci.json is the go-wallet-toolbox config with four edits, written out
literally rather than generated: the gci prefix, goimports local-prefixes, the
revive settings dropped (revive is disabled and .revive.toml does not exist
here) and the mage build tag dropped. Generating it from an absolute path into
another repository would have worked on exactly one machine.

nolintlint runs with allow-unused: false, so an unnecessary //nolint is itself
a lint failure. This port contains none.

golangci-lint v2.12.2 and gofumpt v0.10.0 are the versions go-wallet-toolbox
pins and the versions every lint decision in this plan was checked against.

The make targets are named go-* because this Makefile already has Docker-oriented
build and test targets that must keep working. No TypeScript is touched by this
commit or by any other in this plan."
```

---

## Task 2: The record types and the 33-field ordered schema

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/types.go`
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/schema.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/types_test.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/schema_test.go`

**Interfaces:**
- Consumes: the module and the `weather` package from Task 1a, and the lint config and make targets from Task 1b. No Go identifiers.
- Produces, all in `package weather`:
  - `const Version = 1` (untyped)
  - `const FloatScale = 1_000_000` (untyped)
  - `const FloatEpsilon = 1e-6` (untyped)
  - `const DataFieldsPerRecord = 33` (untyped)
  - `const ChunksPerRecord = 36` (untyped)
  - `const MaxScriptInt = int64(1)<<53 - 1` (typed `int64`)
  - `type FieldType uint8`, with `FieldInteger`, `FieldFloat`, `FieldString`, `FieldBoolean` in that iota order
  - `func (t FieldType) String() string` returning `"integer"`, `"float"`, `"string"`, `"boolean"`, `"unknown"`
  - `type FieldDefinition struct { Name string; Type FieldType }` — deliberately **no** `Required` field
  - `type WeatherData struct { ... }` — 33 exported fields, declared alphabetically by json tag: `AirDensity float64`, `StationPressure float64`, `IsPrecipLocalDayRainCheck bool`, `IsPrecipLocalYesterdayRainCheck bool`, `Conditions`/`Icon`/`LightningStrikeLastDistanceMsg`/`PressureTrend`/`WindDirectionCardinal` all `string`, and the remaining 24 `int64`
  - `var FieldSchema = []FieldDefinition{...}` — exactly 33 entries, strict alphabetical
  - `func (d *WeatherData) fieldPtrs() []any` — 33 pointers, index-aligned with `FieldSchema`, concrete types `*int64` / `*float64` / `*string` / `*bool`
  - test-scope helper `func jsonTags(t *testing.T) []string`, declared in `types_test.go` and used by `schema_test.go`

### Steps

- [ ] **Write the first failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/types_test.go` with exactly this content:

```go
package weather

import (
	"reflect"
	"slices"
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

	if ChunksPerRecord != 36 {
		t.Errorf("ChunksPerRecord = %d, want 36 (OP_FALSE, OP_RETURN, OP_1, 33 fields)", ChunksPerRecord)
	}

	if MaxScriptInt != 9007199254740991 {
		t.Errorf("MaxScriptInt = %d, want 9007199254740991 (2^53-1)", MaxScriptInt)
	}
}

func TestFieldTypeString(t *testing.T) {
	cases := map[FieldType]string{
		FieldInteger: "integer",
		FieldFloat:   "float",
		FieldString:  "string",
		FieldBoolean: "boolean",
		FieldType(9): "unknown",
	}

	for ft, want := range cases {
		if got := ft.String(); got != want {
			t.Errorf("FieldType(%d).String() = %q, want %q", ft, got, want)
		}
	}
}

// jsonTags returns the json tag of every WeatherData field, in declaration
// order. schema_test.go uses it too.
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

	sorted := slices.Clone(tags)
	slices.Sort(sorted)

	for i := range tags {
		if tags[i] != sorted[i] {
			t.Errorf("json tag %d is %q but alphabetical order wants %q: declaration order is the wire order",
				i, tags[i], sorted[i])
		}
	}

	if tags[0] != "air_density" {
		t.Errorf("first json tag = %q, want air_density", tags[0])
	}

	if tags[len(tags)-1] != "wind_gust" {
		t.Errorf("last json tag = %q, want wind_gust", tags[len(tags)-1])
	}
}

// TestWeatherDataIsComparable pins the property the round-trip tests rely on:
// every field is a scalar, so two records can be compared with ==. Adding a
// slice, map or pointer field would break that silently.
func TestWeatherDataIsComparable(t *testing.T) {
	if !reflect.TypeOf(WeatherData{}).Comparable() {
		t.Fatal("WeatherData is no longer comparable: a field was added that is not a scalar")
	}
}
```

- [ ] **Write the second failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/schema_test.go` with exactly this content. The name list is transcribed independently rather than derived from `FieldSchema`: a test that reads the value it is checking proves nothing.

```go
package weather

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// wantFieldNames is the wire order, transcribed independently from the
// TypeScript src/format/schema.ts. It is deliberately duplicated rather than
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
	}
}

func TestFieldSchemaIsStrictlyAlphabetical(t *testing.T) {
	names := make([]string, 0, len(FieldSchema))
	for _, f := range FieldSchema {
		names = append(names, f.Name)
	}

	sorted := slices.Clone(names)
	slices.Sort(sorted)

	for i := range names {
		if names[i] != sorted[i] {
			t.Fatalf("FieldSchema[%d] = %q but alphabetical order wants %q: port the order from schema.ts, never from ENCODING.md",
				i, names[i], sorted[i])
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

func TestSchemaNamesMatchJSONTags(t *testing.T) {
	tags := jsonTags(t)

	if len(tags) != len(FieldSchema) {
		t.Fatalf("%d json tags, %d schema entries", len(tags), len(FieldSchema))
	}

	for i, f := range FieldSchema {
		if tags[i] != f.Name {
			t.Errorf("index %d: json tag %q, schema name %q", i, tags[i], f.Name)
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
		if got := reflect.TypeOf(ptrs[i]).String(); got != wantType[f.Type] {
			t.Errorf("fieldPtrs[%d] (%s) is %s, want %s for schema type %s",
				i, f.Name, got, wantType[f.Type], f.Type)
		}
	}
}

// TestFieldPtrsAreDistinctAndInStructOrder writes a unique marker through every
// pointer and reads it back through JSON, so a copy-paste slip in fieldPtrs -
// the same struct field listed twice - cannot survive.
func TestFieldPtrsAreDistinctAndInStructOrder(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	for i, f := range FieldSchema {
		switch f.Type {
		case FieldInteger:
			p, ok := ptrs[i].(*int64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *int64", i, f.Name, ptrs[i])
			}

			*p = int64(i) + 1
		case FieldFloat:
			p, ok := ptrs[i].(*float64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *float64", i, f.Name, ptrs[i])
			}

			*p = float64(i) + 1
		case FieldString:
			p, ok := ptrs[i].(*string)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *string", i, f.Name, ptrs[i])
			}

			*p = f.Name
		case FieldBoolean:
			p, ok := ptrs[i].(*bool)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *bool", i, f.Name, ptrs[i])
			}

			*p = true
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
				t.Errorf("field %d (%s) = %v, want %v: the fieldPtrs index does not match the schema index",
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

- [ ] **Run the tests and see them fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -14
```

Expected: a compile failure listing undefined identifiers, then the build-failed line. The exact set shown depends on the order the compiler reports them and Go truncates after ten, so what matters is that every identifier is undefined and the package does not build:

```
# github.com/bsv-blockchain-demos/weather-proof/internal/weather [github.com/bsv-blockchain-demos/weather-proof/internal/weather.test]
internal/weather/schema_test.go:51:9: undefined: FieldSchema
internal/weather/schema_test.go:51:25: undefined: DataFieldsPerRecord
internal/weather/schema_test.go:52:50: undefined: FieldSchema
internal/weather/schema_test.go:52:64: undefined: DataFieldsPerRecord
internal/weather/schema_test.go:55:28: undefined: DataFieldsPerRecord
internal/weather/schema_test.go:56:78: undefined: DataFieldsPerRecord
internal/weather/schema_test.go:60:6: undefined: FieldSchema
internal/weather/schema_test.go:62:8: undefined: FieldSchema
internal/weather/schema_test.go:68:33: undefined: FieldSchema
internal/weather/schema_test.go:69:20: undefined: FieldSchema
internal/weather/schema_test.go:69:20: too many errors
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [build failed]
FAIL
```

- [ ] **Write the types.** Create `/Users/personal/git/demos/weather-chain/internal/weather/types.go` with exactly this content:

```go
package weather

// Version is the record schema version.
//
// WARNING: the version is emitted as a SINGLE OPCODE (OP_1 = 0x51), not a data
// push. At version 17 and above it stops being a one-byte opcode and becomes a
// data push (17 encodes as 0111), which silently changes the record prefix from
// 3 bytes to 4 and the chunk count from 36 to 37. Any bump is a coordinated
// change to encoder.go, decoder.go, ChunksPerRecord and the golden file, in one
// commit.
const Version = 1

const (
	// FloatScale is the fixed-point scale for the two float fields: 6 decimals.
	FloatScale = 1_000_000

	// FloatEpsilon is the comparison tolerance for a decoded float. Encoding is
	// lossy by design: a value is stored as round(v * FloatScale).
	FloatEpsilon = 1e-6

	// DataFieldsPerRecord is the number of schema fields in every record.
	DataFieldsPerRecord = 33

	// ChunksPerRecord is the chunk count of a well-formed record script:
	// OP_FALSE, OP_RETURN, the version opcode, then 33 field pushes.
	//
	// It is 36 and not 34 because script.DecodeOptionsParseOpReturn steps PAST
	// the 0x6a byte but still appends the OP_RETURN chunk. A guard written on a
	// 34 basis accepts a script truncated by two whole fields.
	ChunksPerRecord = 36
)

// MaxScriptInt is the largest magnitude this package will encode: 2^53 - 1.
//
// The bound is not a script limitation - script numbers are arbitrary width
// after Genesis. It is the range in which a value survives a round trip through
// JSON, whose numbers are IEEE-754 doubles, and every weather value arrives as
// JSON from the Tempest API and leaves as JSON to the browser.
const MaxScriptInt = int64(1)<<53 - 1

// FieldType is the wire type of a schema field.
type FieldType uint8

const (
	// FieldInteger is emitted with appendScriptNum.
	FieldInteger FieldType = iota
	// FieldFloat is scaled by FloatScale, rounded half away from zero, then
	// emitted with appendScriptNum.
	FieldFloat
	// FieldString is emitted as a UTF-8 data push.
	FieldString
	// FieldBoolean is emitted as appendScriptNum(0) or appendScriptNum(1).
	FieldBoolean
)

// String implements fmt.Stringer.
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

// FieldDefinition is one entry of the wire schema.
//
// There is deliberately no Required flag: the wire format has no optionality.
// All 33 fields are always emitted, in FieldSchema order, and a record that is
// missing one is not a shorter record - it is a malformed script.
type FieldDefinition struct {
	Name string
	Type FieldType
}

// WeatherData is one weather reading.
//
// The JSON tags are the Tempest field names and are also the keys of the golden
// file, so they must never be renamed. The declaration order is the same strict
// alphabetical order as FieldSchema, which is the wire order.
//
// Every field is a comparable scalar, so two records can be compared with ==.
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

- [ ] **Write the schema.** Create `/Users/personal/git/demos/weather-chain/internal/weather/schema.go` with exactly this content:

```go
package weather

// FieldSchema IS THE WIRE FORMAT.
//
// The order below is the on-chain field layout and must never change: strict
// alphabetical order, air_density first and wind_gust last. It is ported from
// the TypeScript src/format/schema.ts, which is the only authority for it.
//
// Do NOT port the order from ENCODING.md. That document's "Field Order" section
// lists a time-first, category-grouped order which was superseded on chain; it
// is kept as history, not as a specification.
//
// It is a SLICE, never a map. Ranging over a map would make the encoder
// non-deterministic, which is the single easiest way to corrupt this format.
var FieldSchema = []FieldDefinition{
	{Name: "air_density", Type: FieldFloat},                            //  0
	{Name: "air_temperature", Type: FieldInteger},                      //  1
	{Name: "brightness", Type: FieldInteger},                           //  2
	{Name: "conditions", Type: FieldString},                            //  3
	{Name: "delta_t", Type: FieldInteger},                              //  4
	{Name: "dew_point", Type: FieldInteger},                            //  5
	{Name: "feels_like", Type: FieldInteger},                           //  6
	{Name: "icon", Type: FieldString},                                  //  7
	{Name: "is_precip_local_day_rain_check", Type: FieldBoolean},       //  8
	{Name: "is_precip_local_yesterday_rain_check", Type: FieldBoolean}, //  9
	{Name: "lightning_strike_count_last_1hr", Type: FieldInteger},      // 10
	{Name: "lightning_strike_count_last_3hr", Type: FieldInteger},      // 11
	{Name: "lightning_strike_last_distance", Type: FieldInteger},       // 12
	{Name: "lightning_strike_last_distance_msg", Type: FieldString},    // 13
	{Name: "lightning_strike_last_epoch", Type: FieldInteger},          // 14
	{Name: "precip_accum_local_day", Type: FieldInteger},               // 15
	{Name: "precip_accum_local_yesterday", Type: FieldInteger},         // 16
	{Name: "precip_minutes_local_day", Type: FieldInteger},             // 17
	{Name: "precip_minutes_local_yesterday", Type: FieldInteger},       // 18
	{Name: "precip_probability", Type: FieldInteger},                   // 19
	{Name: "pressure_trend", Type: FieldString},                        // 20
	{Name: "relative_humidity", Type: FieldInteger},                    // 21
	{Name: "sea_level_pressure", Type: FieldInteger},                   // 22
	{Name: "solar_radiation", Type: FieldInteger},                      // 23
	{Name: "station_pressure", Type: FieldFloat},                       // 24
	{Name: "time", Type: FieldInteger},                                 // 25
	{Name: "uv", Type: FieldInteger},                                   // 26
	{Name: "wet_bulb_globe_temperature", Type: FieldInteger},           // 27
	{Name: "wet_bulb_temperature", Type: FieldInteger},                 // 28
	{Name: "wind_avg", Type: FieldInteger},                             // 29
	{Name: "wind_direction", Type: FieldInteger},                       // 30
	{Name: "wind_direction_cardinal", Type: FieldString},               // 31
	{Name: "wind_gust", Type: FieldInteger},                            // 32
}

// fieldPtrs returns pointers to the 33 wire fields of d in FieldSchema order.
//
// Index i of the result corresponds to index i of FieldSchema, and the concrete
// pointer type matches FieldSchema[i].Type:
//
//	FieldInteger -> *int64    FieldFloat   -> *float64
//	FieldString  -> *string   FieldBoolean -> *bool
//
// One list serves both the encoder (which reads through the pointers) and the
// decoder (which writes through them), so the two can never drift apart.
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
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/ -count=1 -v 2>&1 | grep -E '^(--- |ok|PASS|FAIL)'
```

Expected, exactly these ten lines:

```
--- PASS: TestFieldSchemaOrderAndCount (0.00s)
--- PASS: TestFieldSchemaIsStrictlyAlphabetical (0.00s)
--- PASS: TestFieldSchemaTypeTally (0.00s)
--- PASS: TestSchemaNamesMatchJSONTags (0.00s)
--- PASS: TestFieldPtrsMatchSchemaTypes (0.00s)
--- PASS: TestFieldPtrsAreDistinctAndInStructOrder (0.00s)
--- PASS: TestConstants (0.00s)
--- PASS: TestFieldTypeString (0.00s)
--- PASS: TestWeatherDataJSONTagsAreAlphabeticalAndComplete (0.00s)
--- PASS: TestWeatherDataIsComparable (0.00s)
PASS
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.29s
```

- [ ] **Run the full gate.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofumpt -l internal/ && make check && echo "GATE CLEAN"
```

Expected: `gofumpt -l` prints nothing, `make check` runs build, test and lint, `golangci-lint` prints `0 issues.`, and the last line is `GATE CLEAN`. If `gofumpt -l` names a file, run `gofumpt -w internal/` and re-run.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/types.go internal/weather/types_test.go \
          internal/weather/schema.go internal/weather/schema_test.go && \
  git commit -m "weather: the record types and the 33-field ordered schema

FieldSchema is a 33-entry SLICE, and its order IS the wire format: strict
alphabetical, air_density first and wind_gust last, ported from the TypeScript
src/format/schema.ts. It is never a map, because ranging over a map would make
the encoder non-deterministic.

schema_test.go transcribes the 33 names independently instead of deriving them
from FieldSchema, so the test is capable of disagreeing. It also sorts the json
tags rather than trusting the author, and writes a distinct marker through every
fieldPtrs entry so a duplicated struct field cannot survive.

FieldDefinition deliberately has no Required flag: the wire format has no
optionality. A record missing a field is a malformed script, not a short one.

The Version comment records why 17 is a breaking change: the version is a single
opcode, so at 17 the prefix silently grows from 3 bytes to 4 and the chunk count
from 36 to 37."
```

---

## Task 3: The number and float encoding primitives

Both directions of both primitives live here, so the boundary tests can assert a round trip at the primitive level before any record exists. `appendScriptNum` is deliberately not hand-rolled: `go-sdk` already implements exactly the little-endian sign-magnitude serialization required, and this task's job is to use it correctly rather than to reimplement it.

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum.go`
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/float.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum_test.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/float_test.go`
- Modify: `/Users/personal/git/demos/weather-chain/go.mod` and `/Users/personal/git/demos/weather-chain/go.sum` — regenerated by `go mod tidy`, which this task runs twice and commits. This is the first task that imports the SDK, so it is the first task in which tidy does anything but harm.

**Interfaces:**
- Consumes, from Task 2, all in `package weather`:
  - `const MaxScriptInt = int64(1)<<53 - 1`, `const FloatScale = 1_000_000`, `const FloatEpsilon = 1e-6`
- Consumes, from `go-sdk` v1.3.2:
  - `func (s *script.Script) AppendOpcodes(oo ...uint8) error`
  - `func (s *script.Script) AppendPushData(d []byte) error`
  - `type script.ScriptChunk struct { Op byte; Data []byte }`
  - `func interpreter.MakeScriptNumber(bb []byte, scriptNumLen int, requireMinimal, afterGenesis bool) (*interpreter.ScriptNumber, error)`
  - `type interpreter.ScriptNumber struct { Val *big.Int; AfterGenesis bool }` and `func (n *interpreter.ScriptNumber) Bytes() []byte`, `func (n *interpreter.ScriptNumber) Int64() int64`
- Produces, all in `package weather`:
  - `var ErrNumberOutOfRange = errors.New("number out of encodable range")`
  - `var ErrNotANumber = errors.New("chunk is not a number")`
  - `var ErrNonFinite = errors.New("value is not finite")`
  - `func appendScriptNum(s *script.Script, n int64) error`
  - `func scriptNumFromChunk(c *script.ScriptChunk) (int64, error)`
  - `func scaleFloat(v float64) (int64, error)`
  - `func unscaleFloat(n int64) float64`

### Steps

- [ ] **Write the failing number test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum_test.go` with exactly this content:

```go
package weather

import (
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// TestAppendScriptNumTable pins the number encoding. Every hex string here was
// measured against go-sdk v1.3.2; if any of them changes, the wire format has
// changed and every published record has become unreadable.
func TestAppendScriptNumTable(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "00"},                           // OP_0
		{-1, "4f"},                          // OP_1NEGATE
		{1, "51"},                           // OP_1
		{2, "52"},                           // OP_2
		{16, "60"},                          // OP_16, the last opcode branch
		{17, "0111"},                        // the first data push
		{127, "017f"},                       // largest single byte with no sign bit
		{128, "028000"},                     // needs a sign-extension byte
		{-128, "028080"},                    // sign-extension byte set to 0x80
		{255, "02ff00"},                     //
		{256, "020001"},                     // little-endian
		{-256, "020081"},                    //
		{-127, "01ff"},                      // sign bit folded into the top byte
		{2147483647, "04ffffff7f"},          // max int32
		{2147483648, "050000008000"},        // one past it - five bytes, not four
		{-2147483648, "050000008080"},       //
		{MaxScriptInt, "07ffffffffffff1f"},  // 2^53-1
		{-MaxScriptInt, "07ffffffffffff9f"}, //
	}

	for _, tc := range cases {
		s := &script.Script{}
		if err := appendScriptNum(s, tc.n); err != nil {
			t.Errorf("appendScriptNum(%d) returned %v, want nil", tc.n, err)

			continue
		}

		if got := s.String(); got != tc.want {
			t.Errorf("appendScriptNum(%d) = %s, want %s", tc.n, got, tc.want)
		}
	}
}

// TestAppendScriptNumEmitsAllSixteenOpcodes walks 1..16 so an off-by-one in
// script.Op1 + byte(n) - 1 cannot hide.
func TestAppendScriptNumEmitsAllSixteenOpcodes(t *testing.T) {
	for n := int64(1); n <= 16; n++ {
		s := &script.Script{}
		if err := appendScriptNum(s, n); err != nil {
			t.Fatalf("appendScriptNum(%d): %v", n, err)
		}

		b := s.Bytes()
		if len(b) != 1 {
			t.Fatalf("appendScriptNum(%d) emitted %d bytes, want 1", n, len(b))
		}

		want := byte(0x50) + byte(n)
		if b[0] != want {
			t.Errorf("appendScriptNum(%d) = %#02x, want %#02x", n, b[0], want)
		}
	}
}

func TestAppendScriptNumOutOfRange(t *testing.T) {
	for _, n := range []int64{MaxScriptInt + 1, -MaxScriptInt - 1} {
		s := &script.Script{}

		err := appendScriptNum(s, n)
		if !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("appendScriptNum(%d) error = %v, want ErrNumberOutOfRange", n, err)
		}

		if len(*s) != 0 {
			t.Errorf("appendScriptNum(%d) wrote %d bytes on failure, want 0", n, len(*s))
		}
	}
}

// TestScriptNumberBytesMutatesItsReceiver documents the go-sdk gotcha that
// forces appendScriptNum to build a fresh ScriptNumber on every call. If a
// future SDK release fixes it this test fails, which is the correct outcome: the
// comment in scriptnum.go then needs updating.
func TestScriptNumberBytesMutatesItsReceiver(t *testing.T) {
	first := &script.Script{}
	if err := appendScriptNum(first, -128); err != nil {
		t.Fatalf("first: %v", err)
	}

	second := &script.Script{}
	if err := appendScriptNum(second, -128); err != nil {
		t.Fatalf("second: %v", err)
	}

	if first.String() != second.String() {
		t.Fatalf("two calls with -128 gave %s then %s: a ScriptNumber is being reused across calls",
			first.String(), second.String())
	}
}

// TestScriptNumRoundTrip is the property that makes the decoder possible.
func TestScriptNumRoundTrip(t *testing.T) {
	values := []int64{
		0, 1, -1, 2, -2, 15, 16, 17, -16, -17, 127, -127, 128, -128, 255, -255,
		256, -256, 32767, -32768, 65535, 16777215, 2147483647, -2147483648,
		4294967296, MaxScriptInt, -MaxScriptInt,
	}

	for _, n := range values {
		s := &script.Script{}
		if err := appendScriptNum(s, n); err != nil {
			t.Fatalf("appendScriptNum(%d): %v", n, err)
		}

		chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Fatalf("DecodeScript(%s): %v", s.String(), err)
		}

		if len(chunks) != 1 {
			t.Fatalf("appendScriptNum(%d) produced %d chunks, want 1", n, len(chunks))
		}

		got, err := scriptNumFromChunk(chunks[0])
		if err != nil {
			t.Fatalf("scriptNumFromChunk(%s): %v", s.String(), err)
		}

		if got != n {
			t.Errorf("round trip of %d gave %d (via %s)", n, got, s.String())
		}
	}
}

func TestScriptNumFromChunkRejectsNonNumbers(t *testing.T) {
	cases := []struct {
		name  string
		chunk *script.ScriptChunk
	}{
		{"OP_RETURN", &script.ScriptChunk{Op: script.OpRETURN}},
		{"OP_DUP", &script.ScriptChunk{Op: script.OpDUP}},
		{"OP_RESERVED", &script.ScriptChunk{Op: 0x50}},
		{"non-minimal 7f00", &script.ScriptChunk{Op: script.OpDATA2, Data: []byte{0x7f, 0x00}}},
		{"negative zero 80", &script.ScriptChunk{Op: script.OpDATA1, Data: []byte{0x80}}},
		{"nine bytes", &script.ScriptChunk{
			Op:   script.OpDATA9,
			Data: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9},
		}},
	}

	for _, tc := range cases {
		if _, err := scriptNumFromChunk(tc.chunk); err == nil {
			t.Errorf("scriptNumFromChunk(%s) returned nil error, want a rejection", tc.name)
		}
	}
}

// TestScriptNumFromChunkRejectsOversizedMagnitude covers the 8-byte push that is
// short enough for MakeScriptNumber but larger than MaxScriptInt.
func TestScriptNumFromChunkRejectsOversizedMagnitude(t *testing.T) {
	// 0xffffffffffffff7f little-endian = 2^63-1, minimally encoded.
	chunk := &script.ScriptChunk{
		Op:   script.OpDATA8,
		Data: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f},
	}

	_, err := scriptNumFromChunk(chunk)
	if !errors.Is(err, ErrNumberOutOfRange) {
		t.Errorf("error = %v, want ErrNumberOutOfRange", err)
	}
}
```

- [ ] **Write the failing float test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/float_test.go` with exactly this content:

```go
package weather

import (
	"errors"
	"math"
	"testing"
)

// TestScaleFloatRoundingRule is the documented rule, asserted. Half away from
// zero, Go's math.Round. The four negative-half cases are the ones where
// JavaScript's Math.round disagrees, and the JS column is recorded so nobody
// "fixes" this back toward JavaScript by accident.
func TestScaleFloatRoundingRule(t *testing.T) {
	cases := []struct {
		in     float64
		want   int64
		wantJS int64 // what ECMAScript Math.round would have produced
	}{
		{-1.2345675, -1234568, -1234567},
		{-0.0000005, -1, 0},
		{-1.5e-6, -2, -1},
		{-2.5e-6, -3, -2},
		{1.5e-6, 2, 2},
		{2.5e-6, 3, 3},
		{0.0000005, 1, 1},
		{0, 0, 0},
		{-0, 0, 0},
		{1.29, 1290000, 1290000},
		{979.7, 979700000, 979700000},
		{999.999999, 999999999, 999999999},
		{1234.56789, 1234567890, 1234567890},
		{-1.234567, -1234567, -1234567},
		{-0.000001, -1, -1},
	}

	for _, tc := range cases {
		got, err := scaleFloat(tc.in)
		if err != nil {
			t.Errorf("scaleFloat(%v) returned %v, want nil", tc.in, err)

			continue
		}

		if got != tc.want {
			t.Errorf("scaleFloat(%v) = %d, want %d (JavaScript would give %d; the Go rule is half away from zero)",
				tc.in, got, tc.want, tc.wantJS)
		}
	}
}

func TestScaleFloatRejectsNonFinite(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := scaleFloat(v)
		if !errors.Is(err, ErrNonFinite) {
			t.Errorf("scaleFloat(%v) error = %v, want ErrNonFinite", v, err)
		}
	}
}

func TestScaleFloatRejectsOutOfRange(t *testing.T) {
	// 1e10 * 1e6 = 1e16, which is above 2^53-1 = 9.007e15.
	for _, v := range []float64{1e10, -1e10, 1e300} {
		_, err := scaleFloat(v)
		if !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("scaleFloat(%v) error = %v, want ErrNumberOutOfRange", v, err)
		}
	}
}

// TestScaleFloatBoundary pins both sides of the MaxScriptInt edge.
//
// Note that 2^53-1 is NOT reachable through the float path: 9007199254.740991
// is not exactly representable, and scaling it back up lands on 9007199254740992
// - one above the bound - so it is correctly rejected. The largest value that
// passes is therefore stated as a round number.
func TestScaleFloatBoundary(t *testing.T) {
	got, err := scaleFloat(9e9)
	if err != nil {
		t.Fatalf("scaleFloat(9e9) returned %v, want nil", err)
	}

	if got != 9_000_000_000_000_000 {
		t.Errorf("scaleFloat(9e9) = %d, want 9000000000000000", got)
	}

	onEdge := 9_007_199_254.74

	got, err = scaleFloat(onEdge)
	if err != nil {
		t.Fatalf("scaleFloat(%v) returned %v, want nil", onEdge, err)
	}

	if got != 9_007_199_254_740_000 {
		t.Errorf("scaleFloat(%v) = %d, want 9007199254740000", onEdge, got)
	}

	overEdge := 9_007_199_254.741
	if _, err := scaleFloat(overEdge); !errors.Is(err, ErrNumberOutOfRange) {
		t.Errorf("scaleFloat(%v) error = %v, want ErrNumberOutOfRange", overEdge, err)
	}
}

func TestUnscaleFloat(t *testing.T) {
	cases := []struct {
		in   int64
		want float64
	}{
		{0, 0},
		{1, 0.000001},
		{-1, -0.000001},
		{1290000, 1.29},
		{979700000, 979.7},
		{999999999, 999.999999},
		{-1234567, -1.234567},
	}

	for _, tc := range cases {
		if got := unscaleFloat(tc.in); math.Abs(got-tc.want) > FloatEpsilon {
			t.Errorf("unscaleFloat(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestFloatRoundTripIsLossyOnlyBelowEpsilon states the precision contract: a
// value that is an exact multiple of 1e-6 survives exactly, and anything finer
// is quantized, not preserved.
func TestFloatRoundTripIsLossyOnlyBelowEpsilon(t *testing.T) {
	exact := []float64{0, 1.29, 979.7, 999.999999, -1.234567, 1234.56789}
	for _, v := range exact {
		n, err := scaleFloat(v)
		if err != nil {
			t.Fatalf("scaleFloat(%v): %v", v, err)
		}

		if got := unscaleFloat(n); got != v {
			t.Errorf("round trip of %v gave %v, want it exact", v, got)
		}
	}

	// 7 decimals: the last digit is discarded, and the result differs from the
	// input by less than FloatEpsilon.
	n, err := scaleFloat(1.2345678)
	if err != nil {
		t.Fatalf("scaleFloat(1.2345678): %v", err)
	}

	if n != 1234568 {
		t.Errorf("scaleFloat(1.2345678) = %d, want 1234568", n)
	}

	if diff := math.Abs(unscaleFloat(n) - 1.2345678); diff > FloatEpsilon {
		t.Errorf("quantization error %v exceeds FloatEpsilon", diff)
	}
}
```

- [ ] **Tidy the module now that the SDK is actually imported.** This step comes BEFORE the red gate, and the order is load-bearing. `scriptnum_test.go`, just written, is the first file in the repository to import `github.com/bsv-blockchain/go-sdk`, and Task 1a's `go.sum` has two lines and no transitive entries, so `go test` right now fails at package load instead of at type checking. Measured on go1.26.3:

```
# github.com/bsv-blockchain-demos/weather-proof/internal/weather
/Users/personal/go/pkg/mod/github.com/bsv-blockchain/go-sdk@v1.3.2/primitives/hash/hash.go:8:2: missing go.sum entry for module providing package golang.org/x/crypto/ripemd160 (imported by github.com/bsv-blockchain/go-sdk/primitives/hash); to add:
	go get github.com/bsv-blockchain/go-sdk/primitives/hash@v1.3.2
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather [setup failed]
FAIL
```

The leading path is your module cache (`go env GOMODCACHE`), so it differs per machine; the rest of the text does not.

That is a setup failure, not a red test, and it would hide the TDD gate. `go mod tidy` resolves imports without type-checking bodies, so it succeeds even though `scaleFloat` and friends do not exist yet — verified: exit status 0 with both test files present and every primitive undefined. Run:

```bash
cd /Users/personal/git/demos/weather-chain && go mod tidy && cat go.mod && grep -c '' go.sum
```

Expected — and this is a REAL diff to `go.mod` and `go.sum`, which is why both are in this task's commit:

```
module github.com/bsv-blockchain-demos/weather-proof

go 1.26.3

require github.com/bsv-blockchain/go-sdk v1.3.2

require (
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/crypto v0.54.0 // indirect
)
```

followed by `14`. Three things changed from Task 1a's four-line `go.mod`: the `// indirect` marker is dropped from the go-sdk line (a file in this module now imports the SDK directly — a `_test.go` file of a package in the main module counts as a direct import), a second `require` block appears with the SDK's two transitive dependencies, and `go.sum` grows from 2 lines to 14. All three are expected: this is the first task that imports the SDK, so this is the first point at which `go mod tidy` is both safe and necessary.

`go.sum` reaches its final 16 lines two steps later, when `scriptnum.go` adds the `script/interpreter` import; a second `go mod tidy` step below records those two lines, and `go.mod` does not change again.

- [ ] **Run the tests and see them fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -12
```

Expected, exactly — a compile failure naming the undefined primitives and errors. Go truncates after ten, and this is the measured list:

```
# github.com/bsv-blockchain-demos/weather-proof/internal/weather [github.com/bsv-blockchain-demos/weather-proof/internal/weather.test]
internal/weather/float_test.go:37:15: undefined: scaleFloat
internal/weather/float_test.go:53:13: undefined: scaleFloat
internal/weather/float_test.go:54:22: undefined: ErrNonFinite
internal/weather/float_test.go:63:13: undefined: scaleFloat
internal/weather/float_test.go:64:22: undefined: ErrNumberOutOfRange
internal/weather/float_test.go:77:14: undefined: scaleFloat
internal/weather/float_test.go:88:13: undefined: scaleFloat
internal/weather/float_test.go:98:15: undefined: scaleFloat
internal/weather/float_test.go:98:53: undefined: ErrNumberOutOfRange
internal/weather/float_test.go:118:13: undefined: unscaleFloat
internal/weather/float_test.go:118:13: too many errors
```

- [ ] **Write the number primitive.** Create `/Users/personal/git/demos/weather-chain/internal/weather/scriptnum.go` with exactly this content:

```go
package weather

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/script/interpreter"
)

// ErrNumberOutOfRange is returned for a value whose magnitude exceeds
// MaxScriptInt, in either direction.
var ErrNumberOutOfRange = errors.New("number out of encodable range")

// ErrNotANumber is returned when a chunk in a numeric field position is not a
// number push or a small-integer opcode.
var ErrNotANumber = errors.New("chunk is not a number")

// appendScriptNum appends n to s using MINIMAL push encoding.
//
// The three opcode branches come first because minimal push encoding is a
// script-validity rule, and they are also why a small value costs one byte
// instead of two:
//
//	n == 0            -> OP_0        (0x00)
//	n == -1           -> OP_1NEGATE  (0x4f)
//	1 <= n <= 16      -> OP_1..OP_16 (0x51..0x60)
//	anything else     -> a data push of the little-endian sign-magnitude bytes
//
// The serialization for the last branch is NOT hand-rolled. go-sdk already
// implements exactly it in script/interpreter, and its output was measured at
// v1.3.2: 17 -> 0111, 127 -> 017f, 128 -> 028000, -128 -> 028080,
// 2147483647 -> 04ffffff7f, 9007199254740991 -> 07ffffffffffff1f.
//
// GOTCHA, verified at v1.3.2: (*interpreter.ScriptNumber).Bytes() calls n.Neg()
// on a negative value and never restores the sign, so it MUTATES its receiver.
// Measured: a ScriptNumber holding -128 returns 8080 on the first call and 8000
// on the second, with Val left at +128. A fresh ScriptNumber is therefore
// constructed on every call - never cache one, never Set() and reuse it.
//
// AfterGenesis is set explicitly true. At v1.3.2 it only sizes a slice, but the
// !AfterGenesis branch clamps to int32 and a future release could make that
// load-bearing; the intent here is full-width values.
//
// Do NOT use (*script.Script).AppendBigInt: it is AppendPushData(bInt.Bytes()),
// which is big-endian magnitude with NO sign byte. Measured: it encodes -128 as
// 0180, i.e. as +128. Undecodable.
func appendScriptNum(s *script.Script, n int64) error {
	if n > MaxScriptInt || n < -MaxScriptInt {
		return fmt.Errorf("%w: %d", ErrNumberOutOfRange, n)
	}

	switch {
	case n == 0:
		return s.AppendOpcodes(script.Op0)
	case n == -1:
		return s.AppendOpcodes(script.Op1NEGATE)
	case n >= 1 && n <= 16:
		// n is in [1,16] so the sum is in [0x51,0x60]; it cannot overflow.
		return s.AppendOpcodes(script.Op1 + byte(n) - 1)
	default:
		sn := &interpreter.ScriptNumber{Val: big.NewInt(n), AfterGenesis: true}

		return s.AppendPushData(sn.Bytes())
	}
}

// scriptNumFromChunk is the exact inverse of appendScriptNum.
//
// It reads the small-integer opcodes from the opcode byte itself, because those
// chunks carry no Data, and everything else through
// interpreter.MakeScriptNumber with requireMinimal set. Requiring minimal
// encoding is what makes a hand-forged script fail loudly: 7f00 is rejected as
// non-minimal even though it decodes numerically to 127.
func scriptNumFromChunk(c *script.ScriptChunk) (int64, error) {
	switch {
	case c.Op == script.Op0:
		return 0, nil
	case c.Op == script.Op1NEGATE:
		return -1, nil
	case c.Op >= script.Op1 && c.Op <= script.Op16:
		return int64(c.Op-script.Op1) + 1, nil
	case c.Op >= script.OpDATA1 && c.Op <= script.OpPUSHDATA4:
		// scriptNumLen 8 admits the 7 bytes that 2^53-1 needs plus a sign byte.
		sn, err := interpreter.MakeScriptNumber(c.Data, 8, true, true)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", ErrNotANumber, err)
		}

		n := sn.Int64()
		if n > MaxScriptInt || n < -MaxScriptInt {
			return 0, fmt.Errorf("%w: %d", ErrNumberOutOfRange, n)
		}

		return n, nil
	default:
		return 0, fmt.Errorf("%w: opcode %#02x", ErrNotANumber, c.Op)
	}
}
```

- [ ] **Write the float primitive.** Create `/Users/personal/git/demos/weather-chain/internal/weather/float.go` with exactly this content:

```go
package weather

import (
	"errors"
	"fmt"
	"math"
)

// ErrNonFinite is returned for NaN and for either infinity.
var ErrNonFinite = errors.New("value is not finite")

// scaleFloat converts a physical value to its on-chain integer representation.
//
// THE ROUNDING RULE, chosen deliberately and documented here because it is
// observable in the bytes:
//
//	Scaled floats are rounded HALF AWAY FROM ZERO, using Go's math.Round.
//	-1.5e-6 therefore encodes as -2, not -1.
//
// This is Go's native behavior, it is symmetric about zero, and it is
// deliberately NOT JavaScript's Math.round, which rounds half toward +Infinity
// and would give -1. The old TypeScript encoder used Math.round; nothing outside
// this backend decodes these values, so no consumer can observe the difference,
// and the golden file pins the rule against accidental change.
//
// Measured divergences from ECMAScript, all four asserted in float_test.go:
//
//	value       Go (this rule)   JavaScript Math.round
//	-1.2345675      -1234568              -1234567
//	-0.0000005            -1                     0
//	-1.5e-6               -2                    -1
//	-2.5e-6               -3                    -2
//
// The non-finite guard is load-bearing and is not a style choice: in Go,
// int64(math.NaN()) is implementation-defined, so without it one junk reading
// from the weather API would commit arbitrary bytes to mainnet with no error
// raised anywhere. The range check likewise runs BEFORE the conversion, so an
// overflowing float can never reach int64.
func scaleFloat(v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: %v", ErrNonFinite, v)
	}

	scaled := math.Round(v * FloatScale)
	if scaled > float64(MaxScriptInt) || scaled < -float64(MaxScriptInt) {
		return 0, fmt.Errorf("%w: %v scaled to %v", ErrNumberOutOfRange, v, scaled)
	}

	return int64(scaled), nil
}

// unscaleFloat is the inverse of scaleFloat, to within FloatEpsilon.
//
// It is lossy by construction: the wire carries round(v*FloatScale), so any
// precision finer than 1e-6 was discarded at encode time and cannot come back.
func unscaleFloat(n int64) float64 {
	return float64(n) / FloatScale
}
```

- [ ] **Tidy once more, now that `script/interpreter` is imported too.** `scriptnum.go` added an import that no file had when the earlier tidy ran, so two `golang.org/x/sync` hash lines are still missing from `go.sum`. Nothing is broken without this — the package builds and tests pass on the 14-line `go.sum` — but leaving it undone means the next person to run `go mod tidy` gets an unexplained diff. Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  cp go.mod go.mod.before && go mod tidy && \
  diff go.mod.before go.mod && echo "GO.MOD UNCHANGED" && \
  rm go.mod.before && grep -c '' go.sum
```

Expected, exactly: `GO.MOD UNCHANGED`, then `16`. `go.mod` stays byte-identical to the block printed two steps ago, because `script/interpreter` is another package of a module that is already required. Only `go.sum` grows, and a third `go mod tidy` changes neither file — verified idempotent from here on. `go.mod.before` is deleted by the same command, so it cannot be committed by accident.

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofumpt -l internal/ && go test ./internal/weather/ -count=1 -run 'ScriptNum|Float|Unscale|Scale' -v 2>&1 | grep -E '^(--- |ok|PASS|FAIL)'
```

Expected: `gofumpt -l` prints nothing, then exactly these thirteen tests. Note that the filter `ScriptNum` also matches `TestAppendScriptNum*` and `TestScriptNumberBytes*`, which is intended. Go runs test files in alphabetical order, so `float_test.go` reports before `scriptnum_test.go`:

```
--- PASS: TestScaleFloatRoundingRule (0.00s)
--- PASS: TestScaleFloatRejectsNonFinite (0.00s)
--- PASS: TestScaleFloatRejectsOutOfRange (0.00s)
--- PASS: TestScaleFloatBoundary (0.00s)
--- PASS: TestUnscaleFloat (0.00s)
--- PASS: TestFloatRoundTripIsLossyOnlyBelowEpsilon (0.00s)
--- PASS: TestAppendScriptNumTable (0.00s)
--- PASS: TestAppendScriptNumEmitsAllSixteenOpcodes (0.00s)
--- PASS: TestAppendScriptNumOutOfRange (0.00s)
--- PASS: TestScriptNumberBytesMutatesItsReceiver (0.00s)
--- PASS: TestScriptNumRoundTrip (0.00s)
--- PASS: TestScriptNumFromChunkRejectsNonNumbers (0.00s)
--- PASS: TestScriptNumFromChunkRejectsOversizedMagnitude (0.00s)
PASS
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.27s
```

- [ ] **Run the full gate.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make check && echo "GATE CLEAN"
```

Expected: build and all tests pass, `golangci-lint` prints `0 issues.`, and the last line is `GATE CLEAN`. Note in particular that `gosec` does **not** flag `script.Op1 + byte(n) - 1` or `int64(scaled)` — its range analysis handles both — so no `//nolint` is needed and adding one would fail `nolintlint`.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add go.mod go.sum internal/weather/scriptnum.go internal/weather/scriptnum_test.go \
          internal/weather/float.go internal/weather/float_test.go && \
  git commit -m "weather: the number and float encoding primitives

appendScriptNum is minimal push encoding: OP_0, OP_1NEGATE and OP_1..OP_16 for
the small values, and go-sdk's own interpreter.ScriptNumber.Bytes() for the
magnitude. The serialization is NOT hand-rolled and the SDK is NOT emulated.

Two go-sdk gotchas are documented in the source and one is pinned by a test:
Bytes() mutates its receiver on a negative value (a cached ScriptNumber holding
-128 returns 8080 then 8000), and AppendBigInt encodes -128 as +128 because it
is big-endian magnitude with no sign byte.

The float rule is stated and asserted: half away from zero, math.Round. It is
deliberately NOT JavaScript's Math.round, and float_test.go records what
JavaScript would have produced for the four negative-half cases so nobody drifts
back toward it.

The non-finite guard is load-bearing, not stylistic: int64(math.NaN()) is
implementation-defined in Go, so without it one junk API reading would commit
arbitrary bytes to mainnet with no error raised anywhere.

go.mod and go.sum change here because this is the first task to import the SDK:
go mod tidy drops the // indirect marker from the go-sdk require, adds the two
transitive requires (github.com/pkg/errors, golang.org/x/crypto) and takes
go.sum from 2 lines to 16. Tidy runs before the red gate on purpose - on the
two-line go.sum from the bootstrap, go test fails with 'missing go.sum entry'
at package load and the undefined-identifier red state is never observable."
```

---

## Task 4: The encoder, the 297-byte cap, determinism and the golden file

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/encoder.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/golden_test.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/encoder_test.go`
- Create (generated, committed): `/Users/personal/git/demos/weather-chain/internal/weather/testdata/golden/records.json`

**Interfaces:**
- Consumes, from Task 2: `const Version = 1`, `const FloatScale = 1_000_000`, `const DataFieldsPerRecord = 33`, `const ChunksPerRecord = 36`, `const MaxScriptInt = int64(1)<<53 - 1`, `type FieldType uint8` with `FieldInteger`/`FieldFloat`/`FieldString`/`FieldBoolean`, `type FieldDefinition struct { Name string; Type FieldType }`, `type WeatherData struct{...}`, `var FieldSchema []FieldDefinition`, `func (d *WeatherData) fieldPtrs() []any`.
- Consumes, from Task 3: `func appendScriptNum(s *script.Script, n int64) error`, `func scriptNumFromChunk(c *script.ScriptChunk) (int64, error)`, `func scaleFloat(v float64) (int64, error)`, `var ErrNumberOutOfRange`, `var ErrNonFinite`.
- Consumes, from `go-sdk` v1.3.2: `func (s *script.Script) AppendOpcodes(oo ...uint8) error`, `func (s *script.Script) AppendPushData(d []byte) error`, `func (s *script.Script) Bytes() []byte`, `func (s *script.Script) String() string`, `func (s *script.Script) IsData() bool`, `func script.DecodeScript(b []byte, options ...script.DecodeOptions) ([]*script.ScriptChunk, error)`, `script.DecodeOptionsParseOpReturn`.
- Produces, all in `package weather`:
  - `const MaxScriptBytes = 297`
  - `const maxPushDataLen = 0xFFFF`
  - `var ErrScriptTooLarge = errors.New("script exceeds the maximum size")`
  - `var ErrStringTooLong = errors.New("string field exceeds the maximum push size")`
  - `var ErrUnknownFieldType = errors.New("unknown field type in schema")`
  - `func Encode(d *WeatherData) (*script.Script, error)`
  - `func EncodeHex(d *WeatherData) (string, error)`
  - `func appendField(s *script.Script, f FieldDefinition, ptr any) error`
- Produces, in `package weather` test scope (Task 5 consumes all of these):
  - `const goldenPath = "testdata/golden/records.json"`
  - `var updateGolden = flag.Bool("update", false, "rewrite testdata/golden/records.json from the current encoder")` (type `*bool`)
  - `type goldenFile struct { FormatVersion int; SchemaFieldCount int; Records []goldenRecord }`
  - `type goldenRecord struct { Name string; ScriptLen int; ScriptHex string; Data WeatherData }`
  - `type goldenCase struct { Name string; Data WeatherData }`
  - `func goldenCases() []goldenCase`
  - `func onCapRecord() WeatherData`

### Steps

- [ ] **Write the golden-file test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/golden_test.go` with exactly this content. It carries the frozen input set, so it is written before the encoder exists.

```go
package weather

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

// updateGolden rewrites goldenPath instead of comparing against it. Run:
//
//	go test ./internal/weather -run TestGolden -update
//
// Plain `go test` VERIFIES. CI additionally runs
// `git diff --exit-code internal/weather/testdata/golden/`, so a drifting
// encoder cannot be laundered by regenerating the file.
var updateGolden = flag.Bool("update", false, "rewrite testdata/golden/records.json from the current encoder")

// goldenPath is a string literal, not a value built at run time. That is
// deliberate: os.ReadFile on a computed path trips gosec G304, and the fix for
// that is an explained //nolint, which nolintlint then polices. A constant path
// has neither problem.
const goldenPath = "testdata/golden/records.json"

// goldenFile is the on-disk shape.
//
// FormatVersion and SchemaFieldCount are the ONLY provenance recorded. There is
// deliberately no generatedAt and no git revision: a timestamp differs between
// two runs seconds apart, and a git revision is stable within a run but changes
// on the very next commit, so either one makes regenerate-and-diff fail forever.
// A golden file must be a pure function of the code under test.
type goldenFile struct {
	FormatVersion    int            `json:"formatVersion"`
	SchemaFieldCount int            `json:"schemaFieldCount"`
	Records          []goldenRecord `json:"records"`
}

type goldenRecord struct {
	Name      string      `json:"name"`
	ScriptLen int         `json:"scriptLen"`
	ScriptHex string      `json:"scriptHex"`
	Data      WeatherData `json:"data"`
}

// goldenCase is one input record. decoder_test.go consumes goldenCases() too.
type goldenCase struct {
	Name string
	Data WeatherData
}

// goldenCases returns the frozen input set, in a fixed order.
//
//   - minimal  the all-zero floor: every field encodes as a single 0x00 byte
//   - sample   the repository's real Tempest reading
//   - extreme  the repository's adversarial fixture: unicode, punctuation, int32
//     maxima
//   - negative every numeric field negative, exercising OP_1NEGATE and the
//     sign-extension byte
//   - oncap    sits exactly on MaxScriptBytes, so a future field addition that
//     pushes the worst case over the line fails here
func goldenCases() []goldenCase {
	return []goldenCase{
		{Name: "minimal", Data: WeatherData{}},
		{Name: "sample", Data: WeatherData{
			AirDensity:                      1.29,
			AirTemperature:                  -9,
			Brightness:                      68055,
			Conditions:                      "Clear",
			DeltaT:                          2,
			DewPoint:                        -17,
			FeelsLike:                       -13,
			Icon:                            "clear-day",
			IsPrecipLocalDayRainCheck:       true,
			IsPrecipLocalYesterdayRainCheck: true,
			LightningStrikeLastDistance:     32,
			LightningStrikeLastDistanceMsg:  "30 - 34 km",
			LightningStrikeLastEpoch:        1761103981,
			PressureTrend:                   "falling",
			RelativeHumidity:                49,
			SeaLevelPressure:                1019,
			SolarRadiation:                  567,
			StationPressure:                 979.7,
			Time:                            1769529302,
			UV:                              2,
			WetBulbGlobeTemperature:         -9,
			WetBulbTemperature:              -11,
			WindAvg:                         2,
			WindDirection:                   280,
			WindDirectionCardinal:           "W",
			WindGust:                        4,
		}},
		{Name: "extreme", Data: WeatherData{
			AirDensity:                     999.999999,
			AirTemperature:                 -100,
			Brightness:                     999999,
			Conditions:                     "Extreme conditions with special chars: !@#$%^&*()",
			DeltaT:                         50,
			DewPoint:                       -50,
			FeelsLike:                      -120,
			Icon:                           "extreme-weather-⚡️",
			IsPrecipLocalDayRainCheck:      true,
			LightningStrikeCountLast1hr:    999,
			LightningStrikeCountLast3hr:    9999,
			LightningStrikeLastDistance:    999,
			LightningStrikeLastDistanceMsg: "Very far away with unicode: ⚡⚡",
			LightningStrikeLastEpoch:       2147483647,
			PrecipAccumLocalDay:            999,
			PrecipAccumLocalYesterday:      999,
			PrecipMinutesLocalDay:          1440,
			PrecipMinutesLocalYesterday:    1440,
			PrecipProbability:              100,
			PressureTrend:                  "rapidly falling",
			RelativeHumidity:               100,
			SeaLevelPressure:               2000,
			SolarRadiation:                 9999,
			StationPressure:                1234.56789,
			Time:                           2147483647,
			UV:                             20,
			WetBulbGlobeTemperature:        60,
			WetBulbTemperature:             50,
			WindAvg:                        200,
			WindDirection:                  359,
			WindDirectionCardinal:          "NNE",
			WindGust:                       300,
		}},
		{Name: "negative", Data: WeatherData{
			AirDensity:                     -1.234567,
			AirTemperature:                 -1,
			Brightness:                     -68055,
			Conditions:                     "neg",
			DeltaT:                         -1,
			DewPoint:                       -128,
			FeelsLike:                      -256,
			Icon:                           "n",
			LightningStrikeCountLast1hr:    -1,
			LightningStrikeCountLast3hr:    -16,
			LightningStrikeLastDistance:    -17,
			LightningStrikeLastEpoch:       -2147483648,
			PrecipAccumLocalDay:            -1,
			PrecipAccumLocalYesterday:      -1,
			PrecipMinutesLocalDay:          -1,
			PrecipMinutesLocalYesterday:    -1,
			PrecipProbability:              -1,
			RelativeHumidity:               -1,
			SeaLevelPressure:               -1,
			SolarRadiation:                 -1,
			StationPressure:                -0.000001,
			Time:                           -MaxScriptInt,
			UV:                             -1,
			WetBulbGlobeTemperature:        -1,
			WetBulbTemperature:             -1,
			WindAvg:                        -1,
			WindDirection:                  -1,
			LightningStrikeLastDistanceMsg: "",
			WindGust:                       -1,
		}},
		{Name: "oncap", Data: onCapRecord()},
	}
}

// onCapRecord returns a record that encodes to EXACTLY MaxScriptBytes.
//
// The arithmetic, so it can be re-derived rather than trusted: the all-zero
// floor is 36 bytes (3 prefix + 33 single-byte fields), leaving 261 bytes of
// budget. A 200-byte string costs OP_PUSHDATA1 + a length byte + 200 = 202 in
// place of 1, so +201. A 60-byte string costs a length opcode + 60 = 61 in place
// of 1, so +60. 36 + 201 + 60 = 297.
func onCapRecord() WeatherData {
	return WeatherData{
		Conditions: strings.Repeat("A", 200),
		Icon:       strings.Repeat("B", 60),
	}
}

func TestGolden(t *testing.T) {
	cases := goldenCases()

	want := goldenFile{
		FormatVersion:    Version,
		SchemaFieldCount: DataFieldsPerRecord,
		Records:          make([]goldenRecord, 0, len(cases)),
	}

	for _, tc := range cases {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		want.Records = append(want.Records, goldenRecord{
			Name:      tc.Name,
			ScriptLen: len(*s),
			ScriptHex: s.String(),
			Data:      tc.Data,
		})
	}

	produced, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	produced = append(produced, '\n')

	if *updateGolden {
		if mkErr := os.MkdirAll("testdata/golden", 0o750); mkErr != nil {
			t.Fatalf("mkdir: %v", mkErr)
		}

		if writeErr := os.WriteFile(goldenPath, produced, 0o600); writeErr != nil {
			t.Fatalf("write: %v", writeErr)
		}

		t.Logf("wrote %s (%d records, %d bytes)", goldenPath, len(want.Records), len(produced))

		return
	}

	onDisk, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/weather -run TestGolden -update)", goldenPath, err)
	}

	if !bytes.Equal(produced, onDisk) {
		t.Errorf("%s is out of date.\nThe encoder now produces different bytes for at least one golden record.\n"+
			"If that change is intended, review it and run:\n"+
			"  go test ./internal/weather -run TestGolden -update\n"+
			"produced %d bytes, on disk %d bytes", goldenPath, len(produced), len(onDisk))
	}
}

// TestGoldenFileHasNoTimestampOrRevision guards the reproducibility trap
// directly: if anyone adds a generatedAt or a git revision to the file, every
// later commit fails the CI diff.
func TestGoldenFileHasNoTimestampOrRevision(t *testing.T) {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}

	allowed := map[string]bool{"formatVersion": true, "schemaFieldCount": true, "records": true}
	for key := range parsed {
		if !allowed[key] {
			t.Errorf("unexpected top-level key %q in %s: the file must be a pure function of the code, "+
				"so no timestamp and no git revision", key, goldenPath)
		}
	}

	for _, banned := range []string{"generatedAt", "generated_at", "gitRev", "commit", "timestamp"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Errorf("%s contains %q, which makes regenerate-and-diff fail on the next commit", goldenPath, banned)
		}
	}
}
```

- [ ] **Write the encoder test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/encoder_test.go` with exactly this content:

```go
package weather

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// TestEncodeShape checks the three fixed leading bytes and the chunk count.
func TestEncodeShape(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		b := s.Bytes()
		if len(b) < 3 || b[0] != script.OpFALSE || b[1] != script.OpRETURN || b[2] != script.Op1 {
			t.Fatalf("Encode(%s) does not start 00 6a 51: %s", tc.Name, s.String())
		}

		if !s.IsData() {
			t.Errorf("Encode(%s) is not recognized as a data script", tc.Name)
		}

		chunks, err := script.DecodeScript(b, script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Fatalf("DecodeScript(%s): %v", tc.Name, err)
		}

		if len(chunks) != ChunksPerRecord {
			t.Errorf("Encode(%s) produced %d chunks, want %d", tc.Name, len(chunks), ChunksPerRecord)
		}
	}
}

// TestEncodeKnownSizes pins the measured sizes. They are the basis of the fuel
// arithmetic, so a change here changes what the app costs to run.
func TestEncodeKnownSizes(t *testing.T) {
	want := map[string]int{
		"minimal":  36,
		"sample":   99,
		"extreme":  211,
		"negative": 64,
		"oncap":    MaxScriptBytes,
	}

	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		if got := len(*s); got != want[tc.Name] {
			t.Errorf("Encode(%s) is %d bytes, want %d", tc.Name, got, want[tc.Name])
		}
	}
}

// TestEncodeMinimalIsAllZeroBytes states the floor explicitly: 00 6a 51 followed
// by 33 OP_0 bytes.
func TestEncodeMinimalIsAllZeroBytes(t *testing.T) {
	s, err := EncodeHex(&WeatherData{})
	if err != nil {
		t.Fatalf("EncodeHex: %v", err)
	}

	want := "006a51" + strings.Repeat("00", DataFieldsPerRecord)
	if s != want {
		t.Errorf("EncodeHex(zero record) = %s, want %s", s, want)
	}
}

// TestEncodeIsDeterministic encodes the same record 100 times and requires
// byte-identical output every time. Combined with TestEncodeFollowsSchemaOrder
// this is what rules out a map ever entering the encode path.
func TestEncodeIsDeterministic(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		first, err := EncodeHex(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		for i := range 100 {
			again, err := EncodeHex(&data)
			if err != nil {
				t.Fatalf("Encode(%s) attempt %d: %v", tc.Name, i, err)
			}

			if again != first {
				t.Fatalf("Encode(%s) is not deterministic: attempt %d differs\nfirst %s\nthen  %s",
					tc.Name, i, first, again)
			}
		}
	}
}

// TestEncodeFollowsSchemaOrder proves the emitted order is FieldSchema's slice
// order, by giving every field a distinct value and checking each chunk in place.
// A map in the encode path would fail this test on most runs.
func TestEncodeFollowsSchemaOrder(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	// Field i gets the value i+17: above the OP_1..OP_16 opcode range, so every
	// field is a distinct data push and its position is unambiguous.
	for i, f := range FieldSchema {
		switch f.Type {
		case FieldInteger:
			p, ok := ptrs[i].(*int64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *int64", i, ptrs[i])
			}

			*p = int64(i) + 17
		case FieldFloat:
			p, ok := ptrs[i].(*float64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *float64", i, ptrs[i])
			}

			*p = float64(i+17) / FloatScale
		case FieldString:
			p, ok := ptrs[i].(*string)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *string", i, ptrs[i])
			}

			*p = f.Name
		case FieldBoolean:
			p, ok := ptrs[i].(*bool)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *bool", i, ptrs[i])
			}

			*p = true
		default:
			t.Fatalf("field %d has unknown type %d", i, f.Type)
		}
	}

	s, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}

	if len(chunks) != ChunksPerRecord {
		t.Fatalf("%d chunks, want %d", len(chunks), ChunksPerRecord)
	}

	for i, f := range FieldSchema {
		c := chunks[3+i]

		switch f.Type {
		case FieldInteger, FieldFloat:
			n, numErr := scriptNumFromChunk(c)
			if numErr != nil {
				t.Errorf("chunk %d (%s): %v", i, f.Name, numErr)

				continue
			}

			if n != int64(i)+17 {
				t.Errorf("chunk %d holds %d, want %d: the emitted order is not FieldSchema's order",
					i, n, int64(i)+17)
			}
		case FieldString:
			if string(c.Data) != f.Name {
				t.Errorf("chunk %d holds %q, want %q: the emitted order is not FieldSchema's order",
					i, string(c.Data), f.Name)
			}
		case FieldBoolean:
			if c.Op != script.Op1 {
				t.Errorf("chunk %d is %#02x, want OP_1", i, c.Op)
			}
		default:
			t.Errorf("field %d has unknown type %d", i, f.Type)
		}
	}
}

// TestEncodeNeverEmitsOpReturnInAFieldPosition is the invariant that keeps the
// payload parseable: every field byte is either a push opcode (at most 0x4b),
// OP_PUSHDATA1/2 (0x4c/0x4d), OP_1NEGATE (0x4f) or OP_1..OP_16 (0x51..0x60), all
// of which are below OP_RETURN (0x6a).
func TestEncodeNeverEmitsOpReturnInAFieldPosition(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Fatalf("DecodeScript(%s): %v", tc.Name, err)
		}

		for i := 3; i < len(chunks); i++ {
			if chunks[i].Op == script.OpRETURN {
				t.Errorf("Encode(%s): chunk %d is OP_RETURN", tc.Name, i)
			}

			if chunks[i].Op > script.Op16 {
				t.Errorf("Encode(%s): chunk %d opcode %#02x is above OP_16", tc.Name, i, chunks[i].Op)
			}
		}
	}
}

// TestEncodeCapBoundary is the money test. 297 bytes is accepted, 298 is
// rejected, and the rejection is the typed ErrScriptTooLarge that the publisher
// keys on to isolate the row before any wallet call happens.
func TestEncodeCapBoundary(t *testing.T) {
	if MaxScriptBytes != 297 {
		t.Fatalf("MaxScriptBytes = %d, want 297: it is the top of the contiguous one-claim window at D=50",
			MaxScriptBytes)
	}

	onCap := onCapRecord()

	s, err := Encode(&onCap)
	if err != nil {
		t.Fatalf("Encode(on-cap record) returned %v, want nil", err)
	}

	if len(*s) != MaxScriptBytes {
		t.Fatalf("the on-cap record is %d bytes, want exactly %d", len(*s), MaxScriptBytes)
	}

	// One more byte in a string field, so one more byte of script.
	overCap := onCapRecord()
	overCap.Icon += "B"

	_, err = Encode(&overCap)
	if !errors.Is(err, ErrScriptTooLarge) {
		t.Fatalf("Encode(298-byte record) error = %v, want ErrScriptTooLarge", err)
	}

	if !strings.Contains(err.Error(), "298") {
		t.Errorf("error %q should report the actual size so the operator can see how far over it went", err)
	}
}

// TestEncodeExtremeFixtureIsUnderTheCap records the headroom the cap was chosen
// for: the repository's own adversarial fixture is well inside it.
func TestEncodeExtremeFixtureIsUnderTheCap(t *testing.T) {
	var extreme WeatherData

	for _, tc := range goldenCases() {
		if tc.Name == "extreme" {
			extreme = tc.Data
		}
	}

	s, err := Encode(&extreme)
	if err != nil {
		t.Fatalf("Encode(extreme): %v", err)
	}

	if len(*s) > MaxScriptBytes {
		t.Fatalf("the extreme fixture is %d bytes, over the %d cap", len(*s), MaxScriptBytes)
	}

	t.Logf("extreme fixture: %d bytes, %d bytes of headroom", len(*s), MaxScriptBytes-len(*s))
}

func TestEncodeRejectsNonFiniteFloats(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		d := WeatherData{AirDensity: v}
		if _, err := Encode(&d); !errors.Is(err, ErrNonFinite) {
			t.Errorf("Encode(air_density=%v) error = %v, want ErrNonFinite", v, err)
		}

		d2 := WeatherData{StationPressure: v}
		if _, err := Encode(&d2); !errors.Is(err, ErrNonFinite) {
			t.Errorf("Encode(station_pressure=%v) error = %v, want ErrNonFinite", v, err)
		}
	}
}

func TestEncodeRejectsOutOfRangeIntegers(t *testing.T) {
	for _, n := range []int64{MaxScriptInt + 1, -MaxScriptInt - 1, math.MaxInt64, math.MinInt64} {
		d := WeatherData{Time: n}
		if _, err := Encode(&d); !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("Encode(time=%d) error = %v, want ErrNumberOutOfRange", n, err)
		}
	}
}

// TestEncodeErrorNamesTheField checks the error text carries the field name, so
// a rejected reading is diagnosable from one log line.
func TestEncodeErrorNamesTheField(t *testing.T) {
	d := WeatherData{StationPressure: math.NaN()}

	_, err := Encode(&d)
	if err == nil {
		t.Fatal("Encode returned nil error for a NaN station_pressure")
	}

	if !strings.Contains(err.Error(), "station_pressure") {
		t.Errorf("error %q does not name the offending field", err)
	}
}

// TestEncodeRejectsAnOversizedString covers ErrStringTooLong. The cap makes this
// unreachable through Encode in practice, which is why the guard is tested at
// appendField's own level of detail: a 65536-byte string is rejected as a push,
// not merely as an oversized script.
func TestEncodeRejectsAnOversizedString(t *testing.T) {
	s := &script.Script{}
	f := FieldDefinition{Name: "conditions", Type: FieldString}
	huge := strings.Repeat("x", maxPushDataLen+1)

	err := appendField(s, f, &huge)
	if !errors.Is(err, ErrStringTooLong) {
		t.Errorf("appendField(65536-byte string) error = %v, want ErrStringTooLong", err)
	}
}

// TestEncodeStringsAreAlwaysDataPushes is why appendField calls AppendPushData
// and never appendScriptNum for strings: a one-byte string whose byte is
// 0x01..0x10 must stay recoverable, and OP_1..OP_16 carry no data.
func TestEncodeStringsAreAlwaysDataPushes(t *testing.T) {
	d := WeatherData{Conditions: "\x01", Icon: "\x10"}

	s, err := Encode(&d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}

	// conditions is FieldSchema index 3, icon is index 7.
	if got := string(chunks[3+3].Data); got != "\x01" {
		t.Errorf("conditions chunk holds %q, want \\x01", got)
	}

	if got := string(chunks[3+7].Data); got != "\x10" {
		t.Errorf("icon chunk holds %q, want \\x10", got)
	}
}
```

- [ ] **Run the tests and see them fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -12
```

Expected: a compile failure naming the undefined encoder API. Go truncates after ten errors, so the exact list depends on report order:

```
# github.com/bsv-blockchain-demos/weather-proof/internal/weather [github.com/bsv-blockchain-demos/weather-proof/internal/weather.test]
internal/weather/encoder_test.go:17:13: undefined: Encode
internal/weather/encoder_test.go:50:15: undefined: MaxScriptBytes
internal/weather/encoder_test.go:56:13: undefined: Encode
internal/weather/encoder_test.go:70:12: undefined: EncodeHex
internal/weather/encoder_test.go:88:17: undefined: EncodeHex
internal/weather/encoder_test.go:94:18: undefined: EncodeHex
internal/weather/encoder_test.go:151:12: undefined: Encode
internal/weather/encoder_test.go:204:13: undefined: Encode
internal/weather/encoder_test.go:230:5: undefined: MaxScriptBytes
internal/weather/encoder_test.go:232:4: undefined: MaxScriptBytes
internal/weather/encoder_test.go:232:4: too many errors
```

- [ ] **Write the encoder.** Create `/Users/personal/git/demos/weather-chain/internal/weather/encoder.go` with exactly this content:

```go
package weather

import (
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
)

// MaxScriptBytes is a HARD cap on the encoded script, and it is money.
//
// 297 bytes is the top of the CONTIGUOUS one-claim fuel window at denomination
// 50: a script one byte longer silently costs a second fuel claim for every
// record published, forever. It is not 331 (the top of a disjoint island - the
// 298..321 gap in between already costs two claims) and not 250 (an earlier
// figure that wastes 47 bytes of usable window). The number must be re-derived
// whenever the denomination changes, never copied.
//
// Headroom: the real weather sample encodes to 99 bytes and the repository's own
// adversarial fixture to 211 bytes. The cap exists because conditions, icon,
// pressure_trend and lightning_strike_last_distance_msg are FREE-FORM strings,
// not enums, so nothing else bounds the script.
const MaxScriptBytes = 297

// maxPushDataLen is the largest string a single OP_PUSHDATA2 can carry. Above it
// go-sdk would emit OP_PUSHDATA4, which this format does not use.
const maxPushDataLen = 0xFFFF

var (
	// ErrScriptTooLarge is returned when the finished script exceeds
	// MaxScriptBytes. The publisher consumes this to mark the row terminally
	// failed instead of paying two fuel claims.
	ErrScriptTooLarge = errors.New("script exceeds the maximum size")

	// ErrStringTooLong is returned for a string field above maxPushDataLen. The
	// MaxScriptBytes cap makes it unreachable in practice; it is a defensive
	// guard so the format can never silently grow an OP_PUSHDATA4.
	ErrStringTooLong = errors.New("string field exceeds the maximum push size")

	// ErrUnknownFieldType is returned if FieldSchema ever grows a type this
	// encoder does not handle. It cannot be triggered by input data.
	ErrUnknownFieldType = errors.New("unknown field type in schema")
)

// Encode serializes d as a weather locking script:
//
//	00 6a        OP_FALSE OP_RETURN - provably unspendable, so the output adds
//	             no UTXO to any basket
//	51           the version opcode (OP_1)
//	...          the 33 fields, in FieldSchema order
//
// It is a pure function of d: it reads no clock, consumes no randomness and
// never ranges over a map, so the same record always produces the same bytes.
//
// It returns ErrScriptTooLarge, ErrNonFinite, ErrNumberOutOfRange or
// ErrStringTooLong rather than approximate bytes. There is no path that returns
// a script the cap forbids.
func Encode(d *WeatherData) (*script.Script, error) {
	s := &script.Script{}

	// AppendOpcodes rejects 0x01..0x4e, so it cannot be used for pushes - but
	// OP_FALSE (0x00), OP_RETURN (0x6a) and OP_1 (0x51) are all outside that
	// range and are appended verbatim.
	if err := s.AppendOpcodes(script.OpFALSE, script.OpRETURN, script.Op1); err != nil {
		return nil, fmt.Errorf("append prefix: %w", err)
	}

	ptrs := d.fieldPtrs()

	for i, f := range FieldSchema {
		if err := appendField(s, f, ptrs[i]); err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i, f.Name, err)
		}
	}

	if len(*s) > MaxScriptBytes {
		return nil, fmt.Errorf("%w: %d bytes > cap %d", ErrScriptTooLarge, len(*s), MaxScriptBytes)
	}

	return s, nil
}

// EncodeHex is Encode returning the lowercase hex of the script.
func EncodeHex(d *WeatherData) (string, error) {
	s, err := Encode(d)
	if err != nil {
		return "", err
	}

	return s.String(), nil
}

// appendField emits one field. ptr is the corresponding entry of fieldPtrs, so
// its concrete type is guaranteed by f.Type; a mismatch means schema.go and
// types.go have drifted and is reported as ErrUnknownFieldType rather than
// panicking.
func appendField(s *script.Script, f FieldDefinition, ptr any) error {
	switch f.Type {
	case FieldInteger:
		v, ok := ptr.(*int64)
		if !ok {
			return fmt.Errorf("%w: %s is integer but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		return appendScriptNum(s, *v)
	case FieldFloat:
		v, ok := ptr.(*float64)
		if !ok {
			return fmt.Errorf("%w: %s is float but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scaleFloat(*v)
		if err != nil {
			return err
		}

		return appendScriptNum(s, n)
	case FieldString:
		v, ok := ptr.(*string)
		if !ok {
			return fmt.Errorf("%w: %s is string but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		if len(*v) > maxPushDataLen {
			return fmt.Errorf("%w: %d bytes > %d", ErrStringTooLong, len(*v), maxPushDataLen)
		}

		// AppendPushData, never appendScriptNum: a string field must always be a
		// length-prefixed data push. Minimizing a one-byte string whose byte is
		// 0x01..0x10 into OP_1..OP_16 would produce a chunk with no Data, and the
		// value would decode as the empty string. Measured at v1.3.2:
		// AppendPushData emits 00 for empty, 0x01..0x4b + data for 1..75 bytes,
		// 4c <len> for 76..255 and 4d <len:2 LE> for 256..65535.
		return s.AppendPushData([]byte(*v))
	case FieldBoolean:
		v, ok := ptr.(*bool)
		if !ok {
			return fmt.Errorf("%w: %s is boolean but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n := int64(0)
		if *v {
			n = 1
		}

		return appendScriptNum(s, n)
	default:
		return fmt.Errorf("%w: %s has type %d", ErrUnknownFieldType, f.Name, f.Type)
	}
}
```

- [ ] **See the golden test fail for the right reason.** The package now compiles, so `TestGolden` runs and fails because the file does not exist yet. Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/ -count=1 -run TestGolden 2>&1 | head -8
```

Expected:

```
--- FAIL: TestGolden (0.00s)
    golden_test.go:225: read testdata/golden/records.json: open testdata/golden/records.json: no such file or directory (run: go test ./internal/weather -run TestGolden -update)
--- FAIL: TestGoldenFileHasNoTimestampOrRevision (0.00s)
    golden_test.go:242: read testdata/golden/records.json: open testdata/golden/records.json: no such file or directory
FAIL
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.34s
FAIL
```

- [ ] **Generate the golden file.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make go-golden
```

Expected: a `TestGolden` log line reading `wrote testdata/golden/records.json (5 records, 8201 bytes)` and `ok`. The 8201 is a consequence of the record set and `json.MarshalIndent`; if your byte count differs, the input set differs.

- [ ] **Verify the golden file's contents and that regeneration is idempotent.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  python3 -c "
import json
f = json.load(open('internal/weather/testdata/golden/records.json'))
assert set(f) == {'formatVersion','schemaFieldCount','records'}, sorted(f)
assert f['formatVersion'] == 1 and f['schemaFieldCount'] == 33
want = {'minimal':36,'sample':99,'extreme':211,'negative':64,'oncap':297}
got = {r['name']: r['scriptLen'] for r in f['records']}
assert got == want, got
for r in f['records']:
    assert len(r['scriptHex']) == r['scriptLen']*2
    assert r['scriptHex'].startswith('006a51')
    assert len(r['data']) == 33
print('golden verified:', got)
" && \
  cp internal/weather/testdata/golden/records.json /tmp/golden-before.json && \
  make go-golden >/dev/null && \
  diff -q /tmp/golden-before.json internal/weather/testdata/golden/records.json && \
  echo "REGENERATION IS IDEMPOTENT"
```

Expected:

```
golden verified: {'minimal': 36, 'sample': 99, 'extreme': 211, 'negative': 64, 'oncap': 297}
REGENERATION IS IDEMPOTENT
```

The second half is the reproducibility check: two runs seconds apart produce byte-identical files, which is only true because nothing in the file is a timestamp or a git revision.

- [ ] **Prove the golden file is a real change detector.** Temporarily swap two same-typed fields in `fieldPtrs`, watch the golden test fail, then restore it. Two `int64` fields are chosen deliberately: the swap still compiles and still type-checks, so **nothing but the golden file catches it**. Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  cp internal/weather/schema.go /tmp/schema.go.bak && \
  python3 - <<'PY'
p = 'internal/weather/schema.go'
s = open(p).read()
a = '\t\t&d.WetBulbGlobeTemperature,         // 27 wet_bulb_globe_temperature           integer\n'
b = '\t\t&d.WetBulbTemperature,              // 28 wet_bulb_temperature                 integer\n'
assert a in s and b in s, 'the two fieldPtrs lines were not found verbatim'
open(p, 'w').write(s.replace(a + b, b + a))
PY
  go test ./internal/weather/ -count=1 -run TestGolden 2>&1 | head -8; \
  cp /tmp/schema.go.bak internal/weather/schema.go && \
  go test ./internal/weather/ -count=1 -run TestGolden 2>&1 | tail -2
```

Expected: the first run fails, then the second run, after restoring the file, prints `ok`:

```
--- FAIL: TestGolden (0.00s)
    golden_test.go:229: testdata/golden/records.json is out of date.
        The encoder now produces different bytes for at least one golden record.
        If that change is intended, review it and run:
          go test ./internal/weather -run TestGolden -update
        produced 8201 bytes, on disk 8201 bytes
FAIL
FAIL	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.29s
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.24s
```

Note that the file is the same length either way — the change is two swapped values inside it. That is exactly why a byte comparison is used rather than a size check, and exactly the kind of change that would otherwise ship silently and write every future record with two fields transposed.

- [ ] **Run the tests and see them all pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofumpt -l internal/ && go test ./internal/weather/ -count=1 -v -run 'TestEncode|TestGolden' 2>&1 | grep -E '^(--- |    encoder_test|ok|PASS|FAIL)'
```

Expected: `gofumpt -l` prints nothing, then:

```
--- PASS: TestEncodeShape (0.00s)
--- PASS: TestEncodeKnownSizes (0.00s)
--- PASS: TestEncodeMinimalIsAllZeroBytes (0.00s)
--- PASS: TestEncodeIsDeterministic (0.00s)
--- PASS: TestEncodeFollowsSchemaOrder (0.00s)
--- PASS: TestEncodeNeverEmitsOpReturnInAFieldPosition (0.00s)
--- PASS: TestEncodeCapBoundary (0.00s)
    encoder_test.go:280: extreme fixture: 211 bytes, 86 bytes of headroom
--- PASS: TestEncodeExtremeFixtureIsUnderTheCap (0.00s)
--- PASS: TestEncodeRejectsNonFiniteFloats (0.00s)
--- PASS: TestEncodeRejectsOutOfRangeIntegers (0.00s)
--- PASS: TestEncodeErrorNamesTheField (0.00s)
--- PASS: TestEncodeRejectsAnOversizedString (0.00s)
--- PASS: TestEncodeStringsAreAlwaysDataPushes (0.00s)
--- PASS: TestGolden (0.00s)
--- PASS: TestGoldenFileHasNoTimestampOrRevision (0.01s)
PASS
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.31s
```

- [ ] **Run the full gate.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make check && echo "GATE CLEAN"
```

Expected: build and all tests pass, `golangci-lint` prints `0 issues.`, last line `GATE CLEAN`.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/encoder.go internal/weather/encoder_test.go \
          internal/weather/golden_test.go internal/weather/testdata/golden/records.json && \
  git commit -m "weather: the encoder, the 297-byte cap and the self-generated goldens

Encode emits 00 6a 51 then the 33 fields in FieldSchema order and is a pure
function of its input: no clock, no randomness, no map iteration. Two tests hold
that down - 100 encodes of each golden record must be byte-identical, and a
record with a distinct value in every field must come back out in exactly
FieldSchema's slice order.

MaxScriptBytes = 297 is fuel arithmetic, not style: it is the top of the
contiguous one-claim window at denomination 50, so one byte more costs a second
fuel claim per record forever. The boundary is pinned from both sides - a
synthetic record encoding to exactly 297 is accepted, the same record with one
more string byte is rejected with ErrScriptTooLarge, and the error text carries
the actual size.

testdata/golden/records.json is generated by this encoder and nothing else. It
is a CHANGE DETECTOR, not a correctness oracle: there is no external byte string
to agree with, so its job is to make an accidental edit to field order or number
encoding fail loudly. It records formatVersion and schemaFieldCount and nothing
else - no timestamp and no git revision, both of which make regenerate-and-diff
fail on the very next commit. A test asserts that directly.

Measured sizes, now frozen: 36 B all-zero floor, 99 B real sample, 211 B
extreme fixture (86 B of headroom), 64 B all-negative, 297 B on-cap."
```

---

## Task 5: The decoder and the round trip

**Files:**
- Create: `/Users/personal/git/demos/weather-chain/internal/weather/decoder.go`
- Test: `/Users/personal/git/demos/weather-chain/internal/weather/decoder_test.go`

**Interfaces:**
- Consumes, from Task 2: `const Version = 1`, `const FloatEpsilon = 1e-6`, `const DataFieldsPerRecord = 33`, `const ChunksPerRecord = 36`, `const MaxScriptInt = int64(1)<<53 - 1`, `type FieldType uint8` with `FieldInteger`/`FieldFloat`/`FieldString`/`FieldBoolean`, `type FieldDefinition struct { Name string; Type FieldType }`, `type WeatherData struct{...}`, `var FieldSchema []FieldDefinition`, `func (d *WeatherData) fieldPtrs() []any`.
- Consumes, from Task 3: `func scriptNumFromChunk(c *script.ScriptChunk) (int64, error)`, `func unscaleFloat(n int64) float64`, `var ErrNumberOutOfRange`, `var ErrNotANumber`.
- Consumes, from Task 4: `func Encode(d *WeatherData) (*script.Script, error)`, `const MaxScriptBytes = 297`, `var ErrUnknownFieldType`, and in test scope `const goldenPath = "testdata/golden/records.json"`, `type goldenFile`, `type goldenRecord`, `type goldenCase struct { Name string; Data WeatherData }`, `func goldenCases() []goldenCase`.
- Consumes, from `go-sdk` v1.3.2: `func script.DecodeScript(b []byte, options ...script.DecodeOptions) ([]*script.ScriptChunk, error)`, `script.DecodeOptionsParseOpReturn`, `func script.NewFromBytes(b []byte) *script.Script`, `type script.ScriptChunk struct { Op byte; Data []byte }`, and the opcode constants `script.OpFALSE`, `script.OpRETURN`, `script.OpPUSHDATA4`, `script.Op0`, `script.Op1`, `script.Op2`, `script.Op16`, `script.Op1NEGATE`, `script.OpDATA2`.
- Produces, all in `package weather`:
  - `var ErrMalformedScript = errors.New("malformed weather script")`
  - `var ErrUnsupportedVersion = errors.New("unsupported weather record version")`
  - `func Decode(s *script.Script) (*WeatherData, error)`
  - `func DecodeHex(h string) (*WeatherData, error)`
  - `func IsValidScript(s *script.Script) bool`
  - `func readField(c *script.ScriptChunk, f FieldDefinition, ptr any) error`
- Produces, in test scope: `func equalRecords(a, b WeatherData) bool`

### Steps

- [ ] **Write the failing test.** Create `/Users/personal/git/demos/weather-chain/internal/weather/decoder_test.go` with exactly this content:

```go
package weather

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// equalRecords compares two records: the two float fields within FloatEpsilon,
// because fixed-point encoding at 1e-6 is lossy by design, and every other field
// exactly. WeatherData is all scalars, so == covers the remaining 31 fields.
func equalRecords(a, b WeatherData) bool {
	if math.Abs(a.AirDensity-b.AirDensity) > FloatEpsilon {
		return false
	}

	if math.Abs(a.StationPressure-b.StationPressure) > FloatEpsilon {
		return false
	}

	a.AirDensity, b.AirDensity = 0, 0
	a.StationPressure, b.StationPressure = 0, 0

	return a == b
}

// TestRoundTripGoldenCases is the correctness test: whatever Encode writes,
// Decode reads back. The golden file is the change detector; this is the part
// that says the format is self-consistent.
func TestRoundTripGoldenCases(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		got, err := Decode(s)
		if err != nil {
			t.Fatalf("Decode(%s): %v", tc.Name, err)
		}

		if !equalRecords(tc.Data, *got) {
			t.Errorf("round trip of %s changed the record\nin  %+v\nout %+v", tc.Name, tc.Data, *got)
		}
	}
}

// TestDecodeGoldenHex decodes the hex COMMITTED in the golden file rather than
// hex produced in this process, so the stored bytes are proved readable and not
// merely self-consistent.
func TestDecodeGoldenHex(t *testing.T) {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}

	var file goldenFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}

	if len(file.Records) == 0 {
		t.Fatalf("%s holds no records", goldenPath)
	}

	if file.SchemaFieldCount != DataFieldsPerRecord {
		t.Fatalf("%s says %d fields, this package has %d",
			goldenPath, file.SchemaFieldCount, DataFieldsPerRecord)
	}

	for _, rec := range file.Records {
		got, err := DecodeHex(rec.ScriptHex)
		if err != nil {
			t.Errorf("DecodeHex(%s): %v", rec.Name, err)

			continue
		}

		if !equalRecords(rec.Data, *got) {
			t.Errorf("golden %s decoded to a different record\nstored  %+v\ndecoded %+v", rec.Name, rec.Data, *got)
		}

		if len(rec.ScriptHex) != rec.ScriptLen*2 {
			t.Errorf("golden %s: scriptLen %d does not match %d hex characters",
				rec.Name, rec.ScriptLen, len(rec.ScriptHex))
		}
	}
}

// TestRoundTripEdgeCases covers the value shapes the golden set does not, one
// field at a time so a failure names the cause.
func TestRoundTripEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		data WeatherData
	}{
		{"all zero", WeatherData{}},
		{"empty strings only", WeatherData{Time: 1}},
		{"unicode strings", WeatherData{
			Conditions:                     "☔ ☂ ☀",
			Icon:                           "partly-cloudy-night-⚡️",
			PressureTrend:                  "→",
			LightningStrikeLastDistanceMsg: "ünïcødé",
			WindDirectionCardinal:          "N",
		}},
		{"one byte strings in the opcode range", WeatherData{
			Conditions:                     "\x01",
			Icon:                           "\x10",
			PressureTrend:                  "\x00",
			LightningStrikeLastDistanceMsg: "\x81",
			WindDirectionCardinal:          "\x4f",
		}},
		{"75 byte string", WeatherData{Conditions: strings.Repeat("c", 75)}},
		{"76 byte string crosses into OP_PUSHDATA1", WeatherData{Conditions: strings.Repeat("c", 76)}},
		{"both booleans true", WeatherData{
			IsPrecipLocalDayRainCheck:       true,
			IsPrecipLocalYesterdayRainCheck: true,
		}},
		{"one boolean true", WeatherData{IsPrecipLocalYesterdayRainCheck: true}},
		{"opcode range integers", WeatherData{
			AirTemperature: 1, Brightness: 16, DeltaT: 17, DewPoint: -1,
			FeelsLike: 15, RelativeHumidity: 2,
		}},
		{"sign extension integers", WeatherData{
			AirTemperature: 127, Brightness: 128, DeltaT: -127, DewPoint: -128,
			FeelsLike: 255, SeaLevelPressure: -255, SolarRadiation: 256,
		}},
		{"maximum magnitudes", WeatherData{
			Time: MaxScriptInt, Brightness: -MaxScriptInt,
			LightningStrikeLastEpoch: 2147483647, SolarRadiation: -2147483648,
		}},
		{"maximum floats", WeatherData{AirDensity: 9e9, StationPressure: -9e9}},
		{"smallest floats", WeatherData{AirDensity: 0.000001, StationPressure: -0.000001}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.data

			s, err := Encode(&data)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			got, err := Decode(s)
			if err != nil {
				t.Fatalf("Decode(%s): %v", s.String(), err)
			}

			if !equalRecords(tc.data, *got) {
				t.Errorf("round trip changed the record\nin  %+v\nout %+v\nhex %s", tc.data, *got, s.String())
			}
		})
	}
}

// TestRoundTripSweep is the property test, done as a deterministic sweep rather
// than with a random generator: it is reproducible from nothing but the source,
// and it needs no pseudo-random number generator (math/rand in a test trips the
// gosec weak-randomness check, and hand-rolling one trips the integer-conversion
// check).
//
// Record k gives field i the value at index (i+k) mod len(table) of the table for
// that field's type, so across 600 records every value meets every field and a
// great many cross-field combinations are covered.
func TestRoundTripSweep(t *testing.T) {
	ints := []int64{
		0, 1, -1, 2, -2, 15, 16, 17, -16, -17, 127, -127, 128, -128, 255, -255,
		256, -256, 32767, -32768, 65535, 16777215, 2147483647, -2147483648,
		4294967296, MaxScriptInt, -MaxScriptInt,
	}
	floats := []float64{0, 0.000001, -0.000001, 1.29, -1.234567, 979.7, 999.999999, 9e9, -9e9}
	strs := []string{"", "a", "\x01", "\x10", "\x81", "abc def", "ünïcødé"}
	bools := []bool{false, true}

	for k := range 600 {
		d := WeatherData{}
		ptrs := d.fieldPtrs()

		for i, f := range FieldSchema {
			switch f.Type {
			case FieldInteger:
				p, ok := ptrs[i].(*int64)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *int64", i, ptrs[i])
				}

				*p = ints[(i+k)%len(ints)]
			case FieldFloat:
				p, ok := ptrs[i].(*float64)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *float64", i, ptrs[i])
				}

				*p = floats[(i+k)%len(floats)]
			case FieldString:
				p, ok := ptrs[i].(*string)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *string", i, ptrs[i])
				}

				*p = strs[(i+k)%len(strs)]
			case FieldBoolean:
				p, ok := ptrs[i].(*bool)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *bool", i, ptrs[i])
				}

				*p = bools[(i+k)%len(bools)]
			default:
				t.Fatalf("field %d has unknown type %d", i, f.Type)
			}
		}

		want := d

		s, err := Encode(&d)
		if err != nil {
			t.Fatalf("record %d: Encode(%+v): %v", k, want, err)
		}

		if len(*s) > MaxScriptBytes {
			t.Fatalf("record %d is %d bytes, over the %d cap: shrink the sweep tables",
				k, len(*s), MaxScriptBytes)
		}

		got, err := Decode(s)
		if err != nil {
			t.Fatalf("record %d: Decode(%s): %v", k, s.String(), err)
		}

		if !equalRecords(want, *got) {
			t.Fatalf("record %d round trip changed the record\nin  %+v\nout %+v\nhex %s",
				k, want, *got, s.String())
		}
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	s, err := Encode(&WeatherData{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	for _, op := range []byte{script.Op0, script.Op2, script.Op16, script.Op1NEGATE} {
		b := append([]byte(nil), s.Bytes()...)
		b[2] = op

		_, err := Decode(script.NewFromBytes(b))
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("version opcode %#02x: error = %v, want ErrUnsupportedVersion", op, err)
		}
	}
}

func TestDecodeRejectsTooFewChunks(t *testing.T) {
	s, err := Encode(&WeatherData{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Every field of the all-zero record is exactly one byte, so dropping the
	// last two bytes drops two whole fields and leaves a 34-chunk script - the
	// shape a guard written on the wrong basis would wrongly accept.
	b := s.Bytes()

	_, err = Decode(script.NewFromBytes(b[:len(b)-2]))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("34-chunk script: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "34 chunks") {
		t.Errorf("error %q should say how many chunks it found", err)
	}
}

func TestDecodeRejectsAMissingPrefix(t *testing.T) {
	// A bare version opcode plus 33 zero fields: 34 chunks, no 00 6a.
	bare := make([]byte, 0, 1+DataFieldsPerRecord)
	bare = append(bare, script.Op1)

	for range DataFieldsPerRecord {
		bare = append(bare, script.Op0)
	}

	if _, err := Decode(script.NewFromBytes(bare)); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("prefix-less script: error = %v, want ErrMalformedScript", err)
	}

	// And a full-length script whose first two bytes are not 00 6a.
	padded := make([]byte, 0, 3+DataFieldsPerRecord)
	padded = append(padded, script.Op1, script.Op1, script.Op1)

	for range DataFieldsPerRecord {
		padded = append(padded, script.Op0)
	}

	if _, err := Decode(script.NewFromBytes(padded)); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("wrong prefix: error = %v, want ErrMalformedScript", err)
	}
}

func TestDecodeToleratesTrailingChunks(t *testing.T) {
	in := WeatherData{Conditions: "Clear", Time: 1769529302}

	s, err := Encode(&in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	b := append([]byte(nil), s.Bytes()...)
	b = append(b, script.Op1, script.Op2, script.Op16)

	got, err := Decode(script.NewFromBytes(b))
	if err != nil {
		t.Fatalf("Decode with trailing chunks: %v", err)
	}

	if !equalRecords(in, *got) {
		t.Errorf("trailing chunks changed the record\nin  %+v\nout %+v", in, *got)
	}
}

func TestDecodeRejectsOpReturnInAFieldPosition(t *testing.T) {
	// air_density (schema index 0) is replaced by a bare OP_RETURN, and one extra
	// zero field is appended so the chunk count still reaches 36.
	b := make([]byte, 0, 4+DataFieldsPerRecord)
	b = append(b, script.OpFALSE, script.OpRETURN, script.Op1, script.OpRETURN)

	for range DataFieldsPerRecord {
		b = append(b, script.Op0)
	}

	_, err := Decode(script.NewFromBytes(b))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("OP_RETURN in a numeric field: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "air_density") {
		t.Errorf("error %q does not name the offending field", err)
	}

	// And in a string position: conditions is schema index 3.
	b2 := []byte{script.OpFALSE, script.OpRETURN, script.Op1}
	for i := range DataFieldsPerRecord {
		if i == 3 {
			b2 = append(b2, script.OpRETURN)

			continue
		}

		b2 = append(b2, script.Op0)
	}

	b2 = append(b2, script.Op0)

	_, err = Decode(script.NewFromBytes(b2))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("OP_RETURN in a string field: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "conditions") {
		t.Errorf("error %q does not name the offending field", err)
	}
}

func TestDecodeRejectsANonMinimalPush(t *testing.T) {
	// air_density carries 7f00, which decodes numerically to 127 but is not
	// minimally encoded. Accepting it would mean two distinct scripts decode to
	// the same record, so Encode would no longer be the only writer of a record's
	// bytes.
	b := make([]byte, 0, 6+DataFieldsPerRecord)
	b = append(b, script.OpFALSE, script.OpRETURN, script.Op1, script.OpDATA2, 0x7f, 0x00)

	for range DataFieldsPerRecord {
		b = append(b, script.Op0)
	}

	if _, err := Decode(script.NewFromBytes(b)); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("non-minimal push: error = %v, want ErrMalformedScript", err)
	}
}

func TestDecodeRejectsAnOpcodeInAStringPosition(t *testing.T) {
	// conditions (index 3) carries OP_1, which is a valid number but carries no
	// data, so accepting it would silently decode as the empty string.
	b := []byte{script.OpFALSE, script.OpRETURN, script.Op1}
	for i := range DataFieldsPerRecord {
		if i == 3 {
			b = append(b, script.Op1)

			continue
		}

		b = append(b, script.Op0)
	}

	_, err := Decode(script.NewFromBytes(b))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("OP_1 in a string field: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "not a data push") {
		t.Errorf("error %q should say the chunk is not a data push", err)
	}
}

func TestDecodeHexRejectsBadHex(t *testing.T) {
	for _, h := range []string{"zz", "006a5", ""} {
		if _, err := DecodeHex(h); !errors.Is(err, ErrMalformedScript) {
			t.Errorf("DecodeHex(%q) error = %v, want ErrMalformedScript", h, err)
		}
	}
}

func TestIsValidScript(t *testing.T) {
	good, err := Encode(&WeatherData{Conditions: "Clear"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if !IsValidScript(good) {
		t.Error("IsValidScript returned false for a script this package just encoded")
	}

	if IsValidScript(script.NewFromBytes([]byte{script.OpFALSE, script.OpRETURN})) {
		t.Error("IsValidScript returned true for a bare OP_FALSE OP_RETURN")
	}

	if IsValidScript(script.NewFromBytes(nil)) {
		t.Error("IsValidScript returned true for an empty script")
	}
}
```

- [ ] **Run the tests and see them fail.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/... 2>&1 | head -12
```

Expected: a compile failure naming the undefined decoder API. Go truncates after ten errors, so the exact list depends on report order:

```
# github.com/bsv-blockchain-demos/weather-proof/internal/weather [github.com/bsv-blockchain-demos/weather-proof/internal/weather.test]
internal/weather/decoder_test.go:44:15: undefined: Decode
internal/weather/decoder_test.go:79:15: undefined: DecodeHex
internal/weather/decoder_test.go:152:16: undefined: Decode
internal/weather/decoder_test.go:234:15: undefined: Decode
internal/weather/decoder_test.go:256:13: undefined: Decode
internal/weather/decoder_test.go:257:22: undefined: ErrUnsupportedVersion
internal/weather/decoder_test.go:274:11: undefined: Decode
internal/weather/decoder_test.go:275:21: undefined: ErrMalformedScript
internal/weather/decoder_test.go:293:15: undefined: Decode
internal/weather/decoder_test.go:293:15: too many errors
```

- [ ] **Write the decoder.** Create `/Users/personal/git/demos/weather-chain/internal/weather/decoder.go` with exactly this content:

```go
package weather

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
)

var (
	// ErrMalformedScript is returned for anything that is not the layout Encode
	// writes: a missing prefix, too few chunks, or a chunk that is not a legal
	// value for its field.
	ErrMalformedScript = errors.New("malformed weather script")

	// ErrUnsupportedVersion is returned when the version opcode is not Version.
	ErrUnsupportedVersion = errors.New("unsupported weather record version")
)

// Decode parses a weather locking script back into a record.
//
// EXACTLY ONE LAYOUT is supported: the one Encode writes. There is deliberately
// no tolerance for historical layouts. Records in older layouts do exist on
// chain but are unlocatable - the database that held their txids is gone - so
// multi-layout support would be untestable code guarding against input that
// cannot arrive. It is also a live hazard: two of the historical layouts are
// pure permutations of the same 33 chunks, so a mis-selected layout decodes to
// plausible garbage instead of an error.
//
// Trailing chunks after the 33 fields are tolerated and ignored. Fewer than
// ChunksPerRecord chunks is a hard rejection.
func Decode(s *script.Script) (*WeatherData, error) {
	// DecodeOptionsParseOpReturn is REQUIRED. Without it go-sdk's DecodeScript
	// sets op.Data to the whole remainder of the script including the 0x6a byte
	// and stops, so a weather script parses as 2 chunks instead of 36. Verified
	// at v1.3.2: 006a5101ff0102 yields 5 chunks with the option and 2 without.
	chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedScript, err)
	}

	if len(chunks) < ChunksPerRecord {
		return nil, fmt.Errorf("%w: %d chunks, want at least %d",
			ErrMalformedScript, len(chunks), ChunksPerRecord)
	}

	if chunks[0].Op != script.OpFALSE || chunks[1].Op != script.OpRETURN {
		return nil, fmt.Errorf("%w: want an OP_FALSE OP_RETURN prefix, got %#02x %#02x",
			ErrMalformedScript, chunks[0].Op, chunks[1].Op)
	}

	version, err := scriptNumFromChunk(chunks[2])
	if err != nil {
		return nil, fmt.Errorf("%w: version chunk: %w", ErrMalformedScript, err)
	}

	if version != Version {
		return nil, fmt.Errorf("%w: %d, want %d", ErrUnsupportedVersion, version, Version)
	}

	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	for i, f := range FieldSchema {
		if err := readField(chunks[3+i], f, ptrs[i]); err != nil {
			return nil, fmt.Errorf("%w: field %d (%s): %w", ErrMalformedScript, i, f.Name, err)
		}
	}

	return d, nil
}

// DecodeHex is Decode over a hex string.
func DecodeHex(h string) (*WeatherData, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedScript, err)
	}

	return Decode(script.NewFromBytes(raw))
}

// IsValidScript reports whether s decodes as a weather record. It is the cheap
// gate for "is this output one of ours" and never returns a partial record.
func IsValidScript(s *script.Script) bool {
	_, err := Decode(s)

	return err == nil
}

// readField writes one chunk into the struct field ptr points at.
func readField(c *script.ScriptChunk, f FieldDefinition, ptr any) error {
	switch f.Type {
	case FieldInteger:
		v, ok := ptr.(*int64)
		if !ok {
			return fmt.Errorf("%w: %s is integer but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}

		*v = n

		return nil
	case FieldFloat:
		v, ok := ptr.(*float64)
		if !ok {
			return fmt.Errorf("%w: %s is float but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}

		*v = unscaleFloat(n)

		return nil
	case FieldString:
		v, ok := ptr.(*string)
		if !ok {
			return fmt.Errorf("%w: %s is string but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		// Everything from OP_0 (0x00, the empty push) up to OP_PUSHDATA4 (0x4e)
		// is a data push. Anything above it is an opcode, which in a string
		// position means a forged or corrupt script - including 0x6a.
		if c.Op > script.OpPUSHDATA4 {
			return fmt.Errorf("%w: opcode %#02x is not a data push", ErrMalformedScript, c.Op)
		}

		*v = string(c.Data)

		return nil
	case FieldBoolean:
		v, ok := ptr.(*bool)
		if !ok {
			return fmt.Errorf("%w: %s is boolean but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}

		*v = n != 0

		return nil
	default:
		return fmt.Errorf("%w: %s has type %d", ErrUnknownFieldType, f.Name, f.Type)
	}
}
```

- [ ] **Run the tests and see them pass.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && gofumpt -l internal/ && go test ./internal/weather/ -count=1 -v -run 'TestRoundTrip|TestDecode|TestIsValid' 2>&1 | grep -E '^(--- |ok|PASS|FAIL)'
```

Expected: `gofumpt -l` prints nothing, then:

```
--- PASS: TestRoundTripGoldenCases (0.00s)
--- PASS: TestDecodeGoldenHex (0.00s)
--- PASS: TestRoundTripEdgeCases (0.00s)
--- PASS: TestRoundTripSweep (0.01s)
--- PASS: TestDecodeRejectsWrongVersion (0.00s)
--- PASS: TestDecodeRejectsTooFewChunks (0.00s)
--- PASS: TestDecodeRejectsAMissingPrefix (0.00s)
--- PASS: TestDecodeToleratesTrailingChunks (0.00s)
--- PASS: TestDecodeRejectsOpReturnInAFieldPosition (0.00s)
--- PASS: TestDecodeRejectsANonMinimalPush (0.00s)
--- PASS: TestDecodeRejectsAnOpcodeInAStringPosition (0.00s)
--- PASS: TestDecodeHexRejectsBadHex (0.00s)
--- PASS: TestIsValidScript (0.00s)
PASS
ok  	github.com/bsv-blockchain-demos/weather-proof/internal/weather	0.30s
```

`TestRoundTripEdgeCases` also prints its 13 subtests as `    --- PASS: TestRoundTripEdgeCases/all_zero (0.00s)` and so on; the grep above hides them. Drop the grep to see them.

- [ ] **Run the whole suite and confirm the count.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && go test ./internal/weather/ -count=1 -v 2>&1 | grep -cE '^--- PASS'
```

Expected: `51`. That is every top-level test from Tasks 2 through 5: **10** from Task 2 (4 in `types_test.go`, 6 in `schema_test.go`), **13** from Task 3 (7 in `scriptnum_test.go`, 6 in `float_test.go`), **15** from Task 4 (13 in `encoder_test.go`, 2 in `golden_test.go`) and **13** from Task 5. The `^--- PASS` anchor counts top-level tests only; `TestRoundTripEdgeCases`'s 13 subtests are indented and are not counted. If the number is lower, a test file is missing; if a `FAIL` appears, stop and read it.

- [ ] **Run the full gate.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && make check && \
  git diff --exit-code internal/weather/testdata/golden/ && \
  echo "GATE CLEAN AND GOLDEN UNCHANGED"
```

Expected: build and all tests pass, `golangci-lint` prints `0 issues.`, `git diff --exit-code` is silent, and the last line is `GATE CLEAN AND GOLDEN UNCHANGED`. The `git diff` step is the one CI will also run: it proves the committed golden file matches what this encoder produces, so a drift cannot be laundered by regenerating the file.

- [ ] **Confirm no TypeScript was touched.** The base commit is found by searching the log for Task 1a's commit rather than by counting back a fixed number of commits, so the check still works if extra commits were made. Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  BASE="$(git log --format='%H' --grep='^weather: bootstrap the Go module' -n 1)^" && \
  git diff --stat "$BASE" -- src tests frontend package.json tsconfig.json jest.config.js && \
  echo "NO TYPESCRIPT TOUCHED (base $BASE)"
```

Expected: `git diff --stat` prints nothing at all, then `NO TYPESCRIPT TOUCHED (base <sha>^)`. Any file listed means a step went outside this plan's scope; revert it. If `BASE` comes out empty, Task 1a's commit message was changed — find its SHA by hand and re-run.

- [ ] **Commit.** Run:

```bash
cd /Users/personal/git/demos/weather-chain && \
  git add internal/weather/decoder.go internal/weather/decoder_test.go && \
  git commit -m "weather: the decoder and the round trip

Decode reads EXACTLY ONE layout - the one Encode writes. No historical-layout
tolerance: the only records in older layouts are on chain but unlocatable, since
the database holding their txids is gone, so multi-layout support would be
untestable code guarding against input that cannot arrive. It is also a hazard,
because two of those layouts are pure permutations of the same 33 chunks and a
mis-selected layout decodes to plausible garbage rather than an error.

script.DecodeOptionsParseOpReturn is required, and the chunk count is 36, not
34: the option steps past the 0x6a byte but still appends the chunk. A guard
written on 34 would accept a script truncated by two whole fields, which is
exactly what TestDecodeRejectsTooFewChunks now forbids.

Round-trip coverage: the five golden records both freshly encoded and read from
the committed hex, a 13-case edge table (empty strings, unicode, one-byte
strings whose byte collides with an opcode, the 75/76 push boundary, negatives,
zero, 2^53-1), and a 600-record deterministic sweep. The sweep is a fixed table
walk rather than a random generator, so a failure is reproducible from the
source alone.

Decoder negatives: wrong version, fewer than 36 chunks, a missing or wrong
prefix, trailing chunks tolerated, OP_RETURN in both a numeric and a string
field position, a non-minimal push, an opcode where a string belongs, and bad
hex. Every rejection is a typed error and names the offending field."
```

---

## Self-review

### 1. Coverage against the amended specification's encoder section

| Requirement | Where it is satisfied |
|---|---|
| A valid script: `OP_FALSE OP_RETURN`, version byte, 33 fields, minimal push encoding | Task 4 `Encode`; `TestEncodeShape`, `TestEncodeMinimalIsAllZeroBytes`; Task 3 `appendScriptNum` and `TestAppendScriptNumTable` |
| A fixed, documented field order: the existing alphabetical 33-field schema, unchanged, not redesigned | Task 2 `FieldSchema`; `TestFieldSchemaOrderAndCount`, `TestFieldSchemaIsStrictlyAlphabetical`, `TestFieldSchemaTypeTally` |
| A hard 297-byte cap, enforced in the encoder | Task 4 `MaxScriptBytes`, the check inside `Encode`, `TestEncodeCapBoundary` (297 accepted, 298 rejected with `ErrScriptTooLarge` and the size in the message), `TestEncodeExtremeFixtureIsUnderTheCap` |
| Encoder/decoder round trip | Task 5 `TestRoundTripGoldenCases`, `TestDecodeGoldenHex`, `TestRoundTripEdgeCases` (13 cases), `TestRoundTripSweep` (600 records) |
| Determinism: identical input, identical bytes; no map iteration, no wall clock, no randomness | Task 4 `TestEncodeIsDeterministic` (100 encodes of each of 5 records) and `TestEncodeFollowsSchemaOrder`; Task 2's `FieldSchema` is a slice and a comment says why |
| Self-generated golden files as a change detector | Task 4 `golden_test.go`, `testdata/golden/records.json`, the `make go-golden` regeneration path, the deliberate break-and-restore step that proves the detector works |
| Goldens contain no timestamp and no git revision | Task 4 `TestGoldenFileHasNoTimestampOrRevision`, plus the idempotent-regeneration verification step |
| Documented float rule (scale then round), chosen not inherited | Task 3 `scaleFloat`'s doc comment and `TestScaleFloatRoundingRule`, which records the four ECMAScript divergences in a `wantJS` column |
| Documented integer rule | Task 2's `MaxScriptInt` comment, Task 3 `appendScriptNum`, `TestAppendScriptNumTable`, `TestAppendScriptNumOutOfRange` |
| Typed errors: `ErrNonFinite`, `ErrNumberOutOfRange`, `ErrStringTooLong`, `ErrScriptTooLarge` | Task 3 and Task 4; `TestEncodeRejectsNonFiniteFloats`, `TestEncodeRejectsOutOfRangeIntegers`, `TestEncodeRejectsAnOversizedString`, `TestEncodeCapBoundary`, `TestEncodeErrorNamesTheField` |
| Decoder reads ONE layout, using `go-sdk`'s parser with `DecodeOptionsParseOpReturn`, on a 36-chunk basis | Task 5 `Decode`; `TestDecodeRejectsTooFewChunks` asserts the 34-chunk shape is rejected, which is the mistake a 34 basis would make |
| Decoder negatives: version ≠ 1, fewer than 36 chunks, trailing chunks tolerated, non-minimal push, `0x6a` in a field position | Task 5 `TestDecodeRejectsWrongVersion`, `TestDecodeRejectsTooFewChunks`, `TestDecodeToleratesTrailingChunks`, `TestDecodeRejectsANonMinimalPush`, `TestDecodeRejectsOpReturnInAFieldPosition`, plus `TestDecodeRejectsAnOpcodeInAStringPosition` and `TestDecodeRejectsAMissingPrefix` |
| No `parity/ts/`, no pinned `@bsv/sdk`, no `make parity`, no TypeScript oracle, no on-chain fixture task, no `FieldSchemaV0` | Absent by construction. Task 5's final step asserts mechanically that no TypeScript file changed |
| Version-evolution warning in a comment above the constant | Task 2 `types.go`, the `Version` doc comment |
| The `WEATHER_MAX_SCRIPT_BYTES` config knob and the publisher's use of `ErrScriptTooLarge` | **Out of scope, deliberately.** This plan produces the typed error and the 297 constant; `internal/config` and `internal/publisher` are later plans, and the constant is what their validation rule must be checked against |
| The Tempest mapper's coercion rules | **Out of scope, deliberately.** `internal/tempest/mapper.go` is a later plan. It consumes `WeatherData` from Task 2 and the typed errors from Tasks 3 and 4 |

### 2. Placeholder scan

No `TBD`, no `TODO`, no `FIXME`, no "add appropriate error handling", no "write tests for the above", no "similar to Task N". Every Go block is complete and compilable exactly as written — all fourteen were compiled, run, `gofumpt`-checked and linted before this plan was written, and the code blocks are byte-identical to the files that passed. Every command is runnable as written; every expected output is a measured value, not an illustration. That includes all five failure texts — Task 1a's missing-module build error, Task 1b's `NO LINT CONFIG` / `0`, Task 3's `missing go.sum entry` setup failure (the reason `go mod tidy` runs before Task 3's red gate rather than after it), Task 3's undefined-identifier list, and the deliberate golden-break demonstration in Task 4.

The only `...` sequences anywhere in the document are legitimate Go and shell: the `./...` package pattern, the variadic `...uint8` and `...script.DecodeOptions` in verified signatures, the `s.Bytes()...` slice spread, the ASCII layout diagram inside `Encode`'s doc comment, and prose abbreviations in the **Interfaces** summary bullets (`type WeatherData struct { ... }`) whose full text appears verbatim in the corresponding code block. There is no elided code an implementer must fill in.

The three places where an expected value legitimately depends on the environment — `go version`'s platform suffix, per-test elapsed times, and the module-cache path (`/Users/personal/go/pkg/mod/...`) inside Task 3's `missing go.sum entry` text, which follows `go env GOMODCACHE` — say so explicitly.

### 3. Type consistency

Every identifier a later task uses is defined by an earlier one with the same signature. Spot-checked in both directions:

- `appendScriptNum(s *script.Script, n int64) error` — defined Task 3, used by Task 4 `appendField` and Task 3's own tests. Same signature in both Interfaces blocks.
- `scriptNumFromChunk(c *script.ScriptChunk) (int64, error)` — defined Task 3, used by Task 4 `TestEncodeFollowsSchemaOrder` and Task 5 `Decode`/`readField`. Same signature in all three Interfaces blocks.
- `scaleFloat(v float64) (int64, error)` / `unscaleFloat(n int64) float64` — defined Task 3; `scaleFloat` used by Task 4 `appendField`, `unscaleFloat` by Task 5 `readField`.
- `(d *WeatherData) fieldPtrs() []any` — defined Task 2, used by Task 2's own tests, Task 4 `Encode` and `TestEncodeFollowsSchemaOrder`, Task 5 `Decode` and `TestRoundTripSweep`. Index alignment with `FieldSchema` is asserted by `TestFieldPtrsMatchSchemaTypes` and `TestFieldPtrsAreDistinctAndInStructOrder`.
- `FieldDefinition struct { Name string; Type FieldType }` — two fields, no `Required`. Consistent everywhere: Task 2's literal, Task 4's `appendField(s, f, ptr)` and its `FieldDefinition{Name: "conditions", Type: FieldString}` construction, Task 5's `readField(c, f, ptr)`.
- `ChunksPerRecord = 36` — one name, one value, used by Task 4's shape and order tests and Task 5's guard. There is no `ChunksPrefixed`/`ChunksLegacy` pair, because there is only one layout.
- `ErrUnknownFieldType` — declared once, in Task 4's `encoder.go`, and used by both `appendField` (Task 4) and `readField` (Task 5). Task 5's Interfaces block lists it under Consumes for that reason.
- `goldenPath`, `goldenFile`, `goldenRecord`, `goldenCase`, `goldenCases()`, `onCapRecord()` — declared once, in Task 4's `golden_test.go`, and consumed by Task 4's `encoder_test.go` and Task 5's `decoder_test.go`. Both consuming tasks list them in their Interfaces blocks.
- `jsonTags(t *testing.T) []string` — declared once, in Task 2's `types_test.go`, used by Task 2's `schema_test.go`.
- `equalRecords(a, b WeatherData) bool` — declared once, in Task 5's `decoder_test.go`, used only there. It takes values, not pointers, because it zeroes the float fields on its local copies before comparing with `==`.
