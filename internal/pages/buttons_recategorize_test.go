package pages

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#2465: POST /api/buttons/recategorize -- the sale screen's /
// Designer's jiggle-mode "move this quick button to another category" (a
// drag onto a category tab, or the Move to category badge's dialog). Same
// gate as /api/buttons/reorder: primary till only, catalog_management or a
// manager PIN (a cashier gets the elevation prompt, never a 204).

func seedRecategorize(t *testing.T, d *common.Deps) {
	t.Helper()
	for _, s := range []string{
		`INSERT INTO categories (id, name, is_active) VALUES ('rc-drinks','RC Drinks',1),('rc-food','RC Food',1),('rc-old','RC Retired',0)`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('rc-item','RC-SKU','RC Latte',320,1,'rc-drinks')`,
		`INSERT INTO shortcut_buttons (barcode, label, item_id, sort_order) VALUES ('RC-SKU','RC Latte','rc-item',0)`,
	} {
		if _, err := d.Db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

func rcCategory(t *testing.T, d *common.Deps) sql.NullString {
	t.Helper()
	var c sql.NullString
	if err := d.Db.QueryRow(`SELECT category_id FROM items WHERE id = 'rc-item'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestButtonsRecategorize_ManagerMovesItem(t *testing.T) {
	for _, role := range []string{"manager", "admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			mux, d := newButtonsMuxRealSession(t)
			seedRecategorize(t, d)
			rec := postForm(mux, "/api/buttons/recategorize", url.Values{"item_id": {"rc-item"}, "category_id": {"rc-food"}}, &auth.User{ID: "u-" + role, Role: role})
			if rec.Code != http.StatusNoContent {
				t.Fatalf("code = %d, want 204: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
				t.Fatalf("HX-Trigger = %q, want buttons-changed", got)
			}
			if rec.Header().Get(ui.SellVersionHeader) == "" {
				t.Fatalf("missing %s header", ui.SellVersionHeader)
			}
			if got := rcCategory(t, d); got.String != "rc-food" {
				t.Fatalf("category = %+v, want rc-food", got)
			}
			// A manager's own direct action is not a PIN override: no audit row.
			var n int
			_ = d.Db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'buttons_recategorize'`).Scan(&n)
			if n != 0 {
				t.Fatalf("got %d buttons_recategorize audit rows for a non-elevated manager, want 0", n)
			}
		})
	}
}

func TestButtonsRecategorize_EmptyCategoryIsUncategorised(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedRecategorize(t, d)
	rec := postForm(mux, "/api/buttons/recategorize", url.Values{"item_id": {"rc-item"}, "category_id": {""}}, &auth.User{ID: "m", Role: "manager"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rcCategory(t, d); got.Valid {
		t.Fatalf("category = %q, want NULL", got.String)
	}
}

func TestButtonsRecategorize_CashierGetsElevationPrompt(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedRecategorize(t, d)
	rec := postForm(mux, "/api/buttons/recategorize", url.Values{"item_id": {"rc-item"}, "category_id": {"rc-food"}}, &auth.User{ID: "c1", Role: "cashier"})
	if !isElevationPrompt(rec) {
		t.Fatalf("cashier: want the elevation prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The PIN retry must carry both fields, or it would move nothing.
	for _, want := range []string{`name="item_id"`, `value="rc-item"`, `name="category_id"`, `value="rc-food"`, `/api/buttons/recategorize`} {
		if !strings.Contains(body, want) {
			t.Fatalf("elevation prompt missing %q: %s", want, body)
		}
	}
	if got := rcCategory(t, d); got.String != "rc-drinks" {
		t.Fatalf("cashier moved the item to %q", got.String)
	}
}

func TestButtonsRecategorize_ElevatedPINMovesAndAudits(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedRecategorize(t, d)
	mgrID, blockedID := newElevationTestPrincipals(t, d, "mgr-rc", "blocked-cashier-rc", "665544")
	rec := postForm(mux, "/api/buttons/recategorize", url.Values{"item_id": {"rc-item"}, "category_id": {"rc-food"}, "override_pin": {"665544"}}, &auth.User{ID: blockedID, Role: "cashier"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rcCategory(t, d); got.String != "rc-food" {
		t.Fatalf("category = %q, want rc-food", got.String)
	}
	var actor, blocked, target string
	if err := d.Db.QueryRow(`SELECT actor_id, blocked_actor_id, entity_id FROM audit_log WHERE action = 'buttons_recategorize'`).Scan(&actor, &blocked, &target); err != nil {
		t.Fatalf("expected a buttons_recategorize audit row: %v", err)
	}
	if actor != mgrID || blocked != blockedID || target != "rc-item" {
		t.Fatalf("audit = actor %q blocked %q target %q, want %q %q rc-item", actor, blocked, target, mgrID, blockedID)
	}
}

func TestButtonsRecategorize_RefusedOnReplica(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedRecategorize(t, d)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/buttons/recategorize", url.Values{"item_id": {"rc-item"}, "category_id": {"rc-food"}}, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "manage quick-sale buttons on the primary till") {
		t.Fatalf("replica: want the localized 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rcCategory(t, d); got.String != "rc-drinks" {
		t.Fatalf("replica moved the item to %q", got.String)
	}
}

func TestButtonsRecategorize_BadInput(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
		want int
	}{
		{"missing item_id", url.Values{"category_id": {"rc-food"}}, http.StatusBadRequest},
		{"unknown category", url.Values{"item_id": {"rc-item"}, "category_id": {"rc-nope"}}, http.StatusBadRequest},
		{"inactive category", url.Values{"item_id": {"rc-item"}, "category_id": {"rc-old"}}, http.StatusBadRequest},
		{"unknown item", url.Values{"item_id": {"rc-nope"}, "category_id": {"rc-food"}}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux, d := newButtonsMux(t)
			seedRecategorize(t, d)
			rec := postForm(mux, "/api/buttons/recategorize", c.form, nil)
			if rec.Code != c.want {
				t.Fatalf("code = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			if got := rcCategory(t, d); got.String != "rc-drinks" {
				t.Fatalf("refused request moved the item to %q", got.String)
			}
		})
	}
}

func TestButtonsRecategorize_GetNotAllowed(t *testing.T) {
	mux, d := newButtonsMux(t)
	seedRecategorize(t, d)
	rec := getWithUser(mux, "/api/buttons/recategorize?item_id=rc-item&category_id=rc-food", nil)
	if rec.Code == http.StatusNoContent || rcCategory(t, d).String != "rc-drinks" {
		t.Fatalf("a GET must never move an item: %d", rec.Code)
	}
}

// The render half: a catalog_management session's jiggle badges carry the
// Move to category badge, the strip carries the one shared dialog listing
// every category tab (in tab order), and each tile grid names its own
// category so app.js knows which tab/dialog entry is the tile's current
// one. A cashier (.Locked) gets none of it -- they never see the control.
func TestButtonsPartial_MoveToCategoryControls(t *testing.T) {
	setup := func(t *testing.T) (*http.ServeMux, *common.Deps) {
		mux, d := newButtonsMuxRealSession(t)
		seedRecategorize(t, d)
		for _, s := range []string{
			`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('rc-item2','RC-SKU2','RC Toast',250,1,'rc-food')`,
			`INSERT INTO shortcut_buttons (barcode, label, item_id, sort_order) VALUES ('RC-SKU2','RC Toast','rc-item2',1)`,
		} {
			if _, err := d.Db.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
		return mux, d
	}

	t.Run("manager sale screen", func(t *testing.T) {
		mux, _ := setup(t)
		rec := getWithUser(mux, "/ui/buttons", &auth.User{ID: "m", Role: "manager"})
		body := rec.Body.String()
		for _, want := range []string{
			`data-testid="tile-badge-move"`,
			`id="tile-move-dialog"`,
			`data-move-cat="rc-drinks"`,
			`data-move-cat="rc-food"`,
			`class="grid" data-grid-cat="rc-drinks"`,
			`class="grid" data-grid-cat="rc-food"`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("manager /ui/buttons missing %q", want)
			}
		}
		// Tab order: the dialog lists categories in the same order as the tabs.
		tabA, tabB := strings.Index(body, `data-tab-id="rc-drinks"`), strings.Index(body, `data-tab-id="rc-food"`)
		dlgA, dlgB := strings.Index(body, `data-move-cat="rc-drinks"`), strings.Index(body, `data-move-cat="rc-food"`)
		if (tabA < tabB) != (dlgA < dlgB) {
			t.Fatalf("dialog order differs from the tab order")
		}
	})

	t.Run("cashier sale screen", func(t *testing.T) {
		mux, _ := setup(t)
		rec := getWithUser(mux, "/ui/buttons", &auth.User{ID: "c", Role: "cashier"})
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("cashier /ui/buttons = %d", rec.Code)
		}
		for _, unwanted := range []string{`tile-badge-move`, `id="tile-move-dialog"`} {
			if strings.Contains(body, unwanted) {
				t.Fatalf("cashier /ui/buttons must not carry %q", unwanted)
			}
		}
	})

	t.Run("designer replica", func(t *testing.T) {
		mux, _ := setup(t)
		rec := getWithUser(mux, "/ui/buttons?mode=edit", &auth.User{ID: "m", Role: "manager"})
		body := rec.Body.String()
		if n := strings.Count(body, `data-testid="tile-badge-move"`); n < 2 {
			t.Fatalf("designer replica: want a server-rendered move badge per tile, got %d", n)
		}
		if !strings.Contains(body, `id="tile-move-dialog"`) {
			t.Fatalf("designer replica: missing the move dialog")
		}
	})
}
