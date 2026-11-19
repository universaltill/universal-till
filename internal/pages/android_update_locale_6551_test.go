package pages

import (
	"encoding/json"
	"os"
	"testing"
)

// ut-docs#6551: every new key is in en.json AND carries en.meta.json
// metadata (code PRs add English only, ADR-0160).
func TestAndroidUpdateVisibilityKeysExist(t *testing.T) {
	keys := []string{
		"status.update_available_ask_manager",
		"settings.update.android.banner_title",
		"settings.update.android.banner_version",
		"settings.update.android.banner_body",
		"settings.update.android.banner_action",
		"settings.update.android.allow_title",
		"settings.update.android.allow_body",
		"settings.update.android.allow_open",
		"settings.update.android.allow_later",
		"settings.update.android.allow_granted",
		"settings.update.android.allow_still_denied",
	}
	load := func(p string) map[string]json.RawMessage {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]json.RawMessage{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	en := load("web/locales/en.json")
	meta := load("web/i18n-meta/en.meta.json")
	for _, k := range keys {
		var s string
		if raw, ok := en[k]; !ok || json.Unmarshal(raw, &s) != nil || s == "" {
			t.Errorf("en.json lacks a non-empty %q", k)
		}
		if _, ok := meta[k]; !ok {
			t.Errorf("en.meta.json lacks %q", k)
		}
	}
}
