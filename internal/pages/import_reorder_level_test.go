package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2084: a "Reorder level" column in a catalogue import sets
// items.reorder_level on new items.

func commitReorderCSV(t *testing.T, dp *common.Deps, csv string) string {
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

func reorderLevelOf(t *testing.T, dp *common.Deps, sku string) int {
	t.Helper()
	var lvl int
	if err := dp.Db.QueryRow(`SELECT reorder_level FROM items WHERE sku = ?`, sku).Scan(&lvl); err != nil {
		t.Fatalf("read item %s: %v", sku, err)
	}
	return lvl
}

func TestImport_ReorderLevelColumnSetsItem(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	commitReorderCSV(t, dp, "Name,SKU,Price,In stock,Reorder level\n"+
		"Seven,RL-7,1.00,3,7\n"+
		"Zero,RL-0,1.00,3,0\n"+
		"Blank,RL-B,1.00,3,\n")
	if got := reorderLevelOf(t, dp, "RL-7"); got != 7 {
		t.Errorf("RL-7 reorder_level = %d, want 7", got)
	}
	if got := reorderLevelOf(t, dp, "RL-0"); got != 0 {
		t.Errorf("RL-0 reorder_level = %d, want 0", got)
	}
	if got := reorderLevelOf(t, dp, "RL-B"); got != 0 {
		t.Errorf("RL-B reorder_level = %d, want 0", got)
	}
	low, err := data.NewPOSRepo(dp.Db).GetLowStockItems(t.Context(), "")
	if err != nil {
		t.Fatalf("GetLowStockItems: %v", err)
	}
	found := map[string]bool{}
	for _, l := range low {
		found[l.SKU] = true
	}
	if !found["RL-7"] {
		t.Errorf("RL-7 (stock 3, reorder 7) should be low stock, got %+v", low)
	}
	if found["RL-0"] {
		t.Errorf("RL-0 has no reorder level and must not be low stock")
	}
}

func TestImport_NoReorderColumnLeavesZero(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	commitReorderCSV(t, dp, "Name,SKU,Price\nPlain,RL-P,1.00\n")
	if got := reorderLevelOf(t, dp, "RL-P"); got != 0 {
		t.Errorf("reorder_level = %d, want 0", got)
	}
}

func TestImport_XLSXUploadImportsReorderLevel(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)
	xlsxBytes := buildXLSXForPagesTest(t, [][]string{
		{"Name", "SKU", "Price", "Reorder level"},
		{"Widget", "RLX-1", "1.00", "12"},
		{"Plain", "RLX-2", "1.00", ""},
	})
	body, ct := multipartFile(t, "catalog.xlsx", xlsxBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	if got := reorderLevelOf(t, dp, "RLX-1"); got != 12 {
		t.Errorf("RLX-1 reorder_level = %d, want 12", got)
	}
	if got := reorderLevelOf(t, dp, "RLX-2"); got != 0 {
		t.Errorf("RLX-2 reorder_level = %d, want 0", got)
	}
}

func TestImport_ExistingSKUKeepsReorderLevel(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active,reorder_level) VALUES('itmRL','RL-E','Existing',100,1,4)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	commitReorderCSV(t, dp, "Name,SKU,Price,Reorder level\nExisting,RL-E,1.00,9\n")
	if got := reorderLevelOf(t, dp, "RL-E"); got != 4 {
		t.Errorf("existing item reorder_level = %d, want 4 (unchanged)", got)
	}
}

func TestImport_InvalidReorderLevelWarnsButStillImports(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)
	csv := "Name,SKU,Price,Reorder level\nBad,RL-X,1.00,2.5\n"

	body, ct := multipartCSV(t, csv, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Reorder level: 2.5") {
		t.Errorf("preview should warn with the refused value, got: %s", rec.Body.String())
	}

	resp := commitReorderCSV(t, dp, csv)
	if !strings.Contains(resp, "Reorder level: 2.5") {
		t.Errorf("commit should warn with the refused value, got: %s", resp)
	}
	if !strings.Contains(resp, `class="row-warn"`) {
		t.Errorf("row should carry the warned treatment, got: %s", resp)
	}
	if got := reorderLevelOf(t, dp, "RL-X"); got != 0 {
		t.Errorf("reorder_level = %d, want 0", got)
	}
}

func TestImport_GiftVoucherIgnoresReorderLevel(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	commitReorderCSV(t, dp, "Name,SKU,Price,Item type,Reorder level\nVoucher,RL-GV,,gift_voucher,5\n")
	if got := reorderLevelOf(t, dp, "RL-GV"); got != 0 {
		t.Errorf("gift voucher reorder_level = %d, want 0", got)
	}
}

func TestCatalogExport_ReorderLevelSurvivesExportWipeReimport(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	for _, q := range []string{
		`INSERT INTO items(id,sku,name,base_price,is_active,reorder_level) VALUES('itmA','RT-1','With Level',100,1,12)`,
		`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmB','RT-2','No Level',100,1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	exported := rec.Body.String()
	lines := strings.Split(strings.TrimSpace(exported), "\n")
	if !strings.HasSuffix(lines[0], ",Item type,Reorder level") {
		t.Fatalf("header missing Reorder level: %q", lines[0])
	}
	var sawBlank, sawTwelve bool
	for _, l := range lines[1:] {
		if strings.Contains(l, "RT-1") && strings.HasSuffix(l, ",12") {
			sawTwelve = true
		}
		if strings.Contains(l, "RT-2") && strings.HasSuffix(l, ",") {
			sawBlank = true
		}
	}
	if !sawTwelve || !sawBlank {
		t.Fatalf("cells wrong (12=%v blank=%v): %q", sawTwelve, sawBlank, exported)
	}
	if _, err := dp.Db.Exec(`DELETE FROM items WHERE id IN ('itmA','itmB')`); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	commitReorderCSV(t, dp, exported)
	if got := reorderLevelOf(t, dp, "RT-1"); got != 12 {
		t.Errorf("round-tripped reorder_level = %d, want 12", got)
	}
	if got := reorderLevelOf(t, dp, "RT-2"); got != 0 {
		t.Errorf("no-level item = %d, want 0", got)
	}
}
