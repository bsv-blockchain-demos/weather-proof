package config

import (
	"errors"
	"fmt"
	"strings"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

// errFuelMathUnavailable is the sentinel behind every rule 8/10/12/13
// failure. internal/fuelmath does not exist yet, so those four rules cannot
// be evaluated at all; Validate reports that fact rather than skipping it,
// which fails serve and preflight closed until the fuel plan lands. See the
// package doc comment on ruleFuelMathUnavailable.
var errFuelMathUnavailable = errors.New("fuel shape rules not yet implemented (internal/fuelmath does not exist)")

// validBSVNetworks is the enum for rule 4.
var validBSVNetworks = map[string]bool{"main": true, "test": true}

// validPGSSLModes is the enum for rule 7's PG_SSLMODE clause.
var validPGSSLModes = map[string]bool{"disable": true, "require": true}

// validLogLevels is the enum for rule 19.
var validLogLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// ruleSets maps each Subcommand to the rule functions spec §8.15's
// "Required for" column names for it. Rule 19 (log level) and rule 20 (join
// every failure, never short-circuit) apply universally: 19 is included in
// every slice below, and 20 is not a rule function at all — it is Validate's
// own aggregation behavior. Adding a new rule is a matter of writing a new
// ruleN method and appending it to whichever subcommands' slices need it;
// Task 3's rules 17 and 18 slot in the same way.
var ruleSets = map[Subcommand][]func(*Config) error{
	SubServe: {
		(*Config).rule1ServerKey,
		(*Config).rule1PostgresPassword,
		(*Config).rule2,
		(*Config).rule3Serve,
		(*Config).rule4,
		(*Config).rule5,
		(*Config).rule6,
		(*Config).rule7,
		(*Config).ruleFuelMathUnavailable,
		(*Config).rule9,
		(*Config).rule11,
		(*Config).rule14,
		(*Config).rule15,
		(*Config).rule16,
		(*Config).rule17,
		(*Config).rule18,
		(*Config).rule19,
	},
	SubDepositAddress: {
		(*Config).rule1ServerKey,
		(*Config).rule1PostgresPassword,
		(*Config).rule2,
		(*Config).rule4,
		(*Config).rule5,
		(*Config).rule7,
		(*Config).rule19,
	},
	SubDeposit: {
		(*Config).rule1ServerKey,
		(*Config).rule1PostgresPassword,
		(*Config).rule2,
		(*Config).rule4,
		(*Config).rule5,
		(*Config).rule7,
		(*Config).rule19,
	},
	SubRequeue: {
		(*Config).rule1PostgresPassword,
		(*Config).rule7,
		(*Config).rule14,
		(*Config).rule19,
	},
	SubStatsRecompute: {
		(*Config).rule1PostgresPassword,
		(*Config).rule7,
		(*Config).rule19,
	},
	SubPreflight: {
		(*Config).rule1ServerKey,
		(*Config).rule1PostgresPassword,
		(*Config).rule2,
		(*Config).rule4,
		(*Config).rule5,
		(*Config).ruleFuelMathUnavailable,
		(*Config).rule9,
		(*Config).rule19,
	},
}

// Validate evaluates every rule of spec §8.15 that sub's required set can
// satisfy and returns ALL failures joined, never the first (rule 20). No
// returned error string contains any configured value.
func Validate(c *Config, sub Subcommand) error {
	var errs []error

	if err := c.ParseErrors(); err != nil {
		errs = append(errs, err)
	}

	for _, rule := range ruleSets[sub] {
		if err := rule(c); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// rule1ServerKey is spec rule 1's SERVER_PRIVATE_KEY half: the value must be
// present.
func (c *Config) rule1ServerKey() error {
	if c.ServerPrivateKey.Reveal() == "" {
		return errors.New("SERVER_PRIVATE_KEY: required, not set")
	}
	return nil
}

// rule1PostgresPassword is spec rule 1's POSTGRES_PASSWORD half: the value
// must be present.
func (c *Config) rule1PostgresPassword() error {
	if c.PostgresPassword.Reveal() == "" {
		return errors.New("POSTGRES_PASSWORD: required, not set")
	}
	return nil
}

// rule2 requires SERVER_PRIVATE_KEY to be a well-formed private key. The
// check is deliberately network-independent: BSV private key encoding does
// not vary by network, so this rule parses the hex with go-sdk's
// PrivateKeyFromHex and stops there. It does not, and must not, cross-check
// the key against BSVNetwork — that would be a fabricated constraint the
// spec does not impose. Skips when the key is empty; that case is rule 1's.
func (c *Config) rule2() error {
	key := c.ServerPrivateKey.Reveal()
	if key == "" {
		return nil
	}
	if _, err := ec.PrivateKeyFromHex(key); err != nil {
		return errors.New("SERVER_PRIVATE_KEY: not a well-formed private key")
	}
	return nil
}

// rule3Serve requires TEMPEST_API_KEY only for SubServe; other subcommands
// never evaluate this rule at all (it is absent from their ruleSets entry).
func (c *Config) rule3Serve() error {
	if c.TempestAPIKey.Reveal() == "" {
		return errors.New("TEMPEST_API_KEY: required, not set")
	}
	return nil
}

// rule4 requires BSV_NETWORK to be one of the known enum values.
func (c *Config) rule4() error {
	if !validBSVNetworks[c.BSVNetwork] {
		return errors.New("BSV_NETWORK: must be one of the known network names")
	}
	return nil
}

// rule5 requires WALLET_STORAGE_URL to be a bare https host: scheme https,
// no path beyond "/".
func (c *Config) rule5() error {
	u := c.WalletStorageURL
	if !strings.HasPrefix(u, "https://") {
		return errors.New("WALLET_STORAGE_URL: must use https")
	}
	rest := strings.TrimPrefix(u, "https://")
	if strings.ContainsAny(rest, "/") {
		return errors.New("WALLET_STORAGE_URL: must be a bare host with no path")
	}
	return nil
}

// rule6 requires API_PORT and OPS_PORT to be distinct and each in the valid
// TCP port range.
func (c *Config) rule6() error {
	var errs []error
	if c.APIPort < 1 || c.APIPort > 65535 {
		errs = append(errs, errors.New("API_PORT: must be between 1 and 65535"))
	}
	if c.OpsPort < 1 || c.OpsPort > 65535 {
		errs = append(errs, errors.New("OPS_PORT: must be between 1 and 65535"))
	}
	if c.APIPort == c.OpsPort {
		errs = append(errs, errors.New("API_PORT, OPS_PORT: must be distinct"))
	}
	return errors.Join(errs...)
}

// rule7 requires a complete Postgres DSN: non-blank PG_HOST, PG_USER,
// PG_DATABASE, a positive PG_PORT, and a known PG_SSLMODE.
func (c *Config) rule7() error {
	var errs []error
	if c.PGHost == "" {
		errs = append(errs, errors.New("PG_HOST: required, not set"))
	}
	if c.PGUser == "" {
		errs = append(errs, errors.New("PG_USER: required, not set"))
	}
	if c.PGDatabase == "" {
		errs = append(errs, errors.New("PG_DATABASE: required, not set"))
	}
	if c.PGPort < 1 || c.PGPort > 65535 {
		errs = append(errs, errors.New("PG_PORT: must be between 1 and 65535"))
	}
	if !validPGSSLModes[c.PGSSLMode] {
		errs = append(errs, errors.New("PG_SSLMODE: must be a known SSL mode"))
	}
	return errors.Join(errs...)
}

// ruleFuelMathUnavailable is the single named failure standing in for spec
// rules 8, 10, 12 and 13, all of which need internal/fuelmath to evaluate
// their fuel-shape predicates. That package does not exist yet. Rather than
// stub it, or silently skip these rules, Validate reports them as
// unevaluated whenever sub's required set needs any of them — which fails
// serve and preflight closed, exactly what rule 8 exists to guarantee. When
// the fuel plan lands, this function is deleted and replaced by four real
// rule methods wired into ruleSets in its place.
func (c *Config) ruleFuelMathUnavailable() error {
	return fmt.Errorf("rules 8, 10, 12, 13 (fuel shape): %w", errFuelMathUnavailable)
}

// rule9 requires FANOUT_OUTPUTS_PER_TX to be exactly 100.
func (c *Config) rule9() error {
	if c.FanoutOutputsPerTx != 100 {
		return errors.New("FANOUT_OUTPUTS_PER_TX: must be 100")
	}
	return nil
}

// rule11 requires FUEL_FANOUT_MAX_TXS_PER_ROUND to be between 1 and 25
// inclusive.
func (c *Config) rule11() error {
	if c.FuelFanoutMaxTxsPerRound < 1 || c.FuelFanoutMaxTxsPerRound > 25 {
		return errors.New("FUEL_FANOUT_MAX_TXS_PER_ROUND: must be between 1 and 25")
	}
	return nil
}

// rule14 requires PROCESSING_LEASE to exceed the create-action timeout,
// fixed at 1 minute in this plan.
func (c *Config) rule14() error {
	const createActionTimeout = 60 // seconds, spelled out to avoid importing time solely for a constant literal.
	if c.ProcessingLease.Seconds() <= createActionTimeout {
		return errors.New("PROCESSING_LEASE: must exceed the create-action timeout")
	}
	return nil
}

// rule15 requires FUEL_INTERVAL, PROCESSOR_INTERVAL and RECONCILE_INTERVAL
// to be strictly positive, POLL_RATE to be at least 60 seconds, and
// RECONCILE_INTERVAL to exceed PROCESSOR_INTERVAL.
func (c *Config) rule15() error {
	var errs []error
	if c.FuelInterval <= 0 {
		errs = append(errs, errors.New("FUEL_INTERVAL: must be positive"))
	}
	if c.ProcessorInterval <= 0 {
		errs = append(errs, errors.New("PROCESSOR_INTERVAL: must be positive"))
	}
	if c.ReconcileInterval <= 0 {
		errs = append(errs, errors.New("RECONCILE_INTERVAL: must be positive"))
	}
	if c.PollRate.Seconds() < 60 {
		errs = append(errs, errors.New("POLL_RATE: must be at least 60s"))
	}
	if c.ReconcileInterval > 0 && c.ProcessorInterval > 0 && c.ReconcileInterval <= c.ProcessorInterval {
		errs = append(errs, errors.New("RECONCILE_INTERVAL: must exceed PROCESSOR_INTERVAL"))
	}
	return errors.Join(errs...)
}

// rule16 requires the resolved water band to be non-inverted: LowWaterPercent
// below HighWaterPercent. LowWaterPercent and HighWaterPercent are not
// environment variables (see their field doc comment); this rule reads
// whatever Load resolved them to, including the inherited defaults 60/85.
func (c *Config) rule16() error {
	if c.LowWaterPercent >= c.HighWaterPercent {
		return errors.New("water band: low water percent must be below high water percent")
	}
	return nil
}

// rule17 requires TRUSTED_PROXY_CIDRS to parse: every entry must be a valid
// CIDR. The parsed value itself is not kept here — main builds it once via
// ParseCIDRList and hands it to the limiter — this rule only proves
// parseability so serve fails closed on a malformed ConfigMap entry.
func (c *Config) rule17() error {
	if _, err := ParseCIDRList(c.TrustedProxyCIDRs); err != nil {
		return err
	}
	return nil
}

// rule18 requires PROOF_RATE_LIMIT_PER_MIN to be strictly positive.
func (c *Config) rule18() error {
	if c.ProofRateLimitPerMin < 1 {
		return errors.New("PROOF_RATE_LIMIT_PER_MIN: must be at least 1")
	}
	return nil
}

// rule19 requires LOG_LEVEL to be one of the four known slog levels.
func (c *Config) rule19() error {
	if !validLogLevels[c.LogLevel] {
		return errors.New("LOG_LEVEL: must be one of debug, info, warn, error")
	}
	return nil
}
