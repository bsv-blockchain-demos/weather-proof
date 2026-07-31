package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
)

// wellFormedPrivateKey builds a 64-hex-char value from parts, per the
// gosec G101 convention for this repository: no named credential-shaped
// literal, only a runtime concatenation.
func wellFormedPrivateKey() string {
	return "0123456789abcdef" + strings.Repeat("00", 24)
}

// validConfig returns a *Config that passes every rule sub's required set
// needs, built from Load() after t.Setenv of the two secrets and
// WALLET_STORAGE_URL.
//
// ClearAmbientEnv first, and it is not optional: every "accepts" test below
// asserts Validate() == nil against this fixture, so an exported BSV_NETWORK,
// PG_SSLMODE, LOG_LEVEL or PROOF_RATE_LIMIT_PER_MIN in the surrounding shell
// would make a rule fail for a reason the test never mentions. The fixture has
// to be a known one, not the developer's environment plus four overrides.
func validConfig(t *testing.T) *config.Config {
	t.Helper()
	config.ClearAmbientEnv(t)
	t.Setenv("SERVER_PRIVATE_KEY", wellFormedPrivateKey())
	t.Setenv("POSTGRES_PASSWORD", "fedcba9876543210"+strings.Repeat("11", 24))
	t.Setenv("WALLET_STORAGE_URL", "https://storage.example")
	t.Setenv("TEMPEST_API_KEY", "aabbccdd"+strings.Repeat("22", 24))
	return config.Load()
}

func TestRule1RejectsAMissingServerPrivateKey(t *testing.T) {
	c := validConfig(t)
	c.ServerPrivateKey = ""

	err := config.Validate(c, config.SubServe)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming SERVER_PRIVATE_KEY")
	}
	if !strings.Contains(err.Error(), "SERVER_PRIVATE_KEY") {
		t.Errorf("Validate() = %q, want it to name SERVER_PRIVATE_KEY", err.Error())
	}
}

func TestRule1RejectsAMissingPostgresPassword(t *testing.T) {
	c := validConfig(t)
	c.PostgresPassword = ""

	err := config.Validate(c, config.SubServe)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming POSTGRES_PASSWORD")
	}
	if !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
		t.Errorf("Validate() = %q, want it to name POSTGRES_PASSWORD", err.Error())
	}
}

func TestRule1AcceptsBothPresent(t *testing.T) {
	c := validConfig(t)

	// SubDeposit requires rule 1 (both halves) but never rules 8/10/12/13,
	// so it is a safe subcommand for asserting a clean nil result.
	err := config.Validate(c, config.SubDeposit)
	if err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestRule2RejectsAKeyThatIsNotAPrivateKey(t *testing.T) {
	c := validConfig(t)
	c.ServerPrivateKey = "nothex"

	err := config.Validate(c, config.SubServe)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming SERVER_PRIVATE_KEY")
	}
	if !strings.Contains(err.Error(), "SERVER_PRIVATE_KEY") {
		t.Errorf("Validate() = %q, want it to name SERVER_PRIVATE_KEY", err.Error())
	}
}

func TestRule2AcceptsAWellFormedKey(t *testing.T) {
	c := validConfig(t)
	c.ServerPrivateKey = config.Secret(wellFormedPrivateKey())

	// SubDeposit requires rule 2 but never rules 8/10/12/13.
	err := config.Validate(c, config.SubDeposit)
	if err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestRule2RejectsAnOutOfRangeScalar covers what PARSING alone accepts.
// PrivateKeyFromHex validates hex and nothing else — PrivateKeyFromBytes sets D
// with SetBytes and never range-checks it — so each value here decoded cleanly
// and passed rule 2 before the bound check existed, then misbehaved at signing
// time instead. The well-formed key is in the table as the negative control: a
// rule that rejected everything would satisfy the reject rows alone.
func TestRule2RejectsAnOutOfRangeScalar(t *testing.T) {
	// N-1 and N, built from the secp256k1 order's hex. N is the first invalid
	// scalar; N-1 is the last valid one, so the pair pins the boundary rather
	// than merely testing somewhere either side of it.
	const orderMinusOne = "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364140"
	const order = "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141"

	cases := []struct {
		name   string
		key    string
		accept bool
	}{
		{name: "zero", key: strings.Repeat("00", 32), accept: false},
		{name: "at the group order", key: order, accept: false},
		{name: "above the group order", key: strings.Repeat("ff", 32), accept: false},
		{name: "one below the group order", key: orderMinusOne, accept: true},
		{name: "short but valid hex", key: "01", accept: false},
		{name: "well formed", key: wellFormedPrivateKey(), accept: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig(t)
			c.ServerPrivateKey = config.Secret(tc.key)

			// SubDeposit requires rule 2 but never rules 8/10/12/13.
			err := config.Validate(c, config.SubDeposit)
			if tc.accept && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.accept && (err == nil || !strings.Contains(err.Error(), "SERVER_PRIVATE_KEY")) {
				t.Fatalf("Validate() = %v, want an error naming SERVER_PRIVATE_KEY", err)
			}
		})
	}
}

