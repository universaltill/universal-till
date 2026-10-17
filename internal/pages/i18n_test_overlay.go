package pages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
)

// readTestI18nOverlays reads every *.json file directly in dir (no
// recursion) and returns them as I18n overlays keyed by the file's basename
// without extension (de.json -> locale "de"), so UT_TEST_I18N_OVERLAY_DIR
// (see Init) can exercise a language-pack's translations without a full
// Ed25519-signed WASM plugin install.
//
// Test/e2e harness only: never a production plugin-install path, never
// signature-verified. See scripts/ci/audit-locale-render.sh.
func readTestI18nOverlays(dir string) (map[string]map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("UT_TEST_I18N_OVERLAY_DIR %q: %w", dir, err)
	}

	overlays := make(map[string]map[string]string)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		locale := strings.TrimSuffix(name, ".json")
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("UT_TEST_I18N_OVERLAY_DIR: read %s: %w", name, err)
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("UT_TEST_I18N_OVERLAY_DIR: %s: %w", name, err)
		}
		overlays[locale] = m
	}

	return overlays, nil
}

// testI18nOverlayLocalizer is the plugins.Localizer the plugin manager talks
// to when UT_TEST_I18N_OVERLAY_DIR is set: it merges the directory's locale
// files into EVERY set of overlays the manager publishes, then forwards the
// merged result to the real translator.
//
// This wrapper (rather than a one-shot config.I18n.SetPluginLocales call after
// pm.SetLocalizer) is load-bearing, not defensive: SetPluginLocales atomically
// REPLACES every overlay in one call, and the plugin manager re-publishes
// its own overlays from scratch on every Manager.Reload — which
// Deps.ReloadPlugins triggers on any plugin lifecycle change AND on two
// non-plugin paths that a plain e2e run really does hit (the boot-time
// builtin-layout reconciliation in Init below, and the setup wizard's /
// Settings' shop-type save, setup_page.go and settings_page.go). A one-shot
// call is silently wiped by the first of those, and every page then renders
// in English again — exactly the false signal the render audit exists to
// detect. Verified live: without this wrapper, POST /api/settings/shop-type
// dropped the de overlay mid-run.
//
// The test overlay wins over a plugin's own overlay for the same locale+key,
// since it is the thing the run under way is deliberately exercising. Base
// locale files and shop overrides still win over both (config.I18n.T's
// layering is untouched).
type testI18nOverlayLocalizer struct {
	base  plugins.Localizer
	extra map[string]map[string]string
}

// newTestI18nOverlayLocalizer reads dir once at startup; a bad directory or a
// malformed locale file is a hard error so a mistyped
// UT_TEST_I18N_OVERLAY_DIR fails loudly instead of silently auditing an
// untranslated till.
func newTestI18nOverlayLocalizer(base plugins.Localizer, dir string) (*testI18nOverlayLocalizer, error) {
	extra, err := readTestI18nOverlays(dir)
	if err != nil {
		return nil, err
	}
	return &testI18nOverlayLocalizer{base: base, extra: extra}, nil
}

// SetPluginLocales merges the test overlays over the stale first-party
// layer the plugin manager published — the highest plugin layer that still
// loses to the built-in locale files, so the test overlay beats every
// installed language pack (none carries a bundle.json yet) and every
// third-party plugin — then forwards the result. The incoming maps are never
// mutated — they belong to the caller.
func (l *testI18nOverlayLocalizer) SetPluginLocales(pl config.PluginLocales) {
	merged := make(map[string]map[string]string, len(pl.FirstPartyStale)+len(l.extra))
	for locale, m := range pl.FirstPartyStale {
		cp := make(map[string]string, len(m))
		for k, v := range m {
			cp[k] = v
		}
		merged[locale] = cp
	}
	for locale, m := range l.extra {
		if merged[locale] == nil {
			merged[locale] = make(map[string]string, len(m))
		}
		for k, v := range m {
			merged[locale][k] = v
		}
	}
	pl.FirstPartyStale = merged
	l.base.SetPluginLocales(pl)
}
