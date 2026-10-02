package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

// storeSchema is this package's private Postgres schema. See
// storetest.Schema's doc comment for why all three strings are spelled out
// and why no test in this package may call t.Parallel().
var storeSchema = storetest.Schema{
	Name:      "wp_test_store",
	CreateSQL: "CREATE SCHEMA IF NOT EXISTS wp_test_store",
	DropSQL:   "DROP SCHEMA IF EXISTS wp_test_store CASCADE",
}

// sqlState returns the SQLSTATE of err, or "" if err is not a Postgres error.
func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := storetest.Pool(t, storeSchema, 4)
	ctx := context.Background()

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("third Migrate: %v", err)
	}

	// The NAMES, for exactly the reason spelled out for the indexes below: the
	// count was an opaque 5 that had to be bumped by hand, and adding
	// completed_txids produced "tables = 6, want 5" — a message that names every
	// table EXCEPT the one that changed. Comparing names means one edit in one
	// place and a failure that says which table appeared or vanished.
	tableRows, err := pool.Query(ctx,
		"SELECT tablename FROM pg_tables WHERE schemaname = $1 ORDER BY tablename", storeSchema.Name)
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	gotTables, err := pgx.CollectRows(tableRows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collecting table names: %v", err)
	}
	wantTables := []string{
		"app_preflight",
		"app_stats",
		"completed_txids",
		"deposits",
		"stations",
		"weather_records",
	}
	if len(gotTables) != len(wantTables) {
		t.Fatalf("tables = %v (%d), want %v (%d)", gotTables, len(gotTables), wantTables, len(wantTables))
	}
	for i := range wantTables {
		if gotTables[i] != wantTables[i] {
			t.Fatalf("tables = %v, want %v", gotTables, wantTables)
		}
	}

	// The NAMES, not a count. A count is an opaque integer that has to be
	// bumped by hand and was wrong in an earlier draft of this plan (it said 8,
	// double-counting ux_records_station_obs as both the dedupe index and one of
	// the record indexes, while the migration creates 7). Comparing names means
	// adding an index forces exactly one edit in exactly one place, and the
	// failure message says which index is missing rather than "7 != 8".
	//
	// Note the parentheses around the OR: `A AND B OR A AND C` happens to parse
	// correctly here because AND binds tighter, but writing it out is how the
	// next person avoids finding out the hard way.
	rows, err := pool.Query(ctx, `
		SELECT indexname FROM pg_indexes
		 WHERE schemaname = $1
		   AND (indexname LIKE 'ix_%' OR indexname LIKE 'ux_%')
		 ORDER BY indexname`, storeSchema.Name)
	if err != nil {
		t.Fatalf("listing indexes: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collecting index names: %v", err)
	}
	want := []string{
		"ix_records_list",
		"ix_records_reconcile",
		"ix_records_station_created",
		"ix_records_status_created",
		"ix_records_txid",
		"ix_stations_search",
		"ux_records_station_obs",
	}
	if len(got) != len(want) {
		t.Fatalf("named indexes = %v (%d), want %v (%d: 5 record + 1 unique dedupe + 1 station GIN)",
			got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("named indexes = %v, want %v", got, want)
		}
	}
}

func TestAppStatsIsASingleton(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	var rows, id int
	if err := pool.QueryRow(ctx, "SELECT count(*), min(id) FROM app_stats").Scan(&rows, &id); err != nil {
		t.Fatalf("reading app_stats: %v", err)
	}
	if rows != 1 || id != 1 {
		t.Fatalf("app_stats has %d rows with min id %d, want 1 row with id 1", rows, id)
	}

	// A second id is structurally impossible.
	_, err := pool.Exec(ctx, "INSERT INTO app_stats (id) VALUES (2)")
	if got := sqlState(err); got != "23514" {
		t.Fatalf("inserting app_stats id=2 gave SQLSTATE %q (err %v), want 23514", got, err)
	}
}

