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
		"label_key", "description_key", "region", "min_till_version", "audit", "writable_by_till_agent", "remote_writable"} {
		if _, ok := doc.Entries[0][f]; !ok {
			t.Errorf("entry lacks field %q", f)
		}
	}
	if strings.Contains(string(raw), `"value"`) {
		t.Error("the artefact must carry no values")
	}
}
