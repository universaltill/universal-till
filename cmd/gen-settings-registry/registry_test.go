package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/registrydesc"
	"github.com/universaltill/universal-till/internal/uislot"
)

func registryJSONPath(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", settingsRegistryJSONPath)
}

// The committed artefact must match the registry: regenerate with
// `make gen-settings-registry`.
func TestSettingsRegistryJSON_MatchesCommittedArtefact(t *testing.T) {
	want, err := settingsRegistryJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(registryJSONPath(t))
	if err != nil {
		t.Fatalf("read artefact: %v (run `make gen-settings-registry`)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run `make gen-settings-registry` and commit it", settingsRegistryJSONPath)
	}
}

func TestSettingsRegistryJSON_Shape(t *testing.T) {
	raw, err := settingsRegistryJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema  int                          `json:"schema"`
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != 1 || len(doc.Entries) != len(data.AgentSettings) {
		t.Fatalf("schema %d, %d entries", doc.Schema, len(doc.Entries))
	}
	for _, f := range []string{"key", "type", "tier", "secret", "default", "on", "off", "min", "max", "options",
		"label_key", "description_key", "region", "min_till_version", "audit", "writable_by_till_agent", "remote_writable",
		"owner", "scope", "restorable", "restorable_reason", "confirm_by", "step_up", "gate_switch", "class"} {
		if _, ok := doc.Entries[0][f]; !ok {
			t.Errorf("entry lacks field %q", f)
		}
	}
	if strings.Contains(string(raw), `"value"`) {
		t.Error("the artefact must carry no values")
	}
}

// Descriptor fields (design-B RC1) are derived from the registry, additive.
func TestSettingsRegistryJSON_DescriptorFields(t *testing.T) {
	raw, err := settingsRegistryJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Entries []struct {
			Key, Owner, Scope, Tier string
			ConfirmBy               string `json:"confirm_by"`
			GateSwitch              string `json:"gate_switch"`
			Restorable              bool
			RestorableReason        string `json:"restorable_reason"`
			StepUp                  bool   `json:"step_up"`
			RemoteWritable          bool   `json:"remote_writable"`
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	by := map[string]int{}
	for i, e := range doc.Entries {
		by[e.Key] = i
		if e.Owner != "core" {
			t.Errorf("%s owner = %q, want core", e.Key, e.Owner)
		}
		if e.Scope != "shop" && e.Scope != "device" {
			t.Errorf("%s scope = %q", e.Key, e.Scope)
		}
		if e.Restorable && !e.RemoteWritable {
			t.Errorf("%s restorable but not remote-writable", e.Key)
		}
		if e.Restorable && e.RestorableReason != "" {
			t.Errorf("%s restorable with a reason", e.Key)
		}
		if e.Tier == "sensitive" && (e.ConfirmBy != "owner" || !e.StepUp || e.GateSwitch != "allow_sensitive_settings") {
			t.Errorf("%s: sensitive gate wrong: %+v", e.Key, e)
		}
	}
	for _, k := range []string{"store.currency", "shop.type", "report_retention_mode"} {
		i, ok := by[k]
		if !ok {
			continue
		}
		if doc.Entries[i].Restorable || doc.Entries[i].RestorableReason == "" {
			t.Errorf("%s must be restorable=false with a reason: %+v", k, doc.Entries[i])
		}
	}
	if e := doc.Entries[by["kiosk.idle_reset_seconds"]]; !e.Restorable || e.ConfirmBy != "owner_or_manager" || e.StepUp {
		t.Errorf("kiosk.idle_reset_seconds descriptor: %+v", e)
	}
	if strings.Contains(string(raw), `"value"`) {
		t.Error("the artefact must carry no values")
	}
}

// Every artefact row carries the base visibility class of its region (ADR-0190).
func TestSettingsRegistryJSON_ClassMatchesRegion(t *testing.T) {
	raw, err := settingsRegistryJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Entries []struct{ Key, Region, Class string }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, e := range doc.Entries {
		want, ok := registrydesc.ClassForRegion(e.Region)
		if !ok {
			t.Fatalf("%s: region %q has no class", e.Key, e.Region)
		}
		if e.Class != string(want) || !uislot.Class(e.Class).Valid() {
			t.Errorf("%s: class %q, want %q", e.Key, e.Class, want)
		}
	}
	// Sub-region inherits its card: currency display follows settings-currency.
	for _, e := range doc.Entries {
		if e.Region == "settings-currency-display" && e.Class != "essential" {
			t.Errorf("%s: currency display class %q, want essential", e.Key, e.Class)
		}
	}
}

func TestRegistryClass_FailsWhenRegionHasNoCard(t *testing.T) {
	if _, err := registryClass("no-such-card"); err == nil {
		t.Fatal("want an error for a region with no class")
	}
	if c, err := registryClass("settings-currency"); err != nil || c != "essential" {
		t.Fatalf("settings-currency = %q, %v", c, err)
	}
}
