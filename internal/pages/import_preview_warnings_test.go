package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// assertPreviewRowWarns checks that a Preview response carries want on the
// row naming rowName, and that the row has the warned (⚠ row-warn)
// treatment rather than a plain "ok" (ut-docs#4520).
func assertPreviewRowWarns(t *testing.T, resp, rowName, want string) {
	t.Helper()
	i := strings.Index(resp, want)
	if i < 0 {
		t.Fatalf("preview should warn %q, got: %s", want, resp)
	}
	rowStart := strings.LastIndex(resp[:i], "<tr")
	if rowStart < 0 || !strings.HasPrefix(resp[rowStart:], `<tr class="row-warn">`) ||
		!strings.Contains(resp[rowStart:i], rowName) {
		t.Fatalf("the warning must sit on %s's row-warn row, got row: %s", rowName, resp[max(rowStart, 0):i])
	}
}

func postImportPreview(t *testing.T, mux *http.ServeMux, filename string, file []byte) string {
	t.Helper()
	body, ct := multipartFile(t, filename, file, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestImport_PreviewWarnsParseTimeIssues pins ut-docs#4520: every
// non-blocking issue catimport already knows at parse time shows on the
// Preview as a warned row with the commit path's own text — not a plain
// "ok" the operator only learns about after committing.
func TestImport_PreviewWarnsParseTimeIssues(t *testing.T) {
	cases := []struct {
		name    string
		csv     string
		narrow  bool // disable the permissive barcode catch-alls
		rowName string
		want    string
	}{
		{
			name:    "unparseable tax rate",
			csv:     "Name,SKU,Price,Tax rate,Takeaway tax\nOddball,T8,1.00,abc,\n",
			rowName: "Oddball",
			want:    `could not be read as a percentage`,
		},
		{
			name:    "unparseable takeaway tax rate",
			csv:     "Name,SKU,Price,Tax rate,Takeaway tax\nTo go,T9,1.00,19,xyz\n",
			rowName: "To go",
			want:    `tax rate &quot;xyz&quot; not imported: could not be read as a percentage`,
		},
		{
			name:    "invalid net quantity",
			csv:     "Name,SKU,Price,Net quantity,Net quantity unit\nFlour,NQ9,1.20,1500,kg\n",
			rowName: "Flour",
			want:    `1500 kg`,
		},
		{
			name:    "barcode matching no enabled symbology",
			csv:     "Name,SKU,Barcode,Price,Category,In stock\nBananas,PLU4011,4011,0.59,Produce,0\n",
			narrow:  true,
			rowName: "Bananas",
			want:    `not imported`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UT_AUTH", "off")
			dp := newImportTestDeps(t)
			initAuthTestI18n(t)
			if tc.narrow {
				if err := data.NewSettingsRepo(dp.Db).SetEnabledBarcodeSymbologies(t.Context(),
					[]string{"EAN13", "EAN8", "UPCA", "UPCE", "GTIN14"}); err != nil {
					t.Fatalf("set enabled symbologies: %v", err)
				}
			}
			mux := http.NewServeMux()
			registerImport(mux, dp)

			resp := postImportPreview(t, mux, "items.csv", []byte(tc.csv))
			assertPreviewRowWarns(t, resp, tc.rowName, tc.want)

			var n int
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("a preview must not write items (n=%d err=%v)", n, err)
			}
		})
	}
}

