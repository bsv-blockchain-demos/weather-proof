package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

var _ func(context.Context, string, []store.Publication) (store.Stats, error) = (*postgres.RecordStore)(nil).Complete

func TestCompleteMovesRecordsStatsAndStationsTogether(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, name, location, is_active) VALUES (1000, 'A', 'Bristol', true)"); err != nil {
		t.Fatalf("seeding station: %v", err)
	}
	seedPending(t, pool, 3, 1000)

	claimed, err := rs.ClaimPending(ctx, 3, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d rows, want 3", len(claimed))
	}

	pubs := make([]store.Publication, 0, len(claimed))
	for i, r := range claimed {
		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
	}

	stats, err := rs.Complete(ctx, "a1b2c3", pubs)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// ONE transaction per CreateAction, not one per record.
	if stats.TotalTx != 1 {
		t.Errorf("TotalTx = %d, want 1 (one per action, never per record)", stats.TotalTx)
	}
	if stats.TotalRecords != 3 {
		t.Errorf("TotalRecords = %d, want 3", stats.TotalRecords)
	}
	if stats.TotalDataPoints() != 99 {
		t.Errorf("TotalDataPoints = %d, want 99", stats.TotalDataPoints())
	}
	if stats.ActiveStations != 1 {
		t.Errorf("ActiveStations = %d, want 1", stats.ActiveStations)
	}
	if stats.LastRecordWrite == nil {
		t.Error("LastRecordWrite is nil after Complete")
	}

	// Every record row is fully published, which the
	// ck_records_completed_published constraint also enforces.
	rows, err := pool.Query(ctx, `
		SELECT status, txid, output_index, chain_status, processed_at IS NOT NULL,
		       claimed_at IS NULL, adopt_required, error IS NULL
		  FROM weather_records ORDER BY output_index ASC`)
	if err != nil {
		t.Fatalf("reading records: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var status, txid, chain string
		var vout int32
		var hasProcessed, leaseCleared, adopt, errCleared bool
		if scanErr := rows.Scan(&status, &txid, &vout, &chain, &hasProcessed, &leaseCleared, &adopt, &errCleared); scanErr != nil {
			t.Fatalf("scan: %v", scanErr)
		}
		if status != string(store.StatusCompleted) {
			t.Errorf("status = %q, want completed", status)
		}
		if txid != "a1b2c3" {
			t.Errorf("txid = %q, want a1b2c3", txid)
		}
		if chain != string(store.ChainARCAccepted) {
			t.Errorf("chain_status = %q, want arc-accepted", chain)
		}
		if int(vout) != seen {
			t.Errorf("output_index = %d, want %d", vout, seen)
		}
		if !hasProcessed || !leaseCleared || adopt || !errCleared {
			t.Errorf("row %d: processed=%v leaseCleared=%v adopt=%v errCleared=%v",
				seen, hasProcessed, leaseCleared, adopt, errCleared)
		}
		seen++
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatalf("rows: %v", rowsErr)
	}
	if seen != 3 {
		t.Fatalf("saw %d rows, want 3", seen)
	}

	// The station counters moved in the same transaction.
	var txRecords int64
	var lastTemp float64
	var lastConditions string
	var lastReading time.Time
	err = pool.QueryRow(ctx, `
		SELECT tx_records, last_temp, last_conditions, last_reading
		  FROM stations WHERE station_id = 1000`).
		Scan(&txRecords, &lastTemp, &lastConditions, &lastReading)
	if err != nil {
		t.Fatalf("reading station: %v", err)
	}
	if txRecords != 3 {
		t.Errorf("stations.tx_records = %d, want 3", txRecords)
	}
	// air_temperature is FieldInteger, so last_temp can only ever be integral —
	// a fractional example value such as 18.3 is unachievable by construction.
	if lastTemp != 18 {
		t.Errorf("stations.last_temp = %v, want 18", lastTemp)
	}
	if lastConditions != "Clear" {
		t.Errorf("stations.last_conditions = %q, want Clear", lastConditions)
	}
	if lastReading.IsZero() {
		t.Error("stations.last_reading was not set")
	}
}

