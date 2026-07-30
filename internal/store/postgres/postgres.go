package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// New assembles the aggregate seam over one pool.
//
// Health is the pool itself: *pgxpool.Pool.Ping is exactly store.Pinger, and it
// is what /api/ready calls. Note the deliberate asymmetry in how a failing
// Ping is handled upstream — the database being unreachable must remove the pod
// from the Service (readiness) and must NEVER restart it (liveness), because
// Postgres is a single-replica Recreate deployment and every node drain or
// image bump makes it briefly unreachable.
func New(pool *pgxpool.Pool) store.Store {
	return store.Store{
		Records:   NewRecordStore(pool),
		Stations:  NewStationStore(pool),
		Deposits:  NewDepositStore(pool),
		Preflight: NewPreflightStore(pool),
		Health:    pool,
	}
}

// AllQueries returns every statement this package executes, keyed by the name
// of its constant with the SQL suffix removed.
//
// It is exported for exactly one reason: a test PREPAREs all of them against a
// real server, which is the mechanical proof that each is static, parameterized
// SQL with contiguous placeholders and valid syntax. That proof is necessary
// because gosec's SQL rules were MEASURED not to fire on pgx sinks at all — a
// runtime concatenation into (*sql.DB).ExecContext raises G202, while the
// identical concatenation into (*pgxpool.Pool).Exec raises nothing. A green
// lint is therefore not evidence.
//
// sqldiscipline_test.go asserts that this map is COMPLETE: every `const
// <name>SQL` declared in the package must appear here, so a new statement
// cannot be added without also being prepared.
//
// migrations.sql is deliberately absent. It is a zero-argument multi-statement
// DDL script, so it cannot be prepared, and it has nothing to interpolate.
func AllQueries() map[string]string {
	return map[string]string{
		// records.go
		"insertRecord":        insertRecordSQL,
		"claim":               claimSQL,
		"completeRecords":     completeRecordsSQL,
		"bumpAppStats":        bumpAppStatsSQL,
		"bumpStations":        bumpStationsSQL,
		"stats":               statsSQL,
		"failPermanent":       failPermanentSQL,
		"requeueInfra":        requeueInfraSQL,
		"markUnknown":         markUnknownSQL,
		"reapExpired":         reapExpiredSQL,
		"requeueCount":        requeueCountSQL,
		"requeue":             requeueSQL,
		"listRecords":         listRecordsSQL,
		"countRecords":        countRecordsSQL,
		"getRecord":           getRecordSQL,
		"txIDExists":          txIDExistsSQL,
		"snapshot":            snapshotSQL,
		"setBlockHeight":      setBlockHeightSQL,
		"setStationHeight":    setStationHeightSQL,
		"reconcileCandidates": reconcileCandidatesSQL,

		// stations.go
		"upsertStation":       upsertStationSQL,
		"getStation":          getStationSQL,
		"listStationsAll":     listStationsAllSQL,
		"countStationsAll":    countStationsAllSQL,
		"listStationsByID":    listStationsByIDSQL,
		"countStationsByID":   countStationsByIDSQL,
		"listStationsSearch":  listStationsSearchSQL,
		"countStationsSearch": countStationsSearchSQL,

		// deposits.go
		"newDeposit":       newDepositSQL,
		"pendingDeposits":  pendingDepositsSQL,
		"markInternalized": markInternalizedSQL,
		"preflightOK":      preflightOKSQL,
		"recordPreflight":  recordPreflightSQL,
	}
}
