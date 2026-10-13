package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
