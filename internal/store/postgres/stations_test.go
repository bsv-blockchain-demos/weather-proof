package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

var (
	_ func(context.Context, store.Station) error                                 = (*postgres.StationStore)(nil).Upsert
	_ func(context.Context, int64) (store.Station, error)                        = (*postgres.StationStore)(nil).Get
	_ func(context.Context, store.StationFilter) ([]store.Station, int64, error) = (*postgres.StationStore)(nil).List
)

func seedStations(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	rows := []struct {
		id       int64
		name     string
		location string
		active   bool
	}{
		{1000, "Harbor Mast", "Bristol Docks", true},
		{1001, "Clifton Ridge", "Bristol Downs", true},
		{1002, "Severn Beach", "Gloucestershire", false},
		{2000, "Kelvin Yard", "Glasgow", true},
	}
	for _, r := range rows {
		if _, err := pool.Exec(ctx,
			"INSERT INTO stations (station_id, name, location, is_active) VALUES ($1, $2, $3, $4)",
			r.id, r.name, r.location, r.active); err != nil {
			t.Fatalf("seeding station %d: %v", r.id, err)
		}
	}
}

func stationIDs(sts []store.Station) []int64 {
	out := make([]int64, 0, len(sts))
	for _, s := range sts {
		out = append(out, s.StationID)
	}
	return out
}

func TestStationUpsertInsertsThenUpdates(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ss := postgres.NewStationStore(pool)

	lat, lon := 51.4545, -2.5879
	if err := ss.Upsert(ctx, store.Station{
		StationID: 1000, Name: "Harbor Mast", Location: "Bristol Docks",
		Latitude: &lat, Longitude: &lon, IsActive: true,
	}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	got, err := ss.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Harbor Mast" || got.Location != "Bristol Docks" || !got.IsActive {
		t.Fatalf("after insert: %+v", got)
	}
	if got.Latitude == nil || *got.Latitude != lat {
		t.Fatalf("latitude = %v, want %v", got.Latitude, lat)
	}
	if got.TxRecords != 0 {
		t.Fatalf("TxRecords = %d, want 0", got.TxRecords)
	}
	if got.LastReading != nil || got.LastTemp != nil || got.LastBlockHeight != nil {
		t.Fatalf("a fresh station must have nil nullable fields: %+v", got)
	}
	if got.LastConditions != "" {
		t.Fatalf("LastConditions = %q, want the empty string (NOT null: the frontend types it as a plain string)", got.LastConditions)
	}

	// Simulate the counters having moved, then upsert again: the identity
	// columns refresh and the COUNTERS MUST SURVIVE. An upsert that reset
	// tx_records would silently zero the whole dashboard on every poll.
	if _, execErr := pool.Exec(ctx, `
		UPDATE stations
		   SET tx_records = 17, last_temp = 18, last_conditions = 'Clear', last_reading = now()
		 WHERE station_id = 1000`); execErr != nil {
		t.Fatalf("simulating counters: %v", execErr)
	}

	if upsertErr := ss.Upsert(ctx, store.Station{
		StationID: 1000, Name: "Harbor Mast II", Location: "Bristol Harborside", IsActive: false,
	}); upsertErr != nil {
		t.Fatalf("second Upsert: %v", upsertErr)
	}
	got, err = ss.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Name != "Harbor Mast II" || got.Location != "Bristol Harborside" || got.IsActive {
		t.Fatalf("after update: %+v", got)
	}
	if got.TxRecords != 17 {
		t.Fatalf("TxRecords = %d after upsert, want the preserved 17", got.TxRecords)
	}
	if got.LastTemp == nil || *got.LastTemp != 18 {
		t.Fatalf("LastTemp = %v after upsert, want the preserved 18", got.LastTemp)
	}
	if got.LastConditions != "Clear" {
		t.Fatalf("LastConditions = %q after upsert, want the preserved Clear", got.LastConditions)
	}
	if got.Latitude != nil {
		t.Fatalf("Latitude = %v; the second upsert supplied nil and identity columns DO refresh", got.Latitude)
	}
}

func TestStationGetUnknownIsErrNotFound(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ss := postgres.NewStationStore(pool)

	for _, id := range []int64{0, -1, 999999, 9223372036854775807} {
		if _, err := ss.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get(%d) error = %v, want store.ErrNotFound", id, err)
		}
	}
}

