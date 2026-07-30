package postgres

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
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

// completeRecordsSQL publishes one batch under one txid.
//
// The `AND r.status = 'processing'` predicate is what makes a retried Complete
// a no-op instead of a double count. The publication list arrives as two
// parallel arrays through unnest rather than as N statements, so the whole
// batch is one round trip. Two empty arrays are a clean no-op, which is what
// the retried and empty-batch paths rely on.
//
// `FROM unnest(...) AS u(cols)` and NOT
// `FROM (SELECT * FROM unnest(...) AS t(cols)) AS u`: the wrapping subquery
// would work, but it puts a literal `SELECT *` in the statement, which the
// no-star-selects guard in sqldiscipline_test.go rejects on sight — and that
// guard exists for a good reason, so the statement is written not to need an
// exemption.
const completeRecordsSQL = `
UPDATE weather_records AS r
   SET status = 'completed', txid = $1, output_index = u.output_index,
       chain_status = 'arc-accepted', processed_at = now(), error = NULL,
       claimed_at = NULL, adopt_required = false
  FROM unnest($2::text[], $3::int[]) AS u(record_id, output_index)
 WHERE r.id = u.record_id AND r.status = 'processing'
RETURNING r.station_id, r.timestamp, r.data`

// bumpAppStatsSQL increments the singleton. total_tx moves by ONE per action,
// never per record: counting per record is one of the two stats bugs this
// design fixes.
const bumpAppStatsSQL = `
UPDATE app_stats
   SET total_tx = total_tx + 1,
       total_records = total_records + $1,
       last_record_write = greatest(coalesce(last_record_write, now()), now()),
       updated_at = now()
 WHERE id = 1`

// bumpStationsSQL moves the per-station counters. A station with no row simply
// matches nothing, which is deliberate: a record can legitimately arrive
// before its station upsert has landed, and failing the publish for that would
// be worse than a missing counter. It is an UPDATE and never an upsert, so a
// station the poller has not reported never appears on the dashboard.
//
// last_temp and last_conditions move ONLY when this batch is newer, under the
// same predicate as last_reading. Assigning them unconditionally (which an
// earlier draft did) desynchronises them: a batch completing late with an OLDER
// reading would leave last_reading at the newer instant while overwriting the
// temperature with the older one, so the dashboard would show a stale
// temperature stamped with a fresh time and nothing would detect it. The fake
// has the same rule, and Task 19's conformance suite asserts it on both.
const bumpStationsSQL = `
UPDATE stations AS s
   SET tx_records = s.tx_records + u.n,
       last_reading = greatest(coalesce(s.last_reading, u.ts), u.ts),
       last_temp = CASE WHEN s.last_reading IS NULL OR u.ts > s.last_reading
                        THEN u.temp ELSE s.last_temp END,
       last_conditions = CASE WHEN s.last_reading IS NULL OR u.ts > s.last_reading
                              THEN u.conditions ELSE s.last_conditions END,
       updated_at = now()
  FROM unnest($1::bigint[], $2::bigint[], $3::timestamptz[],
              $4::double precision[], $5::text[])
         AS u(station_id, n, ts, temp, conditions)
 WHERE s.station_id = u.station_id`

// statsSQL is the four dashboard values.
//
// activeStations is a LIVE count rather than a stored counter (~20 rows, free),
// so it can never be stuck at 0 — which was the other of the two stats bugs.
// total_records is multiplied by weather.DataFieldsPerRecord in Go, never by a
// literal 33 in SQL: the constant already exists once in this module and a
// second copy is how the two drift.
const statsSQL = `
SELECT (SELECT count(*) FROM stations WHERE is_active) AS active_stations,
       total_tx, total_records, last_record_write
  FROM app_stats WHERE id = 1`

// rowQuerier is the single-row query surface shared by *pgxpool.Pool and
// pgx.Tx, so readStats can run either inside a transaction or standalone.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readStats reads the four dashboard values through q.
func readStats(ctx context.Context, q rowQuerier) (store.Stats, error) {
	var out store.Stats
	err := q.QueryRow(ctx, statsSQL).
		Scan(&out.ActiveStations, &out.TotalTx, &out.TotalRecords, &out.LastRecordWrite)
	if err != nil {
		return store.Stats{}, classify(err)
	}
	return out, nil
}

// stationDelta is the aggregate one Complete applies to one station.
type stationDelta struct {
	n          int64
	ts         time.Time
	temp       float64
	conditions string
}

// movedRecord is the projection completeRecordsSQL returns.
type movedRecord struct {
	stationID int64
	ts        time.Time
	data      weather.WeatherData
}

