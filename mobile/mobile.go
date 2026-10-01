// Package mobile is the gomobile-bind entry point for the Android/iOS till
// shells (ADR-0023, ut-docs): starts the same server internal/app.Run drives
// for the CLI/desktop build, in-process — a mobile app can't spawn a sibling
// binary the way cmd/unitill-desktop's WebView shell spawns unitill-pos as a
// child process, so this package drives the identical boot sequence directly
// instead. Once running, the native shell points its own WebView at the
// returned loopback address — same "start, poll until it answers, then show
// a window" shape as cmd/unitill-desktop/desktop.go, just in-process instead
// of a child process.
//
// The server itself binds ALL interfaces (0.0.0.0), not just loopback
// (ut-docs#1256): a phone/tablet till is then LAN-reachable exactly like a
// Linux/Pi SERVICE till (the bare unitill-pos binary, config default
// UT_LISTEN_ADDR=:8080 — NOT cmd/unitill-desktop's WebView shell, which
// stays loopback-only, its own separate design) — it advertises over mDNS
// (internal/discovery) and other tills can discover it and pair with it as
// their primary. The security boundary is the one ADR-0033 already ships
// for every other platform, unchanged: approve-to-pair with a verification
// code (internal/pages/pairing_api.go) and a per-till bearer token on the
// /api/sync/* surface (internal/pages/sync_api.go). ADR-0023's original
// loopback-only bind was a conservative placeholder, not a considered
// permanent restriction. What the native shell receives from Start is
// still the loopback address — the WebView contract did not change.
//
// gomobile bind's cross-language boundary only supports a narrow set of
// types (strings, ints, bools, []byte, error, and a few others — no generics,
// no complex struct fields crossing directly) — this package's exported
// surface is deliberately minimal for that reason: three lifecycle
// functions plus one setter, SetBluetoothBridge, whose argument is the one
// exported interface type declared IN this package, BluetoothBridge
// (ADR-0080). It must be declared here rather than merely referenced from
// internal/bluetooth: gobind only binds types from the package named on its
// command line, so a type from an unbound package compiles and builds a
// green .aar while silently dropping the function that referenced it (see
// BluetoothBridge's own doc comment for how this was found). The interface
// is itself built from bind-safe types only (string/int64/error), and the
// Go-interface-implemented-in-Kotlin "callback" shape is gomobile's
// standard mechanism for a Go handler that needs to reach native Android
// state — it is the pattern any future till-needs-native-hardware
// integration reuses (ADR-0080 §4), each declaring its own bind-local
// interface the same way.
//
// This package mutates PROCESS-WIDE environment variables (os.Setenv) to
// configure internal/app.Run, the same way the CLI's own pos.env/shell
// environment does. That's fine for a mobile app where this package owns
// the whole process, but means this is NOT safe to embed alongside other
// Go code that also touches os.Setenv/os.Environ() concurrently outside
// this package's own synchronization.
//
// Build (needs the Android SDK+NDK or Xcode, per platform — see
// ut-docs/adr/0023-android-ios-till-strategy.md for the full setup):
//
//	gomobile bind -target=android ./mobile
//	gomobile bind -target=ios ./mobile
package mobile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/app"
	"github.com/universaltill/universal-till/internal/bluetooth"
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/listenport"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/procrestart"
	"github.com/universaltill/universal-till/internal/recovery"
	"github.com/universaltill/universal-till/internal/server"
)

// instance is one running server lifecycle. Start/Stop/IsRunning agree on
// current state by swapping this pointer under mu — never by holding mu
// across the blocking work (starting or stopping the actual server), so a
// slow Start can never make IsRunning/Stop block for the whole timeout.
type instance struct {
	dataDir string
	addr    string
	cancel  context.CancelFunc
	done    chan struct{} // closed once app.Run has fully returned
	err     error         // app.Run's result; only valid after done is closed
	// bound receives the server's one bind report (ut-docs#3290). nil in
	// the recovery-mode unit tests: a nil channel never fires.
	bound chan bindReport
	// listenAddr is the address runOnce asked the server to bind.
	listenAddr string
}

// bindReport is what server.WithBoundAddr told runOnce about the bind.
type bindReport struct {
	addr  string
	moved bool
}

var (
	mu       sync.Mutex
	inst     *instance
	starting bool
	// restarting is non-nil (and closed when done) while restartInProcess
	// runs: Start and Stop wait for it and IsRunning reports true, so a
	// native caller (iOS TillController.resume's probe, Android's
	// TillService) never races the restart's own Stop/Start (ut-docs#3220).
	restarting chan struct{}
)

