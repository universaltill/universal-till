package mobile

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/bluetooth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/listenport"
	"github.com/universaltill/universal-till/internal/procrestart"
	"github.com/universaltill/universal-till/internal/recovery"
)

// mobileTestEnv points the till at an isolated temp data dir and disables
// auth (no PIN session needed to hit /healthz) for every test in this file —
// mirrors how other live-verification runs in this repo set up a throwaway
// till instance.
// Plain t.TempDir() is safe here: app.Run now joins every background
// goroutine it starts (directly or via server.Start) before returning, so by
// the time Stop() returns (it blocks on app.Run's return), nothing is still
// writing into the data dir. Before that fix, a straggler could still be
// mid-write for a moment after Stop() returned, and t.TempDir()'s own
// RemoveAll cleanup raced it, flaking with "TempDir RemoveAll cleanup:
// directory not empty" (main CI 2026-07-30, run 30531313594) — worked around
// there with a manual dir + retrying removal. That workaround is gone. NOTE:
// this is a regression canary, not a proof by itself — it only ever caught
// the bug as a rare, timing-dependent CI flake (a clean local run here proves
// nothing on its own; internal/app and internal/server carry the actual
// deterministic join tests). Cleanup order (LIFO): Stop runs first, then
// t.TempDir()'s own (single-attempt) removal.
func mobileTestEnv(t *testing.T) string {
	t.Helper()
	// ut-docs#1239: Start may export TMPDIR into a test's (later-deleted)
	// dataDir on machines where TMPDIR isn't set — e.g. ubuntu CI runners;
	// macOS always sets it. A stale export poisons os.TempDir() for the
	// whole process, so every later t.TempDir() call — including the ones
	// below — fails with ENOENT before the test body even runs. Clear a
	// dangling TMPDIR first (so t.TempDir() resolves somewhere real), then
	// pin a per-test one so Start's own-export branch only runs in the
	// tests that opt in by re-clearing it.
	if cur := os.Getenv("TMPDIR"); cur != "" {
		if _, err := os.Stat(cur); err != nil {
			t.Setenv("TMPDIR", "")
		}
	}
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_ENV_FILE", t.TempDir()+"/does-not-exist.env") // don't pick up a stray local pos.env
	dataDir := t.TempDir()
	// ut-docs#2722: Start now prefers a stable port (listenport.DefaultPort,
	// 8080) instead of an ephemeral one. Point this test's default at a
	// port that is free right now, so tests never grab 8080 (other tills
	// and worktrees on this machine use it) and never depend on it.
	free, err := freePort()
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	prev := defaultListenPort
	defaultListenPort, _ = strconv.Atoi(free)
	t.Cleanup(func() { defaultListenPort = prev })
	t.Cleanup(Stop)
	return dataDir
}

func TestStartAndStop_RealServerBoots(t *testing.T) {
	dataDir := mobileTestEnv(t)

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if addr == "" {
		t.Fatal("expected a non-empty address")
	}
	if !IsRunning() {
		t.Fatal("expected IsRunning() to be true after a successful Start")
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}

	// Stop() blocks until the server has FULLY torn down (not just until
	// the shutdown signal was sent) — so a single check right after it
	// returns is deterministic, not a race against an async shutdown.
	Stop()
	if IsRunning() {
		t.Fatal("expected IsRunning() to be false after Stop")
	}
	if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
		t.Fatalf("server at %s still answering immediately after Stop returned", addr)
	}
}

func TestStart_IdempotentWhileRunning(t *testing.T) {
	dataDir := mobileTestEnv(t)

	addr1, err := Start(dataDir)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// A second Start call with the SAME dataDir while already running must
	// NOT try to boot a second server (which would fail: the first already
	// holds the DB and whatever port it bound) — it should just hand back
	// the same address.
	addr2, err := Start(dataDir)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if addr1 != addr2 {
		t.Fatalf("second Start returned %q, want the same address %q", addr2, addr1)
	}
}

