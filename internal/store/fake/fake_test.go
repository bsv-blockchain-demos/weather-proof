package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// Compile-time conformance. If any interface method signature drifts, this
// fails to build rather than failing at runtime in a later plan.
//
// StationStore is asserted against the adapter rather than against *fake.Store,
// because *fake.Store cannot carry both Get(ctx, string) and Get(ctx, int64).
// That is the same asymmetry that forces store.Store to be a struct of
// interfaces rather than one composed interface.
var (
	_ store.RecordStore    = (*fake.Store)(nil)
	_ store.DepositStore   = (*fake.Store)(nil)
	_ store.PreflightStore = (*fake.Store)(nil)
	_ store.Pinger         = (*fake.Store)(nil)
	_ store.StationStore   = fake.New().Stations()
)

func newRecord(id string, stationID int64, obs time.Time) store.NewRecord {
	return store.NewRecord{
		ID:              id,
		StationID:       stationID,
		Timestamp:       obs,
		ObservationTime: obs,
		Data:            weather.WeatherData{AirTemperature: 18, Conditions: "Clear"},
	}
}

func TestStoreAggregateIsWired(t *testing.T) {
	f := fake.New()
	agg := f.Store()
	if agg.Records == nil || agg.Stations == nil || agg.Deposits == nil ||
		agg.Preflight == nil || agg.Health == nil {
		t.Fatal("fake.Store().Store() left a field nil")
	}
}

func TestInsertDedupeIsNotAnError(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

	inserted, err := f.Insert(ctx, newRecord("a", 1000, obs))
	if err != nil || !inserted {
		t.Fatalf("first Insert = (%v, %v), want (true, nil)", inserted, err)
	}

	inserted, err = f.Insert(ctx, newRecord("b", 1000, obs))
	if err != nil {
		t.Fatalf("duplicate Insert error = %v, want nil", err)
	}
	if inserted {
		t.Fatal("duplicate Insert = true, want false")
	}

	// A repeated ID with a DIFFERENT dedupe key is a genuine surprise, and the
	// Postgres store reports it as store.ErrConflict (the ON CONFLICT arbiter
	// covers (station_id, observation_time), not the primary key). The fake must
	// agree, or every B2 test of the 409 path passes against a fake that cannot
	// produce one.
	if _, conflictErr := f.Insert(ctx, newRecord("a", 2000, obs.Add(time.Hour))); !errors.Is(conflictErr, store.ErrConflict) {
		t.Fatalf("Insert with a repeated id = %v, want store.ErrConflict", conflictErr)
	}
}

// TestFakeStationSearchMatchesTheSQLBranches is the anti-drift test on the one
// divergence that would be invisible: if ListStations ignored f.Search, every
// B2 test of ?search= would pass while proving nothing at all.
//
// The fake cannot reproduce websearch_to_tsquery's ranking and does not try.
// What it reproduces is the CONTRACT both implementations share, which
// Task 19's conformance suite asserts against both: an all-digits search is an
// exact station_id lookup, anything else matches name or location
// case-insensitively, a NUL byte matches nothing without erroring, and the
// total is the unpaged count.
func TestFakeStationSearchMatchesTheSQLBranches(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	f.SeedStation(store.Station{StationID: 1000, Name: "Harbor Mast", Location: "Bristol Docks", IsActive: true})
	f.SeedStation(store.Station{StationID: 1001, Name: "Clifton Ridge", Location: "Bristol Downs", IsActive: true})
	f.SeedStation(store.Station{StationID: 2000, Name: "Kelvin Yard", Location: "Glasgow", IsActive: true})
	ss := f.Stations()

	cases := []struct {
		search string
		want   int64
	}{
		{"", 3},
		{"1001", 1},
		{"424242", 0},
		{"bristol", 2},
		{"KELVIN", 1},
		{"reykjavik", 0},
		{"12345abc", 0},
		{"\x00nul", 0},
	}
	for _, c := range cases {
		sts, total, err := ss.List(ctx, store.StationFilter{Search: c.search, Limit: 50})
		if err != nil {
			t.Errorf("List(search=%q) error = %v, want nil", c.search, err)
			continue
		}
		if total != c.want {
			t.Errorf("List(search=%q) total = %d, want %d", c.search, total, c.want)
		}
		if int64(len(sts)) != c.want {
			t.Errorf("List(search=%q) returned %d rows, want %d", c.search, len(sts), c.want)
		}
	}
}

