package postgres

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// stationColumns is the explicit column list of stations, matching
// store.Station's db tags in order. As everywhere in this package, SELECT * is
// forbidden.
const stationColumns = `station_id, name, location, latitude, longitude, is_active, tx_records,
       last_reading, last_temp, last_conditions, last_block_height, created_at, updated_at`

// upsertStationSQL refreshes a station's IDENTITY columns only.
//
// What it deliberately does not touch: tx_records, last_reading, last_temp,
// last_conditions and last_block_height. Those are counters that only Complete
// and SetBlockHeights move, and an upsert that reset them would zero the whole
// dashboard on every poll.
const upsertStationSQL = `
INSERT INTO stations (station_id, name, location, latitude, longitude, is_active, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (station_id) DO UPDATE
   SET name       = EXCLUDED.name,
       location   = EXCLUDED.location,
       latitude   = EXCLUDED.latitude,
       longitude  = EXCLUDED.longitude,
       is_active  = EXCLUDED.is_active,
       updated_at = now()`

const getStationSQL = `
SELECT ` + stationColumns + `
  FROM stations WHERE station_id = $1`

// The three list branches. ONE parsed value drives BOTH the filter and the
// sort, which is what removes the TypeScript's ?search=0 500 (there the filter
// and the sort were decided by two different expressions that disagreed for
// the input "0").
//
// Each branch is a WHOLE constant statement rather than a shared skeleton with
// an appended fragment. That is the pattern this codebase requires wherever a
// query shape varies: map an opaque input to a complete const query, never
// concatenate.
const listStationsAllSQL = `
SELECT ` + stationColumns + `
  FROM stations ORDER BY station_id ASC LIMIT $1 OFFSET $2`

const countStationsAllSQL = `SELECT count(*) FROM stations`

const listStationsByIDSQL = `
SELECT ` + stationColumns + `
  FROM stations WHERE station_id = $1 ORDER BY station_id ASC LIMIT $2 OFFSET $3`

const countStationsByIDSQL = `SELECT count(*) FROM stations WHERE station_id = $1`

// websearch_to_tsquery and NEVER to_tsquery. to_tsquery RAISES on unbalanced
// quotes or a bare &, |, ! or :, which on a public search box is a 500 from a
// single keystroke. websearch_to_tsquery is defined never to error on arbitrary
// input; verified over 25+ adversarial strings including 200 characters, ":*",
// "&|!()" and an unclosed quote.
//
// station_id ASC is the mandatory tiebreaker, not decoration: ts_rank ties
// routinely (two rows both scoring a single-token match), and without a
// tiebreaker the order is not total. See
// TestStationSearchRankTiesBreakByIDAscending, which was written by first
// reading this query's EXPLAIN plan.
//
// A NUL byte, and any malformed UTF-8, is the one family of input this
// statement cannot receive at all: binding either fails during parameter BIND
// with SQLSTATE 22021, before any function runs. List screens f.Search with
// store.ValidText first, so the "cannot 500" property belongs to the pair and
// not to websearch_to_tsquery alone.
const listStationsSearchSQL = `
SELECT ` + stationColumns + `
  FROM stations
 WHERE search_tsv @@ websearch_to_tsquery('english', $1)
 ORDER BY ts_rank(search_tsv, websearch_to_tsquery('english', $1)) DESC, station_id ASC
 LIMIT $2 OFFSET $3`

const countStationsSearchSQL = `
SELECT count(*) FROM stations WHERE search_tsv @@ websearch_to_tsquery('english', $1)`

// StationStore is the pgx/v5 implementation of store.StationStore.
type StationStore struct {
	db *pgxpool.Pool
}

// NewStationStore returns a StationStore backed by db.
func NewStationStore(db *pgxpool.Pool) *StationStore {
	return &StationStore{db: db}
}

// Upsert implements store.StationStore.
func (s *StationStore) Upsert(ctx context.Context, in store.Station) error {
	_, err := s.db.Exec(ctx, upsertStationSQL,
		in.StationID, in.Name, in.Location, in.Latitude, in.Longitude, in.IsActive)
	if err != nil {
		return classify(err)
	}
	return nil
}

