package pages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/bluetooth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#3479: the till reports the devices it already knows about on its
// sync device record, so my. can draw them under the till (#3478).

func peripheralsTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	return &common.Deps{Db: db, Settings: settings.NewStore(db)}
}

func setSettings(t *testing.T, d *common.Deps, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
}

func findPeripheral(list []map[string]any, kind string) map[string]any {
	for _, p := range list {
		if p["kind"] == kind {
			return p
		}
	}
	return nil
}

// The card's acceptance: a till with a receipt printer and a paired scanner
// reports both — and the wire carries no address, MAC or device path.
func TestPeripheralsReport_ReceiptPrinterAndPairedScanner(t *testing.T) {
	d := peripheralsTestDeps(t)
	setSettings(t, d, map[string]string{"printer.mode": "network", "printer.address": "192.168.1.50:9100"})
	stubBluetooth(t, &fakeBluetoothClient{devices: []bluetooth.Device{
		{Address: "AA:BB:CC:DD:EE:01", Name: "Barcode Scanner X1", Icon: "input-keyboard", Paired: true, Connected: true},
	}}, nil)

	got := remotePeripheralsReport(t.Context(), d)

	pr := findPeripheral(got, "printer")
	if pr == nil || pr["connection"] != "network" || pr["status"] != "unknown" {
		t.Fatalf("receipt printer = %+v, want kind printer / network / unknown in %+v", pr, got)
	}
	sc := findPeripheral(got, "scanner")
	if sc == nil || sc["name"] != "Barcode Scanner X1" || sc["connection"] != "bluetooth" || sc["status"] != "ok" {
		t.Fatalf("scanner = %+v, want the paired, connected BT scanner in %+v", sc, got)
	}
	raw, _ := json.Marshal(got)
	for _, leak := range []string{"192.168", "9100", "AA:BB", "EE:01"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("peripherals report leaks %q: %s", leak, raw)
		}
	}
}

func TestPeripheralsReport_PrinterConnectionFromSettings(t *testing.T) {
	cases := []struct {
		mode, address, device, wantConn, wantName string
	}{
		{"device", "", "/dev/usb/lp0", "usb", ""},
		{"device", "", "/dev/ttyUSB0", "serial", ""},
		{"device", "", "/dev/rfcomm0", "bluetooth", ""},
		{"system", "EPSON_TM_T20", "", "", "EPSON_TM_T20"},
		// A CUPS queue "name" that is really an address never leaves the till.
		{"system", "10.0.0.7", "", "", ""},
	}
	for _, c := range cases {
		d := peripheralsTestDeps(t)
		setSettings(t, d, map[string]string{"printer.mode": c.mode, "printer.address": c.address, "printer.device": c.device})
		stubBluetooth(t, nil, bluetooth.ErrUnavailable)
		pr := findPeripheral(remotePeripheralsReport(t.Context(), d), "printer")
		if pr == nil {
			t.Fatalf("%s %s%s: no printer reported", c.mode, c.address, c.device)
		}
		if conn, _ := pr["connection"].(string); conn != c.wantConn {
			t.Errorf("%s %s%s: connection = %q, want %q", c.mode, c.address, c.device, conn, c.wantConn)
		}
		if name, _ := pr["name"].(string); name != c.wantName {
			t.Errorf("%s %s%s: name = %q, want %q", c.mode, c.address, c.device, name, c.wantName)
		}
	}
}

func TestPeripheralsReport_PrinterOffAndNoBluetooth_EmptyList(t *testing.T) {
	d := peripheralsTestDeps(t)
	stubBluetooth(t, nil, bluetooth.ErrUnsupportedPlatform)
	got := remotePeripheralsReport(t.Context(), d)
	if got == nil || len(got) != 0 {
		t.Fatalf("got %+v, want an empty (non-nil) list: the till supports the report and has nothing to report", got)
	}
}