func TestStart_DifferentDataDirWhileRunningErrors(t *testing.T) {
	dataDir := mobileTestEnv(t)

	if _, err := Start(dataDir); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// A second Start with a DIFFERENT dataDir while already running must
	// fail loudly, not silently ignore the request and keep serving the
	// first dataDir under a caller's mistaken belief it switched. (This
	// other dir sees no live server write, so an inline t.TempDir is fine.)
	if _, err := Start(t.TempDir()); err == nil {
		t.Fatal("expected Start against a different dataDir while running to error")
	}
}

func TestStart_FastFailWhenServerDiesImmediately(t *testing.T) {
	_ = mobileTestEnv(t) // Start below never boots a server; env setup only

	// A regular FILE where the data dir should be a directory makes
	// db.Open fail immediately (DBPath = filepath.Join(dataDir,
	// "unitill-pos.db") can never be created under a non-directory) —
	// exercises waitUntilReady's fast-fail path (app.Run's goroutine
	// returns an error before the /healthz poll ever succeeds) rather
	// than waiting out the full 10s timeout.
	notADir := filepath.Join(t.TempDir(), "this-is-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed a non-directory dataDir: %v", err)
	}

	_, err := Start(notADir)
	if err == nil {
		t.Fatal("expected Start to fail fast when the server can't open its database")
	}
	if IsRunning() {
		t.Fatal("expected IsRunning() to be false after a failed Start")
	}
}

// A server that dies on its own (e.g. a listener error) without Stop()
// ever being called must not leave IsRunning()/Start() reporting stale
// success — this is the "abandoned runErrCh" gap flagged in code review:
// cancelling the instance's own context directly (as a real crash would,
// from the caller's point of view) simulates that, without needing to
// engineer an actual server-internal failure.
func TestIsRunning_DetectsServerDiedWithoutStop(t *testing.T) {
	dataDir := mobileTestEnv(t)

	if _, err := Start(dataDir); err != nil {
		t.Fatalf("Start: %v", err)
	}

	mu.Lock()
	died := inst
	mu.Unlock()
	died.cancel()
	<-died.done // wait for the real teardown, same as Stop() would

	if IsRunning() {
		t.Fatal("expected IsRunning() to detect the dead instance and report false")
	}

	// A subsequent Start with the same dataDir must actually restart the
	// server, not return the old (now-dead) address as if nothing happened.
	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start after an unobserved crash: %v", err)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz on the restarted server: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}
}

// ut-docs#1256: the embedded server must bind ALL interfaces (so an Android
// till can be a LAN-reachable primary that other tills discover and pair
// with — the existing ADR-0033 discovery/pairing/bearer-token model is the
// security boundary, same as desktop), while the address Start hands the
// native shell stays loopback (the WebView contract is unchanged). Two
// distinct addresses, one port: the code-level guarantee this test pins is
// the UT_LISTEN_ADDR host, since that is exactly what internal/server binds.
func TestStart_ListensOnAllInterfacesButReturnsLoopbackAddress(t *testing.T) {
	dataDir := mobileTestEnv(t)
	// Start sets UT_LISTEN_ADDR process-wide via os.Setenv; pinning it here
	// first makes t.Setenv restore whatever was there once this test ends,
	// and proves the value read below came from THIS Start, not a stale one.
	t.Setenv("UT_LISTEN_ADDR", "")

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("Start returned %q, not a host:port: %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("Start returned host %q, want the loopback 127.0.0.1 the WebView loads", host)
	}

	listenHost, listenPort, err := net.SplitHostPort(os.Getenv("UT_LISTEN_ADDR"))
	if err != nil {
		t.Fatalf("UT_LISTEN_ADDR = %q, not a host:port: %v", os.Getenv("UT_LISTEN_ADDR"), err)
	}
	if listenHost != "0.0.0.0" {
		t.Fatalf("UT_LISTEN_ADDR host = %q, want the all-interfaces 0.0.0.0 bind", listenHost)
	}
	if listenPort != port {
		t.Fatalf("UT_LISTEN_ADDR port = %q, want the same port %q Start returned", listenPort, port)
	}

	// A wildcard bind still accepts loopback — the returned address must
	// actually answer, exactly as it did when the bind itself was loopback.
	get := func(target string) {
		t.Helper()
		resp, err := http.Get("http://" + target + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz via %s: %v", target, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/healthz via %s = %d, want 200", target, resp.StatusCode)
		}
	}
	get(addr)

	// And the point of the change: the same port answers on this host's
	// real (non-loopback IPv4) interfaces — enumerated the way
	// internal/discovery.localIPs does, so this is the address a LAN peer
	// would actually dial. A host with no such interface (a sandboxed CI
	// network namespace) can't exercise this leg; the UT_LISTEN_ADDR
	// assertion above is the guarantee that still holds there.
	lanIPs := nonLoopbackIPv4s(t)
	if len(lanIPs) == 0 {
		t.Log("no non-loopback IPv4 interface on this host; LAN reachability leg skipped")
		return
	}
	for _, ip := range lanIPs {
		get(net.JoinHostPort(ip.String(), port))
	}
}