// Start boots the till server with its on-device data directory at dataDir
// (the native shell resolves this to whatever platform-appropriate sandboxed
// storage path it has — Android's Context.getFilesDir(), iOS's
// NSSearchPathForDirectoriesInDomains(.applicationSupportDirectory), or
// similar) and returns "127.0.0.1:<port>" once the server is confirmed
// accepting connections — ready for a WebView to load. That address may be
// serving the boot-failure recovery screen rather than the till itself
// (ut-docs#1437): a shell must load it either way and must not read a
// returned address as "healthy" — see waitUntilReady for the distinction.
//
// The returned address is what the in-process WebView loads, NOT what the
// server binds: the bind is "0.0.0.0:<port>" — every interface, so the till
// is reachable from the LAN for ADR-0033 discovery/pairing/sync
// (ut-docs#1256; see the package comment). A wildcard bind still accepts
// loopback connections, so the loopback address the caller gets keeps
// working exactly as it did when the bind itself was loopback-only.
//
// The port is stable across launches (ut-docs#2722, chooseListenPort): the
// one served on last time (persisted in dataDir), else 8080, moving only if
// that is genuinely busy — paired replicas store this till's host:port.
//
// mDNS on Android (ut-docs#1256 gap, closed by ut-docs#2722): many Android
// Wi-Fi drivers drop inbound multicast an app hasn't asked to receive, so
// the native shell's TillService holds a WifiManager MulticastLock
// (CHANGE_WIFI_MULTICAST_STATE) while the server runs — without it an
// Android main till can't answer a replica searching for it after it moved.
// Still needs a real on-device check per device model; direct-IP sync and
// the pairing-code fallback work regardless.
//
// Idempotent while genuinely running: a second Start call with the same
// dataDir just returns the existing address (mirrors cmd/unitill-desktop's
// tillAlreadyRunning re-attach behavior — useful if the native shell's
// lifecycle calls Start more than once, e.g. iOS returning from
// background). A second Start with a DIFFERENT dataDir while already
// running is an error, not a silent ignore — the caller asked for
// something this process can't currently give it. If the server died on
// its own since the last successful Start (e.g. a listener error, not a
// Stop() call), Start does not lie about still being up — it notices via
// the dead instance's closed done channel and starts fresh instead of
// returning a stale address.
func Start(dataDir string) (string, error) {
	mu.Lock()
	waitForRestartLocked()
	mu.Unlock()
	return start(dataDir)
}

