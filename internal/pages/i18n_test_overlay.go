package pages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
)

// loadTestI18nOverlays reads every *.json file directly in dir (no
// recursion) and installs each as an I18n overlay keyed by the file's
// basename without extension (de.json -> locale "de"), so
// UT_TEST_I18N_OVERLAY_DIR (see Init, below) can exercise a language-pack's
// translations without a full Ed25519-signed WASM plugin install.
//
// config.I18n.SetOverlays atomically REPLACES every overlay in one call
// (see internal/config/i18n.go), so every file's map is collected first and
// SetOverlays is called exactly once with the full result -- calling it
// once per file would silently drop every earlier locale's overlay.
//
// Test/e2e harness only: never a production plugin-install path, never
// signature-verified. See scripts/ci/audit-locale-render.sh.
func loadTestI18nOverlays(i *config.I18n, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("UT_TEST_I18N_OVERLAY_DIR %q: %w", dir, err)
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
			return fmt.Errorf("UT_TEST_I18N_OVERLAY_DIR: read %s: %w", name, err)
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("UT_TEST_I18N_OVERLAY_DIR: %s: %w", name, err)
		}
		overlays[locale] = m
	}

	i.SetOverlays(overlays)
	return nil
}