// TestValidateRejectsAnUndeclaredSubcommand pins the fail-closed behaviour of
// the rule-set lookup. A map miss yields a nil slice, so before the explicit
// check the loop ran zero rules and Validate returned nil — a call site typo,
// or the zero value Subcommand(""), booted the process against configuration
// nothing had looked at. The config here is deliberately BROKEN in a way every
// declared subcommand would reject, so a nil return could only mean the rules
// never ran.
func TestValidateRejectsAnUndeclaredSubcommand(t *testing.T) {
	for _, sub := range []config.Subcommand{"", "srve", "Serve"} {
		c := validConfig(t)
		c.PGHost = ""
		c.BSVNetwork = "nonsense"

		if err := config.Validate(c, sub); err == nil {
			t.Errorf("Validate(_, %q) = nil, want an error: no rule set is declared for it", sub)
		}
	}
}

func TestRule3RequiresTempestKeyForServeOnly(t *testing.T) {
	c := validConfig(t)
	c.TempestAPIKey = ""

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "TEMPEST_API_KEY") {
		t.Fatalf("Validate(SubServe) = %v, want an error naming TEMPEST_API_KEY", err)
	}

	for _, sub := range []config.Subcommand{
		config.SubDeposit, config.SubRequeue, config.SubStatsRecompute, config.SubPreflight,
	} {
		err := config.Validate(c, sub)
		if err != nil && strings.Contains(err.Error(), "TEMPEST_API_KEY") {
			t.Errorf("Validate(%s) = %v, want no error naming TEMPEST_API_KEY", sub, err)
		}
	}
}

func TestRule4RejectsAnUnknownNetwork(t *testing.T) {
	c := validConfig(t)
	c.BSVNetwork = "mainnet"

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "BSV_NETWORK") {
		t.Fatalf("Validate() = %v, want an error naming BSV_NETWORK", err)
	}
}

func TestRule4AcceptsMainAndTest(t *testing.T) {
	c := validConfig(t)

	// SubDeposit requires rule 4 but never rules 8/10/12/13.
	c.BSVNetwork = "main"
	if err := config.Validate(c, config.SubDeposit); err != nil {
		t.Errorf("Validate() with main = %v, want nil", err)
	}

	c.BSVNetwork = "test"
	if err := config.Validate(c, config.SubDeposit); err != nil {
		t.Errorf("Validate() with test = %v, want nil", err)
	}
}

func TestRule5RejectsANonHTTPSStorageURL(t *testing.T) {
	c := validConfig(t)
	c.WalletStorageURL = "http://x"

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "WALLET_STORAGE_URL") {
		t.Fatalf("Validate() = %v, want an error naming WALLET_STORAGE_URL", err)
	}
}

func TestRule5RejectsAURLWithAPath(t *testing.T) {
	c := validConfig(t)
	c.WalletStorageURL = "https://x/v1"

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "WALLET_STORAGE_URL") {
		t.Fatalf("Validate() = %v, want an error naming WALLET_STORAGE_URL", err)
	}
}

