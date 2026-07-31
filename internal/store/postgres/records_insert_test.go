package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// Compile-time proof that Insert's signature is exactly the one
// store.RecordStore declares. A method value on a nil pointer is legal
// because it is never called.
var _ func(context.Context, store.NewRecord) (bool, error) = (*postgres.RecordStore)(nil).Insert

// fullWeatherData returns a WeatherData with all 33 fields set, each to a
// distinct value, so a jsonb round trip that transposes any two fields is
// detectable rather than accidentally correct.
//
// NOT all non-zero, and the exception matters. IsPrecipLocalYesterdayRainCheck
// is deliberately false, so the two booleans cover both states — which makes it
// the ONE field a value comparison cannot prove was stored: a dropped key
// unmarshals straight back to false and `got != want` never fires. The
// count(jsonb_object_keys) = DataFieldsPerRecord assertion in
// TestJSONBRoundTripsAllThirtyThreeFields is what covers that case, and it is
// required for exactly this reason rather than as a belt-and-braces extra.
func fullWeatherData() weather.WeatherData {
	return weather.WeatherData{
		AirDensity:                      1.204521,
		AirTemperature:                  18,
		Brightness:                      41234,
		Conditions:                      "Clear",
		DeltaT:                          3,
		DewPoint:                        11,
		FeelsLike:                       19,
		Icon:                            "clear-day",
		IsPrecipLocalDayRainCheck:       true,
		IsPrecipLocalYesterdayRainCheck: false,
		LightningStrikeCountLast1hr:     2,
		LightningStrikeCountLast3hr:     7,
		LightningStrikeLastDistance:     13,
		LightningStrikeLastDistanceMsg:  "13 km away",
		LightningStrikeLastEpoch:        1776441600,
		PrecipAccumLocalDay:             3,
		PrecipAccumLocalYesterday:       5,
		PrecipMinutesLocalDay:           17,
		PrecipMinutesLocalYesterday:     41,
		PrecipProbability:               22,
		PressureTrend:                   "steady",
		RelativeHumidity:                63,
		SeaLevelPressure:                1013,
		SolarRadiation:                  312,
		StationPressure:                 1009.874321,
		Time:                            1776441600,
		UV:                              4,
		WetBulbGlobeTemperature:         16,
		WetBulbTemperature:              14,
		WindAvg:                         6,
		WindDirection:                   214,
		WindDirectionCardinal:           "SW",
		WindGust:                        11,
	}
}

