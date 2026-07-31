package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// opsGoldenPath is a constant per call site — gosec G304's rule.
const opsGoldenPath = "testdata/ops.json"

// stubRecordStore implements store.RecordStore, overriding ONLY Snapshot.
// Embedding a nil store.RecordStore means any other method panics if
// called — acceptable, because handleOps calls nothing else on this
// interface, and a future handler that started calling one would need this
// fixture updated anyway.
type stubRecordStore struct {
	store.RecordStore

	snap    store.Snapshot
	snapErr error
}

func (s stubRecordStore) Snapshot(_ context.Context) (store.Snapshot, error) {
	return s.snap, s.snapErr
}

// stubStationStore implements store.StationStore, overriding ONLY Stats.
type stubStationStore struct {
	store.StationStore

	stats    store.Stats
	statsErr error
}

func (s stubStationStore) Stats(_ context.Context) (store.Stats, error) {
	return s.stats, s.statsErr
}

// opsFixtureSnapshot gives all six Snapshot buckets pairwise-distinct
// values — the B1 lesson: a fixture with four buckets at 1 let a swapped
// scan destination (pending reported as processing) pass the whole suite.
var opsFixtureSnapshot = store.Snapshot{
	PendingRows:             11,
	ProcessingRows:          22,
	FailedRows:              33,
	StillUnminedOlderThan1h: 44,
	MinedCount:              55,
	AbortedCount:            0,
}

// opsFixtureStats gives both Stats fields distinct values.
var opsFixtureStats = store.Stats{
	ActiveStations: 19,
	TotalTx:        2701,
}

// opsFixtureHubSize is the client count every ops test's hub carries. It is
// non-zero specifically so TestOpsSSEClientsReflectsTheHub fails against a
// hardcoded 0.
const opsFixtureHubSize = 3

// opsFixtureHub returns a hub with opsFixtureHubSize registered (and never
// unregistered) clients, for asserting sseClients reflects live state rather
// than a hardcoded value.
func opsFixtureHub(t *testing.T) *Hub {
	t.Helper()
	h := NewHub(sseGlobalMax, sseConcurrentPerIP, discardLogger())
	for i := 0; i < opsFixtureHubSize; i++ {
		if _, _, err := h.Register("ip-" + string(rune('a'+i))); err != nil {
			t.Fatalf("register client %d: %v", i, err)
		}
	}
	return h
}

func newOpsRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, pathOps, nil)
}

func doOpsRequest(t *testing.T, recs store.RecordStore, sts store.StationStore, h *Hub) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handleOps(recs, sts, h)(rec, newOpsRequest(t))
	return rec
}

func decodeOpsBody(t *testing.T, rec *httptest.ResponseRecorder) (map[string]any, []byte) {
	t.Helper()
	raw := rec.Body.Bytes()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal body: %v\nbody: %s", err, raw)
	}
	return m, raw
}

func TestOpsMatchesItsGoldenFile(t *testing.T) {
	recs := stubRecordStore{snap: opsFixtureSnapshot}
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	_, gotBytes := decodeOpsBody(t, rec)

	var gotIndented bytes.Buffer
	if err := json.Indent(&gotIndented, gotBytes, "", "  "); err != nil {
		t.Fatalf("indent got: %v", err)
	}

	wantRaw, readErr := os.ReadFile(opsGoldenPath)
	if readErr != nil {
		t.Fatalf("read golden: %v", readErr)
	}
	var wantIndented bytes.Buffer
	if err := json.Indent(&wantIndented, wantRaw, "", "  "); err != nil {
		t.Fatalf("indent want: %v", err)
	}

	if gotIndented.String() != wantIndented.String() {
		t.Errorf("golden mismatch\ngot:\n%s\nwant:\n%s", gotIndented.String(), wantIndented.String())
	}
}

