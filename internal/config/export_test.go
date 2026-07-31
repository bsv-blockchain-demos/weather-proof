package config

import (
	"os"
	"testing"
)

// specEnvVars is every variable Load reads — spec §8.15's inventory in full.
// Kept as one list so ClearAmbientEnv cannot fall behind Load: a variable added
// to Load and not added here reintroduces exactly the leak ClearAmbientEnv
// exists to close.
var specEnvVars = []string{
	"API_PORT",
	"BSV_NETWORK",
	"DENOMINATION_SATOSHIS",
	"FANOUT_OUTPUTS_PER_TX",
	"FUEL_FANOUT_MAX_TXS_PER_ROUND",
	"FUEL_INTERVAL",
	"FUEL_TARGET_POOL_SIZE",
	"LOG_LEVEL",
	"OPS_PORT",
	"PG_DATABASE",
	"PG_HOST",
	"PG_PORT",
	"PG_SSLMODE",
	"PG_USER",
	"POLL_RATE",
	"POSTGRES_PASSWORD",
	"PROCESSING_LEASE",
	"PROCESSOR_INTERVAL",
	"PROOF_RATE_LIMIT_PER_MIN",
	"RECONCILE_INTERVAL",
	"SERVER_PRIVATE_KEY",
	"TEMPEST_API_KEY",
	"TRUSTED_PROXY_CIDRS",
	"WALLET_STORAGE_URL",
	"WEATHER_ALLOW_MULTICLAIM_SCRIPTS",
	"WEATHER_MAX_SCRIPT_BYTES",
	"WEATHER_OUTPUTS_PER_TX",
}

// ClearAmbientEnv unsets every §8.15 variable for the duration of t and
// restores the previous values in t.Cleanup.
//
// WHY EVERY TEST THAT CALLS Load NEEDS THIS: nothing in this package's tests
// used to clear the environment, so an exported PG_HOST, LOG_LEVEL, PG_SSLMODE
// or POSTGRES_PASSWORD — in a developer's shell, or in the integration job,
// which exports a Postgres DSN and could as easily export these — changed what
// Load returned and failed a defaults assertion for a reason that had nothing
// to do with the code under test. The failure reads as a code bug and is not
// reproducible on another machine, which is the worst shape a flake can take.
//
// It is exported so the external config_test package can use it too; both
// entry points into Load are covered.
//
// The restore is registered BEFORE any t.Setenv the caller makes, so it runs
// last: t.Cleanup is LIFO, and each t.Setenv registers its own restore.
func ClearAmbientEnv(t *testing.T) {
	t.Helper()
	for _, key := range specEnvVars {
		prev, had := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		if !had {
			continue
		}
		t.Cleanup(func() {
			if err := os.Setenv(key, prev); err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
		})
	}
}
