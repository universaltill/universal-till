package pages

import (
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#204: export.requested.ask's request/response shapes are a real
// cross-repo contract between this host (internal/pages/data_api.go) and
// any export/report-type plugin (currently ut-plugin-tax-de) — documented
// field-for-field in ut-docs' reference/plugin-manifest.md. Until this
// file, nothing enforced the two staying in sync except a manual read: a
// Go-side field rename/add/remove could silently drift from the doc, and a
// third-party plugin author reading only the doc would build against a
// contract that no longer matches what the host actually sends.
//
// This test pins the JSON wire shape of every type export.requested.ask
// puts on the wire, by JSON tag, via reflection — not a byte-for-byte
// golden JSON compare (too brittle against unrelated struct-literal churn)
// but a field-set-and-order check, which is exactly what a plugin author
// binding to named JSON fields actually depends on. A failing case here
// means one of two things changed and the other didn't: either
// plugin-manifest.md's contract section, or this list — bring them back in
// sync (and if the change is deliberate, update BOTH in the same commit).
//
// Deliberately NOT covering data.EODReport's own nested fields (TaxBand,
// MethodTaxBand, EODMethod, EODTip, DeptSales, ...): `eod_closes[].report`
// is the archived Z-report format, which predates and is scoped outside
// this event's own contract (see EODCloseExport below) — plugin-manifest.md
// itself treats it the same way, describing it in prose rather than
// enumerating it field-by-field.
func TestExportRequestPayloadSchema_PinnedFields(t *testing.T) {
	assertJSONFields(t, "exportRequestPayload", reflect.TypeOf(exportRequestPayload{}), []string{
		"from", "to", "entry_key", "sales", "stock", "items", "tax_codes", "eod_closes",
	})
}

func TestExportResponseSchema_PinnedFields(t *testing.T) {
	assertJSONFields(t, "exportResponse", reflect.TypeOf(exportResponse{}), []string{
		"ok", "filename,omitempty", "content_b64,omitempty",
		"message,omitempty", "error,omitempty",
	})
}

func TestExportSaleRowSchema_PinnedFields(t *testing.T) {
	assertJSONFields(t, "data.ExportSaleRow", reflect.TypeOf(data.ExportSaleRow{}), []string{
		"receipt_no", "created_at", "total", "tax_lines", "payments",
	})
	assertJSONFields(t, "data.ExportSaleTaxLine", reflect.TypeOf(data.ExportSaleTaxLine{}), []string{
		"rate_bp", "net", "tax",
	})
	assertJSONFields(t, "data.ExportSalePayment", reflect.TypeOf(data.ExportSalePayment{}), []string{
		"method", "amount",
	})
}

func TestExportStockRowSchema_PinnedFields(t *testing.T) {
	assertJSONFields(t, "data.ExportStockRow", reflect.TypeOf(data.ExportStockRow{}), []string{
		"item_id", "name", "sku", "variant_id,omitempty", "variant_name,omitempty",
		"location_id", "location_name", "current_qty", "reorder_level",
	})
}

func TestExportItemRowSchema_PinnedFields(t *testing.T) {
	assertJSONFields(t, "data.ExportRow", reflect.TypeOf(data.ExportRow{}), []string{
		"name", "sku", "barcode", "price_minor", "category", "description",
		"is_weighed", "stock", "is_active", "tax_rate_bp", "has_tax",
		"takeaway_rate_bp", "has_takeaway",
	})
}

func TestExportTaxCodeViewSchema_PinnedFields(t *testing.T) {
	assertJSONFields(t, "data.TaxCodeView", reflect.TypeOf(data.TaxCodeView{}), []string{
		"id", "name", "rate_bp", "takeaway_rate_bp", "is_active",
	})
}

func TestExportEODCloseExportSchema_PinnedFields(t *testing.T) {
	// Only the two fields this event's own contract owns -- report's
	// internal shape is the archived Z-report format, out of scope here
	// (see the file-level doc comment above).
	assertJSONFields(t, "data.EODCloseExport", reflect.TypeOf(data.EODCloseExport{}), []string{
		"z_number", "report",
	})
}

// assertJSONFields walks typ's exported struct fields in declaration order
// and asserts their `json` tag names exactly match want, in the same order.
// An entry in want carries a ",omitempty" suffix exactly when the field's
// tag does: whether a field is omitted-when-empty is itself part of this
// contract, not a formatting detail — plugin-manifest.md documents
// `variant_id`/`variant_name` as absent-on-item-rows *because* they are
// omitempty, and `eod_closes` as deliberately NOT omitempty so that `[]`
// (no closes in range) stays wire-distinguishable from `null` (entity not
// declared / not granted). Adding or dropping the option silently breaks
// either of those documented behaviours without changing a single field
// name, so the pin covers it.
//
// A field tagged `json:"-"` is skipped, matching encoding/json (note the
// `json:"-,"` special case, which names a field literally "-" and is not
// skipped). Unexported fields never reach the wire and are skipped too.
func assertJSONFields(t *testing.T, typeName string, typ reflect.Type, want []string) {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s: assertJSONFields requires a struct type, got %s", typeName, typ.Kind())
	}
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" { // unexported — never marshaled
			continue
		}
		tag, ok := f.Tag.Lookup("json")
		if !ok {
			t.Fatalf("%s: field %s has no `json` tag -- every field on the wire must be explicitly named", typeName, f.Name)
		}
		name, hasOmitempty := splitJSONTag(tag)
		if name == "-" && !strings.Contains(tag, ",") {
			continue
		}
		if name == "" {
			t.Fatalf("%s: field %s has an empty json tag name", typeName, f.Name)
		}
		if hasOmitempty {
			name += ",omitempty"
		}
		got = append(got, name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: wire field set/order changed.\n  got:  %v\n  want: %v\n"+
			"If this is a deliberate contract change, update BOTH this test AND "+
			"ut-docs' reference/plugin-manifest.md's export.requested.ask section "+
			"in the same commit -- a third-party export/report plugin author binds "+
			"to that doc, not to this Go source.", typeName, got, want)
	}
}

// splitJSONTag splits a struct tag's `json:"..."` value into its wire name
// (everything before the first comma) and whether "omitempty" appears among
// the comma-separated options after it — the same split encoding/json does.
func splitJSONTag(tag string) (name string, hasOmitempty bool) {
	name, opts, _ := strings.Cut(tag, ",")
	for opts != "" {
		var opt string
		opt, opts, _ = strings.Cut(opts, ",")
		if opt == "omitempty" {
			hasOmitempty = true
		}
	}
	return name, hasOmitempty
}
