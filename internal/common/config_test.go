package common

import "testing"

func TestConfigFromEnvDefaults(t *testing.T) {
	// Ensure environment variables are empty to test defaults
	t.Setenv("UT_LISTEN_ADDR", "")
	t.Setenv("UT_DEFAULT_LOCALE", "")
	t.Setenv("UT_ENV", "")
	t.Setenv("UT_SAMPLES_DIR", "")
	t.Setenv("UT_CURRENCY", "")
	t.Setenv("UT_TAX_RATE", "")
	t.Setenv("UT_TAX_INCLUSIVE", "")

	cfg := ConfigFromEnv()

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr default = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.DefaultLocale != "en" {
		t.Errorf("DefaultLocale default = %q, want %q", cfg.DefaultLocale, "en")
	}
	if cfg.Env != "dev" {
		t.Errorf("Env default = %q, want %q", cfg.Env, "dev")
	}
	if cfg.SamplesDir != "" {
		t.Errorf("SamplesDir default = %q, want empty", cfg.SamplesDir)
	}
	if cfg.Currency != "GBP" {
		t.Errorf("Currency default = %q, want %q", cfg.Currency, "GBP")
	}
	if cfg.TaxRatePct != 20 {
		t.Errorf("TaxRatePct default = %d, want %d", cfg.TaxRatePct, 20)
	}
	if cfg.TaxInclusive {
		t.Errorf("TaxInclusive default = %v, want false", cfg.TaxInclusive)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("UT_LISTEN_ADDR", ":9090")
	t.Setenv("UT_DEFAULT_LOCALE", "fr")
	t.Setenv("UT_ENV", "prod")
	t.Setenv("UT_SAMPLES_DIR", "/data")
	t.Setenv("UT_CURRENCY", "USD")
	// UT_TAX_RATE is ignored by ConfigFromEnv
	t.Setenv("UT_TAX_INCLUSIVE", "true")

	cfg := ConfigFromEnv()

	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
	if cfg.DefaultLocale != "fr" {
		t.Errorf("DefaultLocale = %q, want %q", cfg.DefaultLocale, "fr")
	}
	if cfg.Env != "prod" {
		t.Errorf("Env = %q, want %q", cfg.Env, "prod")
	}
	if cfg.SamplesDir != "/data" {
		t.Errorf("SamplesDir = %q, want %q", cfg.SamplesDir, "/data")
	}
	if cfg.Currency != "USD" {
		t.Errorf("Currency = %q, want %q", cfg.Currency, "USD")
	}
	if cfg.TaxRatePct != 20 {
		t.Errorf("TaxRatePct = %d, want %d (env ignored)", cfg.TaxRatePct, 20)
	}
	if !cfg.TaxInclusive {
		t.Errorf("TaxInclusive = %v, want true", cfg.TaxInclusive)
	}
}
