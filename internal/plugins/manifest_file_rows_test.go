package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0164 §2 (ut-docs#4526): an import entry may declare "source": "file"
// — a file-rows entry that parses an uploaded file in the import sandbox
// and hands rows back through import_rows_submit. It must name the file
// formats it reads and the items entity, and it claims a generic
// spreadsheet only by its header row (file_headers), never every .csv.
func TestPersistManifest_ImportEntrySourceFile(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	manifest := func(id, entries string) *Manifest {
		t.Helper()
		m, err := ParseManifest(strings.NewReader(`{"id":"` + id + `","name":"File Import","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","entries":[` + entries + `]}`))
		if err != nil {
			t.Fatalf("ParseManifest: %v", err)
		}
		return m
	}
	m := manifest("com.test.filerows", `{"type":"import","key":"export","label":"Back office export","entities":["items","stock"],"source":"file","file_formats":[".csv",".xlsx"],"file_headers":["Product Ref"," Product Title "]},
		{"type":"import","key":"own","label":"Own format","entities":["items"],"source":"file","file_formats":[".abc"]}`)
	if err := PersistManifest(context.Background(), db, m, InstallOptions{}); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}
	rows, err := data.NewPluginRepo(db).ListImportEntries(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListImportEntries = %+v, %v", rows, err)
	}
	byKey := map[string]data.ImportEntryRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if e := byKey["export"]; e.Source != ImportSourceFile || strings.Join(e.FileHeaders, "|") != "Product Ref|Product Title" {
		t.Fatalf("export entry = %+v, want source file and trimmed headers", e)
	}
	if e := byKey["own"]; e.Source != ImportSourceFile || len(e.FileHeaders) != 0 {
		t.Fatalf("own-extension entry = %+v", e)
	}

	long := strings.Repeat("x", FileHeaderMaxBytes+1)
	many := `"a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"`
	for name, entries := range map[string]string{
		"no file formats":       `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file"}`,
		"no items entity":       `{"type":"import","key":"x","label":"X","entities":["customers"],"source":"file","file_formats":[".abc"]}`,
		"generic csv no claim":  `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".csv"]}`,
		"generic xlsx no claim": `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":["XLSX"]}`,
		"core bkp no claim":     `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".bkp"]}`,
		"ods no claim":          `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".ods"]}`,
		"one header":            `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".csv"],"file_headers":["Name"]}`,
		"headers on legacy":     `{"type":"import","key":"x","label":"X","entities":["items"],"file_formats":[".csv"],"file_headers":["A"]}`,
		"headers on api":        `{"type":"import","key":"x","label":"X","entities":["items"],"source":"api","file_headers":["A"]}`,
		"blank header":          `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".csv"],"file_headers":["A","B","  "]}`,
		"long header":           `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".csv"],"file_headers":["A","` + long + `"]}`,
		"neutral headers only":  `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".csv"],"file_headers":["Name","Price"]}`,
		"decorated neutral":     `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".csv"],"file_headers":["SKU "," price [GBP]","Tax rate (%)"]}`,
		"too many headers":      `{"type":"import","key":"x","label":"X","entities":["items"],"source":"file","file_formats":[".csv"],"file_headers":[` + many + `]}`,
	} {
		bad := manifest("com.test.bad"+strings.ReplaceAll(name, " ", ""), entries)
		err := PersistManifest(context.Background(), db, bad, InstallOptions{})
		if err == nil || !(strings.Contains(err.Error(), "source") || strings.Contains(err.Error(), "file_")) {
			t.Errorf("%s: PersistManifest err = %v, want a refusal naming source/file_*", name, err)
		}
	}
}

// Rollback re-validates the same rules (it persists entries too).
func TestValidateImportEntrySource_FileRules(t *testing.T) {
	ok := []ManifestEntry{
		{Type: "import", Key: "a", Entities: []string{"items"}, Source: ImportSourceFile, FileFormats: []string{".csv"}, FileHeaders: []string{"Ref", "Title"}},
		{Type: "import", Key: "b", Entities: []string{"items"}, Source: ImportSourceFile, FileFormats: []string{"abc"}},
		{Type: "import", Key: "c", Entities: []string{"items"}, FileFormats: []string{".bkp"}},
		// Neutral columns are fine beside one vendor-specific column.
		{Type: "import", Key: "d", Entities: []string{"items"}, Source: ImportSourceFile, FileFormats: []string{".csv"}, FileHeaders: []string{"Name", "Price", "Fixture Ref"}},
	}
	if err := validateImportEntrySource(ok); err != nil {
		t.Fatalf("valid entries refused: %v", err)
	}
}
