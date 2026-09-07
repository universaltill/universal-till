package bluetooth

// Tests for the BlueZ client run against fakeBus — an in-memory stand-in for
// the D-Bus connection (universaltill/ut-docs#76's AC: "tested against a
// fake/mock D-Bus interface, no real adapter in CI"). They pin the
// METHOD-CALL SHAPING — which org.bluez object/interface/method is called,
// in which order, with which arguments — since that is exactly what a
// missing D-Bus policy grant (ADR-0078) or a wrong BlueZ call sequence
// would break in production while looking fine in review.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// call is one recorded bus call: "<path> <method>" plus its args.
type call struct {
	path   dbus.ObjectPath
	method string
	args   []any
}

// fakeBus records every call and answers from a canned managed-objects
// tree. errs maps "<method>" (or "<path> <method>") to the error that call
// should return.
type fakeBus struct {
	objects      managedObjects
	objectsErr   error
	errs         map[string]error
	calls        []call
	props        []call
	agents       []dbus.ObjectPath
	exportErr    error
	closed       bool
	onDiscovered func() // invoked on StartDiscovery so a test can "find" a device mid-scan
}

func newFakeBus() *fakeBus {
	return &fakeBus{objects: managedObjects{}, errs: map[string]error{}}
}

func (f *fakeBus) ManagedObjects(ctx context.Context) (managedObjects, error) {
	if f.objectsErr != nil {
		return nil, f.objectsErr
	}
	return f.objects, nil
}

func (f *fakeBus) Call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) error {
	f.calls = append(f.calls, call{path: path, method: method, args: args})
	if strings.HasSuffix(method, ".StartDiscovery") && f.onDiscovered != nil {
		f.onDiscovered()
	}
	if err, ok := f.errs[string(path)+" "+method]; ok {
		return err
	}
	return f.errs[method]
}

func (f *fakeBus) SetProperty(ctx context.Context, path dbus.ObjectPath, iface, prop string, value any) error {
	f.props = append(f.props, call{path: path, method: iface + "." + prop, args: []any{value}})
	return f.errs["set "+iface+"."+prop]
}

func (f *fakeBus) ExportAgent(a *agent, path dbus.ObjectPath) error {
	f.agents = append(f.agents, path)
	return f.exportErr
}

func (f *fakeBus) Close() error {
	f.closed = true
	return nil
}

func (f *fakeBus) methods() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

// addAdapter / addDevice build the org.bluez object tree BlueZ's
// ObjectManager.GetManagedObjects returns.
func (f *fakeBus) addAdapter(path string, powered bool) {
	f.objects[dbus.ObjectPath(path)] = map[string]map[string]dbus.Variant{
		"org.bluez.Adapter1": {
			"Address": dbus.MakeVariant("00:11:22:33:44:55"),
			"Powered": dbus.MakeVariant(powered),
		},
	}
}

func (f *fakeBus) addDevice(adapter, address, name string, paired, trusted, connected bool) dbus.ObjectPath {
	path := dbus.ObjectPath(adapter + "/dev_" + strings.ReplaceAll(address, ":", "_"))
	f.objects[path] = map[string]map[string]dbus.Variant{
		"org.bluez.Device1": {
			"Address":   dbus.MakeVariant(address),
			"Name":      dbus.MakeVariant(name),
			"Alias":     dbus.MakeVariant(name),
			"Paired":    dbus.MakeVariant(paired),
			"Trusted":   dbus.MakeVariant(trusted),
			"Connected": dbus.MakeVariant(connected),
			"Adapter":   dbus.MakeVariant(dbus.ObjectPath(adapter)),
			"Icon":      dbus.MakeVariant("input-keyboard"),
		},
	}
	return path
}

func TestListDevices_ReturnsPairedDevicesSortedByName(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:02", "Zebra Scanner", true, true, false)
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:01", "Metum Scanner", true, true, true)
	// An unpaired device BlueZ still has cached from a previous scan must
	// not show up as "paired".
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:03", "Someone's phone", false, false, false)

	devs, err := newClient(f).ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("want 2 paired devices, got %d: %+v", len(devs), devs)
	}
	if devs[0].Name != "Metum Scanner" || devs[0].Address != "AA:BB:CC:DD:EE:01" || !devs[0].Connected || !devs[0].Trusted {
		t.Errorf("first device: %+v", devs[0])
	}
	if devs[1].Name != "Zebra Scanner" || devs[1].Connected {
		t.Errorf("second device: %+v", devs[1])
	}
	if devs[0].Icon != "input-keyboard" {
		t.Errorf("icon not carried through: %+v", devs[0])
	}
}

