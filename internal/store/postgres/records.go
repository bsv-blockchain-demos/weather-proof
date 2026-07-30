package postgres

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// recordColumns is the fixed, explicit column list of weather_records, used by
// every UNALIASED query in this package (List's and Get's SELECT). Column
// order here is cosmetic — pgx.RowToStructByName matches each result column to
// a store.Record field by NAME (its `db` tag), not position — but completeness
// is not: this list, and recordColumnsAliased below, are the only two places
// naming every column, and no query in this package ever selects `*` (an
// unmatched column is a hard RowToStructByName runtime error, not a compile
// error).
const recordColumns = `id, station_id, timestamp, observation_time, data,
       status, attempts, claim_ref, adopt_required, claimed_at, txid,
       output_index, block_height, chain_status, mined_at, error,
       created_at, processed_at`

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

// clampLimit is the SINGLE enforcement point, for the whole package, of
// interfaces.go's frozen rule: a non-positive limit returns zero rows (or a
// zero count) with a NIL error, and is never left to Postgres's own LIMIT
// clause to decide. LIMIT 0 already returns zero rows with no error on its
// own, but a negative LIMIT raises a runtime error (SQLSTATE 2201W, which
// classify below maps to ErrOperation) rather than the "zero rows, nil
// error" the interface promises for EVERY non-positive value.
//
// It exists because prose alone has now failed TWICE to prevent this exact
// divergence, in this exact package. First, ClaimPending shipped with n <= 0
// unclamped despite interfaces.go already stating the rule in words, and
// only a later review caught it (fixed by an inline `if n <= 0` guard).
// Second — with that very doc comment sitting right above it, and a
// prescribed test suite that never exercised a non-positive limit — the
// verbatim implementation given for ReapExpired and Requeue omitted the same
// guard, and a negative limit measured the identical SQLSTATE 2201W leaking
// through classify. Two independent methods re-deriving the same one-line
// check is exactly how the second divergence happened, so every limit-taking
// query in this package (ClaimPending's n, ReapExpired's limit, both of
// Requeue's f.Limit uses) now calls this one function instead of writing its
// own `if n <= 0`. Nobody should ever need to inline a fresh non-positive
// check again; if a future method takes a limit, it calls clampLimit.
//
// ok reports whether the caller should proceed with the returned (always
// positive) limit. When ok is false, the caller must return its own
// zero-value success result — an empty-but-NON-NIL slice, or a zero count —
// with a nil error, and must not build or send a query at all: a canceled
// context reaching that far would otherwise surface as a non-nil error,
// which is exactly how the package's tests prove the guard fired before any
// database round trip.
func clampLimit(n int) (limit int, ok bool) {
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// clampOffset is the SINGLE enforcement point, for the whole package, of a
// rule interfaces.go's ListFilter.Offset and StationFilter.Offset doc
// comments now state explicitly: a negative Offset is clamped to zero, never
// left to reach Postgres's own OFFSET clause. OFFSET 0 already behaves this
// way on its own, but a negative OFFSET raises a runtime error (SQLSTATE
// 2201X, invalid_row_count_in_result_offset_clause, which classify maps to
// ErrOperation) rather than being treated as "start from the top" — the same
// class of gap clampLimit closes for a non-positive LIMIT, and reached the
// same way: a caller building a page from an HTTP query string can send a
// negative offset as easily as a negative limit, and interfaces.go used to
// document a rule for one of that pair and not the other.
//
// Clamp-to-zero is the chosen semantic — it matches "the store is total",
// exactly as clampLimit's "zero rows, never unbounded" does for a
// non-positive limit — rather than rejecting the call with an error: List's
// contract is to always answer, never to validate its filter.
//
// Both List methods in this package (RecordStore.List and
// StationStore.listStations) now call this one function instead of passing
// f.Offset straight through to a query argument.
func clampOffset(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ClaimPending implements store.RecordStore.
//
// ref is a parameter rather than a gen_random_uuid() call inside the SQL
// because gen_random_uuid() is VOLATILE: Postgres evaluates it once per
// updated row, so a 21-row claim would stamp 21 different refs and the batch
// label the adopt design uses as its idempotency key would not exist.
// Measured: the inline form produced 21 distinct refs for 21 rows; this form
// produces exactly 1.
//
// n is clamped by clampLimit — see its doc for why this is the package's
// single enforcement point rather than an inline check here. []store.Record{}
// rather than nil matches what pgx.CollectRows itself returns for a genuine
// zero-row result — CollectRows is AppendRows([]T{}, ...), so a real "nothing
// pending" result is also a non-nil empty slice — meaning a caller cannot
// distinguish "clamped" from "nothing was pending" by nil-ness either way.
func (s *RecordStore) ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]store.Record, error) {
	n, ok := clampLimit(n)
	if !ok {
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
	// classify wraps BeginFunc's own result, not just the closure's: BeginFunc
	// returns its own Begin/Commit failure unclassified when the closure never
	// even ran (e.g. the pool cannot acquire a connection to start the
	// transaction at all), and that path carries raw driver text — including
	// the DSN's user= and database= — that must never reach a caller. classify
	// is idempotent, so re-classifying a value the closure already classified
	// (e.g. store.ErrConflict from completeRecordsSQL) returns it unchanged
	// rather than downgrading it to ErrOperation.
	if err != nil {
		return store.Stats{}, classify(err)
	}
	return out, nil
}

// The three terminal writes the publisher's error classifier makes. All three
// share the same guard — `AND r.status = 'processing'` — so a write for a row
// that was already reaped, already completed or never claimed is a no-op
// rather than a corrupting overwrite.
//
// What differs between them is exactly the three columns that carry the
// classification, and each difference is load-bearing:
//
//	FailPermanent  status=failed   attempts+1  adopt_required=false
//	RequeueInfra   status=pending  attempts    adopt_required=false
//	MarkUnknown    status=pending  attempts    adopt_required=TRUE
//
// Only a permanent error spends the attempt budget. Only an AMBIGUOUS outcome
// sets adopt_required: it is the bit that makes the next claim run an adopt
// check first, and without it a row whose prior batch did in fact publish gets
// broadcast a second time. Preserving claim_ref alone is not enough, because a
// pending row with a ref but adopt_required false runs no adopt check at all.
const failPermanentSQL = `
UPDATE weather_records AS r
   SET status = 'failed', attempts = r.attempts + 1, error = $2,
       processed_at = now(), claimed_at = NULL, adopt_required = false
 WHERE r.id = ANY($1::text[]) AND r.status = 'processing'`

const requeueInfraSQL = `
UPDATE weather_records AS r
   SET status = 'pending', error = $2, claimed_at = NULL, adopt_required = false
 WHERE r.id = ANY($1::text[]) AND r.status = 'processing'`

const markUnknownSQL = `
UPDATE weather_records AS r
   SET status = 'pending', error = $2, claimed_at = NULL, adopt_required = true
 WHERE r.id = ANY($1::text[]) AND r.status = 'processing'`

// FailPermanent implements store.RecordStore.
func (s *RecordStore) FailPermanent(ctx context.Context, ids []string, reason string) error {
	return s.classifierWrite(ctx, failPermanentSQL, ids, reason)
}

// RequeueInfra implements store.RecordStore.
func (s *RecordStore) RequeueInfra(ctx context.Context, ids []string, reason string) error {
	return s.classifierWrite(ctx, requeueInfraSQL, ids, reason)
}

// MarkUnknown implements store.RecordStore.
func (s *RecordStore) MarkUnknown(ctx context.Context, ids []string, reason string) error {
	return s.classifierWrite(ctx, markUnknownSQL, ids, reason)
}

// classifierWrite runs one of the three terminal statements.
//
// stmt is always one of the three package constants above. It is a parameter
// of a private method and never derived from input, which is the only shape in
// which passing SQL as a value is acceptable: there is no code path by which a
// caller can supply a statement.
//
// ids is sorted before it is sent, on top of what the design spelled out:
// Task 8's review measured that a writer touching a claimed batch in a
// DIFFERENT id order than the claim took them produces deadlocks even under
// SKIP LOCKED (184 vs a 35-deadlock bare-FOR-UPDATE baseline; 33 vs 35 even
// with SKIP LOCKED against a differently-ordered concurrent writer). Each of
// the three classifier writes is exactly that shape: a multi-row UPDATE over
// `r.id = ANY($1::text[])` against rows ClaimPending already claimed (and
// ordered). classify already buckets SQLSTATE 40P01 as transient, so a
// deadlock here is survivable, but sorting removes the lock-order cycle at the
// source instead of leaning on retry — the same fix Complete applies to pubs
// and to the station order slice. The caller's slice is left untouched —
// sorted is a copy — because interfaces.go makes no promise that any of these
// three may reorder its argument.
func (s *RecordStore) classifierWrite(ctx context.Context, stmt string, ids []string, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	sorted := make([]string, len(ids))
	copy(sorted, ids)
	sort.Strings(sorted)
	if _, err := s.db.Exec(ctx, stmt, sorted, reason); err != nil {
		return classify(err)
	}
	return nil
}

// reapExpiredSQL reclaims rows stranded in processing past the lease.
//
// SKIP LOCKED here too, so a reap tick never blocks behind a live claim or a
// Complete. attempts is NOT touched: only a permanent error spends the budget.
// claim_ref is PRESERVED and claimed_at is deliberately LEFT SET — claimed_at
// is the ops timeline evidence, and adopt_required is the distinguishing
// signal:
//
//	pending,   adopt_required=true,  claim_ref NOT NULL -> REAPED, adopt-check first
//	pending,   adopt_required=false, claim_ref IS NULL  -> never claimed, publish fresh
//	processing, claimed_at within lease                 -> untouched
//
// Both the reaper and the error classifier converge on this same transition, so
// neither is dead code: the classifier writes it when it knows the outcome is
// ambiguous (resolved in seconds), and the reaper is the backstop for the
// process dying before that write landed (resolved after the lease).
//
// The ck_records_processing_leased CHECK is what makes the
// `claimed_at < now() - lease` predicate TOTAL, so no defensive
// `OR claimed_at IS NULL` is needed.
//
// `, c.id ASC` for the same reason claimSQL carries it, and the reasoning is
// NOT weaker here: claimSQL sets `claimed_at = now()` for every row it touches
// in one statement, and now() is transaction_timestamp(), so an entire claimed
// batch shares one claimed_at to the microsecond. `ORDER BY c.claimed_at ASC`
// alone is therefore a non-total order over exactly the rows the reaper looks
// at, and `LIMIT` over a non-total order selects an unspecified subset — so two
// successive limited reaps could return overlapping sets and leave part of the
// batch stranded for another lease.
const reapExpiredSQL = `
UPDATE weather_records AS r
   SET status = 'pending', adopt_required = true
 WHERE r.id IN (
         SELECT c.id
           FROM weather_records AS c
          WHERE c.status = 'processing'
            AND c.claimed_at < now() - $1::interval
          ORDER BY c.claimed_at ASC, c.id ASC
          LIMIT $2
            FOR UPDATE SKIP LOCKED
       )
RETURNING ` + recordColumnsAliased

// requeueCountSQL is the DryRun half of Requeue: the identical predicate with
// no write, so a dry run can never disagree with the real thing about which
// rows it would touch.
const requeueCountSQL = `
SELECT count(*) FROM (
  SELECT c.id
    FROM weather_records AS c
   WHERE c.status = $1
     AND c.created_at < now() - $2::interval
     AND ($3::bigint IS NULL OR c.station_id = $3)
   ORDER BY c.created_at ASC, c.id ASC
   LIMIT $4
) AS t`

// requeueSQL is the operator-driven bulk requeue.
//
// adopt_required is set for the same reason the reaper sets it: a requeued row
// may already have been published by a prior batch, and preserving claim_ref
// without the flag would run no adopt check at all. attempts is NOT reset —
// an operator requeue is not an amnesty on the budget.
const requeueSQL = `
UPDATE weather_records AS r
   SET status = 'pending', adopt_required = true, error = 'requeued by operator'
 WHERE r.id IN (
         SELECT c.id
           FROM weather_records AS c
          WHERE c.status = $1
            AND c.created_at < now() - $2::interval
            AND ($3::bigint IS NULL OR c.station_id = $3)
          ORDER BY c.created_at ASC, c.id ASC
          LIMIT $4
            FOR UPDATE SKIP LOCKED
       )`

// intervalArg renders d for a $n::interval bind parameter.
//
// This builds a VALUE, not SQL text: the statements above are constants and d
// travels as a parameter through the extended protocol. Microseconds is the
// finest unit a Postgres interval carries, so no precision is lost.
func intervalArg(d time.Duration) string {
	return strconv.FormatInt(d.Microseconds(), 10) + " microseconds"
}

// ReapExpired implements store.RecordStore.
//
// limit is clamped by clampLimit — see its doc for why this is the package's
// single enforcement point rather than an inline check here.
func (s *RecordStore) ReapExpired(ctx context.Context, lease time.Duration, limit int) ([]store.Record, error) {
	limit, ok := clampLimit(limit)
	if !ok {
		return []store.Record{}, nil
	}
	rows, err := s.db.Query(ctx, reapExpiredSQL, intervalArg(lease), limit)
	if err != nil {
		return nil, classify(err)
	}
	return collectRecords(rows)
}

// Requeue implements store.RecordStore.
//
// f.Limit is clamped by clampLimit — see its doc for why this is the
// package's single enforcement point rather than an inline check here. The
// clamp applies identically to both the DryRun count and the real write, so
// neither path can disagree with the other about a non-positive limit.
//
// f.Status == StatusCompleted is refused before either path runs, for the
// same reason and with the same (0, nil) semantic — see
// RequeueFilter.Status's doc comment. requeueSQL and requeueCountSQL both
// accept ANY status value as their $1 predicate, including 'completed', and
// would otherwise un-publish a completed row back to pending without
// reversing app_stats or the station counters Complete already advanced,
// making the row re-claimable and letting a second Complete double-count one
// reading.
func (s *RecordStore) Requeue(ctx context.Context, f store.RequeueFilter) (int64, error) {
	if f.Status == store.StatusCompleted {
		return 0, nil
	}
	limit, ok := clampLimit(f.Limit)
	if !ok {
		return 0, nil
	}
	if f.DryRun {
		var n int64
		err := s.db.QueryRow(ctx, requeueCountSQL,
			string(f.Status), intervalArg(f.Since), f.StationID, limit).Scan(&n)
		if err != nil {
			return 0, classify(err)
		}
		return n, nil
	}
	ct, err := s.db.Exec(ctx, requeueSQL,
		string(f.Status), intervalArg(f.Since), f.StationID, limit)
	if err != nil {
		return 0, classify(err)
	}
	return ct.RowsAffected(), nil
}

// listRecordsSQL is the record list.
//
// The optional filters are NULL-able bind parameters in ONE static statement
// rather than a conditionally assembled WHERE clause. That is the rule for
// every optional filter in this package: if a clause is ever assembled at all,
// only fixed literal fragments may be appended and every value goes in the
// args slice.
//
// ORDER BY created_at DESC, id DESC — the tiebreaker is mandatory, not
// decoration. See claimSQL's comment and TestListIsATotalOrderAcrossATiedBatch.
//
// ACCEPTED RESIDUAL, stated rather than hidden: the tiebreaker makes the order
// TOTAL, which is necessary but not sufficient for stable pagination. Page
// boundaries still shift when a new poll lands between the page-1 and page-2
// requests, because OFFSET counts from a moving head. Keyset pagination
// (WHERE (created_at, id) < ($1, $2)) would fix both, but the frontend sends
// page=, so OFFSET is required.
const listRecordsSQL = `
SELECT ` + recordColumns + `
    FROM weather_records
   WHERE ($1::bigint IS NULL OR station_id = $1)
     AND ($2::text   IS NULL OR status = $2)
   ORDER BY created_at DESC, id DESC
   LIMIT $3 OFFSET $4`

// countRecordsSQL is the unpaged total for pagination. Its predicate is
// character-for-character the same as listRecordsSQL's, so the two can never
// disagree about what is in scope.
const countRecordsSQL = `
SELECT count(*) FROM weather_records
   WHERE ($1::bigint IS NULL OR station_id = $1)
     AND ($2::text   IS NULL OR status = $2)`

// getRecordSQL is the detail read. The id column is text, so a malformed id is
// a miss rather than a database error — which, together with the store.ValidText
// screen in Get, is why this path can never produce a 500 for bad input.
const getRecordSQL = `
SELECT ` + recordColumns + `
    FROM weather_records WHERE id = $1`

// txIDExistsSQL is the anti-amplification gate in front of the BEEF proof
// endpoint. The partial index ix_records_txid ... WHERE txid IS NOT NULL serves
// it.
const txIDExistsSQL = `SELECT 1 FROM weather_records WHERE txid = $1 LIMIT 1`

// List implements store.RecordStore.
//
// f.Limit is clamped by clampLimit — see its doc for why this is the
// package's single enforcement point rather than an inline check here. Unlike
// ClaimPending/ReapExpired/Requeue, a clamped limit does NOT short-circuit the
// whole method: interfaces.go requires Total to still report the full unpaged
// count of matching rows even when the page is empty, so the count query
// below always runs against the same two filter args, independent of whether
// the page query ran at all.
func (s *RecordStore) List(ctx context.Context, f store.ListFilter) ([]store.Record, int64, error) {
	// A *store.Status encodes as text or NULL; passing the pointer straight
	// through is what makes the NULL-able predicate work.
	var statusArg *string
	if f.Status != nil {
		value := string(*f.Status)
		statusArg = &value
	}

	limit, ok := clampLimit(f.Limit)
	recs := []store.Record{}
	if ok {
		rows, err := s.db.Query(ctx, listRecordsSQL, f.StationID, statusArg, limit, clampOffset(f.Offset))
		if err != nil {
			return nil, 0, classify(err)
		}
		recs, err = collectRecords(rows)
		if err != nil {
			return nil, 0, err
		}
	}

	var total int64
	if err := s.db.QueryRow(ctx, countRecordsSQL, f.StationID, statusArg).Scan(&total); err != nil {
		return nil, 0, classify(err)
	}
	return recs, total, nil
}

// Get implements store.RecordStore.
//
// The store.ValidText screen is not defensive noise. A NUL byte cannot be bound
// to a text parameter at all — measured, SQLSTATE 22021 invalid byte sequence,
// raised before the predicate is evaluated — and `GET /api/weather/%00`
// delivers one straight from Go's path decoding. No record id can contain a NUL
// (they are uuidv7 strings), so such an id is a MISS by definition, and
// answering ErrNotFound is both true and the only answer that keeps this path
// unable to 500. B2 additionally rejects the input with a 400 at its parse
// layer; this is the belt to that braces.
func (s *RecordStore) Get(ctx context.Context, id string) (store.Record, error) {
	if store.ValidText(id) != nil {
		return store.Record{}, store.ErrNotFound
	}
	rows, err := s.db.Query(ctx, getRecordSQL, id)
	if err != nil {
		return store.Record{}, classify(err)
	}
	rec, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[store.Record])
	if err != nil {
		return store.Record{}, classify(err)
	}
	return rec, nil
}

