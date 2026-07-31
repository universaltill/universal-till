package settings

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// openMigratedDB gives the test a real, fully migrated schema — same
// pattern as internal/data and internal/cloudsync's own test helpers.
func openMigratedDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(openMigratedDB(t, "settings.db").DB)
}

func TestStore_GetSetAll(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// "theme" is not one of the migrated schema's seeded defaults
	// (store.name, store.currency), so it's genuinely absent here.
	if _, ok, err := s.Get(ctx, "theme"); err != nil || ok {
		t.Fatalf("Get(theme) before any Set = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	if err := s.Set(ctx, "theme", "dark"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok, err := s.Get(ctx, "theme")
	if err != nil || !ok || v != "dark" {
		t.Fatalf("Get(theme) = %q ok=%v err=%v, want %q ok=true", v, ok, err, "dark")
	}

	// ON CONFLICT update, not a duplicate row.
	if err := s.Set(ctx, "theme", "light"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}

	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if all["theme"] != "light" {
		t.Fatalf("All()[theme] = %q, want %q (Set must UPDATE on conflict, not insert a second row)", all["theme"], "light")
	}
}

func baseCfg() *config.Config {
	return &config.Config{
		Theme:     "monarch",
		StoreName: "My Store",
		Locales: config.Locales{
			Currency:       "GBP",
			CurrencySymbol: "£",
			Locale:         "en-GB",
			TaxRate:        2000,
			TaxInclusive:   false,
		},
	}
}

// Beyond the migrated schema's seeded rows (store.name, store.currency —
// which happen to already match baseCfg's values; 001 also seeded the dead
// pos.tax_inclusive key, removed by migration 022), nothing else is in the
// DB: every other field must keep whatever the caller's cfg already had
// (env/default-derived), not get zeroed out.
func TestLoadRuntimeConfig_EmptyStoreKeepsDefaults(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cfg := baseCfg()
	want := *cfg

	s.LoadRuntimeConfig(ctx, cfg)

	if *cfg != want {
		t.Fatalf("LoadRuntimeConfig mutated cfg from an empty store: got %+v, want %+v", *cfg, want)
	}
}

func TestLoadRuntimeConfig_OverridesFromStore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for k, v := range map[string]string{
		"theme":                 "dark",
		"store.name":            "Farshid's Shop",
		"store.currency":        "EUR",
		"store.currency_symbol": "€",
		"store.locale":          "de-DE",
		"store.tax_inclusive":   "true",
		"store.tax_rate":        "1900",
	} {
		if err := s.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	cfg := baseCfg()
	s.LoadRuntimeConfig(ctx, cfg)

	if cfg.Theme != "dark" {
		t.Errorf("Theme = %q, want %q", cfg.Theme, "dark")
	}
	if cfg.StoreName != "Farshid's Shop" {
		t.Errorf("StoreName = %q, want %q", cfg.StoreName, "Farshid's Shop")
	}
	if cfg.Locales.Currency != "EUR" {
		t.Errorf("Currency = %q, want %q", cfg.Locales.Currency, "EUR")
	}
	if cfg.Locales.CurrencySymbol != "€" {
		t.Errorf("CurrencySymbol = %q, want %q", cfg.Locales.CurrencySymbol, "€")
	}
	if cfg.Locales.Locale != "de-DE" {
		t.Errorf("Locale = %q, want %q", cfg.Locales.Locale, "de-DE")
	}
	if !cfg.Locales.TaxInclusive {
		t.Errorf("TaxInclusive = false, want true")
	}
	if cfg.Locales.TaxRate != 1900 {
		t.Errorf("TaxRate = %d, want 1900", cfg.Locales.TaxRate)
	}
}

// Regression: LoadRuntimeConfig used to gate the currency_symbol assignment
// on whether "store.currency" was set, not "store.currency_symbol" — so a
// symbol-only override (currency itself genuinely unset) was silently
// dropped. Currency and its symbol are independent settings keys and must
// be loaded independently.
func TestLoadRuntimeConfig_CurrencySymbolIndependentOfCurrency(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "settings.db")
	s := NewStore(d.DB)

	// 001_init.sql seeds "store.currency"='GBP' by default — clear it so
	// this test genuinely exercises "currency unset, symbol set" rather
	// than accidentally passing because the seeded default is non-empty
	// (which is exactly what let the original buggy `if curr != ""` guard
	// slip through: curr is never actually empty in a fresh migrated DB).
	repo := data.NewSettingsRepo(d.DB)
	if err := repo.Delete(ctx, "store.currency"); err != nil {
		t.Fatalf("clear seeded default: %v", err)
	}
	if err := s.Set(ctx, "store.currency_symbol", "kr"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := baseCfg()
	s.LoadRuntimeConfig(ctx, cfg)

	if cfg.Locales.CurrencySymbol != "kr" {
		t.Fatalf("CurrencySymbol = %q, want %q (a currency_symbol-only override must not be ignored)", cfg.Locales.CurrencySymbol, "kr")
	}
	if cfg.Locales.Currency != "GBP" {
		t.Fatalf("Currency = %q, want unchanged default %q", cfg.Locales.Currency, "GBP")
	}
}

// SaveRuntimeConfig must persist under the exact keys LoadRuntimeConfig
// reads back — otherwise a value written at boot is never seen again.
// Regression: tax_inclusive was written to "pos.tax_inclusive" while
// LoadRuntimeConfig (and pages/common's KeyTaxInclusive, the real
// settings-admin path) both read "store.tax_inclusive", so a saved value
// was silently orphaned and never round-tripped.
func TestSaveRuntimeConfig_RoundTripsThroughLoad(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	cfg := baseCfg()
	cfg.Theme = "dark"
	cfg.StoreName = "Farshid's Shop"
	cfg.Locales.Currency = "EUR"
	cfg.Locales.CurrencySymbol = "€"
	cfg.Locales.Locale = "de-DE"
	cfg.Locales.TaxInclusive = true
	cfg.Locales.TaxRate = 1900

	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveRuntimeConfig: %v", err)
	}

	reloaded := baseCfg()
	*reloaded = config.Config{} // zero it so Load must actually populate every field from the store
	s.LoadRuntimeConfig(ctx, reloaded)

	if reloaded.Theme != cfg.Theme {
		t.Errorf("Theme round-trip = %q, want %q", reloaded.Theme, cfg.Theme)
	}
	if reloaded.StoreName != cfg.StoreName {
		t.Errorf("StoreName round-trip = %q, want %q", reloaded.StoreName, cfg.StoreName)
	}
	if reloaded.Locales.Currency != cfg.Locales.Currency {
		t.Errorf("Currency round-trip = %q, want %q", reloaded.Locales.Currency, cfg.Locales.Currency)
	}
	if reloaded.Locales.CurrencySymbol != cfg.Locales.CurrencySymbol {
		t.Errorf("CurrencySymbol round-trip = %q, want %q", reloaded.Locales.CurrencySymbol, cfg.Locales.CurrencySymbol)
	}
	if reloaded.Locales.Locale != cfg.Locales.Locale {
		t.Errorf("Locale round-trip = %q, want %q", reloaded.Locales.Locale, cfg.Locales.Locale)
	}
	if reloaded.Locales.TaxInclusive != cfg.Locales.TaxInclusive {
		t.Errorf("TaxInclusive round-trip = %v, want %v", reloaded.Locales.TaxInclusive, cfg.Locales.TaxInclusive)
	}
	if reloaded.Locales.TaxRate != cfg.Locales.TaxRate {
		t.Errorf("TaxRate round-trip = %d, want %d", reloaded.Locales.TaxRate, cfg.Locales.TaxRate)
	}
}

// 001_init.sql seeds "store.name"='My Store' by default, and baseCfg() also
// uses "My Store" — so TestLoadRuntimeConfig_EmptyStoreKeepsDefaults can't
// actually distinguish "kept cfg.StoreName" from "read the seeded row" for
// this field (an independent review of this batch caught the same blind
// spot the currency_symbol test above was already fixed for). Clear the
// seeded row and use a cfg value that couldn't be confused with it.
func TestLoadRuntimeConfig_EmptyNameKeepsCfgDefault(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "settings.db")
	s := NewStore(d.DB)
	if err := data.NewSettingsRepo(d.DB).Delete(ctx, "store.name"); err != nil {
		t.Fatalf("clear seeded default: %v", err)
	}

	cfg := baseCfg()
	cfg.StoreName = "Farshid's Shop"
	s.LoadRuntimeConfig(ctx, cfg)

	if cfg.StoreName != "Farshid's Shop" {
		t.Fatalf("StoreName = %q, want unchanged cfg default %q when store.name is genuinely unset", cfg.StoreName, "Farshid's Shop")
	}
}

