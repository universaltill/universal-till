// Package bluetooth drives BlueZ (the Linux Bluetooth daemon) over the D-Bus
// system bus so a manager can pair, trust, connect and forget a Bluetooth
// HID device (keyboard-emulating barcode scanner, scale) from inside the
// kiosk UI, with no OS-level/SSH access (universaltill/ut-docs#76).
//
// This is the first D-Bus consumer in the codebase. The till service runs
// as the unprivileged `pos` system user with no console session, which
// BlueZ's default D-Bus policy denies outright — the shipped policy file
// packaging/linux/dbus-unitill-bluetooth.conf grants exactly the six
// org.bluez interfaces this package calls and nothing wider (ADR-0078). If
// this package ever needs another org.bluez interface, that file's allow
// list is where the grant goes; nothing here works without it.
//
// BlueZ itself is the source of truth for what is paired/trusted
// (bluetoothd persists pairings under /var/lib/bluetooth across reboots),
// so this package keeps no state of its own and the till has no table for
// it — every call reads live state from the daemon.
//
// Scope: "Just Works" pairing only. The pairing agent registers with the
// NoInputNoOutput capability (see agent.go) — HID scanners/scales
// overwhelmingly pair without a PIN, and a background D-Bus call has no way
// to show or type one. A device that genuinely demands PIN/passkey entry is
// out of scope and still needs the manual bluetoothctl-over-SSH path.
package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// Device is one Bluetooth device BlueZ knows about. JSON tags are for
// internal/pages/bluetooth_devices_page.go's responses — snake_case per this
// repo's API convention.
type Device struct {
	// Address is the canonical upper-case "AA:BB:CC:DD:EE:FF" form.
	Address string `json:"address"`
	// Name is BlueZ's Alias (user-visible name, falls back to the device's
	// advertised Name). Empty when the device advertises no name at all —
	// the UI supplies a localized generic label then, this is Go and has
	// no way to route a fallback through web/locales (same reasoning as
	// discovery.PrinterCandidate.Name).
	Name string `json:"name"`
	// Icon is BlueZ's freedesktop icon hint for the device class
	// ("input-keyboard", "input-mouse", "phone", …) — lets the UI mark the
	// HID candidates a manager is actually looking for.
	Icon      string `json:"icon"`
	Paired    bool   `json:"paired"`
	Trusted   bool   `json:"trusted"`
	Connected bool   `json:"connected"`
}

// Client is what the pages layer programs against; NewDBusClient is the
// real thing and handler tests substitute a fake through the
// newBluetoothClient package-var seam in internal/pages.
type Client interface {
	// ListDevices returns the devices currently paired with this till's
	// adapter, sorted by name.
	ListDevices(ctx context.Context) ([]Device, error)
	// Scan runs one bounded discovery — StartDiscovery, wait up to timeout
	// (or ctx), StopDiscovery — and returns the not-yet-paired devices
	// BlueZ has seen, as candidates only: nothing is paired here.
	Scan(ctx context.Context, timeout time.Duration) ([]Device, error)
	// Pair pairs, trusts and connects the device with this address in one
	// go — the "one confirmed action" the panel's Pair button is.
	Pair(ctx context.Context, address string) error
	// Forget removes the device (pairing, trust and connection) from the
	// adapter.
	Forget(ctx context.Context, address string) error
	// Close releases the bus connection. Discovery sessions and the
	// pairing agent are per-connection in BlueZ, so closing also ends any
	// scan or agent registration this client left behind.
	Close() error
}

var (
	// ErrUnavailable: no system bus, bluetoothd not running, or no
	// Bluetooth adapter — the "this box has no Bluetooth" case the panel
	// degrades to a status notice for (ADR-0078 consequences).
	ErrUnavailable = errors.New("bluetooth: not available on this till")
	// ErrAccessDenied: the D-Bus policy denied the call — in practice the
	// ADR-0078 policy file is missing or out of date. Kept distinct from
	// ErrUnavailable because it is a packaging fault an admin can fix, not
	// a hardware absence.
	ErrAccessDenied = errors.New("bluetooth: D-Bus access denied (is /etc/dbus-1/system.d/unitill-bluetooth.conf installed? ADR-0078)")
	// ErrNotFound: no device with that address is known to the adapter —
	// e.g. a scan candidate that has since aged out of BlueZ's cache.
	ErrNotFound = errors.New("bluetooth: device not found")
	// ErrPairingFailed: the device refused or timed out the pairing —
	// typically one that needs a PIN this agent cannot supply, or one
	// that is no longer in pairing mode.
	ErrPairingFailed = errors.New("bluetooth: pairing failed")
)

