package api

// gates_test.go holds the plan's structural gates: the properties that are
// facts about the build graph, the type layer or the source tree rather than
// about one request/response pair.
//
// It is in package api rather than an external api_test package — a departure
// from the brief, which assumed no gate needed package internals. Two do:
// gates 2 and 3 reflect over UNEXPORTED DTO types, which an external test
// package cannot name at all. Keeping the file internal also lets gate 4 reuse
// testDeps/discardLogger instead of duplicating a fixture, and nothing here is
// weakened by it: none of these gates asserts an external caller's view.
//
// Every gate below states its mutation and its runtime sibling, and every one
// was run RED against that mutation in a scratch copy outside the repository
// before this file was committed. Where a gate is a static scan kept only
// because a runtime sibling carries the property, that is said at the gate:
// B1 shipped twelve gates of which one could not be made to fail, and eleven
// real gates beat twelve with one that lies.

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// moduleRoot is where `go list` must run: this package's directory is two
// levels below the module root, and `go list ./internal/...` is relative to
// the module, not to the test's working directory.
const moduleRoot = "../.."

// goldenFS embeds every committed golden file. Embedding rather than reading
// through os.ReadFile at a computed path is deliberate twice over: gosec G304
// fires on a non-constant path argument, and an embed pattern is evaluated by
// the compiler, so a golden file that is deleted from disk cannot be papered
// over by a test that silently skips the missing name.
//
//go:embed testdata/*.json
var goldenFS embed.FS

// ─────────────────────────────────────────────────────────────────────────────
// Gate 1 — the import prohibition.
//
// Mechanism: `go list -deps` over the three packages this plan created.
// Mutation: add `import _ ".../internal/store/postgres"` to dto.go → RED.
// Runtime sibling: NONE. This gate is load-bearing entirely on its own — the
// property is a build-graph fact with no runtime expression, and the whole
// database-free CI story (every B2 test runs in the ordinary `check` job with
// no service container) rests on it.
//
// The >20-line assertion is the vacuity guard: `go list` printing nothing, or
// failing in a way that yielded an empty stdout, would otherwise satisfy a
// bare "no line contains jackc" check perfectly.
// ─────────────────────────────────────────────────────────────────────────────

