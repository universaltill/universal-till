package locales

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

// ut-docs#4898: the Settings section is headed settings.data.title ("Data
// management"), so the strings that send the operator there must name it
// the same way the screen and the help manual do.
func TestSettingsDataPointersUseSectionTitle(t *testing.T) {
	keys := []string{
		"elevation.summary.data_reset_transactions",
		"import.sample.already_loaded",
		"import.sample.help",
		"import.sample.present",
		"import.sample.settings_link",
		"import.welcome.sample.body",
		"settings.data.demo_item_has_history",
		"settings.data.demo_kept_reason_history",
		"settings.data.reset_done",
		"settings.language.install_unavailable",
	}
	names, err := fs.Glob(FS, "*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		b, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		title := m["settings.data.title"]
		if title == "" {
			t.Fatalf("%s: settings.data.title missing", name)
		}
		for _, k := range keys {
			if v, ok := m[k]; ok && !strings.Contains(v, title) {
				t.Errorf("%s: %s = %q, want it to name the section %q", name, k, v, title)
			}
		}
	}
}

// Any new English string pointing at the section must use its full name too.
func TestEnglishNeverSaysSettingsDataAlone(t *testing.T) {
	b, err := fs.ReadFile(FS, "en.json")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		if strings.Contains(strings.ReplaceAll(v, "Settings → Data management", ""), "Settings → Data") {
			t.Errorf("en.json %s = %q says \"Settings → Data\"; the section is \"Settings → Data management\"", k, v)
		}
	}
}
