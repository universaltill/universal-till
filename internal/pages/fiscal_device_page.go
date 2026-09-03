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
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required") // page-error:allow mirrors fiscal_register_page.go, tracked in ut-docs#1458
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
			if v, _, err := d.Settings.Get(ctx, fiscal.KeyTSEConfigured); err == nil {
				configured = settingIsTrue(v)
			}
			if v, _, err := d.Settings.Get(ctx, fiscal.KeySystemOfRecord); err == nil {
				systemOfRecord = settingIsTrue(v)
			}
		}
		latest, _, err := posRepo.LatestFiscalDeviceReceipt(ctx)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", "fiscal_device", err) // page-error:allow mirrors fiscal_register_page.go, tracked in ut-docs#1458
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
		httpx.Render("ui/pages/fiscal_device.html", map[string]any{
			"title":          "Fiscal device",
			"theme":          d.CurrentState().Theme,
			"menuItems":      d.MenuSnapshot(),
			"pluginID":       fiscal.PluginIDTaxTR,
			"pluginActive":   fiscalDevicePluginActive(ctx, d),
			"driver":         fiscalDeviceSetting(ctx, d, "okc.driver"),
			"host":           fiscalDeviceSetting(ctx, d, "okc.host"),
			"port":           fiscalDeviceSetting(ctx, d, "okc.port"),
			"maker":          fiscalDeviceSetting(ctx, d, "okc.maker"),
			"configured":     configured,
			"systemOfRecord": systemOfRecord,
			"latest":         latest,
			"countToday":     countToday,
			"msgKey":         msgKey,
		})(w, r)
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
		if d.Settings == nil {
			common.LocalizedError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server") // page-error:allow mirrors fiscal_register_page.go, tracked in ut-docs#1458
			return
		}
		if err := d.Settings.Set(r.Context(), fiscal.KeyTSEConfigured, "true"); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", "fiscal_device", err) // page-error:allow mirrors fiscal_register_page.go, tracked in ut-docs#1458
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
		if d.Settings == nil {
			common.LocalizedError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server") // page-error:allow mirrors fiscal_register_page.go, tracked in ut-docs#1458
			return
		}
		if err := d.Settings.Set(r.Context(), fiscal.KeyTSEConfigured, "false"); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", "fiscal_device", err) // page-error:allow mirrors fiscal_register_page.go, tracked in ut-docs#1458
			return
		}
		audit(r, actor.ID, fiscalDeviceAuditUnpaired, nil)
		http.Redirect(w, r, "/fiscal-device?msg=fiscaldevice.msg.unpaired", http.StatusSeeOther)
	})
}
