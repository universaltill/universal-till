package config

import (
	"os"
	"testing"
)

// configEnvKeys lists every env var Init() reads. Kept in one place so both
// tests below stay in sync with config.go if a var is ever added/removed.
var configEnvKeys = []string{
	"UT_DATA_DIR", "UT_STORE_NAME", "UT_LISTEN_ADDR", "UT_DB_PATH", "UT_LOG_LEVEL",
	"UT_THEME", "UT_DEV_MODE",
	"UT_MARKETPLACE_ENDPOINT_URL", "UT_MARKETPLACE_STORE_ID", "UT_MARKETPLACE_DEVICE_ID",
	"UT_MARKETPLACE_PUBLIC_KEY", "UT_MARKETPLACE_UPLOAD_TOKEN", "UT_MARKETPLACE_MERCHANT_TOKEN",
	"UT_MARKETPLACE_CLIENT_ID", "UT_MARKETPLACE_CLIENT_SECRET", "UT_MARKETPLACE_API_VERSION",
	"UT_MARKETPLACE_TELEMETRY_OPT_IN", "UT_MARKETPLACE_DEV_OVERRIDE_URL",
	"UT_MARKETPLACE_HEALTH_CHECK_TIMEOUT_SEC", "UT_MARKETPLACE_FALLBACK_TIMEOUT_SEC",
	"UT_TAX_RATE", "UT_TAX_INCLUSIVE", "UT_CURRENCY",
	"UT_DEFAULT_LOCALE", "UT_MARKETPLACE_LOCALE",
}

// unsetForTest clears every key in configEnvKeys for the duration of the
// test, restoring each one's original value (set or unset) afterwards — so
// this test's assertions about Init()'s hardcoded defaults aren't at the
// mercy of whatever happens to be in the ambient environment.
func unsetForTest(t *testing.T, keys []string) {
	t.Helper()
	for _, k := range keys {
		orig, had := os.LookupEnv(k)
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, orig)
			}
		})
	}
}

