package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

func TestWithRequestIDSetsTheHeader(t *testing.T) {
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	id := rec.Header().Get(requestIDHeader)
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("expected a parseable UUID, got %q: %v", id, err)
	}
}

func TestWithRequestIDIsUniquePerRequest(t *testing.T) {
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		handler.ServeHTTP(rec, req)
		id := rec.Header().Get(requestIDHeader)
		if seen[id] {
			t.Fatalf("duplicate request id seen: %q", id)
		}
		seen[id] = true
	}
	if len(seen) != 50 {
		t.Fatalf("expected 50 distinct ids, got %d", len(seen))
	}
}

func TestWithRequestIDIgnoresAnInboundHeader(t *testing.T) {
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, "attacker-chosen")
	handler.ServeHTTP(rec, req)

	id := rec.Header().Get(requestIDHeader)
	if id == "attacker-chosen" {
		t.Fatalf("inbound header was echoed rather than replaced")
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("expected a parseable UUID, got %q: %v", id, err)
	}
}

func TestRequestIDFromReturnsEmptyWithoutTheMiddleware(t *testing.T) {
	if got := requestIDFrom(context.Background()); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

const panicMsg = "SQLSTATE 28P01: password authentication failed for user \"weather\""

func TestWithRecoverTurnsAPanicIntoAnOpaque500(t *testing.T) {
	handler := withRequestID(withRecover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(errors.New(panicMsg))
	})))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if got := rec.Header().Get(headerContentType); got != contentTypeJSON {
		t.Fatalf("expected Content-Type %q, got %q", contentTypeJSON, got)
	}
	var body serverErrorDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.Error != msgInternal {
		t.Fatalf("expected error %q, got %q", msgInternal, body.Error)
	}
	if body.RequestID == "" {
		t.Fatalf("expected non-empty request id")
	}
	raw := rec.Body.String()
	for _, forbidden := range []string{"SQLSTATE", "28P01", "password", "weather"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("body leaked forbidden substring %q: %s", forbidden, raw)
		}
	}

	// The struct-unmarshal assertions above cannot see an extra leaked key,
	// since json.Unmarshal into serverErrorDTO silently ignores anything not
	// named "error" or "request_id". Decode into a raw key set and assert it
	// is exactly those two, and separately assert the exact serialized body.
	var rawKeys map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawKeys); err != nil {
		t.Fatalf("unmarshal body into raw key set: %v", err)
	}
	if len(rawKeys) != 2 {
		t.Fatalf("expected exactly 2 keys, got %d: %v", len(rawKeys), rawKeys)
	}
	if _, ok := rawKeys["error"]; !ok {
		t.Fatalf("expected an %q key, got %v", "error", rawKeys)
	}
	if _, ok := rawKeys["request_id"]; !ok {
		t.Fatalf("expected a %q key, got %v", "request_id", rawKeys)
	}
	wantBody := `{"error":"internal server error","request_id":"` + body.RequestID + `"}` + "\n"
	if raw != wantBody {
		t.Fatalf("expected exact body %q, got %q", wantBody, raw)
	}
}

func TestWithRecoverLogsTheValueAndAStack(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := withRequestID(withRecover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(errors.New(panicMsg))
	})))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	logged := buf.String()
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("unmarshal log line: %v; raw: %s", err, logged)
	}
	if record["level"] != "ERROR" {
		t.Fatalf("expected ERROR level, got %v", record["level"])
	}
	respID := rec.Header().Get(requestIDHeader)
	logID, _ := record["request_id"].(string)
	if logID == "" || logID != respID {
		t.Fatalf("log request_id %q does not match response header %q", logID, respID)
	}
	stack, _ := record["stack"].(string)
	if stack == "" {
		t.Fatalf("expected a non-empty stack trace in the log")
	}
	if !strings.Contains(stack, "TestWithRecoverLogsTheValueAndAStack") {
		t.Fatalf("expected stack to contain this test's function name, got: %s", stack)
	}
}

// recordingResponseWriter counts WriteHeader calls so the "does not write
// twice" test can assert exactly one, and would surface a superfluous second
// WriteHeader (which net/http itself merely logs to stderr and ignores).
type recordingResponseWriter struct {
	header      http.Header
	statuses    []int
	body        bytes.Buffer
	wroteHeader bool
}

func newRecordingResponseWriter() *recordingResponseWriter {
	return &recordingResponseWriter{header: make(http.Header)}
}

