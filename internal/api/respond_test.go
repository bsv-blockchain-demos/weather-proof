package api

// respond_test.go covers the two properties the final review found unheld:
// that a 500 is LOGGED with the same request id it puts in the body, and that a
// nil Deps.Logger cannot turn a handler panic into a second panic inside the
// deferred recovery.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
)

// captureDefaultLogger points slog's default logger at a buffer for the
// duration of one test and restores it afterwards. The package logs 5xx through
// slog's default logger — the same one writeJSON already uses for an encode
// failure — so capturing it is how a test observes the record.
//
// No test in this package calls t.Parallel, so swapping a process-global for the
// length of one test is safe here; a parallel test would have to plumb a handler
// instead.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

// logRecords parses every line the capture buffer collected. A JSON handler
// writes one object per record, so a scanner over lines is the whole parser.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	records := make([]map[string]any, 0, 4)
	scanner := bufio.NewScanner(strings.NewReader(buf.String()))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record map[string]any
		if unmarshalErr := json.Unmarshal(line, &record); unmarshalErr != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, unmarshalErr)
		}
		records = append(records, record)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		t.Fatalf("scan log buffer: %v", scanErr)
	}
	return records
}

// storeFailure is the driver-shaped error the failing store returns. It is
// built inline at the errors.New call rather than assigned to a named const,
// because gosec G101 fires on a credential-shaped literal bound to a name.
func storeFailure() error {
	return errors.New(`SQLSTATE 42P01: relation "weather_records" does not exist (constraint weather_records_pkey)`)
}

// TestFiveHundredLogsTheSameRequestIDAsTheBody is the gate on the correlation
// id being USABLE. Before the final fix wave no 500 path logged anything at all:
// every response carried {"error":"internal server error","request_id":"…"} and
// a grep of the logs for that id returned nothing, which makes the id
// decoration (plan Global Constraints, spec §12.5, docs/api.md "Error bodies").
//
// The assertion is EQUALITY of the two captured strings — the id in the body and
// the id in the log record — not that each is separately non-empty. Two
// independently generated ids would satisfy the weaker check while still leaving
// an operator unable to correlate, which is the entire property.
func TestFiveHundredLogsTheSameRequestIDAsTheBody(t *testing.T) {
	buf := captureDefaultLogger(t)

	d := testDeps(t)
	failing := fake.New()
	failing.FailAll = storeFailure()
	d.Store = failing.Store()

	rec := httptest.NewRecorder()
	NewRouter(d).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, pathWeather, nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var body serverErrorDTO
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &body); unmarshalErr != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), unmarshalErr)
	}
	if body.RequestID == "" {
		t.Fatal("body carries no request_id")
	}
	if body.Error != msgInternal {
		t.Errorf("body message = %q, want %q", body.Error, msgInternal)
	}

	// The body must not leak the cause. Asserted here as well as in the
	// existing forbidden-substring tests, because this test is the one that
	// deliberately puts a driver-shaped error into the log.
	for _, forbidden := range []string{"SQLSTATE", "42P01", "weather_records_pkey"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("body %q leaks %q", rec.Body.String(), forbidden)
		}
	}

	records := logRecords(t, buf)
	if len(records) == 0 {
		t.Fatal("no log record was emitted for a 500")
	}

	matched := 0
	for _, record := range records {
		if record["level"] != slog.LevelError.String() {
			continue
		}
		loggedID, isString := record["request_id"].(string)
		if !isString || loggedID == "" {
			continue
		}
		if loggedID != body.RequestID {
			t.Errorf("log record request_id = %q, body request_id = %q: the two must be the SAME string",
				loggedID, body.RequestID)
			continue
		}
		matched++
		loggedErr, hasErr := record["error"].(string)
		if !hasErr || !strings.Contains(loggedErr, "SQLSTATE") {
			t.Errorf("log record for %s carries error=%q, want the underlying store error", loggedID, loggedErr)
		}
	}
	if matched != 1 {
		t.Fatalf("found %d error-level log records carrying the body's request id %q, want exactly 1",
			matched, body.RequestID)
	}
}