// TestFakeCompleteMatchesTheSQLStationSemantics pins the two station-bump rules
// the SQL has and an earlier fake did not: a missing station row is NOT created
// (bumpStationsSQL matches nothing), and last_temp/last_conditions advance only
// when the reading is NEWER than the stored last_reading.
func TestFakeCompleteMatchesTheSQLStationSemantics(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

	// No station row: the record still completes and the counters still move,
	// but no station is invented.
	if _, err := f.Insert(ctx, newRecord("orphan", 4242, base)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	claimed, err := f.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if _, completeErr := f.Complete(ctx, "orphan-tx",
		[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}
	if _, getErr := f.Stations().Get(ctx, 4242); !errors.Is(getErr, store.ErrNotFound) {
		t.Fatalf("station 4242 = %v, want store.ErrNotFound: Complete must not invent a station", getErr)
	}

	// An OLDER reading completing later must not overwrite last_temp.
	f.SeedStation(store.Station{StationID: 1000, IsActive: true})
	newer := store.NewRecord{
		ID: "newer", StationID: 1000, Timestamp: base.Add(time.Hour), ObservationTime: base.Add(time.Hour),
		Data: weather.WeatherData{AirTemperature: 21, Conditions: "Clear"},
	}
	older := store.NewRecord{
		ID: "older", StationID: 1000, Timestamp: base, ObservationTime: base,
		Data: weather.WeatherData{AirTemperature: 4, Conditions: "Snow"},
	}
	for _, r := range []store.NewRecord{newer, older} {
		if _, insertErr := f.Insert(ctx, r); insertErr != nil {
			t.Fatalf("Insert %s: %v", r.ID, insertErr)
		}
		batch, claimErr := f.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
		if claimErr != nil {
			t.Fatalf("ClaimPending %s: %v", r.ID, claimErr)
		}
		if _, completeErr := f.Complete(ctx, "tx-"+r.ID,
			[]store.Publication{{RecordID: batch[0].ID, OutputIndex: 0}}); completeErr != nil {
			t.Fatalf("Complete %s: %v", r.ID, completeErr)
		}
	}
	st, err := f.Stations().Get(ctx, 1000)
	if err != nil {
		t.Fatalf("station Get: %v", err)
	}
	if st.LastTemp == nil || *st.LastTemp != 21 {
		t.Errorf("last_temp = %v, want the newer reading's 21", st.LastTemp)
	}
	if st.LastConditions != "Clear" {
		t.Errorf("last_conditions = %q, want Clear", st.LastConditions)
	}
	if st.TxRecords != 2 {
		t.Errorf("tx_records = %d, want 2 (both records counted)", st.TxRecords)
	}
}

func TestGetUnknownIsErrNotFound(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	for _, id := range []string{"nope", "", "0", "not-a-uuid"} {
		if _, err := f.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want store.ErrNotFound", id, err)
		}
	}
}

func TestStationGetUnknownIsErrNotFound(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	ss := f.Stations()
	for _, id := range []int64{0, -1, 999} {
		if _, err := ss.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("station Get(%d) error = %v, want store.ErrNotFound", id, err)
		}
	}
}