// TestRule5AcceptsABareHTTPSHost covers BOTH accepted spellings, with and
// without the trailing slash, plus a real path as the negative control in the
// same table — a table asserting only "these are accepted" is satisfied by a
// rule that accepts everything.
//
// The trailing-slash case is here because rule 5's own doc comment says "no
// path beyond /" while the code used to reject "https://host/": a plausible
// WALLET_STORAGE_URL (a copy out of a browser address bar) failed closed with
// a message reading as though a path had been supplied.
func TestRule5AcceptsABareHTTPSHost(t *testing.T) {
	cases := []struct {
		url    string
		accept bool
	}{
		{url: "https://storage.example", accept: true},
		{url: "https://storage.example/", accept: true},
		{url: "https://storage.example/v1", accept: false},
		{url: "https://storage.example//", accept: false},
		// The two shapes a prefix-and-substring check could not see, because
		// neither contains a second slash: a scheme with no host at all, which
		// left a REQUIRED value effectively unvalidated, and embedded
		// credentials, which end up in outbound request logs.
		{url: "https://", accept: false},
		// Assembled from parts, per this file's gosec G101 convention: a literal
		// URL with embedded credentials is flagged even in a test that exists to
		// prove the value is REJECTED.
		{url: "https://" + "user" + ":" + "pass" + "@storage.example", accept: false},
		{url: "https://storage.example?k=v", accept: false},
		{url: "https://storage.example#frag", accept: false},
	}

	for _, tc := range cases {
		c := validConfig(t)
		c.WalletStorageURL = tc.url

		// SubDeposit requires rule 5 but never rules 8/10/12/13.
		err := config.Validate(c, config.SubDeposit)
		if tc.accept && err != nil {
			t.Errorf("Validate() with %q = %v, want nil", tc.url, err)
		}
		if !tc.accept && (err == nil || !strings.Contains(err.Error(), "WALLET_STORAGE_URL")) {
			t.Errorf("Validate() with %q = %v, want an error naming WALLET_STORAGE_URL", tc.url, err)
		}
	}
}

func TestRule6RejectsEqualPorts(t *testing.T) {
	c := validConfig(t)
	c.APIPort = 9090
	c.OpsPort = 9090

	err := config.Validate(c, config.SubServe)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming API_PORT or OPS_PORT")
	}
	if !strings.Contains(err.Error(), "API_PORT") && !strings.Contains(err.Error(), "OPS_PORT") {
		t.Errorf("Validate() = %q, want it to name API_PORT or OPS_PORT", err.Error())
	}
}

func TestRule6RejectsAnOutOfRangePort(t *testing.T) {
	c := validConfig(t)
	c.APIPort = 70000

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "API_PORT") {
		t.Fatalf("Validate() = %v, want an error naming API_PORT", err)
	}
}

func TestRule6AcceptsDistinctInRangePorts(t *testing.T) {
	c := validConfig(t)
	c.APIPort = 3001
	c.OpsPort = 9090

	// Rule 6 is required only by SubServe, which always fails closed on
	// rules 8/10/12/13 until fuelmath lands, so a nil-result assertion is
	// impossible here. Assert instead that ports are not named.
	err := config.Validate(c, config.SubServe)
	if err != nil {
		for _, name := range []string{"API_PORT", "OPS_PORT"} {
			if strings.Contains(err.Error(), name) {
				t.Errorf("Validate() = %v, did not want it to name %s", err, name)
			}
		}
	}
}

func TestRule7RejectsEachIncompletePostgresField(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(c *config.Config)
		field string
	}{
		{"blank PG_HOST", func(c *config.Config) { c.PGHost = "" }, "PG_HOST"},
		{"blank PG_USER", func(c *config.Config) { c.PGUser = "" }, "PG_USER"},
		{"blank PG_DATABASE", func(c *config.Config) { c.PGDatabase = "" }, "PG_DATABASE"},
		{"PG_PORT=0", func(c *config.Config) { c.PGPort = 0 }, "PG_PORT"},
		{"PG_SSLMODE=maybe", func(c *config.Config) { c.PGSSLMode = "maybe" }, "PG_SSLMODE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig(t)
			tc.mut(c)

			// SubDeposit requires rule 7 but never rules 8/10/12/13.
			err := config.Validate(c, config.SubDeposit)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Validate() = %v, want an error naming %s", err, tc.field)
			}
		})
	}
}

func TestRule7AcceptsACompleteDSN(t *testing.T) {
	c := validConfig(t)

	if err := config.Validate(c, config.SubDeposit); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestRules8And10And12And13AreReportedUnevaluatedUntilFuelmathLands(t *testing.T) {
	c := validConfig(t)

	err := config.Validate(c, config.SubServe)
	if err == nil {
		t.Fatal("Validate(SubServe) = nil, want an error naming rules 8, 10, 12, 13")
	}
	msg := err.Error()
	for _, want := range []string{"8", "10", "12", "13"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Validate(SubServe) = %q, want it to name rule %s", msg, want)
		}
	}

	err = config.Validate(c, config.SubRequeue)
	if err != nil && strings.Contains(err.Error(), "fuel shape") {
		t.Errorf("Validate(SubRequeue) = %v, want no fuel-shape-unevaluated error", err)
	}
}