// maxCandidates caps how many devices one Scan reports. A busy shop floor
// can see dozens of phones advertising; the manager is looking for one
// scanner. Same bounded-result discipline as discovery.maxCandidates.
const maxCandidates = 64

// stopDiscoveryTimeout bounds the best-effort StopDiscovery after a scan —
// including after the caller's own ctx is already cancelled, which is why
// it gets a context of its own.
const stopDiscoveryTimeout = 3 * time.Second

// agentPath is where this process exports its org.bluez.Agent1 object.
const agentPath = dbus.ObjectPath("/com/universaltill/bluetooth/agent")

const (
	bluezService = "org.bluez"
	bluezRoot    = dbus.ObjectPath("/org/bluez")

	ifaceAdapter      = "org.bluez.Adapter1"
	ifaceDevice       = "org.bluez.Device1"
	ifaceAgentManager = "org.bluez.AgentManager1"
)

// managedObjects is the shape org.freedesktop.DBus.ObjectManager.
// GetManagedObjects returns: path → interface → property → value.
type managedObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// bus is the thin seam between the BlueZ call shaping (this file) and the
// actual D-Bus transport (dbus.go). Tests fake it in-process; there is no
// real adapter in CI.
type bus interface {
	ManagedObjects(ctx context.Context) (managedObjects, error)
	// Call invokes method (fully qualified, "org.bluez.Adapter1.
	// StartDiscovery") on the org.bluez object at path.
	Call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) error
	// SetProperty is org.freedesktop.DBus.Properties.Set on the org.bluez
	// object at path.
	SetProperty(ctx context.Context, path dbus.ObjectPath, iface, prop string, value any) error
	// ExportAgent publishes a on this connection as an org.bluez.Agent1
	// object at path, so BlueZ can call back into it during pairing.
	ExportAgent(a *agent, path dbus.ObjectPath) error
	Close() error
}

type client struct {
	bus bus
}

func newClient(b bus) *client { return &client{bus: b} }

// addressRe is the only address shape accepted from the outside (the
// handler's JSON body): six colon-separated hex octets.
var addressRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

// NormalizeAddress validates an operator-supplied Bluetooth address and
// returns its canonical upper-case form. Everything the pages layer accepts
// from a request goes through here before it reaches a D-Bus call.
func NormalizeAddress(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !addressRe.MatchString(s) {
		return "", false
	}
	return strings.ToUpper(s), true
}

func (c *client) Close() error { return c.bus.Close() }

func (c *client) ListDevices(ctx context.Context) ([]Device, error) {
	objs, err := c.objects(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := adapterPath(objs); err != nil {
		return nil, err
	}
	var out []Device
	for _, d := range devices(objs) {
		if d.Paired {
			out = append(out, d)
		}
	}
	return out, nil
}

func (c *client) Scan(ctx context.Context, timeout time.Duration) ([]Device, error) {
	objs, err := c.objects(ctx)
	if err != nil {
		return nil, err
	}
	adapter, err := adapterPath(objs)
	if err != nil {
		return nil, err
	}
	if !boolProp(objs[adapter][ifaceAdapter], "Powered") {
		// A freshly booted Pi with rfkill'd or never-enabled Bluetooth
		// answers StartDiscovery with org.bluez.Error.NotReady; powering
		// the adapter is what bluetoothctl's `power on` does.
		if err := c.bus.SetProperty(ctx, adapter, ifaceAdapter, "Powered", true); err != nil {
			return nil, classify(err)
		}
	}
	if err := c.bus.Call(ctx, adapter, ifaceAdapter+".StartDiscovery"); err != nil {
		return nil, classify(err)
	}
	// Wait out the bounded window (or the caller giving up), then stop —
	// unconditionally, with its own short deadline, so a cancelled request
	// never leaves the adapter in discovery mode (BlueZ would end it when
	// this connection closes anyway, but Close is the caller's business).
	timer := time.NewTimer(timeout)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopDiscoveryTimeout)
	stopErr := c.bus.Call(stopCtx, adapter, ifaceAdapter+".StopDiscovery")
	cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stopErr != nil {
		return nil, classify(stopErr)
	}
	objs, err = c.objects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0)
	for _, d := range devices(objs) {
		if d.Paired {
			continue
		}
		if len(out) == maxCandidates {
			break
		}
		out = append(out, d)
	}
	return out, nil
}