// TxIDExists implements store.RecordStore.
//
// Same screen as Get, and it matters more here: this is the anti-amplification
// gate in front of the proof endpoint, so it is reachable unauthenticated with
// an arbitrary path segment. A txid containing a NUL matches no row, so
// (false, nil) is the truthful answer as well as the un-500-able one.
func (s *RecordStore) TxIDExists(ctx context.Context, txID string) (bool, error) {
	if store.ValidText(txID) != nil {
		return false, nil
	}
	var one int
	err := s.db.QueryRow(ctx, txIDExistsSQL, txID).Scan(&one)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, classify(err)
	}
	return true, nil
}

// snapshotSQL is the row-count half of the operational heartbeat, in one query.
//
// This is a Seq Scan and will stay one: the chain_status and processed_at
// filters cannot be served by ix_records_status_created. That is fine at a
// 60-second sampler interval and ~105k rows a year, but it IS an
// unbounded-growth full scan, so it is written down here rather than being a
// surprise in year five.
const snapshotSQL = `
SELECT count(*) FILTER (WHERE status = 'pending')    AS pending_rows,
       count(*) FILTER (WHERE status = 'processing') AS processing_rows,
       count(*) FILTER (WHERE status = 'failed')     AS failed_rows,
       count(*) FILTER (WHERE status = 'completed'
                          AND (chain_status IS NULL OR chain_status <> 'mined')
                          AND processed_at < now() - interval '1 hour')
                                                     AS still_unmined_older_than_1h,
       count(*) FILTER (WHERE chain_status = 'mined')   AS mined_count,
       count(*) FILTER (WHERE chain_status = 'aborted') AS aborted_count
  FROM weather_records`

