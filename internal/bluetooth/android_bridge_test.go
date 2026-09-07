package bluetooth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeAndroidBridge is the in-process stand-in for the Kotlin
// BluetoothAdapter-backed implementation (ADR-0080) — the same shape
// client_test.go's fakeBus plays for D-Bus. It records what Go handed it
// and answers with whatever the test seeded, so a test can assert both
// the forwarding (arguments, call order) and the JSON-decoding.
type fakeAndroidBridge struct {
	calls []string

	listJSON string
	listErr  error
	// listBlock, when non-nil, makes ListDevices wait for it to be closed
	// before returning — lets a test hold the call "in flight" (as if
	// Kotlin's createBond/scan never returns) to exercise bridgeCall's
	// ctx-cancellation race rather than only its before-the-call fast path.
	// listStarted, when non-nil, is sent to right as ListDevices begins —
	// a test's own synchronization point, since ListDevices runs on
	// bridgeCall's spawned goroutine and polling f.calls from the test
	// goroutine without a lock would itself be a data race (caught by
	// -race while writing this test).
	listBlock   chan struct{}
	listStarted chan struct{}

	scanJSON    string
	scanErr     error
	scanTimeout int64

	pairAddr   string
	pairErr    error
	forgetAddr string
	forgetErr  error
}

func (f *fakeAndroidBridge) ListDevices() (string, error) {
	f.calls = append(f.calls, "ListDevices")
	if f.listStarted != nil {
		f.listStarted <- struct{}{}
	}
	if f.listBlock != nil {
		<-f.listBlock
	}
	return f.listJSON, f.listErr
}

func (f *fakeAndroidBridge) Scan(timeoutMillis int64) (string, error) {
	f.calls = append(f.calls, "Scan")
	f.scanTimeout = timeoutMillis
	return f.scanJSON, f.scanErr
}

func (f *fakeAndroidBridge) Pair(address string) error {
	f.calls = append(f.calls, "Pair")
	f.pairAddr = address
	return f.pairErr
}

func (f *fakeAndroidBridge) Forget(address string) error {
	f.calls = append(f.calls, "Forget")
	f.forgetAddr = address
	return f.forgetErr
}

// installBridge registers f for the duration of the test and un-registers
// it again on cleanup, so no test leaks a bridge into
// TestNewDBusClientFor_AndroidIsUnsupportedPlatform (dbus_test.go), which
// asserts the no-bridge default.
func installBridge(t *testing.T, f *fakeAndroidBridge) {
	t.Helper()
	SetAndroidBridge(f)
	t.Cleanup(func() { SetAndroidBridge(nil) })
}

// androidClient builds the Client newDBusClientFor hands out for Android
// once a bridge is registered.
func androidClient(t *testing.T, f *fakeAndroidBridge) Client {
	t.Helper()
	installBridge(t, f)
	c, err := newDBusClientFor("android")
	if err != nil {
		t.Fatalf("newDBusClientFor(\"android\") with a bridge registered: %v", err)
	}
	if c == nil {
		t.Fatal("newDBusClientFor(\"android\") returned a nil Client with a bridge registered")
	}
	return c
}

// sampleDevices is what the Kotlin side would JSON-encode — the same shape
// Device already marshals for the HTTP API, so the round trip is exact.
var sampleDevices = []Device{
	{Address: "AA:BB:CC:DD:EE:01", Name: "Zebra DS2278", Icon: "input-keyboard", Paired: true, Trusted: true, Connected: true},
	{Address: "AA:BB:CC:DD:EE:02", Name: "", Icon: "phone"},
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestNewDBusClientFor_AndroidWithoutBridgeStaysUnsupported pins the
// ADR-0080 "additive, not a behavior change" guarantee: an Android build
// that never registered a bridge (every build until ut-docs#1731 lands
// the Kotlin side) still gets ErrUnsupportedPlatform, never a Client that
// would nil-deref on first use.
func TestNewDBusClientFor_AndroidWithoutBridgeStaysUnsupported(t *testing.T) {
	SetAndroidBridge(nil)
	c, err := newDBusClientFor("android")
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("no bridge registered: err = %v, want ErrUnsupportedPlatform", err)
	}
	if c != nil {
		t.Fatalf("no bridge registered: Client = %#v, want nil", c)
	}
}

