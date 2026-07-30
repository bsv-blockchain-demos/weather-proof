package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

func ptrStr(s string) *string        { return &s }
func ptrInt32(i int32) *int32        { return &i }
func ptrInt64(i int64) *int64        { return &i }
func ptrFloat64(f float64) *float64  { return &f }
func ptrTime(t time.Time) *time.Time { return &t }

func distinctWeatherData() weather.WeatherData {
	return weather.WeatherData{
		AirDensity:                      1.23,
		AirTemperature:                  21,
		Brightness:                      1000,
		Conditions:                      "clear",
		DeltaT:                          3,
		DewPoint:                        11,
		FeelsLike:                       22,
		Icon:                            "clear-day",
		IsPrecipLocalDayRainCheck:       true,
		IsPrecipLocalYesterdayRainCheck: false,
		LightningStrikeCountLast1hr:     2,
		LightningStrikeCountLast3hr:     4,
		LightningStrikeLastDistance:     5,
		LightningStrikeLastDistanceMsg:  "5 km",
		LightningStrikeLastEpoch:        1700000000,
		PrecipAccumLocalDay:             6,
		PrecipAccumLocalYesterday:       7,
		PrecipMinutesLocalDay:           8,
		PrecipMinutesLocalYesterday:     9,
		PrecipProbability:               10,
		PressureTrend:                   "rising",
		RelativeHumidity:                55,
		SeaLevelPressure:                1013,
		SolarRadiation:                  200,
		StationPressure:                 987.6,
		Time:                            1700000001,
		UV:                              3,
		WetBulbGlobeTemperature:         18,
		WetBulbTemperature:              17,
		WindAvg:                         12,
		WindDirection:                   270,
		WindDirectionCardinal:           "W",
		WindGust:                        20,
	}
}

func baseRecord() store.Record {
	return store.Record{
		ID:          "0192f0c1-abcd",
		StationID:   12345,
		Timestamp:   time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC),
		Data:        weather.WeatherData{},
		Status:      store.StatusPending,
		CreatedAt:   time.Date(2026, 4, 17, 15, 40, 0, 123_000_000, time.UTC),
		ProcessedAt: nil,
	}
}

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestWeatherItemAlwaysEmitsAllThirtyThreeDataKeys(t *testing.T) {
	r := baseRecord()
	item := toWeatherItem(r)
	m := marshalToMap(t, item)

	data, ok := m["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %#v", m["data"])
	}
	if len(data) != weather.DataFieldsPerRecord {
		t.Fatalf("got %d data keys, want %d", len(data), weather.DataFieldsPerRecord)
	}

	rt := reflect.TypeOf(weather.WeatherData{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if _, ok := data[name]; !ok {
			t.Errorf("missing data key %q", name)
		}
	}
}

