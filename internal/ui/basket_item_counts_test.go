package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3241: the per-item basket counts reach the sale screen's tile
// badges as a server-rendered data attribute on #basket (never computed in JS).
func TestBasketView_RendersItemCountsAttribute(t *testing.T) {
	v, err := NewBasketView(httpx.FuncsFor("en"))
	if err != nil {
		t.Fatalf("NewBasketView: %v", err)
	}
	b := &pos.Basket{Lines: []pos.BasketLine{{ItemID: "A", Qty: 2, LineKey: "1"}, {ItemID: "A", Qty: 1, LineKey: "2"}}}
	var buf bytes.Buffer
	if err := v.Render(&buf, b); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `data-item-counts="{&#34;A&#34;:3}"`) {
		t.Fatalf("missing data-item-counts; got: %.600s", out)
	}
	if !strings.Contains(out, `data-in-basket-label="`) {
		t.Fatalf("missing data-in-basket-label")
	}
}
