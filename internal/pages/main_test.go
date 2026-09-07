package pages

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/secrets"
)

// TestMain wires real i18n once for this package's whole test binary, and
// chdirs to the repo root (needed to resolve "web/locales" and template
// paths — same computation ui_smoke_test.go's chdirRoot does per-test).
//
// Before this (ut-docs#303 review), tests that assert on translated content
// silently depended on some OTHER test in the package having called
// httpx.InitI18n first: httpx.T falls back to returning the bare key when
// no translator is wired, so a test run in isolation (`go test -run
// <name>`, not the full package) got e.g. "import.status.created" instead
// of "created" in its response body — a real, reproduced failure, not a
// hypothetical one. Doing it once here removes the whole class instead of
// re-adding the same bootstrap to every affected test.
func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		panic("TestMain: chdir to repo root: " + err.Error())
	}
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		panic("TestMain: load locales: " + err.Error())
	}
	httpx.InitI18n(i18n, "en")
	// ADR-0082 (ut-docs#1739): the plugin-settings repository seals a
	// credential-named setting on every write and refuses the write when no
	// key store is registered — so the settings-page tests (which seed
	// api_key etc.) need one, exactly like internal/data's own TestMain. A
	// throwaway self-generating store, never the production path.
	secretsDir, err := os.MkdirTemp("", "ut-pages-secrets-")
	if err != nil {
		panic("TestMain: secrets temp dir: " + err.Error())
	}
	secrets.SetDefault(secrets.NewKeyStoreAt(filepath.Join(secretsDir, "plugin_settings_key.bin"), nil))
	code := m.Run()
	_ = os.RemoveAll(secretsDir)
	os.Exit(code)
}
