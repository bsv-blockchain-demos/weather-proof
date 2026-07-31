package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// weatherGoldenPath is a constant per call site — see gosec G304's rule in
// the plan's Global Constraints.
const weatherGoldenPath = "testdata/weather_list.json"

var updateWeatherGolden = flag.Bool("update", false, "rewrite testdata/weather_list.json from the current handler")

// isoMillisPattern matches the exact fixed-width UTC millis shape.
var isoMillisPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// stationExcluded is the station id no filter test asks for — the
// "row a filter must reject" fixture requirement.
const (
	stationFiltered  int64 = 100
	stationOther     int64 = 200
	stationExcluded  int64 = 999
	stationForStatus int64 = 300
)

// seedWeatherFixture seeds five pairwise-distinct records: two on
// stationFiltered (for the stationId filter test), one on stationOther, one
// on stationExcluded (excluded from every filter test), and one more on
// stationForStatus with status "failed" so the status filter test has
// exactly one failed row among four distinct statuses. At least one record
// has every blockchain field nil, one has all three set. Two share a
// created_at so the id DESC tiebreaker is exercised.
func seedWeatherFixture(t *testing.T) *fake.Store {
	t.Helper()
	s := fake.New()

	tied := time.Date(2026, 4, 17, 15, 30, 0, 0, time.UTC)

	txid := "abc123def456abc123def456abc123def456abc123def456abc123def456abcd"
	vout := int32(0)
	height := int64(879412)
	processedAt := time.Date(2026, 4, 17, 15, 40, 4, 0, time.UTC)

	// r1, r2 share stationFiltered — the stationId filter's positive match.
	s.SeedRecord(store.Record{
		ID:        "id-1-aaaa",
		StationID: stationFiltered,
		Timestamp: time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC),
		Status:    store.StatusPending,
		CreatedAt: time.Date(2026, 4, 17, 15, 10, 0, 0, time.UTC),
	})
	s.SeedRecord(store.Record{
		ID:          "id-2-bbbb",
		StationID:   stationFiltered,
		Timestamp:   time.Date(2026, 4, 17, 15, 20, 0, 0, time.UTC),
		Status:      store.StatusCompleted,
		CreatedAt:   tied,
		TxID:        &txid,
		OutputIndex: &vout,
		BlockHeight: &height,
		ProcessedAt: &processedAt,
	})
	// r3 is on stationOther, status processing.
	s.SeedRecord(store.Record{
		ID:        "id-3-cccc",
		StationID: stationOther,
		Timestamp: time.Date(2026, 4, 17, 15, 25, 0, 0, time.UTC),
		Status:    store.StatusProcessing,
		CreatedAt: tied, // same created_at as id-2 — exercises id DESC tiebreak
	})
	// r4 is the excluded row: a station no filter test asks for.
	s.SeedRecord(store.Record{
		ID:        "id-4-dddd",
		StationID: stationExcluded,
		Timestamp: time.Date(2026, 4, 17, 15, 5, 0, 0, time.UTC),
		Status:    store.StatusPending,
		CreatedAt: time.Date(2026, 4, 17, 15, 1, 0, 0, time.UTC),
	})
	// r5 is failed, on stationForStatus, and carries a non-nil Error — used
	// by both the status filter test and the no-error-key test.
	errMsg := "broadcast rejected"
	s.SeedRecord(store.Record{
		ID:        "id-5-eeee",
		StationID: stationForStatus,
		Timestamp: time.Date(2026, 4, 17, 15, 35, 0, 0, time.UTC),
		Status:    store.StatusFailed,
		CreatedAt: time.Date(2026, 4, 17, 15, 45, 0, 0, time.UTC),
		Error:     &errMsg,
	})

	return s
}

// countingRecordStore wraps a store.RecordStore and counts calls to List, so
// TestWeatherListRejectsAnUnknownStatus can assert List was never called.
type countingRecordStore struct {
	store.RecordStore

	listCalls int
	getCalls  int
}

func (c *countingRecordStore) List(ctx context.Context, f store.ListFilter) ([]store.Record, int64, error) {
	c.listCalls++
	return c.RecordStore.List(ctx, f)
}

