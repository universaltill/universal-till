package bluetooth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AndroidBridge is the Go-side view of Android's own Bluetooth stack,
// implemented in Kotlin and registered via SetAndroidBridge (ADR-0080,
// ut-docs#1721). Android has no D-Bus and no bluetoothd — its radio is only
// reachable through BluetoothAdapter/BluetoothLeScanner from Kotlin inside
// the app process — so the Linux client in client.go/dbus.go can never work
// there; this is the second backend, reached through gomobile bind's
// standard "Go interface implemented in Kotlin" callback pattern.
//
// This exact interface is NOT what gomobile bind sees: gobind only
// generates Java/Kotlin bindings for types declared in the package named on
// its command line (`gomobile bind ./mobile`), so the actual bound type is
// mobile.BluetoothBridge, declared structurally identical to this one
// (ADR-0080 amendment — an earlier draft declared only this interface and
// gobind silently dropped the registration function that referenced it,
// caught in review before merge). Kotlin implements mobile.BluetoothBridge;
// mobile.SetBluetoothBridge passes it straight through to SetAndroidBridge
// below with no adapter, since Go interface satisfaction is structural.
//
// Every method uses only gomobile-bind-safe types (string/int64/error — no
// structs crossing the boundary); device lists cross as a single
// JSON-encoded string, the same shape Device already marshals for the HTTP
// API, so the Kotlin side emits exactly what the page layer would have
// received from BlueZ. Calls are synchronous: Go blocks in the handler until
// Kotlin returns, so a Scan implementation must honour timeoutMillis itself;
// androidBridgeClient (below) separately enforces the caller's own ctx
// deadline around the call, since the call itself cannot be cancelled once
// started (review finding, ut-docs#1721).
//
// Failures are signalled as a plain error whose message is prefixed with
// one of the bridgeErr* tokens below (see classifyBridgeErr) — gomobile can
// only carry an error's message across the boundary, never a typed Go
// sentinel, so a recognized-string-prefix convention is the only way
// Kotlin can tell Go which of ErrAccessDenied/ErrUnavailable/ErrNotFound/
// ErrPairingFailed applies. An unrecognized message (or no prefix at all)
// still surfaces as a real error — it is simply not classified into a
// sentinel, and the page layer's default "something went wrong" path
// handles it exactly as it always has for an unclassified error today.
//
// The Kotlin implementation, its BLUETOOTH_SCAN/BLUETOOTH_CONNECT runtime
// permission flow, and on-device verification are a separate follow-up
// (ut-docs#1731); until it lands, nothing registers a bridge and Android
// keeps failing closed with ErrUnsupportedPlatform (ut-docs#1643).
type AndroidBridge interface {
	// ListDevices returns the devices currently bonded with this device's
	// adapter, JSON-encoded as a []Device.
	ListDevices() (devicesJSON string, err error)
	// Scan runs one bounded discovery of at most timeoutMillis and returns
	// the not-yet-bonded devices seen, JSON-encoded as a []Device.
	Scan(timeoutMillis int64) (devicesJSON string, err error)
	// Pair bonds (and connects) the device with this address.
	Pair(address string) error
	// Forget removes the bond for the device with this address.
	Forget(address string) error
}

// bridgeErr* are the string-prefix tokens an AndroidBridge implementation
// (ut-docs#1731's Kotlin code) uses to tell classifyBridgeErr which of this
// package's sentinel errors a failure maps to — "TOKEN: detail". A message
// with no recognized prefix passes through unclassified rather than being
// dropped or forced into the wrong sentinel; the page layer's existing
// unclassified-error path already handles that case today.
const (
	bridgeErrAccessDenied  = "ACCESS_DENIED"
	bridgeErrUnavailable   = "UNAVAILABLE"
	bridgeErrNotFound      = "NOT_FOUND"
	bridgeErrPairingFailed = "PAIRING_FAILED"
	// The three below have no D-Bus counterpart — see the sentinels they
	// map to in client.go for why each is its own state rather than a
	// flavour of UNAVAILABLE/ACCESS_DENIED (ut-docs#1751).
	bridgeErrAdapterOff        = "ADAPTER_OFF"
	bridgeErrPermissionReq     = "PERMISSION_REQUIRED"
	bridgeErrForgetUnsupported = "FORGET_UNSUPPORTED"
)

