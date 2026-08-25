package common

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

const (
	KeyTheme             = "theme"
	KeyCurrency          = "store.currency"
	KeyCountry           = "store.country"
	KeyRegion            = "store.region"
	KeyTaxInclusive      = "store.tax_inclusive"
	KeyTaxRate           = "store.tax_rate"
	KeyServiceChargeRate = "store.service_charge_rate_pct"
	// KeyShopType holds the ADR-0026 shop-type taxonomy value chosen in the
	// setup wizard (cafe|retail|service|hospitality|market_stall|other) —
	// ut-docs#539. Optional: empty/missing is fine.
	KeyShopType       = "shop.type"
	KeyUIScale        = "display.ui_scale"
	KeyOSK            = "display.osk"
	KeyIdleLock       = "auth.idle_lock_minutes"
	KeyKioskIdleReset = "kiosk.idle_reset_seconds"
	// KeyWindowMode is the till's own window/process display mode (ut-docs#608
	// scaffold): fullscreen|kiosk|maximized|normal. This card only stores and
	// surfaces the setting — actually applying it to the OS window is
	// #609 (macOS)/#610 (Windows)/#611 (Linux/Pi).
	KeyWindowMode = "display.window_mode"
	// KeyLaunchOnStartup is the till's autostart-on-boot preference
	// (ut-docs#608 scaffold) — this process (unitill-pos) only stores and
	// surfaces it. OS-level application is the desktop shell's own job
	// (ut-docs#611, Linux desktop) via GET /api/window-mode, since the
	// shell — not this one — is the process actually running as the
	// interactive desktop user; macOS/Windows/Pi kiosk are still future
	// per-platform cards.
	KeyLaunchOnStartup = "display.launch_on_startup"
	// KeyReportRetentionMode is the per-shop report_archive retention
	// destination (ADR-0040): "till" | "cloud" | "both". Till-writable
	// locally until card 4 lands, at which point write ownership moves to
	// the cloud sync response and this key becomes a read-only replica.
	// Empty/unset means "till" (the only mode this card actually implements).
	KeyReportRetentionMode = "store.report_retention_mode"
	// KeyRestorePromptStatus tracks the setup wizard's "restore from
	// another POS?" step (ut-docs#617): empty/unset means the operator
	// answered No, or never deferred; RestorePromptStatusDeferred means
	// they picked "Later" and Settings → Data should offer a resume link
	// straight into /import until they either use it or dismiss it.
	KeyRestorePromptStatus = "setup.restore_prompt_status"
	// KeyCurrencyConfirmed marks that an operator has explicitly chosen the
	// till's currency at least once — via the setup wizard, Settings, or the
	// import currency-confirmation prompt (ut-docs#970) — as opposed to it
	// still sitting on the compiled-in default (GBP) nobody ever touched.
	// Deliberately separate from KeyCurrency itself: SaveState always writes
	// KeyCurrency (even when nothing changed it from the default), so its
	// mere presence can't distinguish "operator chose this" from "nobody
	// has ever looked." A catalogue import prices its rows in whatever
	// currency ActiveCurrency() reports, so importing into a till whose
	// currency was never confirmed can silently relabel another currency's
	// prices as the default — this key is what import_page.go gates on
	// before committing.
	KeyCurrencyConfirmed = "store.currency_confirmed"
	// KeyPendingBasePlugins holds the JSON list of still-pending country
	// base-plugin auto-installs (ut-docs#591): populated by the setup wizard
	// the instant a country with a setupBasePlugins entry is confirmed,
	// before any network attempt, and drained entry-by-entry as each
	// resolves+installs (wizard's own best-effort attempt, then the
	// background retry). Empty/unset means nothing pending. A merchant can
	// also drop an entry from Settings without installing it — this is a
	// helpful default, not a lock-in.
	KeyPendingBasePlugins = "setup.pending_base_plugins"
	// KeyTSEProvisioningState holds the JSON lifecycle state of the German
	// TSE reseller-provisioning flow (ADR-0053, ut-docs#802): written by the
	// setup wizard the instant a complete business identity is submitted
	// (BEFORE any network attempt, so an offline wizard run survives), moved
	// through pending_kickoff → awaiting_ready by the kickoff call/retry,
	// and cleared on confirmed local receipt of the operational credential.
	// Empty/unset means nothing in flight. Distinct from
	// fiscal.KeyTSEConfigured, which only ever flips true on that confirmed
	// receipt — never optimistically.
	KeyTSEProvisioningState = "fiscal.tse_provisioning_state"
	// KeyPendingFiscalSignRetries used to hold the JSON list of sales
	// queued for background re-signing under fiscal.sign.ask's old
	// proceed-and-declare retry loop (ADR-0044 Decision 1, ut-docs#675).
	// That retry mechanism was removed outright (ADR-0056, ut-docs#839 —
	// TSE vendors do not permit belated signing of a completed
	// transaction), so this key is READ ONLY by the one-time boot
	// migration (pages.dropStaleFiscalSignRetryQueue), which clears any
	// value a pre-1.4.0 build left behind. No live code path ever writes
	// it.
	KeyPendingFiscalSignRetries = "fiscal.pending_sign_retries"
)

