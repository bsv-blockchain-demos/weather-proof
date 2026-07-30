package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

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