// start is Start without waiting for an in-process restart — the restart
// itself calls it.
func start(dataDir string) (string, error) {
	mu.Lock()
	if inst != nil {
		select {
		case <-inst.done:
			inst = nil // died on its own; fall through and start fresh
		default:
			if inst.dataDir != dataDir {
				addr := inst.addr
				mu.Unlock()
				return "", fmt.Errorf("mobile: already running against %q, cannot Start against %q (current address %s)", inst.dataDir, dataDir, addr)
			}
			addr := inst.addr
			mu.Unlock()
			return addr, nil
		}
	}
	if starting {
		mu.Unlock()
		return "", fmt.Errorf("mobile: Start already in progress")
	}
	starting = true
	mu.Unlock()
	defer func() {
		mu.Lock()
		starting = false
		mu.Unlock()
	}()

	if err := os.Setenv("UT_DATA_DIR", dataDir); err != nil {
		return "", fmt.Errorf("mobile: set UT_DATA_DIR: %w", err)
	}
	// ut-docs#1239 (real device, 2026-08-28): an unrooted Android app has
	// no writable default temp directory — TMPDIR is unset and Go's
	// os.TempDir() fallback (/data/local/tmp) is not writable by an app
	// uid — so the Go-side os.CreateTemp callers (CSV/catalogue upload
	// staging, .bkp import, self-update) all die with an I/O error. Give
	// the process a temp dir inside its own sandbox. SQLite is NOT what
	// this protects: its temp-table needs are closed at the source by
	// temp_store=MEMORY in internal/db, and modernc's libc snapshots the
	// environment on first use, so a TMPDIR exported here may never reach
	// it anyway (independent review, 2026-08-28).
	//
	// A TMPDIR pointing at a directory that no longer exists counts as
	// unset — both for a host that rotated its dirs and for this very
	// export gone stale after a Stop/Start against a new dataDir (the
	// first draft's ==""-only guard left that stale path in place, and on
	// a TMPDIR-less machine it also poisoned os.TempDir() for the whole
	// process — every later os.CreateTemp/testing.TempDir failed with
	// ENOENT; same review).
	if cur := os.Getenv("TMPDIR"); cur == "" || !isDir(cur) {
		tmpDir := filepath.Join(dataDir, "tmp")
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			return "", fmt.Errorf("mobile: create temp dir: %w", err)
		}
		if err := os.Setenv("TMPDIR", tmpDir); err != nil {
			return "", fmt.Errorf("mobile: set TMPDIR: %w", err)
		}
	}
	// The native shell supplies its own window/WebView; the server must
	// never try to open an OS browser on its own (same reasoning as
	// cmd/unitill-desktop/desktop.go's identical env var).
	if err := os.Setenv("UT_OPEN_BROWSER", "0"); err != nil {
		return "", fmt.Errorf("mobile: set UT_OPEN_BROWSER: %w", err)
	}
	// Bug report, 2026-07-28 (real device): the plugin store showed
	// "Marketplace: not connected" and registration failed with "dial tcp
	// 127.0.0.1:8081: connection refused". Root cause: config.Init's
	// UT_MARKETPLACE_ENDPOINT_URL default (internal/config/config.go) is a
	// dev-only loopback pointing at scripts/mock-marketplace, meant to be
	// overridden by a bundled pos.env in real distributions — the desktop
	// installers get this from packaging/pos.env.example
	// (UT_MARKETPLACE_ENDPOINT_URL=https://cloud.universaltill.com/api),
	// but nothing sets it for Android: there's no pos.env on a fresh phone
	// install, and this file never set it either. Only set it if the host
	// process doesn't already have one configured (e.g. a future
	// dev/staging override from the native Kotlin layer), same
	// don't-override-explicit-config posture as enroll.go.
	if os.Getenv("UT_MARKETPLACE_ENDPOINT_URL") == "" {
		if err := os.Setenv("UT_MARKETPLACE_ENDPOINT_URL", "https://cloud.universaltill.com/api"); err != nil {
			return "", fmt.Errorf("mobile: set UT_MARKETPLACE_ENDPOINT_URL: %w", err)
		}
	}

	port, err := chooseListenPort(dataDir)
	if err != nil {
		return "", fmt.Errorf("mobile: find a free port: %w", err)
	}
	// ut-docs#3290: the port is probed free and then bound later by
	// app.Run, so something can take it in between (another package's test
	// on CI, another app on a phone). The server then falls back to a
	// loopback-only address on another port (internal/server,
	// ut-docs#1169), which is neither LAN-reachable nor the port being
	// polled. runOnce notices the moved bind at once; retry on a fresh
	// OS-chosen port instead of waiting out the 30s timeout.
	for attempt := 1; ; attempt++ {
		afterPortChosen(port)
		newInst, err := runOnce(dataDir, port)
		if err == nil {
			// ut-docs#2722: remember the port we actually served on, so the
			// next launch comes back on it and paired replicas keep reaching
			// this till. Best-effort: failing to persist only costs
			// stability, never startup.
			if p, perr := strconv.Atoi(port); perr == nil {
				if err := listenport.Save(dataDir, p); err != nil {
					fmt.Fprintf(os.Stderr, "mobile: could not persist listen port %d: %v\n", p, err)
				}
			}
			mu.Lock()
			inst = newInst
			mu.Unlock()
			return newInst.addr, nil
		}
		var moved *bindMovedError
		if !errors.As(err, &moved) {
			return "", err
		}
		if attempt >= maxBindAttempts {
			return "", fmt.Errorf("mobile: gave up after %d ports were each taken before the bind: %w", maxBindAttempts, err)
		}
		if port, err = freePort(); err != nil {
			return "", fmt.Errorf("mobile: find a free port: %w", err)
		}
	}
}

// maxBindAttempts caps how many ports start tries when each one is taken
// between choosing it and binding it (ut-docs#3290).
const maxBindAttempts = 3

// afterPortChosen runs between choosing a port and app.Run binding it. A
// test seam only: tests take the port here to reproduce ut-docs#3290.
var afterPortChosen = func(port string) {}

// runApp is app.Run. A test seam only: tests swap in a func that panics to
// prove the goroutine recovers and still closes done (ut-docs#3313).
var runApp = app.Run

// bindMovedError is runOnce's report that the server bound somewhere other
// than 0.0.0.0:<port> — start retries on a fresh port.
type bindMovedError struct{ want, got string }