// RestorePromptStatusDeferred is the only KeyRestorePromptStatus value the
// wizard/Settings pair actually branches on (ut-docs#617) — any other value
// (including empty) is treated as "nothing to resume."
const RestorePromptStatusDeferred = "deferred"

// ReportRetentionModeTill is the default/fallback report_retention_mode
// value, and the only mode this card (ADR-0040 card 1) actually prunes for
// -- "cloud"/"both" are visible-but-inert until card 4 wires the cloud side.
const ReportRetentionModeTill = "till"

// DefaultIdleLockMinutes locks an unattended till after 10 minutes unless
// configured otherwise (docs: pos-auth.md idle auto-lock).
const DefaultIdleLockMinutes = 10

// DefaultKioskIdleResetSeconds reloads an idle self-order kiosk back to its
// start screen after 60s unless configured otherwise (ADR-0020) — distinct
// from DefaultIdleLockMinutes, which revokes a cashier SESSION; the kiosk
// route is auth-exempt (no session to revoke).
const DefaultKioskIdleResetSeconds = 60

// DefaultWindowMode is the till's window-mode default (ut-docs#608 scaffold)
// until a shop owner picks something else.
const DefaultWindowMode = "normal"

// validWindowModes is the closed enum KeyWindowMode is allowed to hold.
var validWindowModes = map[string]bool{
	"fullscreen": true,
	"kiosk":      true,
	"maximized":  true,
	"normal":     true,
}

// ClampWindowMode returns mode unchanged if it's one of the four valid
// values, else DefaultWindowMode — used when loading (defense against
// corrupt/old stored data), when saving (defense in depth: the HTTP handler
// already rejects a bad value, but SaveState has other callers), and by the
// unauthenticated GET /api/window-mode endpoint (ut-docs#611) the desktop
// shell reads at launch, which needs the same normalization without
// duplicating the valid-mode enum. Exported for that last caller, outside
// this package.
func ClampWindowMode(mode string) string {
	if validWindowModes[mode] {
		return mode
	}
	return DefaultWindowMode
}

// LoadState pulls settings from the DB-backed settings store with cfg defaults.
func LoadState(ctx context.Context, store *settings.Store, cfg *config.Config) RuntimeState {
	get := func(key, def string) string {
		if v, ok, _ := store.Get(ctx, key); ok && strings.TrimSpace(v) != "" {
			return v
		}
		return def
	}

	st := RuntimeState{
		Theme:                  get(KeyTheme, cfg.Theme),
		Currency:               get(KeyCurrency, cfg.Locales.Currency),
		Country:                get(KeyCountry, "GB"),
		Region:                 get(KeyRegion, ""),
		TaxRatePct:             cfg.Locales.TaxRate,
		TaxInclusive:           cfg.Locales.TaxInclusive,
		AllowNegativeInventory: false,
	}

	if v := get(KeyTaxInclusive, strconv.FormatBool(cfg.Locales.TaxInclusive)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			st.TaxInclusive = b
		}
	}
	if v := get(KeyUIScale, ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			st.UIScale = f
		}
	}
	if v := get(KeyTaxRate, strconv.Itoa(cfg.Locales.TaxRate)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.TaxRatePct = n
		}
	}
	if v := get(KeyServiceChargeRate, "0"); v != "" {
		if bp, ok := ParseServiceChargeRateBasisPoints(v); ok {
			st.ServiceChargeRateBasisPoints = bp
		}
	}
	if v := get("pos.allow_negative_inventory", strconv.FormatBool(st.AllowNegativeInventory)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			st.AllowNegativeInventory = b
		}
	}
	st.IdleLockMinutes = DefaultIdleLockMinutes
	if v := get(KeyIdleLock, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			st.IdleLockMinutes = n
		}
	}
	// On-screen keyboard: auto = only on touch screens (pointer: coarse).
	st.OSKMode = get(KeyOSK, "auto")

	st.KioskIdleResetSeconds = DefaultKioskIdleResetSeconds
	if v := get(KeyKioskIdleReset, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			st.KioskIdleResetSeconds = n
		}
	}

	st.WindowMode = ClampWindowMode(get(KeyWindowMode, DefaultWindowMode))

	st.LaunchOnStartup = false
	if v := get(KeyLaunchOnStartup, "false"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			st.LaunchOnStartup = b
		}
	}

	return st
}