func TestStatusCheckConstraintRejectsAnUnknownStatus(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
		VALUES ('x', 1, now(), now(), '{}'::jsonb, 'Pending')`)
	if got := sqlState(err); got != "23514" {
		t.Fatalf("status 'Pending' gave SQLSTATE %q (err %v), want 23514", got, err)
	}

	for _, bad := range []string{"", "PENDING", "unknown", "arc-accepted"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
			VALUES ($1, 1, now(), now(), '{}'::jsonb, $2)`, "id-"+bad, bad)
		if got := sqlState(err); got != "23514" {
			t.Errorf("status %q gave SQLSTATE %q, want 23514", bad, got)
		}
	}

	for _, good := range []string{"pending", "processing", "failed"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, claimed_at)
			VALUES ($1, 1, now(), $2, '{}'::jsonb, $3, now())`,
			"ok-"+good, time.Now().Add(time.Duration(len(good))*time.Second), good)
		if err != nil {
			t.Errorf("status %q was rejected: %v", good, err)
		}
	}
}

func TestProcessingRequiresALease(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	// This is the constraint that makes the TypeScript's claim shape —
	// SELECT ids, then UPDATE ... SET status='processing' with no claimed_at —
	// structurally unwritable, and it makes the reaper's
	// `claimed_at < now() - lease` predicate total so no defensive
	// `OR claimed_at IS NULL` is needed anywhere.
	_, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status)
		VALUES ('a', 1, now(), now(), '{}'::jsonb, 'processing')`)
	if got := sqlState(err); got != "23514" {
		t.Fatalf("processing without claimed_at gave SQLSTATE %q (err %v), want 23514", got, err)
	}

	if _, insertErr := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
		VALUES ('b', 1, now(), now(), '{}'::jsonb)`); insertErr != nil {
		t.Fatalf("inserting a pending row: %v", insertErr)
	}
	_, err = pool.Exec(ctx, "UPDATE weather_records SET status='processing' WHERE id='b'")
	if got := sqlState(err); got != "23514" {
		t.Fatalf("naive claim UPDATE gave SQLSTATE %q (err %v), want 23514", got, err)
	}
}

func TestCompletedRequiresPublicationColumns(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	cases := []struct {
		name string
		sql  string
	}{
		{"no txid", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, output_index, processed_at)
			VALUES ('a', 1, now(), now(), '{}'::jsonb, 'completed', 0, now())`},
		{"no output_index", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, txid, processed_at)
			VALUES ('b', 1, now(), now(), '{}'::jsonb, 'completed', 'tx', now())`},
		{"no processed_at", `INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, txid, output_index)
			VALUES ('c', 1, now(), now(), '{}'::jsonb, 'completed', 'tx', 0)`},
	}
	for _, c := range cases {
		_, err := pool.Exec(ctx, c.sql)
		if got := sqlState(err); got != "23514" {
			t.Errorf("%s: SQLSTATE %q (err %v), want 23514", c.name, got, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, status, txid, output_index, processed_at)
		VALUES ('ok', 1, now(), now(), '{}'::jsonb, 'completed', 'tx', 0, now())`); err != nil {
		t.Fatalf("a fully published completed row was rejected: %v", err)
	}
}

func TestChainStatusCheckConstraint(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	for _, good := range []string{"arc-accepted", "unmined", "mined", "aborted"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, chain_status)
			VALUES ($1, 1, now(), $2, '{}'::jsonb, $3)`,
			"g-"+good, time.Now().Add(time.Duration(len(good))*time.Second), good)
		if err != nil {
			t.Errorf("chain_status %q was rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"", "MINED", "arc_accepted", "pending"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data, chain_status)
			VALUES ($1, 2, now(), now(), '{}'::jsonb, $2)`, "b-"+bad, bad)
		if got := sqlState(err); got != "23514" {
			t.Errorf("chain_status %q gave SQLSTATE %q, want 23514", bad, got)
		}
	}
}

