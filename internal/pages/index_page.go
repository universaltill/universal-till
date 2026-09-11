package pages

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerIndex(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/help" {
			renderHelpPage(w, r, d, "")
			return
		}
		// The "/" pattern catches every otherwise-unrouted path. Plugin page
		// entries may register arbitrary routes (e.g. /faq), so dispatch those
		// here; anything else unknown is a 404 rather than silently showing
		// the home page.
		if r.URL.Path != "/" {
			if entry, ok := findPageEntry(r, d); ok {
				renderPluginPage(w, r, d, entry)
				return
			}
			http.NotFound(w, r)
			return
		}
		mode, _, _ := d.Settings.Get(r.Context(), "display.mode")
		// Back-office mode (ADR-0018): this device defaults to the manager
		// dashboard instead of the sale screen. Per-till setting (display.*
		// never LAN-syncs). /backoffice is manager/admin role-gated — a
		// cashier session on a backoffice-mode till falls through to the
		// normal sale screen instead of a dead-end 403 (the mode is a
		// default landing preference, not a role bypass).
		if mode == "backoffice" && canPerform(d, r, "reports") {
			http.Redirect(w, r, "/backoffice", http.StatusSeeOther)
			return
		}
		// Self-order kiosk mode (ADR-0020): "/" itself requires a session
		// (it's not auth-exempt), so this branch only runs for a request
		// that already carries a LIVE one (e.g. a manager checking what the
		// kiosk shows, or another operator's still-valid session on a
		// second device) — auth.Middleware's own SetAnonymousRootRedirect
		// seam (ut-docs#1259) is what sends a genuinely anonymous "/" to
		// /self-order before this handler is ever reached, since every real
		// kiosk launcher (packaging/linux/unitill-kiosk-launch.sh, the
		// desktop shell, the Android app) opens "/", not /self-order
		// directly. Applies to every authenticated session here too, not
		// just managers: a self-order-mode till isn't meant to show the
		// cashier screen to anyone by default.
		if mode == "self_order" {
			http.Redirect(w, r, "/self-order", http.StatusSeeOther)
			return
		}
		// One query drives both tender UIs: full rows for the Pay tab,
		// their ids for the split-tender select.
		payMethods, _ := data.NewPOSRepo(d.Db).ListActivePaymentMethods(r.Context())
		// The shop's preferred method (ADR-0016 manual mode: the cheaper/
		// house provider) leads the list, so it's the one-tap default.
		if pref, ok, _ := d.Settings.Get(r.Context(), "payments.default_method"); ok && pref != "" {
			for i, m := range payMethods {
				if m.ID == pref && i > 0 {
					payMethods = append([]data.PaymentMethod{m}, append(payMethods[:i], payMethods[i+1:]...)...)
					break
				}
			}
		}
		// The Split <select> gets the FULL list — ut-docs#1832: that's the
		// one place a voucher-type method (the built-in 'voucher' row that
		// pos.CompleteSale keys tracked redemptions on, and the legacy
		// 'gift' row) belongs, next to the voucher-id field app.js reveals
		// for it.
		methods := make([]string, 0, len(payMethods))
		for _, m := range payMethods {
			methods = append(methods, m.ID)
		}
		// The Pay grid and the ⚡ quick-pay button get the list WITHOUT
		// voucher-type methods: those buttons tender "everything owed" in
		// one tap with no further input, and a voucher redemption needs a
		// voucher id (and refuses change), which the grid has no field for
		// — CompleteSale would reject the payment, or worse, record an
		// untracked 'gift' tender that debits no voucher at all.
		// Case-folded (ut-docs#1832 review): built-in rows carry a lowercase
		// type, but a PLUGIN-provided method's type comes straight out of
		// its manifest — SyncPluginPaymentMethods lifts it verbatim from
		// config_json's `method_type` with no canonicalization — so a
		// manifest saying "Voucher" would slip a voucher-type method into
		// the one-tap grid. Matches how pos.CompleteSale itself compares a
		// payment's MethodID, and the request-boundary lowercasing
		// ut-docs#1795 established for payment methods generally.
		gridMethods := make([]data.PaymentMethod, 0, len(payMethods))
		for _, m := range payMethods {
			if !strings.EqualFold(strings.TrimSpace(m.Type), "voucher") {
				gridMethods = append(gridMethods, m)
			}
		}
		// Per-provider fee rules (B4 cost-rules): manager-entered percent
		// (basis points) + fixed (minor units); the tender UI shows a live
		// "≈ fee" hint so the cashier picks the cheaper provider (ADR-0016
		// manual mode).
		// ut-docs#1323: one prefix-scan query for every payments.fee.* row,
		// instead of one Settings.Get per active payment method on every
		// load of this, the product's highest-traffic page.
		fees := map[string]map[string]int64{}
		feeSettings, _ := d.Settings.GetByPrefix(r.Context(), "payments.fee.")
		for _, m := range payMethods {
			if raw, ok := feeSettings["payments.fee."+m.ID]; ok && raw != "" {
				var f struct {
					BP    int64 `json:"bp"`
					Fixed int64 `json:"fixed"`
				}
				if json.Unmarshal([]byte(raw), &f) == nil && (f.BP > 0 || f.Fixed > 0) {
					fees[m.ID] = map[string]int64{"bp": f.BP, "fixed": f.Fixed}
				}
			}
		}
		feesJSON, _ := json.Marshal(fees)
		if len(methods) == 0 {
			methods = []string{"cash", "card"}
		}
		defaultMethod := methods[0]
		// ut-docs#1336: one-tap quick pay in the default view's footer.
		// gridMethods is already preferred-method-first (the reorder above
		// ran on payMethods before the voucher filter), so its head IS the
		// shop's default; nil when no one-tap method row exists, which the
		// template mirrors with the same hardcoded-cash fallback the
		// overlay's own pay-grid `{{ else }}` branch uses. The Split
		// select's preselected method follows the same head (ut-docs#1832):
		// a voucher-type default there would open the tab on a method whose
		// voucher-id field the operator hasn't asked for yet.
		var defaultPayMethod *data.PaymentMethod
		if len(gridMethods) > 0 {
			defaultPayMethod = &gridMethods[0]
			defaultMethod = gridMethods[0].ID
		}
		// DE+TR fiscal-signing-device hard gate (ADR-0048, fiscal.RequiresHardGate): while an owner override window is
		// active, the sale screen shows a persistent banner — sales are
		// being recorded without a TSE signature, and everyone at the till
		// should see that state, not discover it per-receipt. Rendered via
		// the existing pos-notice surface, never a modal blocker. A gate
		// read error just renders no banner (the gate itself still runs on
		// every tender).
		fiscalOverrideActive := false
		fiscalOverrideUntil := ""
		if g, gErr := evaluateFiscalGate(r.Context(), d); gErr == nil && g.Decision == fiscal.AllowedWithOverride {
			fiscalOverrideActive = true
			fiscalOverrideUntil = g.OverrideUntil.Local().Format("2006-01-02 15:04")
		}
		// ut-docs#1174 item D: the setup wizard's completion redirect carries
		// ?tse_setup=rejected when its own synchronous TSE kickoff attempt
		// got a fast definitive rejection, so the operator sees the failure
		// here, immediately, instead of discovering it in Settings (or at the
		// first refused sale). The param alone proves nothing — the banner
		// only renders when the STORED state really is kickoff_rejected, and
		// it reuses the exact Settings message mapping (tseProvisioningViewFor)
		// rather than inventing new copy. One-shot by construction: any plain
		// later visit to "/" has no param and shows nothing.
		var tseRejectedView *tseProvisioningView
		if r.URL.Query().Get("tse_setup") == "rejected" {
			if st, stErr := loadTSEProvisioningState(r.Context(), d); stErr == nil && st != nil && st.Status == tseStatusKickoffRejected {
				tseRejectedView = tseProvisioningViewFor(st)
			}
		}
		// ut-docs#1984: the Payment button lives outside the #basket fragment
		// (like the fee-hint/pay-voucher scripts below, it can't otherwise
		// react to a scan), so its empty-basket label/disabled state needs
		// the CURRENT basket here at first paint -- rendering it correct from
		// the start avoids a flash from a stale default to the real state
		// once htmx's own "load"-triggered /ui/basket fetch lands. A cheap
		// in-memory read (Engine.Basket(), not Scan), same call update_api.go
		// already makes for its own empty-basket check. d.Engine is nil in
		// some test harnesses that only exercise unrelated routes on this
		// same mux (e.g. TestBackofficeModeRedirectsHome) -- payItemCount=0
		// there just renders the empty-basket state, matching an actually-
		// empty basket.
		var payItemCount int
		var payTotal money.Money
		if d.Engine != nil {
			b := d.Engine.Basket()
			payItemCount = b.ItemCount()
			payTotal = b.Total
		}
		data := map[string]any{
			"title":                "Universal Till",
			"saleScreen":           true,
			"theme":                d.CurrentState().Theme,
			"menuItems":            d.MenuSnapshot(),
			"currency":             d.CurrentState().Currency,
			"paymentMethods":       methods,
			"paymentFeesJSON":      template.JS(feesJSON),
			"paymentMethodDefault": defaultMethod,
			"payMethods":           gridMethods,
			"defaultPayMethod":     defaultPayMethod,
			"aiIdentify":           aiService(r.Context(), d).Enabled(),
			"fiscalOverrideActive": fiscalOverrideActive,
			"fiscalOverrideUntil":  fiscalOverrideUntil,
			"tseKickoffRejected":   tseRejectedView,
			"payItemCount":         payItemCount,
			"payTotal":             payTotal,
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
}