// TestOpsBucketsAreNotSwapped is six separate assertions, not a deep-equal on
// a struct built the same way as the fixture, per the plan's fixture
// discipline: a deep-equal against a value derived the same way a swap bug
// was introduced can pass even when the mapping is wrong.
//
// Mutation check: swap PendingRows and ProcessingRows in handleOps's
// projection. This test must fail.
func TestOpsBucketsAreNotSwapped(t *testing.T) {
	recs := stubRecordStore{snap: opsFixtureSnapshot}
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	m, _ := decodeOpsBody(t, rec)

	cases := []struct {
		field string
		want  float64
	}{
		{"pendingRows", 11},
		{"processingRows", 22},
		{"failedRows", 33},
		{"stillUnminedOlderThan1h", 44},
		{"minedCount", 55},
		{"abortedCount", 0},
	}
	for _, c := range cases {
		got, ok := m[c.field].(float64)
		if !ok {
			t.Errorf("field %q missing or not a number", c.field)
			continue
		}
		if got != c.want {
			t.Errorf("field %q = %v, want %v", c.field, got, c.want)
		}
	}
}

// TestOpsStationStatsAreNotSwapped mirrors the above for the two Stats
// fields.
//
// Mutation check: swap ActiveStations and TotalTx in handleOps's projection.
// This test must fail.
func TestOpsStationStatsAreNotSwapped(t *testing.T) {
	recs := stubRecordStore{snap: opsFixtureSnapshot}
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	m, _ := decodeOpsBody(t, rec)

	active, ok := m["activeStations"].(float64)
	if !ok || active != 19 {
		t.Errorf("activeStations = %v, want 19", m["activeStations"])
	}
	totalTx, ok := m["totalTx"].(float64)
	if !ok || totalTx != 2701 {
		t.Errorf("totalTx = %v, want 2701", m["totalTx"])
	}
}

// TestOpsSSEClientsReflectsTheHub asserts a non-zero live count, so a
// hardcoded 0 fails.
func TestOpsSSEClientsReflectsTheHub(t *testing.T) {
	recs := stubRecordStore{snap: opsFixtureSnapshot}
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	m, _ := decodeOpsBody(t, rec)

	got, ok := m["sseClients"].(float64)
	if !ok || got != opsFixtureHubSize {
		t.Errorf("sseClients = %v, want %d", m["sseClients"], opsFixtureHubSize)
	}
}

// decodeStale extracts the "stale" field as []string, failing the test if it
// is missing, the wrong shape, or contains a non-string entry. It never
// returns nil for an empty list: callers get make([]string, 0), matching
// what json.Unmarshal produces for a present "[]" and letting a caller
// distinguish that from an absent/null key at the raw-map level separately.
func decodeStale(t *testing.T, m map[string]any) []string {
	t.Helper()
	rawStale, ok := m["stale"].([]any)
	if !ok {
		t.Fatalf("stale field missing or wrong shape: %v", m["stale"])
	}
	got := make([]string, 0, len(rawStale))
	for _, v := range rawStale {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("stale entry %v is not a string", v)
		}
		got = append(got, s)
	}
	return got
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("stale = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stale = %v, want exactly %v", got, want)
		}
	}
}

// TestOpsStaleListNamesAbortedCount asserts stale deep-equals
// []string{"abortedCount"} EXACTLY, not "contains", for a Snapshot whose
// AbortedCount is 0 — the shipped-code case, since the frozen RecordStore has
// no path that can write chain_status 'aborted' today.
//
// This test is DESIGNED TO FAIL once a Snapshot with a NONZERO AbortedCount
// is what production actually observes: staleOpsFields is now DERIVED from
// snap.AbortedCount, so the marker disappears automatically the moment the
// data says the bucket is live — see TestOpsStaleOmitsAbortedCountWhenLive,
// which pins the other direction. A future worker who sees only the OTHER
// test (the live one) failing, with THIS one still green on a zero fixture,
// should read that as correct: the two together are the whole gate, not a
// pair where one going red is unconditionally the "right" outcome.
//
// Mutation check: delete the Stale field from opsResponse (or stop populating
// it). This test must fail.
func TestOpsStaleListNamesAbortedCount(t *testing.T) {
	recs := stubRecordStore{snap: opsFixtureSnapshot} // AbortedCount: 0
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	m, _ := decodeOpsBody(t, rec)

	assertStringSliceEqual(t, decodeStale(t, m), []string{"abortedCount"})
}

