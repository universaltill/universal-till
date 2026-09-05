package packaging

// Regression guard for the Bluetooth D-Bus system-bus policy grant
// (ADR-0078, universaltill/ut-docs#76). This is the till's first non-
// file-ownership OS permission grant, and independent review flagged it as
// the one artifact in that PR with zero regression coverage — every other
// packaging change in this directory that touches a trust boundary
// (kiosk_setup_test.go's pos-writable-tree guards) has one, but a D-Bus
// policy file has no CI-visible BlueZ to exercise it against, so nothing
// failed if it silently regressed. Per ADR-0078's own account, the first
// draft of this exact file omitted the AgentManager1 grant and looked
// correct in review — the failure mode this test exists to catch is
// exactly that class of "looks right, isn't," not just a deleted file.
//
// Written as a property (parse the real XML, assert on its structure), not
// a string-contains snapshot: a syntactically different policy that still
// grants the same six interfaces to the same user should stay green, and a
// widened grant in any form should not.

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dbusAllow mirrors one <allow> element of a D-Bus system-bus policy file.
type dbusAllow struct {
	SendDestination string `xml:"send_destination,attr"`
	SendInterface   string `xml:"send_interface,attr"`
	SendType        string `xml:"send_type,attr"`
}

type dbusPolicy struct {
	User  string      `xml:"user,attr"`
	Group string      `xml:"group,attr"`
	Allow []dbusAllow `xml:"allow"`
	Deny  []struct{}  `xml:"deny"`
}

type dbusBusconfig struct {
	XMLName xml.Name     `xml:"busconfig"`
	Policy  []dbusPolicy `xml:"policy"`
}

// wantBluezInterfaces is the exact, smallest-grant-that-works interface set
// ADR-0078 decided on. A future card that needs another org.bluez interface
// extends this list deliberately — it is not meant to grow by accident.
var wantBluezInterfaces = []string{
	"org.freedesktop.DBus.ObjectManager",
	"org.freedesktop.DBus.Properties",
	"org.freedesktop.DBus.Introspectable",
	"org.bluez.Adapter1",
	"org.bluez.Device1",
	"org.bluez.AgentManager1",
}

func TestBluetoothDBusPolicyGrantsExactlyTheADR0078Interfaces(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("linux", "dbus-unitill-bluetooth.conf"))
	if err != nil {
		t.Fatalf("read dbus-unitill-bluetooth.conf: %v", err)
	}

	// The file has a DOCTYPE with an external DTD reference; encoding/xml
	// has no DTD fetcher and doesn't need one — strip everything up to and
	// including the DOCTYPE line before parsing, same shape as any other
	// XML file that carries one.
	content := string(raw)
	if idx := strings.Index(content, "<busconfig"); idx >= 0 {
		content = content[idx:]
	}

	var cfg dbusBusconfig
	if err := xml.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatalf("dbus-unitill-bluetooth.conf did not parse as XML: %v", err)
	}

	if len(cfg.Policy) != 1 {
		t.Fatalf("want exactly one <policy>, got %d", len(cfg.Policy))
	}
	pol := cfg.Policy[0]

	if pol.User != "pos" {
		t.Errorf("policy must scope to user=\"pos\" (the till service's system user, packaging/systemd/unitill-pos.service), got user=%q", pol.User)
	}
	if pol.Group != "" {
		t.Errorf("policy must not also carry a group= grant (widens beyond the till service user), got group=%q", pol.Group)
	}
	if len(pol.Deny) != 0 {
		t.Errorf("policy must carry no <deny> elements — an allow-list-only file is the whole point of the smallest-grant design")
	}

	if len(pol.Allow) != len(wantBluezInterfaces) {
		t.Fatalf("want exactly %d <allow> elements (one per ADR-0078 interface), got %d: %+v", len(wantBluezInterfaces), len(pol.Allow), pol.Allow)
	}

	got := map[string]bool{}
	for _, a := range pol.Allow {
		if a.SendDestination != "org.bluez" {
			t.Errorf("<allow> must scope to send_destination=\"org.bluez\", got %q (interface %q) — a missing/different destination widens the grant to every service on the system bus", a.SendDestination, a.SendInterface)
		}
		if a.SendInterface == "" {
			t.Errorf("<allow> with no send_interface grants ALL org.bluez interfaces, not a scoped set: %+v", a)
			continue
		}
		if a.SendType != "" {
			t.Errorf("<allow> for %q also carries send_type=%q — the six-interface allow-list is meant to be the only scoping, a send_type wildcard alongside it is redundant at best and a sign the interface scoping was dropped at worst", a.SendInterface, a.SendType)
		}
		if got[a.SendInterface] {
			t.Errorf("interface %q is granted more than once", a.SendInterface)
		}
		got[a.SendInterface] = true
	}

	for _, want := range wantBluezInterfaces {
		if !got[want] {
			t.Errorf("missing required grant for %q — internal/bluetooth calls this interface and will fail closed with AccessDenied in production without it (this is exactly the AgentManager1 gap ADR-0078's own review caught)", want)
		}
	}
	for iface := range got {
		found := false
		for _, want := range wantBluezInterfaces {
			if iface == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected grant for %q — not one of the six interfaces ADR-0078 decided on; widening this policy needs a deliberate update to both the .conf file and this test's wantBluezInterfaces list", iface)
		}
	}
}

// TestBluetoothDBusPolicyIsPackagedToSystemD guards the other half of the
// same grant: the .conf file doing the right thing is moot if nfpm never
// ships it, or ships it somewhere dbus-daemon doesn't watch.
func TestBluetoothDBusPolicyIsPackagedToSystemD(t *testing.T) {
	goreleaser, err := os.ReadFile(filepath.Join("..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	dst := nfpmDst(string(goreleaser), "dbus-unitill-bluetooth.conf")
	if dst == "" {
		t.Fatal(".goreleaser.yaml has no nfpm contents: entry shipping packaging/linux/dbus-unitill-bluetooth.conf — without it the policy grant never reaches an installed till and every Bluetooth call fails closed with AccessDenied")
	}
	// dbus-daemon on Debian/Ubuntu (this product's target) only watches
	// /etc/dbus-1/system.d/ for system-bus policy fragments — anywhere
	// else is silently never loaded, no error, no log line.
	if !strings.HasPrefix(dst, "/etc/dbus-1/system.d/") {
		t.Errorf("dbus-unitill-bluetooth.conf must install under /etc/dbus-1/system.d/ (the directory dbus-daemon actually watches), got dst=%q", dst)
	}
}
