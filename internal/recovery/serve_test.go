package recovery

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/settings"
)

// freeAddr returns a loopback address this test can bind Serve to.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func testConfig(t *testing.T, addr string) *config.Config {
	t.Helper()
	return &config.Config{
		ListenAddr: addr,
		DBPath:     filepath.Join(t.TempDir(), "unitill-pos.db"),
		Locales:    config.Locales{Locale: "en"},
	}
}

// waitHealthy polls addr until something answers HTTP (recovery mode
// binding is asynchronous relative to the goroutine that calls Serve).
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s within the deadline", addr)
}

func TestServe_HealthzStaysUnhealthyWhileInRecoveryMode(t *testing.T) {
	addr := freeAddr(t)
	cfg := testConfig(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		r, err := Serve(ctx, cfg, Failure{Kind: KindMigration, Detail: "boom", RefCode: "AAAA-BBBB"})
		done <- struct {
			result Result
			err    error
		}{r, err}
	}()

	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("/healthz reported healthy while recovery mode is serving — every shell's lock/exit-gating logic depends on this staying unhealthy")
	}

	cancel()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Serve returned an error on ctx cancellation: %v", r.err)
		}
		if r.result != Shutdown {
			t.Fatalf("Result = %v, want Shutdown", r.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of ctx cancellation")
	}
}

func TestServe_PageShowsDetailAndRefCode(t *testing.T) {
	addr := freeAddr(t)
	cfg := testConfig(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_, _ = Serve(ctx, cfg, Failure{Kind: KindMigration, Detail: "exec migration 42: syntax error", RefCode: "AAAA-BBBB"})
	}()
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if want := "exec migration 42: syntax error"; !strings.Contains(page, want) {
		t.Fatalf("recovery page missing the failure detail %q\npage:\n%s", want, page)
	}
	if want := "AAAA-BBBB"; !strings.Contains(page, want) {
		t.Fatalf("recovery page missing the ref code %q\npage:\n%s", want, page)
	}
}

func TestServe_RetryReturnsRetryResultAndReleasesThePort(t *testing.T) {
	addr := freeAddr(t)
	cfg := testConfig(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		r, err := Serve(ctx, cfg, Failure{Kind: KindDBOpen, Detail: "boom", RefCode: "CCCC-DDDD"})
		done <- struct {
			result Result
			err    error
		}{r, err}
	}()
	waitListening(t, addr)

	resp, err := http.Post("http://"+addr+"/api/recovery/retry", "", nil)
	if err != nil {
		t.Fatalf("POST /api/recovery/retry: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/recovery/retry status = %d, want 202", resp.StatusCode)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Serve returned an error after Retry: %v", r.err)
		}
		if r.result != Retry {
			t.Fatalf("Result = %v, want Retry", r.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of a Retry request")
	}

	// The whole point of returning Retry rather than looping internally:
	// app.Run needs the port free to re-attempt its own boot on the SAME
	// address (ADR-0075 — recovery mode never falls back to a different
	// port). Prove Serve actually released it.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port %s not released after Serve returned Retry: %v", addr, err)
	}
	_ = ln.Close()
}

func TestServe_SafeModeUnavailableWhenNoDatabaseExists(t *testing.T) {
	addr := freeAddr(t)
	cfg := testConfig(t, addr) // DBPath points at a nonexistent file
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _, _ = Serve(ctx, cfg, Failure{Kind: KindMigration, Detail: "boom", RefCode: "EEEE-FFFF"}) }()
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/recovery/safe-mode")
	if err != nil {
		t.Fatalf("GET /recovery/safe-mode: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("safe-mode route answered 200 with no database to read from — it must not be registered when safe mode is unavailable")
	}
}

func TestServe_SafeModeServesAgainstAGenuinelyMigratedDatabase(t *testing.T) {
	addr := freeAddr(t)
	cfg := testConfig(t, addr)

	// Boot a real database first (proves the migration DID succeed at some
	// point — safe mode's whole premise), then simulate a LATER migration
	// failure by just pointing Serve at the same file with Kind:
	// KindMigration. Doesn't seed a sale row (that needs internal/data's
	// own repo methods to do correctly, not a hand-written INSERT against
	// a schema this test shouldn't need to track) — proving the safe-mode
	// route is registered and queries successfully (zero rows is a valid,
	// honest answer) is what this test is actually verifying; row-content
	// correctness belongs to ListSalesJournal's own tests.
	full, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = full.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = Serve(ctx, cfg, Failure{Kind: KindMigration, Detail: "boom", RefCode: "1111-2222"}) }()
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/recovery/safe-mode")
	if err != nil {
		t.Fatalf("GET /recovery/safe-mode: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /recovery/safe-mode status = %d, body = %s", resp.StatusCode, body)
	}
}

// Integration-level version of TestProbeSafeMode_FailsAgainstPreTillsSchema
// (classify_test.go tests the unit; this proves Serve itself actually wires
// the gate in — independent review's original finding was reproduced
// exactly at this level: db.OpenReadOnly succeeding, then a 500 on
// /recovery/safe-mode with the raw SQL error echoed to the client).
func TestServe_SafeModeNotOfferedWhenTheQueryCantRunAgainstTheSchema(t *testing.T) {
	addr := freeAddr(t)
	cfg := testConfig(t, addr)

	full, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := full.Exec(`DROP TABLE tills`); err != nil {
		t.Fatalf("drop tills: %v", err)
	}
	if err := full.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = Serve(ctx, cfg, Failure{Kind: KindMigration, Detail: "boom", RefCode: "AAAA-0001"}) }()
	waitListening(t, addr)

	// The page must not advertise a safe-mode link that then 500s.
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(string(body), `href="/recovery/safe-mode"`) {
		t.Fatal("recovery page advertised the safe-mode link against a database whose schema can't actually serve it")
	}

	// And the route itself must not be registered at all — not just hidden
	// from the page's own link.
	smResp, err := http.Get("http://" + addr + "/recovery/safe-mode")
	if err != nil {
		t.Fatalf("GET /recovery/safe-mode: %v", err)
	}
	defer smResp.Body.Close()
	if smResp.StatusCode == http.StatusOK {
		t.Fatal("GET /recovery/safe-mode answered 200 against a database whose schema can't run the underlying query")
	}
}

// httpx.ActiveCurrency() silently defaults to GBP when never initialized —
// never a crash, but a real correctness gap for a shop on any other
// currency: the safe-mode sales list would show real figures under the
// wrong symbol. Proves Serve reads the shop's actually-configured currency
// from the read-only DB rather than leaving it at that default.
func TestServe_SafeModeReadsTheShopsRealCurrency(t *testing.T) {
	addr := freeAddr(t)
	cfg := testConfig(t, addr)

	full, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := settings.NewStore(full.DB).Set(context.Background(), "store.currency", "TRY"); err != nil {
		t.Fatalf("seed store.currency: %v", err)
	}
	if err := full.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = Serve(ctx, cfg, Failure{Kind: KindMigration, Detail: "boom", RefCode: "9999-0000"}) }()
	waitListening(t, addr)

	// Give Serve's goroutine a moment past "listening" to have run its
	// currency-read step, which happens before it starts serving.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && httpx.ActiveCurrency().Code != "TRY" {
		time.Sleep(10 * time.Millisecond)
	}
	if got := httpx.ActiveCurrency().Code; got != "TRY" {
		t.Fatalf("httpx.ActiveCurrency().Code = %q, want %q (safe mode must read the shop's real configured currency, not default to GBP)", got, "TRY")
	}
}