func TestListDevices_NoAdapterIsUnavailable(t *testing.T) {
	f := newFakeBus() // empty tree: bluetoothd up, no adapter plugged in
	_, err := newClient(f).ListDevices(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable with no adapter, got %v", err)
	}
}

func TestListDevices_ServiceUnknownIsUnavailable(t *testing.T) {
	f := newFakeBus()
	// What the system bus answers when bluetoothd is not running at all.
	f.objectsErr = dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"The name org.bluez was not provided by any .service files"}}
	_, err := newClient(f).ListDevices(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable when org.bluez has no owner, got %v", err)
	}
}

func TestListDevices_AccessDeniedIsDistinct(t *testing.T) {
	f := newFakeBus()
	// The exact failure a missing /etc/dbus-1/system.d/unitill-bluetooth.conf
	// produces for the pos user (ADR-0078) — must NOT be reported as
	// "Bluetooth not available", it's a packaging fault an admin can fix.
	f.objectsErr = dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied", Body: []any{"Rejected send message"}}
	_, err := newClient(f).ListDevices(context.Background())
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("want ErrAccessDenied, got %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("AccessDenied must not double as ErrUnavailable: %v", err)
	}
}

func TestScan_StartsAndStopsDiscoveryAndReturnsOnlyUnpairedCandidates(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:01", "Metum Scanner", true, true, true)
	f.onDiscovered = func() {
		f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:09", "New Scanner", false, false, false)
	}

	found, err := newClient(f).Scan(context.Background(), 5*time.Millisecond)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].Address != "AA:BB:CC:DD:EE:09" || found[0].Paired {
		t.Fatalf("want only the unpaired candidate, got %+v", found)
	}
	want := []string{"org.bluez.Adapter1.StartDiscovery", "org.bluez.Adapter1.StopDiscovery"}
	if got := f.methods(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("call sequence = %v, want %v", got, want)
	}
	if f.calls[0].path != "/org/bluez/hci0" {
		t.Fatalf("discovery must target the adapter object, got %s", f.calls[0].path)
	}
	if len(f.props) != 0 {
		t.Fatalf("an already-powered adapter must not be re-powered: %+v", f.props)
	}
}