// Kitchen printers: one per physical destination. A display-only or
// disabled station is not a printer; two stations sharing one printer are
// one device; the shop-wide default kitchen printer is reported unless a
// station already names the same destination.
func TestPeripheralsReport_KitchenPrinters(t *testing.T) {
	d := peripheralsTestDeps(t)
	stubBluetooth(t, nil, bluetooth.ErrUnavailable)
	repo := data.NewPOSRepo(d.Db)
	ctx := t.Context()
	mustStation := func(name, dest, addr string) string {
		id, err := repo.CreateKitchenStation(ctx, name, dest, addr)
		if err != nil {
			t.Fatalf("create station %s: %v", name, err)
		}
		return id
	}
	mustStation("Bar", data.KitchenDestinationPrinter, "10.0.0.20:9100")
	mustStation("Grill", data.KitchenDestinationBoth, "10.0.0.20:9100") // same printer as Bar
	mustStation("Pass screen", data.KitchenDestinationDisplay, "")
	off := mustStation("Old fryer", data.KitchenDestinationPrinter, "/dev/usb/lp1")
	if err := repo.SetKitchenStationEnabled(ctx, off, false); err != nil {
		t.Fatal(err)
	}
	setSettings(t, d, map[string]string{"printer.kitchen_addr": "10.0.0.21"})

	var kitchen []map[string]any
	for _, p := range remotePeripheralsReport(ctx, d) {
		if p["kind"] == "kitchen_printer" {
			kitchen = append(kitchen, p)
		}
	}
	if len(kitchen) != 2 {
		t.Fatalf("kitchen printers = %+v, want 2 (Bar/Grill shared + default)", kitchen)
	}
	names := []string{kitchen[0]["name"].(string), kitchen[1]["name"].(string)}
	if !(names[0] == "" && names[1] == "Bar") && !(names[0] == "Bar" && names[1] == "") {
		t.Errorf("kitchen printer names = %q, want the station name and the unnamed default", names)
	}
	for _, k := range kitchen {
		if k["connection"] != "network" {
			t.Errorf("kitchen printer %+v: connection want network", k)
		}
	}
}

