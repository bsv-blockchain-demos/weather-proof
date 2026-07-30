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