func TestAPIDoesNotDependOnPostgresOrPgx(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-deps",
		"./internal/api", "./internal/ratelimit", "./internal/config")
	cmd.Dir = moduleRoot

	out, listErr := cmd.CombinedOutput()
	if listErr != nil {
		t.Fatalf("go list -deps failed: %v\noutput:\n%s", listErr, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) <= 20 {
		t.Fatalf("go list -deps produced only %d lines, which is too few to be a real dependency graph; "+
			"a failed or empty listing must never pass as a clean result:\n%s", len(lines), out)
	}

	for _, dep := range lines {
		if strings.Contains(dep, "jackc") || strings.Contains(dep, "internal/store/postgres") {
			t.Errorf("forbidden dependency %q: internal/api, internal/ratelimit and internal/config must "+
				"never reach the Postgres driver, or the whole read-API suite needs a database to run", dep)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 8 — no package ships untested.
//
// Mechanism: the same `go list` template the CI `check` job uses.
// Mutation: delete internal/ratelimit/ratelimit_test.go → RED.
// Runtime sibling: go.yml's own "No package is silently untested" step, over
// the whole module. This local copy is REDUNDANT BY DESIGN — it exists so the
// failure surfaces while the task is being written rather than minutes later
// in CI. Stated plainly so a reviewer does not read the duplication as an
// oversight, and so nobody deletes the CI step believing this replaced it.
// ─────────────────────────────────────────────────────────────────────────────

func TestEveryPackageThisPlanCreatedHasTestFiles(t *testing.T) {
	const tmpl = `{{if and (eq (len .TestGoFiles) 0) (eq (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}`

	cmd := exec.CommandContext(context.Background(), "go", "list", "-f", tmpl, "./internal/...")
	cmd.Dir = moduleRoot
	out, listErr := cmd.CombinedOutput()
	if listErr != nil {
		t.Fatalf("go list failed: %v\noutput:\n%s", listErr, out)
	}

	// Vacuity guard: an empty template result is the PASS condition, so a
	// `go list` that matched no package at all would look identical to a
	// clean tree. Count the packages separately.
	countCmd := exec.CommandContext(context.Background(), "go", "list", "./internal/...")
	countCmd.Dir = moduleRoot
	countOut, countErr := countCmd.CombinedOutput()
	if countErr != nil {
		t.Fatalf("go list (count) failed: %v\noutput:\n%s", countErr, countOut)
	}
	if pkgs := nonEmptyLines(string(countOut)); len(pkgs) < 5 {
		t.Fatalf("go list ./internal/... returned %d packages, want at least 5; "+
			"an empty listing must not pass as a clean result", len(pkgs))
	}

	if missing := nonEmptyLines(string(out)); len(missing) != 0 {
		t.Errorf("packages with no test files (go test ./... reports these as a pass): %v", missing)
	}
}

func nonEmptyLines(s string) []string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 2 — no omitempty anywhere in the wire layer.
//
// Mechanism: reflection over the seven DTO types plus weather.WeatherData,
// recursing into nested struct fields.
// Mutation: add `,omitempty` to any DTO field → RED.
// Runtime siblings: Task 5's TestStationSummaryLastTempIsPresentAndNullWhenUnknown
// and Task 8's 33-key assertion, which prove the WIRE effect on real bytes.
//
// Task 5 already ships TestNoDTOFieldUsesOmitempty, an AST walk over this
// package's own source. This gate is a second mechanism, not a replacement:
// the AST walk cannot see weather.WeatherData's tags (different package
// directory) and cannot follow a DTO field whose type lives elsewhere, while
// reflection follows the actual marshaled type graph. The field-count floor is
// the liveness assertion — a recursion that silently visited nothing would
// otherwise pass.
// ─────────────────────────────────────────────────────────────────────────────

func TestNoDTOJSONTagUsesOmitempty(t *testing.T) {
	subjects := []reflect.Type{
		reflect.TypeOf(blockchainDTO{}),
		reflect.TypeOf(weatherItem{}),
		reflect.TypeOf(weatherDetail{}),
		reflect.TypeOf(stationSummary{}),
		reflect.TypeOf(statsDTO{}),
		reflect.TypeOf(paginationDTO{}),
		reflect.TypeOf(clientErrorDTO{}),
		reflect.TypeOf(serverErrorDTO{}),
		reflect.TypeOf(opsResponse{}),
		reflect.TypeOf(weather.WeatherData{}),
	}

	visited := 0
	seen := map[reflect.Type]bool{}
	for _, subject := range subjects {
		visited += walkForOmitempty(t, subject, seen)
	}

	const minFields = 60
	if visited < minFields {
		t.Fatalf("visited only %d fields, want at least %d; the walk is not reaching the DTO layer", visited, minFields)
	}
}

// walkForOmitempty reports on every json tag in t and its nested structs,
// returning the number of fields inspected.
func walkForOmitempty(t *testing.T, typ reflect.Type, seen map[reflect.Type]bool) int {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || seen[typ] {
		return 0
	}
	seen[typ] = true

	count := 0
	for i := range typ.NumField() {
		field := typ.Field(i)
		count++
		if strings.Contains(field.Tag.Get("json"), "omitempty") {
			t.Errorf("%s.%s carries omitempty: an omitted key is undefined in the SPA, "+
				"and `undefined !== null` is true — spec §13.1's crash list is exactly this bug",
				typ.Name(), field.Name)
		}
		count += walkForOmitempty(t, field.Type, seen)
	}
	return count
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 3 — the list item and the detail body agree on their shared fields.
//
// Mechanism: reflection over both structs, field by field.
// Mutation: change weatherItem.StationID to int32 → RED.
// Runtime sibling: Task 8's TestWeatherDetailAndListAgreeOnTheSharedEightFields,
// which compares marshaled bytes for ONE record. This gate covers every field
// for every possible value, which is what catches a numeric-width change that
// happens not to alter the fixture's rendering.
// ─────────────────────────────────────────────────────────────────────────────

func TestWeatherItemAndDetailAgreeOnTheSharedFields(t *testing.T) {
	itemType := reflect.TypeOf(weatherItem{})
	detailType := reflect.TypeOf(weatherDetail{})

	const sharedFields = 8
	if itemType.NumField() != sharedFields {
		t.Fatalf("weatherItem has %d fields, want %d", itemType.NumField(), sharedFields)
	}
	if detailType.NumField() != sharedFields+1 {
		t.Fatalf("weatherDetail has %d fields, want exactly one more than weatherItem (%d)",
			detailType.NumField(), sharedFields+1)
	}

	byTag := map[string]reflect.StructField{}
	for i := range detailType.NumField() {
		field := detailType.Field(i)
		byTag[field.Tag.Get("json")] = field
	}

	for i := range itemType.NumField() {
		itemField := itemType.Field(i)
		tag := itemField.Tag.Get("json")

		detailField, ok := byTag[tag]
		if !ok {
			t.Errorf("weatherDetail has no field tagged %q; the detail body must be the list item plus error", tag)
			continue
		}
		if detailField.Name != itemField.Name {
			t.Errorf("tag %q is %s on weatherItem and %s on weatherDetail", tag, itemField.Name, detailField.Name)
		}
		if detailField.Type != itemField.Type {
			t.Errorf("tag %q is %s on weatherItem and %s on weatherDetail: the two endpoints would render "+
				"the same field differently", tag, itemField.Type, detailField.Type)
		}
		if string(detailField.Tag) != string(itemField.Tag) {
			t.Errorf("tag %q: full struct tags differ (%q vs %q)", tag, itemField.Tag, detailField.Tag)
		}
		delete(byTag, tag)
	}

	extra := make([]string, 0, 1)
	for tag := range byTag {
		extra = append(extra, tag)
	}
	sort.Strings(extra)
	if len(extra) != 1 || extra[0] != "error" {
		t.Errorf("weatherDetail's extra fields are %v, want exactly [error] (spec §13.5)", extra)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 4 — every registered route carries all three security headers.
//
// Mechanism: drive the REAL NewRouter and NewOpsRouter over a table of every
// registered pattern plus a 404 and a 405.
// Mutation: remove withSecurityHeaders from either router → RED.
// Runtime sibling: this IS the runtime gate. It supersedes any source scan for
// the header names, so no such scan is written — a scan would pass on a
// router that set them on one branch only.
// ─────────────────────────────────────────────────────────────────────────────

type routeProbe struct {
	name     string
	method   string
	target   string
	ops      bool
	canceled bool
}

// doRequestWithContext drives one request of any method through a handler. The
// existing doGet/doGetCanceled helpers cover GET only, and this table needs a
// POST (verify) and two DELETEs (the 405 rows).
func doRequestWithContext(t *testing.T, ctx context.Context, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(ctx, method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEveryRegisteredRouteCarriesTheSecurityHeaders(t *testing.T) {
	probes := []routeProbe{
		{name: "health", method: http.MethodGet, target: pathHealth},
		{name: "ready", method: http.MethodGet, target: pathReady},
		// The SSE handler only returns on r.Context().Done(), so it is driven
		// with an already-canceled context. The middleware chain, the limiter
		// and the response head all still run.
		{name: "events", method: http.MethodGet, target: pathEvents, canceled: true},
		{name: "verify", method: http.MethodPost, target: pathVerify},
		{name: "proof", method: http.MethodGet, target: "/api/proof/" + strings.Repeat("ab", 32)},
		{name: "weather list", method: http.MethodGet, target: pathWeather},
		{name: "weather list trailing slash", method: http.MethodGet, target: pathWeather + "/"},
		{name: "weather detail", method: http.MethodGet, target: pathWeather + "/" + routerSeededWeatherID},
		{name: "stations list", method: http.MethodGet, target: pathStations},
		{name: "stations list trailing slash", method: http.MethodGet, target: pathStations + "/"},
		{name: "station detail", method: http.MethodGet, target: pathStations + "/42"},
		{name: "catch-all 404", method: http.MethodGet, target: "/api/does-not-exist"},
		{name: "405 on a known path", method: http.MethodDelete, target: pathWeather},
		{name: "ops", method: http.MethodGet, target: pathOps, ops: true},
		{name: "ops 404", method: http.MethodGet, target: "/api/does-not-exist", ops: true},
		{name: "ops 405", method: http.MethodDelete, target: pathOps, ops: true},
	}

	const minProbes = 12
	if len(probes) < minProbes {
		t.Fatalf("the route table has %d rows, want at least %d", len(probes), minProbes)
	}

	deps := testDeps(t)
	apiRouter := NewRouter(deps)
	opsRouter := NewOpsRouter(deps)

	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if probe.canceled {
				cancel()
			}

			router := apiRouter
			if probe.ops {
				router = opsRouter
			}

			rec := doRequestWithContext(t, ctx, router, probe.method, probe.target)

			for header, want := range map[string]string{
				headerContentTypeOptions: valueNoSniff,
				headerFrameOptions:       valueDeny,
				headerReferrerPolicy:     valueNoReferrer,
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s %s: %s = %q, want %q (status was %d)",
						probe.method, probe.target, header, got, want, rec.Code)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 5 — the two banned httprate helpers appear nowhere.
//
// Mechanism: an AST walk over internal/api AND internal/ratelimit looking for
// a selector named KeyByRealIP or CanonicalizeIP. AST rather than a regex over
// source text on purpose: B1's const-anchored regex was defeated by changing
// `const` to `var`, and a text scan also matches its own doc comments, which
// is how such a gate ends up unfailable in the other direction.
// Mutation: add a call to either helper → RED.
// Runtime sibling: Task 15's peer-gate tests, which assert the BEHAVIOR those
// helpers would break (KeyByRealIP trusts X-Forwarded-For[0] with no peer
// check; CanonicalizeIP collapses every 4-in-6 client into one bucket).
// STATED AT THE GATE: this scan exists because spec §6.1 bans the two by name
// and a reviewer greps for them. The behavioral tests are what actually prove
// the property.
// ─────────────────────────────────────────────────────────────────────────────

func TestNoSourceFileInAPIReferencesTheBannedHttprateHelpers(t *testing.T) {
	banned := map[string]string{
		"KeyByRealIP":    "trusts X-Forwarded-For[0] with no peer check",
		"CanonicalizeIP": `returns "::" for any 4-in-6 address, collapsing every mapped IPv4 client into one bucket`,
	}

	files := 0
	for _, dir := range []string{".", "../ratelimit"} {
		entries, readDirErr := os.ReadDir(dir)
		if readDirErr != nil {
			t.Fatalf("read dir %s: %v", dir, readDirErr)
		}
		fset := token.NewFileSet()
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			name := filepath.Join(dir, entry.Name())
			// parser.ParseFile opens the file itself, so this gate never calls
			// os.ReadFile at a computed path — which is also what keeps gosec
			// G304 quiet without a suppression.
			file, parseErr := parser.ParseFile(fset, name, nil, 0)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", name, parseErr)
			}
			files++
			ast.Inspect(file, func(n ast.Node) bool {
				// Both node shapes, deliberately: a qualified call
				// (httprate.KeyByRealIP) is a SelectorExpr, while a
				// hand-rolled local copy of the same broken logic — the more
				// likely regression now that the dependency is not taken — is
				// a bare Ident at its declaration and at every call site.
				var ident string
				switch node := n.(type) {
				case *ast.SelectorExpr:
					ident = node.Sel.Name
				case *ast.Ident:
					ident = node.Name
				default:
					return true
				}
				if why, isBanned := banned[ident]; isBanned {
					t.Errorf("%s references the banned helper %s: %s (spec §6.1)", name, ident, why)
				}
				return true
			})
		}
	}

	const minFiles = 20
	if files < minFiles {
		t.Fatalf("parsed only %d files across internal/api and internal/ratelimit, want at least %d; "+
			"a scan that found nothing must not pass as a clean result", files, minFiles)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 6 — no source in this package formats an error into a response body.
//
// Mechanism: an AST walk (not a line scan) over this package's embedded source.
// Two shapes are flagged:
//  1. a CallExpr to any WRITE SINK whose argument subtree contains a call to
//     .Error() — at any nesting depth, so fmt.Sprintf("…: %s", listErr.Error())
//     inside a sink argument is caught too, and the call may span any number of
//     lines because an AST has no lines;
//  2. any return statement inside statusForStoreError, which is the one
//     function that produces the client-safe message from an error and whose
//     `return http.StatusInternalServerError, err.Error()` carries no sink
//     token at all.
//
// WHY IT IS AN AST WALK: the line-oriented predecessor was evaded twice, both
// verified. A gofmt-clean MULTI-LINE sink call (each argument on its own line)
// put searchErr.Error() on a line with no sink token, and shape 2 has no sink
// token on any line. A gate that reads as structural while being textual is
// worse than an honest heuristic, so this is now actually structural.
//
// Mutations, all proven RED under the FULL package suite in a scratch copy
// outside the repository: (a) the exact multi-line evasion the review verified
// against the predecessor — writeError(\n w, r,\n http.StatusInternalServerError,
// \n searchErr.Error(),\n) in stations.go, gofmt-clean; (b) statusForStoreError's
// default branch returning err.Error(); (c) renaming statusForStoreError, which
// trips the liveness check at the end rather than passing vacuously; (d) the
// re-review's fmt.Sprintf("%v", searchErr) passed to a sink, which carries no
// .Error() token at all and was GREEN against the first AST version of this
// gate — the fmt.Sprint* arm below exists for it.
//
// KNOWN LIMITS, stated rather than overclaimed. This gate matches sinks and
// receivers BY NAME, because it does not run go/types:
//   - The `badReq` exemption below is by identifier. An upstream error
//     deliberately bound to a variable named badReq and passed to a sink as
//     .Error() would pass this gate. Nothing structural distinguishes them
//     without type information.
//   - An error laundered through a local string variable first
//     (msg := listErr.Error(); writeError(…, msg)) is not caught: that needs
//     dataflow, not a syntax walk.
//   - The fmt.Sprint* arm matches an OPERAND IDENT whose name ends in err/Err,
//     which is this package's mandatory naming convention (govet shadow) but is
//     still a name match: fmt.Sprintf("%v", e) with an error bound to a name
//     that does not end in err/Err escapes, as does Sprintf over a struct FIELD
//     or a call result rather than a bare ident.
// Both are covered behaviorally by the runtime siblings — Task 6's
// TestStatusForStoreErrorNeverEchoesTheErrorText, Task 7's and Task 20's
// opaque-500 tests, and the forbidden-substring assertions in params_test.go
// and middleware_test.go, which assert on the actual response bytes.
// ─────────────────────────────────────────────────────────────────────────────

// writeSinkIdents are the package-level write helpers, matched as a bare
// identifier at the call. writeSinkSelectors are matched on the SELECTOR name
// only, so w.Write, rw.Write and fmt.Fprintf all match regardless of the
// receiver's spelling — the predecessor's "w.Write" literal missed a renamed
// receiver.
var (
	writeSinkIdents = map[string]bool{
		"writeJSON":       true,
		"writeError":      true,
		"writeErrorCause": true,
		"writeStoreError": true,
	}
	writeSinkSelectors = map[string]bool{
		"Write":       true,
		"Fprint":      true,
		"Fprintf":     true,
		"Fprintln":    true,
		"WriteString": true,
	}
)

// errTextExemptReceivers are the receivers whose .Error() is a hand-written
// CLIENT message rather than an upstream error's text. badRequestError is
// constructed in params.go from a fixed string per parameter ("invalid status"),
// carries nothing from a driver, and spec §13.6 pins those strings verbatim in
// the 400 body — so passing it to a sink is correct and must not be flagged.
var errTextExemptReceivers = map[string]bool{"badReq": true}

// sprintFuncs are the fmt formatters that turn an error into a string WITHOUT
// ever writing .Error(). fmt.Sprintf("%v", searchErr) is byte-identical in the
// body to searchErr.Error() and was a live hole in this gate: gofmt-clean,
// single line, no .Error() token and no laundering variable.
var sprintFuncs = map[string]bool{"Sprintf": true, "Sprint": true, "Sprintln": true}

// errNamedIdent matches an identifier that names an error by this package's
// mandatory convention: govet's shadow rule forces every inner error binding to
// be named after its operation, so the realistic regression formats listErr,
// searchErr or snapErr. This is the predecessor regex's heuristic, now applied
// to an AST operand rather than to a line of text.
var errNamedIdent = regexp.MustCompile(`[Ee]rr$`)

// callsErrorText reports whether the subtree contains either shape that puts an
// upstream error's text into a string: a call to .Error() on a non-exempt
// receiver, or an fmt.Sprint* call over an error-named operand.
func callsErrorText(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return true
		}
		if sel.Sel.Name == "Error" && len(call.Args) == 0 {
			if recv, isIdent := sel.X.(*ast.Ident); isIdent && errTextExemptReceivers[recv.Name] {
				return true
			}
			found = true
			return false
		}
		// fmt.Sprint*(…, someErr, …) — any operand, any verb, so %v, %s and a
		// bare Sprint are all covered. The receiver is not required to be the
		// ident "fmt": a dot-import or an alias would still match on the
		// selector, and matching more widely here only costs a false positive
		// on a hypothetical non-fmt Sprintf, which a rename fixes.
		if !sprintFuncs[sel.Sel.Name] {
			return true
		}
		for _, arg := range call.Args {
			ident, isIdent := arg.(*ast.Ident)
			if !isIdent || errTextExemptReceivers[ident.Name] {
				continue
			}
			if errNamedIdent.MatchString(ident.Name) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// writeSinkName returns the sink name a CallExpr targets, or "".
func writeSinkName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if writeSinkIdents[fun.Name] {
			return fun.Name
		}
	case *ast.SelectorExpr:
		if writeSinkSelectors[fun.Sel.Name] {
			return fun.Sel.Name
		}
	}
	return ""
}

func TestNoSourceFileInAPIFormatsAnErrorIntoAResponseBody(t *testing.T) {
	entries, readDirErr := packageSourceFS.ReadDir(".")
	if readDirErr != nil {
		t.Fatalf("read embedded package dir: %v", readDirErr)
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		src, readErr := packageSourceFS.ReadFile(entry.Name())
		if readErr != nil {
			t.Fatalf("read embedded file %s: %v", entry.Name(), readErr)
		}
		// Parsed from the embedded bytes, so the walk sees exactly the source
		// that was compiled into this test binary and no os.ReadFile at a
		// computed path is involved (gosec G304).
		file, parseErr := parser.ParseFile(fset, entry.Name(), src, 0)
		if parseErr != nil {
			t.Fatalf("parse embedded file %s: %v", entry.Name(), parseErr)
		}
		scanned++

		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CallExpr:
				sink := writeSinkName(typed)
				if sink == "" {
					return true
				}
				for _, arg := range typed.Args {
					if !callsErrorText(arg) {
						continue
					}
					t.Errorf("%s:%d formats an error into a response body via %s\n"+
						"a *pgconn.PgError's Error() carries the SQLSTATE, Detail, Hint, ConstraintName, "+
						"ColumnName and TableName — none of which may reach a client",
						entry.Name(), fset.Position(typed.Pos()).Line, sink)
					return true
				}
			case *ast.FuncDecl:
				if typed.Name.Name != statusMapperName || typed.Body == nil {
					return true
				}
				for _, stmt := range typed.Body.List {
					ast.Inspect(stmt, func(inner ast.Node) bool {
						ret, isReturn := inner.(*ast.ReturnStmt)
						if !isReturn {
							return true
						}
						for _, result := range ret.Results {
							if callsErrorText(result) {
								t.Errorf("%s:%d %s returns an error's text as the client-safe message; "+
									"its default branch must return the msgInternal constant",
									entry.Name(), fset.Position(ret.Pos()).Line, statusMapperName)
							}
						}
						return true
					})
				}
			}
			return true
		})
	}

	const minScanned = 8
	if scanned < minScanned {
		t.Fatalf("scanned only %d non-test files, want at least %d", scanned, minScanned)
	}

	// Liveness: the FuncDecl arm above is silently vacuous if the function is
	// ever renamed, which is exactly B1's unfailable-gate shape.
	if !funcExistsInPackageSource(t, statusMapperName) {
		t.Fatalf("%s not found in this package's source: the return-path arm of this gate is scanning nothing",
			statusMapperName)
	}
}

// statusMapperName is the one function that turns an error into a status and a
// client-safe message. Named once, so the gate's return-path arm and its
// liveness check cannot drift apart.
const statusMapperName = "statusForStoreError"

// funcExistsInPackageSource reports whether the package declares a function of
// this name, so a gate that keys on a function name fails loudly on a rename
// instead of passing vacuously.
func funcExistsInPackageSource(t *testing.T, name string) bool {
	t.Helper()

	entries, readDirErr := packageSourceFS.ReadDir(".")
	if readDirErr != nil {
		t.Fatalf("read embedded package dir: %v", readDirErr)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		src, readErr := packageSourceFS.ReadFile(entry.Name())
		if readErr != nil {
			t.Fatalf("read embedded file %s: %v", entry.Name(), readErr)
		}
		file, parseErr := parser.ParseFile(fset, entry.Name(), src, 0)
		if parseErr != nil {
			t.Fatalf("parse embedded file %s: %v", entry.Name(), parseErr)
		}
		for _, decl := range file.Decls {
			if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name.Name == name {
				return true
			}
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 7 — the golden set is exactly the six committed files, and each parses.
//
// Mechanism: the embedded testdata directory versus a hardcoded name list;
// every file must be non-empty and must parse as JSON.
// Mutation: add a seventh golden, or delete one → RED. Both were observed at
// RUN TIME, not at compile time: the //go:embed pattern is testdata/*.json, so
// deleting ONE golden still matches the remaining five and the package compiles
// fine. (Only deleting ALL of them makes the pattern match nothing, which is
// the case the compiler rejects.) The earlier claim that a deletion is "caught
// at compile time by the embed pattern as well" was wrong; this test is the
// only gate on a single deletion.
// Runtime sibling: each endpoint's own golden test.
// WHAT THIS GATE DOES NOT DO: detect a LAUNDERED golden — one regenerated
// together with an unintended encoder change and committed, so the test passes
// against new, wrong bytes. go.yml's "Goldens are not stale" step does not close
// that hole either (it catches a stale golden, which is a different thing); the
// control is a human reviewing the golden diff in the PR. Do not read this gate
// as covering it.
// ─────────────────────────────────────────────────────────────────────────────

func TestGoldenFilesAreNotStale(t *testing.T) {
	want := []string{
		"health.json",
		"ops.json",
		"station_detail.json",
		"stations_list.json",
		"weather_detail.json",
		"weather_list.json",
	}

	entries, readDirErr := goldenFS.ReadDir("testdata")
	if readDirErr != nil {
		t.Fatalf("read embedded testdata: %v", readDirErr)
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())

		raw, readErr := goldenFS.ReadFile(path.Join("testdata", entry.Name()))
		if readErr != nil {
			t.Fatalf("read golden %s: %v", entry.Name(), readErr)
		}
		if len(raw) == 0 {
			t.Errorf("golden %s is empty", entry.Name())
			continue
		}
		var parsed any
		if unmarshalErr := json.Unmarshal(raw, &parsed); unmarshalErr != nil {
			t.Errorf("golden %s does not parse as JSON: %v", entry.Name(), unmarshalErr)
		}
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("golden set on disk is %v, want exactly %v; a new endpoint's golden must be added to this "+
			"list AND to go.yml's regenerate-and-diff step, or it ships with no staleness gate", got, want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate 9 — AttachBaseContext cannot be forgotten.
//
// Mechanism: api.ListenAndServe refuses a server whose BaseContext is nil.
// Mutation: delete the nil check in ListenAndServe → RED (the negative case
// then binds a port and returns http.ErrServerClosed instead of the sentinel).
// Runtime sibling: Task 21's shutdown tests, which show the COST of the
// omission (5.00 s versus ~1 ms with a live stream) but cannot notice the
// omission itself.
//
// Why a refusal at the entry point rather than a static gate over cmd/ call
// sites: there is no cmd/ in this tree yet, so a call-site gate could not be
// made to fail today — and the plan's rule is that a gate which cannot fail
// must be fixed or deleted. See ListenAndServe's doc comment for the residual.
// ─────────────────────────────────────────────────────────────────────────────

func TestListenAndServeRefusesAServerWithNoBaseContext(t *testing.T) {
	// An UNBINDABLE address on purpose. With a bindable one, the mutation this
	// gate names (delete the nil check) makes ListenAndServe serve forever and
	// the test HANGS instead of failing — a 10-minute timeout is a much worse
	// failure mode than an assertion, and a hang is the kind of red a reviewer
	// misreads as flake. Here the mutant returns a listen error instead, which
	// is not ErrNoBaseContext, so the mutation fails this test in milliseconds.
	srv := NewAPIServer("256.256.256.256:99999", http.NotFoundHandler())

	err := ListenAndServe(srv)
	if !errors.Is(err, ErrNoBaseContext) {
		t.Fatalf("ListenAndServe with no BaseContext returned %v, want ErrNoBaseContext; "+
			"forgetting AttachBaseContext must never be silent", err)
	}
}

func TestListenAndServeAcceptsAServerWithABaseContext(t *testing.T) {
	// The positive control. Without it the gate above is satisfied by a
	// ListenAndServe that refuses EVERY server, which would be a gate that
	// cannot be distinguished from a broken production path.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lc net.ListenConfig
	ln, listenErr := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatalf("listen: %v", listenErr)
	}
	addr := ln.Addr().String()
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("close probe listener: %v", closeErr)
	}

	srv := NewAPIServer(addr, http.NotFoundHandler())
	AttachBaseContext(ctx, srv)

	done := make(chan error, 1)
	go func() { done <- ListenAndServe(srv) }()

	// Give the listener a moment to come up, then shut it down. The assertion
	// is that ListenAndServe got PAST the nil check: ErrServerClosed can only
	// be returned by a server that actually served.
	deadline := time.After(5 * time.Second)
	for {
		dialer := net.Dialer{Timeout: 200 * time.Millisecond}
		conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr == nil {
			if closeErr := conn.Close(); closeErr != nil {
				t.Fatalf("close probe connection: %v", closeErr)
			}
			break
		}
		select {
		case serveErr := <-done:
			t.Fatalf("ListenAndServe returned before serving: %v", serveErr)
		case <-deadline:
			t.Fatalf("server never accepted a connection on %s", addr)
		case <-time.After(10 * time.Millisecond):
		}
	}

	if shutdownErr := Shutdown(context.Background(), srv); shutdownErr != nil {
		t.Fatalf("shutdown: %v", shutdownErr)
	}
	if serveErr := <-done; !errors.Is(serveErr, http.ErrServerClosed) {
		t.Fatalf("ListenAndServe returned %v, want http.ErrServerClosed", serveErr)
	}
}
