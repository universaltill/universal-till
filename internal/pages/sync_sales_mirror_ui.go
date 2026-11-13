package pages

// The Tills page's sales-copy card (ut-docs#5074 M5; design §10).

import (
	"context"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

type salesMirrorTillLine struct {
	Name    string
	Behind  int64
	Waiting bool // no acknowledgement yet in this epoch (not pulled yet)
}

type salesMirrorCardView struct {
	IsMain     bool
	On         bool
	Killed     bool   // main till: the shop setting is on but this till's kill switch stops it
	ErrKey     string // why the last switch press was refused (htmx path)
	WindowDays int
	Tills      []salesMirrorTillLine
	// An additional till's own copy.
	StateKey string
	Behind   int64
	Sales    int64
	Since    string
}

// salesMirrorStateKeys maps a copy status to its words.
var salesMirrorStateKeys = map[string]string{
	"on":                  "tills.sales_mirror.state.up_to_date",
	"off":                 "tills.sales_mirror.state.off",
	"killed":              "tills.sales_mirror.state.off_here",
	"paused:low_disk":     "tills.sales_mirror.state.paused_low_disk",
	"paused:budget":       "tills.sales_mirror.state.paused_budget",
	"paused:version_wait": "tills.sales_mirror.state.needs_update",
	"needs_tls":           "tills.sales_mirror.state.needs_tls",
	"main_off":            "tills.sales_mirror.state.main_off",
	"satellite":           "tills.sales_mirror.state.satellite",
}

func salesMirrorCard(ctx context.Context, d *common.Deps) salesMirrorCardView {
	v := salesMirrorCardView{WindowDays: salesMirrorWindowDays(ctx, d)}
	setting, _, _ := d.Settings.Get(ctx, data.SalesMirrorEnabledKey)
	v.On = strings.TrimSpace(setting) == "1"
	if d.SyncPrimaryURL(ctx) == "" {
		v.IsMain = true
		// The switch follows the shop setting; a kill switch on this till
		// (its own sync.sales_mirror_off, or UT_SALES_MIRROR=off) is shown,
		// never offered as "Turn on" (M5 review finding 3).
		if v.On && !salesMirrorMainGate(ctx, d) {
			v.Killed = true
			return v
		}
		if !v.On {
			return v
		}
		st := salesMirrorMainStatus(ctx, d)
		acked := map[string]SalesMirrorAck{}
		for _, a := range st.Acks {
			if a.Epoch == st.Epoch {
				acked[a.TillID] = a
			}
		}
		tills, _ := data.NewTillsRepo(d.Db).ListTills(ctx)
		for _, t := range tills {
			if t.Role == data.TillRoleSatellite {
				continue
			}
			a, ok := acked[t.ID]
			v.Tills = append(v.Tills, salesMirrorTillLine{Name: t.Name, Behind: a.Behind, Waiting: !ok})
		}
		return v
	}
	state := "off"
	if cp := salesMirrorCopyStatus(ctx, d); cp != nil {
		if s, ok := cp["status"].(SalesMirrorStatus); ok {
			state = s.State
			v.Behind = s.Lag
		}
		v.Sales, _ = cp["sales"].(int64)
		v.Since, _ = cp["oldest_local_date"].(string)
	}
	v.StateKey = salesMirrorStateKeys[state]
	if v.StateKey == "" {
		v.StateKey = "tills.sales_mirror.state.off"
	}
	if state == "on" {
		// Never "Up to date — N to go" (M5 review finding 2): behind, or a
		// re-sync under way, reads "Catching up".
		if st, err := data.NewSalesMirrorRepo(d.Db).State(ctx); err == nil && (v.Behind > 0 || st.ResyncEpoch != "" || st.Epoch == "") {
			v.StateKey = "tills.sales_mirror.state.catching_up"
		}
	} else {
		v.Behind = 0
	}
	return v
}

// registerSalesMirrorUI serves the card: GET /ui/tills/sales-mirror (same
// gate as the Tills page).
func registerSalesMirrorUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/tills/sales-mirror", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		renderSalesMirrorCard(w, r, d)
	})
}

func renderSalesMirrorCard(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	renderSalesMirrorCardErr(w, r, d, "")
}

// renderSalesMirrorCardErr re-renders the card with a refusal line: htmx
// swaps only a 2xx, so a refused press answers 200 with the card saying why
// (M5 review finding 5).
func renderSalesMirrorCardErr(w http.ResponseWriter, r *http.Request, d *common.Deps, errKey string) {
	v := salesMirrorCard(r.Context(), d)
	v.ErrKey = errKey
	httpx.RenderPartial("ui/partials/tills_sales_mirror.html", v)(w, r)
}
