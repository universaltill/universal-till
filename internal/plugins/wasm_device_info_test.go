package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0140 (ut-docs#3862): device_id_get, device_local_ips_get and
// device_timezone_get hand a plugin holding the review-gated, read-only
// device-info permission this till's identity — every grant and every
// denial is audited.

const deviceInfoPerm = "device-info"

// deviceInfoFns maps the guest's result prefix to the host function name
// recorded in the device_info_read audit row.
var deviceInfoFns = map[string]string{
	"id":       "device_id_get",
	"ips":      "device_local_ips_get",
	"timezone": "device_timezone_get",
}

// withDeviceAddrs replaces the interface-address seam for one test.
func withDeviceAddrs(t *testing.T, fn func() ([]net.Addr, error)) {
	t.Helper()
	prev := deviceInterfaceAddrs
	deviceInterfaceAddrs = fn
	t.Cleanup(func() { deviceInterfaceAddrs = prev })
}

func fixedAddrs(addrs ...net.Addr) func() ([]net.Addr, error) {
	return func() ([]net.Addr, error) { return addrs, nil }
}

func ipNet(s string) *net.IPNet {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

// newDeviceInfoRuntime loads the hostfn guest for pluginID on d. setup
// declares/grants permissions; "storage" is always granted (the guest
// stores its results).
func newDeviceInfoRuntime(t *testing.T, guest string, d *sql.DB, pluginID string, setup func()) *WasmRuntime {
	t.Helper()
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	if setup != nil {
		setup()
	}
	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.db = d
	return w
}

type auditRow struct {
	Function   string `json:"function"`
	Permission string `json:"permission"`
	Reason     string `json:"reason"`
}

func auditRows(t *testing.T, d *sql.DB, pluginID, action string) []auditRow {
	t.Helper()
	rows, err := d.Query(`SELECT data_json FROM audit_log WHERE entity_type = 'plugin' AND entity_id = ? AND action = ?`, pluginID, action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var r auditRow
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("audit data_json %q: %v", raw, err)
		}
		out = append(out, r)
	}
	return out
}

func TestDeviceInfoHostFunctions_Granted(t *testing.T) {
	guest := buildHostfnGuest(t)
	withDeviceAddrs(t, fixedAddrs(ipNet("192.168.1.20/24"), ipNet("127.0.0.1/8")))
	d := hostfnTestDB(t)
	const pluginID = "com.test.deviceinfo"
	w := newDeviceInfoRuntime(t, guest, d, pluginID, func() { grantPerm(t, d, pluginID, deviceInfoPerm) })

	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "device_info"})
	for prefix := range deviceInfoFns {
		if c, _ := res[prefix+"_code"].(float64); c <= 0 {
			t.Fatalf("%s code = %v, want a positive length", prefix, res[prefix+"_code"])
		}
	}
	id, _ := res["id_val"].(string)
	if u, err := uuid.Parse(id); err != nil || u.Version() != 4 || u.String() != id {
		t.Fatalf("device_id_get = %q, want a canonical v4 UUID (err %v)", id, err)
	}
	if res["ips_val"] != `["192.168.1.20"]` {
		t.Fatalf("device_local_ips_get = %v, want [\"192.168.1.20\"]", res["ips_val"])
	}
	if tz, _ := res["timezone_val"].(string); tz != formatUTCOffset(deviceNow()) {
		t.Fatalf("device_timezone_get = %q, want %q", tz, formatUTCOffset(deviceNow()))
	}

	// ADR-0140 D1: every grant is audited, one row per call, naming it.
	got := map[string]int{}
	for _, r := range auditRows(t, d, pluginID, "device_info_read") {
		got[r.Function]++
	}
	for _, fn := range deviceInfoFns {
		if got[fn] != 1 {
			t.Errorf("device_info_read audit rows for %s = %d, want 1 (all: %v)", fn, got[fn], got)
		}
	}
	if n := len(auditRows(t, d, pluginID, "permission_denied")); n != 0 {
		t.Errorf("a granted call wrote %d permission_denied rows", n)
	}
}