// TestSetAndroidBridge_NilUnregisters: registering then clearing must
// restore the default exactly — this is what keeps one test's bridge from
// leaking into another's, and what a future Kotlin teardown path would use.
func TestSetAndroidBridge_NilUnregisters(t *testing.T) {
	installBridge(t, &fakeAndroidBridge{listJSON: "[]"})
	if _, err := newDBusClientFor("android"); err != nil {
		t.Fatalf("with bridge: %v", err)
	}
	SetAndroidBridge(nil)
	if _, err := newDBusClientFor("android"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("after SetAndroidBridge(nil): err = %v, want ErrUnsupportedPlatform", err)
	}
}

// TestNewDBusClientFor_LinuxIgnoresBridge: a registered bridge must not
// hijack the Linux/BlueZ path — the bridge is Android-only (ADR-0080).
func TestNewDBusClientFor_LinuxIgnoresBridge(t *testing.T) {
	installBridge(t, &fakeAndroidBridge{listJSON: "[]"})
	c, err := newDBusClientFor("linux")
	if err == nil {
		// A sandbox with a real system bus: must be the D-Bus client, not
		// the bridge one.
		if _, isBridge := c.(*androidBridgeClient); isBridge {
			t.Fatal("linux got the Android bridge client")
		}
		_ = c.Close()
		return
	}
	if errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("linux must not be treated as an unsupported platform, got %v", err)
	}
}

func TestAndroidClient_ListDevicesForwardsAndDecodes(t *testing.T) {
	f := &fakeAndroidBridge{listJSON: mustJSON(t, sampleDevices)}
	c := androidClient(t, f)

	got, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if !reflect.DeepEqual(got, sampleDevices) {
		t.Fatalf("ListDevices = %+v, want %+v", got, sampleDevices)
	}
	if !reflect.DeepEqual(f.calls, []string{"ListDevices"}) {
		t.Fatalf("bridge calls = %v, want [ListDevices]", f.calls)
	}
}

// An empty list from Kotlin ("[]") must come back as an empty, non-nil
// slice — the page renders it as "no devices", and a nil slice would
// marshal as JSON null on the HTTP API instead of [].
func TestAndroidClient_ListDevicesEmptyIsNonNil(t *testing.T) {
	c := androidClient(t, &fakeAndroidBridge{listJSON: "[]"})
	got, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ListDevices on \"[]\" = %#v, want an empty non-nil slice", got)
	}
}

func TestAndroidClient_ListDevicesBridgeErrorIsWrapped(t *testing.T) {
	boom := errors.New("BluetoothAdapter is null")
	c := androidClient(t, &fakeAndroidBridge{listErr: boom})
	_, err := c.ListDevices(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("ListDevices err = %v, want it to wrap %v", err, boom)
	}
}

func TestAndroidClient_ScanForwardsTimeoutInMillisAndDecodes(t *testing.T) {
	f := &fakeAndroidBridge{scanJSON: mustJSON(t, sampleDevices[1:])}
	c := androidClient(t, f)

	got, err := c.Scan(context.Background(), 8*time.Second)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !reflect.DeepEqual(got, sampleDevices[1:]) {
		t.Fatalf("Scan = %+v, want %+v", got, sampleDevices[1:])
	}
	if f.scanTimeout != 8000 {
		t.Fatalf("bridge Scan timeoutMillis = %d, want 8000", f.scanTimeout)
	}
	if !reflect.DeepEqual(f.calls, []string{"Scan"}) {
		t.Fatalf("bridge calls = %v, want [Scan]", f.calls)
	}
}

func TestAndroidClient_ScanBridgeErrorIsWrapped(t *testing.T) {
	boom := errors.New("BLUETOOTH_SCAN not granted")
	c := androidClient(t, &fakeAndroidBridge{scanErr: boom})
	_, err := c.Scan(context.Background(), time.Second)
	if !errors.Is(err, boom) {
		t.Fatalf("Scan err = %v, want it to wrap %v", err, boom)
	}
}