func (rw *recordingResponseWriter) Header() http.Header { return rw.header }

func (rw *recordingResponseWriter) WriteHeader(status int) {
	rw.statuses = append(rw.statuses, status)
	rw.wroteHeader = true
}

func (rw *recordingResponseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.body.Write(b)
}

func TestWithRecoverDoesNotWriteTwiceWhenTheHandlerAlreadyWrote(t *testing.T) {
	handler := withRequestID(withRecover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
		panic(errors.New(panicMsg))
	})))
	rw := newRecordingResponseWriter()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rw, req)

	if len(rw.statuses) != 1 {
		t.Fatalf("expected exactly one WriteHeader call, got %d: %v", len(rw.statuses), rw.statuses)
	}
	if rw.statuses[0] != http.StatusOK {
		t.Fatalf("expected recorded status 200, got %d", rw.statuses[0])
	}
	if rw.body.String() != "hello" {
		t.Fatalf("expected body %q, got %q", "hello", rw.body.String())
	}
}

func TestWithRecoverRepanicsErrAbortHandler(t *testing.T) {
	handler := withRecover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		v := recover()
		panicErr, ok := v.(error)
		if !ok || !errors.Is(panicErr, http.ErrAbortHandler) {
			t.Fatalf("expected http.ErrAbortHandler, got %v", v)
		}
	}()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	t.Fatalf("expected a panic to propagate")
}

func TestWithRecoverPassesANonPanickingResponseThrough(t *testing.T) {
	handler := withRecover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", rec.Body.String())
	}
}

func TestStatusRecorderPassesFlushThrough(t *testing.T) {
	handler := withRequestID(withRecover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			http.Error(w, "flush failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	})))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", resp.StatusCode, body)
	}
	if len(body) != 0 {
		t.Fatalf("expected empty body (flush succeeded, no error text), got %q", body)
	}
}

func TestStatusRecorderReportsTheHandlersStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
		rec.WriteHeader(status)
		if rec.status != status {
			t.Fatalf("expected recorded status %d, got %d", status, rec.status)
		}
	}
}

func TestFiveHundredBodiesAlwaysCarryARequestID(t *testing.T) {
	handler := withRequestID(withRecover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, msg := statusForStoreError(errors.New("boom"))
		writeError(w, r, status, msg)
	})))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	var body serverErrorDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	headerID := rec.Header().Get(requestIDHeader)
	if body.RequestID == "" || body.RequestID != headerID {
		t.Fatalf("body request_id %q does not match header %q", body.RequestID, headerID)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func assertSecurityHeaders(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get(headerContentTypeOptions); got != valueNoSniff {
		t.Errorf("%s = %q, want %q", headerContentTypeOptions, got, valueNoSniff)
	}
	if got := h.Get(headerFrameOptions); got != valueDeny {
		t.Errorf("%s = %q, want %q", headerFrameOptions, got, valueDeny)
	}
	if got := h.Get(headerReferrerPolicy); got != valueNoReferrer {
		t.Errorf("%s = %q, want %q", headerReferrerPolicy, got, valueNoReferrer)
	}
}

func TestSecurityHeadersOnASuccessfulResponse(t *testing.T) {
	handler := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	assertSecurityHeaders(t, rec.Header())
}

func TestSecurityHeadersOnAFourHundred(t *testing.T) {
	handler := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusBadRequest, "bad request")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	assertSecurityHeaders(t, rec.Header())
}

// errRecordStore embeds a nil store.RecordStore so it satisfies the
// interface via promotion, and overrides only List to return an opaque
// error (as opposed to panicRecordStore, which panics). Used to drive the
// "store failure" 500, distinct from the "handler panics" 500.
type errRecordStore struct {
	store.RecordStore
}

func (errRecordStore) List(_ context.Context, _ store.ListFilter) ([]store.Record, int64, error) {
	return nil, 0, errors.New(panicMsg)
}

// TestSecurityHeadersOnAFiveHundred drives a panic THROUGH NewRouter itself,
// not a hand-composed chain — the panic path is the one that skips a
// middleware installed inside the recovery, so only the router's real
// wiring can catch that mutation. See TestNewRouterOrdersRequestIDOutsideRecovery
// for the analogous reasoning for withRequestID.
func TestSecurityHeadersOnAFiveHundred(t *testing.T) {
	d := testDeps(t)
	d.Store.Records = panicRecordStore{}
	h := NewRouter(d)
	rec := doGet(t, h, "/api/weather")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertSecurityHeaders(t, rec.Header())
}