func TestRule9RejectsAFanoutWidthOtherThan100(t *testing.T) {
	c := validConfig(t)

	c.FanoutOutputsPerTx = 50
	if err := config.Validate(c, config.SubPreflight); err == nil || !strings.Contains(err.Error(), "FANOUT_OUTPUTS_PER_TX") {
		t.Errorf("Validate() with 50 = %v, want an error naming FANOUT_OUTPUTS_PER_TX", err)
	}

	c.FanoutOutputsPerTx = 200
	if err := config.Validate(c, config.SubPreflight); err == nil || !strings.Contains(err.Error(), "FANOUT_OUTPUTS_PER_TX") {
		t.Errorf("Validate() with 200 = %v, want an error naming FANOUT_OUTPUTS_PER_TX", err)
	}
}

func TestRule9Accepts100(t *testing.T) {
	c := validConfig(t)
	c.FanoutOutputsPerTx = 100

	// Rule 9 is required by SubPreflight, which always fails closed on
	// rules 8/10 until fuelmath lands, so assert absence of the field
	// rather than a nil result.
	err := config.Validate(c, config.SubPreflight)
	if err != nil && strings.Contains(err.Error(), "FANOUT_OUTPUTS_PER_TX") {
		t.Errorf("Validate() = %v, did not want it to name FANOUT_OUTPUTS_PER_TX", err)
	}
}

func TestRule11RejectsABatchCapOutsideOneToTwentyFive(t *testing.T) {
	c := validConfig(t)

	// Rule 11 is required only by SubServe.
	c.FuelFanoutMaxTxsPerRound = 0
	if err := config.Validate(c, config.SubServe); err == nil || !strings.Contains(err.Error(), "FUEL_FANOUT_MAX_TXS_PER_ROUND") {
		t.Errorf("Validate() with 0 = %v, want an error naming FUEL_FANOUT_MAX_TXS_PER_ROUND", err)
	}

	c.FuelFanoutMaxTxsPerRound = 26
	if err := config.Validate(c, config.SubServe); err == nil || !strings.Contains(err.Error(), "FUEL_FANOUT_MAX_TXS_PER_ROUND") {
		t.Errorf("Validate() with 26 = %v, want an error naming FUEL_FANOUT_MAX_TXS_PER_ROUND", err)
	}
}

func TestRule11AcceptsTheBoundaries(t *testing.T) {
	c := validConfig(t)

	// SubServe always fails closed on rules 8/10/12/13 until fuelmath
	// lands, so assert absence of the field rather than a nil result.
	c.FuelFanoutMaxTxsPerRound = 1
	if err := config.Validate(c, config.SubServe); err != nil && strings.Contains(err.Error(), "FUEL_FANOUT_MAX_TXS_PER_ROUND") {
		t.Errorf("Validate() with 1 = %v, did not want it to name FUEL_FANOUT_MAX_TXS_PER_ROUND", err)
	}

	c.FuelFanoutMaxTxsPerRound = 25
	if err := config.Validate(c, config.SubServe); err != nil && strings.Contains(err.Error(), "FUEL_FANOUT_MAX_TXS_PER_ROUND") {
		t.Errorf("Validate() with 25 = %v, did not want it to name FUEL_FANOUT_MAX_TXS_PER_ROUND", err)
	}
}

func TestRule14RejectsALeaseNotExceedingTheCreateActionTimeout(t *testing.T) {
	c := validConfig(t)

	for _, d := range []time.Duration{60 * time.Second, 30 * time.Second} {
		c.ProcessingLease = d
		if err := config.Validate(c, config.SubRequeue); err == nil || !strings.Contains(err.Error(), "PROCESSING_LEASE") {
			t.Errorf("Validate() with lease %s = %v, want an error naming PROCESSING_LEASE", d, err)
		}
	}
}

func TestRule14AcceptsFiveMinutes(t *testing.T) {
	c := validConfig(t)
	c.ProcessingLease = 5 * time.Minute

	if err := config.Validate(c, config.SubRequeue); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestRule15RejectsEachUnorderedInterval(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(c *config.Config)
		field string
	}{
		{"FUEL_INTERVAL=0", func(c *config.Config) { c.FuelInterval = 0 }, "FUEL_INTERVAL"},
		{"PROCESSOR_INTERVAL=0", func(c *config.Config) { c.ProcessorInterval = 0 }, "PROCESSOR_INTERVAL"},
		{"RECONCILE_INTERVAL=0", func(c *config.Config) { c.ReconcileInterval = 0 }, "RECONCILE_INTERVAL"},
		{"POLL_RATE=59s", func(c *config.Config) { c.PollRate = 59 * time.Second }, "POLL_RATE"},
		{
			"RECONCILE_INTERVAL=1s with PROCESSOR_INTERVAL=3s",
			func(c *config.Config) {
				c.ProcessorInterval = 3 * time.Second
				c.ReconcileInterval = 1 * time.Second
			},
			"RECONCILE_INTERVAL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig(t)
			tc.mut(c)

			err := config.Validate(c, config.SubServe)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Validate() = %v, want an error naming %s", err, tc.field)
			}
		})
	}
}

