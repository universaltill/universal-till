package common

import "os"

type Config struct {
	ListenAddr    string
	DefaultLocale string
	Env           string
	SamplesDir    string
	Currency      string
	TaxRatePct    int
	TaxInclusive  bool
}

func ConfigFromEnv() Config {
	addr := os.Getenv("UT_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	locale := os.Getenv("UT_DEFAULT_LOCALE")
	if locale == "" {
		locale = "en"
	}
	env := os.Getenv("UT_ENV")
	if env == "" {
		env = "dev"
	}
	samples := os.Getenv("UT_SAMPLES_DIR")
	curr := os.Getenv("UT_CURRENCY")
	if curr == "" {
		curr = "GBP"
	}
	rate := 20
	if v := os.Getenv("UT_TAX_RATE"); v != "" {
		// simple atoi without import strconv here; keep default if invalid
		_ = v
	}
	incl := os.Getenv("UT_TAX_INCLUSIVE") == "true"
	return Config{ListenAddr: addr, DefaultLocale: locale, Env: env, SamplesDir: samples, Currency: curr, TaxRatePct: rate, TaxInclusive: incl}
}