// TestSecurityHeadersOnAFiveHundredFromAStoreFailure is the 500 sibling that
// TestSecurityHeadersOnAFiveHundred does not cover: an ordinary error
// returned by the store (mapped to an opaque 500 by statusForStoreError),
// as opposed to a panic recovered by withRecover.
func TestSecurityHeadersOnAFiveHundredFromAStoreFailure(t *testing.T) {
	d := testDeps(t)
	d.Store.Records = errRecordStore{}
	h := NewRouter(d)
	rec := doGet(t, h, "/api/weather")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	assertSecurityHeaders(t, rec.Header())
}

func TestSecurityHeadersOnAJSON404(t *testing.T) {
	h := NewRouter(testDeps(t))
	rec := doGet(t, h, "/api/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	assertSecurityHeaders(t, rec.Header())
}

func TestSecurityHeadersOnAJSON405(t *testing.T) {
	h := NewRouter(testDeps(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/weather", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	assertSecurityHeaders(t, rec.Header())
}

// TestSecurityHeadersOnEveryRegisteredRoute is a table over all eight read
// patterns plus the 404 and 405 cases, driven through NewRouter. Ten rows,
// and the test asserts it visited ten — a table-driven sweep that silently
// iterated an empty slice is precisely the vacuous-gate shape this guards
// against.
func TestSecurityHeadersOnEveryRegisteredRoute(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		want   int
	}{
		{"weather list", http.MethodGet, "/api/weather", http.StatusOK},
		{"weather list trailing slash", http.MethodGet, "/api/weather/", http.StatusOK},
		{"weather detail", http.MethodGet, "/api/weather/" + routerSeededWeatherID, http.StatusOK},
		{"stations list", http.MethodGet, "/api/stations", http.StatusOK},
		{"stations list trailing slash", http.MethodGet, "/api/stations/", http.StatusOK},
		{"stations detail", http.MethodGet, "/api/stations/42", http.StatusOK},
		{"weather list bare question mark", http.MethodGet, "/api/weather?", http.StatusOK},
		{"stations list bare question mark", http.MethodGet, "/api/stations?", http.StatusOK},
		{"unrouted path", http.MethodGet, "/api/nope", http.StatusNotFound},
		{"wrong method", http.MethodPost, "/api/weather", http.StatusMethodNotAllowed},
	}

	h := NewRouter(testDeps(t))
	visited := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			visited++
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.target, nil)
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, rec.Code)
			}
			assertSecurityHeaders(t, rec.Header())
		})
	}
	if visited != len(cases) {
		t.Fatalf("expected to visit %d cases, visited %d", len(cases), visited)
	}
	if visited != 10 {
		t.Fatalf("expected exactly 10 cases, got %d", visited)
	}
}

// TestSecurityHeadersAreSetBeforeTheHandlerWrites is the ordering assertion.
// It must go over a real network connection: httptest.NewRecorder does not
// enforce net/http's "headers written after WriteHeader are silent no-ops"
// semantics (its Header() map is mutable at any time), so only a real
// http.Server, exercised via httptest.NewServer, can catch a middleware that
// sets headers after calling next.ServeHTTP.
func TestSecurityHeadersAreSetBeforeTheHandlerWrites(t *testing.T) {
	handler := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	assertSecurityHeaders(t, resp.Header)
}

func TestSecurityHeaderValuesAreExact(t *testing.T) {
	handler := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get(headerContentTypeOptions); got != "nosniff" {
		t.Errorf("%s = %q, want %q", headerContentTypeOptions, got, "nosniff")
	}
	if got := rec.Header().Get(headerFrameOptions); got != "DENY" {
		t.Errorf("%s = %q, want %q", headerFrameOptions, got, "DENY")
	}
	if got := rec.Header().Get(headerReferrerPolicy); got != "no-referrer" {
		t.Errorf("%s = %q, want %q", headerReferrerPolicy, got, "no-referrer")
	}
}

// setShortWriteDeadline shortens the package writeDeadline for the duration
// of a test, restoring it on cleanup. A 30-second sleep in a unit test is how
// a suite becomes something nobody runs.
func setShortWriteDeadline(t *testing.T) time.Duration {
	t.Helper()
	original := writeDeadline
	short := 150 * time.Millisecond
	writeDeadline = short
	t.Cleanup(func() { writeDeadline = original })
	return short
}

