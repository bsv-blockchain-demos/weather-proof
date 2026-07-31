package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsMatchTheSpecInventory(t *testing.T) {
	ClearAmbientEnv(t)
	c := Load()

	if c.BSVNetwork != "test" {
		t.Errorf("BSVNetwork = %q, want %q", c.BSVNetwork, "test")
	}
	if c.APIPort != 3001 {
		t.Errorf("APIPort = %d, want %d", c.APIPort, 3001)
	}
	if c.OpsPort != 9090 {
		t.Errorf("OpsPort = %d, want %d", c.OpsPort, 9090)
	}
	if c.PGHost != "postgres" {
		t.Errorf("PGHost = %q, want %q", c.PGHost, "postgres")
	}
	if c.PGPort != 5432 {
		t.Errorf("PGPort = %d, want %d", c.PGPort, 5432)
	}
	if c.PGUser != "weather" {
		t.Errorf("PGUser = %q, want %q", c.PGUser, "weather")
	}
	if c.PGDatabase != "weather" {
		t.Errorf("PGDatabase = %q, want %q", c.PGDatabase, "weather")
	}
	if c.PGSSLMode != "disable" {
		t.Errorf("PGSSLMode = %q, want %q", c.PGSSLMode, "disable")
	}
	if c.DenominationSatoshis != 50 {
		t.Errorf("DenominationSatoshis = %d, want %d", c.DenominationSatoshis, 50)
	}
	if c.FanoutOutputsPerTx != 100 {
		t.Errorf("FanoutOutputsPerTx = %d, want %d", c.FanoutOutputsPerTx, 100)
	}
	if c.FuelTargetPoolSize != 1000 {
		t.Errorf("FuelTargetPoolSize = %d, want %d", c.FuelTargetPoolSize, 1000)
	}
	if c.FuelInterval != time.Minute {
		t.Errorf("FuelInterval = %v, want %v", c.FuelInterval, time.Minute)
	}
	if c.FuelFanoutMaxTxsPerRound != 12 {
		t.Errorf("FuelFanoutMaxTxsPerRound = %d, want %d", c.FuelFanoutMaxTxsPerRound, 12)
	}
	if c.WeatherOutputsPerTx != 21 {
		t.Errorf("WeatherOutputsPerTx = %d, want %d", c.WeatherOutputsPerTx, 21)
	}
	if c.WeatherMaxScriptBytes != 297 {
		t.Errorf("WeatherMaxScriptBytes = %d, want %d", c.WeatherMaxScriptBytes, 297)
	}
	if c.WeatherAllowMulticlaimScripts != false {
		t.Errorf("WeatherAllowMulticlaimScripts = %v, want %v", c.WeatherAllowMulticlaimScripts, false)
	}
	if c.PollRate != 300*time.Second {
		t.Errorf("PollRate = %v, want %v", c.PollRate, 300*time.Second)
	}
	if c.ProcessorInterval != 3*time.Second {
		t.Errorf("ProcessorInterval = %v, want %v", c.ProcessorInterval, 3*time.Second)
	}
	if c.ProcessingLease != 5*time.Minute {
		t.Errorf("ProcessingLease = %v, want %v", c.ProcessingLease, 5*time.Minute)
	}
	if c.ReconcileInterval != 5*time.Minute {
		t.Errorf("ReconcileInterval = %v, want %v", c.ReconcileInterval, 5*time.Minute)
	}
	if len(c.TrustedProxyCIDRs) != 0 {
		t.Errorf("TrustedProxyCIDRs = %v, want empty", c.TrustedProxyCIDRs)
	}
	if c.ProofRateLimitPerMin != 60 {
		t.Errorf("ProofRateLimitPerMin = %d, want %d", c.ProofRateLimitPerMin, 60)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", c.LogLevel, "info")
	}
	if c.ServerPrivateKey.Reveal() != "" {
		t.Errorf("ServerPrivateKey.Reveal() = %q, want empty", c.ServerPrivateKey.Reveal())
	}
	if c.PostgresPassword.Reveal() != "" {
		t.Errorf("PostgresPassword.Reveal() = %q, want empty", c.PostgresPassword.Reveal())
	}
	if c.TempestAPIKey.Reveal() != "" {
		t.Errorf("TempestAPIKey.Reveal() = %q, want empty", c.TempestAPIKey.Reveal())
	}
}

func TestLoadHasNoDefaultForEitherSecret(t *testing.T) {
	ClearAmbientEnv(t)
	c := Load()

	if got := c.ServerPrivateKey.Reveal(); got != "" {
		t.Errorf("ServerPrivateKey.Reveal() = %q, want empty", got)
	}
	if got := c.PostgresPassword.Reveal(); got != "" {
		t.Errorf("PostgresPassword.Reveal() = %q, want empty", got)
	}
}