// nonLoopbackIPv4s mirrors internal/discovery.localIPs's interface walk
// (minus its loopback fallback) — the addresses a LAN peer would reach
// this host on.
func nonLoopbackIPv4s(t *testing.T) []net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("net.InterfaceAddrs: %v", err)
	}
	var ips []net.IP
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		ips = append(ips, ipNet.IP)
	}
	return ips
}

// stubBluetoothBridge is the smallest possible BluetoothBridge — a
// stand-in for the Kotlin implementation ut-docs#1731 will supply. It
// implements BluetoothBridge (this package) structurally, which — since
// BluetoothBridge and bluetooth.AndroidBridge are kept identical on purpose
// (see BluetoothBridge's own doc comment) — also satisfies
// bluetooth.AndroidBridge with no adapter, exactly what SetBluetoothBridge
// relies on below. This test only needs to prove the setter reaches
// internal/bluetooth's registration; the bridge's own forwarding/decoding
// behaviour is covered in internal/bluetooth/android_bridge_test.go.
type stubBluetoothBridge struct{ listCalls int }

func (s *stubBluetoothBridge) ListDevices() (string, error) { s.listCalls++; return "[]", nil }
func (s *stubBluetoothBridge) Scan(int64) (string, error)   { return "[]", nil }
func (s *stubBluetoothBridge) Pair(string) error            { return nil }
func (s *stubBluetoothBridge) Forget(string) error          { return nil }

// ADR-0080 / ut-docs#1721: SetBluetoothBridge is the one Kotlin → Go entry
// point that registers Android's Bluetooth stack with internal/bluetooth.
// It must actually reach bluetooth.SetAndroidBridge — a stub setter that
// gomobile binds but that drops the value on the floor would compile,
// bind, and silently leave Android on ErrUnsupportedPlatform forever.
func TestSetBluetoothBridge_RegistersWithBluetoothPackage(t *testing.T) {
	if got := bluetooth.RegisteredAndroidBridge(); got != nil {
		t.Fatalf("a bridge is already registered before this test ran: %#v", got)
	}
	stub := &stubBluetoothBridge{}
	SetBluetoothBridge(stub)
	t.Cleanup(func() { SetBluetoothBridge(nil) })

	got := bluetooth.RegisteredAndroidBridge()
	if got != bluetooth.AndroidBridge(stub) {
		t.Fatalf("bluetooth.RegisteredAndroidBridge() = %#v, want the stub passed to SetBluetoothBridge", got)
	}
	// It's the same object, not a copy or a wrapper: a call through the
	// registered bridge lands on the stub.
	if _, err := got.ListDevices(); err != nil {
		t.Fatalf("ListDevices via the registered bridge: %v", err)
	}
	if stub.listCalls != 1 {
		t.Fatalf("stub ListDevices calls = %d, want 1", stub.listCalls)
	}

	// And clearing it restores the pre-ADR-0080 default (no bridge).
	SetBluetoothBridge(nil)
	if got := bluetooth.RegisteredAndroidBridge(); got != nil {
		t.Fatalf("after SetBluetoothBridge(nil): RegisteredAndroidBridge() = %#v, want nil", got)
	}
}

func TestStop_SafeWhenNotRunning(t *testing.T) {
	_ = mobileTestEnv(t)
	Stop() // must not panic
	if IsRunning() {
		t.Fatal("IsRunning() should be false when Start was never called")
	}
}

