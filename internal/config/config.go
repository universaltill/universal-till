package config

import (
	"os"
	"strconv"
)

type Locales struct {
	Currency       string
	CurrencySymbol string
	Locale         string
	TaxRate        int
	TaxInclusive   bool

	// add more fields as needed (DB, SB, etc.)
}

type MarketplaceConfig struct {
	// Production/Staging/Custom endpoint (FR-015)
	EndpointURL string

	// OAuth2 client credentials for marketplace authentication (FR-018)
	ClientID     string
	ClientSecret string

	// API version pinning (FR-016)
	APIVersion string // semver format: "1.2.3"

	// Telemetry opt-in flag (FR-013)
	TelemetryOptIn bool

	// Dev-mode local override (FR-015, only honored when UT_DEV_MODE=true)
	DevOverrideURL string

	// Health check timeout for endpoint validation (FR-015)
	HealthCheckTimeoutSec int

	// Fallback timeout for dev override (FR-015)
	FallbackTimeoutSec int

	// Request timeout for marketplace HTTP calls
	RequestTimeoutSec int
}

type Config struct {
	StoreName      string
	ListenAddr     string
	Env            string
	DBPath         string
	Locales        Locales
	LogLevel       string
	Theme          string
	MarketplaceURL string // Deprecated: use Marketplace.EndpointURL
	Marketplace    MarketplaceConfig
	DevMode        bool
	// add more fields as needed (DB, SB, etc.)
}

func Init() (*Config, error) {
	devMode, _ := strconv.ParseBool(getenv("UT_DEV_MODE", "false"))
	telemetryOptIn, _ := strconv.ParseBool(getenv("UT_MARKETPLACE_TELEMETRY_OPT_IN", "false"))
	healthCheckTimeout, _ := strconv.Atoi(getenv("UT_MARKETPLACE_HEALTH_CHECK_TIMEOUT_SEC", "5"))
	fallbackTimeout, _ := strconv.Atoi(getenv("UT_MARKETPLACE_FALLBACK_TIMEOUT_SEC", "30"))

	cfg := &Config{
		StoreName:  getenv("UT_STORE_NAME", "My Store"),
		ListenAddr: getenv("UT_LISTEN_ADDR", ":8080"),
		// Env:        getenv("UT_ENV", "local"),
		DBPath:         getenv("UT_DB_PATH", "./data/unitill-pos.db"),
		LogLevel:       getenv("UT_LOG_LEVEL", "info"),
		Theme:          getenv("UT_THEME", "monarch"),
		MarketplaceURL: getenv("UT_MARKETPLACE_URL", "http://127.0.0.1:8081"), // Deprecated
		DevMode:        devMode,
		Marketplace: MarketplaceConfig{
			EndpointURL:           getenv("UT_MARKETPLACE_ENDPOINT_URL", "http://127.0.0.1:8081"),
			ClientID:              getenv("UT_MARKETPLACE_CLIENT_ID", ""),
			ClientSecret:          getenv("UT_MARKETPLACE_CLIENT_SECRET", ""),
			APIVersion:            getenv("UT_MARKETPLACE_API_VERSION", "1.0.0"),
			TelemetryOptIn:        telemetryOptIn,
			DevOverrideURL:        getenv("UT_MARKETPLACE_DEV_OVERRIDE_URL", ""),
			HealthCheckTimeoutSec: healthCheckTimeout,
			FallbackTimeoutSec:    fallbackTimeout,
			RequestTimeoutSec:     30,
		},
	}

	taxRate, _ := strconv.Atoi(getenv("UT_TAX_RATE", "20"))
	tax_inclusive, _ := strconv.ParseBool(getenv("UT_TAX_INCLUSIVE", "true"))

	locales := Locales{
		Currency:       getenv("UT_CURRENCY", "GBP"),
		CurrencySymbol: getenv("UT_CURRENCY_SYMBOL", "£"),
		Locale:         getenv("UT_DEFAULT_LOCALE", "en"),
		TaxRate:        taxRate,
		TaxInclusive:   tax_inclusive,
	}
	cfg.Locales = locales
	// if you need validation, do it here and return an error
	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
