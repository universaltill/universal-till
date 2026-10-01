package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newStore3259(t *testing.T) *settings.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return settings.NewStore(d.DB)
}

func cfg3259() *config.Config {
	return &config.Config{
		Theme:     "monarch",
		StoreName: "My Store",
		Locales:   config.Locales{Currency: "CHF", Locale: "de-CH", TaxRateBP: 2000},
	}
}

// ut-docs#3259 regression: app.Run calls LoadRuntimeConfig then
// SaveRuntimeConfig on every boot. A fractional default rate ("8.1") used to
// fail strconv.Atoi, leave the config default (UT_TAX_RATE, 20) in place,
// and the Save then wrote "20" over the shop's rate.
func TestBootRoundTrip_PreservesFractionalTaxRate(t *testing.T) {
	ctx := context.Background()
	s := newStore3259(t)
	if err := s.Set(ctx, common.KeyTaxRate, "8.1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := cfg3259()
	s.LoadRuntimeConfig(ctx, cfg)
	if cfg.Locales.TaxRateBP != 810 {
		t.Fatalf("LoadRuntimeConfig TaxRateBP = %d, want 810", cfg.Locales.TaxRateBP)
	}
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveRuntimeConfig: %v", err)
	}
	if v, _, _ := s.Get(ctx, common.KeyTaxRate); v != "8.1" {
		t.Fatalf("store.tax_rate after boot Load→Save = %q, want %q", v, "8.1")
	}

	if st := common.LoadState(ctx, s, cfg3259()); st.TaxRateBP != 810 {
		t.Fatalf("LoadState TaxRateBP = %d, want 810", st.TaxRateBP)
	}
}

// A whole-percent value written by every earlier build ("19") still reads
// as 1900 bp and is written back byte-identically.
func TestBootRoundTrip_WholePercentUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newStore3259(t)
	if err := s.Set(ctx, common.KeyTaxRate, "19"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := cfg3259()
	s.LoadRuntimeConfig(ctx, cfg)
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveRuntimeConfig: %v", err)
	}
	if v, _, _ := s.Get(ctx, common.KeyTaxRate); v != "19" {
		t.Fatalf("store.tax_rate = %q, want %q", v, "19")
	}
	if cfg.Locales.TaxRateBP != 1900 {
		t.Fatalf("TaxRateBP = %d, want 1900", cfg.Locales.TaxRateBP)
	}
}

// A stored value this build can't read (e.g. a future, finer format) must
// not be clobbered by the config default on boot: LoadState falls back to
// the default for this run, but the shop's stored rate survives.
func TestBootRoundTrip_UnparseableStoredRateNotClobbered(t *testing.T) {
	ctx := context.Background()
	s := newStore3259(t)
	if err := s.Set(ctx, common.KeyTaxRate, "8.125"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := cfg3259()
	s.LoadRuntimeConfig(ctx, cfg)
	if cfg.Locales.TaxRateBP != 2000 {
		t.Fatalf("TaxRateBP = %d, want config default 2000", cfg.Locales.TaxRateBP)
	}
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveRuntimeConfig: %v", err)
	}
	if v, _, _ := s.Get(ctx, common.KeyTaxRate); v != "8.125" {
		t.Fatalf("store.tax_rate = %q, want untouched %q", v, "8.125")
	}
}

// An empty store still gets the config default written on first boot.
func TestBootRoundTrip_EmptyStoreWritesDefault(t *testing.T) {
	ctx := context.Background()
	s := newStore3259(t)
	cfg := cfg3259()
	cfg.Locales.TaxRateBP = 810
	s.LoadRuntimeConfig(ctx, cfg)
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveRuntimeConfig: %v", err)
	}
	if v, _, _ := s.Get(ctx, common.KeyTaxRate); v != "8.1" {
		t.Fatalf("store.tax_rate = %q, want %q", v, "8.1")
	}
}