func (e *bindMovedError) Error() string {
	return fmt.Sprintf("mobile: port taken before the server bound it: asked for %s, bind moved to %s", e.want, e.got)
}

// runOnce boots app.Run on port and waits until it is ready. On any error
// the instance is already torn down.
func runOnce(dataDir, port string) (*instance, error) {
	// Two addresses, one port (ut-docs#1256): listenAddr is what the server
	// binds — all interfaces, so the till is LAN-reachable and ADR-0033's
	// discovery + approve-to-pair + bearer-token model applies to it the
	// same way it already does to a desktop/Pi till. localAddr is the
	// loopback view of the same port: what waitUntilReady polls and what
	// the native shell gets back for its WebView, unchanged from before.
	listenAddr := "0.0.0.0:" + port
	localAddr := "127.0.0.1:" + port
	if err := os.Setenv("UT_LISTEN_ADDR", listenAddr); err != nil {
		return nil, fmt.Errorf("mobile: set UT_LISTEN_ADDR: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	newInst := &instance{dataDir: dataDir, addr: localAddr, listenAddr: listenAddr, cancel: cancel, done: make(chan struct{}), bound: make(chan bindReport, 1)}
	ctx = server.WithBoundAddr(ctx, func(actual string, moved bool) {
		select {
		case newInst.bound <- bindReport{addr: actual, moved: moved}:
		default: // one bind per app.Run; never block the server on it
		}
	})

	go func() {
		defer logging.RecoverAndLog("mobile.run")
		defer close(newInst.done) // runs first on unwind, so waiters never hang on a panic
		newInst.err = runApp(ctx)
	}()

	// 10s was too tight in practice: a real low/mid-range Android phone's
	// first boot (fresh install: 18 migrations against a cold SQLite file,
	// plus wazero compiling any bundled plugins' WASM with no cache yet)
	// missed it and surfaced as "server did not become ready within 10s"
	// (reported 2026-07-28, real device). Desktop's equivalent wait
	// (cmd/unitill-desktop/desktop.go) made the same ~10s assumption, but
	// phone hardware skews weaker and more variable than desktop/POS
	// terminal hardware, so mobile gets more headroom than desktop rather
	// than copying its number.
	if err := waitUntilReady(localAddr, 30*time.Second, newInst); err != nil {
		cancel()
		<-newInst.done // wait for the full teardown before reporting failure
		return nil, err
	}
	return newInst, nil
}

// waitForRestartLocked blocks, with mu held on entry and on return, until
// no in-process restart is running.
func waitForRestartLocked() {
	for restarting != nil {
		ch := restarting
		mu.Unlock()
		<-ch
		mu.Lock()
	}
}

// The app can't re-exec itself (iOS forbids exec; on Android it would
// replace the whole app process), so "Restart now" after joining a shop or
// staging a backup restore restarts the server in-process instead
// (ut-docs#3220): procrestart calls restartInProcess in place of its
// re-exec, and Supported() is true only because of this registration.
func init() {
	procrestart.SetRestarter(restartInProcess)
}

// restartInProcess stops the running server and starts it again on the
// same data dir. Start reuses the persisted port (just released by Stop)
// and runs app.Run's startup path again, which applies a staged restore
// (db.ApplyPendingRestore) — so the WebView's /healthz poll on the same
// address reloads straight into the restored till.
func restartInProcess() error {
	mu.Lock()
	if restarting != nil {
		mu.Unlock()
		return errors.New("mobile: a restart is already in progress")
	}
	cur := inst
	if cur == nil {
		mu.Unlock()
		return errors.New("mobile: restart requested but the till server is not running")
	}
	done := make(chan struct{})
	restarting = done
	mu.Unlock()
	defer func() {
		mu.Lock()
		restarting = nil
		close(done)
		mu.Unlock()
	}()
	stop()
	if _, err := start(cur.dataDir); err != nil {
		return fmt.Errorf("mobile: restart: %w", err)
	}
	return nil
}

// Stop gracefully shuts the server down and BLOCKS until it has actually
// finished — including internal/server.Start's own shutdown drain and
// internal/app.Run's deferred database.Close() — not just until the
// shutdown signal has been sent. This matters: a native shell that calls
// Stop() and then quickly Start()s again against the SAME on-device
// dataDir (e.g. an app backgrounded then rapidly foregrounded) must not
// race the old instance's in-flight teardown against the new instance's
// db.Open() on the same SQLite file. Safe to call when not running
// (no-op). A caller wanting non-blocking behavior should call this from
// its own background thread/coroutine, same as any blocking mobile API.
func Stop() {
	mu.Lock()
	waitForRestartLocked()
	mu.Unlock()
	stop()
}

// stop is Stop without waiting for an in-process restart.
func stop() {
	mu.Lock()
	cur := inst
	inst = nil
	mu.Unlock()
	if cur == nil {
		return
	}
	cur.cancel()
	<-cur.done
}

// IsRunning reports whether a Start'd server is genuinely still up — not
// just whether Start was last called without a matching Stop. If the
// server died on its own (closed done channel, no Stop() call), this
// notices and corrects the tracked state instead of reporting stale
// success.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	if restarting != nil {
		return true // coming straight back; a native restart now would race it
	}
	if inst == nil {
		return false
	}
	select {
	case <-inst.done:
		inst = nil
		return false
	default:
		return true
	}
}

