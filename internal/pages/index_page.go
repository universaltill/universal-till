package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerIndex(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/help" {
			renderHelpPage(w, r, d)
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
		// Back-office mode (ADR-0018): this device is a manager station, not
		// a register — the home surface is the manager dashboard and the sale
		// screen is unreachable. Per-till setting (display.* never LAN-syncs).
		if mode, _, _ := d.Settings.Get(r.Context(), "display.mode"); mode == "backoffice" {
			http.Redirect(w, r, "/backoffice", http.StatusSeeOther)
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
		methods := make([]string, 0, len(payMethods))
		for _, m := range payMethods {
			methods = append(methods, m.ID)
		}
		// Per-provider fee rules (B4 cost-rules): manager-entered percent
		// (basis points) + fixed (minor units); the tender UI shows a live
		// "≈ fee" hint so the cashier picks the cheaper provider (ADR-0016
		// manual mode).
		fees := map[string]map[string]int64{}
		for _, m := range payMethods {
			if raw, ok, _ := d.Settings.Get(r.Context(), "payments.fee."+m.ID); ok && raw != "" {
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
		data := map[string]any{
			"title":                "Universal Till",
			"saleScreen":           true,
			"theme":                d.CurrentState().Theme,
			"menuItems":            d.Menu,
			"currency":             d.CurrentState().Currency,
			"paymentMethods":       methods,
			"paymentFeesJSON":      template.JS(feesJSON),
			"paymentMethodDefault": defaultMethod,
			"payMethods":           payMethods,
			"aiIdentify":           aiService(r.Context(), d).Enabled(),
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
}

func collectPaymentMethods(ctx context.Context, db *sql.DB) []string {
	defaults := []string{"cash", "card", "voucher", "account"}
	if db == nil {
		return defaults
	}
	methods, err := data.NewPOSRepo(db).ListPaymentMethodIDs(ctx)
	if err != nil {
		return defaults
	}
	if len(methods) == 0 {
		return defaults
	}
	return methods
}
