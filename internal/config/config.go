package config

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/universaltill/universal-till/internal/paths"
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
	StoreID     string
	DeviceID    string
	PublicKey   string
	// UploadToken authenticates the install-intent status-report endpoint.
	UploadToken string
	// MerchantToken is this merchant's portal token; sent as a bearer on the
	// entitlements endpoint when the marketplace enforces merchant auth
	// (docs repo: architecture/marketplace-merchant-auth.md).
	MerchantToken string

	// OAuth2 client credentials for marketplace authentication (FR-018)
	ClientID     string
	ClientSecret string

	// API version pinning (FR-016)
	APIVersion string // semver format: "1.2.3"

	// Telemetry opt-in flag (FR-013)
	TelemetryOptIn bool

	// DevMode mirrors Config.DevMode, co-located here because the
	// marketplace Client only holds *MarketplaceConfig, not the full
	// Config — this is what actually gates DevOverrideURL (FR-015).
	DevMode bool

	// Dev-mode local override (FR-015, only honored when DevMode is true)
	DevOverrideURL string

	// Health check timeout for endpoint validation (FR-015)
	HealthCheckTimeoutSec int

	// Fallback timeout for dev override (FR-015)
	FallbackTimeoutSec int

	// Request timeout for marketplace HTTP calls
	RequestTimeoutSec int
}

type Config struct {
	StoreName     string
	ListenAddr    string
	Env           string
	DataDir       string
	DBPath        string
	Locales       Locales
	LogLevel      string
	Theme         string
	Marketplace   MarketplaceConfig
	DevMode       bool
	DefaultLocale string // BCP 47 format: en-US, fr-CA, es-MX, etc.
	// add more fields as needed (DB, SB, etc.)
}

func Init() (*Config, error) {
	// Resolve the stable data directory FIRST so every data path (DB, backups,
	// plugins, caches) hangs off it. Defaults to a per-user OS location so data
	// survives version upgrades; override with UT_DATA_DIR (the .deb uses
	// /var/lib/unitill). paths.Init makes it available to the plugin subsystem.
	dataDir := getenv("UT_DATA_DIR", paths.Default())
	paths.Init(dataDir)

	devMode, _ := strconv.ParseBool(getenv("UT_DEV_MODE", "false"))
	telemetryOptIn, _ := strconv.ParseBool(getenv("UT_MARKETPLACE_TELEMETRY_OPT_IN", "false"))
	healthCheckTimeout, _ := strconv.Atoi(getenv("UT_MARKETPLACE_HEALTH_CHECK_TIMEOUT_SEC", "5"))
	fallbackTimeout, _ := strconv.Atoi(getenv("UT_MARKETPLACE_FALLBACK_TIMEOUT_SEC", "30"))

	cfg := &Config{
		StoreName:  getenv("UT_STORE_NAME", "My Store"),
		ListenAddr: getenv("UT_LISTEN_ADDR", ":8080"),
		// Env:        getenv("UT_ENV", "local"),
		DataDir:  dataDir,
		DBPath:   getenv("UT_DB_PATH", filepath.Join(dataDir, "unitill-pos.db")),
		LogLevel: getenv("UT_LOG_LEVEL", "info"),
		Theme:    getenv("UT_THEME", "monarch"),
		DevMode:  devMode,
		Marketplace: MarketplaceConfig{
			EndpointURL:           getenv("UT_MARKETPLACE_ENDPOINT_URL", "http://127.0.0.1:8081"),
			StoreID:               getenv("UT_MARKETPLACE_STORE_ID", getenv("UT_STORE_NAME", "My Store")),
			DeviceID:              getenv("UT_MARKETPLACE_DEVICE_ID", ""),
			PublicKey:             getenv("UT_MARKETPLACE_PUBLIC_KEY", ""),
			UploadToken:           getenv("UT_MARKETPLACE_UPLOAD_TOKEN", ""),
			MerchantToken:         getenv("UT_MARKETPLACE_MERCHANT_TOKEN", ""),
			ClientID:              getenv("UT_MARKETPLACE_CLIENT_ID", ""),
			ClientSecret:          getenv("UT_MARKETPLACE_CLIENT_SECRET", ""),
			APIVersion:            getenv("UT_MARKETPLACE_API_VERSION", "1.0.0"),
			TelemetryOptIn:        telemetryOptIn,
			DevMode:               devMode,
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
		Locale:         getenv("UT_DEFAULT_LOCALE", "en-US"),
		TaxRate:        taxRate,
		TaxInclusive:   tax_inclusive,
	}
	cfg.Locales = locales
	cfg.DefaultLocale = getenv("UT_MARKETPLACE_LOCALE", "en-US") // BCP 47 format for marketplace
	// if you need validation, do it here and return an error
	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
