package pages

import (
	"encoding/base64"
	"encoding/json"
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
		exportEntries, exportEntriesErr := data.NewPluginRepo(d.Db).ListExportEntries(r.Context())
		if exportEntriesErr != nil {
			// Non-fatal: the settings page still renders without the
			// export section, matching a genuinely-empty install — but a
			// real DB error here shouldn't look identical to that in the
			// logs (ut-docs#189 review).
			logging.L().Errorf("list export entries: %v", exportEntriesErr)
		}
		data := map[string]any{
			"title":             "Settings",
			"theme":             st.Theme,
			"themes":            availableThemes(r.Context(), d),
			"settings":          st,
			"settingsMap":       all,
			"menuItems":         d.Menu,
			"uiScale":           strconv.FormatFloat(scale, 'f', -1, 64),
			"isManager":         isManagerOrAuthOff(r),
			"printer":           printerConfig(r.Context(), d),
			"backups":           listBackupsForUI(d),
			"payMethods":        payMethods,
			"payDefault":        payDefault,
			"payFees":           feeRows,
			"exportEntries":     exportEntries,
			"autoUpdateEnabled": autoUpdateEnabled == "true",
			"autoUpdateTime":    autoUpdateTime,
			"TillName":          tillNameOrDefault(r.Context(), d, locale),
			"IsPrimaryTill":     d.SyncPrimaryURL(r.Context()) == "",
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
		d.Engine.SetConfig(pos.Config{
			TaxInclusive:                 st.TaxInclusive,
			TaxRateBasisPoints:           st.TaxRatePct * 100,
			ServiceChargeRateBasisPoints: st.ServiceChargeRateBasisPoints,
		})
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
			d.Engine.SetConfig(pos.Config{
				TaxInclusive:                 st.TaxInclusive,
				TaxRateBasisPoints:           st.TaxRatePct * 100,
				ServiceChargeRateBasisPoints: st.ServiceChargeRateBasisPoints,
			})
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