func (c *countingRecordStore) Get(ctx context.Context, id string) (store.Record, error) {
	c.getCalls++
	return c.RecordStore.Get(ctx, id)
}

// newWeatherListRequest builds a request for target. Requests are driven
// straight through the handler via httptest.ResponseRecorder rather than
// through rec.Result(): the recorder's Body is a plain *bytes.Buffer, not an
// io.ReadCloser wrapping a real connection, so there is nothing to close.
func newWeatherListRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
}

func doWeatherListRequest(t *testing.T, recs store.RecordStore, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := newWeatherListRequest(t, target)
	rec := httptest.NewRecorder()
	handleWeatherList(recs)(rec, req)
	return rec
}

// decodedWeatherItem carries ONLY the id, which is all the ordering, filtering
// and pagination assertions below need. It deliberately does NOT mirror
// weatherItem: isoMillis implements MarshalJSON and not UnmarshalJSON, so a
// weatherItem cannot be decoded at all — the wire format is write-only from this
// package's own types.
//
// The timestamp's shape is therefore asserted elsewhere and off a different
// decode: TestWeatherListTimestampsMatchTheIsoMillisShape reads it out of a
// map[string]any. Looking for a timestamp assertion on this type finds nothing.
type decodedWeatherItem struct {
	ID string `json:"id"`
}

// decodedWeatherList mirrors weatherListResponse for test decoding.
// Pagination unmarshals cleanly (plain ints); Items only needs the id here.
type decodedWeatherList struct {
	Items      []decodedWeatherItem `json:"items"`
	Pagination paginationDTO        `json:"pagination"`
}

func decodeWeatherList(t *testing.T, rec *httptest.ResponseRecorder) (decodedWeatherList, []byte) {
	t.Helper()
	raw := rec.Body.Bytes()
	var out decodedWeatherList
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, raw)
	}
	return out, raw
}

func TestWeatherListMatchesItsGoldenFile(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?page=1&limit=20")
	_, gotBytes := decodeWeatherList(t, resp)

	var gotIndented bytes.Buffer
	if err := json.Indent(&gotIndented, gotBytes, "", "  "); err != nil {
		t.Fatalf("indent got: %v", err)
	}

	if *updateWeatherGolden {
		if writeErr := os.WriteFile(weatherGoldenPath, gotIndented.Bytes(), 0o600); writeErr != nil {
			t.Fatalf("write golden: %v", writeErr)
		}
		return
	}

	wantRaw, readErr := os.ReadFile(weatherGoldenPath)
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

func TestWeatherListItemsIsNonEmptyForTheFixture(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?page=1&limit=20")
	out, _ := decodeWeatherList(t, resp)

	if len(out.Items) < 4 {
		t.Fatalf("len(items) = %d, want >= 4", len(out.Items))
	}
}

func TestWeatherListPreservesStoreOrder(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?page=1&limit=20")
	out, _ := decodeWeatherList(t, resp)

	if len(out.Items) == 0 {
		t.Fatalf("len(items) = 0")
	}

	wantRecs, _, err := s.List(context.Background(), store.ListFilter{Limit: 20, Offset: 0})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(wantRecs) != len(out.Items) {
		t.Fatalf("len mismatch: store=%d response=%d", len(wantRecs), len(out.Items))
	}
	for i, r := range wantRecs {
		if out.Items[i].ID != r.ID {
			t.Errorf("item %d: got id %q, want %q", i, out.Items[i].ID, r.ID)
		}
	}
}

func TestWeatherListEmptyStoreEmitsAnEmptyArrayNotNull(t *testing.T) {
	s := fake.New()
	resp := doWeatherListRequest(t, s, "/api/weather?page=1&limit=20")
	out, raw := decodeWeatherList(t, resp)

	if !bytes.Contains(raw, []byte(`"items":[]`)) {
		t.Errorf("raw body does not contain %q: %s", `"items":[]`, raw)
	}
	if out.Items == nil {
		t.Errorf("items unmarshaled as nil, want non-nil empty slice")
	}
	if len(out.Items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(out.Items))
	}
}