func TestDeviceInfoHostFunctions_Denied(t *testing.T) {
	guest := buildHostfnGuest(t)
	withDeviceAddrs(t, fixedAddrs(ipNet("192.168.1.20/24")))
	cases := []struct {
		name   string
		setup  func(t *testing.T, d *sql.DB, pluginID string)
		reason string
	}{
		{"not declared", func(*testing.T, *sql.DB, string) {}, "permission not declared"},
		{"declared but not granted", func(t *testing.T, d *sql.DB, pluginID string) {
			if err := data.NewPluginRepo(d).InsertPluginPermissions(context.Background(), nil, pluginID, []string{deviceInfoPerm}); err != nil {
				t.Fatal(err)
			}
		}, "permission not granted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			const pluginID = "com.test.deviceinfo.denied"
			w := newDeviceInfoRuntime(t, guest, d, pluginID, func() { tc.setup(t, d, pluginID) })
			res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "device_info"})
			for prefix, fn := range deviceInfoFns {
				if res[prefix+"_code"] != float64(hostErrDenied) {
					t.Errorf("%s = %v, want %d", fn, res[prefix+"_code"], hostErrDenied)
				}
				if v, ok := res[prefix+"_val"]; ok {
					t.Errorf("%s leaked %v on denial", fn, v)
				}
			}
			denied := auditRows(t, d, pluginID, "permission_denied")
			n := 0
			for _, r := range denied {
				if r.Permission == deviceInfoPerm {
					n++
					if r.Reason != tc.reason {
						t.Errorf("denial reason = %q, want %q", r.Reason, tc.reason)
					}
				}
			}
			if n != len(deviceInfoFns) {
				t.Errorf("permission_denied rows for device-info = %d, want %d (one per call)", n, len(deviceInfoFns))
			}
			if g := auditRows(t, d, pluginID, "device_info_read"); len(g) != 0 {
				t.Errorf("a denied call wrote %d device_info_read rows", len(g))
			}
		})
	}
}

// The id is minted once and survives a marketplace re-pair (rewriting
// marketplace.device_id / marketplace.token) — it is not derived from them.
// A fresh DB mints a different one.
func TestDeviceIDGet_StableAndIndependent(t *testing.T) {
	guest := buildHostfnGuest(t)
	ctx := context.Background()
	run := func(t *testing.T, d *sql.DB, w *WasmRuntime, pluginID string) string {
		t.Helper()
		res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "device_info"})
		id, _ := res["id_val"].(string)
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("device_id_get = %q (code %v), want a UUID", id, res["id_code"])
		}
		return id
	}
	const pluginID = "com.test.deviceid"
	d1 := hostfnTestDB(t)
	settings := data.NewSettingsRepo(d1)
	if err := settings.Set(ctx, "marketplace.device_id", "dev-original"); err != nil {
		t.Fatal(err)
	}
	w1 := newDeviceInfoRuntime(t, guest, d1, pluginID, func() { grantPerm(t, d1, pluginID, deviceInfoPerm) })
	first := run(t, d1, w1, pluginID)
	if first == "dev-original" {
		t.Fatal("device id copied marketplace.device_id")
	}
	if second := run(t, d1, w1, pluginID); second != first {
		t.Fatalf("second call = %q, want the same id %q", second, first)
	}
	stored, found, err := settings.Get(ctx, data.DeviceInfoIDSettingsKey)
	if err != nil || !found || stored != first {
		t.Fatalf("persisted %s = %q (err %v), want %q", data.DeviceInfoIDSettingsKey, stored, err, first)
	}
	// Simulated re-pair.
	for k, v := range map[string]string{"marketplace.device_id": "dev-repaired", "marketplace.token": "tok-new"} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if after := run(t, d1, w1, pluginID); after != first {
		t.Fatalf("after re-pair = %q, want the unchanged id %q", after, first)
	}

	d2 := hostfnTestDB(t)
	w2 := newDeviceInfoRuntime(t, guest, d2, pluginID, func() { grantPerm(t, d2, pluginID, deviceInfoPerm) })
	if other := run(t, d2, w2, pluginID); other == first {
		t.Fatalf("a second till's fresh DB minted the same id %q", other)
	}
}

