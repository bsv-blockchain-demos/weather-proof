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
	"testing"

	"github.com/google/uuid"
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

func TestWithRequestIDIgnoresAnInboundHeaderWithCRLF(t *testing.T) {
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header["X-Request-Id"] = []string{"a\r\nX-Injected: 1"}
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Injected") != "" {
		t.Fatalf("expected no X-Injected header, header injection succeeded")
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
