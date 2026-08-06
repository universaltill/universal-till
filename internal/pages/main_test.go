package pages

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
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
	os.Exit(m.Run())
}