// BluetoothBridge is the gomobile-bind-visible mirror of
// bluetooth.AndroidBridge, declared HERE rather than referenced from
// internal/bluetooth (ADR-0080 amendment, review finding on ut-docs#1721:
// an earlier draft declared only bluetooth.AndroidBridge and referenced it
// from this package's SetBluetoothBridge signature — that compiled, bound,
// and built a green .aar, but gobind only generates Java/Kotlin bindings
// for types declared in the package named on its `gomobile bind` command
// line (`./mobile`, per android/app/build.gradle.kts's generateAar task);
// a parameter type from an unbound package is silently dropped — verified
// empirically: gobind emits "skipped function SetBluetoothBridge with
// unsupported parameter or return types" and still exits 0, so every gate
// stayed green while the method Kotlin needed simply didn't exist).
//
// Kept structurally identical to bluetooth.AndroidBridge on purpose: Go
// interface satisfaction is structural, so SetBluetoothBridge below passes
// its argument straight to bluetooth.SetAndroidBridge with no adapter, and
// the two types staying in lockstep is enforced by internal/bluetooth's own
// androidBridgeClient tests (which use bluetooth.AndroidBridge directly) —
// a mismatch between the two shapes is a compile error at that call site,
// not a runtime surprise.
type BluetoothBridge interface {
	// ListDevices returns the devices currently bonded with this device's
	// adapter, JSON-encoded as a []Device (see bluetooth.Device).
	ListDevices() (devicesJSON string, err error)
	// Scan runs one bounded discovery of at most timeoutMillis and returns
	// the not-yet-bonded devices seen, JSON-encoded as a []Device.
	Scan(timeoutMillis int64) (devicesJSON string, err error)
	// Pair bonds (and connects) the device with this address.
	Pair(address string) error
	// Forget removes the bond for the device with this address.
	Forget(address string) error
}

// SetBluetoothBridge registers Android's own Bluetooth stack with
// internal/bluetooth (ADR-0080, ut-docs#1721). Kotlin calls it exactly once,
// from TillService.kt, right after Mobile.start() succeeds, passing an
// object implementing the gomobile-generated Java/Kotlin binding of
// BluetoothBridge above (BluetoothAdapter/BluetoothLeScanner behind it).
// From then on the Bluetooth devices page's bluetooth.NewDBusClient hands
// out a Client that forwards to that bridge on Android, instead of the
// ErrUnsupportedPlatform it returns while no bridge is registered
// (ut-docs#1643). Passing nil un-registers, restoring that default.
//
// The Kotlin implementation now calls this with a real implementation
// (TillService.kt:115, Mobile.setBluetoothBridge(BluetoothBridgeImpl(...)));
// its BLUETOOTH_SCAN/BLUETOOTH_CONNECT permission flow and on-device
// verification were the separate follow-up ut-docs#1731.
func SetBluetoothBridge(b BluetoothBridge) {
	bluetooth.SetAndroidBridge(b)
}

// SetDeviceModel registers the on-device hardware model string (Android's
// Build.MODEL) with internal/diagnostics (ut-docs#2235), mirroring
// SetBluetoothBridge's shape above. Kotlin calls it exactly once, from
// TillService.kt, before Mobile.start(), so the very first diagnostics
// inventory event this boot already carries the real model instead of the
// "" every build reported before this existed. Passing "" clears it,
// restoring that default.
func SetDeviceModel(model string) {
	diagnostics.SetDeviceModel(model)
}

// defaultListenPort is the port a till with nothing persisted asks for
// first. A var only so tests can point it at a port that is free on the
// machine running them.
var defaultListenPort = listenport.DefaultPort