func TestWeatherListTotalPagesIsZeroOnAnEmptyStore(t *testing.T) {
	s := fake.New()
	resp := doWeatherListRequest(t, s, "/api/weather?page=1&limit=20")
	out, _ := decodeWeatherList(t, resp)

	if out.Pagination.Total != 0 {
		t.Errorf("total = %d, want 0", out.Pagination.Total)
	}
	if out.Pagination.TotalPages != 0 {
		t.Errorf("totalPages = %d, want 0", out.Pagination.TotalPages)
	}
}

func TestWeatherListFiltersByStationID(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?stationId=100")
	out, _ := decodeWeatherList(t, resp)

	if len(out.Items) == 0 {
		t.Fatalf("len(items) = 0")
	}

	ids := make(map[string]bool, len(out.Items))
	for _, it := range out.Items {
		ids[it.ID] = true
	}
	if !ids["id-1-aaaa"] || !ids["id-2-bbbb"] {
		t.Errorf("expected id-1-aaaa and id-2-bbbb present, got %v", ids)
	}
	if len(out.Items) != 2 {
		t.Errorf("len(items) = %d, want 2", len(out.Items))
	}
	if ids["id-4-dddd"] {
		t.Errorf("excluded row id-4-dddd present in stationId-filtered response")
	}
}

func TestWeatherListFiltersByStatus(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?status=failed")
	out, _ := decodeWeatherList(t, resp)

	if len(out.Items) == 0 {
		t.Fatalf("len(items) = 0")
	}

	ids := make(map[string]bool, len(out.Items))
	for _, it := range out.Items {
		ids[it.ID] = true
	}
	if !ids["id-5-eeee"] {
		t.Errorf("expected failed row id-5-eeee present, got %v", ids)
	}
	for _, excluded := range []string{"id-1-aaaa", "id-3-cccc", "id-2-bbbb"} {
		if ids[excluded] {
			t.Errorf("non-failed row %q present in status=failed response", excluded)
		}
	}
}

func TestWeatherListRejectsAnUnknownStatus(t *testing.T) {
	s := seedWeatherFixture(t)
	counting := &countingRecordStore{RecordStore: s}
	rec := doWeatherListRequest(t, counting, "/api/weather?status=done")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("body has no \"error\" key: %v", body)
	}
	if counting.listCalls != 0 {
		t.Errorf("List was called %d times, want 0", counting.listCalls)
	}
}