// TestReadyLogsThePingFailure covers the one 5xx whose body is a probe body
// rather than an error DTO, so it does not pass through writeErrorCause and
// needs its own log call.
func TestReadyLogsThePingFailure(t *testing.T) {
	buf := captureDefaultLogger(t)

	d := testDeps(t)
	failing := fake.New()
	failing.PingErr = storeFailure()
	d.Store = failing.Store()

	rec := httptest.NewRecorder()
	NewRouter(d).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, pathReady, nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(rec.Body.String(), "SQLSTATE") {
		t.Errorf("body %q leaks the driver error", rec.Body.String())
	}

	logged := false
	for _, record := range logRecords(t, buf) {
		loggedErr, hasErr := record["error"].(string)
		if record["level"] == slog.LevelError.String() && hasErr && strings.Contains(loggedErr, "SQLSTATE") {
			logged = true
		}
	}
	if !logged {
		t.Error("a failing readiness ping emitted no error-level log record carrying the cause")
	}
}

// panickingRecords panics on the two methods the two routers reach — List for
// GET /api/weather, Snapshot for GET /api/ops. It embeds the interface so the
// remaining methods exist without being written out; none of them is called.
type panickingRecords struct{ store.RecordStore }

// panicMessage is the panic value both routers' tests drive. It must not appear
// in any response body.
const panicMessage = "handler panic with a nil Deps.Logger"

func (panickingRecords) List(_ context.Context, _ store.ListFilter) ([]store.Record, int64, error) {
	panic(panicMessage)
}

func (panickingRecords) Snapshot(_ context.Context) (store.Snapshot, error) {
	panic(panicMessage)
}

// nilLoggerDeps is testDeps with Logger explicitly cleared. It also points the
// default logger at a discard sink, so the coerced logger's own output does not
// pollute the test log — the coercion is still exercised, since NewRouter has to
// perform the nil check either way.
func nilLoggerDeps(t *testing.T) Deps {
	t.Helper()

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	d := testDeps(t)
	d.Logger = nil
	return d
}

// TestNewRouterToleratesANilLoggerThroughAPanic is the double-panic gate.
// router.go passed d.Logger straight into withRecover and withWriteDeadline,
// both of which dereference it — withRecover INSIDE its deferred recovery. So a
// Deps built without a Logger (Plan C's cmd/ is the caller that will do this)
// turned the first panic in any handler into a second panic during recovery:
// no 500, no log, connection aborted. Every existing test set discardLogger(),
// so nothing covered it.
//
// This drives a real panicking handler rather than only constructing the router,
// because the constructor never touches the logger — the recovery path does.
func TestNewRouterToleratesANilLoggerThroughAPanic(t *testing.T) {
	d := nilLoggerDeps(t)
	d.Store.Records = panickingRecords{}

	rec := httptest.NewRecorder()
	NewRouter(d).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, pathWeather, nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: a panic under a nil Logger must still produce the opaque 500",
			rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), msgInternal) {
		t.Errorf("body = %q, want the opaque %q", rec.Body.String(), msgInternal)
	}
	if strings.Contains(rec.Body.String(), panicMessage) {
		t.Errorf("body %q leaks the panic value", rec.Body.String())
	}
}

// TestNewOpsRouterToleratesANilLogger is the ops sibling. The ops router wraps
// the same two middlewares with the same d.Logger.
func TestNewOpsRouterToleratesANilLogger(t *testing.T) {
	d := nilLoggerDeps(t)

	rec := httptest.NewRecorder()
	NewOpsRouter(d).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, pathOps, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ops status = %d, want %d", rec.Code, http.StatusOK)
	}

	panicking := nilLoggerDeps(t)
	panicking.Store.Records = panickingRecords{}

	panicRec := httptest.NewRecorder()
	NewOpsRouter(panicking).ServeHTTP(panicRec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, pathOps, nil))
	if panicRec.Code != http.StatusInternalServerError {
		t.Fatalf("ops status on a panic = %d, want %d", panicRec.Code, http.StatusInternalServerError)
	}
}

// TestNewHubCoercesItsCapsAndLogger covers NewHub's coercion, which was
// untested. The cap coercion is a SECURITY control: a zero globalMax must mean
// "one stream", never "unlimited" — the caps exist to bound concurrent streams,
// so reading zero as no-limit would invert the control it implements.
func TestNewHubCoercesItsCapsAndLogger(t *testing.T) {
	h := NewHub(0, -3, nil)

	if h.globalMax != 1 {
		t.Errorf("globalMax = %d, want 1: a zero cap must not mean unlimited", h.globalMax)
	}
	if h.perIP != 1 {
		t.Errorf("perIP = %d, want 1: a negative cap must not mean unlimited", h.perIP)
	}
	if h.log == nil {
		t.Error("log is nil: a nil logger must be coerced to slog.Default(), or the first refusal panics")
	}
}