// classifyBridgeErr maps a bridgeErr*-prefixed message onto this package's
// sentinels (same purpose as classify() in dbus.go, adapted for a transport
// that can only carry an error string, never a typed error). nil in, nil
// out; an unrecognized message passes through unchanged, still a real
// error — it is only ever upgraded to carry a sentinel, never discarded.
func classifyBridgeErr(err error) error {
	if err == nil {
		return nil
	}
	for token, sentinel := range map[string]error{
		bridgeErrAccessDenied:  ErrAccessDenied,
		bridgeErrUnavailable:   ErrUnavailable,
		bridgeErrNotFound:      ErrNotFound,
		bridgeErrPairingFailed: ErrPairingFailed,

		bridgeErrAdapterOff:        ErrAdapterOff,
		bridgeErrPermissionReq:     ErrPermissionRequired,
		bridgeErrForgetUnsupported: ErrForgetUnsupported,
	} {
		prefix := token + ": "
		if msg := err.Error(); strings.HasPrefix(msg, prefix) {
			return fmt.Errorf("%w: %s", sentinel, strings.TrimPrefix(msg, prefix))
		}
	}
	return err
}

// androidBridgeMu guards androidBridge: the mobile package sets it once at
// boot, but a handler goroutine may already be reading it (and tests set
// and clear it around a request) — an RWMutex keeps that a defined
// read/write rather than a data race, without contending on the hot path.
var (
	androidBridgeMu sync.RWMutex
	androidBridge   AndroidBridge
)

// SetAndroidBridge registers the Kotlin-backed implementation. Called once,
// from the mobile package (mobile.SetBluetoothBridge), right after
// Mobile.start() succeeds. A nil bridge (the default — nothing has called
// this yet) means newDBusClientFor keeps returning ErrUnsupportedPlatform
// for Android, exactly as before ADR-0080; passing nil again un-registers
// a previously set bridge.
func SetAndroidBridge(b AndroidBridge) {
	androidBridgeMu.Lock()
	defer androidBridgeMu.Unlock()
	androidBridge = b
}

// RegisteredAndroidBridge reports the bridge SetAndroidBridge last
// registered, or nil when none is — a read-only probe for the mobile
// package's tests (and any future status surface wanting to say whether
// Android Bluetooth is wired), never a second way to call the bridge:
// handlers go through NewDBusClient like every other platform.
func RegisteredAndroidBridge() AndroidBridge {
	androidBridgeMu.RLock()
	defer androidBridgeMu.RUnlock()
	return androidBridge
}

// androidBridgeClient is the Client that fronts a registered AndroidBridge —
// the same "thin seam over a transport" shape client is over bus, minus
// the BlueZ object-tree plumbing: the Kotlin side already shapes results
// as []Device, so each method is forward (ctx-bounded), decode, wrap.
type androidBridgeClient struct {
	bridge AndroidBridge
}

func newAndroidBridgeClient(b AndroidBridge) *androidBridgeClient {
	return &androidBridgeClient{bridge: b}
}

// bridgeResult carries a synchronous bridge call's outcome back across the
// goroutine boundary bridgeCall spawns.
type bridgeResult[T any] struct {
	val T
	err error
}