func TestCompleteAcrossTwoStationsSplitsTheCounters(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	for _, id := range []int64{1000, 2000} {
		if _, err := pool.Exec(ctx,
			"INSERT INTO stations (station_id, is_active) VALUES ($1, true)", id); err != nil {
			t.Fatalf("seeding station %d: %v", id, err)
		}
	}
	seedPending(t, pool, 2, 1000)
	seedPending(t, pool, 3, 2000)

	claimed, err := rs.ClaimPending(ctx, 5, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	pubs := make([]store.Publication, 0, len(claimed))
	for i, r := range claimed {
		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
	}
	if _, completeErr := rs.Complete(ctx, "multi", pubs); completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}

	counts := map[int64]int64{}
	rows, err := pool.Query(ctx, "SELECT station_id, tx_records FROM stations ORDER BY station_id")
	if err != nil {
		t.Fatalf("reading stations: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int64
		if scanErr := rows.Scan(&id, &n); scanErr != nil {
			t.Fatalf("scan: %v", scanErr)
		}
		counts[id] = n
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatalf("rows: %v", rowsErr)
	}
	if counts[1000] != 2 || counts[2000] != 3 {
		t.Fatalf("tx_records = %v, want map[1000:2 2000:3]", counts)
	}

	// Still ONE transaction, even across two stations.
	var totalTx int64
	if statsErr := pool.QueryRow(ctx, "SELECT total_tx FROM app_stats WHERE id = 1").Scan(&totalTx); statsErr != nil {
		t.Fatalf("reading app_stats: %v", statsErr)
	}
	if totalTx != 1 {
		t.Fatalf("total_tx = %d, want 1", totalTx)
	}
}

func TestCompleteIsANoOpWhenNothingIsProcessing(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, is_active) VALUES (1000, true)"); err != nil {
		t.Fatalf("seeding station: %v", err)
	}
	seedPending(t, pool, 2, 1000)
	claimed, err := rs.ClaimPending(ctx, 2, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	pubs := make([]store.Publication, 0, len(claimed))
	for i, r := range claimed {
		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
	}

	first, err := rs.Complete(ctx, "once", pubs)
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if first.TotalTx != 1 || first.TotalRecords != 2 {
		t.Fatalf("first Complete stats = %+v, want TotalTx 1 TotalRecords 2", first)
	}

	// A retried Complete must not double-count: the rows are no longer
	// processing, so the UPDATE matches nothing.
	second, err := rs.Complete(ctx, "once", pubs)
	if err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if second.TotalTx != 1 || second.TotalRecords != 2 {
		t.Fatalf("second Complete stats = %+v, want the same TotalTx 1 TotalRecords 2", second)
	}

	var txRecords int64
	if stationErr := pool.QueryRow(ctx,
		"SELECT tx_records FROM stations WHERE station_id = 1000").Scan(&txRecords); stationErr != nil {
		t.Fatalf("reading station: %v", stationErr)
	}
	if txRecords != 2 {
		t.Fatalf("stations.tx_records = %d after a retried Complete, want 2", txRecords)
	}

	// An empty publication list is also a no-op.
	empty, err := rs.Complete(ctx, "none", []store.Publication{})
	if err != nil {
		t.Fatalf("Complete with no publications: %v", err)
	}
	if empty.TotalTx != 1 {
		t.Fatalf("TotalTx = %d after an empty Complete, want 1", empty.TotalTx)
	}
}

// TestCompleteIsTolerantOfAMissingStation covers the case the SQL deliberately
// allows: a record can arrive before its station upsert has landed, so
// bumpStationsSQL matching nothing must NOT fail the publish. The station row
// is not created either — it is an UPDATE, and inventing a station here would
// put a row on the dashboard that the poller never reported.
func TestCompleteIsTolerantOfAMissingStation(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	seedPending(t, pool, 1, 4242)
	claimed, err := rs.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	pubs := []store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}
	if _, completeErr := rs.Complete(ctx, "orphan", pubs); completeErr != nil {
		t.Fatalf("Complete with no stations row: %v", completeErr)
	}

	var status string
	if readErr := pool.QueryRow(ctx,
		"SELECT status FROM weather_records WHERE id = $1", claimed[0].ID).Scan(&status); readErr != nil {
		t.Fatalf("reading record: %v", readErr)
	}
	if status != string(store.StatusCompleted) {
		t.Fatalf("status = %q, want completed", status)
	}

	var stations int
	if countErr := pool.QueryRow(ctx,
		"SELECT count(*) FROM stations WHERE station_id = 4242").Scan(&stations); countErr != nil {
		t.Fatalf("counting stations: %v", countErr)
	}
	if stations != 0 {
		t.Fatalf("stations rows for 4242 = %d, want 0: Complete must not invent a station", stations)
	}

	// Completing an id that does not exist at all is also a no-op rather than an
	// error, and must not move total_tx a second time.
	ghost, err := rs.Complete(ctx, "ghost", []store.Publication{{RecordID: "does-not-exist", OutputIndex: 0}})
	if err != nil {
		t.Fatalf("Complete for a missing id: %v", err)
	}
	if ghost.TotalTx != 1 {
		t.Fatalf("TotalTx = %d after completing a missing id, want 1", ghost.TotalTx)
	}
}