// Malformed JSON from the Kotlin side is a bug on that side, but it must
// surface as an error the handler logs — never a panic that takes the
// whole till down, and never a silently empty list.
func TestAndroidClient_MalformedJSONIsAnErrorNotAPanic(t *testing.T) {
	cases := map[string]string{
		"truncated":     `[{"address":"AA:BB:CC:DD:EE:01"`,
		"not-an-array":  `{"address":"AA:BB:CC:DD:EE:01"}`,
		"garbage":       `<html>`,
		"wrong-element": `[42]`,
	}
	for name, bad := range cases {
		t.Run("ListDevices/"+name, func(t *testing.T) {
			c := androidClient(t, &fakeAndroidBridge{listJSON: bad})
			got, err := c.ListDevices(context.Background())
			if err == nil {
				t.Fatalf("ListDevices on %q: got %+v, want an error", bad, got)
			}
			if !strings.Contains(err.Error(), "bluetooth") {
				t.Fatalf("ListDevices error %q should name this package's boundary", err)
			}
		})
		t.Run("Scan/"+name, func(t *testing.T) {
			c := androidClient(t, &fakeAndroidBridge{scanJSON: bad})
			got, err := c.Scan(context.Background(), time.Second)
			if err == nil {
				t.Fatalf("Scan on %q: got %+v, want an error", bad, got)
			}
		})
	}
}

func TestAndroidClient_PairForwardsAddress(t *testing.T) {
	f := &fakeAndroidBridge{}
	c := androidClient(t, f)
	if err := c.Pair(context.Background(), "AA:BB:CC:DD:EE:01"); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if f.pairAddr != "AA:BB:CC:DD:EE:01" {
		t.Fatalf("bridge Pair address = %q, want AA:BB:CC:DD:EE:01", f.pairAddr)
	}
	if !reflect.DeepEqual(f.calls, []string{"Pair"}) {
		t.Fatalf("bridge calls = %v, want [Pair]", f.calls)
	}
}

func TestAndroidClient_PairBridgeErrorIsWrapped(t *testing.T) {
	boom := errors.New("createBond returned false")
	c := androidClient(t, &fakeAndroidBridge{pairErr: boom})
	if err := c.Pair(context.Background(), "AA:BB:CC:DD:EE:01"); !errors.Is(err, boom) {
		t.Fatalf("Pair err = %v, want it to wrap %v", err, boom)
	}
}

func TestAndroidClient_ForgetForwardsAddress(t *testing.T) {
	f := &fakeAndroidBridge{}
	c := androidClient(t, f)
	if err := c.Forget(context.Background(), "AA:BB:CC:DD:EE:02"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if f.forgetAddr != "AA:BB:CC:DD:EE:02" {
		t.Fatalf("bridge Forget address = %q, want AA:BB:CC:DD:EE:02", f.forgetAddr)
	}
	if !reflect.DeepEqual(f.calls, []string{"Forget"}) {
		t.Fatalf("bridge calls = %v, want [Forget]", f.calls)
	}
}

func TestAndroidClient_ForgetBridgeErrorIsWrapped(t *testing.T) {
	boom := errors.New("removeBond returned false")
	c := androidClient(t, &fakeAndroidBridge{forgetErr: boom})
	if err := c.Forget(context.Background(), "AA:BB:CC:DD:EE:02"); !errors.Is(err, boom) {
		t.Fatalf("Forget err = %v, want it to wrap %v", err, boom)
	}
}

// Close is a no-op: the bridge is a process-wide singleton registered once
// at boot, not a per-call connection like the D-Bus client's. Closing one
// handler's Client must not touch the bridge at all.
func TestAndroidClient_CloseIsANoOp(t *testing.T) {
	f := &fakeAndroidBridge{}
	c := androidClient(t, f)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("Close reached the bridge: calls = %v", f.calls)
	}
	// The bridge stays registered for the next handler.
	if _, err := newDBusClientFor("android"); err != nil {
		t.Fatalf("after Close, newDBusClientFor(\"android\") = %v, want the bridge still registered", err)
	}
}

