package postgres_test

import (
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/postgres"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

// The Postgres half. Fresh drops, recreates and re-migrates the package schema,
// so each subtest that asks for a store gets an empty database — and no subtest
// may call t.Parallel(), which RunStoreConformance does not.
func TestPostgresConformsToTheStoreContract(t *testing.T) {
	storetest.RunStoreConformance(t, "postgres", func(sub *testing.T) store.Store {
		return postgres.New(storetest.Fresh(sub, storeSchema))
	})
}