func TestScan_PowersOnAnUnpoweredAdapterFirst(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", false)
	if _, err := newClient(f).Scan(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(f.props) != 1 || f.props[0].method != "org.bluez.Adapter1.Powered" || f.props[0].args[0] != true {
		t.Fatalf("want Adapter1.Powered=true set before discovery, got %+v", f.props)
	}
}

func TestScan_StopsDiscoveryEvenWhenCancelledMidScan(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	ctx, cancel := context.WithCancel(context.Background())
	f.onDiscovered = func() { cancel() }

	_, err := newClient(f).Scan(ctx, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if got := f.methods(); len(got) != 2 || got[1] != "org.bluez.Adapter1.StopDiscovery" {
		t.Fatalf("discovery must be stopped on cancel, calls = %v", got)
	}
}

func TestScan_StartDiscoveryFailureIsReported(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	f.errs["org.bluez.Adapter1.StartDiscovery"] = dbus.Error{Name: "org.bluez.Error.NotReady", Body: []any{"Resource Not Ready"}}
	_, err := newClient(f).Scan(context.Background(), time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "NotReady") {
		t.Fatalf("want the BlueZ error surfaced for the server log, got %v", err)
	}
}

func TestPair_RegistersNoInputNoOutputAgentThenPairsTrustsConnects(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	devPath := f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:09", "New Scanner", false, false, false)

	if err := newClient(f).Pair(context.Background(), "aa:bb:cc:dd:ee:09"); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	want := []string{
		"org.bluez.AgentManager1.RegisterAgent",
		"org.bluez.AgentManager1.RequestDefaultAgent",
		"org.bluez.Device1.Pair",
		"org.bluez.Device1.Connect",
		"org.bluez.AgentManager1.UnregisterAgent",
	}
	if got := f.methods(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("call sequence = %v, want %v", got, want)
	}
	reg := f.calls[0]
	if reg.path != "/org/bluez" {
		t.Errorf("AgentManager1 lives at /org/bluez, got %s", reg.path)
	}
	if len(reg.args) != 2 || reg.args[0] != agentPath || reg.args[1] != "NoInputNoOutput" {
		t.Errorf("RegisterAgent args = %v, want [%s NoInputNoOutput]", reg.args, agentPath)
	}
	if len(f.agents) != 1 || f.agents[0] != agentPath {
		t.Errorf("Agent1 object must be exported at %s before RegisterAgent, got %v", agentPath, f.agents)
	}
	if f.calls[2].path != devPath || f.calls[3].path != devPath {
		t.Errorf("Pair/Connect must target the device object %s: %+v", devPath, f.calls[2:4])
	}
	if len(f.props) != 1 || f.props[0].path != devPath || f.props[0].method != "org.bluez.Device1.Trusted" || f.props[0].args[0] != true {
		t.Errorf("want Device1.Trusted=true set on the device, got %+v", f.props)
	}
}

func TestPair_AlreadyPairedDeviceSkipsPairButStillTrustsAndConnects(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:01", "Metum Scanner", true, false, false)

	if err := newClient(f).Pair(context.Background(), "AA:BB:CC:DD:EE:01"); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	for _, m := range f.methods() {
		if m == "org.bluez.Device1.Pair" {
			t.Fatalf("Device1.Pair must be skipped for an already-paired device (BlueZ answers AlreadyExists): %v", f.methods())
		}
	}
	if len(f.props) != 1 || f.props[0].method != "org.bluez.Device1.Trusted" {
		t.Fatalf("still expected Trusted=true, got %+v", f.props)
	}
}

func TestPair_ConnectFailureAfterSuccessfulTrustIsNotAFailure(t *testing.T) {
	// Independent review finding (ut-docs#76): Pair+Trust are already
	// committed in BlueZ by the time Connect runs, and a HID device
	// commonly drops the ACL right after "Just Works" pairing as it
	// switches into HID mode — BlueZ answers a generic Connect failure
	// even though the (now trusted) device reconnects on its own
	// seconds later. Reporting this as ErrPairingFailed would strand the
	// manager: Scan filters out already-paired devices, so a "failed"
	// pairing that actually succeeded could never be retried through
	// this panel.
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:07", "New Scanner", false, false, false)
	f.errs["org.bluez.Device1.Connect"] = dbus.Error{Name: "org.bluez.Error.Failed", Body: []any{"le-connection-abort-by-local"}}

	if err := newClient(f).Pair(context.Background(), "AA:BB:CC:DD:EE:07"); err != nil {
		t.Fatalf("a Connect failure after successful Pair+Trust must not fail Pair(), got %v", err)
	}
	if len(f.props) != 1 || f.props[0].method != "org.bluez.Device1.Trusted" {
		t.Fatalf("Trusted=true must still have been set: %+v", f.props)
	}
}

func TestPair_UnknownAddressIsNotFound(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	err := newClient(f).Pair(context.Background(), "AA:BB:CC:DD:EE:99")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing must be called for an unknown address, got %v", f.methods())
	}
}

func TestPair_AuthenticationFailureIsPairingFailedAndAgentIsUnregistered(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:09", "New Scanner", false, false, false)
	f.errs["org.bluez.Device1.Pair"] = dbus.Error{Name: "org.bluez.Error.AuthenticationFailed", Body: []any{"Authentication Failed"}}

	err := newClient(f).Pair(context.Background(), "AA:BB:CC:DD:EE:09")
	if !errors.Is(err, ErrPairingFailed) {
		t.Fatalf("want ErrPairingFailed, got %v", err)
	}
	got := f.methods()
	if got[len(got)-1] != "org.bluez.AgentManager1.UnregisterAgent" {
		t.Fatalf("agent must be unregistered on the failure path too: %v", got)
	}
	if len(f.props) != 0 {
		t.Fatalf("a device that failed to pair must not be trusted: %+v", f.props)
	}
}

func TestPair_RegisterAgentAccessDeniedIsReportedAsAccessDenied(t *testing.T) {
	// The exact production failure ADR-0078's review caught: a policy file
	// without the AgentManager1 grant lets everything else work and fails
	// only here.
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:09", "New Scanner", false, false, false)
	f.errs["org.bluez.AgentManager1.RegisterAgent"] = dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied", Body: []any{"Rejected send message"}}

	err := newClient(f).Pair(context.Background(), "AA:BB:CC:DD:EE:09")
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("want ErrAccessDenied, got %v", err)
	}
}