// Snapshot implements store.RecordStore.
func (s *RecordStore) Snapshot(ctx context.Context) (store.Snapshot, error) {
	var out store.Snapshot
	err := s.db.QueryRow(ctx, snapshotSQL).Scan(
		&out.PendingRows, &out.ProcessingRows, &out.FailedRows,
		&out.StillUnminedOlderThan1h, &out.MinedCount, &out.AbortedCount)
	if err != nil {
		return store.Snapshot{}, classify(err)
	}
	return out, nil
}

// setBlockHeightSQL refreshes the mined height of every record sharing a txid.
//
// `AND status = 'completed'` is what stops an aborted row (which can carry a
// txid) being reported as mined. mined_at falls back to now() when the caller
// has no upstream timestamp.
const setBlockHeightSQL = `
UPDATE weather_records
   SET block_height = $2,
       chain_status = 'mined',
       mined_at     = coalesce($3::timestamptz, now())
 WHERE txid = $1 AND status = 'completed'`

// setStationHeightSQL mirrors the height onto every station that has a record
// in this transaction. greatest(...) means a later confirmation for an OLDER
// transaction can never lower a station's displayed height.
//
// `AND r.status = 'completed'` in the subquery, for the SAME reason
// setBlockHeightSQL carries it, and its absence was a real hole rather than a
// theoretical one: an aborted row keeps its txid, so without the filter a txid
// that exists ONLY on aborted or failed rows still raised that station's
// displayed last_block_height — on the unauthenticated verify path, where the
// caller chooses the txids. The record filter alone was not enough because the
// two statements select their targets independently.
const setStationHeightSQL = `
UPDATE stations AS s
   SET last_block_height = greatest(coalesce(s.last_block_height, $2), $2),
       updated_at        = now()
 WHERE s.station_id IN (
         SELECT r.station_id FROM weather_records AS r
          WHERE r.txid = $1 AND r.status = 'completed'
       )`