func TestLoadReadsEverySetValue(t *testing.T) {
	ClearAmbientEnv(t)
	t.Setenv("SERVER_PRIVATE_KEY", "0123456789abcdef"+strings.Repeat("00", 24))
	t.Setenv("POSTGRES_PASSWORD", "fedcba9876543210"+strings.Repeat("11", 24))
	t.Setenv("TEMPEST_API_KEY", "aabbccdd"+strings.Repeat("22", 24))
	t.Setenv("BSV_NETWORK", "main")
	t.Setenv("WALLET_STORAGE_URL", "https://example.test/storage")
	t.Setenv("API_PORT", "4001")
	t.Setenv("OPS_PORT", "9091")
	t.Setenv("PG_HOST", "pg-host")
	t.Setenv("PG_PORT", "5433")
	t.Setenv("PG_USER", "pg-user")
	t.Setenv("PG_DATABASE", "pg-db")
	t.Setenv("PG_SSLMODE", "require")
	t.Setenv("DENOMINATION_SATOSHIS", "51")
	t.Setenv("FANOUT_OUTPUTS_PER_TX", "101")
	t.Setenv("FUEL_TARGET_POOL_SIZE", "1001")
	t.Setenv("FUEL_INTERVAL", "2m")
	t.Setenv("FUEL_FANOUT_MAX_TXS_PER_ROUND", "13")
	t.Setenv("WEATHER_OUTPUTS_PER_TX", "22")
	t.Setenv("WEATHER_MAX_SCRIPT_BYTES", "298")
	t.Setenv("WEATHER_ALLOW_MULTICLAIM_SCRIPTS", "true")
	t.Setenv("POLL_RATE", "301s")
	t.Setenv("PROCESSOR_INTERVAL", "4s")
	t.Setenv("PROCESSING_LEASE", "6m")
	t.Setenv("RECONCILE_INTERVAL", "6m")
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	t.Setenv("PROOF_RATE_LIMIT_PER_MIN", "61")
	t.Setenv("LOG_LEVEL", "debug")

	c := Load()

	if got := c.ServerPrivateKey.Reveal(); got != "0123456789abcdef"+strings.Repeat("00", 24) {
		t.Errorf("ServerPrivateKey.Reveal() = %q", got)
	}
	if got := c.PostgresPassword.Reveal(); got != "fedcba9876543210"+strings.Repeat("11", 24) {
		t.Errorf("PostgresPassword.Reveal() = %q", got)
	}
	if got := c.TempestAPIKey.Reveal(); got != "aabbccdd"+strings.Repeat("22", 24) {
		t.Errorf("TempestAPIKey.Reveal() = %q", got)
	}
	if c.BSVNetwork != "main" {
		t.Errorf("BSVNetwork = %q", c.BSVNetwork)
	}
	if c.WalletStorageURL != "https://example.test/storage" {
		t.Errorf("WalletStorageURL = %q", c.WalletStorageURL)
	}
	if c.APIPort != 4001 {
		t.Errorf("APIPort = %d", c.APIPort)
	}
	if c.OpsPort != 9091 {
		t.Errorf("OpsPort = %d", c.OpsPort)
	}
	if c.PGHost != "pg-host" {
		t.Errorf("PGHost = %q", c.PGHost)
	}
	if c.PGPort != 5433 {
		t.Errorf("PGPort = %d", c.PGPort)
	}
	if c.PGUser != "pg-user" {
		t.Errorf("PGUser = %q", c.PGUser)
	}
	if c.PGDatabase != "pg-db" {
		t.Errorf("PGDatabase = %q", c.PGDatabase)
	}
	if c.PGSSLMode != "require" {
		t.Errorf("PGSSLMode = %q", c.PGSSLMode)
	}
	if c.DenominationSatoshis != 51 {
		t.Errorf("DenominationSatoshis = %d", c.DenominationSatoshis)
	}
	if c.FanoutOutputsPerTx != 101 {
		t.Errorf("FanoutOutputsPerTx = %d", c.FanoutOutputsPerTx)
	}
	if c.FuelTargetPoolSize != 1001 {
		t.Errorf("FuelTargetPoolSize = %d", c.FuelTargetPoolSize)
	}
	if c.FuelInterval != 2*time.Minute {
		t.Errorf("FuelInterval = %v", c.FuelInterval)
	}
	if c.FuelFanoutMaxTxsPerRound != 13 {
		t.Errorf("FuelFanoutMaxTxsPerRound = %d", c.FuelFanoutMaxTxsPerRound)
	}
	if c.WeatherOutputsPerTx != 22 {
		t.Errorf("WeatherOutputsPerTx = %d", c.WeatherOutputsPerTx)
	}
	if c.WeatherMaxScriptBytes != 298 {
		t.Errorf("WeatherMaxScriptBytes = %d", c.WeatherMaxScriptBytes)
	}
	if c.WeatherAllowMulticlaimScripts != true {
		t.Errorf("WeatherAllowMulticlaimScripts = %v", c.WeatherAllowMulticlaimScripts)
	}
	if c.PollRate != 301*time.Second {
		t.Errorf("PollRate = %v", c.PollRate)
	}
	if c.ProcessorInterval != 4*time.Second {
		t.Errorf("ProcessorInterval = %v", c.ProcessorInterval)
	}
	if c.ProcessingLease != 6*time.Minute {
		t.Errorf("ProcessingLease = %v", c.ProcessingLease)
	}
	if c.ReconcileInterval != 6*time.Minute {
		t.Errorf("ReconcileInterval = %v", c.ReconcileInterval)
	}
	if len(c.TrustedProxyCIDRs) != 1 || c.TrustedProxyCIDRs[0] != "10.0.0.0/8" {
		t.Errorf("TrustedProxyCIDRs = %v", c.TrustedProxyCIDRs)
	}
	if c.ProofRateLimitPerMin != 61 {
		t.Errorf("ProofRateLimitPerMin = %d", c.ProofRateLimitPerMin)
	}
	if c.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", c.LogLevel)
	}
}