// TestOpsStaleOmitsAbortedCountWhenLive is the direction
// TestOpsStaleListNamesAbortedCount alone could not catch: a Snapshot
// reporting a NONZERO AbortedCount — simulating Plan C's write path actually
// being exercised — must NOT carry "abortedCount" in stale, because the
// field is demonstrably being counted at that point and marking it stale
// would then be the OPPOSITE lie the brief warns about (an operator told a
// live counter is unimplemented). With AbortedCount the only field this
// package ever marks stale, the list must come back empty — asserted as
// exactly []string{}, not nil and not absent, since opsResponse.Stale has no
// omitempty and json.Marshal on the make([]string, 0, ...) staleOpsFields
// builds must emit "[]", never "null".
//
// Mutation check: revert staleOpsFields to the static
// []string{"abortedCount"} var (ignoring snap entirely) and set
// opsFixtureSnapshot.AbortedCount to a nonzero value. This test must fail.
func TestOpsStaleOmitsAbortedCountWhenLive(t *testing.T) {
	live := opsFixtureSnapshot
	live.AbortedCount = 77 // nonzero: simulates Plan C's write path firing.

	recs := stubRecordStore{snap: live}
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	m, raw := decodeOpsBody(t, rec)

	got := decodeStale(t, m)
	assertStringSliceEqual(t, got, []string{})

	// Belt-and-suspenders on the wire shape itself: "stale":[] must appear
	// literally in the raw JSON, never "stale":null.
	if strings.Contains(string(raw), `"stale":null`) {
		t.Errorf("stale serialized as null, want []: %s", raw)
	}
}

// TestOpsStillEmitsAbortedCountAsANumber asserts the key is present and
// numeric even though it is always zero today: dropping a key from a
// monitored JSON object breaks a dashboard silently, so the field ships AND
// is declared stale.
func TestOpsStillEmitsAbortedCountAsANumber(t *testing.T) {
	recs := stubRecordStore{snap: opsFixtureSnapshot}
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	m, _ := decodeOpsBody(t, rec)

	v, present := m["abortedCount"]
	if !present {
		t.Fatal("abortedCount key missing")
	}
	if _, ok := v.(float64); !ok {
		t.Errorf("abortedCount = %v (%T), want a number", v, v)
	}
}

// TestOpsCarriesNoFuelOrWalletFields asserts the deliberate omission of the
// heartbeat's fuel/wallet fields, so nobody adds them as zeros before there is
// anything to populate them.
func TestOpsCarriesNoFuelOrWalletFields(t *testing.T) {
	recs := stubRecordStore{snap: opsFixtureSnapshot}
	sts := stubStationStore{stats: opsFixtureStats}
	h := opsFixtureHub(t)

	rec := doOpsRequest(t, recs, sts, h)
	m, _ := decodeOpsBody(t, rec)

	forbidden := []string{
		"fuelPoolOutputs",
		"reserveSatoshis",
		"defaultBalanceSats",
		"walletConnected",
		"breakerOpen",
		"lastKeeperMintAt",
		"lastPublishAt",
	}
	for _, key := range forbidden {
		if _, present := m[key]; present {
			t.Errorf("body carries forbidden key %q", key)
		}
	}
}

// opsBoomText is the sentinel driver-detail string that must never reach a
// client. Built as a binary expression rather than a bare literal per the
// plan's gosec G101 convention, even though this one is not credential-shaped
// — consistency with the rest of the package's error fixtures.
var opsBoomText = "boom: driver detail that must never reach a" + " client"

