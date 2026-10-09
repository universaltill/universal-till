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

// pinCurrency sets the process-global active currency (httpx.InitCurrency)
// for this test and re-inits it to the GBP baseline when it ends (ut-docs#970
// convention; ut-docs#3819). Handlers that persist a currency (setup wizard,
// settings save/upsert, import commit) call InitCurrency themselves, so the
// shared deps helpers pin GBP through this and the leak is undone per test.
func pinCurrency(t *testing.T, code string) {
	t.Helper()
	httpx.InitCurrency(code)
	t.Cleanup(func() { httpx.InitCurrency("GBP") })
}

// pagesGlobalsBaseline is every httpx process-global as TestMain left it
// (real i18n, "en", unset currency = GBP, ...) — ut-docs#3822.
var pagesGlobalsBaseline func()

// resetProcessGlobals puts every httpx process-global back to TestMain's
// baseline now and again when this test ends (ut-docs#3822). Handlers under
// test publish settings there (the setup wizard's locale, a settings save's
// UI scale or OSK mode, a cloud set_setting), and nothing undid them, so a
// shuffled run rendered the next test in lang="tr" or with a key-less
// translator. chdirRoot (the first call of nearly every handler test and
// shared deps helper) calls it, so those tests start from the baseline —
// whatever a helper-less test before them left behind — and clean up after
// themselves; openPagesTestDB adds the exit reset alone. Set a global for
// your test AFTER chdirRoot (or a helper that calls it), or it is reset.
// CI's pages-shuffle job runs this package under pinned -shuffle seeds so
// a new leak fails there (ut-docs#3865).
func resetProcessGlobals(t *testing.T) {
	t.Helper()
	pagesGlobalsBaseline()
	t.Cleanup(pagesGlobalsBaseline)
}

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
	pagesGlobalsBaseline = httpx.SnapshotStateForTests()
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