// deadlineProbeHandler writes one byte, flushes it, sleeps past whatever
// write deadline the caller configured, then writes and flushes a second
// byte and reports the FLUSH error on result. The second write alone is not
// enough: net/http buffers small writes, so a one-byte Write can succeed
// against a full buffer without ever touching the underlying connection,
// and only Flush forces the actual syscall that the deadline governs.
func deadlineProbeHandler(sleep time.Duration, result chan<- error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_, _ = w.Write([]byte("a"))
		_ = rc.Flush()
		time.Sleep(sleep)
		_, _ = w.Write([]byte("b"))
		flushErr := rc.Flush()
		result <- flushErr
	}
}

// getViaClient issues a GET through client using an explicit
// context-carrying request, rather than http.Get / http.Client.Get: gosec's
// G107 flags a variable URL passed straight to http.Get, and golangci-lint's
// noctx rule requires every outbound request to carry a context.
func getViaClient(client *http.Client, url string) (*http.Response, error) {
	req, newErr := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if newErr != nil {
		return nil, newErr
	}
	return client.Do(req)
}

// drain performs a GET against url and discards the body, tolerating a
// connection reset caused by a server-side write-deadline expiry: the test
// cares about the error recorded server-side on the channel, not about the
// client's view of a deliberately truncated response.
func drain(url string) {
	resp, getErr := getViaClient(http.DefaultClient, url)
	if getErr != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func TestWriteDeadlineIsSetOnANonSSEHandler(t *testing.T) {
	short := setShortWriteDeadline(t)
	result := make(chan error, 1)
	handler := withWriteDeadline(discardLogger())(deadlineProbeHandler(short+50*time.Millisecond, result))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	go drain(srv.URL)

	select {
	case writeErr := <-result:
		if writeErr == nil {
			t.Fatal("expected the second write to fail after the deadline, got nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the handler's second write")
	}
}

func TestNoWriteDeadlineOnAnSSEExemptHandler(t *testing.T) {
	short := setShortWriteDeadline(t)
	result := make(chan error, 1)
	handler := withWriteDeadline(discardLogger())(markSSEExempt(deadlineProbeHandler(short+50*time.Millisecond, result)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	go drain(srv.URL)

	select {
	case writeErr := <-result:
		if writeErr != nil {
			t.Fatalf("expected the second write to succeed under the SSE exemption, got %v", writeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the handler's second write")
	}
}

func TestWriteDeadlineDoesNotBreakANormalResponse(t *testing.T) {
	handler := withWriteDeadline(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, getErr := getViaClient(http.DefaultClient, srv.URL)
	if getErr != nil {
		t.Fatalf("GET: %v", getErr)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

// TestWriteDeadlineFailureIsNotAFiveHundred drives withWriteDeadline directly
// against an httptest.ResponseRecorder, which supports neither
// SetWriteDeadline nor Unwrap. That is the documented, ordinary case for
// every ResponseRecorder-based unit test in this package, and it must not be
// treated as a server error.
func TestWriteDeadlineFailureIsNotAFiveHundred(t *testing.T) {
	handler := withWriteDeadline(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 despite an unsupported write deadline, got %d", rec.Code)
	}
}

// TestSSEExemptFlagDoesNotLeakToTheNextRequest proves the exemption is
// per-request context state, not a package-level flag: an SSE-exempt request
// followed by an ordinary request on the SAME client (and, with keep-alive,
// often the same TCP connection) must still get a deadline on the second
// request. A package-level bool set by markSSEExempt would leak and silently
// disable the deadline for every request after the first SSE connection.
func TestSSEExemptFlagDoesNotLeakToTheNextRequest(t *testing.T) {
	short := setShortWriteDeadline(t)
	sseResult := make(chan error, 1)
	normalResult := make(chan error, 1)

	mux := http.NewServeMux()
	mux.Handle("/sse", markSSEExempt(deadlineProbeHandler(short+50*time.Millisecond, sseResult)))
	mux.Handle("/normal", deadlineProbeHandler(short+50*time.Millisecond, normalResult))

	srv := httptest.NewServer(withWriteDeadline(discardLogger())(mux))
	defer srv.Close()

	client := srv.Client()

	drainWithClient(client, srv.URL+"/sse")
	select {
	case writeErr := <-sseResult:
		if writeErr != nil {
			t.Fatalf("expected the SSE-exempt request to succeed, got %v", writeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the SSE-exempt handler")
	}

	drainWithClient(client, srv.URL+"/normal")
	select {
	case writeErr := <-normalResult:
		if writeErr == nil {
			t.Fatal("expected the following ordinary request to still get a write deadline, got nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the ordinary handler")
	}
}

// TestWriteDeadlineSupportedThroughTheProductionRecoverChain drives
// SetWriteDeadline through the EXACT withRecover(withWriteDeadline(...))
// composition NewRouter builds, rather than withWriteDeadline alone: a
// handler in production always runs behind withRecover, which wraps the
// ResponseWriter in *statusRecorder before withWriteDeadline ever sees it.
// It asserts the returned error explicitly, rather than ignoring it or
// inferring success from a 200 (a ResponseRecorder-based test would return
// http.ErrNotSupported unconditionally and prove nothing about production).
// This is the test that catches statusRecorder.Unwrap being removed or
// renamed: without it, http.ResponseController cannot see past
// *statusRecorder to the real connection and this assertion fails.
func TestWriteDeadlineSupportedThroughTheProductionRecoverChain(t *testing.T) {
	result := make(chan error, 1)
	handler := withRecover(discardLogger())(withWriteDeadline(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		result <- rc.SetWriteDeadline(time.Now().Add(time.Second))
		w.WriteHeader(http.StatusOK)
	})))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	go drain(srv.URL)

	select {
	case setErr := <-result:
		if errors.Is(setErr, http.ErrNotSupported) {
			t.Fatalf("SetWriteDeadline returned ErrNotSupported through the production withRecover(withWriteDeadline(...)) chain — statusRecorder.Unwrap is not reachable: %v", setErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SetWriteDeadline")
	}
}

// TestWriteDeadlineIsSetThroughTheProductionRecoverChain re-runs the
// load-bearing deadline-expiry probe, but through withRecover(...) — the
// composition every real handler runs behind — instead of withWriteDeadline
// alone. This is what catches statusRecorder lacking FlushError: without
// it, http.ResponseController.Flush stops at statusRecorder's error-less
// Flush() and returns nil even after the deadline has genuinely expired,
// which is exactly how a dead SSE client (Task 18) would look alive
// forever.
func TestWriteDeadlineIsSetThroughTheProductionRecoverChain(t *testing.T) {
	short := setShortWriteDeadline(t)
	result := make(chan error, 1)
	handler := withRecover(discardLogger())(withWriteDeadline(discardLogger())(deadlineProbeHandler(short+50*time.Millisecond, result)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	go drain(srv.URL)

	select {
	case flushErr := <-result:
		if flushErr == nil {
			t.Fatal("expected the second flush to fail after the deadline through the withRecover(withWriteDeadline(...)) chain, got nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the handler's second write")
	}
}

// lockedResponseWriter serializes access to an underlying ResponseWriter
// that is not itself safe for concurrent use (httptest.ResponseRecorder's
// Body buffer is a plain *bytes.Buffer). It exists purely so
// TestApplyDeadlineIsRaceFreeUnderConcurrentWrites isolates the ONE race it
// is checking for — deadlineResponseWriter.applied — from an unrelated,
// expected race in the test double it writes through.
type lockedResponseWriter struct {
	http.ResponseWriter

	mu sync.Mutex
}

func (l *lockedResponseWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ResponseWriter.Write(b)
}

func (l *lockedResponseWriter) WriteHeader(status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ResponseWriter.WriteHeader(status)
}

// TestApplyDeadlineIsRaceFreeUnderConcurrentWrites writes to the same
// *deadlineResponseWriter from two goroutines at once — the shape an SSE
// heartbeat (Task 18) writing alongside the handler's own goroutine takes —
// and must be clean under `go test -race`. Before applied became an
// atomic.Bool, this test reproduced "WARNING: DATA RACE" on the applied
// field (read in the CompareAndSwap-guarded check, written just after) on
// every run under -race.
func TestApplyDeadlineIsRaceFreeUnderConcurrentWrites(t *testing.T) {
	safe := &lockedResponseWriter{ResponseWriter: httptest.NewRecorder()}
	dw := &deadlineResponseWriter{
		ResponseWriter: safe,
		rc:             http.NewResponseController(safe),
		ctx:            context.Background(),
		log:            discardLogger(),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = dw.Write([]byte("a"))
	}()
	go func() {
		defer wg.Done()
		_, _ = dw.Write([]byte("b"))
	}()
	wg.Wait()
}

func drainWithClient(client *http.Client, url string) {
	resp, getErr := getViaClient(client, url)
	if getErr != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