// ut-docs#1239: on Android TMPDIR is unset and Go's os.TempDir() fallback
// is unwritable for an app uid, so anything spilling to temp files (SQLite
// VACUUM/backup, os.CreateTemp) dies with an I/O error. Start must give the
// process a writable temp dir inside its own sandbox — unless the host
// already configured one, which it must respect.
func TestStart_SetsWritableTMPDIRWhenUnset(t *testing.T) {
	dataDir := mobileTestEnv(t)
	t.Setenv("TMPDIR", "") // simulate Android: no temp dir configured

	if _, err := Start(dataDir); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := os.Getenv("TMPDIR")
	want := filepath.Join(dataDir, "tmp")
	if got != want {
		t.Fatalf("TMPDIR = %q, want %q", got, want)
	}
	f, err := os.CreateTemp("", "probe-*")
	if err != nil {
		t.Fatalf("os.CreateTemp in the exported TMPDIR: %v", err)
	}
	f.Close()
	os.Remove(f.Name())
}

// The counterpart guard: a host-configured TMPDIR is never overridden.
func TestStart_RespectsExistingTMPDIR(t *testing.T) {
	dataDir := mobileTestEnv(t)
	preset := t.TempDir()
	t.Setenv("TMPDIR", preset)

	if _, err := Start(dataDir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := os.Getenv("TMPDIR"); got != preset {
		t.Fatalf("TMPDIR = %q, want the preset %q left untouched", got, preset)
	}
}

// Review follow-up on ut-docs#1239: a TMPDIR pointing at a directory that
// no longer exists (a prior Start's own export after its dataDir was
// removed, or a host that rotated its dirs) must be treated as unset and
// re-exported — leaving it dangling reproduces exactly the ENOENT failure
// class the export exists to remove.
func TestStart_ReplacesStaleTMPDIR(t *testing.T) {
	dataDir := mobileTestEnv(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "deleted", "gone"))

	if _, err := Start(dataDir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, want := os.Getenv("TMPDIR"), filepath.Join(dataDir, "tmp"); got != want {
		t.Fatalf("TMPDIR = %q, want the fresh export %q", got, want)
	}
}

// ut-docs#1437: waitUntilReady's job is not "is the till healthy" — it's
// "is a listener up that the operator can act on", and recovery mode
// (internal/recovery.Serve) answers /healthz 503 by design for the entire
// time it's serving (ut-docs#1437/#1438's healthy-vs-unhealthy contract).
// Before this fix, waitUntilReady only accepted a 200 — a real boot-failure
// recovery mode on Android timed out after 30s ("mobile: server did not
// become ready within 30s") and the WebView never navigated, leaving the
// operator looking at a white page instead of the recovery screen with its
// reference code, Retry and safe-mode buttons.
func TestWaitUntilReady_RecoveryModeWithHeaderIsReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(recovery.HeaderMode, recovery.ModeRecovery)
		http.Error(w, "recovery mode", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	inst := &instance{done: make(chan struct{})}
	if err := waitUntilReady(strings.TrimPrefix(srv.URL, "http://"), 2*time.Second, inst); err != nil {
		t.Fatalf("waitUntilReady against a 503 recovery-mode response with %s: %s: %v", recovery.HeaderMode, recovery.ModeRecovery, err)
	}
}

// A bare 503 (no recovery-mode header — e.g. some other unhealthy state, or
// a future handler that doesn't set it) must NOT be treated as ready: it
// keeps polling until the timeout, same as before this change.
func TestWaitUntilReady_BareUnhealthy503KeepsPollingUntilTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	inst := &instance{done: make(chan struct{})}
	err := waitUntilReady(strings.TrimPrefix(srv.URL, "http://"), 700*time.Millisecond, inst)
	if err == nil {
		t.Fatal("expected waitUntilReady to time out against a bare 503 with no recovery-mode header")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "did not become ready")
	}
}

// The existing, unchanged case: a healthy 200 is ready regardless of any
// header.
func TestWaitUntilReady_Healthy200IsReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	inst := &instance{done: make(chan struct{}), bound: make(chan bindReport, 1)}
	inst.bound <- bindReport{addr: srv.Listener.Addr().String()}
	if err := waitUntilReady(strings.TrimPrefix(srv.URL, "http://"), 2*time.Second, inst); err != nil {
		t.Fatalf("waitUntilReady against a healthy 200: %v", err)
	}
}

