package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func classesJSONPath(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", shopTemplateClassesJSONPath)
}

// The committed artefact must match the templates: regenerate with
// `make gen-settings-registry`.
func TestShopTemplateClassesJSON_MatchesCommittedArtefact(t *testing.T) {
	want, err := shopTemplateClassesJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(classesJSONPath(t))
	if err != nil {
		t.Fatalf("read artefact: %v (run `make gen-settings-registry`)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run `make gen-settings-registry` and commit it", shopTemplateClassesJSONPath)
	}
}

type classesDoc struct {
	Schema        int      `json:"schema"`
	ProtectedKeys []string `json:"protected_keys"`
	ShopTypes     map[string]struct {
		RarelyNeededCards []string `json:"rarely_needed_cards"`
		RarelyNeededKeys  []string `json:"rarely_needed_keys"`
	} `json:"shop_types"`
}

func TestShopTemplateClassesJSON_ProtectedNeverDemoted(t *testing.T) {
	raw, err := shopTemplateClassesJSON()
	if err != nil {
		t.Fatal(err)
	}
	var d classesDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Schema != 1 || len(d.ProtectedKeys) == 0 {
		t.Fatalf("schema %d, %d protected keys", d.Schema, len(d.ProtectedKeys))
	}
	for st, tc := range d.ShopTypes {
		for _, k := range tc.RarelyNeededKeys {
			if slices.Contains(d.ProtectedKeys, k) {
				t.Errorf("%s demotes protected key %s", st, k)
			}
		}
	}
}

// retail demotes the order-number card; kiosk idle reset is a card the retail
// template names advanced, so its keys are rarely needed for retail only.
func TestShopTemplateClassesJSON_RetailDemotesOrderNo(t *testing.T) {
	raw, err := shopTemplateClassesJSON()
	if err != nil {
		t.Fatal(err)
	}
	var d classesDoc
	_ = json.Unmarshal(raw, &d)
	r := d.ShopTypes["retail"]
	if !slices.Contains(r.RarelyNeededCards, "settings-order-no") {
		t.Errorf("retail cards = %v, want settings-order-no", r.RarelyNeededCards)
	}
	if len(r.RarelyNeededKeys) == 0 {
		t.Error("retail has demoted cards but no keys")
	}
}

// ADR-0190: a template never hides an auth/payment/fiscal/tax key, even when
// it demotes the card holding it (market_stall demotes settings-idle-lock).
func TestShopTemplateClassesJSON_NeverFlagsSecurityKeys(t *testing.T) {
	raw, err := shopTemplateClassesJSON()
	if err != nil {
		t.Fatal(err)
	}
	var d classesDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	for st, tc := range d.ShopTypes {
		for _, k := range tc.RarelyNeededKeys {
			if neverRarely(k) {
				t.Errorf("%s flags %s as rarely needed", st, k)
			}
		}
	}
	if !neverRarely("auth.idle_lock_minutes") {
		t.Error("auth.idle_lock_minutes must be protected")
	}
}

// The generator must create the output directory before writing.
func TestWriteClassesCreatesDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "docs", "x", "classes.json")
	if err := writeClassesJSON(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}
