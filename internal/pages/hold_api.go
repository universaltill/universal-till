package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// registerHoldAPI wires hold/resume: park the current basket so another
// customer can be served, then bring it back. Held sales are persisted
// (held_sales table) so they survive a till restart — offline-first.
func registerHoldAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewHeldSalesRepo(d.Db)

	renderBasket := func(w http.ResponseWriter, r *http.Request, toast string) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b := d.Engine.Basket()
		b.ToastMessage = toast
		_ = basketView.Render(w, &b)
	}

	// Hold the current sale: snapshot → persist → clear the basket.
	mux.HandleFunc("POST /api/pos/hold", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		if !d.Engine.HasItems() {
			renderBasket(w, r, httpx.T(locale, "hold.error.empty"))
			return
		}
		snap := d.Engine.Snapshot()
		payload, err := json.Marshal(snap)
		if err != nil {
			renderBasket(w, r, httpx.T(locale, "hold.error.failed"))
			return
		}
		label := strings.TrimSpace(snap.CustomerName)
		if label == "" {
			label = time.Now().Format("15:04")
		}
		held := data.HeldSale{
			ID:         fmt.Sprintf("hold-%d", time.Now().UnixNano()),
			Label:      label,
			TotalMinor: snap.Total.Minor(),
			LineCount:  len(snap.Lines),
			Payload:    string(payload),
		}
		if err := repo.Insert(ctx, held); err != nil {
			renderBasket(w, r, httpx.T(locale, "hold.error.failed"))
			return
		}
		d.Engine.Reset()
		w.Header().Set("HX-Trigger", "held-changed")
		renderBasket(w, r, httpx.T(locale, "hold.toast.held"))
	})

	// Resume a held sale into the (empty) basket.
	mux.HandleFunc("POST /api/pos/resume", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" {
			renderBasket(w, r, httpx.T(locale, "hold.error.not_found"))
			return
		}
		if d.Engine.HasItems() {
			renderBasket(w, r, httpx.T(locale, "hold.error.busy"))
			return
		}
		held, found, err := repo.Get(ctx, id)
		if err != nil || !found {
			renderBasket(w, r, httpx.T(locale, "hold.error.not_found"))
			return
		}
		var snap pos.BasketSnapshot
		if err := json.Unmarshal([]byte(held.Payload), &snap); err != nil {
			renderBasket(w, r, httpx.T(locale, "hold.error.failed"))
			return
		}
		d.Engine.Restore(snap)
		if err := repo.Delete(ctx, id); err != nil {
			// The sale is restored either way; a stale row is the lesser evil.
			_ = err
		}
		w.Header().Set("HX-Trigger", "held-changed")
		renderBasket(w, r, httpx.T(locale, "hold.toast.resumed"))
	})

	// Held-sales strip: chips the cashier taps to resume.
	mux.HandleFunc("GET /ui/held", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		items, err := repo.List(ctx)
		if err != nil {
			items = nil
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<div id="held-sales" class="held-strip" hx-get="/ui/held" hx-trigger="held-changed from:body" hx-swap="outerHTML">`)
		if len(items) > 0 {
			fmt.Fprintf(&b, `<span class="held-title">%s</span>`, template.HTMLEscapeString(httpx.T(locale, "hold.strip.title")))
			funcs := httpx.FuncsFor(locale)
			moneyFn, _ := funcs["money"].(func(v any) string)
			for _, h := range items {
				total := fmt.Sprintf("%d", h.TotalMinor)
				if moneyFn != nil {
					total = moneyFn(h.TotalMinor)
				}
				fmt.Fprintf(&b,
					`<button class="btn secondary held-chip" hx-post="/api/pos/resume" hx-vals='{"id":%q}' hx-target="#basket" hx-swap="outerHTML">%s · %d × · %s</button>`,
					h.ID, template.HTMLEscapeString(h.Label), h.LineCount, template.HTMLEscapeString(total))
			}
		}
		b.WriteString(`</div>`)
		_, _ = w.Write([]byte(b.String()))
	})
}