func TestWeatherListAcceptsABareQuestionMark(t *testing.T) {
	s := seedWeatherFixture(t)
	rec := doWeatherListRequest(t, s, "/api/weather?")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestWeatherListEchoesTheClampedPage(t *testing.T) {
	s := seedWeatherFixture(t)

	resp := doWeatherListRequest(t, s, "/api/weather?page=0")
	out, _ := decodeWeatherList(t, resp)
	if out.Pagination.Page != 1 {
		t.Errorf("page=0: pagination.page = %d, want 1", out.Pagination.Page)
	}

	resp2 := doWeatherListRequest(t, s, "/api/weather?page=-3")
	out2, _ := decodeWeatherList(t, resp2)
	if out2.Pagination.Page != 1 {
		t.Errorf("page=-3: pagination.page = %d, want 1", out2.Pagination.Page)
	}
}

func TestWeatherListEchoesTheClampedLimit(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?limit=500")
	out, _ := decodeWeatherList(t, resp)

	if out.Pagination.Limit != 100 {
		t.Errorf("pagination.limit = %d, want 100", out.Pagination.Limit)
	}
	if len(out.Items) > 100 {
		t.Errorf("len(items) = %d, want <= 100", len(out.Items))
	}
}

func TestWeatherListPaginatesWithoutChangingTotal(t *testing.T) {
	s := seedWeatherFixture(t)

	unpaged, _, err := s.List(context.Background(), store.ListFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(unpaged) != 5 {
		t.Fatalf("fixture has %d rows, want 5", len(unpaged))
	}

	resp := doWeatherListRequest(t, s, "/api/weather?limit=2&page=2")
	out, _ := decodeWeatherList(t, resp)

	if len(out.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(out.Items))
	}
	if out.Pagination.Total != 5 {
		t.Errorf("total = %d, want 5", out.Pagination.Total)
	}
	if out.Pagination.TotalPages != 3 {
		t.Errorf("totalPages = %d, want 3", out.Pagination.TotalPages)
	}
	if out.Items[0].ID != unpaged[2].ID || out.Items[1].ID != unpaged[3].ID {
		t.Errorf("page 2 ids = [%s %s], want [%s %s]",
			out.Items[0].ID, out.Items[1].ID, unpaged[2].ID, unpaged[3].ID)
	}
}

func TestWeatherListTimestampsMatchTheIsoMillisShape(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?page=1&limit=20")
	_, raw := decodeWeatherList(t, resp)

	var items []map[string]any
	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items = envelope.Items
	if len(items) == 0 {
		t.Fatalf("len(items) = 0")
	}

	for _, it := range items {
		for _, field := range []string{"timestamp", "createdAt"} {
			v, ok := it[field].(string)
			if !ok || !isoMillisPattern.MatchString(v) {
				t.Errorf("item %v field %q = %v, want isoMillis shape", it["id"], field, it[field])
			}
		}
		if pa := it["processedAt"]; pa != nil {
			v, ok := pa.(string)
			if !ok || !isoMillisPattern.MatchString(v) {
				t.Errorf("item %v processedAt = %v, want isoMillis shape or null", it["id"], pa)
			}
		}
	}
}

func TestWeatherListMapsAStoreFailureToAnOpaque500(t *testing.T) {
	s := fake.New()
	s.FailAll = errors.New("SQLSTATE 42P01: relation \"weather_records\" does not exist")

	req := newWeatherListRequest(t, "/api/weather?page=1&limit=20")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey{}, "01970000-cccc-7000-8000-000000000003"))
	rec := httptest.NewRecorder()
	handleWeatherList(s)(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	raw := rec.Body.Bytes()
	if bytes.Contains(raw, []byte("SQLSTATE")) || bytes.Contains(raw, []byte("weather_records")) {
		t.Errorf("body leaks driver detail: %s", raw)
	}
	var decoded struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.RequestID == "" {
		t.Errorf("request_id is empty")
	}
}

// weatherDetailGoldenPath is a constant per call site — gosec G304.
const weatherDetailGoldenPath = "testdata/weather_detail.json"

var updateWeatherDetailGolden = flag.Bool("update-detail", false, "rewrite testdata/weather_detail.json from the current handler")

// weatherDetailMux builds a mux carrying the {id...} pattern so an empty
// trailing-slash segment and r.PathValue("id") both behave as they would
// behind a real router — {id} alone 404s at the mux for a bare
// "/api/weather/" before the handler ever runs, which would hide the very
// distinction this handler exists to make.
func weatherDetailMux(recs store.RecordStore) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/weather/{id...}", handleWeatherDetail(recs))
	return mux
}

func doWeatherDetailRequest(t *testing.T, recs store.RecordStore, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	weatherDetailMux(recs).ServeHTTP(rec, req)
	return rec
}

func TestWeatherListItemsCarryNoErrorKey(t *testing.T) {
	s := seedWeatherFixture(t)
	resp := doWeatherListRequest(t, s, "/api/weather?page=1&limit=20")
	_, raw := decodeWeatherList(t, resp)

	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(envelope.Items) == 0 {
		t.Fatalf("len(items) = 0")
	}
	for _, it := range envelope.Items {
		if _, ok := it["error"]; ok {
			t.Errorf("item %v carries an \"error\" key", it["id"])
		}
	}
}

// detailFixtureID is the id of the one fully-populated record every detail
// test seeds — distinct from the list fixture's ids so the two test groups
// never collide.
const detailFixtureID = "id-detail-full-9001"

