package pages

import (
	"html/template"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3000: GET / used to ship the basket as an empty
// hx-get="/ui/basket" hx-trigger="load" placeholder, so the sale screen's
// basket only appeared after a second request once the page had painted
// (measured on the Android tablet). It is now rendered inline, through the
// exact render path /ui/basket uses; a failed render falls back to the lazy
// placeholder.

const lazyBasketPlaceholder = `<div class="basket" hx-get="/ui/basket" hx-trigger="load" hx-swap="outerHTML"></div>`

func newIndexBasketMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, d := newIndexAndButtonsMux(t)
	registerBasket(mux, d)
	d.Engine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{
		"LATTE": {SKU: "LATTE", Name: "Inline Latte 3000", Qty: 1, PriceCents: money.FromMinor(350)},
	})
	return mux, d
}

func TestIndex_InlinesBasket(t *testing.T) {
	mux, d := newIndexBasketMux(t)
	if _, err := d.Engine.Scan("LATTE"); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	home := getOK(t, mux, "/")
	if strings.Contains(home, lazyBasketPlaceholder) || strings.Contains(home, `hx-get="/ui/basket" hx-trigger="load"`) {
		t.Fatalf("GET / still ships the lazy basket placeholder")
	}
	for _, want := range []string{`id="basket"`, `data-lines-count="1"`, `Inline Latte 3000`, `id="suggest-strip" hx-get="/ui/suggestions"`} {
		if !strings.Contains(home, want) {
			t.Fatalf("GET / missing inline basket marker %q", want)
		}
	}
	// Byte-for-byte the fragment /ui/basket serves: later basket updates
	// swap #basket outerHTML, so the first-paint root must be identical.
	frag := strings.TrimSpace(getOK(t, mux, "/ui/basket"))
	if frag == "" || !strings.Contains(home, frag) {
		t.Fatalf("GET /'s inline basket differs from GET /ui/basket's fragment")
	}
}

func TestIndex_InlineBasketRenderFailureFallsBackToPlaceholder(t *testing.T) {
	mux, _ := newIndexBasketMux(t)
	orig := basketFirstPaint
	basketFirstPaint = func(*common.Deps, http.ResponseWriter, *http.Request) template.HTML { return "" }
	t.Cleanup(func() { basketFirstPaint = orig })

	home := getOK(t, mux, "/")
	if !strings.Contains(home, lazyBasketPlaceholder) {
		t.Fatalf("a failed inline basket render must fall back to the lazy placeholder")
	}
	if strings.Contains(home, `id="basket"`) {
		t.Fatal("fallback page must not carry a (partial) basket")
	}
}

// A Deps without an Engine (several index tests build one) keeps the
// placeholder instead of panicking.
func TestIndex_InlineBasketNoEngineFallsBack(t *testing.T) {
	mux, _ := newIndexAndButtonsMux(t)
	if home := getOK(t, mux, "/"); !strings.Contains(home, lazyBasketPlaceholder) {
		t.Fatal("no Engine: GET / must keep the lazy basket placeholder")
	}
}

// ut-docs#3000 (3): the modifier-modal opener used to be an inline
// hx-on::after-request repeated on every tile (~230x on a real shop's grid).
// It is one delegated htmx:afterSwap listener in app.js now (review of #3000).
func TestSaleGrid_NoPerTileModifierModalHandler(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES ('J1','First','itm1',0),('J2','Second','itm1',1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	frag := getOK(t, mux, "/ui/buttons")
	if !strings.Contains(frag, `class="btn-tile`) {
		t.Fatal("fixture renders no tiles")
	}
	if n := strings.Count(frag, "modifier-modal').showModal()"); n != 0 {
		t.Fatalf("sale grid still carries %d per-tile modifier-modal handlers", n)
	}
	for _, f := range []string{"web/ui/partials/buttons.html", "web/ui/pages/index.html"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "modifier-modal').showModal()") {
			t.Fatalf("%s still carries an inline modifier-modal opener", f)
		}
	}
	app, err := os.ReadFile("web/public/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`htmx:afterSwap[\s\S]{0,400}modifier-modal`).Match(app) {
		t.Fatal("app.js must carry the delegated htmx:afterSwap modifier-modal opener")
	}
}
