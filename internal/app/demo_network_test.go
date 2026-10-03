package app

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// networkServices is every background service that talks to the network,
// started through netaccess.StartService by app.Run or pages.Init — the
// list ADR-0113 §1.6 names (cloud sync and enrolment, marketplace and
// revocation checks, telemetry, self-update, alerts, the order-stream and
// sync bridges, mDNS advertising). The ESC/POS LAN sweep is not here: it
// has no caller (discovery.SweepPrinters is unwired), so there is nothing
// to start or skip.
var networkServices = []string{
	// app.Run
	"cloud enrolment",
	"self-update check",
	"alerts",
	"marketplace catalog, revocation and telemetry",
	"mDNS advertising",
	// pages.Init
	"LAN sync push",
	"LAN sync pull",
	"main-till link client",
	"held-order table-claim re-affirm",
	"cloud sync",
	"cloud link",
	"auto-update scheduler",
	"plugin update scheduler",
	"base-plugin auto-install retry",
	"TSE provisioning retry",
	"order-status stream bridge",
}

type serviceLog struct {
	mu      sync.Mutex
	started []string
	skipped []string
}

func observeNetworkServices(t *testing.T) *serviceLog {
	t.Helper()
	l := &serviceLog{}
	restore := netaccess.ObserveServices(func(name string, started bool) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if started {
			l.started = append(l.started, name)
		} else {
			l.skipped = append(l.skipped, name)
		}
	})
	t.Cleanup(restore)
	return l
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	g, w := sortedCopy(got), sortedCopy(want)
	if strings.Join(g, "|") != strings.Join(w, "|") {
		t.Fatalf("%s:\n got  %q\n want %q", label, g, w)
	}
}

// bootUntilPagesInit runs Run until pages.Init has returned (every service
// start in Run and pages.Init has happened by then), records whether the
// netaccess demo switch was on at that point, then shuts down cleanly.
func bootUntilPagesInit(t *testing.T, dataDir string) (demoDuringBoot bool) {
	t.Helper()
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "127.0.0.1:0")
	// Set so the marketplace start is reached at all; nothing listens there.
	t.Setenv("UT_MARKETPLACE_ENDPOINT_URL", "http://127.0.0.1:1")

	orig := pagesInit
	t.Cleanup(func() { pagesInit = orig })
	reached := make(chan bool, 1)
	pagesInit = func(ctx, bgCtx context.Context, cfg *config.Config, pm *plugins.Manager, dbConn *sql.DB, catalogRepo *marketplace.CatalogRepository, wg *sync.WaitGroup) (http.Handler, *common.Deps) {
		h, d := orig(ctx, bgCtx, cfg, pm, dbConn, catalogRepo, wg)
		reached <- netaccess.Demo()
		return h, d
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	select {
	case demoDuringBoot = <-reached:
	case err := <-done:
		t.Fatalf("Run returned before pages.Init: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("Run did not reach pages.Init within 60s")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Run did not return within 60s of cancellation")
	}
	return demoDuringBoot
}

// ADR-0113 §1.6, ut-docs#2795: a demo till starts none of its network
// services, and its outbound clients are switched to deny-all for the whole
// boot.
func TestRun_DemoModeStartsNoNetworkService(t *testing.T) {
	dataDir := t.TempDir()
	seed, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if err := markDemoInstance(seed.DB); err != nil {
		t.Fatalf("mark demo: %v", err)
	}
	seed.Close()
	if err := os.WriteFile(filepath.Join(dataDir, "demo"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UT_DEMO", "1")
	t.Setenv("UT_DEMO_TOKEN", "tok")

	services := observeNetworkServices(t)
	if !bootUntilPagesInit(t, dataDir) {
		t.Error("netaccess demo switch was off during a demo boot — outbound clients would not deny")
	}
	if netaccess.Demo() {
		t.Error("netaccess demo switch still on after Run returned")
	}

	services.mu.Lock()
	defer services.mu.Unlock()
	if len(services.started) != 0 {
		t.Errorf("demo till started network service(s): %q", services.started)
	}
	assertSameSet(t, "skipped network services", services.skipped, networkServices)
}

// No behaviour change with demo off: the very same services all start.
func TestRun_DemoOffStartsEveryNetworkService(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	services := observeNetworkServices(t)
	if bootUntilPagesInit(t, t.TempDir()) {
		t.Error("netaccess demo switch was on during a normal boot")
	}

	services.mu.Lock()
	defer services.mu.Unlock()
	if len(services.skipped) != 0 {
		t.Errorf("normal till skipped network service(s): %q", services.skipped)
	}
	assertSameSet(t, "started network services", services.started, networkServices)
}
