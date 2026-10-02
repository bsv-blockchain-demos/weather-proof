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

// TestSnapshotCountsEveryBucket gives every one of the six buckets a
// DIFFERENT row count (1 through 6), so a mis-wired Scan destination or a
// mis-targeted FILTER predicate is visible rather than hidden behind two
// buckets that happen to agree. An earlier fixture gave PendingRows,
// ProcessingRows, MinedCount and AbortedCount one row each and FailedRows and
// StillUnminedOlderThan1h two rows each; mutation testing confirmed that a
// Scan-destination swap within either group passed the full suite silently.
//
// ONE documented dependency, not a coincidence: every "aborted" row is ALSO a
// status='failed' row, because a chain-aborted transaction is stored with
// status='failed' by construction (see eea21d8a: aborted is a chain_status,
// not a fifth Status, and shadowing it with 'failed' is deliberate). So
// FailedRows (6) = the 2 plain 'failed' rows below + the 4 'aborted' rows
// below, and AbortedCount's 4 rows are necessarily a SUBSET of FailedRows's
// count — FailedRows is bound to be >= AbortedCount, never an independent
// draw. That is why the two are 6 and 4 rather than, say, 6 and 2.
func TestSnapshotCountsEveryBucket(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	fixtures := []struct {
		id   string
		stmt string
	}{
		// PendingRows = 1.
		{"pending-1", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
			VALUES ('pending-1', 101, now(), now(), $1)`},

		// ProcessingRows = 2.
		{"processing-1", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, claimed_at)
			VALUES ('processing-1', 102, now(), now(), $1, 'processing', now())`},
		{"processing-2", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, claimed_at)
			VALUES ('processing-2', 103, now(), now(), $1, 'processing', now())`},

		// Two PLAIN failed rows (chain_status NULL): part of FailedRows = 6.
		{"failed-1", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
			VALUES ('failed-1', 104, now(), now(), $1, 'failed')`},
		{"failed-2", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
			VALUES ('failed-2', 105, now(), now(), $1, 'failed')`},

		// AbortedCount = 4. Each row is ALSO one of FailedRows's six, per the
		// dependency documented above.
		{"aborted-1", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, chain_status)
			VALUES ('aborted-1', 106, now(), now(), $1, 'failed', 'aborted')`},
		{"aborted-2", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, chain_status)
			VALUES ('aborted-2', 107, now(), now(), $1, 'failed', 'aborted')`},
		{"aborted-3", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, chain_status)
			VALUES ('aborted-3', 108, now(), now(), $1, 'failed', 'aborted')`},
		{"aborted-4", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, chain_status)
			VALUES ('aborted-4', 109, now(), now(), $1, 'failed', 'aborted')`},

		// MinedCount = 3. Old and completed, so these would ALSO satisfy
		// StillUnminedOlderThan1h's age and status predicates if its
		// chain_status <> 'mined' exclusion did not rule them out — the same
		// exclusion the previous fixture tested with one row, now with three.
		{"mined-1", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status, block_height)
			VALUES ('mined-1', 110, now(), now(), $1, 'completed', 'tx-mined-1', 0,
			        now() - interval '3 hours', 'mined', 900001)`},
		{"mined-2", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status, block_height)
			VALUES ('mined-2', 111, now(), now(), $1, 'completed', 'tx-mined-2', 0,
			        now() - interval '3 hours', 'mined', 900002)`},
		{"mined-3", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status, block_height)
			VALUES ('mined-3', 112, now(), now(), $1, 'completed', 'tx-mined-3', 0,
			        now() - interval '3 hours', 'mined', 900003)`},

		// StillUnminedOlderThan1h = 5: three rows exercise
		// chain_status = 'arc-accepted', two exercise chain_status IS NULL,
		// covering both sides of the "IS NULL OR <> 'mined'" predicate.
		{"unmined-old-1", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
			VALUES ('unmined-old-1', 113, now(), now(), $1, 'completed', 'tx-um-1', 0,
			        now() - interval '2 hours', 'arc-accepted')`},
		{"unmined-old-2", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
			VALUES ('unmined-old-2', 114, now(), now(), $1, 'completed', 'tx-um-2', 0,
			        now() - interval '2 hours', 'arc-accepted')`},
		{"unmined-old-3", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
			VALUES ('unmined-old-3', 115, now(), now(), $1, 'completed', 'tx-um-3', 0,
			        now() - interval '2 hours', 'arc-accepted')`},
		{"unmined-old-4", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
			VALUES ('unmined-old-4', 116, now(), now(), $1, 'completed', 'tx-um-4', 0,
			        now() - interval '5 hours')`},
		{"unmined-old-5", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
			VALUES ('unmined-old-5', 117, now(), now(), $1, 'completed', 'tx-um-5', 0,
			        now() - interval '5 hours')`},

		// Excluded: completed and chain_status = 'arc-accepted' just like the
		// unmined-old rows above, but processed WITHIN the last hour — the
		// freshness predicate's own negative case, must NOT be counted
		// anywhere.
		{"unmined-fresh", `INSERT INTO weather_records
			(id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at, chain_status)
			VALUES ('unmined-fresh', 118, now(), now(), $1, 'completed', 'tx-fresh', 0, now(), 'arc-accepted')`},
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
	if snap.ProcessingRows != 2 {
		t.Errorf("ProcessingRows = %d, want 2", snap.ProcessingRows)
	}
	if snap.FailedRows != 6 {
		t.Errorf("FailedRows = %d, want 6 (2 plain failed plus the 4 aborted rows)", snap.FailedRows)
	}
	// The five unmined-old rows qualify; unmined-fresh is inside the hour and
	// the three mined rows are excluded by chain_status.
	if snap.StillUnminedOlderThan1h != 5 {
		t.Errorf("StillUnminedOlderThan1h = %d, want 5", snap.StillUnminedOlderThan1h)
	}
	if snap.MinedCount != 3 {
		t.Errorf("MinedCount = %d, want 3", snap.MinedCount)
	}
	if snap.AbortedCount != 4 {
		t.Errorf("AbortedCount = %d, want 4", snap.AbortedCount)
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
