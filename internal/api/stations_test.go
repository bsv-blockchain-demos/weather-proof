package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// stationsGoldenPath is a constant per call site — see gosec G304's rule in
// the plan's Global Constraints.
const stationsGoldenPath = "testdata/stations_list.json"

var updateStationsGolden = flag.Bool("update-stations", false, "rewrite testdata/stations_list.json from the current handler")

// Fixture station ids, pairwise distinct and visually distinct from the
// weather fixture's ids.
const (
	stationActiveFresh   int64 = 5001
	stationActiveStale   int64 = 5002
	stationInactiveFresh int64 = 5003
	stationNilReading    int64 = 5004
)

// fixedNow matches internal/store/fake's default clock instant so the golden
// file is byte-stable without the test needing to set s.Now itself.
var fixedNow = time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

// pollRate is the fixture's injected poll interval. 3×pollRate = 15m, the
// online/offline freshness boundary.
const pollRate = 300 * time.Second

// seedStationFixture seeds four pairwise-distinct stations spanning the
// online-derivation matrix: active+fresh, active+stale (aged 3×pollRate+1s,
// just past the boundary), inactive+fresh, and one with LastReading/LastTemp
// both nil. TxRecords are pairwise distinct so a projection reading the wrong
// station's counter cannot pass by coincidence.
func seedStationFixture(t *testing.T) *fake.Store {
	t.Helper()
	s := fake.New()

	// freshReading is 2×pollRate old: within the real 3×pollRate freshness
	// window (online) but past a mistaken 1×pollRate window, so a mutation
	// narrowing stationStatus's comparison to pollRate rather than
	// 3*pollRate flips this fixture's active+fresh station to offline.
	freshReading := fixedNow.Add(-2 * pollRate)
	staleReading := fixedNow.Add(-(3*pollRate + time.Second))

	tempFresh := 21.5
	tempStale := 15.0
	tempInactive := 10.0
	heightA := int64(879100)

	s.SeedStation(store.Station{
		StationID:       stationActiveFresh,
		Name:            "Brix Station",
		Location:        "Bristol Docks",
		IsActive:        true,
		TxRecords:       11,
		LastReading:     &freshReading,
		LastTemp:        &tempFresh,
		LastConditions:  "Clear",
		LastBlockHeight: &heightA,
	})
	s.SeedStation(store.Station{
		StationID:      stationActiveStale,
		Name:           "Yeovil Station",
		Location:       "Yeovil Green",
		IsActive:       true,
		TxRecords:      22,
		LastReading:    &staleReading,
		LastTemp:       &tempStale,
		LastConditions: "Cloudy",
	})
	s.SeedStation(store.Station{
		StationID:      stationInactiveFresh,
		Name:           "Dover Station",
		Location:       "Dover Cliffs",
		IsActive:       false,
		TxRecords:      33,
		LastReading:    &freshReading,
		LastTemp:       &tempInactive,
		LastConditions: "Rain",
	})
	s.SeedStation(store.Station{
		StationID: stationNilReading,
		Name:      "Perth Station",
		Location:  "Perth Hills",
		IsActive:  true,
		TxRecords: 44,
		// LastReading and LastTemp are nil — the unknown-temperature station.
	})

	return s
}

// bumpTotalTxAndRecords drives Insert → ClaimPending → Complete against a
// station id NOT present in the fixture, so store.Stats().TotalTx and
// TotalRecords take on values wholly independent of the four seeded
// stations' TxRecords column — the whole point of
// TestStationListStatsComeFromTheStoreNotTheStations. Complete increments
// TotalTx by exactly 1 per call and TotalRecords by the number of pubs
// completed, and skips per-station bookkeeping entirely when the station id
// is unknown (see fake.Store.Complete's doc comment).
func bumpTotalTxAndRecords(t *testing.T, s *fake.Store, n int) {
	t.Helper()
	const orphanStation int64 = 9999

	ctx := context.Background()
	pubs := make([]store.Publication, 0, n)
	for i := range n {
		id := "orphan-" + string(rune('a'+i))
		ok, insertErr := s.Insert(ctx, store.NewRecord{
			ID:              id,
			StationID:       orphanStation,
			Timestamp:       fixedNow,
			ObservationTime: fixedNow.Add(time.Duration(i) * time.Minute),
			Data:            weather.WeatherData{},
		})
		if insertErr != nil || !ok {
			t.Fatalf("insert orphan record %d: ok=%v err=%v", i, ok, insertErr)
		}
		pubs = append(pubs, store.Publication{RecordID: id, OutputIndex: int32(i)})
	}

	claimed, claimErr := s.ClaimPending(ctx, n, uuidZero)
	if claimErr != nil {
		t.Fatalf("claim pending: %v", claimErr)
	}
	if len(claimed) != n {
		t.Fatalf("claimed %d records, want %d", len(claimed), n)
	}

	if _, completeErr := s.Complete(ctx, "orphantxid00000000000000000000000000000000000000000000000000", pubs); completeErr != nil {
		t.Fatalf("complete: %v", completeErr)
	}
}

func newStationListRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
}

func doStationListRequest(t *testing.T, sts store.StationStore, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := newStationListRequest(t, target)
	rec := httptest.NewRecorder()
	handleStationList(sts, func() time.Time { return fixedNow }, pollRate)(rec, req)
	return rec
}

func TestStationListMatchesItsGoldenFile(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	var gotIndented bytes.Buffer
	if err := json.Indent(&gotIndented, resp.Body.Bytes(), "", "  "); err != nil {
		t.Fatalf("indent got: %v", err)
	}

	if *updateStationsGolden {
		if writeErr := os.WriteFile(stationsGoldenPath, gotIndented.Bytes(), 0o600); writeErr != nil {
			t.Fatalf("write golden: %v", writeErr)
		}
		return
	}

	wantRaw, readErr := os.ReadFile(stationsGoldenPath)
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

func TestStationListHasExactlyThreeTopLevelKeys(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("top-level keys = %d (%v), want 3", len(raw), keysOf(raw))
	}
	for _, k := range []string{"stats", "stations", "pagination"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestStationListStationsIsNonEmptyForTheFixture(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	var out struct {
		Stations []json.RawMessage `json:"stations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Stations) != 4 {
		t.Fatalf("len(stations) = %d, want 4", len(out.Stations))
	}
}

func TestStationListEmptyStoreEmitsAnEmptyArrayNotNull(t *testing.T) {
	s := fake.New()
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	if !strings.Contains(resp.Body.String(), `"stations":[]`) {
		t.Fatalf("body does not contain \"stations\":[]: %s", resp.Body.String())
	}

	var out struct {
		Stats      map[string]json.RawMessage `json:"stats"`
		Pagination paginationDTO              `json:"pagination"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Stats) != 4 {
		t.Fatalf("stats keys = %d, want 4", len(out.Stats))
	}
	if out.Pagination.Total != 0 || out.Pagination.TotalPages != 0 {
		t.Errorf("pagination = %+v, want total=0 totalPages=0", out.Pagination)
	}
}

func TestStationOnlineDerivationAtTheBoundary(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	var out struct {
		Stations []struct {
			StationID int64  `json:"stationId"`
			Status    string `json:"status"`
		} `json:"stations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Stations) != 4 {
		t.Fatalf("len(stations) = %d, want 4", len(out.Stations))
	}

	// Literal strings, deliberately NOT the statusOnline/statusOffline
	// constants: comparing against the package's own constant would make
	// this test unfailable against a mutation that changes the constant's
	// wire value (e.g. capitalizing it to "Online"), since both sides of
	// the comparison would mutate together.
	want := map[int64]string{
		stationActiveFresh:   "online",
		stationActiveStale:   "offline",
		stationInactiveFresh: "offline",
		stationNilReading:    "offline",
	}
	got := make(map[int64]string, len(out.Stations))
	for _, st := range out.Stations {
		got[st.StationID] = st.Status
	}
	for id, wantStatus := range want {
		if got[id] != wantStatus {
			t.Errorf("station %d status = %q, want %q", id, got[id], wantStatus)
		}
	}
}

func TestStationSummaryTxRecordsAreNotAllEqual(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	var out struct {
		Stations []struct {
			StationID int64 `json:"stationId"`
			TxRecords int64 `json:"txRecords"`
		} `json:"stations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Stations) != 4 {
		t.Fatalf("len(stations) = %d, want 4", len(out.Stations))
	}

	want := map[int64]int64{
		stationActiveFresh:   11,
		stationActiveStale:   22,
		stationInactiveFresh: 33,
		stationNilReading:    44,
	}
	seen := make(map[int64]bool)
	for _, st := range out.Stations {
		if want[st.StationID] != st.TxRecords {
			t.Errorf("station %d txRecords = %d, want %d", st.StationID, st.TxRecords, want[st.StationID])
		}
		seen[st.TxRecords] = true
	}
	if len(seen) != 4 {
		t.Errorf("txRecords values are not pairwise distinct: %+v", out.Stations)
	}
}

func TestStationListLastTempIsPresentAndNullForTheUnknownStation(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	var out struct {
		Stations []struct {
			StationID int64           `json:"stationId"`
			LastTemp  json.RawMessage `json:"lastTemp"`
		} `json:"stations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Stations) != 4 {
		t.Fatalf("len(stations) = %d, want 4", len(out.Stations))
	}

	found := false
	for _, st := range out.Stations {
		if st.StationID != stationNilReading {
			continue
		}
		found = true
		if st.LastTemp == nil || string(st.LastTemp) != "null" {
			t.Errorf("lastTemp for unknown station = %q, want present key with value null", string(st.LastTemp))
		}
	}
	if !found {
		t.Fatalf("fixture station %d not found in response", stationNilReading)
	}
}

func TestStationListNumericSearchIsAnExactStationIDLookup(t *testing.T) {
	s := seedStationFixture(t)
	target := "/api/stations?search=" + strconv.FormatInt(stationActiveFresh, 10)
	resp := doStationListRequest(t, s.Stations(), target)

	var out struct {
		Stations []struct {
			StationID int64 `json:"stationId"`
		} `json:"stations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Stations) != 1 {
		t.Fatalf("len(stations) = %d, want 1: %+v", len(out.Stations), out.Stations)
	}
	if out.Stations[0].StationID != stationActiveFresh {
		t.Errorf("stationId = %d, want %d", out.Stations[0].StationID, stationActiveFresh)
	}

	others := []int64{stationActiveStale, stationInactiveFresh, stationNilReading}
	for _, id := range others {
		for _, st := range out.Stations {
			if st.StationID == id {
				t.Errorf("station %d present in numeric-search result, want absent", id)
			}
		}
	}
}

func TestStationListNumericSearchForAnAbsentIDIsAnEmpty200(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?search=999999")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	var out struct {
		Stations   []json.RawMessage `json:"stations"`
		Pagination paginationDTO     `json:"pagination"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Stations) != 0 {
		t.Errorf("stations = %+v, want empty", out.Stations)
	}
	if out.Pagination.Total != 0 || out.Pagination.TotalPages != 0 {
		t.Errorf("pagination = %+v, want total=0 totalPages=0", out.Pagination)
	}
}

func TestStationListSearchZeroIsA200(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?search=0")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.Code, resp.Body.String())
	}
}

func TestStationListSingleTokenSearchMatchesCaseInsensitively(t *testing.T) {
	for _, search := range []string{"brix", "BRIX"} {
		t.Run(search, func(t *testing.T) {
			s := seedStationFixture(t)
			resp := doStationListRequest(t, s.Stations(), "/api/stations?search="+search)

			var out struct {
				Stations []struct {
					StationID int64 `json:"stationId"`
				} `json:"stations"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			found := false
			for _, st := range out.Stations {
				if st.StationID == stationActiveFresh {
					found = true
				}
			}
			if !found {
				t.Errorf("search %q did not return station %d: %+v", search, stationActiveFresh, out.Stations)
			}
		})
	}
}

