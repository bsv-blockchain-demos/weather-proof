package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// conformanceBase is the fixed instant every case builds its timestamps from.
// Fixed rather than time.Now so a failure message is the same on every run.
var conformanceBase = time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

// RunStoreConformance asserts the behavior that EVERY store.Store implementation
// must have, and it is meant to be called twice: once over internal/store/fake
// with no database, once over internal/store/postgres behind Fresh.
//
// WHY THIS IS NOT OPTIONAL. The read-API plan's whole test suite runs against
// the fake. Any behavior where the fake and the SQL disagree is a place where a
// handler test passes while proving nothing, and there is no other detector for
// that class of defect — a reviewer would have to read both implementations side
// by side and notice. Four such divergences existed before this suite: an
// ignored search filter, an unconditional last_temp assignment, a station row
// invented by the fake, and a missing ErrConflict on a duplicate id.
//
// WHAT IT DELIBERATELY OMITS. Full-text RANKING (ts_rank has no honest
// in-memory analog), the order of UPDATE … RETURNING rows (PostgreSQL does not
// define it — see ClaimPendingResultIsASetNeverASequence below for the
// assertion shape this forces), and everything concurrent: the fake runs
// every method under one mutex, so a concurrency assertion here would pass
// trivially and report a guarantee that does not exist. That is why the claim
// race lives only in internal/store/postgres. Also omitted: whether
// PendingDeposits' created_at is the PRIMARY sort key rather than suffix —
// that needs two deposits with genuinely different creation times, which the
// fake's frozen clock cannot produce through this package's mk signature
// (every fake insert shares one instant), so that specific regression is
// covered only by each implementation's own test suite, not this one.
//
// Station search MEMBERSHIP is also only PARTLY covered, and this note used
// to claim (wrongly, MEASURED) that only ranking was left unimitated for
// search. StationSearchSplitsOnOneParsedValue below pins exact-id lookup and
// whole-single-word matching — the subset where fake/agree — but that
// fixture uses eight whole, lowercase, unstemmed words that happen to agree,
// which is exactly the kind of blind spot this project has hit repeatedly:
// see StationSearchMembershipIsWholeWordNotSubstring below, which pins the
// PREFIX family instead (six substrings of those same words, all correctly
// matching NOTHING on both subjects after fake/fake.go's stationMatches
// switched from strings.Contains to whole-token matching). Three families
// still disagree and are NOT exercised here, because the fake and Postgres
// would return genuinely different answers for the identical input rather
// than merely different rankings — see fake/fake.go's ListStations doc
// comment for the full account before writing a search test against any of
// them: STEMMED word forms, MULTI-WORD queries (implicit AND, or a quoted
// phrase), and websearch_to_tsquery's own OPERATORS (`or`, a leading `-`
// exclusion, quoting).
//
// mk is called once per subtest and must return an EMPTY store. No subtest calls
// t.Parallel(), because the Postgres implementation of mk shares one schema.
func RunStoreConformance(t *testing.T, name string, mk func(t *testing.T) store.Store) {
	t.Helper()
	t.Run(name+"/InsertDedupeIsNotAnErrorButADuplicateIDIs", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		obs := conformanceBase

		inserted, err := s.Records.Insert(ctx, newConformanceRecord("rec-a", 1000, obs, 18, "Clear"))
		if err != nil || !inserted {
			t.Fatalf("first Insert = (%v, %v), want (true, nil)", inserted, err)
		}

		// Same (station_id, observation_time): the intended steady state.
		inserted, err = s.Records.Insert(ctx, newConformanceRecord("rec-b", 1000, obs, 18, "Clear"))
		if err != nil {
			t.Fatalf("duplicate observation Insert error = %v, want nil", err)
		}
		if inserted {
			t.Error("duplicate observation Insert = true, want false")
		}

		// Same id, different dedupe key: a genuine surprise.
		_, conflictErr := s.Records.Insert(ctx,
			newConformanceRecord("rec-a", 2000, obs.Add(time.Hour), 18, "Clear"))
		if !errors.Is(conflictErr, store.ErrConflict) {
			t.Errorf("duplicate id Insert = %v, want store.ErrConflict", conflictErr)
		}
	})

	t.Run(name+"/GetAndTxIDExistsAreTotalOverHostileInput", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		for _, id := range []string{
			"", "0", "not-a-uuid", "'; DROP TABLE weather_records; --", "\x00", "a\x00b",
		} {
			if _, err := s.Records.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Get(%q) error = %v, want store.ErrNotFound", id, err)
			}
			ok, err := s.Records.TxIDExists(ctx, id)
			if err != nil || ok {
				t.Errorf("TxIDExists(%q) = (%v, %v), want (false, nil)", id, ok, err)
			}
		}
	})

	t.Run(name+"/StationSearchSplitsOnOneParsedValue", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		stations := []store.Station{
			{StationID: 1000, Name: "Harbor Mast", Location: "Bristol Docks", IsActive: true},
			{StationID: 1001, Name: "Clifton Ridge", Location: "Bristol Downs", IsActive: true},
			{StationID: 2000, Name: "Kelvin Yard", Location: "Glasgow", IsActive: true},
		}
		for _, st := range stations {
			if err := s.Stations.Upsert(ctx, st); err != nil {
				t.Fatalf("Upsert %d: %v", st.StationID, err)
			}
		}

		cases := []struct {
			search string
			want   int64
			reason string
		}{
			{"", 3, "no filter"},
			{"1001", 1, "an all-digits search is an EXACT station_id lookup"},
			{"424242", 0, "a numeric miss is an empty page, never an error"},
			{"0", 0, "\"0\" parses, so it looks up station 0 and finds nothing"},
			{"bristol", 2, "a text search matches location, case-insensitively"},
			{"kelvin", 1, "a text search matches name too"},
			{"reykjavik", 0, "a text miss is an empty page"},
			{"\x00nul", 0, "a NUL byte cannot be bound at all: empty page, no error"},
		}
		for _, c := range cases {
			sts, total, err := s.Stations.List(ctx, store.StationFilter{Search: c.search, Limit: 50})
			if err != nil {
				t.Errorf("List(search=%q) error = %v, want nil (%s)", c.search, err, c.reason)
				continue
			}
			if total != c.want {
				t.Errorf("List(search=%q) total = %d, want %d (%s)", c.search, total, c.want, c.reason)
			}
			if int64(len(sts)) != c.want {
				t.Errorf("List(search=%q) rows = %d, want %d (%s)", c.search, len(sts), c.want, c.reason)
			}
		}
	})

	// StationSearchSplitsOnOneParsedValue's own fixture is, by its nature, the
	// blind spot this suite must not repeat: every one of its text cases is a
	// whole, lowercase, unstemmed single word, which is exactly the narrow
	// subset where the fake's substring-based search used to happen to agree
	// with Postgres's websearch_to_tsquery. This subtest instead pins the
	// PREFIX family specifically, MEASURED directly against Postgres
	// (`SELECT ... WHERE search_tsv @@ websearch_to_tsquery('english', $1)`):
	// every one of these six prefixes returns zero rows from a real server,
	// because websearch_to_tsquery has no prefix operator for plain input —
	// it treats "brist" as the complete, unstemmable lexeme "brist," which
	// matches no document containing "Bristol." Before this fix,
	// strings.Contains made the fake answer YES for every one of them (a
	// live search box, "search-as-you-type" false positive), which is
	// membership disagreement, not a ranking difference, and both of this
	// suite's doc comments used to describe it as the latter.
	t.Run(name+"/StationSearchMembershipIsWholeWordNotSubstring", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		stations := []store.Station{
			{StationID: 1000, Name: "Harbor Mast", Location: "Bristol Docks", IsActive: true},
			{StationID: 1001, Name: "Clifton Ridge", Location: "Bristol Downs", IsActive: true},
			{StationID: 2000, Name: "Kelvin Yard", Location: "Glasgow", IsActive: true},
		}
		for _, st := range stations {
			if err := s.Stations.Upsert(ctx, st); err != nil {
				t.Fatalf("Upsert %d: %v", st.StationID, err)
			}
		}

		cases := []struct {
			search string
			want   int64
			reason string
		}{
			{"brist", 0, "a prefix of \"Bristol\" is not a whole word"},
			{"bristo", 0, "one letter short of a whole word is still not a whole word"},
			{"harb", 0, "a prefix of \"Harbor\" is not a whole word"},
			{"kelv", 0, "a prefix of \"Kelvin\" is not a whole word"},
			{"glas", 0, "a prefix of \"Glasgow\" is not a whole word"},
			{"b", 0, "a single leading letter is not a whole word"},
			{"Bristol", 2, "a whole word matches case-insensitively, unlike a bare prefix"},
			{"docks", 1, "a whole word inside Location matches even though it is not the first word"},
		}
		for _, c := range cases {
			sts, total, err := s.Stations.List(ctx, store.StationFilter{Search: c.search, Limit: 50})
			if err != nil {
				t.Errorf("List(search=%q) error = %v, want nil (%s)", c.search, err, c.reason)
				continue
			}
			if total != c.want {
				t.Errorf("List(search=%q) total = %d, want %d (%s)", c.search, total, c.want, c.reason)
			}
			if int64(len(sts)) != c.want {
				t.Errorf("List(search=%q) rows = %d, want %d (%s)", c.search, len(sts), c.want, c.reason)
			}
		}
	})

	t.Run(name+"/CompleteNeverInventsAStation", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if _, err := s.Records.Insert(ctx,
			newConformanceRecord("orphan", 4242, conformanceBase, 18, "Clear")); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
		if err != nil {
			t.Fatalf("ClaimPending: %v", err)
		}
		if len(claimed) != 1 {
			t.Fatalf("claimed %d rows, want 1", len(claimed))
		}
		if _, completeErr := s.Records.Complete(ctx, "orphan-tx",
			[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
			t.Fatalf("Complete with no stations row: %v", completeErr)
		}
		if _, getErr := s.Stations.Get(ctx, 4242); !errors.Is(getErr, store.ErrNotFound) {
			t.Fatalf("station 4242 = %v, want store.ErrNotFound: the station bump is an "+
				"UPDATE and must not create a row the poller never reported", getErr)
		}
	})

	t.Run(name+"/CompleteAdvancesStationReadingsOnlyForwards", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 1000, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		// The NEWER reading is published first, then an OLDER one. last_temp and
		// last_conditions must still describe the newer reading: assigning them
		// unconditionally would leave last_reading fresh and the temperature
		// stale, which no consumer could detect.
		batches := []struct {
			id   string
			ts   time.Time
			temp int64
			cond string
			txid string
		}{
			{"newer", conformanceBase.Add(time.Hour), 21, "Clear", "tx-newer"},
			{"older", conformanceBase, 4, "Snow", "tx-older"},
		}
		for _, b := range batches {
			if _, err := s.Records.Insert(ctx,
				newConformanceRecord(b.id, 1000, b.ts, b.temp, b.cond)); err != nil {
				t.Fatalf("Insert %s: %v", b.id, err)
			}
			claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
			if err != nil {
				t.Fatalf("ClaimPending %s: %v", b.id, err)
			}
			if len(claimed) != 1 {
				t.Fatalf("claimed %d rows for %s, want 1", len(claimed), b.id)
			}
			if _, completeErr := s.Records.Complete(ctx, b.txid,
				[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
				t.Fatalf("Complete %s: %v", b.id, completeErr)
			}
		}

		st, err := s.Stations.Get(ctx, 1000)
		if err != nil {
			t.Fatalf("station Get: %v", err)
		}
		if st.TxRecords != 2 {
			t.Errorf("tx_records = %d, want 2: every completed record counts", st.TxRecords)
		}
		if st.LastReading == nil || !st.LastReading.Equal(conformanceBase.Add(time.Hour)) {
			t.Errorf("last_reading = %v, want the newer %v", st.LastReading, conformanceBase.Add(time.Hour))
		}
		if st.LastTemp == nil || *st.LastTemp != 21 {
			t.Errorf("last_temp = %v, want the newer reading's 21", st.LastTemp)
		}
		if st.LastConditions != "Clear" {
			t.Errorf("last_conditions = %q, want Clear", st.LastConditions)
		}
	})

	t.Run(name+"/StatsAndSnapshotStartEmpty", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		stats, err := s.Stations.Stats(ctx)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if stats != (store.Stats{}) {
			t.Errorf("Stats on an empty store = %+v, want the zero value", stats)
		}
		if stats.TotalDataPoints() != 0 {
			t.Errorf("TotalDataPoints = %d, want 0", stats.TotalDataPoints())
		}
		snap, err := s.Records.Snapshot(ctx)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snap != (store.Snapshot{}) {
			t.Errorf("Snapshot on an empty store = %+v, want the zero value", snap)
		}
	})

	t.Run(name+"/DepositAndPreflightLifecycle", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		d := store.Deposit{Suffix: "s1", Prefix: "p1", Address: "addr", LockingScript: "76a9"}
		if err := s.Deposits.NewDeposit(ctx, d); err != nil {
			t.Fatalf("NewDeposit: %v", err)
		}
		if dupErr := s.Deposits.NewDeposit(ctx, d); !errors.Is(dupErr, store.ErrConflict) {
			t.Errorf("duplicate NewDeposit = %v, want store.ErrConflict", dupErr)
		}
		pending, err := s.Deposits.PendingDeposits(ctx)
		if err != nil {
			t.Fatalf("PendingDeposits: %v", err)
		}
		if len(pending) != 1 {
			t.Fatalf("pending = %d, want 1", len(pending))
		}
		if missErr := s.Deposits.MarkInternalized(ctx, "nope", "tx", 0, 1); !errors.Is(missErr, store.ErrNotFound) {
			t.Errorf("MarkInternalized for an unknown suffix = %v, want store.ErrNotFound", missErr)
		}
		if markErr := s.Deposits.MarkInternalized(ctx, "s1", "tx", 1, 250000); markErr != nil {
			t.Fatalf("MarkInternalized: %v", markErr)
		}
		pending, err = s.Deposits.PendingDeposits(ctx)
		if err != nil {
			t.Fatalf("PendingDeposits after internalize: %v", err)
		}
		if len(pending) != 0 {
			t.Fatalf("pending after internalize = %d, want 0", len(pending))
		}

		ok, err := s.Preflight.PreflightOK(ctx, "fp")
		if err != nil || ok {
			t.Fatalf("PreflightOK before = (%v, %v), want (false, nil)", ok, err)
		}
		if recordErr := s.Preflight.RecordPreflight(ctx, "fp"); recordErr != nil {
			t.Fatalf("RecordPreflight: %v", recordErr)
		}
		// Twice, because it runs on every boot with the same fingerprint.
		if secondErr := s.Preflight.RecordPreflight(ctx, "fp"); secondErr != nil {
			t.Fatalf("second RecordPreflight: %v", secondErr)
		}
		ok, err = s.Preflight.PreflightOK(ctx, "fp")
		if err != nil || !ok {
			t.Fatalf("PreflightOK after = (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run(name+"/PingSucceeds", func(t *testing.T) {
		s := mk(t)
		if err := s.Health.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})

	// The subtests below close specific historical divergences that predate
	// this suite and are not exercised above: non-positive limits across
	// EVERY limit-taking method (ClaimPending panicked on a negative n in the
	// fake; ReapExpired, Requeue and ReconcileCandidates each returned every
	// eligible row instead of none), the exact tie boundary in Complete's
	// station bump, ClaimPending's return-order contract, tiebreak ordering
	// under a genuine tie, and pointer aliasing on returned values. Each is
	// written so it would have failed before its corresponding fix landed —
	// see fake.go's clampLimit, records.go's bumpStationsSQL and clampLimit,
	// and fake.go's cloneRecord/cloneStation.

	t.Run(name+"/NonPositiveLimitIsZeroEverywhereALimitAppears", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6100, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		ids := []string{"clamp-a", "clamp-b", "clamp-c"}
		for i, id := range ids {
			ts := conformanceBase.Add(time.Duration(i) * time.Minute)
			if _, err := s.Records.Insert(ctx, newConformanceRecord(id, 6100, ts, 10, "-")); err != nil {
				t.Fatalf("Insert %s: %v", id, err)
			}
		}

		// ClaimPending: PANICKED on a negative n in the fake, and but for the
		// panic its own loop guard could never equal a negative n, so it
		// would have claimed every pending row instead of none.
		for _, n := range []int{0, -1} {
			recs, err := s.Records.ClaimPending(ctx, n, uuid.Must(uuid.NewV7()))
			if err != nil {
				t.Errorf("ClaimPending(n=%d) error = %v, want nil", n, err)
			}
			if len(recs) != 0 {
				t.Errorf("ClaimPending(n=%d) claimed %d rows, want 0", n, len(recs))
			}
		}

		// RecordStore.List and StationStore.List: Total must still report the
		// full unpaged count even though the page itself is clamped to empty.
		for _, n := range []int{0, -1} {
			recs, total, err := s.Records.List(ctx, store.ListFilter{Limit: n})
			if err != nil {
				t.Errorf("Records.List(Limit=%d) error = %v, want nil", n, err)
			}
			if len(recs) != 0 {
				t.Errorf("Records.List(Limit=%d) returned %d rows, want 0", n, len(recs))
			}
			if total != int64(len(ids)) {
				t.Errorf("Records.List(Limit=%d) total = %d, want %d (unaffected by Limit)", n, total, len(ids))
			}
		}
		for _, n := range []int{0, -1} {
			sts, total, err := s.Stations.List(ctx, store.StationFilter{Limit: n})
			if err != nil {
				t.Errorf("Stations.List(Limit=%d) error = %v, want nil", n, err)
			}
			if len(sts) != 0 {
				t.Errorf("Stations.List(Limit=%d) returned %d rows, want 0", n, len(sts))
			}
			if total != 1 {
				t.Errorf("Stations.List(Limit=%d) total = %d, want 1 (unaffected by Limit)", n, total)
			}
		}

		// Claim everything for real, so ReapExpired has processing rows a
		// broken clamp could wrongly reap.
		claimed, err := s.Records.ClaimPending(ctx, len(ids), uuid.Must(uuid.NewV7()))
		if err != nil || len(claimed) != len(ids) {
			t.Fatalf("setup ClaimPending = (%d claimed, %v), want (%d, nil)", len(claimed), err, len(ids))
		}

		// ReapExpired: a NEGATIVE lease puts the cutoff in the future, so
		// every claimed row is unambiguously stale in BOTH implementations
		// without depending on real-clock drift between the claim and the
		// reap — which would make this fixture flaky against the fake's
		// frozen clock.
		for _, n := range []int{0, -1} {
			reaped, err := s.Records.ReapExpired(ctx, -time.Hour, n)
			if err != nil {
				t.Errorf("ReapExpired(limit=%d) error = %v, want nil", n, err)
			}
			if len(reaped) != 0 {
				t.Errorf("ReapExpired(limit=%d) reaped %d rows, want 0", n, len(reaped))
			}
		}
		for _, id := range ids {
			rec, err := s.Records.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get %s: %v", id, err)
			}
			if rec.Status != store.StatusProcessing {
				t.Errorf("%s status = %q after a clamped ReapExpired, want untouched processing", id, rec.Status)
			}
		}

		// Requeue: same rule, exercised on both DryRun and the real write.
		for _, dryRun := range []bool{true, false} {
			for _, n := range []int{0, -1} {
				f := store.RequeueFilter{
					Status: store.StatusProcessing, Since: -time.Hour, Limit: n, DryRun: dryRun,
				}
				count, err := s.Records.Requeue(ctx, f)
				if err != nil {
					t.Errorf("Requeue(DryRun=%v, Limit=%d) error = %v, want nil", dryRun, n, err)
				}
				if count != 0 {
					t.Errorf("Requeue(DryRun=%v, Limit=%d) = %d, want 0", dryRun, n, count)
				}
			}
		}
		for _, id := range ids {
			rec, err := s.Records.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get %s: %v", id, err)
			}
			if rec.Status != store.StatusProcessing || rec.AdoptRequired {
				t.Errorf("%s = (status=%q, adopt=%v) after a clamped Requeue, want untouched (processing, false)",
					id, rec.Status, rec.AdoptRequired)
			}
		}

		// Complete every row, so ReconcileCandidates has completed-but-unmined
		// rows a broken clamp could wrongly return.
		pubs := make([]store.Publication, 0, len(ids))
		for _, id := range ids {
			pubs = append(pubs, store.Publication{RecordID: id, OutputIndex: 0})
		}
		if _, err := s.Records.Complete(ctx, "clamp-tx", pubs); err != nil {
			t.Fatalf("Complete: %v", err)
		}

		// ReconcileCandidates: same rule, same future-cutoff technique.
		for _, n := range []int{0, -1} {
			candidates, err := s.Records.ReconcileCandidates(ctx, -time.Hour, n)
			if err != nil {
				t.Errorf("ReconcileCandidates(limit=%d) error = %v, want nil", n, err)
			}
			if len(candidates) != 0 {
				t.Errorf("ReconcileCandidates(limit=%d) returned %d rows, want 0", n, len(candidates))
			}
		}
	})

	t.Run(name+"/CompleteTieDoesNotAdvanceStationReading", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6200, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		// The first reading establishes last_reading at exactly conformanceBase.
		first := store.NewRecord{
			ID: "tie-first", StationID: 6200,
			Timestamp: conformanceBase, ObservationTime: conformanceBase,
			Data: weather.WeatherData{AirTemperature: 20, Conditions: "Clear"},
		}
		if _, err := s.Records.Insert(ctx, first); err != nil {
			t.Fatalf("Insert tie-first: %v", err)
		}
		claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimPending tie-first = (%d, %v), want (1, nil)", len(claimed), err)
		}
		if _, completeErr := s.Records.Complete(ctx, "tx-tie-first",
			[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
			t.Fatalf("Complete tie-first: %v", completeErr)
		}

		// The second reading shares the EXACT same timestamp — a different
		// observation_time keeps Insert's dedupe from swallowing it, since
		// that dedupe key is deliberately separate from the tie under test
		// here. The guard is strict (`u.ts > s.last_reading` in SQL,
		// `LastReading.Before` in the fake), so a tie must NOT advance
		// last_temp/last_conditions — nothing else in this suite pins that
		// boundary, and mutating `>` to `>=` (or `.Before` to `!.After`)
		// would leave every other case green.
		second := store.NewRecord{
			ID: "tie-second", StationID: 6200,
			Timestamp: conformanceBase, ObservationTime: conformanceBase.Add(time.Minute),
			Data: weather.WeatherData{AirTemperature: 99, Conditions: "Tornado"},
		}
		if _, insertErr := s.Records.Insert(ctx, second); insertErr != nil {
			t.Fatalf("Insert tie-second: %v", insertErr)
		}
		claimed, err = s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimPending tie-second = (%d, %v), want (1, nil)", len(claimed), err)
		}
		if _, completeErr := s.Records.Complete(ctx, "tx-tie-second",
			[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
			t.Fatalf("Complete tie-second: %v", completeErr)
		}

		st, err := s.Stations.Get(ctx, 6200)
		if err != nil {
			t.Fatalf("Stations.Get: %v", err)
		}
		if st.TxRecords != 2 {
			t.Errorf("tx_records = %d, want 2: every completed record counts even when it loses the tie", st.TxRecords)
		}
		if st.LastTemp == nil || *st.LastTemp != 20 {
			lastTempDisplay := "<nil>"
			if st.LastTemp != nil {
				lastTempDisplay = fmt.Sprintf("%v", *st.LastTemp)
			}
			t.Errorf("last_temp = %s, want the FIRST reading's 20: a tied timestamp must not advance it",
				lastTempDisplay)
		}
		if st.LastConditions != "Clear" {
			t.Errorf("last_conditions = %q, want Clear: a tied timestamp must not advance it", st.LastConditions)
		}
	})

	t.Run(name+"/ClaimPendingResultIsASetNeverASequence", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6300, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		ids := []string{"set-a", "set-b", "set-c"}
		for i, id := range ids {
			ts := conformanceBase.Add(time.Duration(i) * time.Minute)
			if _, err := s.Records.Insert(ctx, newConformanceRecord(id, 6300, ts, 10, "-")); err != nil {
				t.Fatalf("Insert %s: %v", id, err)
			}
		}

		// Measured: Postgres's UPDATE … RETURNING emits rows in ModifyTable
		// processing order, which is not guaranteed to match the subquery's
		// own ORDER BY — interfaces.go documents no return-order contract for
		// ClaimPending beyond atomicity of the claim itself. A conformance
		// assertion that compared this result as a SEQUENCE would pass
		// against the fake's deterministic CreatedAt/ID sort and then flake
		// against Postgres. Comparing as a set is therefore the only
		// comparison this method's contract supports.
		claimed, err := s.Records.ClaimPending(ctx, len(ids), uuid.Must(uuid.NewV7()))
		if err != nil {
			t.Fatalf("ClaimPending: %v", err)
		}
		if len(claimed) != len(ids) {
			t.Fatalf("claimed %d rows, want %d", len(claimed), len(ids))
		}
		got := make(map[string]bool, len(claimed))
		for _, r := range claimed {
			got[r.ID] = true
			if r.Status != store.StatusProcessing {
				t.Errorf("claimed record %s status = %q, want processing", r.ID, r.Status)
			}
			// ClaimedAt is asserted non-nil, never for an exact value: the
			// fake's clock is a fixed constant and Postgres's is now(), so
			// only their SHAPE (set) can agree, never their timestamps.
			if r.ClaimedAt == nil {
				t.Errorf("claimed record %s ClaimedAt is nil, want set", r.ID)
			}
		}
		for _, id := range ids {
			if !got[id] {
				t.Errorf("claimed set is missing %s: got %v", id, claimed)
			}
		}
	})

	t.Run(name+"/ReapExpiredBreaksATiedClaimedAtByIDAscending", func(t *testing.T) {
		ctx := context.Background()

		// Repeated as INDEPENDENT trials, not asserted once. MEASURED: when
		// the id tiebreak is dropped, sort.Slice's comparator reports every
		// pair "equal", so the result falls through to whatever order this
		// call's iteration over the fake's records map happened to start
		// at — and Go deliberately RANDOMIZES that start point per
		// iteration. A single trial was observed to catch a dropped
		// tiebreak only about half the time by luck; enough independent
		// trials make that miss probability negligible without weakening
		// what is asserted (the id-ASC tiebreak is what makes WHICH two rows
		// a limit-truncated reap chooses deterministic, not merely likely).
		//
		// Each trial gets its OWN store from mk(t): ClaimPending and
		// ReapExpired are both global, with no station filter, so a shared
		// store would let one trial's rows outlive it (a reap left one row
		// unclaimed on purpose, to force the tiebreak choice) and contaminate
		// the next trial's claim — MEASURED doing exactly that when trials
		// shared one store.
		const trials = 8
		for trial := range trials {
			t.Run(fmt.Sprintf("trial%d", trial), func(t *testing.T) {
				s := mk(t)
				const stationID = 6400
				if err := s.Stations.Upsert(ctx, store.Station{StationID: stationID, IsActive: true}); err != nil {
					t.Fatalf("Upsert: %v", err)
				}
				// Deliberately out of ID order at insert time, so a passing
				// result cannot be an accident of insertion order.
				ids := []string{"reap-c", "reap-a", "reap-b"}
				for i, id := range ids {
					ts := conformanceBase.Add(time.Duration(i) * time.Minute)
					if _, err := s.Records.Insert(ctx, newConformanceRecord(id, stationID, ts, 10, "-")); err != nil {
						t.Fatalf("Insert %s: %v", id, err)
					}
				}
				// One ClaimPending call stamps every row it touches with a
				// SINGLE claimed_at (Postgres: one UPDATE's now(); the fake:
				// one s.Now() call), so all three tie. The id-ASC tiebreak
				// inside reapExpiredSQL's subquery is what makes WHICH two
				// rows a limit-truncated reap chooses deterministic — but the
				// CHOSEN SET is all it pins, not the sequence the outer
				// UPDATE … RETURNING happens to emit them in. MEASURED: on a
				// freshly loaded table with no prior requeuing at all,
				// Postgres returned the second-lowest id before the lowest —
				// the identical ModifyTable-processing-order hazard
				// ClaimPending's own doc comment warns about, just triggered
				// here without needing a shuffled requeue first. A sequence
				// assertion here would have passed on the fake and flaked on
				// Postgres, exactly per that warning generalized beyond
				// ClaimPending.
				if _, err := s.Records.ClaimPending(ctx, len(ids), uuid.Must(uuid.NewV7())); err != nil {
					t.Fatalf("ClaimPending: %v", err)
				}

				reaped, err := s.Records.ReapExpired(ctx, -time.Hour, 2)
				if err != nil {
					t.Fatalf("ReapExpired: %v", err)
				}
				if len(reaped) != 2 {
					t.Fatalf("reaped %d rows, want 2", len(reaped))
				}
				got := map[string]bool{reaped[0].ID: true, reaped[1].ID: true}
				want := []string{"reap-a", "reap-b"} // the two LOWEST ids, as a set
				for _, id := range want {
					if !got[id] {
						t.Errorf("ReapExpired(limit=2) under a tied claimed_at chose %v, want the set %v "+
							"(id ASC tiebreak)", reaped, want)
					}
				}
			})
		}
	})

	t.Run(name+"/ReconcileCandidatesBreaksATiedProcessedAtByIDAscending", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6500, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		ids := []string{"rec-c", "rec-a", "rec-b"}
		for i, id := range ids {
			ts := conformanceBase.Add(time.Duration(i) * time.Minute)
			if _, err := s.Records.Insert(ctx, newConformanceRecord(id, 6500, ts, 10, "-")); err != nil {
				t.Fatalf("Insert %s: %v", id, err)
			}
		}
		claimed, err := s.Records.ClaimPending(ctx, len(ids), uuid.Must(uuid.NewV7()))
		if err != nil || len(claimed) != len(ids) {
			t.Fatalf("ClaimPending = (%d, %v), want (%d, nil)", len(claimed), err, len(ids))
		}
		pubs := make([]store.Publication, 0, len(claimed))
		for _, r := range claimed {
			pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: 0})
		}
		// One Complete call stamps every row it touches with a SINGLE
		// processed_at, so all three tie — same reasoning as the reap case
		// above.
		if _, completeErr := s.Records.Complete(ctx, "rec-tx", pubs); completeErr != nil {
			t.Fatalf("Complete: %v", completeErr)
		}

		candidates, err := s.Records.ReconcileCandidates(ctx, -time.Hour, 2)
		if err != nil {
			t.Fatalf("ReconcileCandidates: %v", err)
		}
		if len(candidates) != 2 {
			t.Fatalf("got %d candidates, want 2", len(candidates))
		}
		got := []string{candidates[0].ID, candidates[1].ID}
		want := []string{"rec-a", "rec-b"}
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("ReconcileCandidates(limit=2) under a tied processed_at = %v, want %v (id ASC tiebreak)", got, want)
		}
	})

	t.Run(name+"/NegativeOffsetClampsToZeroForBothListMethods", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6600, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6601, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		ids := []string{"off-a", "off-b", "off-c"}
		for i, id := range ids {
			ts := conformanceBase.Add(time.Duration(i) * time.Minute)
			if _, err := s.Records.Insert(ctx, newConformanceRecord(id, 6600, ts, 10, "-")); err != nil {
				t.Fatalf("Insert %s: %v", id, err)
			}
		}

		// A negative Offset must behave EXACTLY like Offset: 0 — clamped, never
		// passed through to a driver runtime error (Postgres: SQLSTATE 2201X,
		// invalid_row_count_in_result_offset_clause) and never reaching an
		// unguarded slice expression (the fake: match[f.Offset:end] panics for a
		// negative f.Offset). interfaces.go's ListFilter.Offset and
		// StationFilter.Offset doc comments state this as the rule.
		zeroRecs, zeroTotal, zeroErr := s.Records.List(ctx, store.ListFilter{Limit: 50, Offset: 0})
		if zeroErr != nil {
			t.Fatalf("Records.List(Offset=0) error = %v, want nil", zeroErr)
		}
		zeroSts, zeroSTotal, zeroSErr := s.Stations.List(ctx, store.StationFilter{Limit: 50, Offset: 0})
		if zeroSErr != nil {
			t.Fatalf("Stations.List(Offset=0) error = %v, want nil", zeroSErr)
		}

		for _, negOffset := range []int{-1, -20, -1000000} {
			recs, total, err := s.Records.List(ctx, store.ListFilter{Limit: 50, Offset: negOffset})
			if err != nil {
				t.Errorf("Records.List(Offset=%d) error = %v, want nil", negOffset, err)
			}
			if total != zeroTotal || len(recs) != len(zeroRecs) {
				t.Errorf("Records.List(Offset=%d) = (%d recs, total=%d), want same as Offset=0 (%d recs, total=%d)",
					negOffset, len(recs), total, len(zeroRecs), zeroTotal)
			}

			sts, stotal, serr := s.Stations.List(ctx, store.StationFilter{Limit: 50, Offset: negOffset})
			if serr != nil {
				t.Errorf("Stations.List(Offset=%d) error = %v, want nil", negOffset, serr)
			}
			if stotal != zeroSTotal || len(sts) != len(zeroSts) {
				t.Errorf("Stations.List(Offset=%d) = (%d stations, total=%d), want same as Offset=0 (%d stations, total=%d)",
					negOffset, len(sts), stotal, len(zeroSts), zeroSTotal)
			}
		}
	})

	t.Run(name+"/RequeueSetsTheOperatorErrorMessage", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6700, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if _, err := s.Records.Insert(ctx,
			newConformanceRecord("requeue-err", 6700, conformanceBase, 10, "-")); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimPending = (%d, %v), want (1, nil)", len(claimed), err)
		}
		if failErr := s.Records.FailPermanent(ctx, []string{claimed[0].ID}, "boom"); failErr != nil {
			t.Fatalf("FailPermanent: %v", failErr)
		}

		// Since: -time.Hour puts the cutoff an hour in the FUTURE relative to
		// whichever clock this subject uses (the fake's frozen clock, or
		// Postgres's real now()), so conformanceBase — fixed in April 2026 —
		// is unambiguously older than it on both subjects without depending
		// on real-clock drift between insert and requeue. The same technique
		// NonPositiveLimitIsZeroEverywhereALimitAppears uses.
		n, requeueErr := s.Records.Requeue(ctx, store.RequeueFilter{
			Status: store.StatusFailed, Since: -time.Hour, Limit: 10,
		})
		if requeueErr != nil {
			t.Fatalf("Requeue: %v", requeueErr)
		}
		if n != 1 {
			t.Fatalf("Requeue = %d, want 1", n)
		}

		rec, getErr := s.Records.Get(ctx, "requeue-err")
		if getErr != nil {
			t.Fatalf("Get: %v", getErr)
		}
		const wantErr = "requeued by operator"
		if rec.Error == nil || *rec.Error != wantErr {
			got := "<nil>"
			if rec.Error != nil {
				got = *rec.Error
			}
			t.Errorf("Error after Requeue = %q, want %q: an operator-driven requeue must overwrite whatever "+
				"reason a prior FailPermanent left, so the error column reflects WHY the row is pending again",
				got, wantErr)
		}
	})

	t.Run(name+"/RequeueRefusesACompletedStatusFilter", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6800, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if _, err := s.Records.Insert(ctx,
			newConformanceRecord("requeue-completed", 6800, conformanceBase, 10, "-")); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimPending = (%d, %v), want (1, nil)", len(claimed), err)
		}
		if _, completeErr := s.Records.Complete(ctx, "requeue-completed-tx",
			[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 3}}); completeErr != nil {
			t.Fatalf("Complete: %v", completeErr)
		}
		before, err := s.Records.Get(ctx, "requeue-completed")
		if err != nil {
			t.Fatalf("Get before Requeue: %v", err)
		}

		// RequeueFilter.Status: completed is a STATE-MACHINE HOLE, not a
		// filter that legitimately matches nothing: un-publishing a completed
		// row back to pending, without reversing app_stats or the station
		// counters that Complete already advanced, makes the row
		// re-claimable and lets a second Complete double-count one reading.
		// Both implementations must refuse it — see RequeueFilter.Status's
		// doc comment for the chosen (0, nil) no-op semantic — for BOTH
		// DryRun and the real write, exactly as a non-positive Limit is
		// refused before either path runs.
		for _, dryRun := range []bool{true, false} {
			n, requeueErr := s.Records.Requeue(ctx, store.RequeueFilter{
				Status: store.StatusCompleted, Since: -time.Hour, Limit: 10, DryRun: dryRun,
			})
			if requeueErr != nil {
				t.Errorf("Requeue(Status=completed, DryRun=%v) error = %v, want nil", dryRun, requeueErr)
			}
			if n != 0 {
				t.Errorf("Requeue(Status=completed, DryRun=%v) = %d, want 0", dryRun, n)
			}
		}

		after, err := s.Records.Get(ctx, "requeue-completed")
		if err != nil {
			t.Fatalf("Get after Requeue: %v", err)
		}
		if after.Status != store.StatusCompleted {
			t.Errorf("status after a refused Requeue = %q, want unchanged %q", after.Status, store.StatusCompleted)
		}
		if after.AdoptRequired {
			t.Error("adopt_required after a refused Requeue = true, want unchanged false")
		}
		if after.TxID == nil || before.TxID == nil || *after.TxID != *before.TxID {
			t.Errorf("txid after a refused Requeue = %v, want unchanged %v", after.TxID, before.TxID)
		}
		if after.OutputIndex == nil || before.OutputIndex == nil || *after.OutputIndex != *before.OutputIndex {
			t.Errorf("output_index after a refused Requeue = %v, want unchanged %v", after.OutputIndex, before.OutputIndex)
		}
		if after.ProcessedAt == nil || before.ProcessedAt == nil || !after.ProcessedAt.Equal(*before.ProcessedAt) {
			t.Errorf("processed_at after a refused Requeue = %v, want unchanged %v", after.ProcessedAt, before.ProcessedAt)
		}
	})

	t.Run(name+"/ReturnedRecordsAndStationsNeverAliasStoreState", func(t *testing.T) {
		ctx := context.Background()
		s := mk(t)
		if err := s.Stations.Upsert(ctx, store.Station{StationID: 6000, IsActive: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if _, err := s.Records.Insert(ctx,
			newConformanceRecord("alias-1", 6000, conformanceBase, 15, "-")); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		claimed, err := s.Records.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimPending = (%d, %v), want (1, nil)", len(claimed), err)
		}
		if _, completeErr := s.Records.Complete(ctx, "alias-tx",
			[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
			t.Fatalf("Complete: %v", completeErr)
		}

		rec, err := s.Records.Get(ctx, "alias-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if rec.TxID == nil || rec.ProcessedAt == nil {
			t.Fatalf("rec.TxID/ProcessedAt unexpectedly nil after Complete")
		}
		// Mutate the CALLER's copy through its pointer fields. If Get ever
		// hands back a pointer into the store's own state (rather than a
		// freshly scanned/cloned value), this corrupts what a later Get
		// sees — structurally impossible against a real Postgres connection,
		// which always decodes a fresh value off the wire.
		*rec.TxID = "corrupted-by-caller"
		corruptedProcessedAt := conformanceBase.Add(999 * time.Hour)
		*rec.ProcessedAt = corruptedProcessedAt

		again, err := s.Records.Get(ctx, "alias-1")
		if err != nil {
			t.Fatalf("second Get: %v", err)
		}
		if again.TxID == nil || *again.TxID != "alias-tx" {
			txIDDisplay := "<nil>"
			if again.TxID != nil {
				txIDDisplay = *again.TxID
			}
			t.Errorf("second Get TxID = %q, want unaffected \"alias-tx\": the store returned an aliased pointer",
				txIDDisplay)
		}
		if again.ProcessedAt == nil || again.ProcessedAt.Equal(corruptedProcessedAt) {
			t.Errorf("second Get ProcessedAt = %v, want unaffected: the store returned an aliased pointer",
				again.ProcessedAt)
		}

		st, err := s.Stations.Get(ctx, 6000)
		if err != nil {
			t.Fatalf("Stations.Get: %v", err)
		}
		if st.LastReading == nil {
			t.Fatalf("st.LastReading unexpectedly nil")
		}
		corruptedReading := conformanceBase.Add(999 * time.Hour)
		*st.LastReading = corruptedReading

		stAgain, err := s.Stations.Get(ctx, 6000)
		if err != nil {
			t.Fatalf("second Stations.Get: %v", err)
		}
		if stAgain.LastReading == nil || stAgain.LastReading.Equal(corruptedReading) {
			t.Errorf("second Stations.Get LastReading = %v, want unaffected: the store returned an aliased pointer",
				stAgain.LastReading)
		}
	})
}

// newConformanceRecord builds a NewRecord whose data carries the two fields the
// station counters read, so a case can assert on last_temp and last_conditions.
//
// temp is int64 because that is what Plan A froze: weather.WeatherData's
// AirTemperature is `int64` with a `json:"air_temperature"` tag. It is fed into
// a `double precision` column, which is why last_temp can never be fractional.
func newConformanceRecord(id string, stationID int64, ts time.Time, temp int64, conditions string) store.NewRecord {
	return store.NewRecord{
		ID:              id,
		StationID:       stationID,
		Timestamp:       ts,
		ObservationTime: ts,
		Data: weather.WeatherData{
			AirTemperature: temp,
			Conditions:     conditions,
		},
	}
}