func TestForget_RemovesDeviceViaItsAdapter(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	devPath := f.addDevice("/org/bluez/hci0", "AA:BB:CC:DD:EE:01", "Metum Scanner", true, true, true)

	if err := newClient(f).Forget(context.Background(), "AA:BB:CC:DD:EE:01"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0].method != "org.bluez.Adapter1.RemoveDevice" || f.calls[0].path != "/org/bluez/hci0" {
		t.Fatalf("want a single Adapter1.RemoveDevice on the adapter, got %+v", f.calls)
	}
	if len(f.calls[0].args) != 1 || f.calls[0].args[0] != devPath {
		t.Fatalf("RemoveDevice arg = %v, want %s", f.calls[0].args, devPath)
	}
}

func TestForget_UnknownAddressIsNotFound(t *testing.T) {
	f := newFakeBus()
	f.addAdapter("/org/bluez/hci0", true)
	if err := newClient(f).Forget(context.Background(), "AA:BB:CC:DD:EE:99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestClose_ClosesTheBus(t *testing.T) {
	f := newFakeBus()
	if err := newClient(f).Close(); err != nil || !f.closed {
		t.Fatalf("Close must close the underlying bus: err=%v closed=%v", err, f.closed)
	}
}

// The pairing agent: NoInputNoOutput means "Just Works" — BlueZ rarely calls
// back at all, but when it does, the answer must be scoped to the ONE
// device the manager explicitly clicked Pair on, and anything needing a
// PIN/passkey typed in must be refused (the till has no way to ask).
func TestAgent_AcceptsOnlyTheDeviceBeingPaired(t *testing.T) {
	a := &agent{expect: "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09"}
	if err := a.RequestConfirmation("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09", 123456); err != nil {
		t.Fatalf("confirmation for the expected device must be accepted: %v", err)
	}
	if err := a.RequestConfirmation("/org/bluez/hci0/dev_11_22_33_44_55_66", 123456); err == nil || err.Name != "org.bluez.Error.Rejected" {
		t.Fatalf("confirmation for any other device must be rejected, got %v", err)
	}
	if err := a.RequestAuthorization("/org/bluez/hci0/dev_11_22_33_44_55_66"); err == nil {
		t.Fatal("authorization for another device must be rejected")
	}
	if err := a.AuthorizeService("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09", "00001124-0000-1000-8000-00805f9b34fb"); err != nil {
		t.Fatalf("HID service authorization for the expected device must be accepted: %v", err)
	}
	if _, err := a.RequestPinCode("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09"); err == nil {
		t.Fatal("a PIN request must be rejected — NoInputNoOutput has nowhere to type one")
	}
	if _, err := a.RequestPasskey("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_09"); err == nil {
		t.Fatal("a passkey request must be rejected")
	}
	if err := a.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := a.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestNormalizeAddress(t *testing.T) {
	for in, want := range map[string]string{
		"aa:bb:cc:dd:ee:ff":   "AA:BB:CC:DD:EE:FF",
		" AA:BB:CC:DD:EE:FF ": "AA:BB:CC:DD:EE:FF",
	} {
		if got, ok := NormalizeAddress(in); !ok || got != want {
			t.Errorf("NormalizeAddress(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "AA:BB:CC:DD:EE", "AA-BB-CC-DD-EE-FF", "AA:BB:CC:DD:EE:GG", "/org/bluez/hci0"} {
		if got, ok := NormalizeAddress(bad); ok {
			t.Errorf("NormalizeAddress(%q) = %q, want rejected", bad, got)
		}
	}
}
