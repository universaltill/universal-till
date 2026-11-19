package plugins

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// listPermissionRows is permission -> granted for one plugin.
func listPermissionRows(t *testing.T, d *sql.DB, pluginID string) (map[string]bool, error) {
	t.Helper()
	rows, err := data.NewPluginRepo(d).ListPermissions(context.Background(), pluginID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Permission] = r.Granted
	}
	return out, nil
}

// ut-docs#6455 (ADR-0198 §3.1): net:payment_broker and device:tap_to_pay are
// manager-granted permissions of a payment plugin. net:payment_broker is NOT
// a host, and no host or wildcard grant implies it.

func brokerManifestJSON(canonical, permissions, entryConfig string) string {
	return `{"id":"com.test.tap","name":"Tap","version":"1.0.0","runtime":"wasm","entrypoint":"plugin.wasm",
	"canonical_type":"` + canonical + `","permissions":[` + permissions + `],
	"entries":[{"type":"payment","key":"stripe_tap","label":"Tap to Pay","trigger_event":"payment.stripe_tap.requested"` + entryConfig + `}]}`
}

func TestBrokerPermissionsKnownAndGrantedZero(t *testing.T) {
	for _, p := range []string{"net:payment_broker", "device:tap_to_pay"} {
		if !isKnownPermission(p) {
			t.Errorf("isKnownPermission(%q) = false", p)
		}
		if !exactPermissions[p] {
			t.Errorf("%q must be an exact permission, not left to the net: host rule", p)
		}
	}
	for _, p := range []string{"net:payment_broker:443", "device:tap_to_pay:x", "device:*", "device:tap_to_pay "} {
		if isKnownPermission(p) {
			t.Errorf("isKnownPermission(%q) = true", p)
		}
	}
	m, err := ParseManifest(strings.NewReader(brokerManifestJSON("payment", `"net:payment_broker","device:tap_to_pay","storage"`, `,"config":{"tap":true}`)))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	d := openRealDB(t)
	if err := PersistManifest(context.Background(), d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}
	rows, err := listPermissionRows(t, d.DB, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"net:payment_broker", "device:tap_to_pay"} {
		granted, declared := rows[p]
		if !declared || granted {
			t.Fatalf("%s: declared=%v granted=%v, want declared and NOT granted", p, declared, granted)
		}
	}
}

func TestBrokerPermissionsOnlyForPaymentPlugins(t *testing.T) {
	for _, perm := range []string{"net:payment_broker", "device:tap_to_pay"} {
		if _, err := ParseManifest(strings.NewReader(brokerManifestJSON("integration", `"`+perm+`"`, ""))); err == nil {
			t.Errorf("%s on an integration plugin was accepted", perm)
		}
		if _, err := ParseManifest(strings.NewReader(brokerManifestJSON("payment", `"`+perm+`"`, ""))); err != nil {
			t.Errorf("%s on a payment plugin was refused: %v", perm, err)
		}
	}
}

func TestTapEntryNeedsDevicePermission(t *testing.T) {
	if _, err := ParseManifest(strings.NewReader(brokerManifestJSON("payment", `"net:payment_broker"`, `,"config":{"tap":true}`))); err == nil {
		t.Fatal("a tap entry without device:tap_to_pay was accepted")
	}
	if _, err := ParseManifest(strings.NewReader(brokerManifestJSON("payment", `"device:tap_to_pay"`, `,"config":{"tap":true}`))); err != nil {
		t.Fatalf("a tap entry with device:tap_to_pay was refused: %v", err)
	}
	// tap is a payment-entry property.
	m := `{"id":"com.test.tap","name":"Tap","version":"1.0.0","runtime":"wasm","entrypoint":"plugin.wasm","canonical_type":"payment",
	"permissions":["device:tap_to_pay"],"entries":[{"type":"page","key":"p","label":"P","route":"/p","config":{"tap":true}}]}`
	if _, err := ParseManifest(strings.NewReader(m)); err == nil {
		t.Fatal("tap:true on a page entry was accepted")
	}
	// tap must be the boolean true, not a truthy string.
	if _, err := ParseManifest(strings.NewReader(brokerManifestJSON("payment", `"device:tap_to_pay"`, `,"config":{"tap":"yes"}`))); err == nil {
		t.Fatal(`config tap:"yes" was accepted`)
	}
}

