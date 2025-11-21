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

type Config struct {
	StoreName  string
	ListenAddr string
	Env        string
	DBPath     string
	Locales    Locales
	LogLevel   string
	// add more fields as needed (DB, SB, etc.)
}

func Init() (*Config, error) {
	cfg := &Config{
		StoreName:  getenv("UT_STORE_NAME", "My Store"),
		ListenAddr: getenv("UT_LISTEN_ADDR", ":8080"),
		// Env:        getenv("UT_ENV", "local"),
		DBPath:   getenv("UT_DB_PATH", "./data/unitill-pos.db"),
		LogLevel: getenv("UT_LOG_LEVEL", "info"),
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