// A blank stored id is never handed out as a placeholder.
func TestDeviceIDGet_BlankStoredIDFailsClosed(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.deviceid.blank"
	if err := data.NewSettingsRepo(d).Set(context.Background(), data.DeviceInfoIDSettingsKey, "  "); err != nil {
		t.Fatal(err)
	}
	w := newDeviceInfoRuntime(t, guest, d, pluginID, func() { grantPerm(t, d, pluginID, deviceInfoPerm) })
	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "device_info"})
	if res["id_code"] != float64(hostErrInternal) {
		t.Fatalf("device_id_get with a blank stored id = %v, want %d", res["id_code"], hostErrInternal)
	}
	// Nothing was handed out, so nothing is recorded as read.
	for _, r := range auditRows(t, d, pluginID, "device_info_read") {
		if r.Function == "device_id_get" {
			t.Errorf("device_id_get failed but wrote a device_info_read row: %v", r)
		}
	}
}

// A permission lookup that fails is an internal error (-3), not a denial:
// a granted plugin must not be told the operator revoked device-info.
func TestDeviceInfo_PermissionLookupErrorIsInternal(t *testing.T) {
	d := hostfnTestDB(t)
	const pluginID = "com.test.deviceinfo.lookup"
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, deviceInfoPerm)
	s := &hostState{pluginID: pluginID, db: d}
	if code := deviceInfoAllowed(context.Background(), s, "device_id_get"); code != 0 {
		t.Fatalf("granted: deviceInfoAllowed = %d, want 0", code)
	}
	if _, err := d.Exec(`DROP TABLE plugin_permissions`); err != nil {
		t.Fatalf("drop plugin_permissions: %v", err)
	}
	if code := deviceInfoAllowed(context.Background(), s, "device_id_get"); code != hostErrInternal {
		t.Fatalf("permission lookup failing: deviceInfoAllowed = %d, want %d", code, hostErrInternal)
	}
}

// No identity without an audit trail: a failed device_info_read write fails
// the call closed.
func TestDeviceInfo_AuditFailureFailsClosed(t *testing.T) {
	guest := buildHostfnGuest(t)
	withDeviceAddrs(t, fixedAddrs(ipNet("192.168.1.20/24")))
	d := hostfnTestDB(t)
	const pluginID = "com.test.deviceinfo.noaudit"
	w := newDeviceInfoRuntime(t, guest, d, pluginID, func() { grantPerm(t, d, pluginID, deviceInfoPerm) })
	if _, err := d.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatalf("drop audit_log: %v", err)
	}
	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "device_info"})
	for prefix, fn := range deviceInfoFns {
		if res[prefix+"_code"] != float64(hostErrInternal) {
			t.Errorf("%s with audit_log unwritable = %v, want %d", fn, res[prefix+"_code"], hostErrInternal)
		}
		if v, ok := res[prefix+"_val"]; ok {
			t.Errorf("%s handed out %v without an audit row", fn, v)
		}
	}
}

// Buffer ABI: a too-small dst returns the FULL length, so the guest can
// retry with a bigger buffer.
func TestDeviceInfo_SmallBufferReturnsFullLength(t *testing.T) {
	guest := buildHostfnGuest(t)
	withDeviceAddrs(t, fixedAddrs(ipNet("192.168.1.20/24")))
	d := hostfnTestDB(t)
	const pluginID = "com.test.deviceinfo.small"
	w := newDeviceInfoRuntime(t, guest, d, pluginID, func() { grantPerm(t, d, pluginID, deviceInfoPerm) })
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"mode": "device_info", "cap": 4})
	want := map[string]int{
		"id":       36, // canonical UUID
		"ips":      len(`["192.168.1.20"]`),
		"timezone": len(formatUTCOffset(deviceNow())),
	}
	for prefix, n := range want {
		if res[prefix+"_code"] != float64(n) {
			t.Errorf("%s with a 4-byte buffer = %v, want the full length %d", deviceInfoFns[prefix], res[prefix+"_code"], n)
		}
	}
}