func TestWeatherItemDataTypesMatchTheCrashList(t *testing.T) {
	r := baseRecord()
	r.Data = distinctWeatherData()
	item := toWeatherItem(r)
	m := marshalToMap(t, item)
	data := m["data"].(map[string]any)

	numericKeys := []string{
		"air_temperature", "feels_like", "dew_point", "wet_bulb_temperature",
		"wet_bulb_globe_temperature", "delta_t", "station_pressure",
		"sea_level_pressure", "air_density", "wind_avg", "wind_gust", "uv",
		"precip_accum_local_day", "precip_accum_local_yesterday",
		"relative_humidity",
		"lightning_strike_count_last_1hr", "lightning_strike_count_last_3hr",
		"wind_direction", "solar_radiation", "brightness", "precip_probability",
		"precip_minutes_local_day", "precip_minutes_local_yesterday",
		"lightning_strike_last_distance",
	}
	stringKeys := []string{
		"conditions", "pressure_trend", "wind_direction_cardinal",
		"lightning_strike_last_distance_msg",
	}

	for _, k := range numericKeys {
		v, ok := data[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if _, isNum := v.(float64); !isNum {
			t.Errorf("key %q: got %T, want number", k, v)
		}
	}
	for _, k := range stringKeys {
		v, ok := data[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if _, isStr := v.(string); !isStr {
			t.Errorf("key %q: got %T, want string", k, v)
		}
	}
}

func TestWeatherItemHasNoErrorKey(t *testing.T) {
	r := baseRecord()
	r.Error = ptrStr("boom")
	item := toWeatherItem(r)
	m := marshalToMap(t, item)
	if _, ok := m["error"]; ok {
		t.Fatalf("weatherItem must not carry an error key")
	}
}

func TestWeatherDetailHasAnErrorKeyEvenWhenNil(t *testing.T) {
	r := baseRecord()
	r.Error = nil
	detail := toWeatherDetail(r)
	m := marshalToMap(t, detail)
	v, ok := m["error"]
	if !ok {
		t.Fatalf("error key must be present")
	}
	if v != nil {
		t.Fatalf("error must be null, got %#v", v)
	}
}

func TestBlockchainIsAnObjectWithAllThreeKeysWhenNothingIsKnown(t *testing.T) {
	r := baseRecord()
	r.TxID = nil
	r.OutputIndex = nil
	r.BlockHeight = nil

	// Go through toWeatherItem (and its wire body), not toBlockchain in
	// isolation: a mutation that makes weatherItem.Blockchain a *blockchainDTO
	// that is nil when there is no txid still leaves toBlockchain itself
	// returning a well-formed value, so asserting only on toBlockchain's
	// direct output would not catch it.
	item := toWeatherItem(r)
	m := marshalToMap(t, item)

	bc, ok := m["blockchain"].(map[string]any)
	if !ok {
		t.Fatalf("blockchain is not a non-nil object: %#v", m["blockchain"])
	}
	if len(bc) != 3 {
		t.Fatalf("got %d keys, want 3: %#v", len(bc), bc)
	}
	for _, k := range []string{"txid", "outputIndex", "blockHeight"} {
		v, ok := bc[k]
		if !ok {
			t.Errorf("missing key %q", k)
		}
		if v != nil {
			t.Errorf("key %q: want nil, got %#v", k, v)
		}
	}

	// Also verify the standalone projection function directly.
	direct := toBlockchain(r)
	dm := marshalToMap(t, direct)
	if len(dm) != 3 {
		t.Fatalf("toBlockchain: got %d keys, want 3: %#v", len(dm), dm)
	}
}

func TestBlockchainCarriesEachValueWhenKnown(t *testing.T) {
	r := baseRecord()
	txid := strings.Repeat("ab", 32)
	r.TxID = ptrStr(txid)
	r.OutputIndex = ptrInt32(7)
	r.BlockHeight = ptrInt64(879412)
	bc := toBlockchain(r)
	m := marshalToMap(t, bc)
	if m["txid"] != txid {
		t.Errorf("txid: got %#v", m["txid"])
	}
	if m["outputIndex"] != float64(7) {
		t.Errorf("outputIndex: got %#v, want 7", m["outputIndex"])
	}
	if m["blockHeight"] != float64(879412) {
		t.Errorf("blockHeight: got %#v, want 879412", m["blockHeight"])
	}
}

func TestWeatherStatusPassesThroughAllFourWireLiterals(t *testing.T) {
	cases := []store.Status{store.StatusPending, store.StatusProcessing, store.StatusCompleted, store.StatusFailed}
	for _, s := range cases {
		t.Run(string(s), func(t *testing.T) {
			r := baseRecord()
			r.Status = s
			item := toWeatherItem(r)
			m := marshalToMap(t, item)
			if m["status"] != string(s) {
				t.Fatalf("got %#v, want %q", m["status"], s)
			}
		})
	}
}

func TestNoChainStatusLiteralAppearsInAnyBody(t *testing.T) {
	chainLiterals := []store.ChainStatus{store.ChainARCAccepted, store.ChainUnmined, store.ChainMined, store.ChainAborted}
	for _, cs := range chainLiterals {
		t.Run(string(cs), func(t *testing.T) {
			r := baseRecord()
			r.Status = store.StatusCompleted
			r.ChainStatus = &cs
			detail := toWeatherDetail(r)
			b, err := json.Marshal(detail)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(b), string(cs)) {
				t.Errorf("body must not contain chain literal %q: %s", cs, b)
			}
			if !strings.Contains(string(b), string(r.Status)) {
				t.Fatalf("positive control failed: body does not contain status %q: %s", r.Status, b)
			}
		})
	}
}

func TestStationSummaryLastTempIsPresentAndNullWhenUnknown(t *testing.T) {
	s := store.Station{StationID: 1, LastTemp: nil}
	summary := toStationSummary(s, time.Now(), 15*time.Minute)
	m := marshalToMap(t, summary)
	v, ok := m["lastTemp"]
	if !ok {
		t.Fatalf("lastTemp key must be present")
	}
	if v != nil {
		t.Fatalf("lastTemp must be null, got %#v", v)
	}
}

func TestStationSummaryLastTempCarriesAnIntegralValue(t *testing.T) {
	s := store.Station{StationID: 1, LastTemp: ptrFloat64(18.0)}
	summary := toStationSummary(s, time.Now(), 15*time.Minute)
	m := marshalToMap(t, summary)
	if m["lastTemp"] != 18.0 {
		t.Fatalf("got %#v, want 18.0", m["lastTemp"])
	}
}

func TestStationSummaryTxRecordsIsANonNullNumber(t *testing.T) {
	s := store.Station{StationID: 1, TxRecords: 0}
	summary := toStationSummary(s, time.Now(), 15*time.Minute)
	m := marshalToMap(t, summary)
	v, ok := m["txRecords"]
	if !ok {
		t.Fatalf("txRecords key must be present")
	}
	f, isFloat := v.(float64)
	if !isFloat {
		t.Fatalf("txRecords: got %T, want float64", v)
	}
	if f != 0.0 {
		t.Fatalf("txRecords: got %v, want 0", f)
	}
}

func TestStationStatusIsOnlineOnlyWhenActiveAndFresh(t *testing.T) {
	now := time.Date(2026, 4, 17, 16, 0, 0, 0, time.UTC)
	pollRate := 15 * time.Minute
	cases := []struct {
		name       string
		isActive   bool
		lastRead   *time.Time
		wantOnline bool
	}{
		{"active-freshest", true, ptrTime(now), true},
		{"active-just-under-boundary", true, ptrTime(now.Add(-(3*pollRate - time.Second))), true},
		{"active-at-boundary", true, ptrTime(now.Add(-3 * pollRate)), true},
		{"active-just-over-boundary", true, ptrTime(now.Add(-(3*pollRate + time.Second))), false},
		{"inactive-fresh", false, ptrTime(now), false},
		{"active-nil-last-reading", true, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := store.Station{StationID: 1, IsActive: c.isActive, LastReading: c.lastRead}
			got := stationStatus(s, now, pollRate)
			want := statusOffline
			if c.wantOnline {
				want = statusOnline
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestStationStatusEmitsOnlyTheTwoMeaningfulLiterals(t *testing.T) {
	now := time.Date(2026, 4, 17, 16, 0, 0, 0, time.UTC)
	pollRate := 15 * time.Minute
	cases := []struct {
		isActive bool
		lastRead *time.Time
	}{
		{true, ptrTime(now)},
		{true, ptrTime(now.Add(-(3*pollRate - time.Second)))},
		{true, ptrTime(now.Add(-3 * pollRate))},
		{true, ptrTime(now.Add(-(3*pollRate + time.Second)))},
		{false, ptrTime(now)},
		{true, nil},
	}
	for _, c := range cases {
		s := store.Station{StationID: 1, IsActive: c.isActive, LastReading: c.lastRead}
		got := stationStatus(s, now, pollRate)
		if got != statusOnline && got != statusOffline {
			t.Fatalf("got %q, want one of statusOnline/statusOffline", got)
		}
	}
}

func TestStatsProjectsTotalDataPointsThroughTheStoreHelper(t *testing.T) {
	stats := store.Stats{TotalRecords: 7}
	dto := toStats(stats)
	want := int64(7) * weather.DataFieldsPerRecord
	if dto.TotalDataPoints != want {
		t.Fatalf("got %d, want %d", dto.TotalDataPoints, want)
	}
}

func TestStatsFieldsAreNotSwapped(t *testing.T) {
	stats := store.Stats{ActiveStations: 19, TotalTx: 2701, TotalRecords: 51239}
	dto := toStats(stats)
	if dto.ActiveStations != 19 {
		t.Errorf("ActiveStations: got %d, want 19", dto.ActiveStations)
	}
	if dto.TotalTx != 2701 {
		t.Errorf("TotalTx: got %d, want 2701", dto.TotalTx)
	}
	want := int64(51239) * weather.DataFieldsPerRecord
	if dto.TotalDataPoints != want {
		t.Errorf("TotalDataPoints: got %d, want %d", dto.TotalDataPoints, want)
	}
}

func TestStatsLastRecordWriteIsNullWhenAbsent(t *testing.T) {
	stats := store.Stats{LastRecordWrite: nil}
	dto := toStats(stats)
	m := marshalToMap(t, dto)
	v, ok := m["lastRecordWrite"]
	if !ok {
		t.Fatalf("lastRecordWrite key must be present")
	}
	if v != nil {
		t.Fatalf("lastRecordWrite must be null, got %#v", v)
	}
}

func TestTotalPagesIsZeroWhenTotalIsZero(t *testing.T) {
	p := newPagination(1, 20, 0)
	if p.TotalPages != 0 {
		t.Fatalf("got %d, want 0", p.TotalPages)
	}
}

func TestTotalPagesRoundsUp(t *testing.T) {
	cases := []struct {
		limit int
		total int64
		want  int64
	}{
		{20, 1, 1},
		{20, 20, 1},
		{20, 21, 2},
		{20, 2701, 136},
		{50, 19, 1},
	}
	for _, c := range cases {
		p := newPagination(1, c.limit, c.total)
		if p.TotalPages != c.want {
			t.Errorf("limit=%d total=%d: got %d, want %d", c.limit, c.total, p.TotalPages, c.want)
		}
	}
}

func TestPaginationEchoesTheEffectivePageAndLimit(t *testing.T) {
	p := newPagination(3, 50, 500)
	if p.Page != 3 {
		t.Errorf("Page: got %d, want 3", p.Page)
	}
	if p.Limit != 50 {
		t.Errorf("Limit: got %d, want 50", p.Limit)
	}
}

func TestProjectionDoesNotAliasTheStoreRecordsPointers(t *testing.T) {
	r := baseRecord()
	txid := "aaaa"
	processedAt := time.Date(2026, 4, 17, 15, 41, 0, 0, time.UTC)
	r.TxID = &txid
	r.ProcessedAt = &processedAt

	item := toWeatherItem(r)
	b1, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	expected := string(b1)

	// Mutate through the original pointers.
	txid = "bbbb"
	processedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	b2, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b2) != expected {
		t.Fatalf("DTO aliased the source record's pointers: got %s, want %s", b2, expected)
	}
}

func TestErrorDTOOmitsRequestIDKeyOnlyWhenNil(t *testing.T) {
	e := errorDTO{Error: "Weather record not found"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"error":"Weather record not found","request_id":null}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func TestNoDTOFieldUsesOmitempty(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(weatherItem{}),
		reflect.TypeOf(weatherDetail{}),
		reflect.TypeOf(blockchainDTO{}),
		reflect.TypeOf(stationSummary{}),
		reflect.TypeOf(statsDTO{}),
		reflect.TypeOf(paginationDTO{}),
		reflect.TypeOf(weather.WeatherData{}),
	}

	fieldCount := 0
	for _, rt := range types {
		for i := 0; i < rt.NumField(); i++ {
			fieldCount++
			tag := rt.Field(i).Tag.Get("json")
			if strings.Contains(tag, "omitempty") {
				t.Errorf("%s.%s carries omitempty in tag %q", rt.Name(), rt.Field(i).Name, tag)
			}
		}
	}
	if fieldCount < 60 {
		t.Fatalf("positive control failed: only inspected %d fields, want >= 60", fieldCount)
	}
}
