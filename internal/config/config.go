package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/taxrate"
)

type Locales struct {
	Currency string
	Locale   string
	// TaxRateBP is the shop's default VAT rate in basis points (810 = 8.1 %),
	// ut-docs#3259. UT_TAX_RATE and the stored store.tax_rate setting stay
	// percent strings; internal/taxrate.ParsePercent/FormatPercent convert.
	TaxRateBP    int
	TaxInclusive bool

	// CurrencySymbol was removed (ut-docs#1172): it was a dead, drift-prone
	// setting that nothing but its own load/save round-trip ever touched —
	// the wizard writes Currency (a code) but never this, so a shop that
	// changed currency kept whatever symbol booted with it. Every live
	// symbol display, receipts included, already derives the symbol from
	// Currency alone via httpx's currency registry (internal/httpx/currency.go),
	// so this second, independently-driftable copy just needed deleting.

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
	// MerchantToken is this till's cloud credential (ADR-0116 per-device
	// token since rotation; read it through enroll.Effective). Sent as the
	// bearer on till-facing cloud routes, including the plugin
	// download-token request (ut-docs#2930).
	MerchantToken string

	// OAuth2 client credentials for marketplace authentication (FR-018)
	ClientID     string
	ClientSecret string

	// API version pinning (FR-016)
	APIVersion string // semver format: "1.2.3"

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

// DefaultStoreName is the name a till has before its owner names it: the
// UT_STORE_NAME fallback, and the value migration 001 seeds into store.name.
// It is a placeholder, never a real shop name (ut-docs#3096).
const DefaultStoreName = "My Store"

// cloudDefaultStoreName is what the cloud's /api/v1/stores/register names a
// shop that registered with a blank name (ut-cloud handlers/stores.go).
const cloudDefaultStoreName = "Universal Till store"

// IsPlaceholderStoreName reports whether name is blank or one of the
// defaults the till or the cloud fills in for an unnamed shop, compared
// trimmed and case-insensitively. The setup wizard refuses such a name, and
// registration never sends one (ut-docs#3096).
func IsPlaceholderStoreName(name string) bool {
	name = strings.TrimSpace(name)
	return name == "" || strings.EqualFold(name, DefaultStoreName) || strings.EqualFold(name, cloudDefaultStoreName)
}

// MaxStoreNameRunes bounds a shop name after trimming — the same limit
// ut-cloud's claims.MaxStoreNameRunes puts on a rename from my. (ut-docs#3115).
const MaxStoreNameRunes = 80

// Reasons NormalizeStoreName refuses a name; compare with errors.Is.
var (
	// ErrStoreNameRequired: blank (after trimming, or only invisible
	// joiners) or a placeholder (IsPlaceholderStoreName).
	ErrStoreNameRequired = errors.New("store name required")
	// ErrStoreNameTooLong: more than MaxStoreNameRunes characters.
	ErrStoreNameTooLong = errors.New("store name too long")
	// ErrStoreNameInvalidChars: invalid UTF-8, control characters (line
	// breaks and tabs included), bidi override/isolate characters or
	// invisible format characters other than ZWNJ/ZWJ.
	ErrStoreNameInvalidChars = errors.New("store name has invalid characters")
)

// isBidiControl reports the explicit bidi embedding/override (U+202A–U+202E)
// and isolate (U+2066–U+2069) characters: they can make a name render as
// something other than what it stores on a receipt.
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// isInvisibleFormat reports a Unicode format (Cf) character other than
// ZWNJ (U+200C) and ZWJ (U+200D), which Persian words and emoji sequences
// need.
func isInvisibleFormat(r rune) bool {
	return unicode.Is(unicode.Cf, r) && r != 0x200C && r != 0x200D
}

// NormalizeStoreName trims s and checks it as a shop name (ut-docs#3115).
// It mirrors ut-cloud's claims.NormalizeStoreName — valid UTF-8, 1 to
// MaxStoreNameRunes runes, no control, bidi-control or invisible format
// characters (ZWNJ/ZWJ allowed, but not on their own) — so a name the
// till accepts is one the cloud would accept too, and additionally refuses
// the placeholders IsPlaceholderStoreName knows. A refusal wraps one of
// ErrStoreNameRequired, ErrStoreNameTooLong or ErrStoreNameInvalidChars.
func NormalizeStoreName(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", ErrStoreNameInvalidChars
	}
	s = strings.TrimSpace(s)
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		return "", ErrStoreNameRequired
	case n > MaxStoreNameRunes:
		return "", ErrStoreNameTooLong
	}
	if strings.ContainsFunc(s, unicode.IsControl) ||
		strings.ContainsFunc(s, isBidiControl) ||
		strings.ContainsFunc(s, isInvisibleFormat) {
		return "", ErrStoreNameInvalidChars
	}
	if !strings.ContainsFunc(s, func(r rune) bool { return !unicode.Is(unicode.Cf, r) }) {
		return "", ErrStoreNameRequired
	}
	if IsPlaceholderStoreName(s) {
		return "", ErrStoreNameRequired
	}
	return s, nil
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
	// CompiledDefaultLocale is UT_DEFAULT_LOCALE's resolved value (or its
	// "en-US" fallback), captured once here and never touched again —
	// unlike Locales.Locale, which internal/settings.Store.LoadRuntimeConfig
	// overwrites with the shop's persisted store.locale on every boot after
	// the first (see that function's own doc comment). A caller that needs
	// "what did this till boot with before any shop ever touched its
	// locale" (ut-docs#1892's backfill is exactly this) must read THIS
	// field, not Locales.Locale, which is live-mutable state by the time
	// pages.Init ever sees it. Also distinct from the top-level
	// DefaultLocale field above, which is UT_MARKETPLACE_LOCALE — the
	// marketplace/catalog locale, an unrelated concept (ut-docs#863 review).
	CompiledDefaultLocale string
	// Demo is UT_DEMO (ADR-0113 §1.1, ut-docs#2687): the public "try the
	// till" demo mode. Read here once; nothing else reads UT_DEMO
	// (scripts/ci/guard-demo-env.sh). On its own it switches nothing on —
	// internal/app's start gate also needs DemoToken, the paths.Data("demo")
	// marker file and the database's demo_instance flag, and refuses to
	// start when any of them is missing.
	Demo bool
	// DemoToken is UT_DEMO_TOKEN: the per-till secret the demo broker sends
	// in X-UT-Demo-Token on every proxied request (ADR-0113 §1.3).
	DemoToken string
	// AuthDisabled is UT_AUTH=off (auth.Disabled), the CI/dev escape hatch
	// that turns the session middleware off. Read here once at boot so
	// pages.Init and the demo start gate (which refuses Demo with auth off,
	// ADR-0113 §1.9) see the same value.
	AuthDisabled bool
	// CSPReportOnly is UT_CSP_REPORT_ONLY (ut-docs#2913, slice 1): when on,
	// every response carries a Content-Security-Policy-Report-Only header
	// and POST /csp-report collects the violations browsers report, so the
	// policy can be inventoried before a later slice enforces it. Default
	// off; an unparseable value is off too, like UT_DEV_MODE — a typo never
	// changes what a production till sends.
	CSPReportOnly bool
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
	// Unlike the other flags, an unparseable UT_DEMO is a refusal to start
	// rather than a silent "false" (ADR-0113 §1.2: demo mode never guesses).
	demo, err := strconv.ParseBool(getenv("UT_DEMO", "false"))
	if err != nil {
		return nil, fmt.Errorf("UT_DEMO=%q is not a boolean: %w", os.Getenv("UT_DEMO"), err)
	}
	cspReportOnly, _ := strconv.ParseBool(getenv("UT_CSP_REPORT_ONLY", "false"))
	healthCheckTimeout, _ := strconv.Atoi(getenv("UT_MARKETPLACE_HEALTH_CHECK_TIMEOUT_SEC", "5"))
	fallbackTimeout, _ := strconv.Atoi(getenv("UT_MARKETPLACE_FALLBACK_TIMEOUT_SEC", "30"))

	cfg := &Config{
		StoreName:  getenv("UT_STORE_NAME", DefaultStoreName),
		ListenAddr: getenv("UT_LISTEN_ADDR", ":8080"),
		// Env:        getenv("UT_ENV", "local"),
		DataDir:       dataDir,
		DBPath:        getenv("UT_DB_PATH", filepath.Join(dataDir, "unitill-pos.db")),
		LogLevel:      getenv("UT_LOG_LEVEL", "info"),
		Theme:         getenv("UT_THEME", "monarch"),
		DevMode:       devMode,
		Demo:          demo,
		DemoToken:     os.Getenv("UT_DEMO_TOKEN"),
		AuthDisabled:  auth.Disabled(os.Getenv("UT_AUTH")),
		CSPReportOnly: cspReportOnly,
		Marketplace: MarketplaceConfig{
			EndpointURL:           getenv("UT_MARKETPLACE_ENDPOINT_URL", "http://127.0.0.1:8081/api"),
			StoreID:               getenv("UT_MARKETPLACE_STORE_ID", getenv("UT_STORE_NAME", DefaultStoreName)),
			DeviceID:              getenv("UT_MARKETPLACE_DEVICE_ID", ""),
			PublicKey:             getenv("UT_MARKETPLACE_PUBLIC_KEY", ""),
			UploadToken:           getenv("UT_MARKETPLACE_UPLOAD_TOKEN", ""),
			MerchantToken:         getenv("UT_MARKETPLACE_MERCHANT_TOKEN", ""),
			ClientID:              getenv("UT_MARKETPLACE_CLIENT_ID", ""),
			ClientSecret:          getenv("UT_MARKETPLACE_CLIENT_SECRET", ""),
			APIVersion:            getenv("UT_MARKETPLACE_API_VERSION", "1.0.0"),
			DevMode:               devMode,
			DevOverrideURL:        getenv("UT_MARKETPLACE_DEV_OVERRIDE_URL", ""),
			HealthCheckTimeoutSec: healthCheckTimeout,
			FallbackTimeoutSec:    fallbackTimeout,
			RequestTimeoutSec:     30,
		},
	}

	// UT_TAX_RATE is a percent string, fractional allowed ("8.1"); an
	// unreadable value falls back to 20 % (ut-docs#3259).
	taxRateBP, ok := taxrate.ParsePercent(getenv("UT_TAX_RATE", "20"))
	if !ok {
		taxRateBP = 2000
	}
	tax_inclusive, _ := strconv.ParseBool(getenv("UT_TAX_INCLUSIVE", "true"))

	locales := Locales{
		Currency:     getenv("UT_CURRENCY", "GBP"),
		Locale:       getenv("UT_DEFAULT_LOCALE", "en-US"),
		TaxRateBP:    taxRateBP,
		TaxInclusive: tax_inclusive,
	}
	cfg.Locales = locales
	cfg.DefaultLocale = getenv("UT_MARKETPLACE_LOCALE", "en-US") // BCP 47 format for marketplace
	// Snapshot locales.Locale into its own immutable field (ut-docs#1892) —
	// see CompiledDefaultLocale's own doc comment on the struct for why this
	// can't just be cfg.Locales.Locale read later.
	cfg.CompiledDefaultLocale = locales.Locale
	// if you need validation, do it here and return an error
	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