// ParseServiceChargeRateBasisPoints parses the decimal-percent string
// persisted under KeyServiceChargeRate (e.g. "12.5") into basis points
// (1250), half-up rounded. A whole-percent string written by a pre-#244
// version ("12") still parses correctly (1200bp) — no migration needed,
// the on-disk key and its string format are unchanged. Returns ok=false
// for a negative, non-finite (NaN/Inf — strconv.ParseFloat accepts
// "NaN"/"Inf"/"Infinity" as valid floats, and float64->int conversion of
// either is undefined behaviour, not a clamped/zero value) or unparsable
// value, so the caller can fall back to a default (LoadState) or reject
// the input outright (the settings-upsert handler).
func ParseServiceChargeRateBasisPoints(v string) (int, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return int(math.Round(f * 100)), true
}

// FormatServiceChargeRatePercent renders basis points back to the
// decimal-percent string ParseServiceChargeRateBasisPoints reads (e.g.
// 1250 -> "12.5", 1200 -> "12" — no spurious ".0").
func FormatServiceChargeRatePercent(bp int) string {
	return strconv.FormatFloat(float64(bp)/100, 'f', -1, 64)
}

// ServiceChargeForbidden reports whether the shop's country bans a
// service-charge/cover line on the bill outright (ut-docs#962 — Turkey,
// since the 2026-01-30 Fiyat Etiketi Yönetmeliği amendment). One predicate,
// so the settings-upsert refusal and the engine-config backstop can never
// disagree about which shops are covered.
//
// The match is deliberately lenient (trimmed, case-insensitive) where
// fiscal.RequiresHardGate's "DE" match is deliberately strict: there, a
// loose match would BLOCK a sale, so a mis-cased code must not widen the
// gate; here, a loose match can only omit a line that is illegal to print,
// so leniency is the fail-closed direction. The setup wizard persists
// uppercase, but /api/settings/upsert, a restored backup and a hand-edited
// settings row can all carry any casing.
func ServiceChargeForbidden(country string) bool {
	return strings.EqualFold(strings.TrimSpace(country), "TR")
}

// EffectiveServiceChargeRateBP is the service-charge rate actually applied
// by an engine — st.ServiceChargeRateBasisPoints, EXCEPT for Turkey, where
// a service-charge/cover line on a bill has been illegal since the
// 2026-01-30 Fiyat Etiketi Yönetmeliği amendment (ut-docs#962): every
// pos.Config{ServiceChargeRateBasisPoints: ...} construction site in
// internal/pages uses this instead of the raw field, so a stale or
// misconfigured nonzero rate can never reach a Turkish till's basket
// preview or tender path — the line becomes structurally unreachable, not
// a rejected sale. Rejecting the whole sale over an illegal-to-charge
// component the till can simply omit would trade a compliance risk for an
// availability one, which offline-first (ADR-0003) rules out. The
// settings-upsert handler (settings_page.go) is the primary defense that
// refuses to let a TR shop save a nonzero rate in the first place; this is
// the fail-closed backstop for whatever reaches here regardless (a rate
// saved before the shop's country was set to TR, a directly-edited DB,
// etc.).
func EffectiveServiceChargeRateBP(st RuntimeState) int {
	if ServiceChargeForbidden(st.Country) {
		return 0
	}
	return st.ServiceChargeRateBasisPoints
}

// SaveState writes every field in one transaction (store.SetMany), so a
// mid-way failure (disk full, SQLITE_BUSY) never leaves a partial mix of
// old and new settings behind — matching Store.SaveRuntimeConfig's guarantee.
func SaveState(ctx context.Context, store *settings.Store, st RuntimeState) error {
	kv := map[string]string{
		KeyTheme:                       st.Theme,
		KeyCurrency:                    st.Currency,
		KeyCountry:                     st.Country,
		KeyRegion:                      st.Region,
		KeyTaxInclusive:                strconv.FormatBool(st.TaxInclusive),
		KeyTaxRate:                     strconv.Itoa(st.TaxRatePct),
		KeyServiceChargeRate:           FormatServiceChargeRatePercent(st.ServiceChargeRateBasisPoints),
		"pos.allow_negative_inventory": strconv.FormatBool(st.AllowNegativeInventory),
		KeyIdleLock:                    strconv.Itoa(st.IdleLockMinutes),
		KeyKioskIdleReset:              strconv.Itoa(st.KioskIdleResetSeconds),
		KeyWindowMode:                  ClampWindowMode(st.WindowMode),
		KeyLaunchOnStartup:             strconv.FormatBool(st.LaunchOnStartup),
	}
	if st.UIScale > 0 {
		kv[KeyUIScale] = strconv.FormatFloat(st.UIScale, 'f', -1, 64)
	}
	if st.OSKMode != "" {
		kv[KeyOSK] = st.OSKMode
	}
	return store.SetMany(ctx, kv)
}

func BuildMenu(base []MenuItem, pm *plugins.Manager) []MenuItem {
	items := append([]MenuItem{}, base...)
	for _, p := range pm.MenuPlugins {
		if p.Route != "" && p.Label != "" {
			items = append(items, MenuItem{Href: p.Route, Label: p.Label})
		}
	}
	return items
}
