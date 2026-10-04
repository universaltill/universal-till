package pages

import (
	"context"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// The connected-devices report (ut-docs#3479): DeviceExtra's `peripherals`,
// so my. can draw a till's devices under it (ut-docs#3478). Built only from
// what the till already knows — the receipt and kitchen printer settings,
// kitchen stations, the devices BlueZ has paired, and the plugin providing
// the ADR-0129 `fiscal.device` capability. Nothing is probed: printers are
// never contacted for this (an ESC/POS probe can print garbage on an
// office printer), so their status is "unknown"; only Bluetooth reports a
// live connected/offline state.
//
// What it exposes: per device a kind, a display name (a station name, a
// Bluetooth alias, an OS print-queue name or a plugin name — never an IP
// address, host:port, device path, MAC or serial) and a coarse connection
// type. Card readers driven by a payment plugin are not reported: core has
// no neutral capability naming one, and plugin-specific code stays out of
// core (#2848) — a Bluetooth-paired reader still shows as "other".
//
// Wire shape per item: {"kind", "name", "connection"?, "status"}. The
// check-in hashes the device record (cloudsync.stateHash), so the list is
// sorted and deduplicated: the same devices always produce the same bytes,
// and only a real change (a device added/removed, a Bluetooth device
// connecting or dropping) costs a POST — at most one per check-in tick.

// maxReportedPeripherals bounds the list (the cloud enforces the same cap).
const maxReportedPeripherals = 32

// maxPeripheralNameRunes bounds each name (the cloud enforces the same cap).
const maxPeripheralNameRunes = 64

// peripheralsBluetoothTimeout bounds the BlueZ read: it runs on the
// check-in goroutine, which must never wedge on a stuck bluetoothd.
const peripheralsBluetoothTimeout = 3 * time.Second

const (
	peripheralStatusOK      = "ok"
	peripheralStatusOffline = "offline"
	peripheralStatusUnknown = "unknown"
)

type peripheral struct {
	kind, name, connection, status string
}

// remotePeripheralsReport builds the list. Never nil: an empty list tells
// the cloud this till reports devices and has none, where an absent field
// (an older till) means "not known". Each source failing just drops its
// devices — the heartbeat never fails over this.
func remotePeripheralsReport(ctx context.Context, d *common.Deps) []map[string]any {
	var list []peripheral
	list = append(list, receiptPrinterPeripheral(ctx, d)...)
	list = append(list, kitchenPrinterPeripherals(ctx, d)...)
	list = append(list, bluetoothPeripherals(ctx)...)
	list = append(list, fiscalDevicePeripherals(ctx, d)...)

	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.name != b.name {
			return a.name < b.name
		}
		if a.connection != b.connection {
			return a.connection < b.connection
		}
		return a.status < b.status
	})
	if len(list) > maxReportedPeripherals {
		list = list[:maxReportedPeripherals]
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		m := map[string]any{"kind": p.kind, "name": p.name, "status": p.status}
		if p.connection != "" {
			m["connection"] = p.connection
		}
		out = append(out, m)
	}
	return out
}

func receiptPrinterPeripheral(ctx context.Context, d *common.Deps) []peripheral {
	cfg, err := printerConfigChecked(ctx, d)
	if err != nil {
		logging.L().Warnf("cloudsync: peripherals report: printer settings: %v", err)
		return nil
	}
	p := peripheral{kind: "printer", status: peripheralStatusUnknown}
	switch cfg.Mode {
	case "network":
		p.connection = "network"
	case "device":
		p.connection = connectionForAddress(cfg.Device)
	case "system":
		// The OS print queue: its name is what the shop named the printer,
		// and how the OS reaches it is not ours to know.
		p.name = peripheralName(cfg.Address)
	default:
		return nil
	}
	return []peripheral{p}
}

// kitchenPrinterPeripherals: one device per distinct kitchen print
// destination — enabled printing stations (named after the first station,
// by name, that uses the destination), then the shop's default kitchen
// printer unless a station already covers it.
func kitchenPrinterPeripherals(ctx context.Context, d *common.Deps) []peripheral {
	var out []peripheral
	seen := map[string]bool{}
	stations, err := data.NewPOSRepo(d.Db).ListKitchenStations(ctx)
	if err != nil {
		logging.L().Warnf("cloudsync: peripherals report: kitchen stations: %v", err)
	}
	for _, s := range stations { // ListKitchenStations orders by name
		addr := strings.TrimSpace(s.PrinterAddress)
		if !s.Enabled || !s.PrintsTickets() || addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, peripheral{kind: "kitchen_printer", name: peripheralName(s.Name), connection: connectionForAddress(addr), status: peripheralStatusUnknown})
	}
	if v, _, err := d.Settings.Get(ctx, keyPrinterKitchen); err == nil {
		if addr := strings.TrimSpace(v); addr != "" && !seen[addr] {
			out = append(out, peripheral{kind: "kitchen_printer", connection: connectionForAddress(addr), status: peripheralStatusUnknown})
		}
	}
	return out
}

