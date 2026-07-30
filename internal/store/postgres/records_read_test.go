package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

var (
	_ func(context.Context, store.ListFilter) ([]store.Record, int64, error) = (*postgres.RecordStore)(nil).List
	_ func(context.Context, string) (store.Record, error)                    = (*postgres.RecordStore)(nil).Get
	_ func(context.Context, string) (bool, error)                            = (*postgres.RecordStore)(nil).TxIDExists
)

func listIDs(recs []store.Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

// TestListIsATotalOrderAcrossATiedBatch asserts that paging through a whole
// tied batch in fours reconstructs the entire order with no repeats, no gaps and
// a stable total. It also asserts its own fixture is the production shape
// (count(DISTINCT created_at) = 1), which is the part that matters most: a
// fixture with distinct created_at values would make every ordering claim in
// this file weaker without anybody noticing.
//
// IT DOES IN FACT DISCRIMINATE AGAINST REMOVING `, id DESC`, on this
// environment, and an earlier draft of this comment (copied from a sibling
// package's prediction before it was re-measured here) claimed the opposite.
// MEASURED on postgres:17.10 (the postgres:17-alpine image this project's
// docker-compose pins): with the tiebreaker deleted from listRecordsSQL's
// ORDER BY, and with ix_records_list additionally commented out of
// migrations.sql so the planner must sort a bare Seq Scan, `go test -count=3
// -run TestListIsATotalOrderAcrossATiedBatch` FAILED three times over, every
// time at position 0 of the very first page, and the accumulated "got" slice
// across all 5 offsets contained a duplicate id (present at two different
// OFFSETs) with a different id missing entirely — the exact "LIMIT/OFFSET over
// a non-total order is UNSPECIFIED" hazard the paragraph below describes,
// reproduced directly rather than only argued for. EXPLAIN ANALYZE on the
// mutated schema showed `Seq Scan on weather_records` feeding a
// `Sort Method: top-N heapsort`, i.e. exactly a plan shape with no total order
// to fall back on: `Sort Key: created_at DESC` alone, ties broken however the
// heapsort's comparisons happen to land, which is not insertion order and is
// not guaranteed stable across repeated executions of the same query.
//
// This test therefore DOES double as a regression gate on the tiebreaker in
// this codebase's measured environment, even though the reasoning below does
// not depend on that: created_at defaults to now(), which is
// transaction_timestamp(), so every row one poll writes shares a single
// created_at to the microsecond (measured: 19 rows in one transaction gave
// count(DISTINCT created_at) = 1). `ORDER BY created_at DESC` alone is
// therefore a NON-TOTAL order over an entire batch, and LIMIT/OFFSET over a
// non-total order is UNSPECIFIED — at production shape two legitimate plans
// for the identical untied query returned 19 of 20 DIFFERENT rows at the same
// offset. Keep the tiebreaker regardless of which way any future re-run of
// this mutation happens to fall: "it passed/failed at twenty rows on this
// planner version" is a data point, not a guarantee about every plan Postgres
// might legitimately choose.
func TestListIsATotalOrderAcrossATiedBatch(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	seeded := seedPending(t, pool, 20, 1000)
	rs := postgres.NewRecordStore(pool)

	var distinct int
	if err := pool.QueryRow(ctx,
		"SELECT count(DISTINCT created_at) FROM weather_records").Scan(&distinct); err != nil {
		t.Fatalf("counting distinct created_at: %v", err)
	}
	if distinct != 1 {
		t.Fatalf("fixture is not the production shape: count(DISTINCT created_at) = %d, want 1", distinct)
	}

	// Newest first means reverse-uuidv7 order, because uuidv7 sorts in
	// generation order.
	want := make([]string, 0, len(seeded))
	for i := len(seeded) - 1; i >= 0; i-- {
		want = append(want, seeded[i])
	}

	// Paging through in fours must reconstruct the whole order exactly, with no
	// repeats and no gaps.
	got := make([]string, 0, len(seeded))
	for offset := 0; offset < len(seeded); offset += 4 {
		page, total, err := rs.List(ctx, store.ListFilter{Limit: 4, Offset: offset})
		if err != nil {
			t.Fatalf("List offset %d: %v", offset, err)
		}
		if total != int64(len(seeded)) {
			t.Fatalf("total at offset %d = %d, want %d", offset, total, len(seeded))
		}
		got = append(got, listIDs(page)...)
	}
	if len(got) != len(want) {
		t.Fatalf("paged %d ids, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d = %s, want %s\n got %v\nwant %v", i, got[i], want[i], got, want)
		}
	}
}