func TestListIsNewestFirstWithIDTiebreak(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	shared := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time { return shared }

	obs := shared
	for _, id := range []string{"id-1", "id-2", "id-3"} {
		obs = obs.Add(time.Minute)
		if _, err := f.Insert(ctx, newRecord(id, 1000, obs)); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	recs, total, err := f.List(ctx, store.ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	want := []string{"id-3", "id-2", "id-1"}
	for i := range want {
		if recs[i].ID != want[i] {
			t.Fatalf("List order = %v, want %v", ids(recs), want)
		}
	}
}

func TestListFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	for i := range 5 {
		r := newRecord(string(rune('a'+i)), int64(1000+i%2), base.Add(time.Duration(i)*time.Minute))
		if _, err := f.Insert(ctx, r); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	station := int64(1000)
	_, total, err := f.List(ctx, store.ListFilter{StationID: &station, Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("station total = %d, want 3", total)
	}

	pending := store.StatusPending
	_, total, err = f.List(ctx, store.ListFilter{Status: &pending, Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 {
		t.Fatalf("pending total = %d, want 5", total)
	}

	page1, _, err := f.List(ctx, store.ListFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	page2, _, err := f.List(ctx, store.ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Both pages PINNED, not merely disjoint. Five records seeded a..e with
	// ascending timestamps, listed newest-first, so page 1 is e,d and page 2 is
	// c,b. The cross-page duplicate check below is satisfied by an EMPTY page 2 —
	// which is exactly what an off-by-one in the offset slice produces — so on its
	// own it cannot tell working pagination from pagination that returns nothing.
	for label, page := range map[string][]store.Record{"page1": page1, "page2": page2} {
		if len(page) != 2 {
			t.Fatalf("%s has %d records (%v), want 2", label, len(page), ids(page))
		}
	}
	if got := ids(page1); got[0] != "e" || got[1] != "d" {
		t.Errorf("page1 = %v, want [e d]", got)
	}
	if got := ids(page2); got[0] != "c" || got[1] != "b" {
		t.Errorf("page2 = %v, want [c b]", got)
	}

	for _, a := range page1 {
		for _, b := range page2 {
			if a.ID == b.ID {
				t.Fatalf("id %q on both pages", a.ID)
			}
		}
	}
}

func TestClaimCompleteAndStats(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	for i := range 3 {
		if _, err := f.Insert(ctx, newRecord(string(rune('a'+i)), 1000, base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := f.Upsert(ctx, store.Station{StationID: 1000, IsActive: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ref := uuid.Must(uuid.NewV7())
	claimed, err := f.ClaimPending(ctx, 2, ref)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d, want 2", len(claimed))
	}
	for _, r := range claimed {
		if r.Status != store.StatusProcessing {
			t.Fatalf("claimed status = %q, want processing", string(r.Status))
		}
		if r.ClaimRef == nil || *r.ClaimRef != ref {
			t.Fatalf("claim ref = %v, want %v", r.ClaimRef, ref)
		}
		if r.ClaimedAt == nil {
			t.Fatal("claimed row has a nil ClaimedAt")
		}
	}

	pubs := make([]store.Publication, 0, len(claimed))
	for i, r := range claimed {
		pubs = append(pubs, store.Publication{RecordID: r.ID, OutputIndex: int32(i)})
	}
	stats, err := f.Complete(ctx, "deadbeef", pubs)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if stats.TotalTx != 1 {
		t.Fatalf("TotalTx = %d, want 1", stats.TotalTx)
	}
	if stats.TotalRecords != 2 {
		t.Fatalf("TotalRecords = %d, want 2", stats.TotalRecords)
	}
	if stats.TotalDataPoints() != 66 {
		t.Fatalf("TotalDataPoints = %d, want 66", stats.TotalDataPoints())
	}
	if stats.ActiveStations != 1 {
		t.Fatalf("ActiveStations = %d, want 1", stats.ActiveStations)
	}
	if stats.LastRecordWrite == nil {
		t.Fatal("LastRecordWrite is nil after Complete")
	}

	ok, err := f.TxIDExists(ctx, "deadbeef")
	if err != nil || !ok {
		t.Fatalf("TxIDExists = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = f.TxIDExists(ctx, "cafebabe")
	if err != nil || ok {
		t.Fatalf("TxIDExists(unknown) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestReapExpiredPreservesRefAndSetsAdopt(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	claimed := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time { return claimed.Add(9 * time.Minute) }
	ref := uuid.Must(uuid.NewV7())

	f.SeedRecord(store.Record{
		ID: "stranded", StationID: 1, Status: store.StatusProcessing,
		ClaimedAt: &claimed, ClaimRef: &ref, CreatedAt: claimed,
		ObservationTime: claimed, Timestamp: claimed,
	})
	fresh := claimed.Add(8*time.Minute + 30*time.Second)
	f.SeedRecord(store.Record{
		ID: "inflight", StationID: 2, Status: store.StatusProcessing,
		ClaimedAt: &fresh, CreatedAt: claimed,
		ObservationTime: claimed.Add(time.Minute), Timestamp: claimed,
	})

	reaped, err := f.ReapExpired(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("ReapExpired: %v", err)
	}
	if len(reaped) != 1 || reaped[0].ID != "stranded" {
		t.Fatalf("reaped = %v, want [stranded]", ids(reaped))
	}
	if !reaped[0].AdoptRequired {
		t.Fatal("reaped row must have AdoptRequired true")
	}
	if reaped[0].ClaimRef == nil || *reaped[0].ClaimRef != ref {
		t.Fatal("reaped row must keep its prior claim ref")
	}
	if reaped[0].Attempts != 0 {
		t.Fatalf("reaped Attempts = %d, want 0 (the reaper never spends the budget)", reaped[0].Attempts)
	}
}

func TestSnapshotCounts(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	now := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	old := now.Add(-2 * time.Hour)
	mined := store.ChainMined
	aborted := store.ChainAborted

	f.SeedRecord(store.Record{ID: "p", Status: store.StatusPending})
	claimedAt := now
	f.SeedRecord(store.Record{ID: "w", Status: store.StatusProcessing, ClaimedAt: &claimedAt})
	f.SeedRecord(store.Record{ID: "f", Status: store.StatusFailed})
	f.SeedRecord(store.Record{ID: "u", Status: store.StatusCompleted, ProcessedAt: &old})
	f.SeedRecord(store.Record{ID: "m", Status: store.StatusCompleted, ProcessedAt: &old, ChainStatus: &mined})
	f.SeedRecord(store.Record{ID: "a", Status: store.StatusFailed, ChainStatus: &aborted})

	snap, err := f.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.PendingRows != 1 || snap.ProcessingRows != 1 || snap.FailedRows != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.StillUnminedOlderThan1h != 1 {
		t.Fatalf("StillUnminedOlderThan1h = %d, want 1", snap.StillUnminedOlderThan1h)
	}
	if snap.MinedCount != 1 || snap.AbortedCount != 1 {
		t.Fatalf("mined/aborted = %d/%d, want 1/1", snap.MinedCount, snap.AbortedCount)
	}
}

func TestFailAllAndPingErrAreInjectable(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	boom := errors.New("boom")
	f.FailAll = boom
	// Three values: RecordStore.List returns ([]Record, int64, error). Two
	// blanks is still legal — dogsled is configured with
	// max-blank-identifiers 2.
	if _, _, err := f.List(ctx, store.ListFilter{Limit: 1}); !errors.Is(err, boom) {
		t.Fatalf("List error = %v, want boom", err)
	}
	if _, err := f.Get(ctx, "x"); !errors.Is(err, boom) {
		t.Fatalf("Get error = %v, want boom", err)
	}
	f.FailAll = nil
	f.PingErr = boom
	if err := f.Ping(ctx); !errors.Is(err, boom) {
		t.Fatalf("Ping error = %v, want boom", err)
	}
}

func TestDepositsAndPreflight(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	if err := f.NewDeposit(ctx, store.Deposit{Suffix: "s1", Prefix: "p1", Address: "addr", LockingScript: "76a9"}); err != nil {
		t.Fatalf("NewDeposit: %v", err)
	}
	pending, err := f.PendingDeposits(ctx)
	if err != nil {
		t.Fatalf("PendingDeposits: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	// markErr, not err: the outer err is still live, and govet's shadow check
	// rejects a redeclaration.
	if markErr := f.MarkInternalized(ctx, "s1", "txid", 0, 5000); markErr != nil {
		t.Fatalf("MarkInternalized: %v", markErr)
	}
	pending, err = f.PendingDeposits(ctx)
	if err != nil {
		t.Fatalf("PendingDeposits: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after internalize = %d, want 0", len(pending))
	}

	ok, err := f.PreflightOK(ctx, "fp")
	if err != nil || ok {
		t.Fatalf("PreflightOK = (%v, %v), want (false, nil)", ok, err)
	}
	if recordErr := f.RecordPreflight(ctx, "fp"); recordErr != nil {
		t.Fatalf("RecordPreflight: %v", recordErr)
	}
	ok, err = f.PreflightOK(ctx, "fp")
	if err != nil || !ok {
		t.Fatalf("PreflightOK = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestReapExpiredTiebreaksByID pins the fix for round-1 finding CRITICAL-1:
// reapExpiredSQL orders claimed_at ASC, id ASC, and one ClaimPending call
// stamps a whole batch with a single claimed_at, so ties are the common case.
// Seeding out of id order and truncating with a limit is the shape that a
// count-only assertion cannot catch: without the id tiebreak, sort.Slice's
// result for tied keys follows Go's randomized map-iteration order, so which
// rows a limit-truncated reap returns would be non-deterministic.
func TestReapExpiredTiebreaksByID(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	claimed := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time { return claimed.Add(9 * time.Minute) }
	ref := uuid.Must(uuid.NewV7())

	for _, id := range []string{"e", "c", "a", "d", "b"} {
		f.SeedRecord(store.Record{
			ID: id, StationID: 1, Status: store.StatusProcessing,
			ClaimedAt: &claimed, ClaimRef: &ref, CreatedAt: claimed,
			ObservationTime: claimed, Timestamp: claimed,
		})
	}

	reaped, err := f.ReapExpired(ctx, 5*time.Minute, 3)
	if err != nil {
		t.Fatalf("ReapExpired: %v", err)
	}
	if len(reaped) != 3 {
		t.Fatalf("len(reaped) = %d, want 3", len(reaped))
	}
	want := []string{"a", "b", "c"}
	for i, id := range want {
		if reaped[i].ID != id {
			t.Fatalf("ReapExpired(limit=3) = %v, want %v (claimed_at ASC, id ASC)", ids(reaped), want)
		}
	}
}

// TestReconcileCandidatesTiebreaksByID pins the fix for round-1 finding
// CRITICAL-2: reconcileCandidatesSQL orders processed_at ASC, id ASC, and
// Complete stamps a whole batch with a single processed_at, so ties are the
// common case. Same shape as the ReapExpired test above, for the same reason:
// a count-only assertion would not catch a missing tiebreak.
func TestReconcileCandidatesTiebreaksByID(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	now := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	processedAt := now.Add(-2 * time.Hour)

	for _, id := range []string{"e", "c", "a", "d", "b"} {
		f.SeedRecord(store.Record{
			ID: id, Status: store.StatusCompleted, ProcessedAt: &processedAt,
		})
	}

	out, err := f.ReconcileCandidates(ctx, time.Hour, 3)
	if err != nil {
		t.Fatalf("ReconcileCandidates: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	want := []string{"a", "b", "c"}
	for i, id := range want {
		if out[i].ID != id {
			t.Fatalf("ReconcileCandidates(limit=3) = %v, want %v (processed_at ASC, id ASC)", ids(out), want)
		}
	}
}

// TestPendingDepositsOrdersByCreatedAtThenSuffix pins the fix for round-1
// finding CRITICAL-3: pendingDepositsSQL orders created_at ASC, suffix ASC —
// suffix is only the TIEBREAKER, not the primary key. "zzz" is created first
// and "aaa" second, so a suffix-only sort (alphabetical) would report them
// backwards from creation order even though neither one ties the other.
func TestPendingDepositsOrdersByCreatedAtThenSuffix(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	tick := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time {
		now := tick
		tick = tick.Add(time.Minute)
		return now
	}

	if err := f.NewDeposit(ctx, store.Deposit{Suffix: "zzz", Prefix: "p", Address: "addr", LockingScript: "76a9"}); err != nil {
		t.Fatalf("NewDeposit zzz: %v", err)
	}
	if err := f.NewDeposit(ctx, store.Deposit{Suffix: "aaa", Prefix: "p", Address: "addr", LockingScript: "76a9"}); err != nil {
		t.Fatalf("NewDeposit aaa: %v", err)
	}

	pending, err := f.PendingDeposits(ctx)
	if err != nil {
		t.Fatalf("PendingDeposits: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("len(pending) = %d, want 2", len(pending))
	}
	if pending[0].Suffix != "zzz" || pending[1].Suffix != "aaa" {
		t.Fatalf("PendingDeposits order = [%s %s], want [zzz aaa] (created_at ASC, suffix ASC)",
			pending[0].Suffix, pending[1].Suffix)
	}
}

// TestListZeroLimitReturnsZeroRows pins the fix for round-1 finding
// IMPORTANT-4: a Limit of zero returns zero rows for both RecordStore.List and
// StationStore.List — never "unbounded" — while Total still reports the full
// unpaged count, matching SQL's own LIMIT 0.
func TestListZeroLimitReturnsZeroRows(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		obs := base.Add(time.Duration(i) * time.Minute)
		if _, err := f.Insert(ctx, newRecord(id, 1000, obs)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	f.SeedStation(store.Station{StationID: 1000, IsActive: true})

	recs, total, err := f.List(ctx, store.ListFilter{Limit: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("List(Limit:0) returned %d rows, want 0", len(recs))
	}
	if total != 3 {
		t.Fatalf("List(Limit:0) total = %d, want 3 (Total ignores Limit)", total)
	}

	sts, total, err := f.Stations().List(ctx, store.StationFilter{Limit: 0})
	if err != nil {
		t.Fatalf("ListStations: %v", err)
	}
	if len(sts) != 0 {
		t.Fatalf("ListStations(Limit:0) returned %d rows, want 0", len(sts))
	}
	if total != 1 {
		t.Fatalf("ListStations(Limit:0) total = %d, want 1", total)
	}
}

// TestGetDoesNotAliasInternalState is not one of round-1's five numbered
// findings but pins the fix for IMPORTANT-5 directly: a Record handed to a
// caller must never share a pointer field's address with what the fake stores
// internally. Real Postgres always returns freshly-scanned values, so a caller
// mutating a field's pointee is structurally impossible against it — and must
// be impossible here too.
//
// wantClaimed is deliberately a SEPARATE variable from claimed, captured
// before anything mutates through a returned pointer. SeedRecord stores
// ClaimedAt as &claimed verbatim (documented, expected — see SeedRecord's own
// doc comment), so under the bug this test exists to catch, `*rec.ClaimedAt =
// mutated` would silently overwrite the memory claimed itself occupies. A
// comparison against claimed would then read the very corruption it was
// trying to detect and pass for the wrong reason; wantClaimed, a distinct
// copy made before the mutation, cannot be reached by that same pointer and
// stays a trustworthy independent witness.
func TestGetDoesNotAliasInternalState(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	claimed := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	wantClaimed := claimed
	ref := uuid.Must(uuid.NewV7())
	f.SeedRecord(store.Record{
		ID: "r1", Status: store.StatusProcessing, ClaimedAt: &claimed, ClaimRef: &ref, CreatedAt: claimed,
	})

	rec, err := f.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	mutated := claimed.Add(time.Hour)
	*rec.ClaimedAt = mutated

	again, err := f.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if again.ClaimedAt == nil || !again.ClaimedAt.Equal(wantClaimed) {
		t.Fatalf("mutating a returned Record's ClaimedAt corrupted the fake: second Get = %v, want %v",
			again.ClaimedAt, wantClaimed)
	}
}

// TestGetStationDoesNotAliasInternalState is round 2's fix for the Station
// twin of IMPORTANT-5: GetStation returned st straight out of s.stations with
// no clone, so a caller mutating a pointer field on a returned Station could
// corrupt the fake's internal state, the same mechanism as the Record case
// above and structurally impossible against real Postgres. Covers two
// distinct pointer field types (time.Time and float64) rather than just one.
//
// wantReading/wantTemp are independent snapshots for the same reason
// TestGetDoesNotAliasInternalState's wantClaimed is: SeedStation aliases
// reading/temp verbatim, so comparing against reading/temp directly would
// read back the very corruption the test exists to catch.
func TestGetStationDoesNotAliasInternalState(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	ss := f.Stations()
	reading := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	temp := 18.5
	wantReading := reading
	wantTemp := temp
	f.SeedStation(store.Station{StationID: 1000, LastReading: &reading, LastTemp: &temp})

	st, err := ss.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	mutatedReading := reading.Add(time.Hour)
	*st.LastReading = mutatedReading
	*st.LastTemp = 99

	again, err := ss.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if again.LastReading == nil || !again.LastReading.Equal(wantReading) {
		t.Fatalf("mutating a returned Station's LastReading corrupted the fake: second Get = %v, want %v",
			again.LastReading, wantReading)
	}
	if again.LastTemp == nil || *again.LastTemp != wantTemp {
		t.Fatalf("mutating a returned Station's LastTemp corrupted the fake: second Get = %v, want %v",
			again.LastTemp, wantTemp)
	}
}

// TestListStationsDoesNotAliasInternalState covers the second round-2 clone
// site: the re-review noted that on the Record side only Get got a direct
// aliasing test and the other four clone sites rested on code inspection
// alone, so this test exists specifically to not repeat that gap for
// ListStations. Mutates a Station obtained from List, then confirms a
// separate Get (itself already proven not to alias, by the test above) still
// sees the original values.
//
// wantReading/wantHeight are independent snapshots, same reasoning as the two
// tests above.
func TestListStationsDoesNotAliasInternalState(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	ss := f.Stations()
	reading := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	height := int64(800000)
	wantReading := reading
	wantHeight := height
	f.SeedStation(store.Station{StationID: 1000, LastReading: &reading, LastBlockHeight: &height, IsActive: true})

	sts, _, err := ss.List(ctx, store.StationFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sts) != 1 {
		t.Fatalf("len(sts) = %d, want 1", len(sts))
	}
	mutatedReading := reading.Add(time.Hour)
	*sts[0].LastReading = mutatedReading
	*sts[0].LastBlockHeight = 999999

	again, err := ss.Get(ctx, 1000)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if again.LastReading == nil || !again.LastReading.Equal(wantReading) {
		t.Fatalf("mutating a Station from ListStations corrupted the fake: LastReading = %v, want %v",
			again.LastReading, wantReading)
	}
	if again.LastBlockHeight == nil || *again.LastBlockHeight != wantHeight {
		t.Fatalf("mutating a Station from ListStations corrupted the fake: LastBlockHeight = %v, want %v",
			again.LastBlockHeight, wantHeight)
	}
}

func ids(recs []store.Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

// TestClaimPendingNonPositiveNReturnsZeroRows closes the regression-test gap
// Task 8 deliberately deferred to Task 19's conformance suite: ClaimPending's
// non-positive-n behavior was already correct (n <= 0 returns zero rows, a
// nil error, never "unbounded") but had no committed fake-side test of its
// own — only the postgres side did.
func TestClaimPendingNonPositiveNReturnsZeroRows(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	if _, err := f.Insert(ctx, newRecord("p1", 1000, base)); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for _, n := range []int{0, -1} {
		recs, err := f.ClaimPending(ctx, n, uuid.Must(uuid.NewV7()))
		if err != nil {
			t.Fatalf("ClaimPending(n=%d): err = %v, want nil", n, err)
		}
		if recs == nil {
			t.Fatalf("ClaimPending(n=%d) returned a nil slice, want a non-nil empty slice", n)
		}
		if len(recs) != 0 {
			t.Fatalf("ClaimPending(n=%d) claimed %d rows, want 0", n, len(recs))
		}
	}

	rec, err := f.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != store.StatusPending {
		t.Fatalf("row status = %q after two non-positive-n claims, want an untouched pending", string(rec.Status))
	}
}

// TestReapExpiredNonPositiveLimitReturnsZeroRowsAndDoesNotMutate is the
// fake's half of the frozen non-positive-limit rule (see clampLimit's doc
// comment in fake.go): interfaces.go bans "unbounded" for every limit-taking
// method, and the fake's own ReapExpired violated it for a NEGATIVE limit
// specifically. Its old `if len(out) == limit { break }` loop guard can
// never equal a negative limit, so it reaped — and MUTATED — every stranded
// row instead of none. limit == 0 happened to work by the same loop's
// accident (len(out) starts at 0, which DOES equal 0), which is exactly the
// kind of inconsistency a single clampLimit chokepoint removes rather than
// leaving to happenstance.
//
// The row must be untouched, not merely "the returned slice is empty": the
// old bug did not just fail to RETURN the stranded row for limit=-1, it
// MUTATED it to pending with adopt_required set — a real side effect a
// caller relying on the documented "zero rows, nothing written" contract
// would never expect.
func TestReapExpiredNonPositiveLimitReturnsZeroRowsAndDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	claimedAt := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time { return claimedAt.Add(9 * time.Minute) }
	ref := uuid.Must(uuid.NewV7())
	f.SeedRecord(store.Record{
		ID: "stranded", StationID: 1, Status: store.StatusProcessing,
		ClaimedAt: &claimedAt, ClaimRef: &ref, CreatedAt: claimedAt,
	})

	for _, limit := range []int{0, -1} {
		reaped, err := f.ReapExpired(ctx, 5*time.Minute, limit)
		if err != nil {
			t.Fatalf("ReapExpired(limit=%d): err = %v, want nil", limit, err)
		}
		if reaped == nil {
			t.Fatalf("ReapExpired(limit=%d) returned a nil slice, want a non-nil empty slice", limit)
		}
		if len(reaped) != 0 {
			t.Fatalf("ReapExpired(limit=%d) reaped %d rows, want 0", limit, len(reaped))
		}
	}

	rec, err := f.Get(ctx, "stranded")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != store.StatusProcessing {
		t.Fatalf("stranded row status = %q after two non-positive-limit reaps, want an untouched processing",
			string(rec.Status))
	}
	if rec.AdoptRequired {
		t.Fatal("stranded row got AdoptRequired=true from a non-positive-limit reap, want untouched")
	}
}

// TestRequeueNonPositiveLimitReturnsZeroAndDoesNotWrite is Requeue's half of
// the same rule: the old `if f.Limit > 0 && len(match) > f.Limit` guard
// skipped truncation entirely for ANY non-positive limit (zero included,
// unlike ReapExpired's accidental zero case), so both the DryRun count and
// the real write touched EVERY matched row regardless of Limit. All four
// combinations of {0, -1} x {write, DryRun} are covered, matching the
// postgres side's own TestRequeueNonPositiveLimitNeverTouchesTheDatabase.
func TestRequeueNonPositiveLimitReturnsZeroAndDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	old := time.Date(2026, 4, 17, 13, 40, 0, 0, time.UTC)
	f.SeedRecord(store.Record{
		ID: "f1", StationID: 1000, Status: store.StatusFailed, Attempts: 5, CreatedAt: old,
	})

	for _, limit := range []int{0, -1} {
		for _, dryRun := range []bool{false, true} {
			n, err := f.Requeue(ctx, store.RequeueFilter{
				Status: store.StatusFailed, Since: time.Hour, Limit: limit, DryRun: dryRun,
			})
			if err != nil {
				t.Fatalf("Requeue(limit=%d, dryRun=%v): err = %v, want nil", limit, dryRun, err)
			}
			if n != 0 {
				t.Fatalf("Requeue(limit=%d, dryRun=%v) = %d, want 0", limit, dryRun, n)
			}
		}
	}

	rec, err := f.Get(ctx, "f1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != store.StatusFailed {
		t.Fatalf("row status = %q after four non-positive-limit requeues, want an untouched failed", string(rec.Status))
	}
	if rec.Attempts != 5 {
		t.Fatalf("row attempts = %d, want an untouched 5", rec.Attempts)
	}
}

// TestReconcileCandidatesNonPositiveLimitReturnsZeroRows is
// ReconcileCandidates' half of the same rule: the old
// `if limit > 0 && len(out) > limit` guard skipped truncation for any
// non-positive limit and returned EVERY candidate instead of none.
// ReconcileCandidates never mutates, so there is no side effect to check —
// the zero-row, nil-error contract is the whole property, proven by a
// subsequent real call still finding the untouched candidate.
func TestReconcileCandidatesNonPositiveLimitReturnsZeroRows(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	now := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	f.Now = func() time.Time { return now }
	processedAt := now.Add(-2 * time.Hour)
	f.SeedRecord(store.Record{ID: "candidate", Status: store.StatusCompleted, ProcessedAt: &processedAt})

	for _, limit := range []int{0, -1} {
		cands, err := f.ReconcileCandidates(ctx, time.Hour, limit)
		if err != nil {
			t.Fatalf("ReconcileCandidates(limit=%d): err = %v, want nil", limit, err)
		}
		if cands == nil {
			t.Fatalf("ReconcileCandidates(limit=%d) returned a nil slice, want a non-nil empty slice", limit)
		}
		if len(cands) != 0 {
			t.Fatalf("ReconcileCandidates(limit=%d) returned %d rows, want 0", limit, len(cands))
		}
	}

	cands, err := f.ReconcileCandidates(ctx, time.Hour, 100)
	if err != nil {
		t.Fatalf("ReconcileCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0].ID != "candidate" {
		t.Fatalf("candidates = %v, want exactly [candidate] (untouched by the two non-positive-limit calls)",
			ids(cands))
	}
}

// TestListAndListStationsNegativeLimitAlsoReturnsZeroRows extends the
// existing TestListZeroLimitReturnsZeroRows, which only exercised Limit: 0.
// List and ListStations were already correct for BOTH non-positive values
// (`f.Limit <= 0`), but no committed test distinguished "correct because of
// the <= 0 comparison" from "correct because 0 coincidentally behaves like
// an unset limit," the same gap this round's audit closes for
// ReapExpired/Requeue/ReconcileCandidates. Limit: -1 is what actually tells
// them apart: a `== 0` or `> 0` comparison (the exact bug shape found
// elsewhere in this file) would leak every row through for -1 while still
// passing the Limit: 0 test.
func TestListAndListStationsNegativeLimitAlsoReturnsZeroRows(t *testing.T) {
	ctx := context.Background()
	f := fake.New()
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		obs := base.Add(time.Duration(i) * time.Minute)
		if _, err := f.Insert(ctx, newRecord(id, 1000, obs)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	f.SeedStation(store.Station{StationID: 1000, IsActive: true})

	recs, total, err := f.List(ctx, store.ListFilter{Limit: -1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("List(Limit:-1) returned %d rows, want 0", len(recs))
	}
	if total != 3 {
		t.Fatalf("List(Limit:-1) total = %d, want 3 (Total ignores Limit)", total)
	}

	sts, total, err := f.Stations().List(ctx, store.StationFilter{Limit: -1})
	if err != nil {
		t.Fatalf("ListStations: %v", err)
	}
	if len(sts) != 0 {
		t.Fatalf("ListStations(Limit:-1) returned %d rows, want 0", len(sts))
	}
	if total != 1 {
		t.Fatalf("ListStations(Limit:-1) total = %d, want 1", total)
	}
}