// reconcileCandidatesSQL finds completed rows that are not yet known mined.
// The partial index ix_records_reconcile serves it.
//
// `, id ASC` for the third and last time in this file, and the premise is
// identical: completeRecordsSQL sets `processed_at = now()` for a whole batch in
// one statement, so every row published together shares one processed_at to the
// microsecond. Without the tiebreaker `LIMIT $2` takes an unspecified subset of
// a tied batch, so two reconciler ticks can keep re-reading the same rows while
// others in the same batch are never looked at.
const reconcileCandidatesSQL = `
SELECT ` + recordColumns + `
  FROM weather_records
 WHERE status = 'completed'
   AND (chain_status IS NULL OR chain_status <> 'mined')
   AND processed_at < now() - $1::interval
 ORDER BY processed_at ASC, id ASC
 LIMIT $2`

// SetBlockHeights implements store.RecordStore.
//
// Both tables move in ONE transaction. The atomicity requirement transfers from
// the Mongo session the TypeScript used on this path; the mechanism does not.
//
// SECURITY INVARIANT, and it is the whole reason this endpoint can be
// unauthenticated: an HTTP caller supplies only txids. The BlockHeight in every
// update is assigned from a block explorer's response by the verify handler,
// and the request DTO has no height field. Do not add one.
//
// ups is sorted by TxID before either statement runs, on top of what the design
// spelled out: Task 8's review measured that a writer touching rows in a
// DIFFERENT order than a concurrent writer deadlocks even under SKIP LOCKED
// (184 vs a 35-deadlock bare-FOR-UPDATE baseline; 33 vs 35 even with SKIP
// LOCKED against a differently-ordered concurrent writer). Each loop iteration
// below issues two multi-row UPDATEs keyed off u.TxID, so two overlapping
// SetBlockHeights calls supplying the same txids in different orders are
// exactly that shape. The caller's slice is left untouched — sorted is a copy
// — matching Complete's pubs and classifierWrite's ids, since interfaces.go
// makes no promise that SetBlockHeights may reorder its argument.
func (s *RecordStore) SetBlockHeights(ctx context.Context, ups []store.BlockHeightUpdate) error {
	if len(ups) == 0 {
		return nil
	}
	sorted := make([]store.BlockHeightUpdate, len(ups))
	copy(sorted, ups)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TxID < sorted[j].TxID })

	// classify wraps BeginFunc's own result here too, for the identical reason
	// Complete does: BeginFunc's own Begin/Commit failure bypasses every
	// classify call inside the closure, and this is the UNAUTHENTICATED verify
	// path — reachable with no login at all — so a raw connect failure
	// leaking the DSN's user= and database= here is the worse place for this
	// gap to exist, not a lesser one.
	return classify(pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, u := range sorted {
			if _, err := tx.Exec(ctx, setBlockHeightSQL, u.TxID, u.BlockHeight, u.MinedAt); err != nil {
				return classify(err)
			}
			if _, err := tx.Exec(ctx, setStationHeightSQL, u.TxID, u.BlockHeight); err != nil {
				return classify(err)
			}
		}
		return nil
	}))
}

// ReconcileCandidates implements store.RecordStore.
//
// limit is clamped by clampLimit — see its doc comment for why this is the
// package's single enforcement point rather than an inline check here.
// ReconcileCandidates has no separate Total to preserve (unlike List and
// ListStations), so a clamped limit short-circuits the whole method exactly as
// ClaimPending and ReapExpired do, rather than still running an unpaged count.
func (s *RecordStore) ReconcileCandidates(
	ctx context.Context, olderThan time.Duration, limit int,
) ([]store.Record, error) {
	limit, ok := clampLimit(limit)
	if !ok {
		return []store.Record{}, nil
	}
	rows, err := s.db.Query(ctx, reconcileCandidatesSQL, intervalArg(olderThan), limit)
	if err != nil {
		return nil, classify(err)
	}
	return collectRecords(rows)
}