// seedWeatherDetailFixture seeds one fully populated record (txid,
// outputIndex 7, blockHeight, processedAt, error all non-nil) so the golden
// and key-presence tests can catch a projection that dropped a field.
func seedWeatherDetailFixture(t *testing.T) *fake.Store {
	t.Helper()
	s := fake.New()

	txid := "def456abc123def456abc123def456abc123def456abc123def456abc123def4"
	vout := int32(7)
	height := int64(881234)
	processedAt := time.Date(2026, 5, 2, 9, 15, 30, 0, time.UTC)
	errMsg := "script_too_large"

	s.SeedRecord(store.Record{
		ID:          detailFixtureID,
		StationID:   stationForStatus,
		Timestamp:   time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC),
		Data:        fullWeatherData(),
		Status:      store.StatusCompleted,
		CreatedAt:   time.Date(2026, 5, 2, 9, 5, 0, 0, time.UTC),
		TxID:        &txid,
		OutputIndex: &vout,
		BlockHeight: &height,
		ProcessedAt: &processedAt,
		Error:       &errMsg,
	})
	return s
}

// fullWeatherData returns a WeatherData with every field set to a distinct,
// non-zero value, so a projection dropping a key is caught by the golden
// rather than masked by a zero value.
func fullWeatherData() weather.WeatherData {
	return weather.WeatherData{
		AirDensity:                      1.204,
		AirTemperature:                  21,
		Brightness:                      15234,
		Conditions:                      "Partly Cloudy",
		DeltaT:                          3,
		DewPoint:                        12,
		FeelsLike:                       22,
		Icon:                            "partly-cloudy-day",
		IsPrecipLocalDayRainCheck:       true,
		IsPrecipLocalYesterdayRainCheck: false,
		LightningStrikeCountLast1hr:     2,
		LightningStrikeCountLast3hr:     5,
		LightningStrikeLastDistance:     14,
		LightningStrikeLastDistanceMsg:  "14 km",
		LightningStrikeLastEpoch:        1735689600,
		PrecipAccumLocalDay:             4,
		PrecipAccumLocalYesterday:       9,
		PrecipMinutesLocalDay:           6,
		PrecipMinutesLocalYesterday:     11,
		PrecipProbability:               35,
		PressureTrend:                   "Rising",
		RelativeHumidity:                58,
		SeaLevelPressure:                1013,
		SolarRadiation:                  452,
		StationPressure:                 1009.5,
		Time:                            1735693200,
		UV:                              4,
		WetBulbGlobeTemperature:         19,
		WetBulbTemperature:              17,
		WindAvg:                         8,
		WindDirection:                   270,
		WindDirectionCardinal:           "W",
		WindGust:                        13,
	}
}

func TestWeatherDetailMatchesItsGoldenFile(t *testing.T) {
	s := seedWeatherDetailFixture(t)
	resp := doWeatherDetailRequest(t, s, "/api/weather/"+detailFixtureID)

	var gotIndented bytes.Buffer
	if err := json.Indent(&gotIndented, resp.Body.Bytes(), "", "  "); err != nil {
		t.Fatalf("indent got: %v", err)
	}

	if *updateWeatherDetailGolden {
		if writeErr := os.WriteFile(weatherDetailGoldenPath, gotIndented.Bytes(), 0o600); writeErr != nil {
			t.Fatalf("write golden: %v", writeErr)
		}
		return
	}

	wantRaw, readErr := os.ReadFile(weatherDetailGoldenPath)
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

func TestWeatherDetailIsABareObjectWithNoEnvelope(t *testing.T) {
	s := seedWeatherDetailFixture(t)
	resp := doWeatherDetailRequest(t, s, "/api/weather/"+detailFixtureID)

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body["id"]; !ok {
		t.Errorf("body has no \"id\" key: %v", body)
	}
	for _, forbidden := range []string{"items", "data-only", "record"} {
		if _, ok := body[forbidden]; ok {
			t.Errorf("body carries a %q envelope key: %v", forbidden, body)
		}
	}
}

func TestWeatherDetailCarriesTheErrorKeyWhenSet(t *testing.T) {
	s := seedWeatherDetailFixture(t)
	resp := doWeatherDetailRequest(t, s, "/api/weather/"+detailFixtureID)

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != "script_too_large" {
		t.Errorf("error = %v, want %q", body["error"], "script_too_large")
	}
}

func TestWeatherDetailCarriesTheErrorKeyAsNullWhenUnset(t *testing.T) {
	s := fake.New()
	s.SeedRecord(store.Record{
		ID:        "id-detail-noerr",
		StationID: stationOther,
		Timestamp: time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC),
		Status:    store.StatusPending,
		CreatedAt: time.Date(2026, 5, 2, 9, 5, 0, 0, time.UTC),
	})
	resp := doWeatherDetailRequest(t, s, "/api/weather/id-detail-noerr")

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := body["error"]
	if !ok {
		t.Fatalf("body has no \"error\" key: %v", body)
	}
	if v != nil {
		t.Errorf("error = %v, want null", v)
	}
}

