package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

// Compile-time conformance. This is the point at which the package is proven to
// implement the whole seam: if any signature drifted while the methods were
// being added one task at a time, the build breaks here.
var (
	_ store.RecordStore    = (*postgres.RecordStore)(nil)
	_ store.StationStore   = (*postgres.StationStore)(nil)
	_ store.DepositStore   = (*postgres.DepositStore)(nil)
	_ store.PreflightStore = (*postgres.PreflightStore)(nil)
)

func TestNewWiresEveryMember(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	agg := postgres.New(pool)
	if agg.Records == nil {
		t.Error("Store.Records is nil")
	}
	if agg.Stations == nil {
		t.Error("Store.Stations is nil")
	}
	if agg.Deposits == nil {
		t.Error("Store.Deposits is nil")
	}
	if agg.Preflight == nil {
		t.Error("Store.Preflight is nil")
	}
	if agg.Health == nil {
		t.Error("Store.Health is nil")
	}
	if err := agg.Health.Ping(context.Background()); err != nil {
		t.Fatalf("Store.Health.Ping: %v", err)
	}
}

// TestEveryStatementPreparesAgainstARealServer is the mechanical proof that
// every statement this package runs is static, parameterized SQL.
//
// PREPARE only succeeds for fixed statement text with numbered placeholders, so
// a statement assembled at runtime, or one with a syntax error, or one whose
// placeholder numbering is not contiguous, cannot pass. This test is what
// replaces the linter: gosec's G201/G202 were MEASURED not to fire on pgx sinks
// at all.
func TestEveryStatementPreparesAgainstARealServer(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	queries := postgres.AllQueries()
	if len(queries) < 20 {
		t.Fatalf("AllQueries() returned %d statements; that cannot be the whole package", len(queries))
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	for name, stmt := range queries {
		if _, prepErr := conn.Conn().Prepare(ctx, "prep_"+name, stmt); prepErr != nil {
			t.Errorf("statement %q does not prepare: %v\n---\n%s\n---", name, prepErr, stmt)
		}
	}
}

// TestNoDriverErrorEscapesTheStore provokes real Postgres errors through the
// public API and asserts that nothing they return carries anything but a
// five-character SQLSTATE.
//
// A *pgconn.PgError's Error() is "severity: message (SQLSTATE code)", and the
// struct also carries Detail, Hint, ConstraintName, ColumnName and TableName —
// any of which can disclose schema and sometimes column VALUES. The TypeScript
// this replaces returned {error: 'Internal server error', message: err.message}
// from its global handler, echoing exactly that.
func TestNoDriverErrorEscapesTheStore(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	ds := postgres.NewDepositStore(pool)
	ss := postgres.NewStationStore(pool)

	// The last four are the CONNECTION-string tokens, and they belong here
	// because of the branch this test used not to exercise: pgx's connect failure
	// reads `failed to connect to \`host=… user=… database=…\`: dial tcp …`, and
	// it satisfies errors.Is(err, context.DeadlineExceeded) — so a classify that
	// returned the cancellation error unchanged would hand the host, the user and
	// the database name to its caller.
	forbidden := []string{
		"SELECT", "INSERT", "UPDATE", "DELETE", "select", "insert", "update",
		"weather_records", "app_stats", "stations", "deposits", "app_preflight",
		"ux_records_station_obs", "ck_records", "deposits_pkey", "weather_records_pkey",
		"duplicate key", "Key (", "DETAIL", "HINT", "constraint", "column",
		"host=", "user=", "database=", "dial",
	}

	assertOpaque := func(label string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error", label)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatalf("%s: a *pgconn.PgError escaped the store: %v", label, err)
		}
		msg := err.Error()
		for _, bad := range forbidden {
			if strings.Contains(msg, bad) {
				t.Errorf("%s: error %q contains the forbidden token %q", label, msg, bad)
			}
		}
	}

	// 23505 through Insert: same id, different dedupe key.
	obs := fullWeatherData()
	if _, err := rs.Insert(ctx, store.NewRecord{
		ID: "dup", StationID: 1, Data: obs,
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	_, insertErr := rs.Insert(ctx, store.NewRecord{ID: "dup", StationID: 2, Data: obs})
	assertOpaque("Insert conflict", insertErr)
	if !errors.Is(insertErr, store.ErrConflict) {
		t.Errorf("Insert conflict is not store.ErrConflict: %v", insertErr)
	}

	// 23505 through NewDeposit.
	d := store.Deposit{Suffix: "s", Prefix: "p", Address: "a", LockingScript: "76a9"}
	if err := ds.NewDeposit(ctx, d); err != nil {
		t.Fatalf("seeding deposit: %v", err)
	}
	assertOpaque("NewDeposit conflict", ds.NewDeposit(ctx, d))

	// ErrNoRows through Get, on both stores.
	_, getErr := rs.Get(ctx, "no-such-record")
	assertOpaque("record Get miss", getErr)
	if !errors.Is(getErr, store.ErrNotFound) {
		t.Errorf("record Get miss is not store.ErrNotFound: %v", getErr)
	}
	_, stationErr := ss.Get(ctx, 987654321)
	assertOpaque("station Get miss", stationErr)
	if !errors.Is(stationErr, store.ErrNotFound) {
		t.Errorf("station Get miss is not store.ErrNotFound: %v", stationErr)
	}

	// The CANCELLATION branch, which is the classifier's weakest and was the one
	// this test did not cover. It must stay classifiable — a client disconnect is
	// not a database fault, and the API layer distinguishes them — while carrying
	// none of the driver's text.
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, _, listErr := rs.List(canceledCtx, store.ListFilter{Limit: 1})
	assertOpaque("List on a canceled context", listErr)
	if !errors.Is(listErr, context.Canceled) {
		t.Errorf("List on a canceled context = %v, want errors.Is(…, context.Canceled)", listErr)
	}

	// A deadline that has already passed takes the same branch through a
	// different sentinel.
	expiredCtx, cancelExpired := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancelExpired()
	_, deadlineErr := rs.Get(expiredCtx, "anything")
	assertOpaque("Get past its deadline", deadlineErr)
	if !errors.Is(deadlineErr, context.DeadlineExceeded) {
		t.Errorf("Get past its deadline = %v, want errors.Is(…, context.DeadlineExceeded)", deadlineErr)
	}
}

// TestUnauthenticatedVerifyPathCannotTouchNonCompletedRows is the §6.0
// parity assertion for the one WRITE an unauthenticated caller can trigger.
//
// POST /api/verify lets the caller choose WHICH txids are refreshed. Both halves
// of SetBlockHeights therefore filter on status='completed': the record UPDATE
// and — the half that was missing — setStationHeightSQL's subquery. Without the
// second filter, knowing the txid of an ABORTED transaction was enough to raise
// a station's displayed last_block_height, which on a proof-of-existence demo is
// data forgery through a public endpoint.
func TestUnauthenticatedVerifyPathCannotTouchNonCompletedRows(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	ss := postgres.NewStationStore(pool)

	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, is_active) VALUES (77, true)"); err != nil {
		t.Fatalf("seeding station: %v", err)
	}
	// Both rows carry the SAME txid and belong to the SAME station, which is the
	// shape that makes the station half of the write observable. observation_time
	// is offset per row because (station_id, observation_time) is unique.
	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	for i, seed := range []struct {
		id     string
		status string
		chain  string
	}{
		{"was-aborted", "failed", "aborted"},
		{"still-pending", "pending", "unmined"},
	} {
		if _, execErr := pool.Exec(ctx, `
			INSERT INTO weather_records
			  (id, station_id, timestamp, observation_time, data, status, txid, chain_status)
			VALUES ($1, 77, $2, $2, $3, $4, 'tx-shared', $5)`,
			seed.id, obs.Add(time.Duration(i)*time.Minute), fullWeatherData(),
			seed.status, seed.chain); execErr != nil {
			t.Fatalf("seeding %s: %v", seed.id, execErr)
		}
	}

	if err := rs.SetBlockHeights(ctx, []store.BlockHeightUpdate{
		{TxID: "tx-shared", BlockHeight: 900999},
	}); err != nil {
		t.Fatalf("SetBlockHeights: %v", err)
	}

	for _, id := range []string{"was-aborted", "still-pending"} {
		rec, err := rs.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if rec.BlockHeight != nil {
			t.Errorf("%s got block height %v; only completed rows may be marked mined", id, rec.BlockHeight)
		}
	}
	st, err := ss.Get(ctx, 77)
	if err != nil {
		t.Fatalf("Get station: %v", err)
	}
	if st.LastBlockHeight != nil {
		t.Fatalf("stations.last_block_height = %v from txids on non-completed rows, want nil",
			st.LastBlockHeight)
	}
}