// A cancelled context is honoured before the (synchronous, uncancellable)
// bridge call is made — the bridge itself has no ctx to hand the cancel to.
func TestAndroidClient_CancelledContextShortCircuits(t *testing.T) {
	f := &fakeAndroidBridge{listJSON: "[]"}
	c := androidClient(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListDevices(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListDevices with cancelled ctx: err = %v, want context.Canceled", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("cancelled ctx still reached the bridge: calls = %v", f.calls)
	}
}

// TestAndroidClient_ContextCancelledMidCallReturnsPromptly (review finding,
// ut-docs#1721): ctx deadlines were previously decorative once the call was
// already in flight — checked only before the (synchronous, uncancellable)
// bridge call, never during it. This proves bridgeCall's ctx-race actually
// unblocks the caller even while the fake bridge is still "in Kotlin,"
// simulating a bond dialog nobody has answered yet.
func TestAndroidClient_ContextCancelledMidCallReturnsPromptly(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	f := &fakeAndroidBridge{listJSON: "[]", listBlock: block, listStarted: started}
	c := androidClient(t, f)
	t.Cleanup(func() { close(block) }) // let the orphaned goroutine finish

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := c.ListDevices(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("ListDevices cancelled mid-call: err = %v, want context.Canceled", err)
		}
	}()

	// Wait for the fake bridge to actually be blocked inside ListDevices
	// (bridgeCall's goroutine has started and is waiting on <-listBlock)
	// before cancelling, so this genuinely exercises the mid-call race and
	// not just the already-cancelled fast path the sibling test covers.
	// The channel receive is also what makes the later f.calls read below
	// race-free: it establishes happens-before against the goroutine's
	// f.calls write.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fake bridge was never reached before the cancellation window closed")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ListDevices did not return promptly after ctx cancellation while the bridge call was in flight")
	}
	if !reflect.DeepEqual(f.calls, []string{"ListDevices"}) {
		t.Fatalf("bridge calls = %v, want [ListDevices] (the orphaned goroutine still made its one call)", f.calls)
	}
}

// TestAndroidClient_ListDevicesSortsByName pins the same ordering
// Client.ListDevices documents for every backend (review finding,
// ut-docs#1721) — the bridge must not just pass through whatever order
// Kotlin happened to return.
func TestAndroidClient_ListDevicesSortsByName(t *testing.T) {
	unsorted := []Device{
		{Address: "AA:BB:CC:DD:EE:03", Name: "Zebra Scanner"},
		{Address: "AA:BB:CC:DD:EE:04", Name: ""}, // nameless sorts last
		{Address: "AA:BB:CC:DD:EE:02", Name: "avery scale"},
	}
	c := androidClient(t, &fakeAndroidBridge{listJSON: mustJSON(t, unsorted)})
	got, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	want := []string{"avery scale", "Zebra Scanner", ""}
	var gotNames []string
	for _, d := range got {
		gotNames = append(gotNames, d.Name)
	}
	if !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("ListDevices order = %v, want %v (case-insensitive alpha, nameless last)", gotNames, want)
	}
}

// TestAndroidClient_NormalizesAndDropsInvalidAddresses matches devices()'s
// own D-Bus-path behaviour: a lowercase address is upper-cased, and an
// entry whose address doesn't parse at all is dropped, not surfaced as-is.
func TestAndroidClient_NormalizesAndDropsInvalidAddresses(t *testing.T) {
	raw := `[{"address":"aa:bb:cc:dd:ee:01","name":"Lowercase"},{"address":"not-a-mac","name":"Bad"}]`
	c := androidClient(t, &fakeAndroidBridge{listJSON: raw})
	got, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListDevices = %+v, want exactly the 1 valid-address entry", got)
	}
	if got[0].Address != "AA:BB:CC:DD:EE:01" {
		t.Fatalf("Address = %q, want canonical upper-case AA:BB:CC:DD:EE:01", got[0].Address)
	}
}