// SaveRuntimeConfig's bug #2 above was exactly a key-name divergence
// between what it wrote and what LoadRuntimeConfig/pages/common read back —
// the round-trip test catches Save and Load disagreeing, but not both
// sides silently agreeing on the WRONG key. Pin the literal key names here
// (rather than referencing internal/pages/common's Key* constants, which
// would risk an import cycle since common imports settings) so a future
// rename on either side fails loudly.
func TestSaveRuntimeConfig_WritesExpectedKeys(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "settings.db")
	s := NewStore(d.DB)

	if err := s.SaveRuntimeConfig(ctx, baseCfg()); err != nil {
		t.Fatalf("SaveRuntimeConfig: %v", err)
	}

	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, k := range []string{
		"theme", "store.name", "store.currency", "store.currency_symbol",
		"store.locale", "store.tax_inclusive", "store.tax_rate",
	} {
		if _, ok := all[k]; !ok {
			t.Errorf("SaveRuntimeConfig did not write expected key %q (all keys: %v)", k, all)
		}
	}
}

func TestSaveRuntimeConfig_PropagatesRepoError(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "settings.db")
	s := NewStore(d.DB)
	d.Close() // force every subsequent Set to fail

	if err := s.SaveRuntimeConfig(ctx, baseCfg()); err == nil {
		t.Fatal("SaveRuntimeConfig on a closed DB returned nil error, want non-nil")
	}
}

