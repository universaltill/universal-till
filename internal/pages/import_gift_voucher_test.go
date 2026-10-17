package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// TestImport_GiftVoucherRowCommitsAsVoucherAndSaysSo (ut-docs#4225): a
// gift voucher row lands as item_type gift_voucher, never stock-tracked,
// and its result row names the type so the operator sees it wasn't
// imported as an ordinary €0 product.
func TestImport_GiftVoucherRowCommitsAsVoucherAndSaysSo(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Category,Item type\n" +
		"Gutscheine,30004,,Gutscheine,gift_voucher\n" +
		"Espresso,10234,2.90,Coffee,standard\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var itemType string
	var untracked int
	if err := dp.Db.QueryRow(`SELECT item_type, stock_untracked FROM items WHERE sku = ?`, "30004").Scan(&itemType, &untracked); err != nil {
		t.Fatalf("voucher item: %v", err)
	}
	if itemType != "gift_voucher" || untracked != 1 {
		t.Errorf("voucher item = type %q untracked %d, want gift_voucher, 1", itemType, untracked)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "(Gift voucher)") {
		t.Errorf("result does not name the gift voucher type:\n%s", out)
	}
	if strings.Count(out, "(Gift voucher)") != 1 {
		t.Errorf("only the voucher row should be labelled, got %d", strings.Count(out, "(Gift voucher)"))
	}
	// The type chip is preview-only; the commit result keeps its suffix.
	if strings.Contains(out, "import-item-type") {
		t.Errorf("commit response must not carry the preview type chip:\n%s", out)
	}
}

// TestImport_GiftVoucherRowShowsTypeChipInPreview (ut-docs#4262): the
// Preview grid tags a gift voucher row with a type chip next to its name
// so it isn't mistaken for a plain €0.00 product; standard rows get none.
func TestImport_GiftVoucherRowShowsTypeChipInPreview(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Category,Item type\n" +
		"Gutscheine,30004,,Gutscheine,gift_voucher\n" +
		"Espresso,10234,2.90,Coffee,standard\n"
	body, ct := multipartCSV(t, csv, nil) // no commit: preview
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()

	const chip = `class="tag import-item-type"`
	if n := strings.Count(out, chip); n != 1 {
		t.Fatalf("want exactly one type chip, got %d:\n%s", n, out)
	}
	rowOf := func(name string) string {
		i := strings.Index(out, "<td>"+name)
		if i < 0 {
			t.Fatalf("row %q not found:\n%s", name, out)
		}
		start := strings.LastIndex(out[:i], "<tr")
		end := strings.Index(out[i:], "</tr>")
		return out[start : i+end]
	}
	voucher := rowOf("Gutscheine")
	if !strings.Contains(voucher, chip) || !strings.Contains(voucher, ">Gift voucher</span>") {
		t.Errorf("voucher row lacks the Gift voucher chip: %s", voucher)
	}
	// Beside the name: the chip is in the row's first (name) cell.
	if nameCell := voucher[:strings.Index(voucher, "</td>")]; !strings.Contains(nameCell, chip) {
		t.Errorf("chip is not in the name cell: %s", voucher)
	}
	if strings.Contains(rowOf("Espresso"), "import-item-type") {
		t.Errorf("standard row must not carry a chip")
	}
}

// ut-docs#4399: a new shop's import that brings its own gift voucher item
// retires the one setup seeded, while nothing has used it, and says so.

func seedSetupGiftVoucherItem(t *testing.T, dp *common.Deps) string {
	t.Helper()
	repo := data.NewCatalogRepo(dp.Db)
	if created, err := repo.SeedDefaultGiftVoucherItem(t.Context(), "Gift voucher"); err != nil || !created {
		t.Fatalf("seed: created=%v err=%v", created, err)
	}
	id, ok, err := repo.SeededGiftVoucherItemID(t.Context())
	if err != nil || !ok {
		t.Fatalf("marker: ok=%v err=%v", ok, err)
	}
	return id
}

func commitImportCSV(t *testing.T, dp *common.Deps, csv string) string {
	t.Helper()
	mux := http.NewServeMux()
	registerImport(mux, dp)
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func giftVoucherItemCount(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE item_type = 'gift_voucher'`).Scan(&n); err != nil {
		t.Fatalf("count voucher items: %v", err)
	}
	return n
}

func TestImport_GiftVoucherRowRetiresTheSeededItem(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	seededID := seedSetupGiftVoucherItem(t, dp)

	out := commitImportCSV(t, dp, "Name,SKU,Price,Category,Item type\n"+
		"Gutscheine,30004,,Gutscheine,gift_voucher\n"+
		"Espresso,10234,2.90,Coffee,standard\n")

	if n := giftVoucherItemCount(t, dp); n != 1 {
		t.Fatalf("voucher items = %d, want 1 (the imported one)", n)
	}
	var exists bool
	if err := dp.Db.QueryRow(`SELECT EXISTS (SELECT 1 FROM items WHERE id = ?)`, seededID).Scan(&exists); err != nil || exists {
		t.Fatalf("seeded item still exists (%v)", err)
	}
	want := "The gift voucher item “Gift voucher” created at setup was removed, because this import brought its own."
	if !strings.Contains(out, `data-seeded-voucher-removed="1"`) || !strings.Contains(out, want) {
		t.Errorf("summary does not say the seeded item was removed:\n%s", out)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type = 'catalog' AND action = 'import' ORDER BY rowid DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if !strings.Contains(payload, `"seeded_gift_voucher_item_removed":true`) {
		t.Errorf("audit payload = %s, want seeded_gift_voucher_item_removed true", payload)
	}
}

func TestImport_SeededGiftVoucherItemStaysWithoutAnImportedOne(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	seedSetupGiftVoucherItem(t, dp)

	out := commitImportCSV(t, dp, "Name,SKU,Price,Category\nEspresso,10234,2.90,Coffee\n")

	if n := giftVoucherItemCount(t, dp); n != 1 {
		t.Fatalf("voucher items = %d, want the seeded one kept", n)
	}
	if strings.Contains(out, "data-seeded-voucher-removed") {
		t.Errorf("summary claims a removal:\n%s", out)
	}
	if _, ok, _ := data.NewCatalogRepo(dp.Db).SeededGiftVoucherItemID(t.Context()); !ok {
		t.Error("marker cleared by an import with no voucher item")
	}
}

func TestImport_SeededGiftVoucherItemInTheLiveBasketIsKept(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	seededID := seedSetupGiftVoucherItem(t, dp)
	dp.Engine = pos.NewServiceWithResolver(pos.Config{}, nil)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "GV", Name: "Gift voucher", ItemID: seededID}, 1, nil)

	out := commitImportCSV(t, dp, "Name,SKU,Price,Category,Item type\nGutscheine,30004,,Gutscheine,gift_voucher\n")

	if n := giftVoucherItemCount(t, dp); n != 2 {
		t.Fatalf("voucher items = %d, want 2 (seeded one is in the basket)", n)
	}
	if strings.Contains(out, "data-seeded-voucher-removed") {
		t.Errorf("summary claims a removal:\n%s", out)
	}
	// Not decided yet: the next import may still retire it.
	if _, ok, _ := data.NewCatalogRepo(dp.Db).SeededGiftVoucherItemID(t.Context()); !ok {
		t.Error("marker cleared while the item was only in a live basket")
	}
}
