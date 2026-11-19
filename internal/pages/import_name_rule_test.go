package pages

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/catimport"
	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
)

// ut-docs#5908: the legacy CSV import applies the single-line name rule to the
// item name, department and category in Preview and Commit.

const (
	wantNameReason = "name has a line break, tab or hidden character — retype it in the file and upload again"
	wantDeptReason = "department name has a line break, tab or hidden character — retype it in the file and upload again"
	wantCatReason  = "category name has a line break, tab or hidden character — retype it in the file and upload again"
)

// nameRuleCSV writes rows (Name,SKU,Price,Department,Category,Tax rate) with
// encoding/csv so embedded LF/TAB/CRLF are quoted exactly as Excel would.
func nameRuleCSV(t *testing.T, rows [][]string) string {
	t.Helper()
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"Name", "SKU", "Price", "Department", "Category", "Tax rate"})
	for _, r := range rows {
		_ = w.Write(r)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func nameRuleRun(t *testing.T, mux *http.ServeMux, csvText string, commit bool) string {
	t.Helper()
	fields := map[string]string{}
	if commit {
		fields["commit"] = "1"
	}
	body, ct := multipartFile(t, "items.csv", []byte(csvText), fields)
	rec := postImport(t, mux, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func nameRuleCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nameRuleSetup(t *testing.T) (*http.ServeMux, *sql.DB) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)
	return mux, dp.Db
}

func TestImport_NameRule_PreviewAndCommit(t *testing.T) {
	mux, db := nameRuleSetup(t)
	fields := []struct {
		field  string
		reason string
		row    func(in, sku string) []string
		table  string
		col    string
	}{
		{"name", wantNameReason, func(in, sku string) []string { return []string{in, sku, "1.00", "", "Cat", "13.7"} }, "items", "name"},
		{"department", wantDeptReason, func(in, sku string) []string { return []string{"Item " + sku, sku, "1.00", in, "Cat", "13.7"} }, "categories", "name"},
		{"category", wantCatReason, func(in, sku string) []string { return []string{"Item " + sku, sku, "1.00", "Dept", in, "13.7"} }, "categories", "name"},
	}
	n := 0
	for _, f := range fields {
		for _, c := range namecases.Cases() {
			n++
			sku := "NR" + string(rune('A'+n/26)) + string(rune('A'+n%26))
			csvText := nameRuleCSV(t, [][]string{f.row(c.In, sku)})

			prev := nameRuleRun(t, mux, csvText, false)
			hasReason := strings.Contains(prev, f.reason)
			if c.OK == hasReason {
				t.Fatalf("%s/%s preview: ok=%v but reason shown=%v", f.field, c.Label, c.OK, hasReason)
			}
			if !c.OK && strings.Contains(prev, "row_include_0") {
				t.Fatalf("%s/%s: refused row must offer no include checkbox", f.field, c.Label)
			}

			items, cats, moves, taxes := nameRuleCount(t, db, "items"), nameRuleCount(t, db, "categories"),
				nameRuleCount(t, db, "stock_movements"), nameRuleCount(t, db, "tax_codes")
			nameRuleRun(t, mux, csvText, true)
			if c.OK {
				var got int
				if err := db.QueryRow(`SELECT COUNT(*) FROM `+f.table+` WHERE `+f.col+` = ?`, c.Want).Scan(&got); err != nil || got == 0 {
					t.Fatalf("%s/%s commit: trimmed %q not stored (%v)", f.field, c.Label, c.Want, err)
				}
				continue
			}
			if a := nameRuleCount(t, db, "items"); a != items {
				t.Fatalf("%s/%s: refused row created an item", f.field, c.Label)
			}
			if a := nameRuleCount(t, db, "categories"); a != cats {
				t.Fatalf("%s/%s: refused row created a category", f.field, c.Label)
			}
			if a := nameRuleCount(t, db, "stock_movements"); a != moves {
				t.Fatalf("%s/%s: refused row moved stock", f.field, c.Label)
			}
			if a := nameRuleCount(t, db, "tax_codes"); a != taxes {
				t.Fatalf("%s/%s: refused row created a tax code", f.field, c.Label)
			}
		}
	}
}

func TestImport_NameRule_QuotedCRLFInCell(t *testing.T) {
	mux, db := nameRuleSetup(t)
	csvText := nameRuleCSV(t, [][]string{
		{"Flat\r\nWhite", "Q1", "1.00", "", "Hot", ""},
		{"Latte", "Q2", "1.00", "", "Drinks", ""},
		{"Mocha", "Q3", "1.00", "", "Drinks\r\n", ""},
	})
	if !strings.Contains(nameRuleRun(t, mux, csvText, false), wantNameReason) {
		t.Fatal("embedded CRLF in a quoted name cell must be an error row")
	}
	nameRuleRun(t, mux, csvText, true)
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM categories WHERE name = 'Drinks'`).Scan(&n)
	if n != 1 {
		t.Fatalf("Drinks and Drinks\\r\\n must resolve to one category, got %d", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM items WHERE name IN ('Latte','Mocha')`).Scan(&n)
	if n != 2 {
		t.Fatalf("accepted rows not imported: %d", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM items WHERE name LIKE 'Flat%'`).Scan(&n)
	if n != 0 {
		t.Fatal("refused row was imported")
	}
}

func TestImport_NameRule_OnlyRefusedCharIsNotMissingName(t *testing.T) {
	mux, _ := nameRuleSetup(t)
	prev := nameRuleRun(t, mux, nameRuleCSV(t, [][]string{
		{"\x1b", "E1", "1.00", "", "", ""},
		{"  ", "E2", "1.00", "", "", ""},
	}), false)
	if !strings.Contains(prev, wantNameReason) {
		t.Fatalf("lone ESC name must show the invalid-characters reason: %s", prev)
	}
	if !strings.Contains(prev, "missing name") {
		t.Fatal("blank name must keep the missing name reason")
	}
}

func TestImport_NameRule_NestedAndSharedRefusal(t *testing.T) {
	mux, db := nameRuleSetup(t)
	csvText := nameRuleCSV(t, [][]string{
		{"A", "N1", "1.00", "Food", "Bad\tCat", ""},
		{"B", "N2", "1.00", "Bad\tDept", "Okay", ""},
		{"C", "N3", "1.00", "", "Bad\tCat", ""},
		{"D", "N4", "1.00", "", "Bad\tCat", ""},
		{"Good", "N5", "1.00", "", "Fine", ""},
	})
	prev := nameRuleRun(t, mux, csvText, false)
	if strings.Count(prev, wantCatReason) != 3 || strings.Count(prev, wantDeptReason) != 1 {
		t.Fatalf("want 3 category + 1 department reasons: %s", prev)
	}
	nameRuleRun(t, mux, csvText, true)
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM items WHERE name IN ('A','B','C','D')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d refused rows created (no shallower fallback allowed)", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM categories WHERE name IN ('Food','Okay')`).Scan(&n)
	if n != 0 {
		t.Fatalf("a valid sibling level of a refused path was created: %d", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM items WHERE name = 'Good'`).Scan(&n)
	if n != 1 {
		t.Fatal("one bad row must not block the rest of the file")
	}
}

func TestImport_NameRule_ForceableRowWithBadNameStaysSkipped(t *testing.T) {
	mux, db := nameRuleSetup(t)
	resetStagedCatalog(t)
	csvText := nameRuleCSV(t, [][]string{{"Flat\tWhite", "F1", "not-a-price", "", "", ""}})
	id, resp := previewAndExtractStagedID(t, mux, csvText)
	if !strings.Contains(resp, wantNameReason) {
		t.Fatalf("reason must name the name problem: %s", resp)
	}
	body, ct := multipartFields(t, map[string]string{
		"commit": "1", "staged_id": id, "row_include_0": "1", "row_price_0": "2.00",
	})
	postImport(t, mux, body, ct)
	if n := nameRuleCount(t, db, "items"); n != 0 {
		t.Fatalf("forged include imported a refused-name row (%d items)", n)
	}
}

func TestImport_ProblemGrid_CorrectedNameRefused(t *testing.T) {
	mux, db := nameRuleSetup(t)
	resetStagedCatalog(t)
	id, _ := previewAndExtractStagedID(t, mux, problemCSV)
	body, ct := multipartFields(t, map[string]string{
		"commit": "1", "staged_id": id, "row_include_0": "1", "row_name_0": "Flat\tWhite",
	})
	rec := postImport(t, mux, body, ct)
	if !strings.Contains(rec.Body.String(), "still skipped: the corrected name has a tab or hidden character — retype it instead of pasting") {
		t.Fatalf("missing refused-correction note: %s", rec.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM items WHERE name LIKE 'Flat%'`).Scan(&n)
	if n != 0 {
		t.Fatal("refused corrected name was imported")
	}
}

// With the preview gate stubbed out, the commit's own defence must map the
// rule's refusal to the translated reasons, never the generic *_failed ones.
func TestImport_CommitMapsInvalidNameFromEnsureAndCreate(t *testing.T) {
	mux, db := nameRuleSetup(t)
	old := importNameRuleReasonFn
	importNameRuleReasonFn = func(func(string) string, catimport.ImportItem) string { return "" }
	t.Cleanup(func() { importNameRuleReasonFn = old })

	for _, tc := range []struct {
		row  []string
		want string
	}{
		{[]string{"A\tB", "M1", "1.00", "", "", ""}, wantNameReason},
		{[]string{"Item", "M2", "1.00", "Bad\tDept", "", ""}, wantDeptReason},
		{[]string{"Item", "M3", "1.00", "", "Bad\tCat", ""}, wantCatReason},
	} {
		out := nameRuleRun(t, mux, nameRuleCSV(t, [][]string{tc.row}), true)
		if !strings.Contains(out, tc.want) {
			t.Fatalf("want %q in: %s", tc.want, out)
		}
		for _, bad := range []string{"could not be created"} {
			if strings.Contains(out, bad) {
				t.Fatalf("generic failure reason %q leaked: %s", bad, out)
			}
		}
	}
	if n := nameRuleCount(t, db, "items"); n != 0 {
		t.Fatalf("%d items created", n)
	}
}

func TestImport_NameRule_RefusedValueIsEscaped(t *testing.T) {
	mux, _ := nameRuleSetup(t)
	out := nameRuleRun(t, mux, nameRuleCSV(t, [][]string{{"<b>x\x1b", "X1", "1.00", "", "", ""}}), false)
	if strings.Contains(out, "<b>x") || !strings.Contains(out, "&lt;b&gt;x") {
		t.Fatalf("refused name must be HTML-escaped: %s", out)
	}
}

// AC10 (tester addition): refused rows count as skipped on preview and are
// never counted as created on commit.
func TestImport_NameRule_SummaryCounts(t *testing.T) {
	mux, _ := nameRuleSetup(t)
	csvText := nameRuleCSV(t, [][]string{
		{"Good one", "S1", "1.00", "", "Fine", ""},
		{"Bad\tName", "S2", "1.00", "", "Fine", ""},
		{"Good two", "S3", "1.00", "Bad‮Dept", "", ""},
		{"Good three", "S4", "1.00", "", "Bad\r\nCat", ""},
		{"Good four", "S5", "1.00", "", "", ""},
	})
	prev := nameRuleRun(t, mux, csvText, false)
	if !strings.Contains(prev, "2 ready to import, 3 will be skipped") {
		t.Fatalf("preview counts wrong: %s", prev)
	}
	out := nameRuleRun(t, mux, csvText, true)
	if !strings.Contains(out, "Imported: 2 —") || !strings.Contains(out, "Skipped: 3") {
		t.Fatalf("commit counts wrong: %s", out)
	}
}