func TestStationListAdversarialSearchNeverErrors(t *testing.T) {
	tests := []struct {
		name      string
		search    string
		wantMatch bool
		want400   bool
	}{
		{name: "NUL byte", search: "%00", want400: true},
		{name: "invalid UTF-8 continuation byte", search: "%80", want400: true},
		{name: "single quote", search: "'"},
		{name: "double quote", search: `"`},
		{name: "ampersand", search: "&"},
		{name: "pipe", search: "|"},
		{name: "exclamation", search: "!"},
		{name: "colon", search: ":"},
		{name: "script tag", search: "<script>"},
		{name: "double dash", search: "--"},
		{name: "drop table fragment", search: ";DROP"},
		{name: "2 KiB string", search: strings.Repeat("a", 2048)},
		{name: "tab and newline", search: "%09%0A"},
		{name: "positive control exact fixture token", search: "brix", wantMatch: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := seedStationFixture(t)
			resp := doStationListRequest(t, s.Stations(), "/api/stations?search="+tc.search)

			if tc.want400 {
				if resp.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400, body=%s", resp.Code, resp.Body.String())
				}
				return
			}
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body=%s", resp.Code, resp.Body.String())
			}
			if tc.wantMatch {
				var out struct {
					Stations []json.RawMessage `json:"stations"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if len(out.Stations) == 0 {
					t.Errorf("positive control %q matched nothing", tc.search)
				}
			}
		})
	}
}

func TestStationListEchoesTheClampedPageAndLimit(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=0&limit=500")

	var out struct {
		Pagination paginationDTO `json:"pagination"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Pagination.Page != 1 {
		t.Errorf("page = %d, want 1", out.Pagination.Page)
	}
	if out.Pagination.Limit != stationsLimitMax {
		t.Errorf("limit = %d, want %d", out.Pagination.Limit, stationsLimitMax)
	}
}

func TestStationListStatsComeFromTheStoreNotTheStations(t *testing.T) {
	s := seedStationFixture(t)
	bumpTotalTxAndRecords(t, s, 3)

	wantStats, statsErr := s.Stats(context.Background())
	if statsErr != nil {
		t.Fatalf("stats: %v", statsErr)
	}
	// 11+22+33+44 = 110, deliberately far from the store's real TotalTx (1).
	if wantStats.TotalTx == 110 {
		t.Fatalf("test fixture invalid: store TotalTx coincidentally equals the page sum")
	}

	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")
	var out struct {
		Stats struct {
			TotalTx int64 `json:"totalTx"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Stats.TotalTx != wantStats.TotalTx {
		t.Errorf("stats.totalTx = %d, want %d (the store's value, not the page sum 110)", out.Stats.TotalTx, wantStats.TotalTx)
	}
}

func TestStationListStatsTotalDataPointsUsesTheStoreHelper(t *testing.T) {
	s := seedStationFixture(t)
	bumpTotalTxAndRecords(t, s, 5)

	wantStats, statsErr := s.Stats(context.Background())
	if statsErr != nil {
		t.Fatalf("stats: %v", statsErr)
	}

	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")
	var out struct {
		Stats struct {
			TotalDataPoints int64 `json:"totalDataPoints"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Stats.TotalDataPoints != wantStats.TotalDataPoints() {
		t.Errorf("stats.totalDataPoints = %d, want %d", out.Stats.TotalDataPoints, wantStats.TotalDataPoints())
	}
}

// statsOnlyFailStore fails Stats and passes List through to the embedded
// store.StationStore.
type statsOnlyFailStore struct {
	store.StationStore

	err error
}

func (f statsOnlyFailStore) Stats(_ context.Context) (store.Stats, error) {
	return store.Stats{}, f.err
}

// listOnlyFailStore fails List and passes Stats through to the embedded
// store.StationStore.
type listOnlyFailStore struct {
	store.StationStore

	err error
}

func (f listOnlyFailStore) List(_ context.Context, _ store.StationFilter) ([]store.Station, int64, error) {
	return nil, 0, f.err
}

func TestStationListMapsAStoreFailureToAnOpaque500(t *testing.T) {
	sentinelErr := errors.New("boom")

	t.Run("Stats fails", func(t *testing.T) {
		s := seedStationFixture(t)
		wrapped := statsOnlyFailStore{StationStore: s.Stations(), err: sentinelErr}
		resp := doStationListRequestWithRequestID(t, wrapped, "/api/stations?page=1&limit=50")
		assertOpaque500(t, resp)
	})

	t.Run("List fails", func(t *testing.T) {
		s := seedStationFixture(t)
		wrapped := listOnlyFailStore{StationStore: s.Stations(), err: sentinelErr}
		resp := doStationListRequestWithRequestID(t, wrapped, "/api/stations?page=1&limit=50")
		assertOpaque500(t, resp)
	})
}

// doStationListRequestWithRequestID mirrors doStationListRequest but attaches
// a request id to the context the way the (not-yet-wired) request-id
// middleware will, since writeError only populates request_id from the
// context and this package's own tests are the only caller until Task 11
// wires the middleware in.
func doStationListRequestWithRequestID(t *testing.T, sts store.StationStore, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := newStationListRequest(t, target)
	req = req.WithContext(context.WithValue(req.Context(), requestIDContextKey{}, "01970000-dddd-7000-8000-000000000004"))
	rec := httptest.NewRecorder()
	handleStationList(sts, func() time.Time { return fixedNow }, pollRate)(rec, req)
	return rec
}

func assertOpaque500(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", resp.Code, resp.Body.String())
	}
	var out serverErrorDTO
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error != msgInternal {
		t.Errorf("error = %q, want opaque message %q", out.Error, msgInternal)
	}
	if out.RequestID == "" {
		t.Errorf("request_id is empty, want present")
	}
	if strings.Contains(resp.Body.String(), "boom") {
		t.Errorf("body leaks the underlying error: %s", resp.Body.String())
	}
}

func TestStationListTimestampsMatchTheIsoMillisShape(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")

	var out struct {
		Stats struct {
			LastRecordWrite *string `json:"lastRecordWrite"`
		} `json:"stats"`
		Stations []struct {
			LastReading *string `json:"lastReading"`
		} `json:"stations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	checked := 0
	for _, st := range out.Stations {
		if st.LastReading == nil {
			continue
		}
		checked++
		if !isoMillisPattern.MatchString(*st.LastReading) {
			t.Errorf("lastReading %q does not match isoMillis shape", *st.LastReading)
		}
	}
	if checked == 0 {
		t.Fatalf("no non-null lastReading values found in fixture")
	}

	if out.Stats.LastRecordWrite != nil && !isoMillisPattern.MatchString(*out.Stats.LastRecordWrite) {
		t.Errorf("stats.lastRecordWrite %q does not match isoMillis shape", *out.Stats.LastRecordWrite)
	}
}

var uuidZero = uuid.UUID{}

// stationDetailGoldenPath is a constant per call site — gosec G304.
const stationDetailGoldenPath = "testdata/station_detail.json"

var updateStationDetailGolden = flag.Bool("update-station-detail", false, "rewrite testdata/station_detail.json from the current handler")

// countingStationStore wraps a store.StationStore and counts calls to Get, so
// tests can assert a 400 short-circuits before ever reaching the store.
type countingStationStore struct {
	store.StationStore

	getCalls int
}

func (c *countingStationStore) Get(ctx context.Context, stationID int64) (store.Station, error) {
	c.getCalls++
	return c.StationStore.Get(ctx, stationID)
}

// stationDetailMux builds a mux carrying the {stationId} pattern the real
// router (Task 11) will use.
func stationDetailMux(sts store.StationStore) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stations/{stationId}", handleStationDetail(sts, func() time.Time { return fixedNow }, pollRate))
	return mux
}

func doStationDetailRequest(t *testing.T, sts store.StationStore, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	stationDetailMux(sts).ServeHTTP(rec, req)
	return rec
}

func TestStationDetailMatchesItsGoldenFile(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationDetailRequest(t, s.Stations(), "/api/stations/"+strconv.FormatInt(stationActiveFresh, 10))

	var gotIndented bytes.Buffer
	if err := json.Indent(&gotIndented, resp.Body.Bytes(), "", "  "); err != nil {
		t.Fatalf("indent got: %v", err)
	}

	if *updateStationDetailGolden {
		if writeErr := os.WriteFile(stationDetailGoldenPath, gotIndented.Bytes(), 0o600); writeErr != nil {
			t.Fatalf("write golden: %v", writeErr)
		}
		return
	}

	wantRaw, readErr := os.ReadFile(stationDetailGoldenPath)
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

func TestStationDetailIsABareObjectWithNineKeys(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationDetailRequest(t, s.Stations(), "/api/stations/"+strconv.FormatInt(stationActiveFresh, 10))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantKeys := []string{
		"stationId", "name", "location", "status", "lastReading",
		"lastTemp", "lastConditions", "txRecords", "lastBlockHeight",
	}
	if len(raw) != len(wantKeys) {
		t.Fatalf("top-level keys = %d (%v), want %d", len(raw), keysOf(raw), len(wantKeys))
	}
	for _, k := range wantKeys {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	for _, forbidden := range []string{"stations", "stats", "pagination"} {
		if _, ok := raw[forbidden]; ok {
			t.Errorf("body carries envelope key %q, want bare object", forbidden)
		}
	}
}

func TestStationDetailSharesTheListsProjection(t *testing.T) {
	s := seedStationFixture(t)

	detailResp := doStationDetailRequest(t, s.Stations(), "/api/stations/"+strconv.FormatInt(stationActiveFresh, 10))

	listResp := doStationListRequest(t, s.Stations(), "/api/stations?page=1&limit=50")
	var listOut struct {
		Stations []json.RawMessage `json:"stations"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listOut); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}

	var match json.RawMessage
	for _, raw := range listOut.Stations {
		var probe struct {
			StationID int64 `json:"stationId"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("unmarshal list element: %v", err)
		}
		if probe.StationID == stationActiveFresh {
			match = raw
		}
	}
	if match == nil {
		t.Fatalf("station %d not found in list response", stationActiveFresh)
	}

	detailBytes := bytes.TrimSpace(detailResp.Body.Bytes())
	listBytes := bytes.TrimSpace(match)
	if !bytes.Equal(detailBytes, listBytes) {
		t.Errorf("detail body != matching list element\ndetail: %s\nlist:   %s", detailBytes, listBytes)
	}
}

func TestStationDetailUnknownIDIsA404(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationDetailRequest(t, s.Stations(), "/api/stations/999999")

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != msgStationNotFound {
		t.Errorf("error = %v, want %q", body["error"], msgStationNotFound)
	}
	if _, ok := body["request_id"]; ok {
		t.Errorf("404 body carries a request_id key: %v", body)
	}
}

func TestStationDetailUnparseableIDIsA400(t *testing.T) {
	s := seedStationFixture(t)

	cases := []string{"abc", "12x", "1.5", "%00"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			counting := &countingStationStore{StationStore: s.Stations()}
			resp := doStationDetailRequest(t, counting, "/api/stations/"+id)

			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", resp.Code, resp.Body.String())
			}
			if counting.getCalls != 0 {
				t.Errorf("Get was called %d times, want 0", counting.getCalls)
			}
		})
	}
}

func TestStationDetailInt32OverflowIsA400NotA500(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationDetailRequest(t, s.Stations(), "/api/stations/2147483648")

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", resp.Code, resp.Body.String())
	}
}

func TestStationDetailInt32BoundaryIsAccepted(t *testing.T) {
	s := fake.New()
	s.SeedStation(store.Station{
		StationID:      math.MaxInt32,
		Name:           "Boundary Station",
		Location:       "Edge Case Bay",
		IsActive:       true,
		TxRecords:      7,
		LastConditions: "Clear",
	})
	resp := doStationDetailRequest(t, s.Stations(), "/api/stations/2147483647")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.Code, resp.Body.String())
	}
}

func TestStationDetailNeverAnswersFiveHundredForAnIDShape(t *testing.T) {
	s := seedStationFixture(t)

	adversarial := []string{
		"abc", "12x", "1.5", "%00", "%80", "%ff",
		"2147483648", "-2147483649",
		strings.Repeat("9", 40),
		"NULL", "%27%20OR%201%3D1", "<script>",
	}
	for _, id := range adversarial {
		t.Run(id, func(t *testing.T) {
			resp := doStationDetailRequest(t, s.Stations(), "/api/stations/"+id)
			if resp.Code != http.StatusBadRequest && resp.Code != http.StatusNotFound {
				t.Errorf("id %q: status = %d, want 400 or 404", id, resp.Code)
			}
		})
	}

	// Positive control: the seeded id must answer 200, so a handler that
	// 404s (or 400s) everything cannot pass this test.
	resp := doStationDetailRequest(t, s.Stations(), "/api/stations/"+strconv.FormatInt(stationActiveFresh, 10))
	if resp.Code != http.StatusOK {
		t.Errorf("seeded id: status = %d, want 200", resp.Code)
	}
}

func TestStationDetailMapsAStoreFailureToAnOpaque500(t *testing.T) {
	s := fake.New()
	s.FailAll = errors.New("SQLSTATE 42P01: relation \"stations\" does not exist")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/stations/"+strconv.FormatInt(stationActiveFresh, 10), nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDContextKey{}, "01970000-dddd-7000-8000-000000000005"))
	rec := httptest.NewRecorder()
	stationDetailMux(s.Stations()).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if bytes.Contains(raw, []byte("SQLSTATE")) || bytes.Contains(raw, []byte("stations")) {
		t.Errorf("body leaks driver detail: %s", raw)
	}
	var decoded serverErrorDTO
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.RequestID == "" {
		t.Errorf("request_id is empty, want present")
	}
}

func TestStationDetailLastTempIsPresentAndNullWhenUnknown(t *testing.T) {
	s := seedStationFixture(t)
	resp := doStationDetailRequest(t, s.Stations(), "/api/stations/"+strconv.FormatInt(stationNilReading, 10))

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	lastTemp, ok := body["lastTemp"]
	if !ok {
		t.Fatalf("body has no \"lastTemp\" key: %v", body)
	}
	if string(lastTemp) != "null" {
		t.Errorf("lastTemp = %s, want null", string(lastTemp))
	}
}