// ut-docs#3290 review: our server reports its bind before it serves, so a
// 200 with no bind report yet came from whatever holds the port. It must not
// count as ready — the till would persist and hand the WebView a stranger's
// address.
func TestWaitUntilReady_200WithoutABindReportIsNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	inst := &instance{done: make(chan struct{}), bound: make(chan bindReport, 1)}
	if err := waitUntilReady(strings.TrimPrefix(srv.URL, "http://"), 700*time.Millisecond, inst); err == nil {
		t.Fatal("a 200 with no bind report was treated as ready")
	}
}

// End-to-end: internal/app.Run's real boot-failure recovery mode, driven
// through mobile.Start, must return a usable address instead of timing out
// — this is the actual ut-docs#1437 regression, not just the unit-level
// waitUntilReady behavior above. Corrupts the version-1 migration ledger
// checksum (same shape as internal/db/migration_drift_test.go's own drift
// test) so db.Open fails at boot with a real, reproducible drift error —
// internal/app's boot loop classifies that as recoverable (internal/recovery.Classify)
// and enters recovery mode instead of exiting.
func TestStart_BootFailureServesRecoveryModeInsteadOfTimingOut(t *testing.T) {
	dataDir := mobileTestEnv(t)
	dbPath := filepath.Join(dataDir, "unitill-pos.db")

	// A real, fully-migrated database first — db.Open records the ledger
	// this test then corrupts.
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if _, err := seed.Exec(`UPDATE schema_migrations SET checksum = ? WHERE version = 1`, strings.Repeat("0", 64)); err != nil {
		t.Fatalf("corrupt the version-1 ledger checksum: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v (recovery mode should make this succeed, not time out or error)", err)
	}
	if addr == "" {
		t.Fatal("expected a non-empty address even though boot failed into recovery mode")
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/healthz status = %d, want %d (recovery mode)\nbody: %s", resp.StatusCode, http.StatusServiceUnavailable, body)
	}
	if got := resp.Header.Get(recovery.HeaderMode); got != recovery.ModeRecovery {
		t.Fatalf("/healthz %s header = %q, want %q", recovery.HeaderMode, got, recovery.ModeRecovery)
	}

	pageResp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	pageBody, _ := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	page := string(pageBody)
	// id="retry-btn" is the recovery page's own stable, non-i18n-translated
	// anchor (internal/recovery/templates/recovery.html) — asserting on it
	// proves the recovery SCREEN rendered, not just some other 200/503 page.
	if !strings.Contains(page, `id="retry-btn"`) {
		t.Fatalf("GET / did not render the recovery page (missing retry-btn)\nbody: %s", page)
	}

	Stop()
	if IsRunning() {
		t.Fatal("expected IsRunning() to be false after Stop")
	}
}

// ut-docs#2722: an Android main till used to pick a fresh ephemeral port on
// every launch, stranding every paired replica. Start must persist the port
// it served on and come back on the SAME port next launch.
func TestStart_PersistsPortAndReusesItNextLaunch(t *testing.T) {
	dataDir := mobileTestEnv(t)

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, port, _ := net.SplitHostPort(addr)
	if want := strconv.Itoa(defaultListenPort); port != want {
		t.Fatalf("first launch served on %s, want the default %s (free, so no fallback)", port, want)
	}
	if got := listenport.Saved(dataDir); strconv.Itoa(got) != port {
		t.Fatalf("persisted port = %d, want %s", got, port)
	}

	Stop()
	// A different default next time proves the second launch reads the
	// persisted value rather than just recomputing the same default.
	defaultListenPort++
	addr2, err := Start(dataDir)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if _, port2, _ := net.SplitHostPort(addr2); port2 != port {
		t.Fatalf("second launch served on %s, want the persisted %s", port2, port)
	}
}