// bluetoothPeripherals reads the devices paired with this till's adapter.
// No Bluetooth on this box (most non-Pi targets, iOS) is the normal case
// and reports nothing.
func bluetoothPeripherals(ctx context.Context) []peripheral {
	c, err := newBluetoothClient()
	if err != nil {
		return nil
	}
	defer c.Close()
	lctx, cancel := context.WithTimeout(ctx, peripheralsBluetoothTimeout)
	defer cancel()
	devices, err := c.ListDevices(lctx)
	if err != nil {
		return nil
	}
	out := make([]peripheral, 0, len(devices))
	for _, dev := range devices {
		if !dev.Paired {
			continue
		}
		p := peripheral{kind: bluetoothKind(dev.Icon), name: peripheralName(dev.Name), connection: "bluetooth", status: peripheralStatusOffline}
		if dev.Connected {
			p.status = peripheralStatusOK
		}
		out = append(out, p)
	}
	return out
}

// bluetoothKind maps BlueZ's freedesktop icon hint. A HID barcode scanner
// pairs as a keyboard — the Bluetooth page exists to pair exactly those
// (ut-docs#76) — so a keyboard reports as a scanner.
func bluetoothKind(icon string) string {
	switch icon {
	case "input-keyboard", "scanner":
		return "scanner"
	case "printer":
		return "printer"
	}
	return "other"
}

// fiscalDevicePeripherals: the active plugin providing `fiscal.device`
// (ADR-0129 §2, exclusive), by its manifest name.
func fiscalDevicePeripherals(ctx context.Context, d *common.Deps) []peripheral {
	repo := data.NewPluginRepo(d.Db)
	ids, err := repo.PluginsProviding(ctx, plugins.CapabilityFiscalDevice, true)
	if err != nil || len(ids) == 0 {
		return nil
	}
	installed, err := repo.ListInstalledPlugins(ctx)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(installed))
	for _, p := range installed {
		names[p.ID] = p.Name
	}
	out := make([]peripheral, 0, len(ids))
	for _, id := range ids {
		out = append(out, peripheral{kind: "fiscal_device", name: peripheralName(names[id]), status: peripheralStatusUnknown})
	}
	return out
}

// connectionForAddress classifies a printer address without sending it: a
// character device by its Linux name, anything else is a network host.
func connectionForAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if !strings.HasPrefix(addr, "/") {
		if addr == "" {
			return ""
		}
		return "network"
	}
	base := addr[strings.LastIndex(addr, "/")+1:]
	switch {
	case strings.HasPrefix(base, "rfcomm"):
		return "bluetooth"
	case strings.HasPrefix(base, "tty"):
		return "serial"
	case strings.HasPrefix(base, "lp"), strings.HasPrefix(base, "usb"), strings.Contains(addr, "/usb/"):
		return "usb"
	}
	return ""
}

// macLike matches a hardware address in any common spelling — BlueZ uses
// the address itself (dashes) as the alias of a device with no name.
var macLike = regexp.MustCompile(`(?i)\b[0-9a-f]{2}([-:_]?[0-9a-f]{2}){5}\b`)

// peripheralName sanitises a display name for the report: control and
// format characters dropped (except ZWNJ/ZWJ, which Persian names and emoji
// sequences need — as config.NormalizeStoreName), whitespace collapsed, capped at
// maxPeripheralNameRunes, and blanked when it is really an address (an IP,
// host:port, a device path or a MAC) — the cloud labels a blank name by
// its kind.
func peripheralName(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r), unicode.In(r, unicode.Cf) && r != 0x200C && r != 0x200D:
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	name := b.String()
	if name == "" || looksLikeAddress(name) {
		return ""
	}
	if r := []rune(name); len(r) > maxPeripheralNameRunes {
		name = strings.TrimSpace(string(r[:maxPeripheralNameRunes]))
	}
	return name
}

func looksLikeAddress(s string) bool {
	if strings.HasPrefix(s, "/") || macLike.MatchString(s) {
		return true
	}
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	return net.ParseIP(strings.Trim(host, "[]")) != nil
}
