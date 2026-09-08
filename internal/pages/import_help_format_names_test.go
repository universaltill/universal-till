package pages

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/web/locales"
)

// namedAutoDetectedFormats is the list of source systems import.help/
// import.file are allowed to claim are auto-detected. This is deliberately
// a plain, hand-maintained list, not derived from catimport.DetectFormat's
// source at runtime (that would just move the risk of silent drift from
// "the copy" to "this list") -- the point is that adding a new detectable
// format (or removing one) requires a deliberate, reviewed edit HERE too,
// the same "ratchet" convention the language packs' own
// i18n-baseline/*.untranslated.txt files already use. "generic"/
// "generic-erp" are catimport.DetectFormat's fallback buckets, not named
// systems a merchant would recognise, so they're deliberately excluded.
//
// Disclosed limitation (independent review, ut-docs#1836): this only
// catches a name already in this list going missing from the copy -- it
// does NOT mechanically catch the opposite drift, a brand new
// DetectFormat branch added with no matching entry here and no copy
// update. That direction still relies on the same reviewer/dev diligence
// this card exists to backstop, just relocated one file over. Adding a
// new named format to DetectFormat should always mean a matching edit to
// this list AND the copy in the same change.
var namedAutoDetectedFormats = []string{"Loyverse", "Square", "SumUp"}

// TestImportHelpCopy_NamesEveryAutoDetectedFormat is ut-docs#1836's guard:
// import.help named Loyverse and Square as auto-detected but not SumUp
// (added by #581, the help text never updated) -- in EVERY locale,
// including English, the source of truth every translation is checked
// against. Existing i18n gates (guard-i18n.sh) only check KEY parity, not
// whether a translated VALUE still states the same facts as English -- a
// translation can drop a clause entirely and stay perfectly "complete" by
// that measure (the German pack's import.help/import.file, fixed in the
// same change as this test, is exactly that case). This test can't reach
// into the external ut-plugin-language-* pack repos (separate repos), but
// closes the gap for every locale THIS repo ships and controls.
func TestImportHelpCopy_NamesEveryAutoDetectedFormat(t *testing.T) {
	entries, err := locales.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read locales dir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := locales.FS.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", e.Name(), err)
		}
		help, ok := m["import.help"]
		if !ok {
			t.Errorf("%s: missing import.help key entirely", e.Name())
			continue
		}
		for _, name := range namedAutoDetectedFormats {
			if !strings.Contains(help, name) {
				t.Errorf("%s: import.help does not mention %q as an auto-detected format:\n%s", e.Name(), name, help)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no locale files found under web/locales -- test is not exercising anything")
	}
}

// TestImportFileFormats_MatchTheRealFilePicker guards the OTHER half of
// ut-docs#1836: the file input's own accept="" attribute (what the browser
// will actually let a merchant pick) is the ground truth for which
// extensions the till really accepts. import.file's copy must mention every
// extension that attribute lists beyond plain CSV -- concretely, ".bkp" --
// in English, so a locale drifting away from English (the German pack's
// import.file dropped it down to "(CSV)" with no .bkp at all) is at least
// caught for the base locale this repo ships. Checked against BOTH
// import.html and setup.html, which the bug report showed carry the
// identical accept list -- if they ever diverge, that's worth knowing too.
func TestImportFileFormats_MatchTheRealFilePicker(t *testing.T) {
	chdirRoot(t)

	const marker = `accept="`
	for _, path := range []string{"web/ui/pages/import.html", "web/ui/pages/setup.html"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		html := string(raw)
		idx := strings.Index(html, marker)
		if idx == -1 {
			t.Fatalf("%s: no accept=\"...\" attribute found on the import file input -- did the markup change shape?", path)
		}
		rest := html[idx+len(marker):]
		end := strings.Index(rest, `"`)
		if end == -1 {
			t.Fatalf("%s: unterminated accept attribute", path)
		}
		accept := rest[:end]
		if !strings.Contains(accept, ".bkp") {
			t.Errorf("%s: accept attribute %q no longer lists .bkp -- import.file's copy claiming .bkp support would now be a lie, not just a translation gap", path, accept)
		}
	}

	// en.json is the source of truth checked here; every other core
	// locale's import.file key presence is already covered by
	// guard-i18n.sh's key-parity check, and this file's sibling test above
	// covers cross-locale VALUE fidelity for import.help.
	data, err := locales.FS.ReadFile("en.json")
	if err != nil {
		t.Fatalf("read en.json: %v", err)
	}
	var en map[string]string
	if err := json.Unmarshal(data, &en); err != nil {
		t.Fatalf("unmarshal en.json: %v", err)
	}
	if !strings.Contains(en["import.file"], ".bkp") {
		t.Errorf("en.json: import.file does not mention .bkp, but the file picker accepts it: %q", en["import.file"])
	}
}