// TestOpsMapsAStoreFailureToAnOpaque500 asserts the Snapshot-only and
// Stats-only failures SEPARATELY, because a handler ignoring one of the two
// errors answers 200 with a zeroed half.
//
// Mutation check: stop checking statsErr in handleOps (fall through to the
// response even when it is non-nil). The Stats-only subtest must fail.
func TestOpsMapsAStoreFailureToAnOpaque500(t *testing.T) {
	boom := errors.New(opsBoomText)

	t.Run("snapshot failure", func(t *testing.T) {
		d := testDeps(t)
		d.Store.Records = stubRecordStore{snapErr: boom}
		d.Store.Stations = stubStationStore{stats: opsFixtureStats}

		router := NewOpsRouter(d)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, newOpsRequest(t))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		m, raw := decodeOpsBody(t, rec)
		if strings.Contains(string(raw), "boom") || strings.Contains(string(raw), "driver detail") {
			t.Errorf("body leaked internal error text: %s", raw)
		}
		if m["error"] != msgInternal {
			t.Errorf("error = %v, want %q", m["error"], msgInternal)
		}
		if id, ok := m["request_id"].(string); !ok || id == "" {
			t.Errorf("request_id missing or empty: %v", m["request_id"])
		}
	})

	t.Run("stats failure", func(t *testing.T) {
		d := testDeps(t)
		d.Store.Records = stubRecordStore{snap: opsFixtureSnapshot}
		d.Store.Stations = stubStationStore{statsErr: boom}

		router := NewOpsRouter(d)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, newOpsRequest(t))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		m, raw := decodeOpsBody(t, rec)
		if strings.Contains(string(raw), "boom") || strings.Contains(string(raw), "driver detail") {
			t.Errorf("body leaked internal error text: %s", raw)
		}
		if m["error"] != msgInternal {
			t.Errorf("error = %v, want %q", m["error"], msgInternal)
		}
		if id, ok := m["request_id"].(string); !ok || id == "" {
			t.Errorf("request_id missing or empty: %v", m["request_id"])
		}
	})
}

// TestOpsIsNotRegisteredOnTheAPIRouter is spec §11.2's whole point: the
// Ingress path-splits /api, so a route on the API mux is world-reachable.
//
// Mutation check: register pathOps on NewRouter's mux. This test must fail.
func TestOpsIsNotRegisteredOnTheAPIRouter(t *testing.T) {
	router := NewRouter(testDeps(t))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newOpsRequest(t))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOpsIsRegisteredOnTheOpsRouter(t *testing.T) {
	router := NewOpsRouter(testDeps(t))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newOpsRequest(t))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestOpsRouterServesNoReadRoutes is the mirror of the previous assertion; a
// shared mux would pass one direction and fail this one.
func TestOpsRouterServesNoReadRoutes(t *testing.T) {
	router := NewOpsRouter(testDeps(t))
	paths := []string{pathWeather, pathStations, pathEvents, pathHealth}
	for _, p := range paths {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("path %q: status = %d, want 404", p, rec.Code)
		}
	}
}

// TestOpsIsNotRateLimited asserts exemption BY CONSTRUCTION — there is no
// limiter on this mux — so a later "let's reuse NewRouter" refactor is
// caught.
func TestOpsIsNotRateLimited(t *testing.T) {
	router := NewOpsRouter(testDeps(t))
	for i := 0; i < 1000; i++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, newOpsRequest(t))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}
}

// TestOpsCarriesTheSecurityHeaders asserts all three headers, since the ops
// mux gets the same middleware chain minus the limiter.
func TestOpsCarriesTheSecurityHeaders(t *testing.T) {
	router := NewOpsRouter(testDeps(t))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newOpsRequest(t))

	headers := map[string]string{
		headerContentTypeOptions: valueNoSniff,
		headerFrameOptions:       valueDeny,
		headerReferrerPolicy:     valueNoReferrer,
	}
	for name, want := range headers {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("header %q = %q, want %q", name, got, want)
		}
	}
}
