package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

var (
	_ func(context.Context) (store.Stats, error)    = (*postgres.StationStore)(nil).Stats
	_ func(context.Context) (store.Snapshot, error) = (*postgres.RecordStore)(nil).Snapshot
)

// TestStatsActiveStationsIsALiveCount is the regression gate on one of the two
// stats bugs this design fixes: activeStations was a STORED counter that could
// get stuck at 0. It is now `SELECT count(*) FROM stations WHERE is_active`
// (~20 rows, free), so being stuck is structurally impossible.
func TestStatsActiveStationsIsALiveCount(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	stats, err := ss.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActiveStations != 3 {
		t.Fatalf("ActiveStations = %d, want 3 (one of the four seeded stations is inactive)", stats.ActiveStations)
	}

	if _, execErr := pool.Exec(ctx,
		"UPDATE stations SET is_active = false WHERE station_id = 1000"); execErr != nil {
		t.Fatalf("deactivating: %v", execErr)
	}
	stats, err = ss.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActiveStations != 2 {
		t.Fatalf("ActiveStations = %d after a deactivation, want 2", stats.ActiveStations)
	}

	if _, allErr := pool.Exec(ctx, "UPDATE stations SET is_active = false"); allErr != nil {
		t.Fatalf("deactivating all: %v", allErr)
	}
	stats, err = ss.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActiveStations != 0 {
		t.Fatalf("ActiveStations = %d with everything inactive, want 0", stats.ActiveStations)
	}
}

func TestStatsOnAnEmptyDatabase(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ss := postgres.NewStationStore(pool)

	stats, err := ss.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActiveStations != 0 || stats.TotalTx != 0 || stats.TotalRecords != 0 {
		t.Fatalf("stats on an empty database = %+v, want all zero", stats)
	}
	if stats.LastRecordWrite != nil {
		t.Fatalf("LastRecordWrite = %v on an empty database, want nil (a zero time.Time "+
			"would marshal to 0001-01-01T00:00:00Z and render as 01/01/0001)", stats.LastRecordWrite)
	}
	if stats.TotalDataPoints() != 0 {
		t.Fatalf("TotalDataPoints = %d, want 0", stats.TotalDataPoints())
	}
}

func TestStatsAfterPublishing(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	rs := postgres.NewRecordStore(pool)
	ss := postgres.NewStationStore(pool)

	seedPending(t, pool, 4, 1000)
	claimed, err := rs.ClaimPending(ctx, 4, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	pubs := make([]store.Publication, 0, len(claimed))
	for i, r := range claimed {
		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
	}
	if _, completeErr := rs.Complete(ctx, "tx-one", pubs); completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}

	stats, err := ss.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalTx != 1 {
		t.Errorf("TotalTx = %d, want 1", stats.TotalTx)
	}
	if stats.TotalRecords != 4 {
		t.Errorf("TotalRecords = %d, want 4", stats.TotalRecords)
	}
	// The multiplier is weather.DataFieldsPerRecord and never a literal 33 in
	// SQL: one definition per module, or the two drift.
	if want := int64(4) * weather.DataFieldsPerRecord; stats.TotalDataPoints() != want {
		t.Errorf("TotalDataPoints = %d, want %d", stats.TotalDataPoints(), want)
	}
	if stats.LastRecordWrite == nil {
		t.Error("LastRecordWrite is nil after a publish")
	}
}

func TestSnapshotCountsEveryBucket(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	// One row per bucket, written directly so the state is unambiguous.
	fixtures := []struct {
		id   string
		stmt string
	}{
		{"pending", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
			VALUES ('pending', 1, now(), now(), $1)`},
		{"processing", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, claimed_at)
			VALUES ('processing', 2, now(), now(), $1, 'processing', now())`},
		{"failed", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
			VALUES ('failed', 3, now(), now(), $1, 'failed')`},
		{"unmined-old", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
			VALUES ('unmined-old', 4, now(), now(), $1, 'completed', 'tx-a', 0,
			        now() - interval '2 hours', 'arc-accepted')`},
		{"unmined-fresh", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
			VALUES ('unmined-fresh', 5, now(), now(), $1, 'completed', 'tx-b', 0, now(), 'arc-accepted')`},
		{"mined", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status, block_height)
			VALUES ('mined', 6, now(), now(), $1, 'completed', 'tx-c', 0,
			        now() - interval '3 hours', 'mined', 900001)`},
		{"aborted", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, chain_status)
			VALUES ('aborted', 7, now(), now(), $1, 'failed', 'aborted')`},
		{"null-chain-old", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
			VALUES ('null-chain-old', 8, now(), now(), $1, 'completed', 'tx-d', 0,
			        now() - interval '5 hours')`},
	}
	for _, f := range fixtures {
		if _, err := pool.Exec(ctx, f.stmt, fullWeatherData()); err != nil {
			t.Fatalf("seeding %s: %v", f.id, err)
		}
	}

	snap, err := rs.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.PendingRows != 1 {
		t.Errorf("PendingRows = %d, want 1", snap.PendingRows)
	}
	if snap.ProcessingRows != 1 {
		t.Errorf("ProcessingRows = %d, want 1", snap.ProcessingRows)
	}
	if snap.FailedRows != 2 {
		t.Errorf("FailedRows = %d, want 2 (failed plus aborted)", snap.FailedRows)
	}
	// unmined-old and null-chain-old qualify; unmined-fresh is inside the hour
	// and mined is excluded by chain_status.
	if snap.StillUnminedOlderThan1h != 2 {
		t.Errorf("StillUnminedOlderThan1h = %d, want 2", snap.StillUnminedOlderThan1h)
	}
	if snap.MinedCount != 1 {
		t.Errorf("MinedCount = %d, want 1", snap.MinedCount)
	}
	if snap.AbortedCount != 1 {
		t.Errorf("AbortedCount = %d, want 1", snap.AbortedCount)
	}
}

func TestSnapshotOnAnEmptyDatabaseIsAllZero(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	snap, err := rs.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap != (store.Snapshot{}) {
		t.Fatalf("snapshot on an empty database = %+v, want the zero value", snap)
	}
}
