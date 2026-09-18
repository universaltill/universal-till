package pages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/web/locales"
)

// TestLoadTestI18nOverlays_LoadsEveryJSONFileKeyedByStem covers the
// UT_TEST_I18N_OVERLAY_DIR escape hatch (ut-docs#2300): every *.json file in
// the named directory becomes an overlay for the locale named by its
// filename stem, and I18n.T resolves through it exactly as it would a real
// language-pack plugin's overlay (pm.SetLocalizer's normal production path).
func TestLoadTestI18nOverlays_LoadsEveryJSONFileKeyedByStem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fr.json"), []byte(`{"nav.sell":"Vendre"}`), 0o644); err != nil {
		t.Fatalf("write fr.json fixture: %v", err)
	}

	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}

	if err := loadTestI18nOverlays(i18n, dir); err != nil {
		t.Fatalf("loadTestI18nOverlays: %v", err)
	}

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q, want Verkaufen", got)
	}
	if got := i18n.T("fr", "nav.sell"); got != "Vendre" {
		t.Fatalf("T(fr, nav.sell) = %q, want Vendre", got)
	}
}

// TestLoadTestI18nOverlays_CollectsAllFilesBeforeOneSetOverlaysCall guards
// the exact bug the card calls out: config.I18n.SetOverlays atomically
// REPLACES all overlays in one call, so loading multiple locale files must
// build the full map first and call it once -- calling it once per file
// would silently drop every earlier locale's overlay. Two files, each with
// a DIFFERENT locale's own key, must both still resolve afterwards.
func TestLoadTestI18nOverlays_CollectsAllFilesBeforeOneSetOverlaysCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "es.json"), []byte(`{"nav.sell":"Vender"}`), 0o644); err != nil {
		t.Fatalf("write es.json fixture: %v", err)
	}

	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}

	if err := loadTestI18nOverlays(i18n, dir); err != nil {
		t.Fatalf("loadTestI18nOverlays: %v", err)
	}

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q, want Verkaufen (de overlay must survive the es.json load too)", got)
	}
	if got := i18n.T("es", "nav.sell"); got != "Vender" {
		t.Fatalf("T(es, nav.sell) = %q, want Vender", got)
	}
}

// TestLoadTestI18nOverlays_MissingDirReturnsError: the caller log.Fatalf's
// on a non-nil error, so a missing/mistyped UT_TEST_I18N_OVERLAY_DIR must
// fail loudly rather than silently loading nothing.
func TestLoadTestI18nOverlays_MissingDirReturnsError(t *testing.T) {
	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	if err := loadTestI18nOverlays(i18n, filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("want an error for a missing overlay directory")
	}
}

// TestLoadTestI18nOverlays_MalformedJSONReturnsError: same "fail loudly"
// requirement for a malformed overlay file.
func TestLoadTestI18nOverlays_MalformedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{not valid json`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	if err := loadTestI18nOverlays(i18n, dir); err == nil {
		t.Fatal("want an error for malformed overlay JSON")
	}
}

// TestLoadTestI18nOverlays_SkipsNonJSONEntriesNonRecursive: only *.json
// files directly in the directory are loaded -- a stray README or a
// subdirectory must not be treated as a locale file/be descended into.
func TestLoadTestI18nOverlays_SkipsNonJSONEntriesNonRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(`not a locale file`), 0o644); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "fr.json"), []byte(`{"nav.sell":"Vendre"}`), 0o644); err != nil {
		t.Fatalf("write nested fixture: %v", err)
	}

	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	if err := loadTestI18nOverlays(i18n, dir); err != nil {
		t.Fatalf("loadTestI18nOverlays: %v", err)
	}

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q, want Verkaufen", got)
	}
	// fr.json was nested one level down -- must not have been picked up.
	if got := i18n.T("fr", "nav.sell"); got != "nav.sell" {
		t.Fatalf("T(fr, nav.sell) = %q, want the bare key back (nested file must be ignored)", got)
	}
}