func TestStart_PersistedPortBusy_FallsBackAndPersistsTheNewOne(t *testing.T) {
	dataDir := mobileTestEnv(t)
	holder, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("hold a port: %v", err)
	}
	defer holder.Close()
	_, heldStr, _ := net.SplitHostPort(holder.Addr().String())
	held, _ := strconv.Atoi(heldStr)
	if err := listenport.Save(dataDir, held); err != nil {
		t.Fatal(err)
	}

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	if port == held {
		t.Fatalf("served on the busy port %d", held)
	}
	if got := listenport.Saved(dataDir); got != port {
		t.Fatalf("persisted port = %d, want the fallback %d actually served on", got, port)
	}
}

// ut-docs#3220: on iOS/Android "Restart now" (after joining a shop, or after
// staging a backup restore) must really restart the till in-process — the
// re-exec procrestart uses on desktop can't work inside an app. The mobile
// package registers its restarter at init, so procrestart must report the
// restart as supported, and a Restart() must bring the server back on the
// SAME address with the staged snapshot applied (the joined shop's data is
// there), exactly what the WebView's /healthz poll then reloads into.
func TestProcrestart_RestartsInProcessAndAppliesStagedRestore(t *testing.T) {
	dataDir := mobileTestEnv(t)
	dbPath := filepath.Join(dataDir, "unitill-pos.db")
	t.Setenv("UT_DB_PATH", dbPath)
	// InProcess, not just Supported: Supported is true on linux anyway, and
	// without the registration Restart would really re-exec the test binary.
	if !procrestart.InProcess() || !procrestart.Supported() {
		t.Fatal("the mobile package did not register its in-process restarter with procrestart")
	}

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The "joined shop": a separate database carrying a marker setting,
	// staged the way completeJoin stages a primary's snapshot.
	snapPath := filepath.Join(t.TempDir(), "joined.db")
	snap, err := db.Open(snapPath)
	if err != nil {
		t.Fatalf("open snapshot db: %v", err)
	}
	if err := data.NewSettingsRepo(snap.DB).Set(context.Background(), "test.joined_marker", "joined-shop"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	snap.Close()
	f, err := os.Open(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.StageRestoreFromReader(dbPath, f); err != nil {
		t.Fatalf("stage restore: %v", err)
	}
	f.Close()
	if !db.PendingRestore(dbPath) {
		t.Fatal("restore not staged")
	}

	procrestart.Restart()

	// The staged file is consumed only by a fresh startup; then the server
	// must answer again on the same address.
	deadline := time.Now().Add(60 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for {
		if !db.PendingRestore(dbPath) && IsRunning() {
			if resp, err := client.Get("http://" + addr + "/healthz"); err == nil {
				ok := resp.StatusCode == http.StatusOK
				resp.Body.Close()
				if ok {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("till never came back on %s with the staged restore applied (pending=%v running=%v)",
				addr, db.PendingRestore(dbPath), IsRunning())
		}
		time.Sleep(100 * time.Millisecond)
	}

	Stop()
	restored, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen restored db: %v", err)
	}
	defer restored.Close()
	v, ok, err := data.NewSettingsRepo(restored.DB).Get(context.Background(), "test.joined_marker")
	if err != nil || !ok || v != "joined-shop" {
		t.Fatalf("after restart the joined shop's marker = %q (found=%v, err=%v), want joined-shop", v, ok, err)
	}
}

// A restart with no running server fails cleanly instead of starting one
// from nowhere.
func TestRestartInProcess_NotRunningErrors(t *testing.T) {
	mobileTestEnv(t)
	if err := restartInProcess(); err == nil {
		t.Fatal("restartInProcess with no server running returned nil")
	}
	if IsRunning() {
		t.Fatal("restartInProcess started a server although none was running")
	}
}

// Native callers must never race an in-process restart (review finding,
// ut-docs#3220): while it runs, IsRunning reports true (iOS's resume probe
// won't start a second restart), and Start/Stop wait for it to finish.
func TestRestartInProcess_NativeCallsWaitForIt(t *testing.T) {
	dataDir := mobileTestEnv(t)
	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- restartInProcess() }()
	// Catch the restart in flight.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		inFlight := restarting != nil
		mu.Unlock()
		if inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restart never observed in flight")
		}
		time.Sleep(time.Millisecond)
	}
	if !IsRunning() {
		t.Fatal("IsRunning() = false during an in-process restart; iOS would start a racing restart")
	}
	addr2, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start during restart: %v", err)
	}
	if addr2 != addr {
		t.Fatalf("Start during restart = %q, want the restarted server's %q", addr2, addr)
	}
	if err := <-done; err != nil {
		t.Fatalf("restartInProcess: %v", err)
	}
	if !IsRunning() {
		t.Fatal("server not running after the restart")
	}
}

