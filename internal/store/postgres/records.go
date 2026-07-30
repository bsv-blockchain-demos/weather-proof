package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// recordColumns is the explicit column list of weather_records, in the exact
// order and with the exact names store.Record's db tags declare.
//
// It is spelled out because SELECT * and RETURNING * are forbidden here:
// pgx.RowToStructByName treats a column with no matching struct field as a
// hard RUNTIME error, and RowToStructByNameLax was verified to behave
// identically — Lax only relaxes struct fields with no matching column, never
// the reverse. A star select would therefore couple every query in this
// package to the table's full column list forever, and the next migration
// would break all of them at runtime with no compile-time signal and no test
// coverage unless the test database already had the new column.
const recordColumns = `id, station_id, timestamp, observation_time, data, status, attempts,
       claim_ref, adopt_required, claimed_at, txid, output_index, block_height,
       chain_status, mined_at, error, created_at, processed_at`

// recordColumnsAliased is recordColumns qualified with the r alias, for the
// RETURNING clause of an `UPDATE weather_records AS r`.
const recordColumnsAliased = `r.id, r.station_id, r.timestamp, r.observation_time, r.data,
       r.status, r.attempts, r.claim_ref, r.adopt_required, r.claimed_at, r.txid,
       r.output_index, r.block_height, r.chain_status, r.mined_at, r.error,
       r.created_at, r.processed_at`

// insertRecordSQL is the poller's write.
//
// ON CONFLICT DO NOTHING against ux_records_station_obs is the structural
// duplicate guard: Mongo had no unique index anywhere, so nothing prevented
// duplicate rows before. A collision reports zero rows affected, which Insert
// turns into inserted=false with a nil error.
const insertRecordSQL = `
INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (station_id, observation_time) DO NOTHING`

// RecordStore is the pgx/v5 implementation of store.RecordStore.
type RecordStore struct {
	db *pgxpool.Pool
}

// NewRecordStore returns a RecordStore backed by db.
func NewRecordStore(db *pgxpool.Pool) *RecordStore {
	return &RecordStore{db: db}
}

// Insert implements store.RecordStore.
//
// The Data field is passed as a plain weather.WeatherData value: pgtype's JSONB
// codec has an encode plan for a struct (it marshals it) and a scan plan for a
// struct pointer (it unmarshals), so no pgtype wrapper and no []byte hop is
// needed in either direction.
func (s *RecordStore) Insert(ctx context.Context, r store.NewRecord) (bool, error) {
	ct, err := s.db.Exec(ctx, insertRecordSQL,
		r.ID, r.StationID, r.Timestamp, r.ObservationTime, r.Data)
	if err != nil {
		return false, classify(err)
	}
	return ct.RowsAffected() == 1, nil
}
