package app

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ADR-0113 §1.2 (ut-docs#2687): demo mode starts only when ALL of UT_DEMO,
// a non-empty UT_DEMO_TOKEN, the paths.Data("demo") marker file and the
// database's demo_instance flag hold; any one missing refuses to start and
// names what is missing. The reverse also refuses: demo off on a flagged
// database. Demo off on an unflagged database (every real till) is
// untouched. Demo on with UT_AUTH=off (cfg.AuthDisabled) also refuses,
// however complete the rest is (ADR-0113 §1.9) — a demo till never serves
// without sign-in. All 32 combinations.
func TestCheckDemoGate_AllCombinations(t *testing.T) {
	for _, demo := range []bool{false, true} {
		for _, token := range []string{"", "tok"} {
			for _, marker := range []bool{false, true} {
				for _, flag := range []bool{false, true} {
					for _, authOff := range []bool{false, true} {
						cfg := &config.Config{Demo: demo, DemoToken: token, AuthDisabled: authOff}
						err := checkDemoGate(cfg, marker, flag)
						name := "demo=" + boolStr(demo) + " token=" + boolStr(token != "") + " marker=" + boolStr(marker) + " flag=" + boolStr(flag) + " authOff=" + boolStr(authOff)
						switch {
						case !demo && !flag:
							if err != nil {
								t.Errorf("%s: real till must start, got %v", name, err)
							}
						case !demo && flag:
							if err == nil || !strings.Contains(err.Error(), "demo") {
								t.Errorf("%s: a demo database without demo mode must refuse to start, got %v", name, err)
							}
						case token != "" && marker && flag && !authOff:
							if err != nil {
								t.Errorf("%s: complete demo must start, got %v", name, err)
							}
						default:
							if err == nil {
								t.Fatalf("%s: incomplete demo must refuse to start", name)
							}
							msg := err.Error()
							for _, c := range []struct {
								missing bool
								word    string
							}{{token == "", "UT_DEMO_TOKEN"}, {!marker, "marker"}, {!flag, "flag"}, {authOff, "UT_AUTH=off"}} {
								if c.missing != strings.Contains(msg, c.word) {
									t.Errorf("%s: error %q: mentions %q = %v, want %v", name, msg, c.word, !c.missing, c.missing)
								}
							}
						}
					}
				}
			}
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// The marker must be a regular file: a directory or a symlink named "demo"
// (something other than the broker's own write) does not count.
func TestDemoMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "demo")
	if demoMarkerPresent(p) {
		t.Fatal("missing marker reported present")
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if demoMarkerPresent(p) {
		t.Fatal("a directory named demo reported as the marker")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if demoMarkerPresent(p) {
		t.Fatal("a symlink named demo reported as the marker")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !demoMarkerPresent(p) {
		t.Fatal("a regular marker file reported missing")
	}
}

// runGateCase boots Run against dataDir and asserts it refuses to start with
// an error containing want — returned directly, not turned into a recovery
// server (it must never reach pages.Init or bind).
func runGateCase(t *testing.T, dataDir, want string) {
	t.Helper()
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "127.0.0.1:0")

	origPagesInit := pagesInit
	t.Cleanup(func() { pagesInit = origPagesInit })
	pagesInit = func(ctx, bgCtx context.Context, cfg *config.Config, pm *plugins.Manager, dbConn *sql.DB, catalogRepo *marketplace.CatalogRepository, wg *sync.WaitGroup) (http.Handler, *common.Deps) {
		t.Errorf("pages.Init reached — the demo gate must refuse before serving")
		return origPagesInit(ctx, bgCtx, cfg, pm, dbConn, catalogRepo, wg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Run = %v, want a refusal mentioning %q", err, want)
		}
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("Run did not refuse to start within 30s (serving, or in recovery mode?)")
	}
}

func TestRun_DemoOnWithoutMarkerRefusesToStart(t *testing.T) {
	t.Setenv("UT_DEMO", "1")
	t.Setenv("UT_DEMO_TOKEN", "tok")
	runGateCase(t, t.TempDir(), "marker")
}

func TestRun_DemoOffOnFlaggedDatabaseRefusesToStart(t *testing.T) {
	dataDir := t.TempDir()
	seed, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if err := data.NewDemoInstanceRepo(seed.DB).MarkDemoInstance(context.Background()); err != nil {
		t.Fatalf("mark demo: %v", err)
	}
	seed.Close()
	t.Setenv("UT_DEMO", "0")
	runGateCase(t, dataDir, "demo")
}

// A demo that is otherwise complete (token, marker, flagged database) still
// refuses to start with UT_AUTH=off (ADR-0113 §1.9), straight from Run.
func TestRun_DemoWithAuthOffRefusesToStart(t *testing.T) {
	dataDir := t.TempDir()
	seed, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if err := data.NewDemoInstanceRepo(seed.DB).MarkDemoInstance(context.Background()); err != nil {
		t.Fatalf("mark demo: %v", err)
	}
	seed.Close()
	if err := os.WriteFile(filepath.Join(dataDir, "demo"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UT_DEMO", "1")
	t.Setenv("UT_DEMO_TOKEN", "tok")
	t.Setenv("UT_AUTH", "off")
	runGateCase(t, dataDir, "UT_AUTH=off")
}

// A demo till whose boot fails recoverably (here: a corrupted database
// header, the same genuine db.Open failure the recovery-mode test uses)
// must exit with the error — fail closed — rather than serve ADR-0075's
// recovery mode, whose unauthenticated retry/restore surface sits outside
// the demo middleware (ADR-0113 §1.2).
func TestRun_DemoBootFailureExitsInsteadOfRecoveryMode(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "unitill-pos.db")
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	copy(raw[:16], []byte("not-a-sqlite-hdr"))
	if err := os.WriteFile(dbPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	addr := freeAddr(t)
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", addr)
	t.Setenv("UT_DEMO", "1")
	t.Setenv("UT_DEMO_TOKEN", "tok")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "demo") {
			t.Fatalf("Run = %v, want the boot failure returned, naming demo mode", err)
		}
	case <-time.After(15 * time.Second):
		cancel()
		<-done
		t.Fatal("Run did not exit within 15s — a demo till must not enter recovery mode")
	}
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("something is listening on %s after a refused demo boot", addr)
	}
}
