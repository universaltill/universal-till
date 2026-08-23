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
	"slices"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/data/seeddata"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/fiscal"
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

// demoBasketMatch names which live basket demoDataInLiveBasket found a
// demo item/customer in, so the caller can tell the manager specifically
// which basket to clear (ut-docs#746 — a single combined bool left a
// kiosk-basket match reported as a generic "current basket," which the
// manager at the Settings screen (not the kiosk) has no way to act on).
type demoBasketMatch string

const (
	noBasketMatch      demoBasketMatch = ""
	cashierBasketMatch demoBasketMatch = "cashier"
	kioskBasketMatch   demoBasketMatch = "kiosk"
)

// demoDataInLiveBasket reports which currently in-progress basket — cashier
// and/or self-order kiosk, both passed in — references a demo catalogue
// item/variant or a demo customer, if either does. ut-docs#633: unlike a
// HELD (parked) sale, a live basket has no held_sales row for
// remove_demo.sql/remove_demo_customers_promos.sql's own safety check to
// catch, so "Remove sample data" must guard against it directly here —
// otherwise the referenced row disappears and tender later FK-fails with
// no clear way to recover. nil engines (KioskEngine is nil in some test
// harnesses, per ADR-0020) are skipped, not treated as a match. Checked in
// a fixed order (cashier before kiosk) so the result is deterministic even
// if both baskets happen to match at once.
//
// ut-docs#746: deliberately over-blocks — this only checks list
// membership, not the shared seeddata removal scripts' own full
// "untouched" predicates: remove_demo.sql's item rule (PRISTINE
// sku/name/base_price, no sale_lines/stock_movements/held_sales
// reference, live or archived) and remove_demo_customers_promos.sql's
// customer rule (no sales/held_sales reference or promotion targeting,
// live or archived — a differently-shaped check, see that script's own
// header). Reimplementing either predicate here would duplicate a
// safety-critical rule in a second language, and the two copies drifting
// apart is a worse failure mode than an occasional over-cautious block —
// so a demo item/customer merely sitting in a basket blocks removal even
// when the SQL script itself would have kept it anyway.
//
// ut-docs#745: deliberately does NOT check an applied demo promo code
// (PROMO50/PROMO500/DISC10), unlike the item/variant lines and the
// customer above. This is safe today, not an oversight: sale_discounts
// (see 001_init.sql) records only the resulting discount amount for a
// redeemed promo, not which code produced it, so removing a demo
// promotion mid-basket can't leave a dangling reference the way an
// item/customer FK could — there's nothing in this basket for the removed
// row to break. remove_demo_customers_promos.sql's own header spells out
// the same gap for its "untouched" check. If promo-code redemption ever
// becomes durable (there's no promotions management UI yet, so it hasn't
// needed to), this stops being true silently — a change there should
// revisit this function too.
func demoDataInLiveBasket(cashier, kiosk *pos.Service) demoBasketMatch {
	baskets := []struct {
		kind demoBasketMatch
		e    *pos.Service
	}{
		{cashierBasketMatch, cashier},
		{kioskBasketMatch, kiosk},
	}
	for _, b := range baskets {
		if b.e == nil {
			continue
		}
		if slices.Contains(seeddata.DemoCustomerIDs, b.e.CustomerID()) {
			return b.kind
		}
		for _, l := range b.e.Lines() {
			if slices.Contains(seeddata.ItemIDs, l.ItemID) || slices.Contains(seeddata.VariantIDs, l.VariantID) {
				return b.kind
			}
		}
	}
	return noBasketMatch
}

// settingsAudit writes the audit entry for one settings mutation wired to
// checkOrElevate (ut-docs#796, mechanism ADR-0052/ut-docs#557): plain
// InsertAudit attributed to the session user on the allowed path,
// InsertAuditElevated (dual attribution — approver acted, session user was
// blocked) once an approver's PIN got the request past the gate.
// Best-effort like backup_api.go's precedent — the settings write itself
// already succeeded by the time this runs, so a failed audit insert doesn't
// fail the request — but logged rather than silently swallowed (same
// posture as the ADR-0048 fiscal-toggle audit in the upsert handler).
func settingsAudit(r *http.Request, repo *data.POSRepo, elev elevationCheck, entityType, entityID, action string, payload map[string]any) {
	now := time.Now().UTC().Format(time.RFC3339)
	var err error
	if elev.Outcome == elevated {
		err = repo.InsertAuditElevated(r.Context(), nil, elev.ApproverID, elev.ActorID, entityType, entityID, action, payload, now, "")
	} else {
		err = repo.InsertAudit(r.Context(), nil, elev.ActorID, entityType, entityID, action, payload, now, "")
	}
	if err != nil {
		logging.L().Errorf("settings audit: %s %s/%s failed: %v", action, entityType, entityID, err)
	}
}