// ut-docs#12: SaveRuntimeConfig writes 7 keys; a mid-way failure must leave
// the settings table exactly as it was, not with a prefix of the new values.
// A trigger aborts the insert of store.locale (which sorts after
// store.currency), so a non-transactional save would already have committed
// the new currency by the time the failure hits.
func TestSaveRuntimeConfig_Atomic(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "atomic.db")
	s := NewStore(d.DB)

	if _, err := d.DB.Exec(`
CREATE TRIGGER boom BEFORE INSERT ON settings
WHEN NEW.key = 'store.locale'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	cfg := baseCfg()
	cfg.Locales.Currency = "EUR" // differs from 001's seeded 'GBP'

	if err := s.SaveRuntimeConfig(ctx, cfg); err == nil {
		t.Fatal("SaveRuntimeConfig with an aborting trigger returned nil error, want non-nil")
	}

	curr, ok, err := s.Get(ctx, "store.currency")
	if err != nil || !ok {
		t.Fatalf("Get(store.currency) = ok=%v err=%v, want the seeded row intact", ok, err)
	}
	if curr != "GBP" {
		t.Fatalf("store.currency = %q after failed save, want seeded %q (partial write not rolled back)", curr, "GBP")
	}
	// store.currency_symbol sorts between store.currency and store.locale,
	// so a non-transactional save would have committed it before the abort.
	if _, ok, _ := s.Get(ctx, "store.currency_symbol"); ok {
		t.Fatal("store.currency_symbol was written despite the save failing; save must be all-or-nothing")
	}
}