// TestImport_BkpPreviewWarnsUnresolvedImage is the ImageIssue case of
// ut-docs#4520: a .bkp row whose photo reference resolves to nothing is
// flagged on the Preview, not only after commit.
func TestImport_BkpPreviewWarnsUnresolvedImage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	dbBytes := buildBkpDBBytesWithImagePath(t, "images/does-not-exist.jpg")
	zipBytes := zipBkpEntries(t, dbBytes) // no documents.zip at all
	resp := postImportPreview(t, mux, "backup.bkp", zipBytes)
	if !strings.Contains(resp, `<tr class="row-warn">`) {
		t.Fatalf("preview row with a dangling image must be warned, got: %s", resp)
	}
	i := strings.Index(resp, "does-not-exist.jpg")
	if i < 0 {
		t.Fatalf("preview should name the dangling image path, got: %s", resp)
	}
	rowStart := strings.LastIndex(resp[:i], "<tr")
	if rowStart < 0 || !strings.HasPrefix(resp[rowStart:], `<tr class="row-warn">`) {
		t.Fatalf("the image warning must sit on a row-warn row, got: %s", resp[max(rowStart, 0):i])
	}
}

// TestImport_PreviewCleanRowStaysOK guards the other side: a row with no
// parse-time issue keeps its plain "ok" status and no warned treatment.
func TestImport_PreviewCleanRowStaysOK(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	resp := postImportPreview(t, mux, "items.csv",
		[]byte("Name,SKU,Price,Tax rate\nPlain,P1,1.00,19\n"))
	if strings.Contains(resp, `class="row-warn"`) {
		t.Fatalf("a clean row must not be warned, got: %s", resp)
	}
}

// TestParseTimeImportWarnings pins the one composer both the Preview and
// the commit loop use (ut-docs#4520 AC2): its order, and that a barcode
// that did parse never also reports a BarcodeIssue.
func TestParseTimeImportWarnings(t *testing.T) {
	T := func(k string) string { return k + "<%s>" }
	it := catimport.ImportItem{
		ImageIssue: catimport.ImageIssueUnresolved, ImageIssueRaw: "img",
		BarcodeIssue: catimport.BarcodeIssueNoSymbologyMatch, BarcodeIssueRaw: "bc",
		TaxIssue: catimport.TaxIssueUnparseable, TaxIssueRaw: "tax",
		TakeawayTaxIssue: catimport.TaxIssueUnparseable, TakeawayTaxIssueRaw: "ta",
		SKUIssue: catimport.SKUIssueDuplicateInFile, SKUIssueRaw: "sku", SKU: "sku-2",
		NetQuantityIssue: catimport.NetQuantityIssueInvalid, NetQuantityIssueRaw: "nq",
	}
	got := parseTimeImportWarnings(T, it)
	wantSubs := []string{"img", "bc", "tax", "ta", "sku", "nq"}
	if len(got) != len(wantSubs) {
		t.Fatalf("want %d warnings, got %d: %q", len(wantSubs), len(got), got)
	}
	for i, sub := range wantSubs {
		if !strings.Contains(got[i], sub) {
			t.Errorf("warning %d = %q, want it to carry %q", i, got[i], sub)
		}
	}

	it.Barcode = "4006381333931"
	for _, w := range parseTimeImportWarnings(T, it) {
		if strings.Contains(w, "bc") {
			t.Errorf("a parsed barcode must not also warn its BarcodeIssue, got %q", w)
		}
	}

	if ws := parseTimeImportWarnings(T, catimport.ImportItem{Name: "clean"}); len(ws) != 0 {
		t.Errorf("a clean row has no parse-time warnings, got %q", ws)
	}
}

// TestImport_PreviewAndCommitShowTheSameParseTimeWarning pins the commit
// side of ut-docs#4520 AC2: the commit summary carries the exact warning
// text the Preview showed, from the same composer.
func TestImport_PreviewAndCommitShowTheSameParseTimeWarning(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Tax rate,Takeaway tax\nTo go,T9,1.00,19,xyz\n"
	want := `tax rate &quot;xyz&quot; not imported: could not be read as a percentage`

	preview := postImportPreview(t, mux, "items.csv", []byte(csv))
	assertPreviewRowWarns(t, preview, "To go", want)

	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	assertPreviewRowWarns(t, rec.Body.String(), "To go", want)
	if n := strings.Count(rec.Body.String(), "xyz"); n != 1 {
		t.Fatalf("the commit summary must report the warning once, got %d", n)
	}
}
