package pages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
	// Registry entries (ut-docs#5349) beyond the lock regions: keys the
	// cloud may write whose cards are not shop-wide-lock fieldsets.
	registryOnly := map[string]bool{
		"printer.receipt_policy": true, "receipt.header1": true, "receipt.header2": true, "receipt.header3": true,
		"receipt.footer": true, "invoice.seller_address": true, "invoice.seller_vat_no": true,
	}
	for _, s := range data.AgentSettings {
		if !covered[s.Key] && !registryOnly[s.Key] {
			t.Errorf("catalogue entry %s matches no shopWideLockRegions key and is not a listed registry-only key", s.Key)
		}
	}
}

// settingsCardsWithoutRegistryKey is the shrink-only baseline of Settings
// cards that carry no registry entry yet (ut-docs#5349 slice 1): action or
// status cards with no settings key, and settings (appearance, backup
// schedule, till roles) that slice 2 ("remaining entries by region") adds.
// A NEW card must get a registry entry, not an entry here.
var settingsCardsWithoutRegistryKey = map[string]string{
	"subscription":         "billing: tier never, not a settings key",
	"settings-issuereport": "action card (bug report), no key",
	"settings-diagnostics": "slice 2: diagnostics consent",
	"settings-menulayout":  "slice 2: menu layout",
	"settings-about":       "read-only version info",
	"settings-theme":       "slice 2: appearance",
	"settings-display":     "slice 2: appearance",
	"settings-backup":      "slice 2: backup schedule (destinations are sensitive)",
	"settings-data":        "action card (import/export), no key",
	"settings-tills":       "slice 2: till roles and names (cloud pairing is tier never)",
	"settings-staff-links": "navigation card, no key",
	"settings-all":         "raw key/value editor; every key it can write is gated by the registry elsewhere",
}

// ut-docs#5349: every Settings card (class "card" with an id) has a registry
// entry, or is in the shrink-only baseline above with a reason.
func TestSettingsCards_EachHasARegistryEntry_5349(t *testing.T) {
	chdirRoot(t)
	tpl, err := os.ReadFile("web/ui/pages/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	regions := map[string]bool{}
	for _, s := range data.AgentSettings {
		regions[s.Region] = true
	}
	cards := map[string]bool{}
	for _, m := range regexp.MustCompile(`<div class="card[^"]*" id="([a-z0-9-]+)"`).FindAllStringSubmatch(string(tpl), -1) {
		cards[m[1]] = true
		if !regions[m[1]] && settingsCardsWithoutRegistryKey[m[1]] == "" {
			t.Errorf("Settings card %s has no registry entry: add one to data.AgentSettings with a tier", m[1])
		}
		if regions[m[1]] && settingsCardsWithoutRegistryKey[m[1]] != "" {
			t.Errorf("card %s now has a registry entry: delete it from settingsCardsWithoutRegistryKey", m[1])
		}
	}
	for id := range settingsCardsWithoutRegistryKey {
		if !cards[id] {
			t.Errorf("baseline names card %s, which is not in settings.html any more", id)
		}
	}
	if len(cards) < 25 {
		t.Fatalf("found only %d cards: the card regexp drifted", len(cards))
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
		if s.Page != "" {
			// Not on /settings: the deep link is the page itself.
			if s.Region == "" || s.Page[0] != '/' {
				t.Errorf("%s: Page %q needs a leading slash and a Region", s.Key, s.Page)
			}
		} else if !strings.Contains(string(tpl), `id="`+s.Region+`"`) {
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
