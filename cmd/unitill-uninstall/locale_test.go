package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLocale(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Per-KEY English fallback (the documented choice for this CLI, see
// locale.go): a locale file that exists but lacks one key falls back to
// English for that key only, keeping every key it does have.
func TestTranslatorPerKeyFallback(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en.json", `{"uninstall.a":"A-en","uninstall.b":"B-en"}`)
	writeLocale(t, dir, "fa.json", `{"uninstall.a":"A-fa"}`)

	tr := loadTranslator(dir, "fa")
	if got := tr.T("uninstall.a"); got != "A-fa" {
		t.Errorf("present key: got %q, want fa value", got)
	}
	if got := tr.T("uninstall.b"); got != "B-en" {
		t.Errorf("missing key must fall back to English per-key: got %q", got)
	}
}

// A missing locale FILE falls back to English wholesale, silently.
func TestTranslatorMissingFileFallsBackToEnglish(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en.json", `{"uninstall.a":"A-en"}`)

	tr := loadTranslator(dir, "tr")
	if got := tr.T("uninstall.a"); got != "A-en" {
		t.Errorf("got %q, want English fallback", got)
	}
}

// An unreadable/corrupt locale file must behave like a missing one, never
// crash the uninstaller over a translation.
func TestTranslatorCorruptFileFallsBackToEnglish(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en.json", `{"uninstall.a":"A-en"}`)
	writeLocale(t, dir, "ar.json", `{not json`)

	tr := loadTranslator(dir, "ar")
	if got := tr.T("uninstall.a"); got != "A-en" {
		t.Errorf("got %q, want English fallback", got)
	}
}

// A key absent even from en.json returns the key itself — a visible,
// greppable marker rather than an empty prompt.
func TestTranslatorUnknownKeyReturnsKey(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en.json", `{}`)
	tr := loadTranslator(dir, "en")
	if got := tr.T("uninstall.nope"); got != "uninstall.nope" {
		t.Errorf("got %q, want the key itself", got)
	}
}

// A path-traversal-shaped --lang value must not escape the locale dir.
func TestTranslatorRejectsPathTraversalLang(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en.json", `{"uninstall.a":"A-en"}`)
	outside := filepath.Join(dir, "..", "evil.json")
	if err := os.WriteFile(outside, []byte(`{"uninstall.a":"EVIL"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)

	tr := loadTranslator(dir, "../evil")
	if got := tr.T("uninstall.a"); got != "A-en" {
		t.Errorf("traversal lang must fall back to English, got %q", got)
	}
}

// The shipped locale files must all carry this CLI's whole key namespace —
// guard-i18n.sh enforces cross-locale parity, but only THIS test ties the
// keys the code actually calls T() with to en.json (the CLI has no
// template, so guard check 1 never sees these keys).
// Reads each locale's raw parsed map directly, NOT through translator.T —
// T's per-key English fallback (locale.go) means a key missing from
// ar/fa/tr.json still resolves through the en.json base and would never
// fail here if checked via T, defeating the point of a per-locale-file
// check (review finding, ut-docs#1083). Real cross-locale key-set parity is
// scripts/ci/guard-i18n.sh's job; this test ties the keys the CODE actually
// uses (allUninstallKeys) to what each shipped file actually defines.
func TestShippedLocalesCarryAllUsedKeys(t *testing.T) {
	repoLocales := filepath.Join("..", "..", "web", "locales")
	for _, lang := range []string{"en", "ar", "fa", "tr"} {
		m := readLocaleFile(filepath.Join(repoLocales, lang+".json"))
		if len(m) == 0 {
			t.Fatalf("%s.json: failed to load or empty — check the path/JSON is valid", lang)
		}
		for _, key := range allUninstallKeys {
			if v, ok := m[key]; !ok || v == "" {
				t.Errorf("%s.json: key %q missing or empty", lang, key)
			}
		}
	}
}

// primaryLang maps a configured BCP-47 locale to the locale-file base name.
func TestPrimaryLang(t *testing.T) {
	for in, want := range map[string]string{
		"en-US": "en",
		"fa":    "fa",
		"tr_TR": "tr",
		"AR-EG": "ar",
		"":      "en",
	} {
		if got := primaryLang(in); got != want {
			t.Errorf("primaryLang(%q) = %q, want %q", in, got, want)
		}
	}
}
