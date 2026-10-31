package pages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4129 guard: the assistant knows ALL settings. A Settings region
// without an agent description (a data.AgentSettings entry), a description
// whose words are missing in a locale, or a link target that is not in the
// template fails here, so coverage cannot drift.
func TestAgentSettingsCatalogue_CoversEverySettingsRegion_4129(t *testing.T) {
	chdirRoot(t)
	covered := map[string]bool{}
	for region, key := range shopWideLockRegions {
		s, ok := data.LookupAgentSetting(key)
		if !ok {
			t.Errorf("Settings region %s (key %s) has no agent description: add it to data.AgentSettings (writable only if ADR-0153 Decision 3 allows)", region, key)
			continue
		}
		covered[s.Key] = true
	}
	for _, s := range data.AgentSettings {
		if !covered[s.Key] {
			t.Errorf("catalogue entry %s matches no shopWideLockRegions key", s.Key)
		}
	}
}

func TestAgentSettingsCatalogue_LinksAndWordsExist_4129(t *testing.T) {
	chdirRoot(t)
	tpl, err := os.ReadFile("web/ui/pages/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("web/locales/*.json")
	if len(files) < 4 {
		t.Fatalf("found %d locale files", len(files))
	}
	locales := map[string]map[string]string{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		locales[filepath.Base(f)] = m
	}
	for _, s := range data.AgentSettings {
		if !strings.Contains(string(tpl), `id="`+s.Region+`"`) {
			t.Errorf("%s: deep-link target id=%q is not in settings.html", s.Key, s.Region)
		}
		if s.LabelKey == "" {
			t.Errorf("%s: no label key", s.Key)
		}
		for _, k := range []string{s.LabelKey, s.HintKey} {
			if k == "" {
				continue
			}
			for name, m := range locales {
				if strings.TrimSpace(m[k]) == "" {
					t.Errorf("%s: key %s missing in %s", s.Key, k, name)
				}
			}
		}
		if s.Writable {
			// The audit event must be the one the manual screen writes.
			src, err := os.ReadFile("internal/pages/settings_page.go")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(src), `"`+s.Audit+`"`) {
				t.Errorf("%s: audit action %q is not written by settings_page.go", s.Key, s.Audit)
			}
		}
	}
}