// bridgeCall runs fn (a synchronous, uncancellable call into Kotlin) in its
// own goroutine and races it against ctx, so a caller's deadline is
// honoured even though the underlying call cannot itself be interrupted —
// the same problem Scan's own StopDiscovery-with-WithoutCancel handles for
// the D-Bus path, just shaped for a call this package cannot cancel at all
// (review finding, ut-docs#1721: ctx deadlines were previously decorative
// on this path — checked once before the call, never enforced during it).
// If ctx wins the race, fn's goroutine is left running to completion in
// the background and its result is discarded — there is no way to abort a
// synchronous JNI call from the Go side, so this bounds the *request*, not
// the underlying Kotlin work.
func bridgeCall[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	ch := make(chan bridgeResult[T], 1)
	go func() {
		v, err := fn()
		ch <- bridgeResult[T]{val: v, err: err}
	}()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case r := <-ch:
		return r.val, r.err
	}
}

func (c *androidBridgeClient) ListDevices(ctx context.Context) ([]Device, error) {
	raw, err := bridgeCall(ctx, c.bridge.ListDevices)
	if err != nil {
		return nil, fmt.Errorf("bluetooth: android bridge ListDevices: %w", classifyBridgeErr(err))
	}
	return decodeDevices("ListDevices", raw, 0)
}

func (c *androidBridgeClient) Scan(ctx context.Context, timeout time.Duration) ([]Device, error) {
	raw, err := bridgeCall(ctx, func() (string, error) { return c.bridge.Scan(timeout.Milliseconds()) })
	if err != nil {
		return nil, fmt.Errorf("bluetooth: android bridge Scan: %w", classifyBridgeErr(err))
	}
	// maxCandidates (client.go): same "a busy shop floor can see dozens of
	// advertising phones, the manager is looking for one scanner" bound the
	// D-Bus Scan path already enforces — kept identical across backends.
	return decodeDevices("Scan", raw, maxCandidates)
}

func (c *androidBridgeClient) Pair(ctx context.Context, address string) error {
	_, err := bridgeCall(ctx, func() (struct{}, error) { return struct{}{}, c.bridge.Pair(address) })
	if err != nil {
		return fmt.Errorf("bluetooth: android bridge Pair: %w", classifyBridgeErr(err))
	}
	return nil
}

func (c *androidBridgeClient) Forget(ctx context.Context, address string) error {
	_, err := bridgeCall(ctx, func() (struct{}, error) { return struct{}{}, c.bridge.Forget(address) })
	if err != nil {
		return fmt.Errorf("bluetooth: android bridge Forget: %w", classifyBridgeErr(err))
	}
	return nil
}

// Close is a no-op: the bridge is a process-wide singleton registered once
// at boot, not a per-call connection the way the D-Bus client's is —
// there is nothing per-Client to release.
func (c *androidBridgeClient) Close() error { return nil }

// decodeDevices turns the bridge's JSON string into the []Device the page
// layer expects, applying the same contract client.go's devices() gives
// every caller regardless of backend (review finding, ut-docs#1721): sorted
// by name (sortDevicesByName), addresses normalized to canonical upper-case
// form via NormalizeAddress (an entry whose address doesn't parse is
// dropped, same as devices() silently skipping one), and — when maxResults
// > 0 — truncated to the first maxResults entries after sorting, mirroring
// Scan's own maxCandidates bound on the D-Bus path. maxResults == 0 means
// unbounded (used by ListDevices, which client.go's own D-Bus ListDevices
// also never caps: the paired-device count is inherently small).
//
// Malformed input is a Kotlin-side bug, but it is reported as a wrapped
// error the handler logs — never a panic, and never a silent empty list. An
// empty JSON array decodes to an empty non-nil slice so the HTTP API
// marshals it as [] rather than null.
func decodeDevices(op, raw string, maxResults int) ([]Device, error) {
	var decoded []Device
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("bluetooth: android bridge %s returned malformed device JSON: %w", op, err)
	}
	out := make([]Device, 0, len(decoded))
	for _, d := range decoded {
		addr, ok := NormalizeAddress(d.Address)
		if !ok {
			continue
		}
		d.Address = addr
		out = append(out, d)
	}
	sortDevicesByName(out)
	if maxResults > 0 && len(out) > maxResults {
		out = out[:maxResults]
	}
	return out, nil
}