func TestInsertReturnsTrueForAFreshRow(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs := postgres.NewRecordStore(pool)
	ctx := context.Background()
	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

	inserted, err := rs.Insert(ctx, store.NewRecord{
		ID: "rec-1", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !inserted {
		t.Fatal("Insert = false for a fresh row, want true")
	}

	var status, id string
	var attempts int32
	err = pool.QueryRow(ctx,
		"SELECT id, status, attempts FROM weather_records WHERE id = $1", "rec-1").
		Scan(&id, &status, &attempts)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if status != string(store.StatusPending) {
		t.Errorf("status = %q, want %q", status, string(store.StatusPending))
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0", attempts)
	}
}

func TestInsertDedupeIsFalseAndNotAnError(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs := postgres.NewRecordStore(pool)
	ctx := context.Background()
	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

	if _, err := rs.Insert(ctx, store.NewRecord{
		ID: "rec-1", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
	}); err != nil {
		t.Fatalf("first Insert: %v", err)
	}

	// A station re-reporting the same observation_time is the intended steady
	// state, so it must NOT be an error — only inserted=false.
	inserted, err := rs.Insert(ctx, store.NewRecord{
		ID: "rec-2", StationID: 1000, Timestamp: obs.Add(time.Second), ObservationTime: obs, Data: fullWeatherData(),
	})
	if err != nil {
		t.Fatalf("duplicate Insert error = %v, want nil", err)
	}
	if inserted {
		t.Fatal("duplicate Insert = true, want false")
	}

	var rows int
	if countErr := pool.QueryRow(ctx, "SELECT count(*) FROM weather_records").Scan(&rows); countErr != nil {
		t.Fatalf("counting: %v", countErr)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}

	// A different observation_time on the same station IS a new row.
	inserted, err = rs.Insert(ctx, store.NewRecord{
		ID: "rec-3", StationID: 1000, Timestamp: obs, ObservationTime: obs.Add(time.Minute), Data: fullWeatherData(),
	})
	if err != nil || !inserted {
		t.Fatalf("Insert with a new observation_time = (%v, %v), want (true, nil)", inserted, err)
	}
}

func TestInsertPrimaryKeyCollisionIsErrConflict(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs := postgres.NewRecordStore(pool)
	ctx := context.Background()
	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

	if _, err := rs.Insert(ctx, store.NewRecord{
		ID: "same-id", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
	}); err != nil {
		t.Fatalf("first Insert: %v", err)
	}

	// Same id, DIFFERENT dedupe key: the ON CONFLICT arbiter does not cover the
	// primary key, so this is a genuine surprise and must surface as a conflict.
	_, err := rs.Insert(ctx, store.NewRecord{
		ID: "same-id", StationID: 2000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Insert error = %v, want store.ErrConflict", err)
	}
}

func TestClassifiedErrorsLeakNothing(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs := postgres.NewRecordStore(pool)
	ctx := context.Background()
	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

	if _, err := rs.Insert(ctx, store.NewRecord{
		ID: "leak", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
	}); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	_, err := rs.Insert(ctx, store.NewRecord{
		ID: "leak", StationID: 2000, Timestamp: obs, ObservationTime: obs, Data: fullWeatherData(),
	})
	if err == nil {
		t.Fatal("expected a conflict")
	}

	// The message may name the SQLSTATE and nothing else. A *pgconn.PgError's
	// own Error() plus its Detail/Hint/ConstraintName/ColumnName/TableName
	// fields disclose schema and sometimes column values, and the API layer must
	// never be in a position where echoing an error is dangerous.
	msg := err.Error()
	forbidden := []string{
		"INSERT", "insert", "SELECT", "select", "weather_records",
		"ux_records_station_obs", "pkey", "duplicate key", "Key (", "DETAIL", "HINT",
	}
	for _, bad := range forbidden {
		if strings.Contains(msg, bad) {
			t.Errorf("classified error %q contains %q", msg, bad)
		}
	}
	if !strings.Contains(msg, "23505") {
		t.Errorf("classified error %q does not name its SQLSTATE", msg)
	}
}

// TestClassifierRejectsAnAlreadyCanceledContext and
// TestClassifierRejectsAnExpiredDeadline were removed at Task 17.
//
// Both drove classify's context.Canceled / context.DeadlineExceeded branch
// through a real Insert call, end to end, with an already-canceled /
// already-expired context, asserting on the FULL error text. Task 7's own
// mutation testing had already established that neither can discriminate
// the mutation it appears to guard: measured against the real driver, a
// context that is already canceled/expired before Insert is even called
// reaches pgx synchronously and comes back as the bare sentinel with nothing
// attached, so a classify mutation that forwards the raw error unclassified
// left both tests passing (confirmed by deliberately introducing that exact
// mutation in a scratch copy). The mutation they could not catch — classify
// silently forwarding driver/connection detail on a canceled or expired
// context, e.g. from a pgxpool acquire/connect race that produces a
// *pgconn.ConnectError wrapping `failed to connect to `user=...
// database=...`: ...` — is covered deterministically instead by
// TestClassifyStripsDetailFromACanceledContextError and
// TestClassifyStripsDetailFromAnExpiredDeadlineError in
// errors_internal_test.go, which call classify directly with a fabricated
// error of that exact shape; reproducing that race live is not possible,
// per Task 7's finding that puddle races the connection constructor's error
// against ctx.Done() over the same expiring context, a genuine coin flip.
//
// At Task 17, TestClassifierRejectsAnAlreadyCanceledContext flaked once
// under system load and could not be reproduced (30 sequential runs clean,
// plus 10 runs of the whole TestClassifier group under -race). The most
// plausible cause is exactly the race described above taking its other
// branch under contention; since that race is inherent to puddle and not
// something a test can force deterministically, there was no way to
// "harden" this test without either loosening its full-text assertion (the
// same anti-pattern this whole security gate exists to rule out) or
// accepting an occasional, unexplained red build in a security-critical
// suite. Given the two black-box tests already added nothing beyond what
// the deterministic white-box tests above prove, and its exact twin
// (TestClassifierRejectsAnExpiredDeadline) carries the identical
// non-discriminating property and the identical flake mechanism even though
// it happened not to flake yet, both were deleted rather than one kept as a
// known-flaky redundant test. The end-to-end, real-driver property that
// remains worth proving — that a canceled or expired context reaching the
// store through the public API surfaces the correct sentinel with nothing
// else attached — is covered by TestNoDriverErrorEscapesTheStore in
// security_test.go (via RecordStore.List and RecordStore.Get rather than
// Insert; classify is shared code, so which method exercises it is
// immaterial to what is being proven).

func TestJSONBRoundTripsAllThirtyThreeFields(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	rs := postgres.NewRecordStore(pool)
	ctx := context.Background()
	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	want := fullWeatherData()

	if _, err := rs.Insert(ctx, store.NewRecord{
		ID: "rt", StationID: 1000, Timestamp: obs, ObservationTime: obs, Data: want,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var got weather.WeatherData
	if err := pool.QueryRow(ctx, "SELECT data FROM weather_records WHERE id = $1", "rt").Scan(&got); err != nil {
		t.Fatalf("scanning data: %v", err)
	}
	// WeatherData is 33 comparable scalars, so == is a total comparison.
	if got != want {
		t.Fatalf("jsonb round trip changed the record:\n got %+v\nwant %+v", got, want)
	}

	// And the stored document has exactly 33 keys, which is what
	// Stats.TotalDataPoints multiplies by.
	var keys int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM jsonb_object_keys((SELECT data FROM weather_records WHERE id = $1))", "rt").
		Scan(&keys); err != nil {
		t.Fatalf("counting keys: %v", err)
	}
	if keys != weather.DataFieldsPerRecord {
		t.Fatalf("stored jsonb has %d keys, want %d", keys, weather.DataFieldsPerRecord)
	}
}
