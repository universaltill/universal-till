package pages

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// shortDeviceID trims a long "till-<uuid>" id to a readable prefix for display.
func shortDeviceID(id string) string {
	if len(id) > 16 {
		return id[:16] + "…"
	}
	return id
}

// isManagerOrAuthOff gates manager-only settings; with UT_AUTH=off there is
// no session to check, so dev/CI tooling passes.
func isManagerOrAuthOff(r *http.Request) bool {
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		return true
	}
	u, ok := auth.FromContext(r.Context())
	return ok && u.IsManager()
}

func registerSettings(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		all, _ := d.Settings.All(r.Context())
		st := d.CurrentState()
		scale := st.UIScale
		if scale <= 0 {
			scale = 1
		}
		payMethods, _ := data.NewPOSRepo(d.Db).ListActivePaymentMethods(r.Context())
		payDefault, _, _ := d.Settings.Get(r.Context(), "payments.default_method")
		type feeRow struct {
			ID, Name   string
			PercentMaj string
			FixedMaj   string
		}
		feeRows := make([]feeRow, 0, len(payMethods))
		for _, m := range payMethods {
			fr := feeRow{ID: m.ID, Name: m.Name}
			if raw, ok, _ := d.Settings.Get(r.Context(), "payments.fee."+m.ID); ok && raw != "" {
				var f struct {
					BP    int64 `json:"bp"`
					Fixed int64 `json:"fixed"`
				}
				if json.Unmarshal([]byte(raw), &f) == nil {
					if f.BP > 0 {
						fr.PercentMaj = fmt.Sprintf("%.2f", float64(f.BP)/100)
					}
					if f.Fixed > 0 {
						fr.FixedMaj = fmt.Sprintf("%.2f", float64(f.Fixed)/100)
					}
				}
			}
			feeRows = append(feeRows, fr)
		}
		autoUpdateEnabled, _, _ := d.Settings.Get(r.Context(), keyAutoUpdateEnabled)
		autoUpdateTime, _, _ := d.Settings.Get(r.Context(), keyAutoUpdateTime)
		// Sample-data note (ut-docs#539, extended to customers/promos by
		// ut-docs#567): best-effort — a schema-less test DB or a query
		// error just renders the page without the note, same posture as
		// payMethods above. Combined across catalogue items + customers +
		// promo codes, since "Remove sample data" now clears all three
		// together and the note should describe what the button actually
		// does.
		demoSeedRepo := data.NewDemoSeedRepo(d.Db)
		sampleItemCount, _ := demoSeedRepo.SampleItemCount(r.Context())
		sampleCustomerPromoCount, _ := demoSeedRepo.SampleCustomerPromoCount(r.Context())
		sampleCount := sampleItemCount + sampleCustomerPromoCount
		shopType, _, _ := d.Settings.Get(r.Context(), common.KeyShopType)
		// Restore-from-another-POS resume prompt (ut-docs#617): only shown
		// when the wizard's "Later" choice left it deferred; best-effort
		// like shopType above, same posture.
		restorePromptStatus, _, _ := d.Settings.Get(r.Context(), common.KeyRestorePromptStatus)
		exportEntries, exportEntriesErr := data.NewPluginRepo(d.Db).ListExportEntries(r.Context())
		if exportEntriesErr != nil {
			// Non-fatal: the settings page still renders without the
			// export section, matching a genuinely-empty install — but a
			// real DB error here shouldn't look identical to that in the
			// logs (ut-docs#189 review).
			logging.L().Errorf("list export entries: %v", exportEntriesErr)
		}
		// ADR-0040 (ut-docs#571 card 1): the current retention mode
		// (defaulting to "till", same fallback the prune step itself uses)
		// and how far back the archive goes, for the new Report Retention
		// card. Non-fatal on error, same reasoning as exportEntries above.
		reportRetentionMode, _, _ := d.Settings.Get(r.Context(), common.KeyReportRetentionMode)
		if reportRetentionMode == "" {
			reportRetentionMode = common.ReportRetentionModeTill
		}
		reportArchiveCoverage, coverageErr := data.NewPOSRepo(d.Db).ReportArchiveCoverage(r.Context())
		if coverageErr != nil {
			logging.L().Errorf("report archive coverage: %v", coverageErr)
		}
		data := map[string]any{
			"title":                 "Settings",
			"theme":                 st.Theme,
			"themes":                availableThemes(r.Context(), d),
			"settings":              st,
			"settingsMap":           all,
			"menuItems":             d.MenuSnapshot(),
			"uiScale":               strconv.FormatFloat(scale, 'f', -1, 64),
			"isManager":             isManagerOrAuthOff(r),
			"printer":               printerConfig(r.Context(), d),
			"backups":               listBackupsForUI(d),
			"payMethods":            payMethods,
			"payDefault":            payDefault,
			"payFees":               feeRows,
			"exportEntries":         exportEntries,
			"autoUpdateEnabled":     autoUpdateEnabled == "true",
			"autoUpdateTime":        autoUpdateTime,
			"TillName":              tillNameOrDefault(r.Context(), d, locale),
			"IsPrimaryTill":         d.SyncPrimaryURL(r.Context()) == "",
			"reportRetentionMode":   reportRetentionMode,
			"reportArchiveCoverage": reportArchiveCoverage,
			"shopType":              shopType,
			"shopTypes":             setupShopTypes,
			"restorePromptDeferred": restorePromptStatus == common.RestorePromptStatusDeferred,
			"sampleCount":           sampleCount,
			"windowMode":            st.WindowMode,
			"launchOnStartup":       st.LaunchOnStartup,
		}
		httpx.Render("ui/pages/settings.html", data)(w, r)
	})

	// Preferred payment method: leads the tender UI (ADR-0016 manual mode —
	// the shop's cheaper/house provider is the one-tap default).
	mux.HandleFunc("POST /api/settings/payments-default", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !isManagerOrAuthOff(r) {
			fmt.Fprintf(w, `<span class="error">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		_ = r.ParseForm()
		method := strings.TrimSpace(r.Form.Get("method"))
		if err := d.Settings.Set(r.Context(), "payments.default_method", method); err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "plugins.settings.saved"))
	})

	// Per-provider fee rules (B4): percent + fixed per transaction, feeding
	// the checkout cost hints. Stored as JSON per method.
	mux.HandleFunc("POST /api/settings/payments-fee", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !isManagerOrAuthOff(r) {
			fmt.Fprintf(w, `<span class="error">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		_ = r.ParseForm()
		method := strings.TrimSpace(r.Form.Get("method"))
		if method == "" {
			fmt.Fprintf(w, `<span class="error">✗ method</span>`)
			return
		}
		pct, _ := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("percent")), 64)
		fixedMaj, _ := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("fixed")), 64)
		if pct < 0 || fixedMaj < 0 || pct > 100 {
			fmt.Fprintf(w, `<span class="error">✗ range</span>`)
			return
		}
		raw, _ := json.Marshal(map[string]int64{
			"bp":    int64(math.Round(pct * 100)),
			"fixed": int64(math.Round(fixedMaj * 100)),
		})
		if err := d.Settings.Set(r.Context(), "payments.fee."+method, string(raw)); err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "plugins.settings.saved"))
	})

	// Immediate marketplace registration attempt (the Settings card's
	// "Register now" button). Registration also runs automatically in the
	// background; this just gives the operator a button and instant feedback.
	// "Claim this store" (ADR-0013 layer 2): mint a short-lived claim code
	// on the marketplace and show it with the redemption link. The owner
	// signs in with their Universal Till ID and enters the code.
	mux.HandleFunc("POST /api/enrol/claim-code", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !isManagerOrAuthOff(r) {
			fmt.Fprintf(w, `<span class="error">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		info, err := enroll.ClaimCode(r.Context(), d.Cfg)
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		// QR of the claim URL: the owner scans it and claims FROM THEIR
		// PHONE — the only escape hatch on shells that can't open an
		// external browser (Pi kiosk, windows/linux webview).
		qrHTML := ""
		if png, err := qrcode.Encode(info.ClaimURL, qrcode.Medium, 180); err == nil {
			qrHTML = fmt.Sprintf(
				`<div class="claim-qr"><img src="data:image/png;base64,%s" alt="" width="180" height="180">`+
					`<div class="muted">%s</div></div>`,
				base64.StdEncoding.EncodeToString(png),
				html.EscapeString(httpx.T(locale, "settings.enrol.claim_scan")))
		}
		fmt.Fprintf(w,
			`<div class="claim-code-box"><div class="claim-code">%s</div>`+
				`<div class="muted">%s</div>`+
				`<a href="%s" target="_blank" rel="noopener">%s</a>%s</div>`,
			html.EscapeString(info.Code),
			html.EscapeString(httpx.T(locale, "settings.enrol.claim_expires")),
			html.EscapeString(info.ClaimURL),
			html.EscapeString(httpx.T(locale, "settings.enrol.claim_open")),
			qrHTML)
	})

	mux.HandleFunc("POST /api/enrol/now", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Always answer 200: this is an hx-swap target, and HTMX silently drops
		// non-2xx responses — a 403/502 here is exactly why the button looked
		// dead. The message carries the outcome (and the reason on failure).
		if !isManagerOrAuthOff(r) {
			fmt.Fprintf(w, `<span class="error">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		status, err := enroll.RegisterNow(r.Context(), d.Cfg, d.Settings)
		if err != nil || !status.Registered {
			// Show the concrete reason (and the endpoint we tried) so the
			// operator can see e.g. an unreachable/misconfigured marketplace.
			reason := httpx.T(locale, "settings.enrol.not_registered")
			if err != nil {
				reason = err.Error()
			}
			endpoint := enroll.Effective(d.Cfg).Marketplace.EndpointURL
			fmt.Fprintf(w, `<span class="error">❌ %s: %s (%s)</span>`,
				httpx.T(locale, "settings.enrol.failed"), reason, endpoint)
			return
		}
		fmt.Fprintf(w, `<span>✅ %s — <code>%s</code></span>`,
			httpx.T(locale, "settings.enrol.registered"), status.StoreID)
	})

	// The store's fleet: every till registered under this store. Lazy-loaded
	// (a marketplace call) so it never blocks the settings page; failure just
	// shows "unavailable" — offline-first.
	mux.HandleFunc("GET /api/enrol/devices", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !isManagerOrAuthOff(r) {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		devices, err := enroll.Fleet(r.Context(), d.Cfg)
		if err != nil {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, httpx.T(locale, "settings.enrol.fleet_unavailable"))
			return
		}
		if len(devices) == 0 {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, httpx.T(locale, "settings.enrol.fleet_empty"))
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, `<p class="muted">%s (%d)</p><ul style="margin:.2rem 0; padding-inline-start:1.1rem">`,
			httpx.T(locale, "settings.enrol.fleet_title"), len(devices))
		for _, dev := range devices {
			name := dev.Name
			if name == "" {
				name = dev.DeviceID
			}
			fmt.Fprintf(&b, `<li>%s <code class="muted">%s</code></li>`,
				html.EscapeString(name), html.EscapeString(shortDeviceID(dev.DeviceID)))
		}
		b.WriteString(`</ul>`)
		_, _ = w.Write([]byte(b.String()))
	})

	// Idle auto-lock window (docs: pos-auth.md). Manager/admin only — an
	// unattended till's security posture is not a cashier decision.
	mux.HandleFunc("POST /api/settings/idle-lock", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		n, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("minutes")))
		if err != nil || n < 0 || n > 480 {
			http.Error(w, "minutes must be between 0 and 480", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.IdleLockMinutes = n
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		d.AuthSvc.SetIdleLockMinutes(n)
		if !auth.Disabled(os.Getenv("UT_AUTH")) {
			httpx.InitIdleLock(n)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Self-order kiosk idle-reset window (ADR-0020): distinct from the
	// idle-lock above — the kiosk route is auth-exempt (no session to
	// revoke), so this is purely a client-side "reload to the start
	// screen" timer, read at render time. Manager/admin only.
	mux.HandleFunc("POST /api/settings/kiosk-idle-reset", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		n, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("seconds")))
		if err != nil || n < 0 || n > 600 {
			http.Error(w, "seconds must be between 0 and 600", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.KioskIdleResetSeconds = n
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		w.WriteHeader(http.StatusNoContent)
	})

	// Window mode (ut-docs#608 scaffold): stores/surfaces the till's
	// window/process display mode. This card does NOT apply it to the OS
	// window — that's #609 (macOS)/#610 (Windows)/#611 (Linux/Pi).
	mux.HandleFunc("POST /api/settings/window-mode", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		switch mode {
		case "fullscreen", "kiosk", "maximized", "normal":
		default:
			http.Error(w, "mode must be one of fullscreen, kiosk, maximized, normal", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.WindowMode = mode
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		w.WriteHeader(http.StatusNoContent)
	})

	// Launch-on-startup (ut-docs#608 scaffold): stores/surfaces the till's
	// autostart-on-boot preference. Not wired to the OS's actual autostart
	// mechanism yet — same future-card split as window-mode above.
	mux.HandleFunc("POST /api/settings/launch-on-startup", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		b, err := strconv.ParseBool(strings.TrimSpace(r.Form.Get("enabled")))
		if err != nil {
			http.Error(w, "enabled must be a boolean", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.LaunchOnStartup = b
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		w.WriteHeader(http.StatusNoContent)
	})

	// Exit to OS window (ut-docs#608 scaffold): a manager-session cookie
	// alone is NOT enough here (per the product owner's #549 comment thread
	// — "need someone with a right role... need pin") — this requires a LIVE
	// PIN, checked the same way as shifts_api.go's cash-adjustment/payout
	// handlers, INCLUDING that its PIN check stays live under UT_AUTH=off —
	// deliberately NOT mirroring those handlers' `auth.Disabled(...)` bypass.
	// The whole point of this endpoint is the live-PIN gate itself (there's
	// no "positive amount, no PIN needed" case here to bypass toward), and
	// the product owner's requirement was for a PIN check that can't be
	// switched off. Cost today is zero (the hook is a no-op stub), but this
	// means the action can't be exercised under this repo's UT_AUTH=off
	// dev/e2e convention until a real manager PIN is seeded — expected, not
	// a bug. A blank manager_pin is rejected BEFORE calling AuthorizeManager,
	// which would otherwise burn a failed-attempt count shared device-wide
	// with keypad login (5 failures = 30s lockout) — the exact blank-PIN
	// lockout burn bug fixed there (see
	// TestExitToOSBlankPINRejectedWithoutBurningLockoutBudget below).
	mux.HandleFunc("POST /api/settings/exit-to-os", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pin := strings.TrimSpace(r.Form.Get("manager_pin"))
		if pin == "" {
			http.Error(w, "manager PIN required", http.StatusForbidden)
			return
		}
		if _, err := d.AuthSvc.AuthorizeManager(r.Context(), pin); err != nil {
			status := http.StatusForbidden
			if errors.Is(err, auth.ErrLockedOut) {
				status = http.StatusTooManyRequests
			}
			http.Error(w, "manager PIN required", status)
			return
		}
		// WindowCtl is set in pages.Init (common.NoopWindowController until
		// #609/#610/#611 wire a real one); nil-checked here so bare-Deps
		// tests/helpers that predate this field stay valid, same convention
		// as Deps.OrderStatus.
		wc := d.WindowCtl
		if wc == nil {
			wc = common.NoopWindowController{}
		}
		if err := wc.ExitToOS(); err != nil {
			http.Error(w, "could not exit to OS", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Plugin telemetry opt-in (FR-013): off by default, manager-only —
	// gates internal/plugins.TelemetryClient.ReportNow's scheduler tick.
	mux.HandleFunc("POST /api/settings/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		optIn := "false"
		if r.Form.Get("optIn") == "on" || r.Form.Get("optIn") == "1" {
			optIn = "true"
		}
		if err := d.Settings.Set(r.Context(), "marketplace.telemetry_opt_in", optIn); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Interface scale for this till's screen; saved and applied immediately.
	mux.HandleFunc("POST /api/settings/ui-scale", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f, err := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("scale")), 64)
		if err != nil || f < 0.5 || f > 2.0 {
			http.Error(w, "scale must be between 0.5 and 2.0", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.UIScale = f
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		httpx.InitUIScale(f)
		w.WriteHeader(http.StatusNoContent)
	})

	// On-screen keyboard mode for this till's screen (auto|on|off).
	mux.HandleFunc("POST /api/settings/osk", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != "auto" && mode != "on" && mode != "off" {
			http.Error(w, "mode must be auto, on or off", http.StatusBadRequest)
			return
		}
		st := d.CurrentState()
		st.OSKMode = mode
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		httpx.InitOSKMode(mode)
		w.WriteHeader(http.StatusNoContent)
	})

	// Device profile (ADR-0018/ADR-0020): register (default), back-office
	// manager station, or self-order kiosk — "/" becomes the reports page
	// (backoffice) or the locked customer-facing self-order flow
	// (self_order). Per-till (display.* never LAN-syncs), so one shop can
	// mix registers, a back-office device, and one or more kiosks.
	mux.HandleFunc("POST /api/settings/display-mode", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != "register" && mode != "backoffice" && mode != "self_order" {
			http.Error(w, "mode must be register, backoffice, or self_order", http.StatusBadRequest)
			return
		}
		if mode == "register" {
			mode = "" // empty = default register profile
		}
		if err := d.Settings.Set(r.Context(), "display.mode", mode); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Shop type (ut-docs#539, taxonomy per ADR-0026) — editable after setup.
	// Manager-only, same gate as the other store-level settings.
	mux.HandleFunc("POST /api/settings/shop-type", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		v := strings.TrimSpace(r.Form.Get("shop_type"))
		if v != "" && !isValidShopType(v) {
			http.Error(w, "unknown shop type", http.StatusBadRequest)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyShopType, v); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Remove all opt-in sample data (ut-docs#539, extended to customers/
	// promo codes by ut-docs#567): the catalogue AND the 3 demo customers
	// AND the 3 demo promo codes together, so the button's copy matches
	// what it actually removes. Only untouched rows go in each category
	// (never sold/stock-adjusted for items; never sold-to or targeted by a
	// promotion for customers; never targeted at a customer for promo
	// codes — see remove_demo_customers_promos.sql's header for why that
	// last rule differs from the other two). The response reports combined
	// removed vs kept. Answers 200 with the outcome in the fragment — it's
	// an hx-swap target, and HTMX drops non-2xx bodies (see the enrol
	// handlers above).
	mux.HandleFunc("POST /api/settings/remove-demo-catalogue", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !isManagerOrAuthOff(r) {
			fmt.Fprintf(w, `<span class="error">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		seedRepo := data.NewDemoSeedRepo(d.Db)
		removedItems, keptItems, err := seedRepo.RemoveDemoCatalogue(r.Context())
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		removedCustPromo, keptCustPromo, err := seedRepo.RemoveDemoCustomersPromos(r.Context())
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		removed := removedItems + removedCustPromo
		kept := keptItems + keptCustPromo
		msg := fmt.Sprintf(httpx.T(locale, "settings.data.demo_removed"), removed)
		if kept > 0 {
			msg += " " + fmt.Sprintf(httpx.T(locale, "settings.data.demo_kept"), kept)
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, html.EscapeString(msg))
	})

	// Dismiss the "restore from another POS?" resume prompt (ut-docs#617)
	// without importing anything — an explicit "no thanks," not just
	// ignoring it forever. hx-swap="outerHTML" on the whole block, so an
	// empty 200 body removes it; 204 wouldn't swap (see remove-demo-
	// catalogue's comment above on why 2xx-with-body is used for hx-swap
	// targets).
	mux.HandleFunc("POST /api/settings/dismiss-restore-prompt", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyRestorePromptStatus, ""); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	})

	// This till's own display name (ut-docs#396) — distinct from a replica's
	// own sync.till_name — shown in Settings and on the /tills page.
	mux.HandleFunc("POST /api/settings/till-name", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		if name := strings.TrimSpace(r.Form.Get("name")); name != "" {
			if rs := []rune(name); len(rs) > 60 { // mirrors the field's own maxlength="60" server-side
				name = string(rs[:60])
			}
			if err := d.Settings.Set(r.Context(), "till.name", name); err != nil {
				http.Error(w, "could not save", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/settings/theme", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.Form.Get("theme")); v != "" {
			st := d.CurrentState()
			st.Theme = v
			if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
				http.Error(w, "could not save", http.StatusInternalServerError)
				return
			}
			d.SetState(st)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/settings/save", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		st := d.CurrentState()
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" {
			st.Currency = v
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			st.Country = v
		}
		if v := strings.TrimSpace(r.Form.Get("region")); v != "" {
			st.Region = v
		}
		// TaxInclusive/AllowNegativeInventory are deliberately NOT set here:
		// the only caller (the currency card) never posts them, and an
		// unconditional write silently zeroed both on every currency change
		// (ut-docs#178). They're settable via /api/settings/upsert instead
		// (store.tax_inclusive / pos.allow_negative_inventory).
		// taxRatePct keeps its guard below though no shipped UI posts it
		// here either — not dead code, exercised by TestDisplayAndStoreSettings.
		if v := r.Form.Get("taxRatePct"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				st.TaxRatePct = n
			}
		}
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		httpx.InitCurrency(st.Currency)
		// In place: replacing the engine would empty a basket in progress.
		// Both engines: the kiosk's separate instance (ut-docs#449) must see
		// the same tax config or it would silently charge stale rates.
		newCfg := pos.Config{
			TaxInclusive:                 st.TaxInclusive,
			TaxRateBasisPoints:           st.TaxRatePct * 100,
			ServiceChargeRateBasisPoints: st.ServiceChargeRateBasisPoints,
		}
		d.Engine.SetConfig(newCfg)
		if d.KioskEngine != nil {
			d.KioskEngine.SetConfig(newCfg)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// generic key/value upsert
	mux.HandleFunc("POST /api/settings/upsert", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		value := strings.TrimSpace(r.Form.Get("value"))
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		// ut-docs#244: validate before persisting, not just before reflecting
		// into RuntimeState — the old code let an unparsable value through to
		// d.Settings.Set unchanged (silently no-op'ing only the in-memory
		// reflection below), so the DB and the live state disagreed with no
		// operator feedback at all.
		if key == common.KeyServiceChargeRate {
			if _, ok := common.ParseServiceChargeRateBasisPoints(value); !ok {
				http.Error(w, "invalid service charge rate", http.StatusBadRequest)
				return
			}
		}
		if err := d.Settings.Set(r.Context(), key, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// reflect into state for known keys
		truthy := func(v string) bool { return strings.ToLower(v) == "true" || v == "1" || v == "on" }
		st := d.UpdateState(func(s *common.RuntimeState) {
			switch key {
			case common.KeyTheme:
				s.Theme = value
			case common.KeyCurrency:
				s.Currency = value
			case common.KeyCountry:
				s.Country = value
			case common.KeyRegion:
				s.Region = value
			case common.KeyTaxInclusive:
				s.TaxInclusive = truthy(value)
			case common.KeyTaxRate:
				if n, err := strconv.Atoi(value); err == nil {
					s.TaxRatePct = n
				}
			case common.KeyServiceChargeRate:
				// Already validated above; guard kept for defensive safety.
				if bp, ok := common.ParseServiceChargeRateBasisPoints(value); ok {
					s.ServiceChargeRateBasisPoints = bp
				}
			case "pos.allow_negative_inventory":
				s.AllowNegativeInventory = truthy(value)
			}
		})
		switch key {
		case common.KeyCurrency:
			httpx.InitCurrency(st.Currency)
		case common.KeyTaxInclusive, common.KeyServiceChargeRate:
			// In place: replacing the engine would empty a basket in progress.
			// Both engines — see the currency-card handler above (ut-docs#449).
			newCfg := pos.Config{
				TaxInclusive:                 st.TaxInclusive,
				TaxRateBasisPoints:           st.TaxRatePct * 100,
				ServiceChargeRateBasisPoints: st.ServiceChargeRateBasisPoints,
			}
			d.Engine.SetConfig(newCfg)
			if d.KioskEngine != nil {
				d.KioskEngine.SetConfig(newCfg)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
