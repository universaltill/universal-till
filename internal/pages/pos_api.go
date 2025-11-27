package pages

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerPOSAPI(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/api/pos/scan", func(w http.ResponseWriter, r *http.Request) {
		code := ""
		qty := 1
		if r.Header.Get("Content-Type") == "application/json" {
			type In struct {
				Code string `json:"code"`
				Qty  int    `json:"qty"`
			}
			var in In
			_ = json.NewDecoder(r.Body).Decode(&in)
			code = in.Code
			if in.Qty > 0 {
				qty = in.Qty
			}
		} else {
			_ = r.ParseForm()
			code = r.Form.Get("code")
			if q := r.Form.Get("qty"); q != "" {
				if v, err := strconv.Atoi(q); err == nil && v > 0 {
					qty = v
				}
			}
		}
		b, _ := d.engine.ScanQty(code, qty)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		_ = basketView.Render(w, b)
	})

	mux.HandleFunc("/api/pos/tender", func(w http.ResponseWriter, r *http.Request) {
		type In struct {
			Amount int64  `json:"amount"`
			Method string `json:"method"`
		}
		var in In
		_ = json.NewDecoder(r.Body).Decode(&in)
		_, _ = d.engine.Tender(in.Amount, in.Method)
		b, _ := d.engine.Scan("")
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		_ = basketView.Render(w, b)
	})
}
