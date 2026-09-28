package pages

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#2534: POST /api/buttons/reorder with scope=subset -- the
// all_filter_chips All grid, under a category chip, posts only that
// category's tiles; the server re-deals them into the global slots they
// already hold (ui.ButtonStore.UpdateOrderSubset). Same catalog_management
// gate as the full-list reorder: a cashier gets the elevation prompt (whose
// PIN retry is still a subset reorder) and nothing is persisted.

func seedSubsetReorder(t *testing.T, d *common.Deps) {
	t.Helper()
	for _, s := range []string{
		`INSERT INTO categories(id,name,sort_order) VALUES ('cat_a','Alpha',1),('cat_b','Beta',2)`,
		`INSERT INTO items(id,sku,name,base_price,category_id,is_active) VALUES
			('a1','SA1','Apple',100,'cat_a',1),('b1','SB1','Banana',100,'cat_b',1),
			('a2','SA2','Cherry',100,'cat_a',1),('b2','SB2','Date',100,'cat_b',1)`,
	} {
		if _, err := d.Db.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func quickOrder(t *testing.T, d *common.Deps) string {
	t.Helper()
	btns, err := d.BtnStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	codes := make([]string, 0, len(btns))
	for _, b := range btns {
		codes = append(codes, b.Code)
	}
	return strings.Join(codes, ",")
}

// swapCodes is order with codes a and b trading places -- the expected
// result of a subset reorder of [b, a] (seedForPages seeds items of its
// own, so the tests derive from the real starting order).
func swapCodes(order, a, b string) string {
	parts := strings.Split(order, ",")
	for i, c := range parts {
		switch c {
		case a:
			parts[i] = b
		case b:
			parts[i] = a
		}
	}
	return strings.Join(parts, ",")
}

func TestButtonsReorder_SubsetScope(t *testing.T) {

	t.Run("manager re-deals within the category", func(t *testing.T) {
		mux, d := newButtonsMuxRealSession(t)
		seedSubsetReorder(t, d)
		start := quickOrder(t, d)
		mgr := auth.User{ID: "m1", Role: "manager"}
		rec := postForm(mux, "/api/buttons/reorder", url.Values{"scope": {"subset"}, "codes": {"SA2", "SA1"}}, &mgr)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("subset reorder = %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("HX-Trigger") != "buttons-changed" {
			t.Fatalf("HX-Trigger = %q, want buttons-changed", rec.Header().Get("HX-Trigger"))
		}
		if got, want := quickOrder(t, d), swapCodes(start, "SA1", "SA2"); got != want {
			t.Fatalf("order = %s, want %s (only Apple and Cherry trade slots)", got, want)
		}
	})

	t.Run("no known codes is a 400", func(t *testing.T) {
		mux, d := newButtonsMuxRealSession(t)
		seedSubsetReorder(t, d)
		start := quickOrder(t, d)
		mgr := auth.User{ID: "m1", Role: "manager"}
		rec := postForm(mux, "/api/buttons/reorder", url.Values{"scope": {"subset"}, "codes": {"nope"}}, &mgr)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unknown-only subset reorder = %d, want 400: %s", rec.Code, rec.Body.String())
		}
		if got := quickOrder(t, d); got != start {
			t.Fatalf("order changed: %s", got)
		}
	})

	t.Run("cashier gets the elevation prompt and nothing persists", func(t *testing.T) {
		mux, d := newButtonsMuxRealSession(t)
		seedSubsetReorder(t, d)
		start := quickOrder(t, d)
		cashier := auth.User{ID: "c1", Role: "cashier"}
		rec := postForm(mux, "/api/buttons/reorder", url.Values{"scope": {"subset"}, "codes": {"SA2", "SA1"}}, &cashier)
		if !isElevationPrompt(rec) {
			t.Fatalf("cashier subset reorder: want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{`name="scope" value="subset"`, `name="codes" value="SA2,SA1"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("elevation prompt must replay %s, got: %s", want, body)
			}
		}
		if got := quickOrder(t, d); got != start {
			t.Fatalf("cashier's subset reorder persisted: %s", got)
		}
	})
}

// Review fix (ut-docs#2534): a reorder answers with the sell_screen_version
// it moved the catalog to (ui.SellVersionHeader), so the open sale screen's
// live-refresh watcher (web/public/sell-screen-watch.js) can treat this
// till's own save as already rendered instead of re-rendering the grid --
// which, under an all_filter_chips category chip, dropped the chip. Both
// the full-list and the subset reorder carry it.
func TestButtonsReorder_ReportsSellVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"subset", url.Values{"scope": {"subset"}, "codes": {"SA2", "SA1"}}},
		{"full list", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, d := newButtonsMuxRealSession(t)
			seedSubsetReorder(t, d)
			before, _, err := d.BtnStore.SellGeneration(context.Background())
			if err != nil {
				t.Fatalf("SellGeneration: %v", err)
			}
			form := tc.form
			if form == nil {
				parts := strings.Split(quickOrder(t, d), ",")
				for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
					parts[i], parts[j] = parts[j], parts[i]
				}
				form = url.Values{"codes": parts}
			}
			mgr := auth.User{ID: "m1", Role: "manager"}
			rec := postForm(mux, "/api/buttons/reorder", form, &mgr)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("reorder = %d: %s", rec.Code, rec.Body.String())
			}
			after, _, err := d.BtnStore.SellGeneration(context.Background())
			if err != nil {
				t.Fatalf("SellGeneration: %v", err)
			}
			if after == before {
				t.Fatalf("reorder did not move sell_screen_version (%d) -- the test proves nothing", after)
			}
			if got, want := rec.Header().Get(ui.SellVersionHeader), strconv.FormatInt(after, 10); got != want {
				t.Fatalf("%s = %q, want %q (the version after the save)", ui.SellVersionHeader, got, want)
			}
		})
	}
}