// TestAndroidClient_ScanCapsAtMaxCandidates mirrors the D-Bus Scan path's
// own maxCandidates bound ("a busy shop floor can see dozens of advertising
// phones") — the bridge path must not return an unbounded list.
func TestAndroidClient_ScanCapsAtMaxCandidates(t *testing.T) {
	many := make([]Device, maxCandidates+10)
	for i := range many {
		many[i] = Device{Address: fmt.Sprintf("AA:BB:CC:DD:%02X:%02X", i/256, i%256), Name: fmt.Sprintf("Device %03d", i)}
	}
	c := androidClient(t, &fakeAndroidBridge{scanJSON: mustJSON(t, many)})
	got, err := c.Scan(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != maxCandidates {
		t.Fatalf("Scan returned %d devices, want the maxCandidates (%d) cap applied", len(got), maxCandidates)
	}
}

// TestAndroidClient_ListDevicesNotCapped: unlike Scan, ListDevices has no
// cap on either backend — the paired-device count is inherently small.
func TestAndroidClient_ListDevicesNotCapped(t *testing.T) {
	many := make([]Device, maxCandidates+10)
	for i := range many {
		many[i] = Device{Address: fmt.Sprintf("AA:BB:CC:DD:%02X:%02X", i/256, i%256), Name: fmt.Sprintf("Device %03d", i)}
	}
	c := androidClient(t, &fakeAndroidBridge{listJSON: mustJSON(t, many)})
	got, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(got) != len(many) {
		t.Fatalf("ListDevices returned %d devices, want all %d (uncapped)", len(got), len(many))
	}
}

// TestClassifyBridgeErr_RecognizedPrefixesMapToSentinels (review finding,
// ut-docs#1721): gomobile can only carry an error's message across the
// Kotlin boundary, never a typed Go sentinel, so ut-docs#1731's Kotlin side
// signals which sentinel applies by prefixing its error message with one of
// these tokens. This pins the convention so #1731 has a contract to build
// against rather than guessing it from the Go source.
func TestClassifyBridgeErr_RecognizedPrefixesMapToSentinels(t *testing.T) {
	cases := []struct {
		msg  string
		want error
	}{
		{"ACCESS_DENIED: BLUETOOTH_SCAN not granted", ErrAccessDenied},
		{"UNAVAILABLE: adapter is null", ErrUnavailable},
		{"NOT_FOUND: no such bonded device", ErrNotFound},
		{"PAIRING_FAILED: createBond returned false", ErrPairingFailed},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			got := classifyBridgeErr(errors.New(tc.msg))
			if !errors.Is(got, tc.want) {
				t.Fatalf("classifyBridgeErr(%q) = %v, want it to wrap %v", tc.msg, got, tc.want)
			}
		})
	}
}

// An unrecognized message must still be a real, non-nil error — never
// silently dropped, and never force-mapped onto the wrong sentinel.
func TestClassifyBridgeErr_UnrecognizedMessagePassesThrough(t *testing.T) {
	boom := errors.New("something Kotlin-specific and undocumented")
	got := classifyBridgeErr(boom)
	if !errors.Is(got, boom) {
		t.Fatalf("classifyBridgeErr(unrecognized) = %v, want it to still wrap the original error", got)
	}
	for _, sentinel := range []error{ErrAccessDenied, ErrUnavailable, ErrNotFound, ErrPairingFailed, ErrUnsupportedPlatform} {
		if errors.Is(got, sentinel) {
			t.Fatalf("classifyBridgeErr(unrecognized) wrongly matches %v", sentinel)
		}
	}
}

func TestClassifyBridgeErr_Nil(t *testing.T) {
	if got := classifyBridgeErr(nil); got != nil {
		t.Fatalf("classifyBridgeErr(nil) = %v, want nil", got)
	}
}

// TestAndroidClient_ClassifiedErrorSurfacesThroughListDevices confirms the
// classification actually reaches the caller through the wrapping
// ListDevices does (fmt.Errorf("...: %w", classifyBridgeErr(err))), not
// just classifyBridgeErr in isolation.
func TestAndroidClient_ClassifiedErrorSurfacesThroughListDevices(t *testing.T) {
	c := androidClient(t, &fakeAndroidBridge{listErr: errors.New("ACCESS_DENIED: BLUETOOTH_SCAN not granted")})
	_, err := c.ListDevices(context.Background())
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("ListDevices err = %v, want it to wrap ErrAccessDenied", err)
	}
}