func (c *client) Pair(ctx context.Context, address string) error {
	objs, err := c.objects(ctx)
	if err != nil {
		return err
	}
	path, dev, err := findDevice(objs, address)
	if err != nil {
		return err
	}
	// BlueZ will not drive a pairing to completion without a registered
	// agent to answer its prompts — this is standard behaviour, not an edge
	// case (ADR-0078). The agent is scoped to exactly this device (see
	// agent.go) and torn down again before this call returns, whatever
	// happens in between.
	ag := &agent{expect: path}
	if err := c.bus.ExportAgent(ag, agentPath); err != nil {
		return fmt.Errorf("export pairing agent: %w", err)
	}
	if err := c.bus.Call(ctx, bluezRoot, ifaceAgentManager+".RegisterAgent", agentPath, agentCapability); err != nil {
		return classify(err)
	}
	defer func() {
		unregCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopDiscoveryTimeout)
		defer cancel()
		_ = c.bus.Call(unregCtx, bluezRoot, ifaceAgentManager+".UnregisterAgent", agentPath)
	}()
	if err := c.bus.Call(ctx, bluezRoot, ifaceAgentManager+".RequestDefaultAgent", agentPath); err != nil {
		return classify(err)
	}
	if !dev.Paired {
		if err := c.bus.Call(ctx, path, ifaceDevice+".Pair"); err != nil && !isBluezError(err, "AlreadyExists") {
			return classify(err)
		}
	}
	// Trusted is what lets the device reconnect on its own after a power
	// cycle without the manager coming back to this panel — without it a
	// paired scanner is a one-session pairing.
	if err := c.bus.SetProperty(ctx, path, ifaceDevice, "Trusted", true); err != nil {
		return classify(err)
	}
	// Pair and Trust are already committed in BlueZ by this point — a
	// Connect failure here does not mean the pairing failed. This is
	// common right after "Just Works" pairing a HID device: it briefly
	// drops the ACL as it switches into HID mode, so BlueZ can answer
	// e.g. ConnectionAttemptFailed on the immediate connect even though
	// the (now trusted) device reconnects on its own seconds later.
	// Reporting this as ErrPairingFailed would be worse than unhelpful:
	// Scan (below) filters out devices that are already paired, so the
	// manager would have no way to ever retry a "failed" pairing that in
	// fact succeeded (independent review finding, ut-docs#76). Treat it
	// as non-fatal; the reload the page does on success re-reads
	// Connected from BlueZ and shows the true state either way.
	_ = c.bus.Call(ctx, path, ifaceDevice+".Connect")
	return nil
}

func (c *client) Forget(ctx context.Context, address string) error {
	objs, err := c.objects(ctx)
	if err != nil {
		return err
	}
	path, _, err := findDevice(objs, address)
	if err != nil {
		return err
	}
	adapter := objectPathProp(objs[path][ifaceDevice], "Adapter")
	if adapter == "" {
		// Older BlueZ without the Adapter property: the device object is
		// always a child of its adapter's path.
		adapter = dbus.ObjectPath(path[:strings.LastIndex(string(path), "/")])
	}
	// RemoveDevice drops the pairing, the trust flag and any connection in
	// one go — the exact inverse of Pair.
	if err := c.bus.Call(ctx, adapter, ifaceAdapter+".RemoveDevice", path); err != nil {
		return classify(err)
	}
	return nil
}

// objects fetches the org.bluez object tree, mapping transport-level
// failures onto this package's sentinel errors.
func (c *client) objects(ctx context.Context) (managedObjects, error) {
	objs, err := c.bus.ManagedObjects(ctx)
	if err != nil {
		return nil, classify(err)
	}
	return objs, nil
}

// adapterPath picks the adapter to use — the lowest-sorted one (hci0 on
// every Pi; a box with two adapters gets the first, which is what
// bluetoothctl defaults to as well). No adapter at all is ErrUnavailable:
// bluetoothd is up but there is no Bluetooth hardware to drive.
func adapterPath(objs managedObjects) (dbus.ObjectPath, error) {
	var paths []string
	for p, ifaces := range objs {
		if _, ok := ifaces[ifaceAdapter]; ok {
			paths = append(paths, string(p))
		}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("%w: no Bluetooth adapter", ErrUnavailable)
	}
	sort.Strings(paths)
	return dbus.ObjectPath(paths[0]), nil
}

