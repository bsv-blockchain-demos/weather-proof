package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

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

// claimSQL is the atomic claim.
//
// WHAT THIS IS, AND WHAT IT IS NOT. It is a mandatory REPLACEMENT for a
// guarantee the system used to get by accident, not an optimization. In the
// TypeScript the claim was never atomic: it read pending ids, then wrote them
// with a filter of {_id: id} rather than {_id: id, status: 'pending'}, so any
// overlapping process claimed the same rows. Duplicate publishing was
// prevented only because both processes then spent from the same funding
// basket and the loser's createAction failed as a double spend — funding-UTXO
// contention was doing unintended duty as the only cross-process serializer of
// RECORD processing. Under the new design the app passes no inputs and no
// input BEEF, so it selects no funding UTXOs: two overlapping processes would
// each be funded from DIFFERENT fuel outputs and BOTH would succeed, putting
// the same readings on chain twice, paying for both, and orphaning whichever
// transaction did not land last. Measured on this schema, against this
// package's own TestClaimPendingNeverDoubleClaims fixture (40 seeded rows,
// 12 workers, 7-row batches): the SELECT-then-UPDATE shape double-claimed 20
// of 20 distinct rows touched in one run, one row taken 8 times (an
// independent rerun measured 16-21 of 16-21 touched, worst 6-9 — the exact
// counts vary by scheduling; the shape, total double allocation, does not);
// this shape double-claimed 0, confirmed across repeated -race runs.
//
// FOR UPDATE SKIP LOCKED must be in the SUBQUERY — it is not legal on the
// outer UPDATE. The inner SELECT takes a row-level exclusive lock on each
// candidate inside the SAME statement as the UPDATE, so there is no
// read-modify-write window at all. SKIP LOCKED rather than plain FOR UPDATE
// matters operationally: plain FOR UPDATE would serialize correctly but block
// for the holder's whole transaction, which next to a 60-second publish is
// latency-fatal.
//
// SCOPE LIMIT, stated because the natural reading is more generous than the
// truth: the row lock lives only for the claim transaction. Durable exclusion
// afterwards is the status column. This closes the concurrent-claim window and
// NOTHING else — it does not make publishing idempotent across a crash between
// the action committing server-side and Complete landing. That window is the
// adopt path's job, and neither substitutes for the other.
//
// attempts is deliberately untouched: the error classifier has sole ownership
// of the attempt budget. ORDER BY c.id ASC is not decoration — created_at
// defaults to now(), which is transaction_timestamp(), so every row a poll
// wrote shares one created_at and the tiebreaker is what gives the FIFO queue
// a total order. uuidv7 is what makes id a MEANINGFUL tiebreaker rather than
// an arbitrary one: it sorts in generation order.
const claimSQL = `
UPDATE weather_records AS r
   SET status     = 'processing',
       claimed_at = now(),
       claim_ref  = COALESCE(r.claim_ref, $2)
 WHERE r.id IN (
         SELECT c.id
           FROM weather_records AS c
          WHERE c.status = 'pending'
          ORDER BY c.created_at ASC, c.id ASC
          LIMIT $1
            FOR UPDATE SKIP LOCKED
       )
RETURNING ` + recordColumnsAliased

// ClaimPending implements store.RecordStore.
//
// ref is a parameter rather than a gen_random_uuid() call inside the SQL
// because gen_random_uuid() is VOLATILE: Postgres evaluates it once per
// updated row, so a 21-row claim would stamp 21 different refs and the batch
// label the adopt design uses as its idempotency key would not exist.
// Measured: the inline form produced 21 distinct refs for 21 rows; this form
// produces exactly 1.
//
// n <= 0 is clamped HERE, in Go, before claimSQL is ever built or sent —
// never left to Postgres's own LIMIT to decide. LIMIT 0 already returns zero
// rows with no error, but a negative LIMIT raises a runtime error (SQLSTATE
// 2201W, classified below to ErrOperation), and interfaces.go's ClaimPending
// doc states one rule for every implementation: zero rows, nil error, never
// unbounded. Leaving that to the driver would make a caller's behavior
// depend on which implementation is wired in, which is exactly what a frozen
// interface exists to prevent. []store.Record{} rather than nil matches what
// pgx.CollectRows itself returns for a genuine zero-row result — CollectRows
// is AppendRows([]T{}, ...), so a real "nothing pending" result is also a
// non-nil empty slice — meaning a caller cannot distinguish "clamped" from
// "nothing was pending" by nil-ness either way.
func (s *RecordStore) ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]store.Record, error) {
	if n <= 0 {
		return []store.Record{}, nil
	}
	rows, err := s.db.Query(ctx, claimSQL, n, ref)
	if err != nil {
		return nil, classify(err)
	}
	return collectRecords(rows)
}

// collectRecords drains rows into []store.Record.
//
// pgx.CollectRows closes rows and returns rows.Err() itself, so there is no
// separate Close or Err to forget. RowToStructByName matches each returned
// column to a struct field by NAME (its db tag), not by position, so the
// column list's order is irrelevant here. What matters is that every query in
// this package names its columns explicitly rather than SELECT * / RETURNING
// *: RowToStructByName treats an unmatched column as a hard runtime error, so
// a star projection would compile fine and then break the next time the
// table's columns change, with no compile-time signal.
func collectRecords(rows pgx.Rows) ([]store.Record, error) {
	recs, err := pgx.CollectRows(rows, pgx.RowToStructByName[store.Record])
	if err != nil {
		return nil, classify(err)
	}
	return recs, nil
}
