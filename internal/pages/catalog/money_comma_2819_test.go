package catalog

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2819: the item cost and the modifier option price are parsed on
// the server. A German keyboard's "3,50" used to fail strconv.ParseFloat
// (and the dot-only field pattern), while "1e3" and "0x10" were accepted.
// Both handlers now read the same grammar as the field's pattern.

func TestItemCost_AcceptsDecimalComma_RefusesFloatSyntax(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Tea", BasePrice: 100, IsActive: true})

	if rec := postForm(t, mux, "/api/catalog/item-cost", "panelItem=itm1&cost=3%2C50"); rec.Code != http.StatusOK {
		t.Fatalf("cost=3,50: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var minor int64
	if err := db.QueryRow(`SELECT cost_price FROM items WHERE id = 'itm1'`).Scan(&minor); err != nil {
		t.Fatal(err)
	}
	if minor != 350 {
		t.Fatalf("cost=3,50 must store 350, got %d", minor)
	}
	for _, bad := range []string{"1e3", "0x10", "NaN", "Inf", "2.505", "1,234.56"} {
		if rec := postForm(t, mux, "/api/catalog/item-cost", "panelItem=itm1&cost="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("cost=%q: want 400, got %d", bad, rec.Code)
		}
	}
	if err := db.QueryRow(`SELECT cost_price FROM items WHERE id = 'itm1'`).Scan(&minor); err != nil || minor != 350 {
		t.Fatalf("a refused cost must not change the stored 350, got %d err=%v", minor, err)
	}
}

func TestModifierOption_AcceptsDecimalComma_RefusesFloatSyntax(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	if _, err := data.NewModifierRepo(db).CreateGroup(t.Context(), "g1", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "EUR"}, Menu: []common.MenuItem{}})

	rec := postModifiers(t, mux, "/api/catalog/modifier-option", "groupId=g1&name=Oat&priceDeltaMajor=0%2C40&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("priceDeltaMajor=0,40: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var delta int64
	if err := db.QueryRow(`SELECT price_delta_minor FROM item_modifier_options WHERE group_id = 'g1'`).Scan(&delta); err != nil || delta != 40 {
		t.Fatalf("option delta = %d err=%v, want 40", delta, err)
	}
	for _, bad := range []string{"1e3", "0x10", "NaN", "0.405", "-1"} {
		rec := postModifiers(t, mux, "/api/catalog/modifier-option", "groupId=g1&name=Soy&priceDeltaMajor="+bad+"&isActive=1")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("priceDeltaMajor=%q: want 400, got %d", bad, rec.Code)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_modifier_options WHERE name = 'Soy'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused options stored: %d err=%v", n, err)
	}
}

// Both server-parsed fields render the comma-tolerant pattern and opt into
// the localized invalid-amount message (data-money-local).
func TestServerParsedMoneyFields_RenderLocalPattern(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Tea", BasePrice: 100, IsActive: true})
	body := get(t, mux, "/api/catalog/item-variants?item_id=itm1").Body.String()
	if !strings.Contains(body, `name="cost" pattern="[0-9]+([.,][0-9]{1,2})?" data-money-local`) {
		t.Fatalf("cost field must use the local money pattern, got: %s", excerptAround(body, `name="cost"`))
	}
}

// The /modifiers option price fields (existing option + new option) use the
// same comma-tolerant pattern, and the new-option placeholder follows the
// currency's decimals instead of a hard-coded "0.00".
func TestModifiersPage_PriceFieldsUseLocalPattern(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	repo := data.NewModifierRepo(db)
	gid, err := repo.CreateGroup(t.Context(), "g1", "Extras", false, 0, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(t.Context(), "o1", gid, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default", Currency: "GBP"}, Menu: []common.MenuItem{}})
	body := get(t, mux, "/modifiers").Body.String()
	if n := strings.Count(body, `name="priceDeltaMajor"`); n != 2 {
		t.Fatalf("want 2 price fields (existing + new option), got %d", n)
	}
	if n := strings.Count(body, `pattern="[0-9]+([.,][0-9]{1,2})?" data-money-local`); n != 2 {
		t.Fatalf("both modifier price fields must use the local money pattern, got %d: %s", n, excerptAround(body, `name="priceDeltaMajor"`))
	}
	if strings.Contains(body, `(\.[0-9]{1,2})?`) {
		t.Fatalf("a dot-only money pattern is left on /modifiers: %s", excerptAround(body, `(\.[0-9]`))
	}
}
