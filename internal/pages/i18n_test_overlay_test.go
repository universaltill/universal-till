package pages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/web/locales"
)

// newOverlayTestI18n is the real translator, loaded from the real embedded
// base locales, exactly as Init builds it.
func newOverlayTestI18n(t *testing.T) *config.I18n {
	t.Helper()
	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	return i18n
}

// applyTestOverlays wires the UT_TEST_I18N_OVERLAY_DIR localizer around i18n
// and drives the one call the plugin manager makes on SetLocalizer/Reload
// (plugins.Manager.syncLocales publishes the overlays of every installed
// plugin -- none here).
func applyTestOverlays(t *testing.T, i18n *config.I18n, dir string) *testI18nOverlayLocalizer {
	t.Helper()
	l, err := newTestI18nOverlayLocalizer(i18n, dir)
	if err != nil {
		t.Fatalf("newTestI18nOverlayLocalizer: %v", err)
	}
	l.SetOverlays(map[string]map[string]string{})
	return l
}

// TestTestI18nOverlays_LoadsEveryJSONFileKeyedByStem covers the
// UT_TEST_I18N_OVERLAY_DIR escape hatch (ut-docs#2300): every *.json file in
// the named directory becomes an overlay for the locale named by its
// filename stem, and I18n.T resolves through it exactly as it would a real
// language-pack plugin's overlay (pm.SetLocalizer's normal production path).
func TestTestI18nOverlays_LoadsEveryJSONFileKeyedByStem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fr.json"), []byte(`{"nav.sell":"Vendre"}`), 0o644); err != nil {
		t.Fatalf("write fr.json fixture: %v", err)
	}

	i18n := newOverlayTestI18n(t)
	applyTestOverlays(t, i18n, dir)

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q, want Verkaufen", got)
	}
	if got := i18n.T("fr", "nav.sell"); got != "Vendre" {
		t.Fatalf("T(fr, nav.sell) = %q, want Vendre", got)
	}
}

// TestTestI18nOverlays_CollectsAllFilesBeforeOneSetOverlaysCall guards the
// exact bug the card calls out: config.I18n.SetOverlays atomically REPLACES
// all overlays in one call, so loading multiple locale files must build the
// full map first and call it once -- calling it once per file would silently
// drop every earlier locale's overlay. Two files, each with a DIFFERENT
// locale's own key, must both still resolve afterwards.
func TestTestI18nOverlays_CollectsAllFilesBeforeOneSetOverlaysCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "es.json"), []byte(`{"nav.sell":"Vender"}`), 0o644); err != nil {
		t.Fatalf("write es.json fixture: %v", err)
	}

	i18n := newOverlayTestI18n(t)
	applyTestOverlays(t, i18n, dir)

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q, want Verkaufen (de overlay must survive the es.json load too)", got)
	}
	if got := i18n.T("es", "nav.sell"); got != "Vender" {
		t.Fatalf("T(es, nav.sell) = %q, want Vender", got)
	}
}

// TestTestI18nOverlays_SurvivesPluginManagerReload is the review regression
// (reviewer, 2026-09-18): the plugin manager re-publishes its overlays from
// scratch on every Manager.Reload -- Deps.ReloadPlugins, which a plain e2e
// run really does hit (boot-time builtin-layout reconciliation, the setup
// wizard's shop-type step, any plugin install/enable). SetOverlays REPLACES
// everything, so a one-shot load of the test overlays is wiped by the first
// such reload and every page silently renders English again. Reproduced live
// against a running till before the fix (POST /api/settings/shop-type
// dropped the de overlay); this asserts the overlay is re-merged instead.
func TestTestI18nOverlays_SurvivesPluginManagerReload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}

	i18n := newOverlayTestI18n(t)
	localizer := applyTestOverlays(t, i18n, dir)

	// A later reload: the manager republishes only the installed plugins'
	// own overlays, with no knowledge of the test directory.
	localizer.SetOverlays(map[string]map[string]string{
		"de": {"plugin.own.key": "Von einem Plugin"},
	})

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q after a plugin reload, want Verkaufen", got)
	}
	if got := i18n.T("de", "plugin.own.key"); got != "Von einem Plugin" {
		t.Fatalf("T(de, plugin.own.key) = %q, want the plugin's own overlay to survive too", got)
	}
}

// TestTestI18nOverlays_TestOverlayWinsOverPluginOverlay: the run under way is
// deliberately exercising the test directory's pack, so its value wins on a
// locale+key collision with an installed plugin's own overlay.
func TestTestI18nOverlays_TestOverlayWinsOverPluginOverlay(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}

	i18n := newOverlayTestI18n(t)
	localizer := applyTestOverlays(t, i18n, dir)
	localizer.SetOverlays(map[string]map[string]string{"de": {"nav.sell": "Von einem Plugin"}})

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q, want the test overlay to win over a plugin's own value", got)
	}
}

// TestTestI18nOverlays_DoesNotMutateCallersMap: SetOverlays' argument belongs
// to the plugin manager, which keeps building its own map -- merging must not
// write the test overlay back into it.
func TestTestI18nOverlays_DoesNotMutateCallersMap(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{"nav.sell":"Verkaufen"}`), 0o644); err != nil {
		t.Fatalf("write de.json fixture: %v", err)
	}

	i18n := newOverlayTestI18n(t)
	localizer, err := newTestI18nOverlayLocalizer(i18n, dir)
	if err != nil {
		t.Fatalf("newTestI18nOverlayLocalizer: %v", err)
	}
	fromPlugins := map[string]map[string]string{"de": {"plugin.own.key": "Von einem Plugin"}}
	localizer.SetOverlays(fromPlugins)

	if _, ok := fromPlugins["de"]["nav.sell"]; ok {
		t.Fatal("the caller's own overlay map was mutated by the merge")
	}
}

// TestTestI18nOverlays_MissingDirReturnsError: the caller log.Fatalf's on a
// non-nil error, so a missing/mistyped UT_TEST_I18N_OVERLAY_DIR must fail
// loudly rather than silently loading nothing.
func TestTestI18nOverlays_MissingDirReturnsError(t *testing.T) {
	i18n := newOverlayTestI18n(t)
	if _, err := newTestI18nOverlayLocalizer(i18n, filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("want an error for a missing overlay directory")
	}
}

// TestTestI18nOverlays_MalformedJSONReturnsError: same "fail loudly"
// requirement for a malformed overlay file.
func TestTestI18nOverlays_MalformedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de.json"), []byte(`{not valid json`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	i18n := newOverlayTestI18n(t)
	if _, err := newTestI18nOverlayLocalizer(i18n, dir); err == nil {
		t.Fatal("want an error for malformed overlay JSON")
	}
}

// TestTestI18nOverlays_SkipsNonJSONEntriesNonRecursive: only *.json files
// directly in the directory are loaded -- a stray README or a subdirectory
// must not be treated as a locale file/be descended into.
func TestTestI18nOverlays_SkipsNonJSONEntriesNonRecursive(t *testing.T) {
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

	i18n := newOverlayTestI18n(t)
	applyTestOverlays(t, i18n, dir)

	if got := i18n.T("de", "nav.sell"); got != "Verkaufen" {
		t.Fatalf("T(de, nav.sell) = %q, want Verkaufen", got)
	}
	// fr.json was nested one level down -- must not have been picked up.
	if got := i18n.T("fr", "nav.sell"); got != "nav.sell" {
		t.Fatalf("T(fr, nav.sell) = %q, want the bare key back (nested file must be ignored)", got)
	}
}