func TestStationListNoSearchIsAscendingByID(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	sts, total, err := ss.List(ctx, store.StationFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	want := []int64{1000, 1001, 1002, 2000}
	got := stationIDs(sts)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// Paging.
	page2, total, err := ss.List(ctx, store.StationFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if total != 4 {
		t.Fatalf("total on page 2 = %d, want 4", total)
	}
	got = stationIDs(page2)
	if len(got) != 2 || got[0] != 1002 || got[1] != 2000 {
		t.Fatalf("page 2 = %v, want [1002 2000]", got)
	}
}

// TestStationListNumericSearchIsAnExactLookup pins the rule that ONE parsed
// value drives BOTH the filter and the sort.
//
// A deliberate behavior change from the TypeScript: JS parseInt("12345abc") is
// 12345, so the TypeScript treats that as an exact station-id lookup. Go's
// strconv.Atoi errors on it, so it routes to full-text search instead. Go's
// behavior is the better one and matches the spec's wording, but it IS a
// visible change on a free-text search box and is asserted here so it is
// stated rather than discovered.
func TestStationListNumericSearchIsAnExactLookup(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	sts, total, err := ss.List(ctx, store.StationFilter{Search: "1001", Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(sts) != 1 || sts[0].StationID != 1001 {
		t.Fatalf("search 1001 = %v / total %d, want [1001] / 1", stationIDs(sts), total)
	}

	// A numeric search matching nothing is 200 with no rows, never an error.
	sts, total, err = ss.List(ctx, store.StationFilter{Search: "424242", Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(sts) != 0 {
		t.Fatalf("search 424242 = %v / total %d, want none", stationIDs(sts), total)
	}

	// "0" parses, so it is an exact lookup for station 0 and finds nothing.
	// In the TypeScript this exact input is a 500: the filter becomes
	// stationId: 0 while the sort expression `search && !parseInt(search,10)` is
	// truthy for "0", so Mongo is asked to sort by text score with no $text in
	// the filter and errors.
	sts, total, err = ss.List(ctx, store.StationFilter{Search: "0", Limit: 50})
	if err != nil {
		t.Fatalf("List with search=0: %v", err)
	}
	if total != 0 || len(sts) != 0 {
		t.Fatalf("search 0 = %v / total %d, want none", stationIDs(sts), total)
	}

	// "12345abc" does NOT parse in Go, so it becomes a text search.
	if _, _, err := ss.List(ctx, store.StationFilter{Search: "12345abc", Limit: 50}); err != nil {
		t.Fatalf("List with search=12345abc: %v", err)
	}
}

func TestStationListTextSearchRanksAndFallsBackToID(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	sts, total, err := ss.List(ctx, store.StationFilter{Search: "bristol", Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 {
		t.Fatalf("search bristol total = %d, want 2", total)
	}
	for _, s := range sts {
		if !strings.Contains(strings.ToLower(s.Location), "bristol") {
			t.Errorf("station %d (%q) matched a bristol search", s.StationID, s.Location)
		}
	}

	// A search matching a name rather than a location.
	sts, total, err = ss.List(ctx, store.StationFilter{Search: "kelvin", Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(sts) != 1 || sts[0].StationID != 2000 {
		t.Fatalf("search kelvin = %v / total %d, want [2000] / 1", stationIDs(sts), total)
	}

	// A search matching nothing.
	sts, total, err = ss.List(ctx, store.StationFilter{Search: "reykjavik", Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(sts) != 0 {
		t.Fatalf("search reykjavik = %v / total %d, want none", stationIDs(sts), total)
	}
}

// TestStationSearchNeverErrorsOnAdversarialInput is the fuzz gate.
//
// websearch_to_tsquery is DEFINED never to raise on arbitrary input, which is
// exactly why it must be used instead of to_tsquery: to_tsquery RAISES on
// unbalanced quotes or a bare &, |, ! or :, and on a public search box that is
// a 500 on a keystroke.
//
// The function being total is necessary but NOT sufficient, and the NUL and
// malformed-UTF-8 entries in the table below are why. A NUL byte, and any
// malformed UTF-8 sequence, never reaches websearch_to_tsquery at all: pgx
// fails to BIND the parameter, with SQLSTATE 22021 invalid byte sequence
// (measured against postgres:17-alpine with pgx v5.10.0), so `?search=%00`
// was a 500 until List learned to screen f.Search with store.ValidText. Keep
// those entries: they are the regression test on the only input in this
// table that ever actually broke, plus the wider malformed-UTF-8 family
// store.ValidText was written to catch alongside it.
func TestStationSearchNeverErrorsOnAdversarialInput(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	inputs := []string{
		"", " ", "0", "00", "0abc", ":*", "&|!()", "\"unclosed", "\\", "\\\\",
		"<->", "a:b:c", "'; DROP TABLE stations; --", "%", "_", "*",
		strings.Repeat("a", 200), "\x00nul", "nul\x00", "bristol OR 1=1", "-bristol",
		"NOT", "AND", "()()()", "\t\n",
		// NUL alone, and the wider malformed-UTF-8 family store.ValidText
		// rejects: a bare high bit, an out-of-range byte, a truncated
		// multibyte sequence, a lone UTF-16 surrogate re-encoded as bytes,
		// and an overlong encoding of NUL. None of these reach
		// websearch_to_tsquery at all — they fail at parameter BIND — which
		// is exactly why store.ValidText, not the function's own totality,
		// is what makes the search box unable to 500.
		"\x00", "\x80", "\xff", "\xe2\x98", "\xed\xa0\x80", "\xc0\x80",
		"bristol or docks", // lowercase "or", the operator keyword itself
	}
	if len(inputs) < 14 {
		t.Fatalf("the fuzz table has %d inputs, want at least 14", len(inputs))
	}
	for _, in := range inputs {
		sts, total, err := ss.List(ctx, store.StationFilter{Search: in, Limit: 50})
		if err != nil {
			t.Errorf("List(search=%q) error = %v, want nil", in, err)
			continue
		}
		if total < 0 {
			t.Errorf("List(search=%q) total = %d", in, total)
		}
		if int64(len(sts)) > total {
			t.Errorf("List(search=%q) returned %d rows with total %d", in, len(sts), total)
		}
	}
}

// TestStationSearchMatchesWellFormedUnicodeText is the positive counterpart
// to the fuzz gate above: rejecting adversarial input is only half of the
// "cannot 500" property, and a search box that rejects everything including
// well-formed multibyte text would technically pass a test that only checks
// "never errors". Accented Latin and an emoji (deliberately non-Han, per this
// repo's gosmopolitan rule) must both still MATCH the station they describe.
func TestStationSearchMatchesWellFormedUnicodeText(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	const unicodeName = "Fjörður Ünïcödé 🌦 Post"
	if err := ss.Upsert(ctx, store.Station{
		StationID: 3000, Name: unicodeName, Location: "Northern Reach", IsActive: true,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	sts, total, err := ss.List(ctx, store.StationFilter{Search: "Fjörður", Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(sts) != 1 || sts[0].StationID != 3000 {
		t.Fatalf("search Fjörður = %v / total %d, want [3000] / 1", stationIDs(sts), total)
	}

	sts, total, err = ss.List(ctx, store.StationFilter{Search: "Ünïcödé", Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(sts) != 1 || sts[0].StationID != 3000 {
		t.Fatalf("search Ünïcödé = %v / total %d, want [3000] / 1", stationIDs(sts), total)
	}
}

// TestStationSearchRankTiesBreakByIDAscending pins `, station_id ASC` in
// listStationsSearchSQL's ORDER BY.
//
// EXPLAIN ANALYZE against this exact schema (measured, scratch database, not
// this suite's fixtures) shows this query's plan shape is a Seq Scan feeding
// an explicit Sort node — ts_rank is not indexed, so Postgres cannot satisfy
// the order from an index — the same shape records_requeue_test.go measured
// for reapExpiredSQL. A tied rank group of 40 rows inserted in ascending
// station_id order, with NO churn, returned in ascending order whether or not
// the tiebreaker was present: the Sort's quicksort happened to preserve the
// Seq Scan's heap order for an all-ties input, so a no-churn fixture cannot
// discriminate this at all (confirmed directly, not assumed). Churning the
// heap into reapChurnOrder's permutation before searching DOES discriminate
// it: with the tiebreaker, the result was still exactly ascending 0..39
// regardless of churn; without it, the result was exactly the churn
// permutation itself. 40 rows and the full permutation are both load-bearing
// here, mirroring reapExpiredSQL's own measured N=40 requirement, and
// reapChurnOrder is reused rather than redeclared: the property it provides —
// some heap order other than insertion order — is not query-specific.
func TestStationSearchRankTiesBreakByIDAscending(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	ss := postgres.NewStationStore(pool)

	// reapChurnCount and reapChurnOrder are declared in
	// records_requeue_test.go, in this same package.
	ids := make([]int64, reapChurnCount)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i := range reapChurnCount {
		ids[i] = int64(i)
		if _, execErr := tx.Exec(ctx,
			"INSERT INTO stations (station_id, name, location, is_active) VALUES ($1, $2, $2, true)",
			ids[i], "Meridian Tie"); execErr != nil {
			t.Fatalf("seeding %d: %v", i, execErr)
		}
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// Churn the heap before searching: see the doc comment above for why a
	// freshly loaded heap cannot discriminate the tiebreaker for THIS query's
	// plan shape. The no-op SET name = name still moves the row to a new
	// physical heap slot under MVCC.
	for _, idx := range reapChurnOrder {
		if _, execErr := pool.Exec(ctx,
			"UPDATE stations SET name = name WHERE station_id = $1", ids[idx]); execErr != nil {
			t.Fatalf("churning row %d: %v", idx, execErr)
		}
	}

	sts, total, err := ss.List(ctx, store.StationFilter{Search: "meridian", Limit: reapChurnCount})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != int64(reapChurnCount) {
		t.Fatalf("total = %d, want %d", total, reapChurnCount)
	}
	got := stationIDs(sts)
	if len(got) != len(ids) {
		t.Fatalf("got %d rows, want %d", len(got), len(ids))
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("got %v, want strictly ascending %v (tiebreaker not honored under churn)", got, ids)
		}
	}
}

// TestStationListNonPositiveLimitIsZeroRowsNilErrorTotalUnaffected pins
// clampLimit's route through StationStore.List: interfaces.go's frozen rule
// is that a non-positive limit returns zero rows with a NIL error and is
// NEVER treated as unbounded, while Total keeps reporting the full unpaged
// count. That rule has shipped violated three times already in this package
// (ClaimPending, then ReapExpired/Requeue, then List had the route right with
// no test proving it) — so every limit-taking query gets its own test proving
// the route, not just a shared helper.
func TestStationListNonPositiveLimitIsZeroRowsNilErrorTotalUnaffected(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	for _, limit := range []int{0, -1, -50} {
		sts, total, err := ss.List(ctx, store.StationFilter{Limit: limit})
		if err != nil {
			t.Errorf("List(Limit=%d) error = %v, want nil", limit, err)
			continue
		}
		if len(sts) != 0 {
			t.Errorf("List(Limit=%d) returned %d rows, want 0", limit, len(sts))
		}
		if total != 4 {
			t.Errorf("List(Limit=%d) total = %d, want 4 (unaffected by the clamp)", limit, total)
		}
	}

	// The same rule must hold on the search branch, since it is clamped by
	// the same shared helper as the unfiltered branch.
	sts, total, err := ss.List(ctx, store.StationFilter{Search: "bristol", Limit: -1})
	if err != nil {
		t.Fatalf("List(search, Limit=-1) error = %v, want nil", err)
	}
	if len(sts) != 0 {
		t.Fatalf("List(search, Limit=-1) returned %d rows, want 0", len(sts))
	}
	if total != 2 {
		t.Fatalf("List(search, Limit=-1) total = %d, want 2 (unaffected by the clamp)", total)
	}
}

func TestStationListReflectsLastReadingForOnlineDerivation(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seedStations(t, pool)
	ss := postgres.NewStationStore(pool)

	fresh := time.Now().UTC().Add(-2 * time.Minute)
	if _, err := pool.Exec(ctx,
		"UPDATE stations SET last_reading = $1 WHERE station_id = 1000", fresh); err != nil {
		t.Fatalf("setting last_reading: %v", err)
	}

	// The store returns IsActive and LastReading and does NOT derive "online".
	// That keeps the freshness window in config, keeps the DTO test
	// deterministic without freezing now(), and stops the poll rate leaking
	// into persistence.
	got, err := ss.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.IsActive {
		t.Error("IsActive = false, want true")
	}
	if got.LastReading == nil {
		t.Fatal("LastReading is nil")
	}
	if got.LastReading.Sub(fresh).Abs() > time.Second {
		t.Errorf("LastReading = %v, want ~%v", got.LastReading, fresh)
	}

	inactive, err := ss.Get(ctx, 1002)
	if err != nil {
		t.Fatalf("Get 1002: %v", err)
	}
	if inactive.IsActive {
		t.Error("station 1002 IsActive = true, want false")
	}
	if inactive.LastReading != nil {
		t.Errorf("station 1002 LastReading = %v, want nil", inactive.LastReading)
	}
}