// A fiscal device comes from the ADR-0129 `fiscal.device` capability — the
// neutral seam — never from a plugin ID.
func TestPeripheralsReport_FiscalDeviceFromCapability(t *testing.T) {
	d := peripheralsTestDeps(t)
	stubBluetooth(t, nil, bluetooth.ErrUnavailable)
	seedTestPlugin(t, d.Db, "com.example.fiscal-device", "Example Fiscal Device", "1.0.0")
	if _, err := d.Db.Exec(`UPDATE plugins SET is_active = 1 WHERE id = ?`, "com.example.fiscal-device"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_provides(plugin_id, capability) VALUES(?, ?)`, "com.example.fiscal-device", plugins.CapabilityFiscalDevice); err != nil {
		t.Fatal(err)
	}
	fd := findPeripheral(remotePeripheralsReport(t.Context(), d), "fiscal_device")
	if fd == nil || fd["name"] != "Example Fiscal Device" || fd["status"] != "unknown" {
		t.Fatalf("fiscal device = %+v, want the capability provider's name", fd)
	}
	if _, ok := fd["connection"]; ok {
		t.Errorf("fiscal device connection is not known to core, want it omitted: %+v", fd)
	}
}

func TestPeripheralsReport_BluetoothMapping(t *testing.T) {
	d := peripheralsTestDeps(t)
	stubBluetooth(t, &fakeBluetoothClient{devices: []bluetooth.Device{
		{Address: "AA:BB:CC:DD:EE:02", Name: "Pocket Printer", Icon: "printer", Paired: true, Connected: false},
		{Address: "AA:BB:CC:DD:EE:03", Name: "AA-BB-CC-DD-EE-03", Icon: "", Paired: true}, // BlueZ's address alias
		{Address: "AA:BB:CC:DD:EE:04", Name: "Card reader", Icon: "phone", Paired: true, Connected: true},
	}}, nil)
	got := remotePeripheralsReport(t.Context(), d)
	pr := findPeripheral(got, "printer")
	if pr == nil || pr["status"] != "offline" || pr["connection"] != "bluetooth" {
		t.Errorf("BT printer = %+v, want printer/bluetooth/offline", pr)
	}
	others := 0
	for _, p := range got {
		if p["kind"] == "other" {
			others++
			if n, _ := p["name"].(string); strings.Contains(n, "EE-03") {
				t.Errorf("an address-shaped BT name leaked: %+v", p)
			}
		}
	}
	if others != 2 {
		t.Errorf("other devices = %d, want 2 in %+v", others, got)
	}
}

// The check-in hashes the device record: the same devices in a different
// order must produce the same report, or every tick would POST.
func TestPeripheralsReport_StableOrder(t *testing.T) {
	d := peripheralsTestDeps(t)
	devs := []bluetooth.Device{
		{Address: "AA:BB:CC:DD:EE:10", Name: "Zeta", Icon: "input-keyboard", Paired: true, Connected: true},
		{Address: "AA:BB:CC:DD:EE:11", Name: "Alpha", Icon: "input-keyboard", Paired: true, Connected: true},
		{Address: "AA:BB:CC:DD:EE:12", Name: "Mid", Icon: "printer", Paired: true},
	}
	stubBluetooth(t, &fakeBluetoothClient{devices: devs}, nil)
	a, _ := json.Marshal(remotePeripheralsReport(t.Context(), d))
	rev := []bluetooth.Device{devs[2], devs[0], devs[1]}
	stubBluetooth(t, &fakeBluetoothClient{devices: rev}, nil)
	b, _ := json.Marshal(remotePeripheralsReport(t.Context(), d))
	if string(a) != string(b) {
		t.Fatalf("report depends on input order:\n%s\n%s", a, b)
	}
}

func TestPeripheralsReport_BoundedAndSanitised(t *testing.T) {
	d := peripheralsTestDeps(t)
	var devs []bluetooth.Device
	long := strings.Repeat("é", 100) + "\x07‮"
	for i := 0; i < 50; i++ {
		devs = append(devs, bluetooth.Device{Address: "AA:BB:CC:DD:EE:FF", Name: long + string(rune('A'+i%26)), Icon: "input-keyboard", Paired: true})
	}
	stubBluetooth(t, &fakeBluetoothClient{devices: devs}, nil)
	got := remotePeripheralsReport(t.Context(), d)
	if len(got) != maxReportedPeripherals {
		t.Fatalf("len = %d, want the %d cap", len(got), maxReportedPeripherals)
	}
	for _, p := range got {
		n := p["name"].(string)
		if utf8.RuneCountInString(n) > 64 {
			t.Errorf("name has %d runes, want <= 64", utf8.RuneCountInString(n))
		}
		if strings.ContainsAny(n, "\x07‮") {
			t.Errorf("name keeps a control/format character: %q", n)
		}
	}
}

func TestPeripheralsReport_BluetoothListBounded(t *testing.T) {
	d := peripheralsTestDeps(t)
	fake := &fakeBluetoothClient{}
	stubBluetooth(t, fake, nil)
	_ = remotePeripheralsReport(t.Context(), d)
	if !fake.listCtxHadDeadline {
		t.Error("the BlueZ list call must run under a deadline: it is on the check-in goroutine")
	}
	if fake.closed != 1 {
		t.Errorf("client closed %d times, want 1", fake.closed)
	}
}

// The report rides DeviceExtra, on every till (main or additional).
func TestCloudHooks_DeviceExtraCarriesPeripherals(t *testing.T) {
	d := peripheralsTestDeps(t)
	stubBluetooth(t, nil, bluetooth.ErrUnavailable)
	// An additional till: the report is per till, and a satellite never
	// creates the main till's directive key (no ./data write from a test).
	setSettings(t, d, map[string]string{"printer.mode": "device", "printer.device": "/dev/usb/lp0", "sync.primary_url": "http://main.local:8080"})
	extra := buildCloudHooks(d, nil).DeviceExtra(context.Background())
	list, ok := extra["peripherals"].([]map[string]any)
	if !ok || findPeripheral(list, "printer") == nil {
		t.Fatalf("DeviceExtra peripherals = %#v, want the receipt printer", extra["peripherals"])
	}
}

// TestPeripheralName_KeepsZWNJAndZWJ: Persian names need ZWNJ (U+200C) and
// emoji sequences ZWJ (U+200D); other format characters are still dropped.
func TestPeripheralName_KeepsZWNJAndZWJ(t *testing.T) {
	cases := map[string]string{
		"آشپزخانه‌ی اصلی": "آشپزخانه‌ی اصلی",
		"a‍b":             "a‍b",
		"a​b‮c":           "abc",
	}
	for in, want := range cases {
		if got := peripheralName(in); got != want {
			t.Errorf("peripheralName(%q) = %q, want %q", in, got, want)
		}
	}
}
