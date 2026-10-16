package entitlement

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ADR-0162 §1 (ut-docs#4463): the entitlement block's catalog_master is
// cached with the block and read back fail-safe — only the exact strings
// "cloud" and "adopting" leave "till".

func TestCatalogMasterModeNormalises(t *testing.T) {
	cases := map[string]string{
		"cloud":     CatalogMasterCloud,
		"adopting":  CatalogMasterAdopting,
		"till":      CatalogMasterTill,
		"":          CatalogMasterTill,
		"Cloud":     CatalogMasterTill,
		" cloud":    CatalogMasterTill,
		"cloud ":    CatalogMasterTill,
		"chain":     CatalogMasterTill,
		"adopting!": CatalogMasterTill,
	}
	for raw, want := range cases {
		if got := CatalogMasterMode(raw); got != want {
			t.Errorf("CatalogMasterMode(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestBlockCatalogMasterParsedAndCached(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"plan":"shop","subscription_status":"active","catalog_master":"cloud"}`, "cloud"},
		{`{"plan":"shop","subscription_status":"active","catalog_master":"adopting"}`, "adopting"},
		{`{"plan":"shop","subscription_status":"active","catalog_master":"till"}`, "till"},
		// An older cloud that omits the field, a null, a future value and
		// a wrong type all read as till.
		{`{"plan":"shop","subscription_status":"active"}`, "till"},
		{`{"plan":"shop","subscription_status":"active","catalog_master":null}`, "till"},
		{`{"plan":"shop","subscription_status":"active","catalog_master":"org"}`, "till"},
	} {
		var b Block
		if err := json.Unmarshal([]byte(tc.body), &b); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.body, err)
		}
		kv, err := b.Values(now)
		if err != nil {
			t.Fatalf("Values %s: %v", tc.body, err)
		}
		if kv[KeyCatalogMaster] != tc.want {
			t.Errorf("%s: cached catalog_master = %q, want %q", tc.body, kv[KeyCatalogMaster], tc.want)
		}
		if got := CatalogMaster(context.Background(), mapReader(kv)); got != tc.want {
			t.Errorf("%s: CatalogMaster = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestCatalogMasterReaderFailSafe(t *testing.T) {
	ctx := context.Background()
	if got := CatalogMaster(ctx, mapReader{}); got != CatalogMasterTill {
		t.Fatalf("no cache: %q, want till", got)
	}
	if got := CatalogMaster(ctx, mapReader{KeyCatalogMaster: "CLOUD"}); got != CatalogMasterTill {
		t.Fatalf("garbage cache: %q, want till", got)
	}
	if got := CatalogMaster(ctx, mapReader{KeyCatalogMaster: "cloud"}); got != CatalogMasterCloud {
		t.Fatalf("cloud cache: %q, want cloud", got)
	}
}
