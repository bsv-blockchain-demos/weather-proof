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

func ids(recs []store.Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}