func TestListFiltersByStationAndStatus(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	seedPending(t, pool, 3, 1000)
	seedPending(t, pool, 2, 2000)

	claimed, err := rs.ClaimPending(ctx, 2, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d, want 2", len(claimed))
	}

	// No filters at all.
	all, total, err := rs.List(ctx, store.ListFilter{Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 || len(all) != 5 {
		t.Fatalf("unfiltered = %d rows / total %d, want 5 / 5", len(all), total)
	}

	// Station only.
	station := int64(1000)
	byStation, total, err := rs.List(ctx, store.ListFilter{StationID: &station, Limit: 100})
	if err != nil {
		t.Fatalf("List by station: %v", err)
	}
	if total != 3 || len(byStation) != 3 {
		t.Fatalf("station 1000 = %d rows / total %d, want 3 / 3", len(byStation), total)
	}
	for _, r := range byStation {
		if r.StationID != 1000 {
			t.Fatalf("station filter leaked station %d", r.StationID)
		}
	}

	// Status only.
	processing := store.StatusProcessing
	byStatus, total, err := rs.List(ctx, store.ListFilter{Status: &processing, Limit: 100})
	if err != nil {
		t.Fatalf("List by status: %v", err)
	}
	if total != 2 || len(byStatus) != 2 {
		t.Fatalf("processing = %d rows / total %d, want 2 / 2", len(byStatus), total)
	}

	// Both.
	pending := store.StatusPending
	both, total, err := rs.List(ctx, store.ListFilter{StationID: &station, Status: &pending, Limit: 100})
	if err != nil {
		t.Fatalf("List by both: %v", err)
	}
	if total != int64(len(both)) {
		t.Fatalf("total %d disagrees with page length %d", total, len(both))
	}
	for _, r := range both {
		if r.StationID != 1000 || r.Status != store.StatusPending {
			t.Fatalf("combined filter leaked (%d, %q)", r.StationID, string(r.Status))
		}
	}

	// A status that matches nothing.
	failed := store.StatusFailed
	none, total, err := rs.List(ctx, store.ListFilter{Status: &failed, Limit: 100})
	if err != nil {
		t.Fatalf("List by failed: %v", err)
	}
	if total != 0 || len(none) != 0 {
		t.Fatalf("failed = %d rows / total %d, want 0 / 0", len(none), total)
	}
}

func TestListOnAnEmptyTableAndPastTheEnd(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	// total 0 is what the API turns into totalPages 0. If totalPages were 1
	// instead, the frontend's Next button would never disable.
	recs, total, err := rs.List(ctx, store.ListFilter{Limit: 20})
	if err != nil {
		t.Fatalf("List on an empty table: %v", err)
	}
	if total != 0 || len(recs) != 0 {
		t.Fatalf("empty table = %d rows / total %d, want 0 / 0", len(recs), total)
	}

	seedPending(t, pool, 3, 1000)
	recs, total, err = rs.List(ctx, store.ListFilter{Limit: 20, Offset: 100})
	if err != nil {
		t.Fatalf("List past the end: %v", err)
	}
	if total != 3 {
		t.Fatalf("total past the end = %d, want 3", total)
	}
	if len(recs) != 0 {
		t.Fatalf("page past the end has %d rows, want 0", len(recs))
	}
}

func TestGetRoundTripsEverySchemaField(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	want := fullWeatherData()
	inserted, err := rs.Insert(ctx, store.NewRecord{
		ID: "the-one", StationID: 4242, Timestamp: obs, ObservationTime: obs, Data: want,
	})
	if err != nil || !inserted {
		t.Fatalf("Insert = (%v, %v)", inserted, err)
	}

	got, err := rs.Get(ctx, "the-one")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "the-one" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.StationID != 4242 {
		t.Errorf("StationID = %d, want 4242", got.StationID)
	}
	if !got.Timestamp.Equal(obs) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, obs)
	}
	if !got.ObservationTime.Equal(obs) {
		t.Errorf("ObservationTime = %v, want %v", got.ObservationTime, obs)
	}
	if got.Data != want {
		t.Errorf("Data round trip changed:\n got %+v\nwant %+v", got.Data, want)
	}
	if got.Status != store.StatusPending {
		t.Errorf("Status = %q, want pending", string(got.Status))
	}
	if got.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0", got.Attempts)
	}
	if got.AdoptRequired {
		t.Error("AdoptRequired = true on a fresh row")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	// Every nullable column must come back as a genuine nil, not a zero value:
	// a zero time.Time marshals to "0001-01-01T00:00:00Z", which the frontend
	// renders as 01/01/0001 where it intends an em dash.
	if got.ClaimRef != nil || got.ClaimedAt != nil || got.TxID != nil ||
		got.OutputIndex != nil || got.BlockHeight != nil || got.ChainStatus != nil ||
		got.MinedAt != nil || got.Error != nil || got.ProcessedAt != nil {
		t.Errorf("a fresh row has a non-nil nullable field: %+v", got)
	}
	if weather.DataFieldsPerRecord != 33 {
		t.Fatalf("DataFieldsPerRecord = %d; this test's fixture assumes 33", weather.DataFieldsPerRecord)
	}
}