// chooseListenPort picks a STABLE port (ut-docs#2722): the port this till
// served on last launch, else the well-known default, moving up only when
// that one is genuinely busy — and only letting the OS pick a random port
// as the very last resort. Before #2722 this was always freePort(), a fresh
// ephemeral port per launch, and every restart of an Android main till left
// its paired replicas knocking on a dead port.
func chooseListenPort(dataDir string) (string, error) {
	if p := listenport.Choose(dataDir, defaultListenPort, listenport.Bindable); p != 0 {
		return strconv.Itoa(p), nil
	}
	return freePort()
}

// freePort asks the OS for an unused port, probed on ALL interfaces
// (0.0.0.0), matching exactly what Start binds it on (ut-docs#1256 review
// finding: probing loopback-only while binding 0.0.0.0 elsewhere meant a
// port merely free on loopback but already held on some other interface
// would pass this probe and then reliably fail the real bind — not the
// harmless microsecond TOCTOU race this comment used to describe. That
// failure is worse than a retry: internal/server.listenWithFallback
// deliberately degrades a wildcard bind failure to 127.0.0.1 (ut-docs#1169),
// silently undoing this whole feature, on a different port than
// waitUntilReady is polling). Probing 0.0.0.0:0 instead makes the probed
// port free on every interface, which is exactly what the real bind needs,
// closing the mismatch. The probe-then-bind gap itself remains: start
// detects a bind that moved anyway and retries (ut-docs#3290).
func freePort() (string, error) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	return port, err
}

// waitUntilReady polls addr's /healthz until a listener the operator can
// act on is up, timeout elapses, or app.Run itself already exited (a fast,
// fatal startup failure — e.g. a bad config — shouldn't make the caller
// wait out the full timeout to find out).
//
// "Ready" here is deliberately NOT the same thing as "healthy" (ut-docs#1437):
// a healthy till answers /healthz 200, but internal/app.Run can also boot
// straight into recovery mode (ADR-0075) on a startup failure an operator
// can plausibly fix — migrations, a corrupt DB file, disk full — and
// internal/recovery's healthHandler deliberately keeps answering 503 for
// the entire time recovery mode is serving, so every shell's
// healthy-vs-unhealthy lock/exit-gating logic (ut-docs#1437, #1438) stays
// unchanged. Before this fix, waitUntilReady only accepted a 200, so a real
// boot-failure recovery mode on Android timed out after the full 30s
// ("mobile: server did not become ready within 30s") and the WebView never
// navigated — the operator saw a white page instead of the recovery
// screen's reference code, Retry and safe-mode buttons (seen on a real
// tablet, ut-docs#1437). A response is now also treated as ready when it's
// a 503 carrying recovery.HeaderMode: recovery.ModeRecovery — recovery
// mode's own signal that a listener is up and actionable, distinct from
// /healthz's unchanged health signal. Any other response (a bare 503,
// connection refused, etc.) keeps polling until the timeout, same as
// today.
//
// Start polls the loopback address even though the server binds 0.0.0.0
// (ut-docs#1256): a wildcard bind answers on loopback, and 0.0.0.0 is not a
// portable DIAL target.
//
// A bind report on inst.bound that says the bind moved off 0.0.0.0:<port>
// ends the wait at once with a *bindMovedError (ut-docs#3290): nothing will
// ever answer on addr, and start retries on a fresh port.
func waitUntilReady(addr string, timeout time.Duration, inst *instance) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	sawBind := false
	for time.Now().Before(deadline) {
		select {
		case <-inst.done:
			if inst.err != nil {
				return fmt.Errorf("mobile: server failed to start: %w", inst.err)
			}
			return fmt.Errorf("mobile: server exited before becoming ready")
		case b := <-inst.bound:
			if b.moved {
				return &bindMovedError{want: inst.listenAddr, got: b.addr}
			}
			sawBind = true
		default:
		}
		if resp, err := client.Get("http://" + addr + "/healthz"); err == nil {
			// A 200 counts only after our own server reported its bind: it
			// reports before it serves, so a 200 before that came from
			// whatever holds the port (ut-docs#3290 review). Recovery mode
			// never reports a bind; its header is its own proof.
			ready := (resp.StatusCode == http.StatusOK && sawBind) ||
				(resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get(recovery.HeaderMode) == recovery.ModeRecovery)
			resp.Body.Close()
			if ready {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("mobile: server did not become ready within %s", timeout)
}

// isDir reports whether path exists and is a directory — the TMPDIR
// validity check Start uses to treat a stale export as unset.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