func TestDeviceLocalIPsGet_OSErrorIsInternal(t *testing.T) {
	guest := buildHostfnGuest(t)
	withDeviceAddrs(t, func() ([]net.Addr, error) { return nil, errors.New("netlink: permission denied") })
	d := hostfnTestDB(t)
	const pluginID = "com.test.deviceips.err"
	w := newDeviceInfoRuntime(t, guest, d, pluginID, func() { grantPerm(t, d, pluginID, deviceInfoPerm) })
	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "device_info"})
	if res["ips_code"] != float64(hostErrInternal) {
		t.Fatalf("device_local_ips_get on an OS error = %v, want %d", res["ips_code"], hostErrInternal)
	}
}

func TestDeviceLocalIPsJSON(t *testing.T) {
	cases := []struct {
		name  string
		addrs []net.Addr
		want  string
	}{
		{"empty is an empty array", nil, `[]`},
		{"only loopback and unspecified", []net.Addr{
			ipNet("127.0.0.1/8"), ipNet("::1/128"), ipNet("0.0.0.0/0"), &net.IPAddr{IP: net.IPv6unspecified},
		}, `[]`},
		{"v4 and v6, zone stripped, deduplicated, sorted", []net.Addr{
			&net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth0"},
			ipNet("192.168.1.20/24"),
			ipNet("127.0.0.1/8"),
			ipNet("fe80::1/64"),
			ipNet("10.0.0.5/8"),
			&net.IPAddr{IP: net.ParseIP("192.168.1.20")},
			ipNet("2001:db8::7/64"),
		}, `["10.0.0.5","192.168.1.20","2001:db8::7","fe80::1"]`},
		{"IPv4-mapped IPv6 reported as IPv4", []net.Addr{
			&net.IPAddr{IP: net.ParseIP("::ffff:192.168.1.20")}, ipNet("192.168.1.20/24"),
		}, `["192.168.1.20"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDeviceAddrs(t, fixedAddrs(tc.addrs...))
			got, err := deviceLocalIPsJSON()
			if err != nil {
				t.Fatalf("deviceLocalIPsJSON: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("deviceLocalIPsJSON = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestFormatUTCOffset(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		offsetSec int
		want      string
	}{
		{3600, "UTC+01:00"},
		{0, "UTC+00:00"},
		{-5 * 3600, "UTC-05:00"},
		{-(5*3600 + 30*60), "UTC-05:30"},
		{5*3600 + 30*60, "UTC+05:30"},
		{12*3600 + 45*60, "UTC+12:45"},
		{-(9*3600 + 30*60), "UTC-09:30"},
	}
	for _, tc := range cases {
		got := formatUTCOffset(at.In(time.FixedZone("test", tc.offsetSec)))
		if got != tc.want {
			t.Errorf("formatUTCOffset(offset %ds) = %q, want %q", tc.offsetSec, got, tc.want)
		}
	}
}

// The host answers with the offset in force at call time (deviceNow), not
// one fixed at start-up.
func TestDeviceTimezoneGet_UsesCallTimeOffset(t *testing.T) {
	guest := buildHostfnGuest(t)
	prev := deviceNow
	deviceNow = func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.FixedZone("test", -(3*3600+30*60))) }
	t.Cleanup(func() { deviceNow = prev })
	d := hostfnTestDB(t)
	const pluginID = "com.test.devicetz"
	w := newDeviceInfoRuntime(t, guest, d, pluginID, func() { grantPerm(t, d, pluginID, deviceInfoPerm) })
	res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "device_info"})
	if res["timezone_val"] != "UTC-03:30" {
		t.Fatalf("device_timezone_get = %v (code %v), want UTC-03:30", res["timezone_val"], res["timezone_code"])
	}
}