// TestCompleteRollsBackWhollyOnFailure is the atomicity assertion, and it needs
// a REAL failure to make.
//
// An earlier draft of this test conceded in a comment that it could not produce
// one (txid is unconstrained text, so no argument is invalid) and asserted the
// two no-op paths instead — which TestCompleteIsANoOpWhenNothingIsProcessing
// already covers, leaving the central claim of Complete untested behind a name
// that said otherwise. A temporary CHECK constraint on the station counter is
// the missing lever: the record UPDATE and the app_stats bump succeed, the
// station bump then violates the constraint, and the whole transaction must
// unwind. Without pgx.BeginFunc's rollback, weather_records would be left
// 'completed' with total_tx at 0 and no way to notice.
func TestCompleteRollsBackWhollyOnFailure(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, is_active) VALUES (1000, true)"); err != nil {
		t.Fatalf("seeding station: %v", err)
	}
	seedPending(t, pool, 3, 1000)
	claimed, err := rs.ClaimPending(ctx, 3, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d rows, want 3", len(claimed))
	}
	pubs := make([]store.Publication, 0, len(claimed))
	for i, r := range claimed {
		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
	}

	// Three records land on one station, so the bump sets tx_records = 3 and
	// this rejects it with 23514.
	if _, addErr := pool.Exec(ctx,
		"ALTER TABLE stations ADD CONSTRAINT ck_probe CHECK (tx_records < 3)"); addErr != nil {
		t.Fatalf("adding the probe constraint: %v", addErr)
	}

	_, completeErr := rs.Complete(ctx, "wedged", pubs)
	if completeErr == nil {
		t.Fatal("Complete succeeded while the station bump was constrained; " +
			"the failure this test needs did not happen")
	}
	// classify maps a check violation to the opaque bucket, so the constraint
	// NAME must not travel with it.
	if !errors.Is(completeErr, postgres.ErrOperation) {
		t.Errorf("Complete error = %v, want postgres.ErrOperation", completeErr)
	}
	if strings.Contains(completeErr.Error(), "ck_probe") {
		t.Errorf("Complete error %q names the constraint", completeErr.Error())
	}

	// NOTHING may have committed: not the records, not app_stats, not the
	// station counter.
	var processing, completed int
	if countErr := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'processing'),
		       count(*) FILTER (WHERE status = 'completed')
		  FROM weather_records`).Scan(&processing, &completed); countErr != nil {
		t.Fatalf("counting records: %v", countErr)
	}
	if processing != 3 || completed != 0 {
		t.Fatalf("after the rollback: processing = %d, completed = %d, want 3 and 0",
			processing, completed)
	}
	var totalTx, totalRecords, txRecords int64
	if statsErr := pool.QueryRow(ctx,
		"SELECT total_tx, total_records FROM app_stats WHERE id = 1").Scan(&totalTx, &totalRecords); statsErr != nil {
		t.Fatalf("reading app_stats: %v", statsErr)
	}
	if totalTx != 0 || totalRecords != 0 {
		t.Fatalf("app_stats moved despite the rollback: total_tx = %d, total_records = %d",
			totalTx, totalRecords)
	}
	if stationErr := pool.QueryRow(ctx,
		"SELECT tx_records FROM stations WHERE station_id = 1000").Scan(&txRecords); stationErr != nil {
		t.Fatalf("reading station: %v", stationErr)
	}
	if txRecords != 0 {
		t.Fatalf("stations.tx_records = %d despite the rollback, want 0", txRecords)
	}

	// And with the constraint gone the identical call succeeds, which proves the
	// rollback left the batch in a retryable state rather than a stuck one.
	if _, dropErr := pool.Exec(ctx,
		"ALTER TABLE stations DROP CONSTRAINT ck_probe"); dropErr != nil {
		t.Fatalf("dropping the probe constraint: %v", dropErr)
	}
	stats, err := rs.Complete(ctx, "wedged", pubs)
	if err != nil {
		t.Fatalf("retried Complete: %v", err)
	}
	if stats.TotalTx != 1 || stats.TotalRecords != 3 {
		t.Fatalf("retried Complete stats = %+v, want TotalTx 1 TotalRecords 3", stats)
	}
}

// TestCompleteStationBumpIgnoresAnOlderReading is one half of the fixture
// mutation testing demanded: a fresh station (last_reading IS NULL) can never
// discriminate bumpStationsSQL's "newer reading only" CASE guard from an
// unconditional assignment, because the IS NULL branch fires either way. This
// test starts the station with a last_reading that is ALREADY NEWER than the
// batch being completed, and with last_temp/last_conditions values that are
// deliberately different from what the (older) batch would write — so an
// assignment that fires when it should not is directly observable, not merely
// "some write happened."
//
// last_reading and tx_records are asserted too, on the SQL's own terms: the
// counter is unconditional (it must still move), and last_reading's
// greatest(...) must still hold the EXISTING, newer value rather than being
// dragged backward by an older batch — that expression has the identical
// fresh-station vacuity as the temp/conditions guard, and this fixture closes
// it as the same byproduct.
func TestCompleteStationBumpIgnoresAnOlderReading(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	existingReading := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	olderBatchReading := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	if _, err := pool.Exec(ctx, `
		INSERT INTO stations (station_id, is_active, tx_records, last_reading, last_temp, last_conditions)
		VALUES (1000, true, 5, $1, 99, 'PreExisting')`, existingReading); err != nil {
		t.Fatalf("seeding station: %v", err)
	}

	id := uuid.Must(uuid.NewV7()).String()
	data := fullWeatherData()
	data.AirTemperature = 5
	data.Conditions = "OlderCondition"
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
		VALUES ($1, 1000, $2, $2, $3)`, id, olderBatchReading, data); err != nil {
		t.Fatalf("seeding record: %v", err)
	}

	claimed, err := rs.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d rows, want 1", len(claimed))
	}
	pubs := []store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}
	if _, completeErr := rs.Complete(ctx, "older-batch", pubs); completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}

	var txRecords int64
	var lastTemp float64
	var lastConditions string
	var lastReading time.Time
	if readErr := pool.QueryRow(ctx, `
		SELECT tx_records, last_temp, last_conditions, last_reading
		  FROM stations WHERE station_id = 1000`).
		Scan(&txRecords, &lastTemp, &lastConditions, &lastReading); readErr != nil {
		t.Fatalf("reading station: %v", readErr)
	}

	// The counter is unconditional and must still move.
	if txRecords != 6 {
		t.Errorf("tx_records = %d, want 6 (5 pre-existing + 1)", txRecords)
	}
	// The batch is OLDER than what the station already has, so none of these
	// three may change. A guard stuck "always assign" would report 5 and
	// "OlderCondition" here; a last_reading expression that dropped
	// greatest(...) would report olderBatchReading here.
	if lastTemp != 99 {
		t.Errorf("last_temp = %v, want 99 (unchanged: the batch is older)", lastTemp)
	}
	if lastConditions != "PreExisting" {
		t.Errorf("last_conditions = %q, want PreExisting (unchanged: the batch is older)", lastConditions)
	}
	if !lastReading.Equal(existingReading) {
		t.Errorf("last_reading = %v, want the untouched existing %v", lastReading, existingReading)
	}
}

