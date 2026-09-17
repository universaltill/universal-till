package catalog

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
// paths). Mirrors internal/pages/main_test.go exactly, for the same reason
// (ut-docs#303): httpx.T falls back to returning the bare key when no
// translator is wired, so a test asserting on translated content silently
// depends on some OTHER test in the package having called httpx.InitI18n
// first — a real, reproduced failure in isolation (`go test -run <name>`),
// not hypothetical. This package had no TestMain at all until
// TestCatalogReplicaBannerNeverLinksAcrossDevices (ut-docs#390) hit exactly
// that gap: it asserted on sync.banner_open_primary_unavailable's real
// translated text and got the bare key back.
func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if err := os.Chdir(root); err != nil {
		panic("TestMain: chdir to repo root: " + err.Error())
	}
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		panic("TestMain: load locales: " + err.Error())
	}
	httpx.InitI18n(i18n, "en")
	// ut-docs#2312: every mutating handler in this package now gates on the
	// catalog_management permission (requireCatalogManagement, handlers.go)
	// -- a check that short-circuits to allowed under the SAME UT_AUTH=off
	// escape hatch internal/pages.canPerform already honours. Setting it
	// once here, for the whole package's test binary, means the ~28
	// existing test files in this package -- none of which set up an
	// AuthSvc/session, since none of these routes carried a permission
	// check before this card -- keep passing unchanged; a test that wants
	// to exercise the real gate (cashier denied / manager allowed) opts
	// back in with its own t.Setenv("UT_AUTH", "on"), which os/exec-style
	// t.Setenv restores back to "off" once that one test ends.
	_ = os.Setenv("UT_AUTH", "off")
	os.Exit(m.Run())
}