// With nothing set in the environment, Init() must fall back to its own
// hardcoded defaults for every field — Init and getenv had zero direct test
// coverage before this batch (only ever exercised indirectly via whatever
// happened to be in a real process's environment).
func TestInitDefaults(t *testing.T) {
	unsetForTest(t, configEnvKeys)

	cfg, err := Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if cfg.StoreName != "My Store" {
		t.Errorf("StoreName = %q", cfg.StoreName)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.DataDir == "" {
		t.Error("DataDir must default to paths.Default(), got empty")
	}
	if cfg.DBPath == "" {
		t.Error("DBPath must default to <DataDir>/unitill-pos.db, got empty")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.Theme != "monarch" {
		t.Errorf("Theme = %q", cfg.Theme)
	}
	if cfg.DevMode {
		t.Error("DevMode must default to false")
	}
	m := cfg.Marketplace
	if m.EndpointURL != "http://127.0.0.1:8081" {
		t.Errorf("EndpointURL = %q", m.EndpointURL)
	}
	if m.StoreID != "My Store" {
		t.Errorf("StoreID must fall back to the StoreName default, got %q", m.StoreID)
	}
	if m.DeviceID != "" || m.PublicKey != "" || m.UploadToken != "" || m.MerchantToken != "" {
		t.Errorf("marketplace secrets must default empty: %+v", m)
	}
	if m.APIVersion != "1.0.0" {
		t.Errorf("APIVersion = %q", m.APIVersion)
	}
	if m.TelemetryOptIn {
		t.Error("TelemetryOptIn must default to false")
	}
	if m.DevMode {
		t.Error("Marketplace.DevMode must mirror the top-level default (false)")
	}
	if m.HealthCheckTimeoutSec != 5 {
		t.Errorf("HealthCheckTimeoutSec = %d, want 5", m.HealthCheckTimeoutSec)
	}
	if m.FallbackTimeoutSec != 30 {
		t.Errorf("FallbackTimeoutSec = %d, want 30", m.FallbackTimeoutSec)
	}
	if m.RequestTimeoutSec != 30 {
		t.Errorf("RequestTimeoutSec = %d, want 30 (not env-driven)", m.RequestTimeoutSec)
	}
	if cfg.Locales.Currency != "GBP" {
		t.Errorf("Locales currency = %+v", cfg.Locales)
	}
	if cfg.Locales.TaxRate != 20 {
		t.Errorf("TaxRate = %d, want 20", cfg.Locales.TaxRate)
	}
	if !cfg.Locales.TaxInclusive {
		t.Error("TaxInclusive must default to true")
	}
	if cfg.Locales.Locale != "en-US" {
		t.Errorf("Locales.Locale = %q", cfg.Locales.Locale)
	}
	if cfg.DefaultLocale != "en-US" {
		t.Errorf("DefaultLocale = %q", cfg.DefaultLocale)
	}
}

// Every env var must actually reach the field it's documented to control —
// the inverse of TestInitDefaults, and the only test that ever exercises
// Init()'s override path at all.
func TestInitHonorsEnvOverrides(t *testing.T) {
	unsetForTest(t, configEnvKeys)
	dataDir := t.TempDir()

	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_STORE_NAME", "Cafe Berlin")
	t.Setenv("UT_LISTEN_ADDR", ":9090")
	t.Setenv("UT_DB_PATH", dataDir+"/custom.db")
	t.Setenv("UT_LOG_LEVEL", "debug")
	t.Setenv("UT_THEME", "midnight")
	t.Setenv("UT_DEV_MODE", "true")
	t.Setenv("UT_MARKETPLACE_ENDPOINT_URL", "https://cloud.example.test")
	t.Setenv("UT_MARKETPLACE_STORE_ID", "store-42")
	t.Setenv("UT_MARKETPLACE_DEVICE_ID", "dev-1")
	t.Setenv("UT_MARKETPLACE_PUBLIC_KEY", "pk-abc")
	t.Setenv("UT_MARKETPLACE_UPLOAD_TOKEN", "up-tok")
	t.Setenv("UT_MARKETPLACE_MERCHANT_TOKEN", "mt-tok")
	t.Setenv("UT_MARKETPLACE_CLIENT_ID", "cid")
	t.Setenv("UT_MARKETPLACE_CLIENT_SECRET", "csec")
	t.Setenv("UT_MARKETPLACE_API_VERSION", "2.1.0")
	t.Setenv("UT_MARKETPLACE_TELEMETRY_OPT_IN", "true")
	t.Setenv("UT_MARKETPLACE_DEV_OVERRIDE_URL", "http://localhost:9999")
	t.Setenv("UT_MARKETPLACE_HEALTH_CHECK_TIMEOUT_SEC", "9")
	t.Setenv("UT_MARKETPLACE_FALLBACK_TIMEOUT_SEC", "42")
	t.Setenv("UT_TAX_RATE", "19")
	t.Setenv("UT_TAX_INCLUSIVE", "false")
	t.Setenv("UT_CURRENCY", "EUR")
	t.Setenv("UT_DEFAULT_LOCALE", "de-DE")
	t.Setenv("UT_MARKETPLACE_LOCALE", "de-DE")

	cfg, err := Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if cfg.DataDir != dataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, dataDir)
	}
	if cfg.StoreName != "Cafe Berlin" {
		t.Errorf("StoreName = %q", cfg.StoreName)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.DBPath != dataDir+"/custom.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.Theme != "midnight" {
		t.Errorf("Theme = %q", cfg.Theme)
	}
	if !cfg.DevMode {
		t.Error("DevMode should be true")
	}
	m := cfg.Marketplace
	if m.EndpointURL != "https://cloud.example.test" {
		t.Errorf("EndpointURL = %q", m.EndpointURL)
	}
	if m.StoreID != "store-42" {
		t.Errorf("StoreID = %q, must not fall back once explicitly set", m.StoreID)
	}
	if m.DeviceID != "dev-1" || m.PublicKey != "pk-abc" || m.UploadToken != "up-tok" || m.MerchantToken != "mt-tok" {
		t.Errorf("marketplace identity/token fields: %+v", m)
	}
	if m.ClientID != "cid" || m.ClientSecret != "csec" {
		t.Errorf("oauth fields: %+v", m)
	}
	if m.APIVersion != "2.1.0" {
		t.Errorf("APIVersion = %q", m.APIVersion)
	}
	if !m.TelemetryOptIn {
		t.Error("TelemetryOptIn should be true")
	}
	if !m.DevMode {
		t.Error("Marketplace.DevMode should mirror the top-level DevMode")
	}
	if m.DevOverrideURL != "http://localhost:9999" {
		t.Errorf("DevOverrideURL = %q", m.DevOverrideURL)
	}
	if m.HealthCheckTimeoutSec != 9 {
		t.Errorf("HealthCheckTimeoutSec = %d", m.HealthCheckTimeoutSec)
	}
	if m.FallbackTimeoutSec != 42 {
		t.Errorf("FallbackTimeoutSec = %d", m.FallbackTimeoutSec)
	}
	if cfg.Locales.Currency != "EUR" {
		t.Errorf("Locales currency = %+v", cfg.Locales)
	}
	if cfg.Locales.TaxRate != 19 {
		t.Errorf("TaxRate = %d", cfg.Locales.TaxRate)
	}
	if cfg.Locales.TaxInclusive {
		t.Error("TaxInclusive should be false")
	}
	// Two distinct fields, two distinct env vars — UT_DEFAULT_LOCALE drives
	// Locales.Locale, UT_MARKETPLACE_LOCALE drives the top-level
	// DefaultLocale; easy to conflate since the names point the other way.
	if cfg.Locales.Locale != "de-DE" {
		t.Errorf("Locales.Locale = %q, want the UT_DEFAULT_LOCALE override", cfg.Locales.Locale)
	}
	if cfg.DefaultLocale != "de-DE" {
		t.Errorf("DefaultLocale = %q, want the UT_MARKETPLACE_LOCALE override", cfg.DefaultLocale)
	}
}

// UT_MARKETPLACE_STORE_ID's own default is a NESTED getenv fallback onto
// UT_STORE_NAME's resolved value (not a hardcoded literal) — easy to
// regress silently since both env-var lookups happen on the same line.
func TestInitMarketplaceStoreIDFallsBackToStoreName(t *testing.T) {
	unsetForTest(t, configEnvKeys)
	t.Setenv("UT_STORE_NAME", "My Custom Shop")

	cfg, err := Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if cfg.Marketplace.StoreID != "My Custom Shop" {
		t.Errorf("Marketplace.StoreID = %q, want it to fall back to StoreName", cfg.Marketplace.StoreID)
	}
}