// devices extracts every org.bluez.Device1 object, sorted by name then
// address so a list is stable between page loads.
func devices(objs managedObjects) []Device {
	var out []Device
	for _, ifaces := range objs {
		props, ok := ifaces[ifaceDevice]
		if !ok {
			continue
		}
		addr, ok := NormalizeAddress(stringProp(props, "Address"))
		if !ok {
			continue
		}
		name := stringProp(props, "Alias")
		if name == "" {
			name = stringProp(props, "Name")
		}
		out = append(out, Device{
			Address:   addr,
			Name:      name,
			Icon:      stringProp(props, "Icon"),
			Paired:    boolProp(props, "Paired"),
			Trusted:   boolProp(props, "Trusted"),
			Connected: boolProp(props, "Connected"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			// Named devices before nameless ones, then alphabetical.
			if out[i].Name == "" || out[j].Name == "" {
				return out[i].Name != ""
			}
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].Address < out[j].Address
	})
	return out
}

// findDevice locates the Device1 object with this address (any case).
func findDevice(objs managedObjects, address string) (dbus.ObjectPath, Device, error) {
	want, ok := NormalizeAddress(address)
	if !ok {
		return "", Device{}, ErrNotFound
	}
	for p, ifaces := range objs {
		props, ok := ifaces[ifaceDevice]
		if !ok {
			continue
		}
		if got, ok := NormalizeAddress(stringProp(props, "Address")); ok && got == want {
			return p, Device{
				Address:   got,
				Name:      stringProp(props, "Alias"),
				Paired:    boolProp(props, "Paired"),
				Trusted:   boolProp(props, "Trusted"),
				Connected: boolProp(props, "Connected"),
			}, nil
		}
	}
	return "", Device{}, ErrNotFound
}

func stringProp(props map[string]dbus.Variant, key string) string {
	if v, ok := props[key]; ok {
		if s, ok := v.Value().(string); ok {
			return s
		}
	}
	return ""
}

func boolProp(props map[string]dbus.Variant, key string) bool {
	if v, ok := props[key]; ok {
		if b, ok := v.Value().(bool); ok {
			return b
		}
	}
	return false
}

func objectPathProp(props map[string]dbus.Variant, key string) dbus.ObjectPath {
	if v, ok := props[key]; ok {
		if p, ok := v.Value().(dbus.ObjectPath); ok {
			return p
		}
	}
	return ""
}

// isBluezError reports whether err is the org.bluez.Error.<name> D-Bus
// error (godbus returns dbus.Error by value from Call.Err).
func isBluezError(err error, name string) bool {
	var derr dbus.Error
	if errors.As(err, &derr) {
		return derr.Name == "org.bluez.Error."+name
	}
	return false
}

// classify maps a raw D-Bus/BlueZ error onto this package's sentinels
// where the caller needs to tell them apart (unavailable vs. denied vs.
// pairing refused), keeping the original text wrapped for the server log.
// Anything else passes through unchanged — the pages layer never shows a
// raw error to the operator either way.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var derr dbus.Error
	if !errors.As(err, &derr) {
		return err
	}
	switch derr.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.NoReply",
		"org.freedesktop.DBus.Error.Disconnected":
		return fmt.Errorf("%w: %v", ErrUnavailable, derr)
	case "org.freedesktop.DBus.Error.AccessDenied":
		return fmt.Errorf("%w: %v", ErrAccessDenied, derr)
	case "org.bluez.Error.AuthenticationFailed",
		"org.bluez.Error.AuthenticationCanceled",
		"org.bluez.Error.AuthenticationRejected",
		"org.bluez.Error.AuthenticationTimeout",
		"org.bluez.Error.ConnectionAttemptFailed":
		return fmt.Errorf("%w: %v", ErrPairingFailed, derr)
	}
	// dbus.Error's own Error() is just the body text ("Resource Not
	// Ready") — the NAME ("org.bluez.Error.NotReady") is the part a server
	// log needs to diagnose anything, so keep both.
	return fmt.Errorf("%s: %w", derr.Name, derr)
}
