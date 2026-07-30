package common

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
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

func newTestStore(t *testing.T) *settings.Store {
	t.Helper()
	return settings.NewStore(openMigratedDB(t, "state.db").DB)
}

func baseCfg() *config.Config {
	return &config.Config{
		Theme: "monarch",
		Locales: config.Locales{
			Currency: "GBP",
			TaxRate:  2000,
		},
	}
}

func TestLoadState_DefaultsWhenStoreEmpty(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	cfg := baseCfg()

	st := LoadState(ctx, store, cfg)

	if st.Theme != "monarch" {
		t.Errorf("Theme = %q, want cfg default %q", st.Theme, "monarch")
	}
	if st.Currency != "GBP" {
		t.Errorf("Currency = %q, want cfg default %q", st.Currency, "GBP")
	}
	if st.Country != "GB" {
		t.Errorf("Country = %q, want hardcoded default %q", st.Country, "GB")
	}
	if st.TaxRatePct != 2000 {
		t.Errorf("TaxRatePct = %d, want cfg default 2000", st.TaxRatePct)
	}
	if st.TaxInclusive {
		t.Errorf("TaxInclusive = true, want cfg default false")
	}
	if st.AllowNegativeInventory {
		t.Errorf("AllowNegativeInventory = true, want default false")
	}
	if st.IdleLockMinutes != DefaultIdleLockMinutes {
		t.Errorf("IdleLockMinutes = %d, want default %d", st.IdleLockMinutes, DefaultIdleLockMinutes)
	}
	if st.OSKMode != "auto" {
		t.Errorf("OSKMode = %q, want default %q", st.OSKMode, "auto")
	}
	if st.KioskIdleResetSeconds != DefaultKioskIdleResetSeconds {
		t.Errorf("KioskIdleResetSeconds = %d, want default %d", st.KioskIdleResetSeconds, DefaultKioskIdleResetSeconds)
	}
	if st.UIScale != 0 {
		t.Errorf("UIScale = %v, want 0 (unset) when never configured", st.UIScale)
	}
}

