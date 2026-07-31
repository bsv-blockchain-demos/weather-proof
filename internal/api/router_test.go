package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
)

// routerFixedNow is the injected clock for every router test, so the
// station-online boundary and any timestamp in a body are deterministic.
var routerFixedNow = time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

const routerSeededWeatherID = "router-weather-1"

const routerSeededStationID int64 = 42

// testDeps builds a Deps against a freshly seeded fake store. Every task that
// adds a Deps field updates this helper in the same commit, per the plan's
// "How Deps grows" table, so a later task's test cannot construct a Deps
// with a nil field that only fails at request time.
func testDeps(t *testing.T) Deps {
	t.Helper()
	s := fake.New()
	s.Now = func() time.Time { return routerFixedNow }

	s.SeedRecord(store.Record{
		ID:        routerSeededWeatherID,
		StationID: routerSeededStationID,
		Timestamp: routerFixedNow.Add(-time.Hour),
		Status:    store.StatusPending,
		CreatedAt: routerFixedNow.Add(-time.Hour),
	})
	lastReading := routerFixedNow
	s.SeedStation(store.Station{
		StationID:   routerSeededStationID,
		Name:        "Router Test Station",
		IsActive:    true,
		LastReading: &lastReading,
	})

	return Deps{
		Store:    s.Store(),
		Now:      func() time.Time { return routerFixedNow },
		PollRate: time.Minute,
		Logger:   slog.Default(),

		// 60 is PROOF_RATE_LIMIT_PER_MIN's default, written as a literal here so
		// a test that cares about the proof scope's number sets its own.
		ProofRateLimitPerMin: 60,

		// A FRESH hub per Deps, with the contract's caps. Never a package-level
		// one: the hub holds process-level state and a shared instance is
		// exactly the cross-test coupling that makes a cap test discriminate
		// only under a -run filter.
		Hub: NewHub(sseGlobalMax, sseConcurrentPerIP, discardLogger()),
	}
}

// TestNewRouterDoesNotPanicOnRegistration is the whole registration set's
// smoke test: a ServeMux pattern conflict panics at registration time, which
// this turns into a red build rather than a boot crash. It must be first in
// the file.
func TestNewRouterDoesNotPanicOnRegistration(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewRouter panicked on registration: %v", r)
		}
	}()
	NewRouter(testDeps(t))
}