func TestLoadRecordsAMalformedIntRatherThanDefaulting(t *testing.T) {
	ClearAmbientEnv(t)
	t.Setenv("API_PORT", "abc")

	c := Load()

	if c.APIPort != 3001 {
		t.Errorf("APIPort = %d, want default %d", c.APIPort, 3001)
	}
	err := c.ParseErrors()
	if err == nil {
		t.Fatal("ParseErrors() = nil, want an error naming API_PORT")
	}
	if !strings.Contains(err.Error(), "API_PORT") {
		t.Errorf("ParseErrors() = %q, want it to name API_PORT", err.Error())
	}
	if n := strings.Count(err.Error(), "API_PORT"); n != 1 {
		t.Errorf("ParseErrors() named API_PORT %d times, want exactly 1: %q", n, err.Error())
	}
}

func TestLoadRecordsAMalformedDurationRatherThanDefaulting(t *testing.T) {
	ClearAmbientEnv(t)
	t.Setenv("POLL_RATE", "5 minutes")

	c := Load()

	if c.PollRate != 300*time.Second {
		t.Errorf("PollRate = %v, want default %v", c.PollRate, 300*time.Second)
	}
	err := c.ParseErrors()
	if err == nil {
		t.Fatal("ParseErrors() = nil, want an error naming POLL_RATE")
	}
	if !strings.Contains(err.Error(), "POLL_RATE") {
		t.Errorf("ParseErrors() = %q, want it to name POLL_RATE", err.Error())
	}
}

func TestLoadRecordsAMalformedBoolRatherThanDefaulting(t *testing.T) {
	ClearAmbientEnv(t)
	t.Setenv("WEATHER_ALLOW_MULTICLAIM_SCRIPTS", "yes")

	c := Load()

	if c.WeatherAllowMulticlaimScripts != false {
		t.Errorf("WeatherAllowMulticlaimScripts = %v, want default %v", c.WeatherAllowMulticlaimScripts, false)
	}
	err := c.ParseErrors()
	if err == nil {
		t.Fatal("ParseErrors() = nil, want an error naming WEATHER_ALLOW_MULTICLAIM_SCRIPTS")
	}
	if !strings.Contains(err.Error(), "WEATHER_ALLOW_MULTICLAIM_SCRIPTS") {
		t.Errorf("ParseErrors() = %q, want it to name WEATHER_ALLOW_MULTICLAIM_SCRIPTS", err.Error())
	}
}

func TestLoadParseErrorNamesTheVariableAndNotTheValue(t *testing.T) {
	ClearAmbientEnv(t)
	t.Setenv("PG_PORT", "hunter2")

	c := Load()

	err := c.ParseErrors()
	if err == nil {
		t.Fatal("ParseErrors() = nil, want an error naming PG_PORT")
	}
	if !strings.Contains(err.Error(), "PG_PORT") {
		t.Errorf("ParseErrors() = %q, want it to name PG_PORT", err.Error())
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("ParseErrors() = %q, must not echo the malformed value", err.Error())
	}
}

func TestLoadSplitsTrustedProxyCIDRsOnCommaAndTrims(t *testing.T) {
	ClearAmbientEnv(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", " 10.0.0.0/8 , 192.168.0.0/16 ")

	c := Load()

	want := []string{"10.0.0.0/8", "192.168.0.0/16"}
	if len(c.TrustedProxyCIDRs) != len(want) {
		t.Fatalf("TrustedProxyCIDRs = %v, want %v", c.TrustedProxyCIDRs, want)
	}
	for i, v := range want {
		if c.TrustedProxyCIDRs[i] != v {
			t.Errorf("TrustedProxyCIDRs[%d] = %q, want %q", i, c.TrustedProxyCIDRs[i], v)
		}
	}
}

func TestLoadTreatsAnEmptyTrustedProxyCIDRsAsEmptyNotOneBlankEntry(t *testing.T) {
	ClearAmbientEnv(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "")

	c := Load()

	if len(c.TrustedProxyCIDRs) != 0 {
		t.Errorf("TrustedProxyCIDRs = %v, want empty", c.TrustedProxyCIDRs)
	}
}