// Get implements store.StationStore.
func (s *StationStore) Get(ctx context.Context, stationID int64) (store.Station, error) {
	rows, err := s.db.Query(ctx, getStationSQL, stationID)
	if err != nil {
		return store.Station{}, classify(err)
	}
	st, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[store.Station])
	if err != nil {
		return store.Station{}, classify(err)
	}
	return st, nil
}

// List implements store.StationStore.
//
// The branch is chosen in Go by strconv.ParseInt, which is a deliberate
// behavior change from the TypeScript's parseInt: JS parseInt("12345abc")
// returns 12345 and so treats that as an exact id lookup, whereas Go's parse
// errors and this routes it to full-text search instead. Go's behavior is the
// better one and matches the spec's phrasing, but it is a visible change on a
// free-text box.
func (s *StationStore) List(ctx context.Context, f store.StationFilter) ([]store.Station, int64, error) {
	if f.Search == "" {
		return s.listStations(ctx, listStationsAllSQL, countStationsAllSQL, f, nil)
	}
	// A search Postgres cannot even BIND matches nothing, and says so with an
	// empty page rather than an error. Binding a NUL byte, or any malformed
	// UTF-8, fails with SQLSTATE 22021 before websearch_to_tsquery is reached
	// (measured), and `?search=%00` hands one over from Go's query decoding,
	// so without this the public search box 500s on a single crafted request.
	// Empty results rather than store.ErrInvalidText is deliberate at THIS
	// layer: the store stays total, and B2's parse helpers return the 400 that
	// tells the caller why.
	if store.ValidText(f.Search) != nil {
		return []store.Station{}, 0, nil
	}
	// ParseInt with bitSize 64 rather than Atoi, so a value that overflows the
	// bigint column is a parse FAILURE and falls through to text search rather
	// than reaching Postgres and raising 22003 numeric_out_of_range, which the
	// API layer would have to render as a 500.
	id, err := strconv.ParseInt(f.Search, 10, 64)
	if err == nil {
		return s.listStations(ctx, listStationsByIDSQL, countStationsByIDSQL, f, []any{id})
	}
	return s.listStations(ctx, listStationsSearchSQL, countStationsSearchSQL, f, []any{f.Search})
}

// listStations runs one of the three branch pairs.
//
// listStmt and countStmt are always two of the six package constants above.
// prefixArgs holds whatever bind parameter precedes limit/offset in listStmt
// (none for the unfiltered branch, the parsed id, or the search text) and is
// reused verbatim as countStmt's whole argument list, since every countStmt
// above takes exactly the same predicate arguments as its listStmt minus
// limit/offset.
//
// f.Limit is clamped by clampLimit — see its doc comment in records.go for why
// this is the package's single enforcement point rather than an inline check
// here. As in RecordStore.List, a clamped (non-positive) limit does NOT
// short-circuit the whole method: interfaces.go requires Total to still
// report the full unpaged count of matching rows even when the page is empty,
// so the count query always runs against the same prefixArgs, independent of
// whether the page query ran at all.
func (s *StationStore) listStations(
	ctx context.Context,
	listStmt, countStmt string,
	f store.StationFilter,
	prefixArgs []any,
) ([]store.Station, int64, error) {
	limit, ok := clampLimit(f.Limit)
	sts := []store.Station{}
	if ok {
		listArgs := make([]any, 0, len(prefixArgs)+2)
		listArgs = append(listArgs, prefixArgs...)
		listArgs = append(listArgs, limit, f.Offset)

		rows, err := s.db.Query(ctx, listStmt, listArgs...)
		if err != nil {
			return nil, 0, classify(err)
		}
		sts, err = pgx.CollectRows(rows, pgx.RowToStructByName[store.Station])
		if err != nil {
			return nil, 0, classify(err)
		}
	}

	var total int64
	if err := s.db.QueryRow(ctx, countStmt, prefixArgs...).Scan(&total); err != nil {
		return nil, 0, classify(err)
	}
	return sts, total, nil
}

// Stats implements store.StationStore.
//
// The statement lives in records.go next to Complete, which reads it inside the
// publish transaction; readStats is shared so the two can never disagree about
// what the four dashboard values are.
func (s *StationStore) Stats(ctx context.Context) (store.Stats, error) {
	return readStats(ctx, s.db)
}