// stealChosenPort makes afterPortChosen take each chosen port on every
// interface before app.Run binds it — the probe-then-bind gap ut-docs#3290
// found on main CI (another package's test took the port), and that another
// app can hit on a real phone. It steals the first `times` ports (-1 = all)
// and returns how many ports Start chose. With serveHTTP the thief answers
// every request 200 (header X-Foreign), like a stray local web server.
func stealChosenPort(t *testing.T, times int, serveHTTP bool) *int {
	t.Helper()
	chosen := 0
	var held []net.Listener
	afterPortChosen = func(port string) {
		chosen++
		if times >= 0 && chosen > times {
			return
		}
		ln, err := net.Listen("tcp", "0.0.0.0:"+port)
		if err != nil {
			t.Errorf("steal port %s: %v", port, err)
			return
		}
		held = append(held, ln)
		if serveHTTP {
			go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:errcheck // ends when ln closes
				w.Header().Set("X-Foreign", "1")
				w.WriteHeader(http.StatusOK)
			}))
		}
	}
	t.Cleanup(func() {
		afterPortChosen = func(string) {}
		for _, ln := range held {
			ln.Close()
		}
	})
	return &chosen
}

func TestStart_PortTakenBeforeBind_RetriesOnAFreshPortAndPersistsIt(t *testing.T) {
	dataDir := mobileTestEnv(t)
	chosen := stealChosenPort(t, 1, false)
	stolen := strconv.Itoa(defaultListenPort) // nothing persisted: the first choice is the default

	began := time.Now()
	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Fatalf("Start took %s: it waited on the stolen port instead of noticing the moved bind", took)
	}
	host, port, _ := net.SplitHostPort(addr)
	if host != "127.0.0.1" || port == stolen {
		t.Fatalf("Start returned %s, want 127.0.0.1 on a port other than the stolen %s", addr, stolen)
	}
	if *chosen != 2 {
		t.Fatalf("Start chose %d ports, want 2 (the stolen one, then one retry)", *chosen)
	}
	if got := listenport.Saved(dataDir); strconv.Itoa(got) != port {
		t.Fatalf("persisted port = %d, want %s, the port actually served on", got, port)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz on the returned address: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}
}

func TestStart_PortTakenBeforeEveryBind_FailsFast(t *testing.T) {
	dataDir := mobileTestEnv(t)
	chosen := stealChosenPort(t, -1, false)

	began := time.Now()
	addr, err := Start(dataDir)
	if err == nil {
		t.Fatalf("Start returned %s, want an error: every chosen port was taken", addr)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Fatalf("Start took %s to fail; it should give up as soon as each bind moves", took)
	}
	if *chosen != maxBindAttempts {
		t.Fatalf("Start chose %d ports, want %d attempts", *chosen, maxBindAttempts)
	}
	if !strings.Contains(err.Error(), "moved") {
		t.Fatalf("error %q should say the bind moved", err)
	}
	if got := listenport.Saved(dataDir); got != 0 {
		t.Fatalf("persisted port %d after a failed Start, want none", got)
	}
}

// ut-docs#3290 review: whatever took the port may answer /healthz 200 itself
// (another package's httptest server on CI, a local server on a phone).
// Start must still move to a fresh port, not return the stranger's address.
func TestStart_PortTakenByAnHTTPServer_RetriesInsteadOfTrustingIt(t *testing.T) {
	dataDir := mobileTestEnv(t)
	chosen := stealChosenPort(t, 1, true)
	stolen := strconv.Itoa(defaultListenPort)

	addr, err := Start(dataDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, port, _ := net.SplitHostPort(addr); port == stolen {
		t.Fatalf("Start returned the stolen port %s, answered by the port's other holder", stolen)
	}
	if *chosen != 2 {
		t.Fatalf("Start chose %d ports, want 2", *chosen)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Foreign") != "" {
		t.Fatal("the returned address is served by the port's other holder, not the till")
	}
}
