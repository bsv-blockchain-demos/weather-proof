package fake_test

import (
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/storetest"
)

// The fake half of the shared suite. No database, so it runs in the ordinary
// `check` job — which is the point: this is the substrate the whole read-API
// plan tests against, and it is only trustworthy while this file and its
// Postgres twin agree.
func TestFakeConformsToTheStoreContract(t *testing.T) {
	storetest.RunStoreConformance(t, "fake", func(_ *testing.T) store.Store {
		return fake.New().Store()
	})
}
