package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// fiscalDevicePluginActive reports whether the Turkish fiscal-device plugin
// is installed and enabled — the tile gate for /fiscal-device on the menu,
// the same is_active check fiscalRegisterPluginActive applies for Germany.
func fiscalDevicePluginActive(ctx context.Context, d *common.Deps) bool {
	active, err := data.NewPluginRepo(d.Db).PluginActive(ctx, fiscal.PluginIDTaxTR)
	if err != nil {
		return false
	}
	return active
}

// fiscalDeviceMarketActive reports whether THIS shop is one the Turkish
// fiscal-device flow may act on: Türkiye, with the tax-tr plugin installed
// and active. It is the same pair menu_page.go already gates the tile on.
//
// ut-docs#1750 (independent review, finding 2). This is a security gate, not
// a tidiness one. Between ADR-0081 and ADR-0083 the confirm/unpair endpoints
// below wrote the SAME key fiscal.EvaluateGate read for GERMANY — and
// registerFiscalDeviceTR is registered on every till regardless of country.
// Without this check one manager POST to /api/fiscal-device/confirm lifted a
// German till out of BlockedNeverConfigured, the one state ADR-0048 Decision
// 2.2 says has no override path, and every sale after it completed unsigned.
// The German route to that same flag costs a real TSE credential written to
// the credential store and read back off disk (setup_tse.go); a button must
// not be a shortcut around it. The mirror matters too: unpair sets the key
// false, so on a German till it was a one-click way to hard-block checkout.
//
// ADR-0083 (ut-docs#1767) since made the posture rows per country, so the
// endpoints below now write Turkey's own row (resolved against the current
// store.country, which this guard has just proven is TR) and could not reach
// Germany's even without it. The guard stays: it is still what keeps a
// non-Turkish shop from declaring an ÖKC posture it does not have.
//
// The GET page itself stays reachable on any country on purpose — the
// docs-shots screenshot harness renders it, the same constraint
// fiscalRegisterPluginActive documents for the German page. Only the two
// endpoints that MUTATE the gate flag are gated.
func fiscalDeviceMarketActive(ctx context.Context, d *common.Deps) bool {
	// Normalized, like every other country predicate here
	// (common.ServiceChargeForbidden, setup_base_plugins.go,
	// tax_rate_switcher_banner.go): POST /api/settings/upsert stores
	// store.country as unvalidated free text and only the setup wizard
	// uppercases it, so a shop carrying "tr" is a real state — and an
	// exact-match gate would leave a genuine Turkish till with buttons that
	// refuse (independent review F3).
	return strings.EqualFold(strings.TrimSpace(d.CurrentState().Country), "TR") && fiscalDevicePluginActive(ctx, d)
}

// fiscalDeviceSetting reads one of the plugin's own settings (plugin_settings,
// the same rows the plugin reads through settings_get) for display. Values
// are stored as JSON; a JSON string is unwrapped exactly as the host
// function does. "" when unset or unreadable.
func fiscalDeviceSetting(ctx context.Context, d *common.Deps, key string) string {
	val, found, err := data.NewPluginRepo(d.Db).GetPluginSetting(ctx, fiscal.PluginIDTaxTR, key)
	if err != nil || !found {
		return ""
	}
	return strings.Trim(strings.TrimSpace(val), `"`)
}