func TestIsTapEntryConfig(t *testing.T) {
	for cfg, want := range map[string]bool{
		`{"tap":true}`: true, `{"tap":true,"x":1}`: true,
		`{"tap":false}`: false, `{"tap":"true"}`: false, `{}`: false, ``: false, `not json`: false, `{"TAP":true}`: false,
	} {
		if got := IsTapEntryConfig(cfg); got != want {
			t.Errorf("IsTapEntryConfig(%q) = %v, want %v", cfg, got, want)
		}
	}
}

// net:payment_broker is a name, not a host: it grants no egress, no network
// widening, and net:* does not grant it.
func TestNetStarDoesNotImplyBroker(t *testing.T) {
	d := hostfnTestDB(t)
	const id = "com.test.netstar"
	seedPlugin(t, d, id)
	grantPerm(t, d, id, "net:*")
	if ok, err := CheckPermissionGranted(context.Background(), d, id, "net:payment_broker"); err != nil || ok {
		t.Fatalf("net:* implied net:payment_broker (granted=%v err=%v)", ok, err)
	}

	const id2 = "com.test.brokeronly"
	seedPlugin(t, d, id2)
	grantPerm(t, d, id2, "net:payment_broker")
	if pluginHasNetPermission(context.Background(), d, id2) {
		t.Fatal("net:payment_broker widened the plugin's network timeout")
	}
	needs, err := PluginNetworkNeeds(context.Background(), d, id2)
	if err != nil || needs.Any || len(needs.Hosts) != 0 || needs.Unprobeable {
		t.Fatalf("PluginNetworkNeeds = %+v %v, want no network need", needs, err)
	}
	exact, wildcard, err := netGrantMatch(context.Background(), d, id2, "payment_broker")
	if err != nil || exact || wildcard {
		t.Fatalf("net:payment_broker grants the host payment_broker (exact=%v wildcard=%v err=%v)", exact, wildcard, err)
	}
}

func TestTapGateTimeoutOnlyForTapEntries(t *testing.T) {
	d := hostfnTestDB(t)
	const id = "com.test.taptimeout"
	seedPlugin(t, d, id)
	for _, e := range []struct{ key, cfg string }{{"stripe_tap", `{"tap":true}`}, {"plain_card", `{}`}} {
		if _, err := d.Exec(`INSERT INTO plugin_entries (id, plugin_id, type, key, label, trigger_event, config_json, is_active)
			VALUES (?, ?, 'payment', ?, ?, ?, ?, 1)`, "e-"+e.key, id, e.key, e.key, "payment."+e.key+".requested", e.cfg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`UPDATE plugins SET is_active = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	grantPerm(t, d, id, "net:api.example.com")
	w := NewWasmRuntime(t.TempDir())
	w.db = d
	w.refreshTapEntries(context.Background(), d)

	if got := w.timeoutFor(id, "payment.stripe_tap.authorize"); got != TapGateTimeout || TapGateTimeout != 120*time.Second {
		t.Fatalf("tap authorize timeout = %v (TapGateTimeout %v), want 120s", got, TapGateTimeout)
	}
	if got := w.timeoutFor(id, "payment.stripe_tap.refund"); got != TapGateTimeout {
		t.Fatalf("tap refund timeout = %v, want 120s", got)
	}
	// Other entries of the same plugin and other event classes are unchanged.
	if got := w.timeoutFor(id, "payment.plain_card.authorize"); got >= TapGateTimeout {
		t.Fatalf("non-tap entry timeout = %v, must stay below the tap timeout", got)
	}
	if got := w.timeoutFor(id, "payment.stripe_tap.requested"); got >= TapGateTimeout {
		t.Fatalf("a non-gate event of a tap entry got %v", got)
	}
	if got := w.timeoutFor("com.other.plugin", "payment.stripe_tap.authorize"); got >= TapGateTimeout {
		t.Fatalf("another plugin using the same entry key got %v", got)
	}
}