// settingsRespondSaved answers success on an elevation-wired handler whose
// plain-session contract is a bare 204: unchanged (204) for an ordinarily
// authorized session, but a real confirmation body once elevation was
// involved — the elevation dialog's retry lands its response in an
// hx-target, and a 204 never swaps under htmx, so the approver would get no
// feedback at all (same reasoning as eod_api.go's report-retention handler,
// ut-docs#794 review finding). X-UT-Response: ok (ut-docs#796 review
// finding #4) is what elevation_prompt.html's own retry-form handler
// actually keys its post-close reload off — without it, the dialog closes
// but shop-type/till-name/till-register/save/upsert's own form still shows
// the pre-change value, exactly the staleness those forms' plain
// window.location.reload() exists to prevent on the ordinary 204 path.
func settingsRespondSaved(w http.ResponseWriter, r *http.Request, elev elevationCheck) {
	if elev.Outcome == elevated {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-UT-Response", "ok")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(httpx.ResolveLocale(w, r), "elevation.approved"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func registerSettings(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
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
		// Country base-plugin auto-install (ut-docs#591): whatever the setup
		// wizard's own attempt and the background retry haven't installed
		// yet, so the merchant can see it's happening (or failing) and
		// dismiss it — best-effort like everything else on this page, a
		// read error just renders with nothing pending.
		pendingBasePlugins, _ := loadPendingBasePlugins(r.Context(), d)
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
		// ADR-0042: archived reset batches for the Data card's restore list.
		// Best-effort like the other Data-card queries above — a schema-less
		// test DB just renders the page without the list.
		resetBatchesRaw, resetBatchesErr := data.NewPOSRepo(d.Db).ListResetBatches(r.Context())
		if resetBatchesErr != nil {
			logging.L().Errorf("list reset batches: %v", resetBatchesErr)
		}
		// Same human-friendly date format the backups table already uses
		// (backup_api.go's listBackupsForUI) rather than a raw RFC3339 string.
		// Purgeable/RetainedUntilDisplay (ut-docs#698) let the template show
		// per-row purge eligibility instead of every row offering a
		// Delete-permanently control that a gated batch will just refuse.
		type resetBatchView struct {
			ID                   string
			CreatedAt            string
			SalesCount           int64
			Purgeable            bool
			RetainedUntilDisplay string
		}
		resetBatches := make([]resetBatchView, 0, len(resetBatchesRaw))
		for _, b := range resetBatchesRaw {
			display := b.CreatedAt
			if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
				display = t.Format("2006-01-02 15:04")
			}
			view := resetBatchView{ID: b.ID, CreatedAt: display, SalesCount: b.SalesCount, Purgeable: b.Purgeable}
			if !b.RetainedUntil.IsZero() {
				view.RetainedUntilDisplay = b.RetainedUntil.Format("2006-01-02")
			}
			resetBatches = append(resetBatches, view)
		}
		// This till's register identity for the Tills card's picker
		// (ut-docs#268). Ambiguous is a normal state here — a
		// multi-register shop where nobody has picked yet renders the
		// dropdown unselected — and any other resolution error is
		// best-effort like the other queries above, never a failed page.
		tillRegisterID := ""
		if resolved, resolveErr := pos.ResolveTillRegisterID(r.Context(), d.Db, d.Settings); resolveErr == nil {
			tillRegisterID = resolved
		} else if !errors.Is(resolveErr, pos.ErrRegisterIdentityAmbiguous) {
			logging.L().Errorf("resolve till register: %v", resolveErr)
		}
		// Listed AFTER resolving, so a register the resolver just
		// self-created on an empty shop shows up as an option too.
		registers, registersErr := data.NewPOSRepo(d.Db).ListRegisters(r.Context())
		if registersErr != nil {
			logging.L().Errorf("list registers: %v", registersErr)
		}
		data := map[string]any{
			"title":                 "Settings",
			"theme":                 st.Theme,
			"themes":                availableThemes(r.Context(), d),
			"settings":              st,
			"settingsMap":           all,
			"menuItems":             d.MenuSnapshot(),
			"uiScale":               strconv.FormatFloat(scale, 'f', -1, 64),
			"isManager":             canPerform(d, r, "settings"),
			"printer":               printerConfig(r.Context(), d),
			"backups":               listBackupsForUI(d),
			"payMethods":            payMethods,
			"payDefault":            payDefault,
			"payFees":               feeRows,
			"exportEntries":         exportEntries,
			"autoUpdateEnabled":     autoUpdateEnabled == "true",
			"autoUpdateTime":        autoUpdateTime,
			"TillName":              tillNameOrDefault(r.Context(), d, locale),
			"TillRegisterID":        tillRegisterID,
			"registers":             registers,
			"IsPrimaryTill":         d.SyncPrimaryURL(r.Context()) == "",
			"reportRetentionMode":   reportRetentionMode,
			"reportArchiveCoverage": reportArchiveCoverage,
			"shopType":              shopType,
			"shopTypes":             setupShopTypes,
			"restorePromptDeferred": restorePromptStatus == common.RestorePromptStatusDeferred,
			"pendingBasePlugins":    pendingBasePluginViews(pendingBasePlugins),
			"resetBatches":          resetBatches,
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
		// ParseForm moved ahead of the gate solely so override_pin is
		// readable — it validates nothing, so an already-authorized
		// request is checked exactly as before (ut-docs#796).
		_ = r.ParseForm()
		method := strings.TrimSpace(r.Form.Get("method"))
		// Mutating + audit-writing (ut-docs#796, mechanism ut-docs#557): a
		// denied session gets an in-place PIN re-auth instead of the flat
		// forbidden span.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/payments-default", "#pay-default-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.payments_default"), method),
				[]elevationHiddenField{{Name: "method", Value: method}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), "payments.default_method", method); err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "settings", "payments.default_method", "payments_default_changed",
			map[string]any{"method": method})
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "plugins.settings.saved"))
	})

	// Per-provider fee rules (B4): percent + fixed per transaction, feeding
	// the checkout cost hints. Stored as JSON per method.
	mux.HandleFunc("POST /api/settings/payments-fee", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Validate BEFORE checking elevation (permission_settings_page.go's
		// reviewed convention, ut-docs#557): burning a manager's live PIN
		// entry on a request that was always going to reject is a needless
		// cost.
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
		// Mutating + audit-writing (ut-docs#796): in-place PIN re-auth
		// instead of the flat forbidden span. Numbers pre-formatted in Go
		// so every locale's summary string takes plain %s args.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/payments-fee", "#fee-msg-"+method,
				fmt.Sprintf(httpx.T(locale, "elevation.summary.payments_fee"), method,
					strconv.FormatFloat(pct, 'f', -1, 64), strconv.FormatFloat(fixedMaj, 'f', -1, 64)),
				[]elevationHiddenField{
					{Name: "method", Value: method},
					{Name: "percent", Value: r.Form.Get("percent")},
					{Name: "fixed", Value: r.Form.Get("fixed")},
				}, elev)
			return
		}
		bp := int64(math.Round(pct * 100))
		fixedMinor := int64(math.Round(fixedMaj * 100))
		raw, _ := json.Marshal(map[string]int64{
			"bp":    bp,
			"fixed": fixedMinor,
		})
		if err := d.Settings.Set(r.Context(), "payments.fee."+method, string(raw)); err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "settings", "payments.fee."+method, "payments_fee_changed",
			map[string]any{"method": method, "percent_bp": bp, "fixed_minor": fixedMinor})
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
		// ut-docs#865: same checkOrElevate/InsertAuditElevated mechanism as
		// #796 — a denied session gets an in-place PIN re-auth instead of the
		// flat forbidden span. No other form fields, so ParseForm only reads
		// override_pin.
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/enrol/claim-code", "#claim-code-msg",
				httpx.T(locale, "elevation.summary.enrol_claim_code"), nil, elev)
			return
		}
		info, err := enroll.ClaimCode(r.Context(), d.Cfg)
		if err != nil {
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(err.Error()))
			return
		}
		settingsAudit(r, posRepo, elev, "enrollment", "-", "claim_code_generated", nil)
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
		// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796) in
		// place of the flat forbidden span. No other form fields, so
		// ParseForm only reads override_pin.
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/enrol/now", "#enrol-msg",
				httpx.T(locale, "elevation.summary.enrol_now"), nil, elev)
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
		settingsAudit(r, posRepo, elev, "enrollment", status.StoreID, "enrol_now_registered", map[string]any{"store_id": status.StoreID})
		fmt.Fprintf(w, `<span>✅ %s — <code>%s</code></span>`,
			httpx.T(locale, "settings.enrol.registered"), status.StoreID)
	})

	// The store's fleet: every till registered under this store. Lazy-loaded
	// (a marketplace call) so it never blocks the settings page; failure just
	// shows "unavailable" — offline-first.
	mux.HandleFunc("GET /api/enrol/devices", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !canPerform(d, r, "settings") {
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
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796) — range
	// validation runs BEFORE elevation (established convention: a value that
	// would be rejected anyway must not burn an approver's live PIN entry).
	mux.HandleFunc("POST /api/settings/idle-lock", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		n, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("minutes")))
		if err != nil || n < 0 || n > 480 {
			http.Error(w, "minutes must be between 0 and 480", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			label := httpx.T(locale, "settings.idle_lock.off")
			if n != 0 {
				label = fmt.Sprintf("%d %s", n, httpx.T(locale, "settings.idle_lock.minutes"))
			}
			renderElevationPrompt(w, r, "/api/settings/idle-lock", "#idle-lock-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.idle_lock"), label),
				[]elevationHiddenField{{Name: "minutes", Value: r.Form.Get("minutes")}}, elev)
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
		settingsAudit(r, posRepo, elev, "settings", common.KeyIdleLock, "idle_lock_changed", map[string]any{"minutes": n})
		settingsRespondSaved(w, r, elev)
	})

	// Self-order kiosk idle-reset window (ADR-0020): distinct from the
	// idle-lock above — the kiosk route is auth-exempt (no session to
	// revoke), so this is purely a client-side "reload to the start
	// screen" timer, read at render time. Manager/admin only.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796), same
	// validation-before-elevation ordering as idle-lock above.
	mux.HandleFunc("POST /api/settings/kiosk-idle-reset", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		n, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("seconds")))
		if err != nil || n < 0 || n > 600 {
			http.Error(w, "seconds must be between 0 and 600", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			label := httpx.T(locale, "settings.kiosk_idle_reset.off")
			if n != 0 {
				label = fmt.Sprintf("%d %s", n, httpx.T(locale, "settings.kiosk_idle_reset.seconds"))
			}
			renderElevationPrompt(w, r, "/api/settings/kiosk-idle-reset", "#kiosk-idle-reset-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.kiosk_idle_reset"), label),
				[]elevationHiddenField{{Name: "seconds", Value: r.Form.Get("seconds")}}, elev)
			return
		}
		st := d.CurrentState()
		st.KioskIdleResetSeconds = n
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyKioskIdleReset, "kiosk_idle_reset_changed", map[string]any{"seconds": n})
		settingsRespondSaved(w, r, elev)
	})

	// Window mode (ut-docs#608 scaffold, #883 for the Pi kiosk path): stores
	// the till's window/process display mode AND applies it via WindowCtl.
	// Real OS effect today: the Pi headless kiosk (#883, immediately, no
	// restart) and the Linux desktop shell (#882, immediately too, over the
	// shell's own cross-process control channel — #611's own next-launch
	// apply still covers the case where nothing is listening at all, e.g.
	// an old shell build). Still scaffolding-only on macOS (#609) and
	// Windows (#610): the channel reaches their shells too, but neither has
	// wired a native handler yet, so a live toggle there is accepted
	// (204, same as before this card) and simply has no visible effect
	// until next launch — deliberately NOT surfaced as an error, since that
	// would regress a working persist-only flow into one that looks broken.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796),
	// validation before elevation as elsewhere in this file.
	mux.HandleFunc("POST /api/settings/window-mode", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		switch mode {
		case "fullscreen", "kiosk", "maximized", "normal":
		default:
			http.Error(w, "mode must be one of fullscreen, kiosk, maximized, normal", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/window-mode", "#window-mode-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.window_mode"), httpx.T(locale, "settings.display.window_mode_"+mode)),
				[]elevationHiddenField{{Name: "mode", Value: mode}}, elev)
			return
		}
		// ut-docs#883: apply BEFORE persisting — on the Pi kiosk path this is
		// what actually flips unitill-kiosk.service; if it fails (e.g. a
		// pre-#883 Pi upgraded without the sudoers grant), the operator sees
		// a clear error and the stored preference never lies about what the
		// OS actually did. That guarantee is real for KioskSystemdWindowController
		// (a synchronous systemctl call that genuinely fails or succeeds) but
		// only best-effort for common.HTTPWindowController (ut-docs#882's
		// desktop-shell path): ApplyMode there always returns nil — the native
		// call is dispatched fire-and-forget onto the shell's UI thread with
		// no completion signal — so persistence still proceeds even though
		// "applied" wasn't actually confirmed. WindowCtl is set in pages.Init
		// (NoopWindowController on a plain browser session); nil-checked here
		// so bare-Deps tests/helpers that predate ut-docs#608 stay valid, same
		// convention as the exit-to-os handler below.
		wc := d.WindowCtl
		if wc == nil {
			wc = common.NoopWindowController{}
		}
		if err := wc.ApplyMode(mode); err != nil {
			logging.L().Errorf("window mode apply %s: %v", mode, err)
			http.Error(w, "could not apply window mode", http.StatusInternalServerError)
			return
		}
		st := d.CurrentState()
		st.WindowMode = mode
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyWindowMode, "window_mode_changed", map[string]any{"mode": mode})
		settingsRespondSaved(w, r, elev)
	})

	// Launch-on-startup (ut-docs#608 scaffold): stores/surfaces the till's
	// autostart-on-boot preference. OS-level application (ut-docs#611) is
	// deliberately NOT done here: this handler runs inside unitill-pos, a
	// separate OS process from the desktop shell (unitill-desktop) that
	// owns the actual window/autostart mechanism and — critically — is the
	// process actually running as the interactive desktop user (on a .deb
	// install, unitill-pos runs as the unprivileged system user `pos`,
	// which has no meaningful XDG autostart directory of its own). The
	// shell reads this persisted value itself via GET /api/window-mode and
	// reconciles its own autostart entry at its own next launch — the same
	// next-launch semantics window-mode already uses, and for the same
	// reason (#549 explicitly allows either). See GET /api/window-mode
	// (window_state_api.go) and cmd/unitill-desktop/autostart_linux.go.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). Two
	// separate summary keys (on/off) rather than a %s placeholder — no
	// generic "on"/"off" i18n key exists yet (mirrors eod_settings_enabled/
	// eod_settings_disabled's precedent pair in eod_api.go).
	mux.HandleFunc("POST /api/settings/launch-on-startup", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		b, err := strconv.ParseBool(strings.TrimSpace(r.Form.Get("enabled")))
		if err != nil {
			http.Error(w, "enabled must be a boolean", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.launch_on_startup_off"
			if b {
				summaryKey = "elevation.summary.launch_on_startup_on"
			}
			renderElevationPrompt(w, r, "/api/settings/launch-on-startup", "#launch-on-startup-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "enabled", Value: r.Form.Get("enabled")}}, elev)
			return
		}
		st := d.CurrentState()
		st.LaunchOnStartup = b
		if err := common.SaveState(r.Context(), d.Settings, st); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		d.SetState(st)
		settingsAudit(r, posRepo, elev, "settings", common.KeyLaunchOnStartup, "launch_on_startup_changed", map[string]any{"enabled": b})
		settingsRespondSaved(w, r, elev)
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
	// switched off. The hook has real effect on Linux since ut-docs#882 (was
	// a no-op stub before that), but this means the action can't be
	// exercised under this repo's UT_AUTH=off dev/e2e convention until a
	// real manager PIN is seeded — expected, not a bug. A blank manager_pin
	// is rejected BEFORE calling AuthorizeManager,
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
		approver, err := d.AuthSvc.AuthorizeManager(r.Context(), pin)
		if err != nil {
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
			logging.L().Errorf("exit to OS: %v", err)
			http.Error(w, "could not exit to OS", http.StatusInternalServerError)
			return
		}
		// ut-docs#616: record who authorized breaking kiosk lockdown, now that
		// #611/#882 give this a real effect on Linux (harmless no-op prior to
		// that, but no accountability trail once it actually does something).
		// Only on success — a failed/blank/wrong PIN attempt above already
		// returned before reaching here, mirroring how other manager-gated
		// actions in this codebase don't audit-log the failure itself. Best-
		// effort like settingsAudit's own posture: the OS-exit already
		// happened, so a failed audit insert must not fail this response, but
		// it is logged rather than silently swallowed.
		if err := posRepo.InsertAudit(r.Context(), nil, approver.ID, "settings", "-", "exit_to_os", nil, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			logging.L().Errorf("exit-to-os audit: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Plugin telemetry opt-in (FR-013): off by default, manager-only —
	// gates internal/plugins.TelemetryClient.ReportNow's scheduler tick.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). Two
	// summary keys (on/off), same reasoning as launch-on-startup above.
	mux.HandleFunc("POST /api/settings/telemetry", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		optIn := "false"
		if r.Form.Get("optIn") == "on" || r.Form.Get("optIn") == "1" {
			optIn = "true"
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.telemetry_off"
			if optIn == "true" {
				summaryKey = "elevation.summary.telemetry_on"
			}
			renderElevationPrompt(w, r, "/api/settings/telemetry", "#telemetry-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "optIn", Value: r.Form.Get("optIn")}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), "marketplace.telemetry_opt_in", optIn); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", "marketplace.telemetry_opt_in", "telemetry_opt_in_changed", map[string]any{"opt_in": optIn})
		settingsRespondSaved(w, r, elev)
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
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796), validation
	// before elevation as elsewhere in this file. The display label is
	// resolved BEFORE the "register" -> "" collapse below, since "" has no
	// settings.display.mode_* translation of its own — used for the
	// approver-facing summary ONLY. The audit payload records rawMode (the
	// actual persisted value, matching every sibling handler's convention —
	// e.g. window-mode audits {"mode": mode}, not a localized label) —
	// review finding F2: auditing modeLabel made the trail locale-dependent
	// (the same action wrote a different string per operator UI language)
	// and, for "register", recorded a label for a value that isn't what
	// actually gets persisted (mode collapses to "").
	mux.HandleFunc("POST /api/settings/display-mode", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != "register" && mode != "backoffice" && mode != "self_order" {
			http.Error(w, "mode must be register, backoffice, or self_order", http.StatusBadRequest)
			return
		}
		rawMode := mode
		modeLabel := httpx.T(locale, "settings.display.mode_"+mode)
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/display-mode", "#display-mode-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.display_mode"), modeLabel),
				[]elevationHiddenField{{Name: "mode", Value: mode}}, elev)
			return
		}
		if mode == "register" {
			mode = "" // empty = default register profile
		}
		if err := d.Settings.Set(r.Context(), "display.mode", mode); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", "display.mode", "display_mode_changed", map[string]any{"mode": rawMode})
		settingsRespondSaved(w, r, elev)
	})

	// Shop type (ut-docs#539, taxonomy per ADR-0026) — editable after setup.
	// Manager-only, same gate as the other store-level settings.
	mux.HandleFunc("POST /api/settings/shop-type", func(w http.ResponseWriter, r *http.Request) {
		// Validate BEFORE the elevation gate (ut-docs#557 convention) — a
		// bad shop type 400s without burning an approver's live PIN entry.
		_ = r.ParseForm()
		v := strings.TrimSpace(r.Form.Get("shop_type"))
		if v != "" && !isValidShopType(v) {
			http.Error(w, "unknown shop type", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#796): in-place PIN re-auth
		// instead of the flat 403.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			// Same label the picker itself shows; "—" mirrors the
			// template's own empty (clear) option.
			label := "—"
			if v != "" {
				label = httpx.T(locale, "setup.shop_type."+v)
			}
			renderElevationPrompt(w, r, "/api/settings/shop-type", "#shop-type-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.shop_type"), label),
				[]elevationHiddenField{{Name: "shop_type", Value: v}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyShopType, v); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", common.KeyShopType, "shop_type_changed",
			map[string]any{"shop_type": v})
		settingsRespondSaved(w, r, elev)
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
		// ut-docs#633: a demo item/customer LIVE in the current (not yet
		// held) basket has no held_sales row for remove_demo*.sql's own
		// safety check to catch — block removal here instead of letting a
		// later tender FK-fail with no clear recovery. Checks both baskets
		// (ADR-0020 kiosk isolation: read-only here, so no guard conflict).
		// Read-only rejecting VALIDATION, not authorization — checked
		// BEFORE the elevation gate below (ut-docs#796 review finding #3),
		// same reasoning as payments-fee's range check: an approver
		// shouldn't burn a live PIN entry on a request the live basket was
		// always going to refuse.
		//
		// ut-docs#746: this check and the DELETE below aren't atomic — a
		// cashier could add a demo item to either basket in the gap between
		// this read and the removal running. Accepted un-fixed: single-till,
		// offline, low-value target (worst case is the same FK-fail this
		// guard exists to avoid in the first place, not data loss), and the
		// window is one HTTP request wide.
		if match := demoDataInLiveBasket(d.Engine, d.KioskEngine); match != noBasketMatch {
			key := "settings.data.demo_in_basket_cashier"
			if match == kioskBasketMatch {
				key = "settings.data.demo_in_basket_kiosk"
			}
			fmt.Fprintf(w, `<span class="error">✗ %s</span>`, httpx.T(locale, key))
			return
		}
		// Mutating, IRREVERSIBLE, and audit-writing (ut-docs#796): a denied
		// session gets an in-place PIN re-auth instead of the flat
		// forbidden span. ParseForm only reads override_pin — this handler
		// takes no other input.
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/remove-demo-catalogue", "#demo-remove-msg",
				httpx.T(locale, "elevation.summary.remove_demo_data"), nil, elev)
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
		// The single highest-value audit site in ut-docs#796 slice 1 — an
		// irreversible bulk deletion — so the payload records both the
		// per-category and the combined removed/kept counts the response
		// itself reports.
		settingsAudit(r, posRepo, elev, "demo_data", "-", "demo_data_removed", map[string]any{
			"removed":                  removed,
			"kept":                     kept,
			"removed_items":            removedItems,
			"kept_items":               keptItems,
			"removed_customers_promos": removedCustPromo,
			"kept_customers_promos":    keptCustPromo,
		})
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
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). hxTarget
	// is the dedicated #restore-resume-msg span, NOT #restore-resume-block
	// itself — review finding F1: the block is what the button's own
	// hx-swap="outerHTML" removes, and the dialog's retry form always
	// innerHTML-swaps into hxTarget (elevation_prompt.html's fixed
	// hx-swap); pointing it at a node the denial hint had just replaced
	// left the retry's own hx-target resolving to nothing — htmx bails
	// with htmx:targetError instead of ever sending the approver's PIN.
	// X-UT-Response: ok, set inline below (not via settingsRespondSaved,
	// which this handler doesn't call — it keeps the pre-existing bare
	// empty-200-body success shape), makes the dialog's own script reload
	// the page on the elevated path instead of leaving a stale msg span —
	// same fix #796's review made for till-name/till-register/save (a
	// missing X-UT-Response: ok left stale values after approval).
	mux.HandleFunc("POST /api/settings/dismiss-restore-prompt", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/dismiss-restore-prompt", "#restore-resume-msg",
				httpx.T(locale, "elevation.summary.dismiss_restore_prompt"), nil, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyRestorePromptStatus, ""); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", common.KeyRestorePromptStatus, "restore_prompt_dismissed", nil)
		if elev.Outcome == elevated {
			w.Header().Set("X-UT-Response", "ok")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	})

	// Dismiss one still-pending country base-plugin auto-install
	// (ut-docs#591) without installing it — "a merchant can decline/remove
	// anything auto-installed" for the not-yet-installed case. hx-swap
	// "outerHTML" on the chip itself, same reasoning as the restore-prompt
	// dismiss above: an empty 200 body removes just that chip.
	// ut-docs#865: checkOrElevate/InsertAuditElevated (#557/#796). hxTarget
	// is this chip's own dedicated #pending-plugin-msg-<canonical_type>
	// span (review finding F1 — same "don't point the retry at a node the
	// button's own outerHTML removal can make vanish" reasoning as
	// dismiss-restore-prompt above), NOT the chip-row itself, via a CSS
	// attribute selector rather than "#id" since canonical_type could
	// contain characters a bare #id selector would need escaping for (the
	// shipped catalogue only ever produces "language" today —
	// setup_base_plugins.go). Same X-UT-Response: ok reasoning as
	// dismiss-restore-prompt above.
	// ut-docs#868: canonical_type/locale are now validated against the
	// currently-pending list BEFORE checkOrElevate (this file's own
	// established convention — till-register/payments-fee): previously
	// neither was checked at all, so a mismatched pair broke the retry's
	// `[id="pending-plugin-msg-%s"]` selector (a value containing `"` or
	// `]`) and polluted the audit trail's entity_id with an arbitrary
	// string. Rejecting here also means a request that was always going
	// to be a no-op doesn't burn an approver's live PIN entry.
	mux.HandleFunc("POST /api/settings/dismiss-pending-base-plugin", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		canonicalType := strings.TrimSpace(r.Form.Get("canonical_type"))
		localeVal := strings.TrimSpace(r.Form.Get("locale"))
		pending, err := loadPendingBasePlugins(r.Context(), d)
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		matched := false
		for _, spec := range pending {
			if spec.CanonicalType == canonicalType && spec.Locale == localeVal {
				matched = true
				break
			}
		}
		if !matched {
			http.Error(w, "unknown pending base plugin", http.StatusBadRequest)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/dismiss-pending-base-plugin",
				fmt.Sprintf(`[id="pending-plugin-msg-%s"]`, canonicalType),
				fmt.Sprintf(httpx.T(locale, "elevation.summary.dismiss_pending_base_plugin"), canonicalType),
				[]elevationHiddenField{{Name: "canonical_type", Value: canonicalType}, {Name: "locale", Value: localeVal}}, elev)
			return
		}
		if err := dismissPendingBasePlugin(r.Context(), d, canonicalType, localeVal); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", canonicalType, "pending_base_plugin_dismissed", map[string]any{"canonical_type": canonicalType, "locale": localeVal})
		if elev.Outcome == elevated {
			w.Header().Set("X-UT-Response", "ok")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	})

	// This till's own display name (ut-docs#396) — distinct from a replica's
	// own sync.till_name — shown in Settings and on the /tills page.
	mux.HandleFunc("POST /api/settings/till-name", func(w http.ResponseWriter, r *http.Request) {
		// No rejecting validation here (the name is only trimmed/
		// truncated), so the gate stays first, exactly as before —
		// ParseForm only moved up to make override_pin readable
		// (ut-docs#796).
		_ = r.ParseForm()
		name := strings.TrimSpace(r.Form.Get("name"))
		if rs := []rune(name); len(rs) > 60 { // mirrors the field's own maxlength="60" server-side
			name = string(rs[:60])
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/till-name", "#till-name-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.till_name"), name),
				[]elevationHiddenField{{Name: "name", Value: name}}, elev)
			return
		}
		if name != "" {
			if err := d.Settings.Set(r.Context(), "till.name", name); err != nil {
				http.Error(w, "could not save", http.StatusInternalServerError)
				return
			}
			settingsAudit(r, posRepo, elev, "settings", "till.name", "till_name_changed",
				map[string]any{"name": name})
		}
		settingsRespondSaved(w, r, elev)
	})

	// This till's own register identity (ut-docs#268) — the register a
	// shift-scoped WRITE (e.g. a Pfandrückgabe payout) resolves against,
	// persisted under till.register_id via pos.ResolveTillRegisterID's
	// settings key. An id that isn't an active register is rejected rather
	// than persisted: garbage here would silently misroute payouts later.
	mux.HandleFunc("POST /api/settings/till-register", func(w http.ResponseWriter, r *http.Request) {
		// Required-field + real-register validation BEFORE the elevation
		// gate (ut-docs#557 convention): a register id that would be
		// rejected anyway must not burn an approver's live PIN entry.
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("register_id"))
		if id == "" {
			http.Error(w, "register_id required", http.StatusBadRequest)
			return
		}
		regs, err := posRepo.ListRegisters(r.Context())
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		regName := ""
		valid := false
		for _, reg := range regs {
			if reg.ID == id {
				valid = true
				regName = reg.Name
				break
			}
		}
		if !valid {
			http.Error(w, "unknown register", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#796): a wrong register here
		// silently misroutes payouts later, so the approver sees the
		// register's display name in the summary, not just its id.
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/till-register", "#till-register-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.till_register"), regName),
				[]elevationHiddenField{{Name: "register_id", Value: id}}, elev)
			return
		}
		if err := d.Settings.Set(r.Context(), pos.SettingsKeyTillRegisterID, id); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", pos.SettingsKeyTillRegisterID, "till_register_changed",
			map[string]any{"register_id": id, "register_name": regName})
		settingsRespondSaved(w, r, elev)
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
		// No rejecting validation on this handler (empty fields are simply
		// skipped, a bad taxRatePct is ignored), so the gate stays first —
		// ParseForm only moved up to make override_pin readable
		// (ut-docs#796).
		_ = r.ParseForm()
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/save", "#settings-save-msg",
				httpx.T(locale, "elevation.summary.store_save"),
				[]elevationHiddenField{
					{Name: "currency", Value: r.Form.Get("currency")},
					{Name: "country", Value: r.Form.Get("country")},
					{Name: "region", Value: r.Form.Get("region")},
					{Name: "taxRatePct", Value: r.Form.Get("taxRatePct")},
				}, elev)
			return
		}
		auditPayload := map[string]any{}
		st := d.CurrentState()
		if v := strings.TrimSpace(r.Form.Get("currency")); v != "" {
			st.Currency = v
			auditPayload["currency"] = v
		}
		if v := strings.TrimSpace(r.Form.Get("country")); v != "" {
			st.Country = v
			auditPayload["country"] = v
		}
		if v := strings.TrimSpace(r.Form.Get("region")); v != "" {
			st.Region = v
			auditPayload["region"] = v
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
				auditPayload["tax_rate_pct"] = n
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
		settingsAudit(r, posRepo, elev, "settings", "-", "store_settings_saved", auditPayload)
		settingsRespondSaved(w, r, elev)
	})

	// generic key/value upsert
	mux.HandleFunc("POST /api/settings/upsert", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		value := strings.TrimSpace(r.Form.Get("value"))
		// Key-independent validation BEFORE the elevation gate (ut-docs#557
		// convention — a request that was always going to 400 must not burn
		// an approver's live PIN entry). The fiscal.* key gates below are
		// deliberately NOT moved: they are ADR-0048's own separate
		// authorization layer, out of scope for ut-docs#796.
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
		// ADR-0048 rejecting VALIDATION (not authorization) — applies
		// regardless of who's asking, so hoisted above the elevation gate
		// below (ut-docs#796 review finding #2): without this, a manager
		// posting one of these two always-400 cases got walked through a
		// live PIN entry for a request that was always going to fail,
		// exactly the cost the key/service-charge checks above already
		// avoid. The actual AUTHORIZATION checks (canPerform on
		// "fiscal_tse_override") stay below, after the elevation gate,
		// unchanged — they depend on the SESSION user, not on whether
		// "settings" got elevated (see the comment there).
		switch key {
		case fiscal.KeyOverrideUntil, fiscal.KeyOverrideReason, fiscal.KeyOverrideActor:
			// Fabricating a non-empty override here is refused for
			// everyone — real validation, not a role check. Clearing
			// (empty value) is allowed past this point; its actual
			// authorization (owner-only) is the canPerform check below.
			if value != "" {
				http.Error(w, "fiscal override state is managed via POST /api/fiscal/tse-override", http.StatusBadRequest)
				return
			}
		case fiscal.KeyTSEFailingSince:
			// ADR-0048 Decision 1: "Not operator-settable in this card" —
			// no UI control ships for this key at all, set or clear, by
			// design (a fake "mark as failing"/"mark as fixed" toggle would
			// let an operator manufacture or erase the very state the
			// override exists to gate). Written only by tests directly, or
			// by a future real fiscal.sign.ask failure callback (#675) —
			// never through this generic editor, for anyone. Always 400,
			// for anyone — no role can ever make this key settable here.
			http.Error(w, "fiscal.tse_failing_since is not settable via this endpoint", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#796): this replaces ONLY the
		// old flat `if !canPerform(d, r, "settings") { 403 }` outer gate —
		// the interior fiscal_tse_override gates below still check the
		// SESSION user, exactly as before (ADR-0048); an elevated
		// "settings" approval never grants "fiscal_tse_override".
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/settings/upsert", "#settings-upsert-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.setting_upsert"), key, value),
				[]elevationHiddenField{
					{Name: "key", Value: key},
					{Name: "value", Value: value},
				}, elev)
			return
		}
		// ADR-0048: this generic editor must not be a side door around the
		// fiscal gate. The override window/metadata is written only by
		// POST /api/fiscal/tse-override (typed acknowledgement, duration
		// cap, audit) — fabricating one here is refused for everyone
		// (validated above); clearing (empty value = revoking an override
		// early) stays possible for an owner, checked here. The fiscal.*
		// flags themselves are owner(admin)-only, not manager-level like
		// the rest of this page — this authorization check reads the
		// SESSION user (canPerform, not the elevation's approver), so an
		// elevated "settings" approval alone can never satisfy it.
		switch key {
		case fiscal.KeyOverrideUntil, fiscal.KeyOverrideReason, fiscal.KeyOverrideActor:
			if !canPerform(d, r, "fiscal_tse_override") {
				http.Error(w, "owner (admin) required", http.StatusForbidden)
				return
			}
		case fiscal.KeySystemOfRecord, fiscal.KeyTSEConfigured:
			if !canPerform(d, r, "fiscal_tse_override") {
				http.Error(w, "owner (admin) required", http.StatusForbidden)
				return
			}
		}
		// Read the prior value first so the fiscal-toggle audit below can
		// record the actual transition, not just the new value.
		fiscalToggleAction := ""
		switch key {
		case fiscal.KeySystemOfRecord:
			fiscalToggleAction = "system_of_record_changed"
		case fiscal.KeyTSEConfigured:
			fiscalToggleAction = "tse_configured_changed"
		}
		prevFiscalValue := ""
		if fiscalToggleAction != "" {
			prevFiscalValue, _, _ = d.Settings.Get(r.Context(), key)
		}
		if err := d.Settings.Set(r.Context(), key, value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Every write to the fiscal posture toggles is itself audit-logged
		// (ADR-0048 Decision 1): the shop's own trail honestly records when
		// it declared itself in- or out-of-scope. The setting write itself
		// already succeeded by this point, so a failed audit insert doesn't
		// fail the request (the toggle did take effect) — but it must not
		// be silently swallowed either, since a lost audit entry here is
		// exactly the gap ADR-0048 added this logging to close.
		if fiscalToggleAction != "" && prevFiscalValue != value {
			if auditErr := data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, getSessionUserID(r),
				"fiscal_settings", key, fiscalToggleAction,
				map[string]any{"actor": getSessionUserID(r), "from": prevFiscalValue, "to": value},
				time.Now().UTC().Format(time.RFC3339), ""); auditErr != nil {
				logging.L().Errorf("fiscal settings: audit log for %s (%s -> %s) failed: %v", key, prevFiscalValue, value, auditErr)
			}
		}
		// General upsert audit (ut-docs#796), mutually exclusive BY KEY with
		// the ADR-0048 fiscal-toggle audit above: for the two fiscal posture
		// toggles that block is the authoritative record (it captures the
		// actual from→to transition, and skips no-op re-saves on purpose) —
		// double-logging the same single write as a generic
		// "setting_upserted" row too would make the trail ambiguous (two
		// rows, one write). Every other key gets the generic entry here,
		// including an early override-clear (fiscal.override_* set to ""),
		// which previously left no trace at all.
		if fiscalToggleAction == "" {
			settingsAudit(r, posRepo, elev, "settings", key, "setting_upserted",
				map[string]any{"key": key, "value": value})
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
		settingsRespondSaved(w, r, elev)
	})
}
