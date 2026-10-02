// Package config holds process configuration.
//
// config.go is the only place in the tree that reads os.Getenv. Load reads
// every variable of spec §8.15 and applies its documented default when the
// variable is unset; it performs no validation. Validate (a later task)
// applies the numbered rules against the resulting Config.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Subcommand selects which validation rules apply. Spec §8.15's subcommand
// column key.
type Subcommand string

const (
	SubServe          Subcommand = "serve"
	SubDepositAddress Subcommand = "deposit-address"
	SubDeposit        Subcommand = "deposit"
	SubRequeue        Subcommand = "requeue"
	SubStatsRecompute Subcommand = "stats-recompute"
	SubPreflight      Subcommand = "preflight"
)

// Config is the whole configuration surface. One flat struct, spec §8.15's
// inventory in the same order, and the only thing in the tree that env vars
// reach.
type Config struct {
	ServerPrivateKey Secret
	PostgresPassword Secret
	TempestAPIKey    Secret

	BSVNetwork       string
	WalletStorageURL string

	APIPort int
	OpsPort int

	PGHost     string
	PGPort     int
	PGUser     string
	PGDatabase string
	PGSSLMode  string

	DenominationSatoshis          int
	FanoutOutputsPerTx            int
	FuelTargetPoolSize            int
	FuelInterval                  time.Duration
	FuelFanoutMaxTxsPerRound      int
	WeatherOutputsPerTx           int
	WeatherMaxScriptBytes         int
	WeatherAllowMulticlaimScripts bool

	PollRate          time.Duration
	ProcessorInterval time.Duration
	ProcessingLease   time.Duration
	ReconcileInterval time.Duration

	TrustedProxyCIDRs    []string
	ProofRateLimitPerMin int
	LogLevel             string

	// LowWaterPercent and HighWaterPercent bound the fuel pool's resolved
	// water band (rule 16). They are deliberately NOT environment
	// variables — spec §8.15 has no corresponding entries — and are set to
	// their inherited defaults 60 and 85 by Load.
	LowWaterPercent  int
	HighWaterPercent int

	// parseErrs collects malformed-value errors from Load so Validate can
	// return them alongside the rule failures. A malformed int must never be
	// silently replaced by the default — that is the FUNDING_BACKET_MIN class
	// of defect spec §5 row 10 names.
	parseErrs []error
}

// ParseErrors returns every malformed-value error Load recorded, joined, or
// nil if every set variable parsed cleanly.
func (c *Config) ParseErrors() error {
	return errors.Join(c.parseErrs...)
}

// Load reads every variable of spec §8.15 from the environment, applying the
// documented default when a variable is UNSET and recording a parse error when
// a variable is set but malformed. It performs no validation; call Validate.
func Load() *Config {
	c := &Config{}

	c.ServerPrivateKey = Secret(envString("SERVER_PRIVATE_KEY", ""))
	c.PostgresPassword = Secret(envString("POSTGRES_PASSWORD", ""))
	c.TempestAPIKey = Secret(envString("TEMPEST_API_KEY", ""))

	c.BSVNetwork = envString("BSV_NETWORK", "test")
	c.WalletStorageURL = envString("WALLET_STORAGE_URL", "")

	c.APIPort = envInt(c, "API_PORT", 3001)
	c.OpsPort = envInt(c, "OPS_PORT", 9090)

	c.PGHost = envString("PG_HOST", "postgres")
	c.PGPort = envInt(c, "PG_PORT", 5432)
	c.PGUser = envString("PG_USER", "weather")
	c.PGDatabase = envString("PG_DATABASE", "weather")
	c.PGSSLMode = envString("PG_SSLMODE", "disable")

	c.DenominationSatoshis = envInt(c, "DENOMINATION_SATOSHIS", 50)
	c.FanoutOutputsPerTx = envInt(c, "FANOUT_OUTPUTS_PER_TX", 100)
	c.FuelTargetPoolSize = envInt(c, "FUEL_TARGET_POOL_SIZE", 1000)
	c.FuelInterval = envDuration(c, "FUEL_INTERVAL", time.Minute)
	c.FuelFanoutMaxTxsPerRound = envInt(c, "FUEL_FANOUT_MAX_TXS_PER_ROUND", 12)
	c.WeatherOutputsPerTx = envInt(c, "WEATHER_OUTPUTS_PER_TX", 21)
	c.WeatherMaxScriptBytes = envInt(c, "WEATHER_MAX_SCRIPT_BYTES", 297)
	c.WeatherAllowMulticlaimScripts = envBool(c, "WEATHER_ALLOW_MULTICLAIM_SCRIPTS", false)

	c.PollRate = envDuration(c, "POLL_RATE", 300*time.Second)
	c.ProcessorInterval = envDuration(c, "PROCESSOR_INTERVAL", 3*time.Second)
	c.ProcessingLease = envDuration(c, "PROCESSING_LEASE", 5*time.Minute)
	c.ReconcileInterval = envDuration(c, "RECONCILE_INTERVAL", 5*time.Minute)

	c.TrustedProxyCIDRs = splitTrimmed(envString("TRUSTED_PROXY_CIDRS", ""))
	c.ProofRateLimitPerMin = envInt(c, "PROOF_RATE_LIMIT_PER_MIN", 60)
	c.LogLevel = envString("LOG_LEVEL", "info")

	// Not environment variables; see the field doc comment.
	c.LowWaterPercent = 60
	c.HighWaterPercent = 85

	return c
}

// splitTrimmed splits s on "," trimming each entry, and drops blank entries
// so an unset or empty variable yields an empty slice rather than one blank
// entry.
func splitTrimmed(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// envString returns the raw value of name, or def when name is unset.
func envString(name, def string) string {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	return v
}

// envInt returns the parsed int value of name, or def when name is unset or
// malformed. A malformed value appends to c.parseErrs.
func envInt(c *Config, name string, def int) int {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		c.parseErrs = append(c.parseErrs, fmt.Errorf("%s: not a valid int", name))
		return def
	}
	return n
}

// envDuration returns the parsed duration value of name, or def when name is
// unset or malformed. A malformed value appends to c.parseErrs.
func envDuration(c *Config, name string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		c.parseErrs = append(c.parseErrs, fmt.Errorf("%s: not a valid duration", name))
		return def
	}
	return d
}

// envBool returns the parsed bool value of name, or def when name is unset or
// malformed. A malformed value appends to c.parseErrs.
func envBool(c *Config, name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		c.parseErrs = append(c.parseErrs, fmt.Errorf("%s: not a valid bool", name))
		return def
	}
	return b
}
