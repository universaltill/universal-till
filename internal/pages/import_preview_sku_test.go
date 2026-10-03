package pages

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3098: a blank-SKU import row already gets a generated SKU at
// commit (ut-docs#3087), but invisibly. The preview now shows that SKU in an
// editable row_sku_<Idx> field; the staged commit uses what the operator
// kept or typed, a cleared field falls back to commit-time generation, and
// the result names the SKU each such row got.

// genSKUCSV: rows 0 and 1 have no SKU and share a category that does not
// exist yet; row 2 carries its own SKU and must get no field.
const genSKUCSV = "Name,SKU,Barcode,Price,Category\n" +
	"Apple,,,1.00,Fruit\n" +
	"Pear,,,1.20,Fruit\n" +
	"Cake,CK1,,2.00,Bakery\n"

func skuFieldValue(t *testing.T, resp string, idx string) (string, bool) {
	t.Helper()
	re := regexp.MustCompile(`<input[^>]*name="row_sku_` + idx + `"[^>]*>`)
	tag := re.FindString(resp)
	if tag == "" {
		return "", false
	}
	m := regexp.MustCompile(`value="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("row_sku_%s input has no value attribute: %s", idx, tag)
	}
	if !strings.Contains(tag, `form="import-form"`) || !strings.Contains(tag, `aria-label="`) {
		t.Fatalf("row_sku_%s input must be form-associated and labelled: %s", idx, tag)
	}
	return m[1], true
}

func newGenSKUMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	resetStagedCatalog(t)
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)
	return mux, dp
}

func TestImport_PreviewShowsDistinctGeneratedSKUs(t *testing.T) {
	mux, _ := newGenSKUMux(t)
	_, resp := previewAndExtractStagedID(t, mux, genSKUCSV)

	v0, ok0 := skuFieldValue(t, resp, "0")
	v1, ok1 := skuFieldValue(t, resp, "1")
	if !ok0 || !ok1 {
		t.Fatalf("blank-SKU rows must render a row_sku_<Idx> field: %s", resp)
	}
	if v0 != "FRU-0001" || v1 != "FRU-0002" {
		t.Fatalf("generated SKUs = %q, %q; want FRU-0001, FRU-0002 (distinct within one preview)", v0, v1)
	}
	if _, ok := skuFieldValue(t, resp, "2"); ok {
		t.Fatalf("a row with its own SKU must get no generated-SKU field: %s", resp)
	}
}

func TestImport_PreviewGeneratedSKUFollowsExistingCategoryNumbers(t *testing.T) {
	mux, dp := newGenSKUMux(t)
	if _, err := dp.Db.Exec(`INSERT INTO categories (id, name) VALUES ('c-fruit', 'Fruit')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES ('i-fig', '4100', 'Fig', 100, 'c-fruit', 1)`); err != nil {
		t.Fatal(err)
	}
	_, resp := previewAndExtractStagedID(t, mux, genSKUCSV)
	v0, _ := skuFieldValue(t, resp, "0")
	v1, _ := skuFieldValue(t, resp, "1")
	if v0 != "4101" || v1 != "4102" {
		t.Fatalf("generated SKUs = %q, %q; want 4101, 4102", v0, v1)
	}
}

// A SKU another row of the same file carries explicitly is not offered
// to a blank row (it would collide at commit).
func TestImport_PreviewGeneratedSKUSkipsInFileSKUs(t *testing.T) {
	mux, _ := newGenSKUMux(t)
	csv := "Name,SKU,Barcode,Price,Category\n" +
		"Apple,,,1.00,Fruit\n" +
		"Plum,FRU-0001,,1.20,Fruit\n"
	_, resp := previewAndExtractStagedID(t, mux, csv)
	if v, _ := skuFieldValue(t, resp, "0"); v != "FRU-0002" {
		t.Fatalf("generated SKU = %q, want FRU-0002 (FRU-0001 is row 1's own SKU)", v)
	}
}