// TestSimpleProtocolIsRejectedWithARealDSN re-asserts the ban against the DSN
// the rest of the suite uses, so the assertion is proven on the shape that
// actually ships rather than only on a synthetic string.
func TestSimpleProtocolIsRejectedWithARealDSN(t *testing.T) {
	dsn := storetest.RequireDSN(t)

	// The real DSN must be acceptable.
	if _, err := postgres.PoolConfig(dsn); err != nil {
		t.Fatalf("PoolConfig on the CI DSN: %v", err)
	}

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	poisoned := dsn + sep + "default_query_exec_mode=simple_protocol"
	if _, err := postgres.PoolConfig(poisoned); !errors.Is(err, postgres.ErrSimpleProtocol) {
		t.Fatalf("PoolConfig on a poisoned DSN = %v, want postgres.ErrSimpleProtocol", err)
	}
	if _, err := postgres.NewPool(context.Background(), poisoned); !errors.Is(err, postgres.ErrSimpleProtocol) {
		t.Fatalf("NewPool on a poisoned DSN = %v, want postgres.ErrSimpleProtocol", err)
	}
}

// TestAdversarialInputReachesNoSQLText runs hostile strings through every
// public read path. Under the extended query protocol none of them can alter a
// statement, and this asserts the observable consequence: an answer, never an
// error and never a different query.
func TestAdversarialInputReachesNoSQLText(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()
	rs := postgres.NewRecordStore(pool)
	ss := postgres.NewStationStore(pool)

	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, name, location, is_active) VALUES (1000, 'A', 'Bristol', true)"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// "\x00" is in this table for a different reason from the rest. The others
	// prove the extended query protocol keeps a value a value; the NUL byte proves
	// store.ValidText screens the ONE value the protocol cannot carry at all
	// (SQLSTATE 22021 at bind time, measured). Before that screen existed, these
	// three assertions failed on this single payload and the honest reading was not
	// "weaken the assertion" but "the search box really does 500 on %00".
	payloads := []string{
		"'; DROP TABLE weather_records; --",
		"' OR '1'='1",
		"1; DELETE FROM stations",
		"$1", "$$", "%s", "\\'", "\x00", "a\x00b", "''''",
		strings.Repeat("'", 100),
		"UNION SELECT NULL,NULL,NULL",
	}
	for _, p := range payloads {
		if _, err := rs.Get(ctx, p); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want store.ErrNotFound", p, err)
		}
		if ok, err := rs.TxIDExists(ctx, p); err != nil || ok {
			t.Errorf("TxIDExists(%q) = (%v, %v), want (false, nil)", p, ok, err)
		}
		if _, _, err := ss.List(ctx, store.StationFilter{Search: p, Limit: 50}); err != nil {
			t.Errorf("station List(search=%q) error = %v, want nil", p, err)
		}
	}

	// The tables are all still there.
	var records, stations int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM weather_records").Scan(&records); err != nil {
		t.Fatalf("counting records: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM stations").Scan(&stations); err != nil {
		t.Fatalf("counting stations: %v", err)
	}
	if stations != 1 {
		t.Fatalf("stations = %d after the payloads, want 1", stations)
	}
	if records != 0 {
		t.Fatalf("records = %d, want 0", records)
	}
}