// Complete implements store.RecordStore.
//
// Records, app_stats and the station counters move in ONE transaction. The
// atomicity requirement transfers from the Mongo session the TypeScript used;
// the mechanism does not. pgx.BeginFunc commits on a nil return and rolls back
// otherwise, and it is a free function taking a structural interface, so
// *pgxpool.Pool satisfies it directly and there is no defer-rollback
// boilerplate to get wrong (and no shadowed err in a deferred closure, which
// govet's shadow check would reject).
//
// pubs is sorted by RecordID before it is sent, on top of what the design
// spelled out: Task 8's review measured that a writer touching a claimed batch
// in a DIFFERENT id order than the claim took them produces deadlocks even
// with SKIP LOCKED (184 vs a 35-deadlock bare-FOR-UPDATE baseline; 33 vs 35
// even with SKIP LOCKED against a differently-ordered concurrent writer), and
// Complete is exactly that writer. classify already buckets SQLSTATE 40P01 as
// transient, so a deadlock here is survivable, but sorting removes the
// lock-order cycle at the source rather than leaning on retry. The caller's
// slice is left untouched — pubs is copied before sorting — because
// interfaces.go makes no promise that Complete may reorder its argument.
func (s *RecordStore) Complete(ctx context.Context, txID string, pubs []store.Publication) (store.Stats, error) {
	var out store.Stats
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		sorted := make([]store.Publication, len(pubs))
		copy(sorted, pubs)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].RecordID < sorted[j].RecordID })

		ids := make([]string, 0, len(sorted))
		vouts := make([]int32, 0, len(sorted))
		for _, p := range sorted {
			ids = append(ids, p.RecordID)
			vouts = append(vouts, p.OutputIndex)
		}

		rows, qErr := tx.Query(ctx, completeRecordsSQL, txID, ids, vouts)
		if qErr != nil {
			return classify(qErr)
		}
		moved, cErr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (movedRecord, error) {
			var m movedRecord
			scanErr := row.Scan(&m.stationID, &m.ts, &m.data)
			return m, scanErr
		})
		if cErr != nil {
			return classify(cErr)
		}

		if len(moved) == 0 {
			// Nothing was in processing. A retried Complete, or one for ids
			// that no longer exist, must not touch a single counter.
			stats, sErr := readStats(ctx, tx)
			if sErr != nil {
				return sErr
			}
			out = stats
			return nil
		}

		deltas := make(map[int64]*stationDelta, len(moved))
		order := make([]int64, 0, len(moved))
		for _, m := range moved {
			d, ok := deltas[m.stationID]
			if !ok {
				d = &stationDelta{}
				deltas[m.stationID] = d
				order = append(order, m.stationID)
			}
			d.n++
			if m.ts.After(d.ts) {
				d.ts = m.ts
				// air_temperature is FieldInteger in the wire schema, so
				// last_temp can only ever hold an integral value. The column
				// stays double precision because the API contract needs a
				// *float64 that is never omitted.
				d.temp = float64(m.data.AirTemperature)
				d.conditions = m.data.Conditions
			}
		}

		// order (station ids in first-moved order) is sorted here too, for the
		// same lock-ordering reason pubs is sorted above: bumpStationsSQL is
		// another multi-row UPDATE via unnest, and two concurrent Complete
		// calls whose batches share a station can deadlock on it exactly like
		// completeRecordsSQL can on weather_records if they touch the shared
		// rows in different orders.
		sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

		stationIDs := make([]int64, 0, len(order))
		counts := make([]int64, 0, len(order))
		timestamps := make([]time.Time, 0, len(order))
		temps := make([]float64, 0, len(order))
		conditions := make([]string, 0, len(order))
		for _, id := range order {
			d := deltas[id]
			stationIDs = append(stationIDs, id)
			counts = append(counts, d.n)
			timestamps = append(timestamps, d.ts)
			temps = append(temps, d.temp)
			conditions = append(conditions, d.conditions)
		}

		if _, execErr := tx.Exec(ctx, bumpAppStatsSQL, int64(len(moved))); execErr != nil {
			return classify(execErr)
		}
		if _, execErr := tx.Exec(ctx, bumpStationsSQL,
			stationIDs, counts, timestamps, temps, conditions); execErr != nil {
			return classify(execErr)
		}

		stats, sErr := readStats(ctx, tx)
		if sErr != nil {
			return sErr
		}
		out = stats
		return nil
	})
	if err != nil {
		return store.Stats{}, err
	}
	return out, nil
}