func TestDedupeIndexRaisesAUniqueViolation(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	obs := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
		VALUES ('a', 1000, $1, $1, '{}'::jsonb)`, obs); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
		VALUES ('b', 1000, $1, $1, '{}'::jsonb)`, obs)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("duplicate (station_id, observation_time) error = %v, want a *pgconn.PgError", err)
	}
	if pgErr.Code != "23505" {
		t.Fatalf("SQLSTATE = %q, want 23505", pgErr.Code)
	}
	if pgErr.ConstraintName != "ux_records_station_obs" {
		t.Fatalf("ConstraintName = %q, want ux_records_station_obs", pgErr.ConstraintName)
	}
}

func TestStationSearchVectorIsGenerated(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	// The two-argument to_tsvector('english', ...) is IMMUTABLE, which is what
	// makes a generated column legal. The one-argument form is only STABLE and
	// would be rejected at CREATE TABLE, so this also proves the migration did
	// not get "simplified".
	if _, err := pool.Exec(ctx,
		"INSERT INTO stations (station_id, name, location) VALUES (7, 'Harbor Mast', 'Bristol Docks')"); err != nil {
		t.Fatalf("insert station: %v", err)
	}
	var tsv string
	if err := pool.QueryRow(ctx, "SELECT search_tsv::text FROM stations WHERE station_id = 7").Scan(&tsv); err != nil {
		t.Fatalf("reading search_tsv: %v", err)
	}
	if tsv == "" {
		t.Fatal("search_tsv is empty; the generated column did not populate")
	}
	var hit int
	err := pool.QueryRow(ctx,
		"SELECT count(*) FROM stations WHERE search_tsv @@ websearch_to_tsquery('english', $1)", "bristol").Scan(&hit)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hit != 1 {
		t.Fatalf("search for bristol matched %d rows, want 1", hit)
	}
}

func TestUUIDv7IsNotAvailableButGenRandomUUIDIs(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	// uuidv7() is a PostgreSQL 18 builtin. The design specifies uuidv7 record
	// ids, so they MUST be generated in Go with uuid.NewV7(); a DB-side default
	// would make the migration fail outright on the pinned 17-alpine image.
	_, err := pool.Exec(ctx, "SELECT uuidv7()")
	if got := sqlState(err); got != "42883" {
		t.Fatalf("SELECT uuidv7() gave SQLSTATE %q (err %v), want 42883 undefined_function", got, err)
	}

	// gen_random_uuid() is core in PG 13+ and needs no pgcrypto, which is why
	// the migration must NOT contain CREATE EXTENSION pgcrypto (it can fail
	// outright on a locked-down managed server).
	var u string
	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&u); err != nil {
		t.Fatalf("gen_random_uuid(): %v", err)
	}
	if len(u) != 36 {
		t.Fatalf("gen_random_uuid() = %q, want a 36-character uuid", u)
	}

	var exts int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_extension WHERE extname = 'pgcrypto'").Scan(&exts); err != nil {
		t.Fatalf("checking pg_extension: %v", err)
	}
	if exts != 0 {
		t.Fatal("pgcrypto is installed; the migration must not create it")
	}
}

func TestCreatedAtIsTheTransactionTimestamp(t *testing.T) {
	pool := storetest.Fresh(t, storeSchema)
	ctx := context.Background()

	// This is why every ordered record query needs the id tiebreaker: now() is
	// transaction_timestamp(), so a whole poll batch shares ONE created_at and
	// `ORDER BY created_at DESC` alone is a non-total order.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	base := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	for i := range 19 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO weather_records (id, station_id, timestamp, observation_time, data)
			VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
			"r"+string(rune('a'+i)), int64(1000+i), base, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var distinct int
	if err := pool.QueryRow(ctx, "SELECT count(DISTINCT created_at) FROM weather_records").Scan(&distinct); err != nil {
		t.Fatalf("counting distinct created_at: %v", err)
	}
	if distinct != 1 {
		t.Fatalf("count(DISTINCT created_at) = %d over one transaction, want 1", distinct)
	}
}