func TestWeatherDetailAllThirtyThreeDataKeysArePresentAndTyped(t *testing.T) {
	s := fake.New()
	s.SeedRecord(store.Record{
		ID:        "id-detail-zerodata",
		StationID: stationOther,
		Timestamp: time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC),
		Status:    store.StatusPending,
		CreatedAt: time.Date(2026, 5, 2, 9, 5, 0, 0, time.UTC),
	})
	resp := doWeatherDetailRequest(t, s, "/api/weather/id-detail-zerodata")

	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Data) != 33 {
		t.Fatalf("len(data) = %d, want 33", len(body.Data))
	}

	stringFields := map[string]bool{
		"conditions": true, "icon": true, "lightning_strike_last_distance_msg": true,
		"pressure_trend": true, "wind_direction_cardinal": true,
	}
	boolFields := map[string]bool{
		"is_precip_local_day_rain_check": true, "is_precip_local_yesterday_rain_check": true,
	}
	// The 24-key subset spec §13.1 lists as rendered; each key's zero value
	// must decode to the JSON type the field's schema type implies.
	rendered := []string{
		"air_density", "air_temperature", "brightness", "conditions", "delta_t",
		"dew_point", "feels_like", "icon", "is_precip_local_day_rain_check",
		"is_precip_local_yesterday_rain_check", "lightning_strike_count_last_1hr",
		"lightning_strike_count_last_3hr", "lightning_strike_last_distance",
		"pressure_trend", "relative_humidity", "sea_level_pressure",
		"solar_radiation", "station_pressure", "uv", "wet_bulb_temperature",
		"wind_avg", "wind_direction", "wind_direction_cardinal", "wind_gust",
	}
	if len(rendered) != 24 {
		t.Fatalf("test setup error: rendered has %d entries, want 24", len(rendered))
	}
	for _, field := range rendered {
		v, ok := body.Data[field]
		if !ok {
			t.Errorf("data missing key %q", field)
			continue
		}
		switch {
		case stringFields[field]:
			if _, ok := v.(string); !ok {
				t.Errorf("data[%q] = %v (%T), want string", field, v, v)
			}
		case boolFields[field]:
			if _, ok := v.(bool); !ok {
				t.Errorf("data[%q] = %v (%T), want bool", field, v, v)
			}
		default:
			if _, ok := v.(float64); !ok {
				t.Errorf("data[%q] = %v (%T), want number", field, v, v)
			}
		}
	}
}

func TestWeatherDetailUnknownIDIsA404WithTheVerbatimBody(t *testing.T) {
	s := seedWeatherDetailFixture(t)
	resp := doWeatherDetailRequest(t, s, "/api/weather/id-does-not-exist")

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != msgWeatherNotFound {
		t.Errorf("error = %v, want %q", body["error"], msgWeatherNotFound)
	}
}

func TestWeatherDetailInvalidIDIsA400NotA500(t *testing.T) {
	s := seedWeatherDetailFixture(t)

	cases := []struct {
		name   string
		target string
	}{
		{"nul byte", "/api/weather/%00"},
		{"invalid utf8", "/api/weather/%80"},
		{"empty segment via trailing slash", "/api/weather/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counting := &countingRecordStore{RecordStore: s}
			resp := doWeatherDetailRequest(t, counting, tc.target)

			if resp.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.Code)
			}
			if counting.getCalls != 0 {
				t.Errorf("Get was called %d times, want 0", counting.getCalls)
			}
		})
	}
}