func TestLoadState_OverridesFromStore(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	for k, v := range map[string]string{
		KeyTheme:                       "dark",
		KeyCurrency:                    "EUR",
		KeyCountry:                     "DE",
		KeyRegion:                      "BY",
		KeyTaxInclusive:                "true",
		KeyUIScale:                     "1.75",
		KeyTaxRate:                     "1900",
		"pos.allow_negative_inventory": "true",
		KeyIdleLock:                    "15",
		KeyOSK:                         "on",
		KeyKioskIdleReset:              "120",
	} {
		if err := store.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	st := LoadState(ctx, store, baseCfg())

	if st.Theme != "dark" {
		t.Errorf("Theme = %q, want %q", st.Theme, "dark")
	}
	if st.Currency != "EUR" {
		t.Errorf("Currency = %q, want %q", st.Currency, "EUR")
	}
	if st.Country != "DE" {
		t.Errorf("Country = %q, want %q", st.Country, "DE")
	}
	if st.Region != "BY" {
		t.Errorf("Region = %q, want %q", st.Region, "BY")
	}
	if !st.TaxInclusive {
		t.Errorf("TaxInclusive = false, want true")
	}
	if st.UIScale != 1.75 {
		t.Errorf("UIScale = %v, want 1.75", st.UIScale)
	}
	if st.TaxRatePct != 1900 {
		t.Errorf("TaxRatePct = %d, want 1900", st.TaxRatePct)
	}
	if !st.AllowNegativeInventory {
		t.Errorf("AllowNegativeInventory = false, want true")
	}
	if st.IdleLockMinutes != 15 {
		t.Errorf("IdleLockMinutes = %d, want 15", st.IdleLockMinutes)
	}
	if st.OSKMode != "on" {
		t.Errorf("OSKMode = %q, want %q", st.OSKMode, "on")
	}
	if st.KioskIdleResetSeconds != 120 {
		t.Errorf("KioskIdleResetSeconds = %d, want 120", st.KioskIdleResetSeconds)
	}
}

// A negative stored idle-lock/kiosk-reset value must not override the
// positive default — both guard with "n >= 0" specifically to reject that.
func TestLoadState_NegativeOverridesIgnored(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Set(ctx, KeyIdleLock, "-5"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Set(ctx, KeyKioskIdleReset, "-1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	st := LoadState(ctx, store, baseCfg())

	if st.IdleLockMinutes != DefaultIdleLockMinutes {
		t.Errorf("IdleLockMinutes = %d, want default %d (negative override must be rejected)", st.IdleLockMinutes, DefaultIdleLockMinutes)
	}
	if st.KioskIdleResetSeconds != DefaultKioskIdleResetSeconds {
		t.Errorf("KioskIdleResetSeconds = %d, want default %d (negative override must be rejected)", st.KioskIdleResetSeconds, DefaultKioskIdleResetSeconds)
	}
}

// A non-numeric stored value must not panic or corrupt state — LoadState
// silently falls back rather than propagating a parse error.
func TestLoadState_UnparsableValuesFallBackSilently(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	for _, k := range []string{KeyTaxInclusive, KeyUIScale, KeyTaxRate, KeyIdleLock, KeyKioskIdleReset} {
		if err := store.Set(ctx, k, "not-a-number"); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	st := LoadState(ctx, store, baseCfg())

	if st.TaxInclusive {
		t.Errorf("TaxInclusive = true from unparsable value, want default false")
	}
	if st.UIScale != 0 {
		t.Errorf("UIScale = %v from unparsable value, want 0", st.UIScale)
	}
	if st.TaxRatePct != 2000 {
		t.Errorf("TaxRatePct = %d from unparsable value, want cfg default 2000", st.TaxRatePct)
	}
	if st.IdleLockMinutes != DefaultIdleLockMinutes {
		t.Errorf("IdleLockMinutes = %d from unparsable value, want default %d", st.IdleLockMinutes, DefaultIdleLockMinutes)
	}
	if st.KioskIdleResetSeconds != DefaultKioskIdleResetSeconds {
		t.Errorf("KioskIdleResetSeconds = %d from unparsable value, want default %d", st.KioskIdleResetSeconds, DefaultKioskIdleResetSeconds)
	}
}

func TestSaveState_RoundTripsThroughLoadState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	want := RuntimeState{
		Theme:                  "dark",
		Currency:               "EUR",
		Country:                "DE",
		Region:                 "BY",
		TaxInclusive:           true,
		TaxRatePct:             1900,
		AllowNegativeInventory: true,
		UIScale:                1.5,
		IdleLockMinutes:        20,
		OSKMode:                "off",
		KioskIdleResetSeconds:  45,
	}

	SaveState(ctx, store, want)
	got := LoadState(ctx, store, baseCfg())

	if got != want {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

// SaveState only writes UIScale when it's > 0 — 0 means "unset, use the
// browser/kiosk default", not "explicitly zero scale". Confirm a
// previously-saved nonzero scale isn't clobbered by a later save with 0.
func TestSaveState_ZeroUIScaleDoesNotClobberPriorValue(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	SaveState(ctx, store, RuntimeState{UIScale: 1.5})
	SaveState(ctx, store, RuntimeState{UIScale: 0})

	st := LoadState(ctx, store, baseCfg())
	if st.UIScale != 1.5 {
		t.Fatalf("UIScale = %v after a zero-value save, want the untouched prior value 1.5", st.UIScale)
	}
}

// SaveState only writes OSKMode when non-empty, matching UIScale's guard.
func TestSaveState_EmptyOSKModeDoesNotClobberPriorValue(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	SaveState(ctx, store, RuntimeState{OSKMode: "on"})
	SaveState(ctx, store, RuntimeState{OSKMode: ""})

	st := LoadState(ctx, store, baseCfg())
	if st.OSKMode != "on" {
		t.Fatalf("OSKMode = %q after an empty-value save, want the untouched prior value %q", st.OSKMode, "on")
	}
}

func TestBuildMenu(t *testing.T) {
	base := []MenuItem{{Href: "/", Label: "Home"}}
	pm := &plugins.Manager{
		MenuPlugins: map[string]plugins.MenuPlugin{
			"a": {Route: "/plugin-a", Label: "Plugin A"},
			"b": {Route: "", Label: "No Route"},  // must be skipped
			"c": {Route: "/plugin-c", Label: ""}, // must be skipped
		},
	}

	got := BuildMenu(base, pm)

	if len(got) != 2 {
		t.Fatalf("BuildMenu returned %d items, want 2 (base + 1 valid plugin): %+v", len(got), got)
	}
	if got[0] != base[0] {
		t.Errorf("BuildMenu()[0] = %+v, want unchanged base item %+v", got[0], base[0])
	}
	want := MenuItem{Href: "/plugin-a", Label: "Plugin A"}
	if got[1] != want {
		t.Errorf("BuildMenu()[1] = %+v, want %+v", got[1], want)
	}

	// The base slice itself must not be mutated (BuildMenu appends to a copy).
	if len(base) != 1 {
		t.Fatalf("BuildMenu mutated the caller's base slice: len=%d, want 1", len(base))
	}
}

func TestBuildMenu_EmptyPlugins(t *testing.T) {
	base := []MenuItem{{Href: "/", Label: "Home"}}
	pm := &plugins.Manager{}

	got := BuildMenu(base, pm)

	if len(got) != 1 || got[0] != base[0] {
		t.Fatalf("BuildMenu with no plugins = %+v, want just the base item", got)
	}
}