func TestRule15AcceptsTheShippedIntervals(t *testing.T) {
	c := validConfig(t)

	if err := config.Validate(c, config.SubServe); err != nil {
		// Rules 8/10/12/13 fail closed for SubServe until fuelmath lands;
		// that's expected and unrelated to rule 15. Confirm rule 15's own
		// fields don't appear.
		for _, name := range []string{"FUEL_INTERVAL", "PROCESSOR_INTERVAL", "RECONCILE_INTERVAL", "POLL_RATE"} {
			if strings.Contains(err.Error(), name) {
				t.Errorf("Validate() = %v, did not want it to name %s", err, name)
			}
		}
	}
}

func TestRule16RejectsAnInvertedWaterBand(t *testing.T) {
	c := validConfig(t)
	c.LowWaterPercent = 60
	c.HighWaterPercent = 40

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "water") {
		t.Fatalf("Validate() = %v, want an error naming the water band", err)
	}
}

func TestRule16AcceptsTheInheritedBand(t *testing.T) {
	c := validConfig(t)
	c.LowWaterPercent = 60
	c.HighWaterPercent = 85

	err := config.Validate(c, config.SubServe)
	if err != nil && strings.Contains(err.Error(), "water") {
		t.Errorf("Validate() = %v, want no water-band error", err)
	}
}

func TestRule17RejectsAMalformedCIDR(t *testing.T) {
	c := validConfig(t)
	c.TrustedProxyCIDRs = []string{"10.0.0.0/99"}

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
		t.Fatalf("Validate() = %v, want an error naming TRUSTED_PROXY_CIDRS", err)
	}
}

func TestRule17AcceptsAnEmptyList(t *testing.T) {
	c := validConfig(t)
	c.TrustedProxyCIDRs = nil

	err := config.Validate(c, config.SubServe)
	if err != nil && strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
		t.Errorf("Validate() = %v, did not want it to name TRUSTED_PROXY_CIDRS", err)
	}
}

func TestRule18RejectsZero(t *testing.T) {
	c := validConfig(t)
	c.ProofRateLimitPerMin = 0

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "PROOF_RATE_LIMIT_PER_MIN") {
		t.Fatalf("Validate() = %v, want an error naming PROOF_RATE_LIMIT_PER_MIN", err)
	}
}

func TestRule18RejectsANegativeValue(t *testing.T) {
	c := validConfig(t)
	c.ProofRateLimitPerMin = -5

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "PROOF_RATE_LIMIT_PER_MIN") {
		t.Fatalf("Validate() = %v, want an error naming PROOF_RATE_LIMIT_PER_MIN", err)
	}
}

func TestRule18AcceptsOne(t *testing.T) {
	c := validConfig(t)
	c.ProofRateLimitPerMin = 1

	err := config.Validate(c, config.SubServe)
	if err != nil && strings.Contains(err.Error(), "PROOF_RATE_LIMIT_PER_MIN") {
		t.Errorf("Validate() = %v, did not want it to name PROOF_RATE_LIMIT_PER_MIN", err)
	}
}

func TestRule18AcceptsTheDefault(t *testing.T) {
	c := validConfig(t)
	// ProofRateLimitPerMin is left at Load's default (60).

	err := config.Validate(c, config.SubServe)
	if err != nil && strings.Contains(err.Error(), "PROOF_RATE_LIMIT_PER_MIN") {
		t.Errorf("Validate() = %v, did not want it to name PROOF_RATE_LIMIT_PER_MIN", err)
	}
}

func TestRule19RejectsAnUnknownLogLevel(t *testing.T) {
	c := validConfig(t)
	c.LogLevel = "verbose"

	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("Validate() = %v, want an error naming LOG_LEVEL", err)
	}
}

func TestRule19AcceptsAllFour(t *testing.T) {
	c := validConfig(t)

	for _, level := range []string{"debug", "info", "warn", "error"} {
		c.LogLevel = level
		if err := config.Validate(c, config.SubRequeue); err != nil {
			t.Errorf("Validate() with LOG_LEVEL=%s = %v, want nil", level, err)
		}
	}
}