func TestGetUnknownIDIsErrNotFoundAndNeverAnError500(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	// id is `text`, so a malformed id is a MISS rather than a cast failure, which
	// makes the TypeScript's CastError-into-500 wart structurally impossible and
	// makes 400-vs-404 purely a handler choice.
	//
	// "The database cannot raise" would be too strong, and an earlier draft of
	// this comment said it: a NUL byte is NOT a bindable text value, and binding
	// one fails with SQLSTATE 22021 before the predicate is ever evaluated
	// (measured). Get therefore screens the id with store.ValidText first, so the
	// property the API layer relies on — user input on this path can never
	// produce a 500 — is delivered by code rather than by assumption. The NUL
	// case is in the table below for exactly that reason.
	for _, id := range []string{
		"", "not-a-uuid", "0", "../../etc/passwd", "'; DROP TABLE weather_records; --",
		"00000000-0000-0000-0000-000000000000", "\x00", "nul\x00byte",
	} {
		_, err := rs.Get(ctx, id)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want store.ErrNotFound", id, err)
		}
	}
}

func TestTxIDExistsIsTheProofGate(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)

	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, is_active) VALUES (1000, true)"); err != nil {
		t.Fatalf("seeding station: %v", err)
	}
	seedPending(t, pool, 1, 1000)
	claimed, err := rs.ClaimPending(ctx, 1, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	const txid = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	if _, completeErr := rs.Complete(ctx, txid,
		[]store.Publication{{RecordID: claimed[0].ID, OutputIndex: 0}}); completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}

	ok, err := rs.TxIDExists(ctx, txid)
	if err != nil || !ok {
		t.Fatalf("TxIDExists(known) = (%v, %v), want (true, nil)", ok, err)
	}

	// This is the anti-amplification control in front of the BEEF proof
	// endpoint: an attacker iterating 64-hex values must be answered from the
	// database, with no outbound call at all.
	for _, unknown := range []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"", "not-hex", txid + "extra", "\x00", txid[:63] + "\x00",
	} {
		ok, existsErr := rs.TxIDExists(ctx, unknown)
		if existsErr != nil {
			t.Errorf("TxIDExists(%q) error = %v, want nil", unknown, existsErr)
		}
		if ok {
			t.Errorf("TxIDExists(%q) = true, want false", unknown)
		}
	}
}

func TestListAndGetSurviveAConcurrentWriter(t *testing.T) {
	// Read paths must not deadlock or error while the claim is running, which is
	// the shape production actually has: the API serves reads on the same pool
	// the processor claims on.
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	seedPending(t, pool, 30, 1000)

	done := make(chan error, 1)
	go func() {
		_, err := rs.ClaimPending(ctx, 10, uuid.Must(uuid.NewV7()))
		done <- err
	}()
	for range 20 {
		if _, _, err := rs.List(ctx, store.ListFilter{Limit: 20}); err != nil {
			t.Errorf("List during a claim: %v", err)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("concurrent ClaimPending: %v", err)
	}
	assertPoolIsHealthy(t, pool)
}

func assertPoolIsHealthy(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool is unhealthy after the test: %v", err)
	}
}