func doGet(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// doGetCanceled drives one request whose context is ALREADY canceled. It is
// the only way to drive the streaming /api/events handler through an
// http.Handler synchronously: the handler's termination signal is
// r.Context().Done(), so a background context would hang the caller forever.
// The limiter, the hub admission and the response head all still run.
func doGetCanceled(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouterAcceptsABareQuestionMarkOnBothListRoutes(t *testing.T) {
	h := NewRouter(testDeps(t))

	for _, target := range []string{"/api/weather?", "/api/stations?"} {
		rec := doGet(t, h, target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: got status %d, want 200; body=%s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestRouterAcceptsTheTrailingSlashOnBothListRoutes(t *testing.T) {
	h := NewRouter(testDeps(t))

	for _, target := range []string{"/api/weather/", "/api/stations/"} {
		rec := doGet(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got status %d, want 200; body=%s", target, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"pagination"`) {
			t.Errorf("%s: body does not contain a list envelope's pagination key: %s", target, rec.Body.String())
		}
	}
}

func TestRouterRoutesADetailPathToTheDetailHandler(t *testing.T) {
	h := NewRouter(testDeps(t))

	targets := []string{
		"/api/weather/" + routerSeededWeatherID,
		"/api/stations/42",
	}
	for _, target := range targets {
		rec := doGet(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got status %d, want 200; body=%s", target, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"pagination"`) {
			t.Errorf("%s: detail body is not bare, contains pagination: %s", target, rec.Body.String())
		}
	}
}

func TestRouterDoesNotRedirect(t *testing.T) {
	h := NewRouter(testDeps(t))

	for _, target := range []string{"/api/weather", "/api/weather/", "/api/stations", "/api/stations/"} {
		rec := doGet(t, h, target)
		if rec.Code == http.StatusMovedPermanently || rec.Code == http.StatusPermanentRedirect {
			t.Errorf("%s: got status %d, must never redirect", target, rec.Code)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("%s: got status %d, want 200", target, rec.Code)
		}
	}
}

func TestRouterAnswersAnUnknownPathWithJSON404(t *testing.T) {
	h := NewRouter(testDeps(t))

	rec := doGet(t, h, "/api/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != contentTypeJSON {
		t.Errorf("got Content-Type %q, want %q", ct, contentTypeJSON)
	}
	var body clientErrorDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body does not unmarshal into clientErrorDTO: %v; body=%s", err, rec.Body.String())
	}

	// A routed path must NOT 404, or a handler that 404s everything would
	// pass this test vacuously.
	okRec := doGet(t, h, "/api/weather")
	if okRec.Code == http.StatusNotFound {
		t.Errorf("a registered route must not 404, got %d", okRec.Code)
	}
}

func TestRouterAnswersAWrongMethodWithJSON405(t *testing.T) {
	h := NewRouter(testDeps(t))

	// wantAllow is per case, not a shared http.MethodGet: POST /api/verify is
	// the API's only non-GET route and an "Allow: GET" there would tell a
	// client to retry with the one verb that cannot work. Without a case whose
	// expectation differs, onlyMethod's whole generalization is unasserted —
	// hardcoding http.MethodGet at methodNotAllowedJSON's call site left the
	// suite green.
	cases := []struct {
		method    string
		target    string
		wantAllow string
	}{
		{http.MethodPost, "/api/weather", http.MethodGet},
		{http.MethodDelete, "/api/stations/1", http.MethodGet},
		{http.MethodGet, pathVerify, http.MethodPost},
	}
	for _, c := range cases {
		req := httptest.NewRequestWithContext(context.Background(), c.method, c.target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: got status %d, want 405", c.method, c.target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != contentTypeJSON {
			t.Errorf("%s %s: got Content-Type %q, want %q", c.method, c.target, ct, contentTypeJSON)
		}
		if allow := rec.Header().Get("Allow"); allow != c.wantAllow {
			t.Errorf("%s %s: got Allow %q, want %q", c.method, c.target, allow, c.wantAllow)
		}
		var body clientErrorDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: body does not unmarshal into clientErrorDTO: %v", c.method, c.target, err)
		}
	}

	// A registered GET method on the same paths must NOT 405.
	for _, target := range []string{"/api/weather", "/api/stations/1"} {
		rec := doGet(t, h, target)
		if rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("GET %s must not 405, got %d", target, rec.Code)
		}
	}
}

func TestRouterRejectsAnUnregisteredMethodOnAKnownPath(t *testing.T) {
	h := NewRouter(testDeps(t))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/api/weather/abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /api/weather/abc: got status %d, want 405 (method check precedes id parse)", rec.Code)
	}
}

func TestRouterQueryParametersReachTheHandler(t *testing.T) {
	h := NewRouter(testDeps(t))

	target := "/api/weather?stationId=" + "42" + "&limit=1"
	rec := doGet(t, h, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var decoded struct {
		Items []struct {
			StationID int64 `json:"stationId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(decoded.Items) != 1 {
		t.Fatalf("got %d items, want 1; body=%s", len(decoded.Items), rec.Body.String())
	}
	if decoded.Items[0].StationID != routerSeededStationID {
		t.Errorf("got stationId %d, want %d", decoded.Items[0].StationID, routerSeededStationID)
	}
}

// panicRecordStore embeds a nil store.RecordStore so it satisfies the
// interface via promotion, and overrides only List to panic. No other method
// is ever called through the /api/weather route this test drives.
type panicRecordStore struct {
	store.RecordStore
}

func (panicRecordStore) List(_ context.Context, _ store.ListFilter) ([]store.Record, int64, error) {
	panic(errors.New(panicMsg))
}

// TestNewRouterOrdersRequestIDOutsideRecovery drives a panic THROUGH
// NewRouter itself, rather than a hand-composed chain, so a future reordering
// of withRequestID/withRecover in router.go has a reachable test to break.
// Verified in a scratch copy: swapping router.go's wiring to
// withRecover(d.Logger)(withRequestID(mux)) makes this test fail under the
// full package suite, because withRecover's own recovery handler reads
// requestIDFrom(r.Context()) — populated only if withRequestID ran first —
// both for the logged attrs and (via writeError) for the response body, so
// the two go empty/blank together and the equality below breaks.
func TestNewRouterOrdersRequestIDOutsideRecovery(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	d := testDeps(t)
	d.Logger = logger
	d.Store.Records = panicRecordStore{}

	h := NewRouter(d)
	rec := doGet(t, h, "/api/weather")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d; body=%s", rec.Code, rec.Body.String())
	}

	headerID := rec.Header().Get(requestIDHeader)
	if headerID == "" {
		t.Fatalf("expected a non-empty %s header", requestIDHeader)
	}

	var body serverErrorDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v; body=%s", err, rec.Body.String())
	}
	if body.RequestID == "" {
		t.Fatalf("expected a non-empty request_id in the body")
	}
	if body.RequestID != headerID {
		t.Fatalf("body request_id %q does not equal response header %q", body.RequestID, headerID)
	}

	var logRecord map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logRecord); err != nil {
		t.Fatalf("unmarshal log line: %v; raw=%s", err, buf.String())
	}
	logID, _ := logRecord["request_id"].(string)
	if logID == "" {
		t.Fatalf("expected a non-empty request_id in the log line")
	}
	if logID != headerID {
		t.Fatalf("logged request_id %q does not equal response header %q", logID, headerID)
	}
}

func TestRouterHasNoCORSHeadersOnAnyResponse(t *testing.T) {
	h := NewRouter(testDeps(t))

	targets := []string{
		"/api/weather", "/api/weather/", "/api/weather/" + routerSeededWeatherID,
		"/api/stations", "/api/stations/", "/api/stations/42",
		"/api/nope",
	}
	for _, target := range targets {
		rec := doGet(t, h, target)
		for _, header := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Vary"} {
			if v := rec.Header().Get(header); v != "" {
				t.Errorf("%s: header %s = %q, want absent", target, header, v)
			}
		}
	}
}

func TestRouterPatternInventoryIsExactlyTheExpectedSet(t *testing.T) {
	h := NewRouter(testDeps(t))

	registered := []string{
		"/api/weather", "/api/weather/",
		"/api/weather/" + routerSeededWeatherID,
		"/api/stations", "/api/stations/",
		"/api/stations/42",
	}
	for _, target := range registered {
		rec := doGet(t, h, target)
		if rec.Code < 200 || rec.Code >= 300 {
			t.Errorf("%s: got status %d, want 2xx", target, rec.Code)
		}
	}

	// The routes whose handlers belong to a later task answer 501 through their
	// placeholder, which is still proof the PATTERN is registered and therefore
	// that its limiter scope is live. A 404 here would mean an unlimited scope
	// shipped.
	placeholders := []string{
		pathHealth, pathReady,
		"/api/proof/" + strings.Repeat("ab", 32),
	}
	for _, target := range placeholders {
		rec := doGet(t, h, target)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: got status %d, want 501 (registered, handler pending)", target, rec.Code)
		}
	}

	// /api/events now carries a real handler and streams until the client
	// disconnects, so it cannot be driven with a background context: the
	// handler would never return. A pre-canceled context makes it answer 200
	// and unwind immediately, which is still proof the pattern is registered.
	if rec := doGetCanceled(t, h, pathEvents); rec.Code != http.StatusOK {
		t.Errorf("%s: got status %d, want 200", pathEvents, rec.Code)
	}

	unregistered := []string{"/api", "/api/weather/x/y", "/apiweather", "/API/weather"}
	for _, target := range unregistered {
		rec := doGet(t, h, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: got status %d, want 404", target, rec.Code)
		}
	}
}