func TestWeatherDetailNeverAnswersFiveHundredForAnIDShape(t *testing.T) {
	s := seedWeatherDetailFixture(t)

	adversarial := []string{
		"%00",
		"%80",
		"%ff",
		"../../etc/passwd",
		"..%2f..%2fetc%2fpasswd",
		strings.Repeat("a", 4096),
		"0192f0c1-",
		"NULL",
		"%27%20OR%201%3D1",
		"<script>",
		"\ufeff",
	}
	for _, id := range adversarial {
		t.Run(id, func(t *testing.T) {
			resp := doWeatherDetailRequest(t, s, "/api/weather/"+id)
			// A "../" segment makes net/http's ServeMux clean the path and
			// answer a 307 to the cleaned location before the handler ever
			// runs — that is the mux's own defense, not a 500, so follow it
			// once and assert on the final status.
			if resp.Code == http.StatusTemporaryRedirect {
				loc := resp.Header().Get("Location")
				resp = doWeatherDetailRequest(t, s, loc)
			}
			if resp.Code != http.StatusBadRequest && resp.Code != http.StatusNotFound {
				t.Errorf("id %q: status = %d, want 400 or 404", id, resp.Code)
			}
		})
	}

	// Positive control: the seeded id must answer 200, so a handler that
	// answers 400/404 to everything cannot pass this test.
	resp := doWeatherDetailRequest(t, s, "/api/weather/"+detailFixtureID)
	if resp.Code != http.StatusOK {
		t.Errorf("seeded id: status = %d, want 200", resp.Code)
	}
}

func TestWeatherDetailMapsAStoreFailureToAnOpaque500(t *testing.T) {
	s := fake.New()
	s.FailAll = errors.New("SQLSTATE 42P01: relation \"weather_records\" does not exist")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/weather/some-id", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey{}, "01970000-cccc-7000-8000-000000000004"))
	rec := httptest.NewRecorder()
	weatherDetailMux(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	raw := rec.Body.Bytes()
	if bytes.Contains(raw, []byte("SQLSTATE")) || bytes.Contains(raw, []byte("weather_records")) {
		t.Errorf("body leaks driver detail: %s", raw)
	}
	var decoded struct {
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Error != msgInternal {
		t.Errorf("error = %q, want %q", decoded.Error, msgInternal)
	}
	if decoded.RequestID == "" {
		t.Errorf("request_id is empty")
	}
}

func TestWeatherDetailTimestampsMatchTheIsoMillisShape(t *testing.T) {
	s := seedWeatherDetailFixture(t)
	resp := doWeatherDetailRequest(t, s, "/api/weather/"+detailFixtureID)

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"timestamp", "createdAt", "processedAt"} {
		v, ok := body[field].(string)
		if !ok || !isoMillisPattern.MatchString(v) {
			t.Errorf("field %q = %v, want isoMillis shape", field, body[field])
		}
	}
}

func TestWeatherDetailAndListAgreeOnTheSharedEightFields(t *testing.T) {
	s := seedWeatherDetailFixture(t)
	rec, err := s.Get(context.Background(), detailFixtureID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	item := toWeatherItem(rec)
	detail := toWeatherDetail(rec)

	itemJSON, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}

	var itemMap, detailMap map[string]json.RawMessage
	if err := json.Unmarshal(itemJSON, &itemMap); err != nil {
		t.Fatalf("unmarshal item: %v", err)
	}
	if err := json.Unmarshal(detailJSON, &detailMap); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}

	shared := []string{"id", "stationId", "timestamp", "data", "blockchain", "status", "createdAt", "processedAt"}
	for _, key := range shared {
		iv, ok := itemMap[key]
		if !ok {
			t.Fatalf("item missing key %q", key)
		}
		dv, ok := detailMap[key]
		if !ok {
			t.Fatalf("detail missing key %q", key)
		}
		if string(iv) != string(dv) {
			t.Errorf("key %q: item=%s detail=%s", key, iv, dv)
		}
	}
}
