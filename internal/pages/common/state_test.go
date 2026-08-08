package common

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
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

// A non-numeric/non-bool stored value must not panic or corrupt state —
// LoadState silently falls back to the cfg default rather than propagating
// a parse error. Uses a cfg whose TaxInclusive default is TRUE specifically
// so this is a real proof, not a coincidence: strconv.ParseBool/Atoi/
// ParseFloat's own error-path zero values (false/0/0.0) all happen to equal
// this package's OTHER defaults, so a naive assertion here could pass even
// if LoadState discarded the cfg default and fell through to a bare zero
// value instead of actually keeping it (a real bug this test caught in
// TaxInclusive/AllowNegativeInventory: they used to do exactly that, via
// `st.TaxInclusive, _ = strconv.ParseBool(v)` swallowing the error and
// leaving the zero value — fixed to guard on `err == nil` like every
// sibling field already does).
func TestLoadState_UnparsableValuesFallBackToCfgDefault(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	for _, k := range []string{KeyTaxInclusive, KeyUIScale, KeyTaxRate, KeyIdleLock, KeyKioskIdleReset, "pos.allow_negative_inventory"} {
		if err := store.Set(ctx, k, "not-a-number"); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	cfg := baseCfg()
	cfg.Locales.TaxInclusive = true // distinguishes "kept the cfg default" from "fell to the zero value"

	st := LoadState(ctx, store, cfg)

	if !st.TaxInclusive {
		t.Errorf("TaxInclusive = false from an unparsable stored value, want cfg default true (must not silently discard it)")
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
	// AllowNegativeInventory's own "default" is a fixed false, not cfg-derived,
	// so an unparsable value must still leave it at that same false — this
	// assertion alone can't distinguish the bug (false is both the buggy
	// zero-value AND the correct default here), which is exactly why
	// TaxInclusive above is pinned to true instead.
	if st.AllowNegativeInventory {
		t.Errorf("AllowNegativeInventory = true from unparsable value, want default false")
	}
}

// 001_init.sql seeds "store.currency"='GBP' by default, and baseCfg() also
// uses "GBP" — so every other LoadState test in this file can't actually
// distinguish "fell back to cfg.Locales.Currency" from "read the seeded
// row" for the Currency field. Clear the seeded row and use a cfg default
// that couldn't be confused with it, so this one genuinely proves the
// get(KeyCurrency, cfg.Locales.Currency) wiring, not just its coincidental
// agreement with the migration's own default.
func TestLoadState_CurrencyFallsBackToCfgDefaultWhenUnset(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "state.db")
	store := settings.NewStore(d.DB)
	if err := data.NewSettingsRepo(d.DB).Delete(ctx, KeyCurrency); err != nil {
		t.Fatalf("clear seeded default: %v", err)
	}

	cfg := baseCfg()
	cfg.Locales.Currency = "JPY"

	st := LoadState(ctx, store, cfg)
	if st.Currency != "JPY" {
		t.Fatalf("Currency = %q, want cfg default %q when store.currency is genuinely unset", st.Currency, "JPY")
	}
}

// ut-docs#244: the service charge rate settings key must support decimal
// percents (12.5% is the standard UK restaurant rate) at basis-point
// precision, not just whole percent.
func TestLoadState_ServiceChargeRateFractionalPercent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Set(ctx, KeyServiceChargeRate, "12.5"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	st := LoadState(ctx, store, baseCfg())

	if st.ServiceChargeRateBasisPoints != 1250 {
		t.Errorf("ServiceChargeRateBasisPoints = %d, want 1250 (12.5%%)", st.ServiceChargeRateBasisPoints)
	}
}

// A whole-percent value written by a pre-fix version ("12") must keep
// meaning exactly what it always did (1200bp) — no migration, no silent
// behaviour change for an existing shop's till on upgrade.
func TestLoadState_ServiceChargeRateWholePercentBackwardCompatible(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Set(ctx, KeyServiceChargeRate, "12"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	st := LoadState(ctx, store, baseCfg())

	if st.ServiceChargeRateBasisPoints != 1200 {
		t.Errorf("ServiceChargeRateBasisPoints = %d, want 1200 (12%%, unchanged from before this fix)", st.ServiceChargeRateBasisPoints)
	}
}

// Invalid/negative stored values must fall back to the default (0 =
// disabled), matching every sibling field's silent-fallback-at-load-time
// convention — not panic, not a negative rate.
func TestLoadState_ServiceChargeRateInvalidFallsBackToDefault(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"not-a-number", "-5", "-5.5", "NaN", "Inf", "-Inf", "Infinity"} {
		t.Run(bad, func(t *testing.T) {
			ctx := ctx
			store := newTestStore(t)
			if err := store.Set(ctx, KeyServiceChargeRate, bad); err != nil {
				t.Fatalf("seed: %v", err)
			}

			st := LoadState(ctx, store, baseCfg())

			if st.ServiceChargeRateBasisPoints != 0 {
				t.Errorf("ServiceChargeRateBasisPoints = %d from %q, want default 0", st.ServiceChargeRateBasisPoints, bad)
			}
		})
	}
}

// SaveState persists basis points back as the same decimal-percent string
// format LoadState reads (round-trips exactly, and a whole-percent value
// keeps rendering without a spurious ".0").
func TestSaveState_ServiceChargeRateRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := SaveState(ctx, store, RuntimeState{ServiceChargeRateBasisPoints: 1250}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	raw, ok, err := store.Get(ctx, KeyServiceChargeRate)
	if err != nil || !ok {
		t.Fatalf("Get(%s) = ok=%v err=%v", KeyServiceChargeRate, ok, err)
	}
	if raw != "12.5" {
		t.Fatalf("stored %s = %q, want %q", KeyServiceChargeRate, raw, "12.5")
	}
	if st := LoadState(ctx, store, baseCfg()); st.ServiceChargeRateBasisPoints != 1250 {
		t.Fatalf("round trip: ServiceChargeRateBasisPoints = %d, want 1250", st.ServiceChargeRateBasisPoints)
	}

	if err := SaveState(ctx, store, RuntimeState{ServiceChargeRateBasisPoints: 1200}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if raw, _, _ := store.Get(ctx, KeyServiceChargeRate); raw != "12" {
		t.Fatalf("stored %s = %q, want %q (whole percent, no spurious decimal)", KeyServiceChargeRate, raw, "12")
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

	if err := SaveState(ctx, store, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
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
	if err := SaveState(ctx, store, RuntimeState{UIScale: 1.5}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := SaveState(ctx, store, RuntimeState{UIScale: 0}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	st := LoadState(ctx, store, baseCfg())
	if st.UIScale != 1.5 {
		t.Fatalf("UIScale = %v after a zero-value save, want the untouched prior value 1.5", st.UIScale)
	}
}

// SaveState only writes OSKMode when non-empty, matching UIScale's guard.
func TestSaveState_EmptyOSKModeDoesNotClobberPriorValue(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := SaveState(ctx, store, RuntimeState{OSKMode: "on"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := SaveState(ctx, store, RuntimeState{OSKMode: ""}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	st := LoadState(ctx, store, baseCfg())
	if st.OSKMode != "on" {
		t.Fatalf("OSKMode = %q after an empty-value save, want the untouched prior value %q", st.OSKMode, "on")
	}
}

// SaveState writes every field in one transaction; a mid-way failure must
// leave no partial mix of old and new values (mirrors
// settings.TestSaveRuntimeConfig_Atomic for the settings-page write path).
func TestSaveState_Atomic(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "state_atomic.db")
	store := settings.NewStore(d.DB)

	if err := SaveState(ctx, store, RuntimeState{Currency: "GBP", TaxRatePct: 20}); err != nil {
		t.Fatalf("seed SaveState: %v", err)
	}

	if _, err := d.DB.Exec(`
CREATE TRIGGER boom BEFORE INSERT ON settings
WHEN NEW.key = 'store.tax_rate'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err := SaveState(ctx, store, RuntimeState{Currency: "EUR", TaxRatePct: 7})
	if err == nil {
		t.Fatal("SaveState with an aborting trigger returned nil error, want non-nil")
	}

	curr, ok, err := store.Get(ctx, KeyCurrency)
	if err != nil || !ok {
		t.Fatalf("Get(%s) = ok=%v err=%v, want the seeded row intact", KeyCurrency, ok, err)
	}
	// store.currency sorts (and would have been written) before
	// store.tax_rate, so a non-transactional save would have committed it
	// before the abort.
	if curr != "GBP" {
		t.Fatalf("Currency = %q after a failed save, want seeded %q (partial write not rolled back)", curr, "GBP")
	}
	rate, ok, err := store.Get(ctx, KeyTaxRate)
	if err != nil || !ok {
		t.Fatalf("Get(%s) = ok=%v err=%v, want the seeded row intact", KeyTaxRate, ok, err)
	}
	if rate != "20" {
		t.Fatalf("TaxRatePct = %q after a failed save, want seeded %q", rate, "20")
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
}

// BuildMenu must not write into the caller's backing array — a naive
// `items := base` (rather than a fresh copy) would only be caught if base
// has spare capacity, since Go's append reuses backing storage when it
// fits. A same-length composite literal always has len==cap, so append
// reallocates regardless of whether BuildMenu copies first — this test
// gives base real spare capacity via a sentinel so a missing copy is
// actually observable.
func TestBuildMenu_DoesNotWriteIntoCallersBackingArray(t *testing.T) {
	backing := make([]MenuItem, 2, 4)
	backing[0] = MenuItem{Href: "/", Label: "Home"}
	sentinel := MenuItem{Href: "/sentinel", Label: "Sentinel"}
	backing[1] = sentinel
	base := backing[:1] // len 1, cap 4 — plenty of room for append to reuse in place

	pm := &plugins.Manager{MenuPlugins: map[string]plugins.MenuPlugin{
		"a": {Route: "/plugin-a", Label: "Plugin A"},
	}}
	_ = BuildMenu(base, pm)

	if backing[1] != sentinel {
		t.Fatalf("BuildMenu overwrote the caller's backing array at index 1: got %+v, want untouched sentinel %+v", backing[1], sentinel)
	}
	if len(base) != 1 {
		t.Fatalf("BuildMenu mutated the caller's base slice header: len=%d, want 1", len(base))
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