// TestCompleteStationBumpAdoptsANewerReading is the mirror of
// TestCompleteStationBumpIgnoresAnOlderReading: without it, a guard that got
// stuck NEVER firing (e.g. an inverted comparison) would pass every other test
// in this file, since it too would leave last_temp/last_conditions unchanged —
// which happens to be what several other fixtures already expect from a fresh
// station. This is the direction that requires the fields to actually move.
func TestCompleteStationBumpAdoptsANewerReading(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	existingReading := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	newerBatchReading := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if _, err := pool.Exec(ctx, `
		INSERT INTO stations (station_id, is_active, tx_records, last_reading, last_temp, last_conditions)
		VALUES (1000, true, 3, $1, 1, 'Stale')`, existingReading); err != nil {
		t.Fatalf("seeding station: %v", err)
	}

	id := uuid.Must(uuid.NewV7()).String()
	data := fullWeatherData()
	data.AirTemperature = 77
	data.Conditions = "Fresh"
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
		VALUES ($1, 1000, $2, $2, $3)`, id, newerBatchReading, data); err != nil {
		t.Fatalf("seeding record: %v", err)
	}

	claimed, err := rs.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d rows, want 1", len(claimed))
	}
	pubs := []store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}
	if _, completeErr := rs.Complete(ctx, "newer-batch", pubs); completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}

	var txRecords int64
	var lastTemp float64
	var lastConditions string
	var lastReading time.Time
	if readErr := pool.QueryRow(ctx, `
		SELECT tx_records, last_temp, last_conditions, last_reading
		  FROM stations WHERE station_id = 1000`).
		Scan(&txRecords, &lastTemp, &lastConditions, &lastReading); readErr != nil {
		t.Fatalf("reading station: %v", readErr)
	}

	if txRecords != 4 {
		t.Errorf("tx_records = %d, want 4 (3 pre-existing + 1)", txRecords)
	}
	// The batch is NEWER than what the station already has, so all three must
	// move to the batch's values.
	if lastTemp != 77 {
		t.Errorf("last_temp = %v, want 77 (the batch is newer and must win)", lastTemp)
	}
	if lastConditions != "Fresh" {
		t.Errorf("last_conditions = %q, want Fresh (the batch is newer and must win)", lastConditions)
	}
	if !lastReading.Equal(newerBatchReading) {
		t.Errorf("last_reading = %v, want the newer batch reading %v", lastReading, newerBatchReading)
	}
}