// registerFiscalDeviceTR wires the Türkiye fiscal-device status page
// (docs/arch/turkey-launch-playbook.md E3): which plugin drives the shop's
// YN ÖKC, where the device is, whether it has proven it prints (the flag
// ADR-0048's TR hard gate reads), the last receipt it issued and today's
// count. Manager/admin only, structural mirror of registerFiscalRegisterDE.
// The page never talks to the device itself — the plugin does, at tender;
// this page reads the till's own records of what the device answered.
func registerFiscalDeviceTR(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	audit := func(r *http.Request, actorID, action string, payload map[string]any) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "fiscal_device", "till", action, payload, now, "")
	}

	render := func(w http.ResponseWriter, r *http.Request, msgKey string) {
		ctx := r.Context()
		configured, systemOfRecord := false, false
		if d.Settings != nil {
			// Turkey's own posture row (ADR-0083): this page is only ever
			// about the ÖKC, so it must show the ÖKC's state, never a
			// German TSE's.
			if v, _, err := d.Settings.Get(ctx, fiscal.SigningDeviceConfiguredKey(d.CurrentState().Country)); err == nil {
				configured = settingIsTrue(v)
			}
			if v, _, err := d.Settings.Get(ctx, fiscal.KeySystemOfRecord); err == nil {
				systemOfRecord = settingIsTrue(v)
			}
		}
		latest, _, err := posRepo.LatestFiscalDeviceReceipt(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", err)
			return
		}
		// Today's count, on the same business-day boundary reports/EOD use
		// (fiscal_api.go's chip does the identical windowing).
		countToday := 0
		if d.Settings != nil {
			bizDayStart, _, _ := d.Settings.Get(ctx, keyReportsBusinessDayStart)
			hh, mm := parseBusinessDayStart(bizDayStart)
			anchor := businessDateFor(reportNow(), hh, mm)
			from := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), hh, mm, 0, 0, anchor.Location())
			if n, err := posRepo.CountFiscalDeviceReceiptsSince(ctx, from); err == nil {
				countToday = n
			}
		}
		fiscalDeviceData := map[string]any{
			"title":        "Fiscal device",
			"theme":        d.CurrentState().Theme,
			"menuItems":    d.MenuSnapshot(),
			"pluginID":     fiscal.PluginIDTaxTR,
			"pluginActive": fiscalDevicePluginActive(ctx, d),
			// ut-docs#1750 (review F2): the actions below POST to endpoints
			// that are now gated on this same predicate, so rendering them
			// when it is false ships a button that 404s — and the shipped
			// manual screenshot tells a shop to click it. The GET page
			// itself deliberately stays reachable on any country (the
			// docs-shots harness renders it); only the actions are hidden.
			"marketActive":   fiscalDeviceMarketActive(ctx, d),
			"driver":         fiscalDeviceSetting(ctx, d, "okc.driver"),
			"host":           fiscalDeviceSetting(ctx, d, "okc.host"),
			"port":           fiscalDeviceSetting(ctx, d, "okc.port"),
			"maker":          fiscalDeviceSetting(ctx, d, "okc.maker"),
			"configured":     configured,
			"systemOfRecord": systemOfRecord,
			"latest":         latest,
			"countToday":     countToday,
			"msgKey":         msgKey,
		}
		// ut-docs#2116: /fiscal-device is one of the /admin tree's six
		// destinations -- an htmx request from that panel (NOT a stale
		// history restore, see httpx.IsFragmentSwap) gets just the
		// "content" block plus an out-of-band refresh of the tree so its
		// is-current highlight follows the click; a plain browser GET
		// (deep link, or the redirect a mutation falls back to) still gets
		// the exact same full standalone page as before this card.
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderContentFragment("ui/pages/fiscal_device.html", fiscalDeviceData)(w, r)
			writeAdminTreeOOB(w, r, httpx.FuncsFor(httpx.RequestLocale(r)), "/fiscal-device", adminGroupsFor(visibleAdminEntries(d, r)))
			return
		}
		httpx.Render("ui/pages/fiscal_device.html", fiscalDeviceData)(w, r)
	}

	mux.HandleFunc("GET /fiscal-device", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		render(w, r, r.URL.Query().Get("msg"))
	})

	// Manual confirm: a manager who has paired the device and watched it
	// print a test receipt can mark it confirmed without waiting for the
	// first real sale (the first real receipt does the same automatically,
	// fiscal_device_hook.go). Audited either way.
	mux.HandleFunc("POST /api/fiscal-device/confirm", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		// ut-docs#1750 (review F1): this writes the same fiscal posture flag
		// that /api/settings/upsert guards behind the owner-only
		// "fiscal_tse_override" permission. A button must not be a weaker
		// path to the same state than the settings editor is — defence in
		// depth beside the country-change clearing in settings_page.go.
		// The ordinary path is unaffected: the first real receipt still
		// auto-confirms (fiscal_device_hook.go), and that needs no manager
		// at all. This is the manual fallback, and declaring fiscal posture
		// by hand is an owner's act.
		if !canPerform(d, r, "fiscal_tse_override") {
			httpx.RenderError(w, r, http.StatusForbidden, "fiscaldevice.error.owner_required", nil)
			return
		}
		// ut-docs#1750: not this shop's flow. 404 rather than 403 because
		// the endpoint genuinely does not exist for this till — not as
		// concealment: the permission checks above already answer 403 on
		// this same URL, so nothing is hidden by the status code (review F4
		// corrected an earlier comment here that claimed otherwise).
		if !fiscalDeviceMarketActive(r.Context(), d) {
			httpx.RenderError(w, r, http.StatusNotFound, "fiscaldevice.error.not_found", nil)
			return
		}
		if d.Settings == nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", nil)
			return
		}
		if err := d.Settings.Set(r.Context(), fiscal.SigningDeviceConfiguredKey(d.CurrentState().Country), "true"); err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", err)
			return
		}
		audit(r, actor.ID, fiscalDeviceAuditConfirmed, map[string]any{"source": "manual"})
		http.Redirect(w, r, "/fiscal-device?msg=fiscaldevice.msg.confirmed", http.StatusSeeOther)
	})

	// Unpair: the device is gone (returned to the bank, replaced) — the
	// next TR sale as system of record is refused again until a device
	// proves itself (ADR-0048 Decision 2.2, no override on this branch).
	mux.HandleFunc("POST /api/fiscal-device/unpair", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		// ut-docs#1750 (review F1): this writes the same fiscal posture flag
		// that /api/settings/upsert guards behind the owner-only
		// "fiscal_tse_override" permission. A button must not be a weaker
		// path to the same state than the settings editor is — defence in
		// depth beside the country-change clearing in settings_page.go.
		// The ordinary path is unaffected: the first real receipt still
		// auto-confirms (fiscal_device_hook.go), and that needs no manager
		// at all. This is the manual fallback, and declaring fiscal posture
		// by hand is an owner's act.
		if !canPerform(d, r, "fiscal_tse_override") {
			httpx.RenderError(w, r, http.StatusForbidden, "fiscaldevice.error.owner_required", nil)
			return
		}
		// ut-docs#1750: same gate as confirm above, and it matters just as
		// much in this direction — this sets the flag FALSE, so ungated it
		// let a manager on a German till hard-block the shop's checkout.
		if !fiscalDeviceMarketActive(r.Context(), d) {
			httpx.RenderError(w, r, http.StatusNotFound, "fiscaldevice.error.not_found", nil)
			return
		}
		if d.Settings == nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", nil)
			return
		}
		if err := d.Settings.Set(r.Context(), fiscal.SigningDeviceConfiguredKey(d.CurrentState().Country), "false"); err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", err)
			return
		}
		audit(r, actor.ID, fiscalDeviceAuditUnpaired, nil)
		http.Redirect(w, r, "/fiscal-device?msg=fiscaldevice.msg.unpaired", http.StatusSeeOther)
	})
}