func TestRule20ReturnsEveryFailureNotTheFirst(t *testing.T) {
	c := validConfig(t)
	c.BSVNetwork = "mainnet"
	c.APIPort = 9090
	c.OpsPort = 9090
	c.PGHost = ""
	c.LogLevel = "verbose"

	err := config.Validate(c, config.SubServe)
	if err == nil {
		t.Fatal("Validate() = nil, want a joined error naming four variables")
	}
	msg := err.Error()
	for _, want := range []string{"BSV_NETWORK", "API_PORT", "PG_HOST", "LOG_LEVEL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Validate() = %q, want it to name %s", msg, want)
		}
	}
}

func TestRule20NeverEchoesAConfiguredValue(t *testing.T) {
	c := validConfig(t)
	c.BSVNetwork = "SENTINEL" + "-network"
	c.WalletStorageURL = "SENTINEL" + "-storageurl"
	c.PGHost = "SENTINEL" + "-pghost"
	c.PGUser = "SENTINEL" + "-pguser"
	c.PGDatabase = "SENTINEL" + "-pgdatabase"
	c.PGSSLMode = "SENTINEL" + "-sslmode"
	c.LogLevel = "SENTINEL" + "-loglevel"
	c.ServerPrivateKey = config.Secret("SENTINEL" + "-serverkey")

	err := config.Validate(c, config.SubServe)
	if err == nil {
		t.Fatal("Validate() = nil, want a joined error")
	}
	msg := err.Error()

	// Positive control: an implementation returning errors.New("") must not
	// pass this test trivially.
	if msg == "" {
		t.Fatal("Validate() error message is empty")
	}
	named := 0
	for _, want := range []string{"BSV_NETWORK", "WALLET_STORAGE_URL", "PG_HOST", "PG_USER", "PG_DATABASE", "PG_SSLMODE", "LOG_LEVEL", "SERVER_PRIVATE_KEY"} {
		if strings.Contains(msg, want) {
			named++
		}
	}
	if named < 4 {
		t.Fatalf("Validate() = %q, want it to name at least four variables, named %d", msg, named)
	}

	for _, sentinel := range []string{
		"SENTINEL" + "-network",
		"SENTINEL" + "-storageurl",
		"SENTINEL" + "-pghost",
		"SENTINEL" + "-pguser",
		"SENTINEL" + "-pgdatabase",
		"SENTINEL" + "-sslmode",
		"SENTINEL" + "-loglevel",
		"SENTINEL" + "-serverkey",
	} {
		if strings.Contains(msg, sentinel) {
			t.Errorf("Validate() = %q, must not echo configured value %q", msg, sentinel)
		}
	}
}

func TestReducedRuleSetSucceedsForDepositRequeueAndStatsRecompute(t *testing.T) {
	config.ClearAmbientEnv(t)
	t.Setenv("SERVER_PRIVATE_KEY", wellFormedPrivateKey())
	t.Setenv("POSTGRES_PASSWORD", "fedcba9876543210"+strings.Repeat("11", 24))
	t.Setenv("WALLET_STORAGE_URL", "https://storage.example")
	// TEMPEST_API_KEY, API_PORT, OPS_PORT and every fuel knob are
	// deliberately left unset.
	c := config.Load()

	for _, sub := range []config.Subcommand{
		config.SubDepositAddress, config.SubDeposit, config.SubRequeue, config.SubStatsRecompute,
	} {
		if err := config.Validate(c, sub); err != nil {
			t.Errorf("Validate(%s) = %v, want nil", sub, err)
		}
	}
}

func TestValidateSurfacesLoadParseErrors(t *testing.T) {
	config.ClearAmbientEnv(t)
	t.Setenv("SERVER_PRIVATE_KEY", wellFormedPrivateKey())
	t.Setenv("POSTGRES_PASSWORD", "fedcba9876543210"+strings.Repeat("11", 24))
	t.Setenv("WALLET_STORAGE_URL", "https://storage.example")
	t.Setenv("TEMPEST_API_KEY", "aabbccdd"+strings.Repeat("22", 24))
	t.Setenv("API_PORT", "abc")

	c := config.Load()
	err := config.Validate(c, config.SubServe)
	if err == nil || !strings.Contains(err.Error(), "API_PORT") {
		t.Fatalf("Validate() = %v, want an error naming API_PORT", err)
	}
}