func commitGenSKU(t *testing.T, mux *http.ServeMux, id string, extra map[string]string) string {
	t.Helper()
	fields := map[string]string{"commit": "1", "staged_id": id}
	for k, v := range extra {
		fields[k] = v
	}
	body, ct := multipartFields(t, fields)
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("staged commit: code %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func skuByName(t *testing.T, dp *common.Deps, name string) string {
	t.Helper()
	var sku string
	if err := dp.Db.QueryRow(`SELECT sku FROM items WHERE name = ?`, name).Scan(&sku); err != nil {
		t.Fatalf("item %q not imported: %v", name, err)
	}
	return sku
}

func TestImport_CommitUsesShownGeneratedSKU(t *testing.T) {
	mux, dp := newGenSKUMux(t)
	id, _ := previewAndExtractStagedID(t, mux, genSKUCSV)
	resp := commitGenSKU(t, mux, id, map[string]string{"row_sku_0": "FRU-0001", "row_sku_1": "FRU-0002"})
	if got := skuByName(t, dp, "Apple"); got != "FRU-0001" {
		t.Fatalf("Apple sku = %q, want FRU-0001", got)
	}
	if got := skuByName(t, dp, "Pear"); got != "FRU-0002" {
		t.Fatalf("Pear sku = %q, want FRU-0002", got)
	}
	// AC3: the result names the SKU each such row got (the grid has no
	// SKU column, so it can only appear through the status text).
	for _, want := range []string{"FRU-0001", "FRU-0002"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("commit result must name generated SKU %s: %s", want, resp)
		}
	}
}

func TestImport_CommitUsesEditedGeneratedSKU(t *testing.T) {
	mux, dp := newGenSKUMux(t)
	id, _ := previewAndExtractStagedID(t, mux, genSKUCSV)
	resp := commitGenSKU(t, mux, id, map[string]string{"row_sku_0": "  APPLE-RED  ", "row_sku_1": "FRU-0002"})
	if got := skuByName(t, dp, "Apple"); got != "APPLE-RED" {
		t.Fatalf("Apple sku = %q, want the edited APPLE-RED", got)
	}
	if !strings.Contains(resp, "APPLE-RED") {
		t.Fatalf("commit result must name the edited SKU: %s", resp)
	}
}

func TestImport_CommitClearedGeneratedSKUFallsBackToAutoGeneration(t *testing.T) {
	mux, dp := newGenSKUMux(t)
	id, _ := previewAndExtractStagedID(t, mux, genSKUCSV)
	commitGenSKU(t, mux, id, map[string]string{"row_sku_0": "", "row_sku_1": "FRU-0002"})
	got := skuByName(t, dp, "Apple")
	if got == "" || got == "FRU-0002" || !strings.HasPrefix(got, "FRU-") {
		t.Fatalf("Apple sku = %q, want a commit-time generated FRU-NNNN distinct from Pear's", got)
	}
}

// A row with its own SKU in the file never takes a submitted row_sku_<i>:
// only rows the preview offered a generated SKU for are editable.
func TestImport_CommitIgnoresSKUFieldForRowWithOwnSKU(t *testing.T) {
	mux, dp := newGenSKUMux(t)
	id, _ := previewAndExtractStagedID(t, mux, genSKUCSV)
	commitGenSKU(t, mux, id, map[string]string{"row_sku_2": "HIJACK"})
	if got := skuByName(t, dp, "Cake"); got != "CK1" {
		t.Fatalf("Cake sku = %q, want its own CK1", got)
	}
}

// The edited SKU survives the currency-confirm detour like the other
// row_* fields (confirmCarriedOverrideField).
func TestImport_GeneratedSKUSurvivesCurrencyConfirmDetour(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	resetStagedCatalog(t)
	dp := newImportTestDepsWithCurrencyState(t, false)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	id, _ := previewAndExtractStagedID(t, mux, genSKUCSV)
	resp := commitGenSKU(t, mux, id, map[string]string{"row_sku_0": "APPLE-RED"})
	if !strings.Contains(resp, `name="confirm_currency"`) {
		t.Fatalf("expected the currency-confirm prompt, got: %s", resp)
	}
	if !strings.Contains(resp, `name="row_sku_0" value="APPLE-RED"`) {
		t.Fatalf("confirm prompt must re-emit row_sku_0: %s", resp)
	}
}
