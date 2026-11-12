package entitlement

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ut-docs#6088: the cloud advertises the catalogue fields its change upload
// accepts in the sync answer's entitlement block (catalog_changes.fields).
// The till caches a canonical, validated copy with the rest of the block;
// anything unreadable caches "" (contract 1.0.0) and never rejects the block.

func TestBlockCatalogChangesFields(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	const base = `"plan":"shop","subscription_status":"active"`
	long := strings.Repeat("a", 65)
	var manyKinds, manyFields []string
	for i := 0; i < 17; i++ {
		manyKinds = append(manyKinds, `"k`+strings.Repeat("a", i)+`":["x"]`)
	}
	// Distinct names: 65 fields.
	for i := 0; i < 65; i++ {
		manyFields = append(manyFields, `"f`+string(rune('a'+i%26))+string(rune('a'+i/26))+`"`)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"absent", `{` + base + `}`, ""},
		{"null", `{` + base + `,"catalog_changes":null}`, ""},
		{"wrong type", `{` + base + `,"catalog_changes":"item"}`, ""},
		{"fields wrong type", `{` + base + `,"catalog_changes":{"fields":[1]}}`, ""},
		{"no fields key", `{` + base + `,"catalog_changes":{"x":1}}`, ""},
		{"kind value wrong type", `{` + base + `,"catalog_changes":{"fields":{"item":"name"}}}`, ""},
		{"field not a string", `{` + base + `,"catalog_changes":{"fields":{"item":["name",1]}}}`, ""},
		{"bad name uppercase", `{` + base + `,"catalog_changes":{"fields":{"item":["Name"]}}}`, ""},
		{"bad name leading digit", `{` + base + `,"catalog_changes":{"fields":{"item":["1name"]}}}`, ""},
		{"bad name too long", `{` + base + `,"catalog_changes":{"fields":{"item":["` + long + `"]}}}`, ""},
		{"bad kind name", `{` + base + `,"catalog_changes":{"fields":{"Item":["name"]}}}`, ""},
		{"too many kinds", `{` + base + `,"catalog_changes":{"fields":{` + strings.Join(manyKinds, ",") + `}}}`, ""},
		{"too many fields", `{` + base + `,"catalog_changes":{"fields":{"item":[` + strings.Join(manyFields, ",") + `]}}}`, ""},
		{"valid canonicalised", `{` + base + `,"catalog_changes":{"fields":{"item":["sku","name","name"],"category":["name"]}}}`,
			`{"category":["name"],"item":["name","sku"]}`},
		{"extra keys ignored", `{` + base + `,"catalog_changes":{"v":2,"fields":{"tax_code":["active"]}}}`, `{"tax_code":["active"]}`},
	} {
		var b Block
		if err := json.Unmarshal([]byte(tc.body), &b); err != nil {
			t.Fatalf("%s: unmarshal rejected the block: %v", tc.name, err)
		}
		kv, err := b.Values(now)
		if err != nil {
			t.Fatalf("%s: Values: %v", tc.name, err)
		}
		got, ok := kv[KeyCatalogChangesFields]
		if !ok {
			t.Errorf("%s: %s not written: a cloud that stops sending it must clear the cache", tc.name, KeyCatalogChangesFields)
		}
		if got != tc.want {
			t.Errorf("%s: cached = %q, want %q", tc.name, got, tc.want)
		}
		if read := CatalogChangesFields(context.Background(), mapReader(kv)); read != tc.want {
			t.Errorf("%s: CatalogChangesFields = %q, want %q", tc.name, read, tc.want)
		}
	}
}

func TestCatalogChangesFieldsNoCacheIsEmpty(t *testing.T) {
	if got := CatalogChangesFields(context.Background(), mapReader(map[string]string{})); got != "" {
		t.Fatalf("no cache = %q, want empty (1.0.0)", got)
	}
}
