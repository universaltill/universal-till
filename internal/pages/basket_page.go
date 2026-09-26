package pages

import (
	"bytes"
	"html/template"
	"io"
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerBasket(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/ui/basket", func(w http.ResponseWriter, r *http.Request) {
		view, err := basketViewFor(w, r)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "basket.error.server", "basket", err)
			return
		}
		_ = renderCurrentBasket(d, view, w)
	})
}

// basketViewFor and renderCurrentBasket are the one render path for the
// cashier's #basket fragment, shared by GET /ui/basket and GET /'s first
// paint (basketFirstPaint, ut-docs#3000) so both emit byte-identical markup.
func basketViewFor(w http.ResponseWriter, r *http.Request) (*ui.BasketView, error) {
	return ui.NewBasketView(httpx.FuncsFor(httpx.ResolveLocale(w, r)))
}

func renderCurrentBasket(d *common.Deps, view *ui.BasketView, out io.Writer) error {
	b, _ := d.Engine.Scan("")
	return view.Render(out, b)
}

// basketFirstPaint renders the sale screen's basket for GET /'s first paint
// (ut-docs#3000): measured on a real Android tablet, the basket arrived only
// after the page had painted, in a second /ui/basket request. Same render
// path as that route, trusted as template.HTML because it IS our own
// template's output. "" when there is no Engine (bare test Deps) or the
// render failed: index.html then keeps the lazy hx-trigger="load"
// placeholder, never a blank or half-rendered basket. A var so a test can
// force the failure path.
var basketFirstPaint = func(d *common.Deps, w http.ResponseWriter, r *http.Request) template.HTML {
	if d.Engine == nil {
		return ""
	}
	view, err := basketViewFor(w, r)
	if err != nil {
		logging.L().Warnf("index: inline basket: %v", err)
		return ""
	}
	var buf bytes.Buffer
	if err := renderCurrentBasket(d, view, &buf); err != nil {
		logging.L().Warnf("index: inline basket: %v", err)
		return ""
	}
	return template.HTML(buf.String()) //nolint:gosec // our own html/template output (basket.html), already escaped
}
