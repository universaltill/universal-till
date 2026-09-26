package plugins

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/idna"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2899: setting-bound exact grants. A plugin whose target is an
// ADMIN-configured endpoint (tax-tr's ÖKC bridge / LAN register, a webhook to
// a LAN ERP) declares net:@setting:<urlKey> or tcp:@setting:<hostKey>:<portKey>;
// the address currently stored in those settings counts as the exact grant —
// resolved at dial time, so changing the setting moves the grant, and no
// other LAN host is reachable.

// setPluginSetting stores a plain string setting the way the settings editor
// does (a JSON string in value_json).
func setPluginSetting(t *testing.T, d *sql.DB, pluginID, key, value string) {
	t.Helper()
	raw, _ := json.Marshal(value)
	if err := data.NewPluginRepo(d).UpsertPluginSettingScoped(context.Background(), pluginID, key, string(raw), "global", false); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func TestNormGrantHost(t *testing.T) {
	puny, err := idna.Lookup.ToASCII("kasa-ö.example")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"Example.COM", "example.com"},
		{"example.com.", "example.com"},
		{"[::1]", "::1"},
		{"::1", "::1"},
		{"0:0:0:0:0:0:0:1", "::1"},
		{"[0:0::1]", "::1"},
		{"::ffff:127.0.0.1", "127.0.0.1"},
		{"192.168.1.50", "192.168.1.50"},
		{"KASA-Ö.example.", puny},
		{puny, puny},
	}
	for _, c := range cases {
		if got := normGrantHost(c.in); got != c.want {
			t.Errorf("normGrantHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseSettingBoundPermission(t *testing.T) {
	cases := []struct {
		perm      string
		wantKeys  []string
		wantBound bool
		wantErr   bool
	}{
		{perm: "net:*"},
		{perm: "net:api.example.com"},
		{perm: "tcp:192.168.1.50:4711"},
		{perm: "storage"},
		{perm: "net:@setting:endpoint_url", wantKeys: []string{"endpoint_url"}, wantBound: true},
		{perm: "tcp:@setting:okc.host:okc.port", wantKeys: []string{"okc.host", "okc.port"}, wantBound: true},
		{perm: "net:@setting:", wantBound: true, wantErr: true},
		{perm: "net:@setting:a:b", wantBound: true, wantErr: true},
		{perm: "tcp:@setting:okc.host", wantBound: true, wantErr: true},
		{perm: "tcp:@setting:okc.host:", wantBound: true, wantErr: true},
		{perm: "tcp:@setting:a:b:c", wantBound: true, wantErr: true},
		{perm: "net:@other:x", wantBound: true, wantErr: true},
		{perm: "tcp:@x", wantBound: true, wantErr: true},
	}
	for _, c := range cases {
		keys, bound, err := ParseSettingBoundPermission(c.perm)
		if bound != c.wantBound || (err != nil) != c.wantErr {
			t.Errorf("%q: bound=%v err=%v, want bound=%v err=%v", c.perm, bound, err, c.wantBound, c.wantErr)
			continue
		}
		if !c.wantErr && strings.Join(keys, ",") != strings.Join(c.wantKeys, ",") {
			t.Errorf("%q: keys=%v, want %v", c.perm, keys, c.wantKeys)
		}
	}
}

func TestParseManifestSettingBoundPermissions(t *testing.T) {
	const head = `{"id":"com.test.bound","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm",
	"settings":[{"key":"okc.host","default_value":"127.0.0.1"},{"key":"okc.port","default_value":"4711"},{"key":"endpoint_url"}],
	"permissions":`
	ok := []string{
		`["tcp:@setting:okc.host:okc.port"]`,
		`["net:@setting:endpoint_url","net:*"]`,
	}
	for _, p := range ok {
		if _, err := ParseManifest(strings.NewReader(head + p + `}`)); err != nil {
			t.Errorf("%s: unexpected error %v", p, err)
		}
	}
	bad := []string{
		`["tcp:@setting:okc.host:okc.missing"]`, // port key not a declared setting
		`["net:@setting:nope"]`,                 // url key not a declared setting
		`["net:@setting:"]`,
		`["tcp:@setting:okc.host"]`,
		`["net:@anything"]`,
	}
	for _, p := range bad {
		if _, err := ParseManifest(strings.NewReader(head + p + `}`)); err == nil {
			t.Errorf("%s: manifest accepted, want refusal", p)
		}
	}
}

func TestHostTCPSettingBoundGrant(t *testing.T) {
	guest := buildTCPGuest(t)
	fxHost, fxPort, accepts := echoFixture(t)
	fxAddr := net.JoinHostPort(fxHost, strconv.Itoa(fxPort))
	_, tillPort, tillAccepts := echoFixture(t)
	_, otherPort, otherAccepts := echoFixture(t)
	puny, _ := idna.Lookup.ToASCII("kasa-ö.example")

	env := &egressEnv{
		dns: map[string][]string{
			"localhost":      {"127.0.0.1"},
			"kasa.lan":       {"192.168.1.50"},
			puny:             {"192.168.1.50"},
			"other.lan":      {"192.168.1.51"},
			"public.example": {"93.184.216.34"},
		},
		route: map[string]string{
			"192.168.1.50": fxAddr, "192.168.1.51": fxAddr, "93.184.216.34": fxAddr, "::1": fxAddr,
		},
		tillPort: tillPort,
	}
	withTCPEgress(t, env)
	const bound = "tcp:@setting:okc.host:okc.port"
	port := strconv.Itoa(fxPort)

	type tc struct {
		name           string
		perms          []string
		setHost, setPt string
		host           string
		port           int
		wantOK         bool
		noHit          func() int32
	}
	cases := []tc{
		{name: "configured loopback bridge", perms: []string{bound}, setHost: fxHost, setPt: port, host: fxHost, port: fxPort, wantOK: true},
		{name: "configured LAN register", perms: []string{bound}, setHost: "192.168.1.50", setPt: port, host: "192.168.1.50", port: fxPort, wantOK: true},
		{name: "configured LAN register by name", perms: []string{bound}, setHost: "kasa.lan", setPt: port, host: "kasa.lan", port: fxPort, wantOK: true},
		{name: "another LAN host refused", perms: []string{bound}, setHost: "192.168.1.50", setPt: port, host: "192.168.1.51", port: fxPort, noHit: accepts},
		{name: "a name resolving to another LAN host refused", perms: []string{bound}, setHost: "kasa.lan", setPt: port, host: "other.lan", port: fxPort, noHit: accepts},
		{name: "configured host on another port refused", perms: []string{bound}, setHost: fxHost, setPt: port, host: fxHost, port: otherPort, noHit: otherAccepts},
		{name: "public host not granted by the setting alone", perms: []string{bound}, setHost: "192.168.1.50", setPt: port, host: "public.example", port: fxPort, noHit: accepts},
		{name: "till's own port refused even when configured", perms: []string{bound}, setHost: fxHost, setPt: strconv.Itoa(tillPort), host: fxHost, port: tillPort, noHit: tillAccepts},
		{name: "till's own port refused via localhost too", perms: []string{bound, "tcp:*"}, setHost: "localhost", setPt: strconv.Itoa(tillPort), host: "localhost", port: tillPort, noHit: tillAccepts},
		{name: "declared but not granted", perms: nil, setHost: fxHost, setPt: port, host: fxHost, port: fxPort, noHit: accepts},
		{name: "setting upper-case + trailing dot", perms: []string{bound}, setHost: "LocalHost.", setPt: port, host: "localhost", port: fxPort, wantOK: true},
		{name: "setting [::1], plugin asks ::1", perms: []string{bound}, setHost: "[::1]", setPt: port, host: "::1", port: fxPort, wantOK: true},
		{name: "setting ::1, plugin asks [::1]", perms: []string{bound}, setHost: "::1", setPt: port, host: "[::1]", port: fxPort, wantOK: true},
		{name: "setting unicode IDN, plugin asks punycode", perms: []string{bound}, setHost: "KASA-Ö.example.", setPt: port, host: puny, port: fxPort, wantOK: true},
		{name: "exact grant compared case-insensitively", perms: []string{"tcp:KASA.LAN:" + port}, host: "kasa.lan", port: fxPort, wantOK: true},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			pluginID := fmt.Sprintf("com.test.tcpbound%d", i)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			if c.perms == nil {
				// declared (as the manifest would) but never granted
				if err := data.NewPluginRepo(d).InsertPluginPermissions(context.Background(), nil, pluginID, []string{bound}); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
			}
			if c.setHost != "" {
				setPluginSetting(t, d, pluginID, "okc.host", c.setHost)
				setPluginSetting(t, d, pluginID, "okc.port", c.setPt)
			}
			w := newTCPTestRuntime(t, guest, pluginID)
			var before int32
			if c.noHit != nil {
				before = c.noHit()
			}
			res := runTCPGuest(t, w, d, pluginID, map[string]any{
				"mode": "roundtrip", "host": c.host, "port": c.port,
				"connect_timeout_ms": 2000, "read_timeout_ms": 2000, "send": "ping",
			})
			if c.wantOK {
				if res["open_code"] != float64(0) || res["read_data"] != "ping" {
					t.Fatalf("open_code = %v read_data = %v, want an echoed round trip", res["open_code"], res["read_data"])
				}
				return
			}
			if res["open_code"] != float64(hostErrDenied) {
				t.Fatalf("open_code = %v, want %d (denied)", res["open_code"], hostErrDenied)
			}
			if c.noHit != nil {
				time.Sleep(50 * time.Millisecond)
				if c.noHit() != before {
					t.Fatal("refused tcp_open still reached the target")
				}
			}
		})
	}
}

// Changing the setting moves the grant: an open handle to the OLD address is
// denied on its next write/read, the old address can no longer be opened,
// and the new one can.
func TestHostTCPSettingChangeMovesGrant(t *testing.T) {
	guest := buildTCPGuest(t)
	fxHost, fxPort, _, conns := startTCPFixture(t, nil)
	fxAddr := net.JoinHostPort(fxHost, strconv.Itoa(fxPort))
	echoHost, echoPort, _ := echoFixture(t)
	echoAddr := net.JoinHostPort(echoHost, strconv.Itoa(echoPort))
	env := &egressEnv{
		dns:   map[string][]string{},
		route: map[string]string{"192.168.1.50": fxAddr, "192.168.1.60": echoAddr},
	}
	withTCPEgress(t, env)

	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpboundmove"
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "tcp:@setting:okc.host:okc.port")
	setPluginSetting(t, d, pluginID, "okc.host", "192.168.1.50")
	setPluginSetting(t, d, pluginID, "okc.port", strconv.Itoa(fxPort))

	w := newTCPTestRuntime(t, guest, pluginID)
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "openonly", "host": "192.168.1.50", "port": fxPort, "connect_timeout_ms": 2000,
	})
	if res["open_code"] != float64(0) {
		t.Fatalf("open_code = %v, want 0", res["open_code"])
	}
	select {
	case c := <-conns:
		defer c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("fixture never received the connection")
	}

	setPluginSetting(t, d, pluginID, "okc.host", "192.168.1.60")
	setPluginSetting(t, d, pluginID, "okc.port", strconv.Itoa(echoPort))

	ctx := withHostState(context.Background(), &hostState{pluginID: pluginID, db: d})
	if got := hostTCPWrite(ctx, nil, 0, 0, 0); got != hostErrDenied {
		t.Errorf("tcp_write on the old address after the setting moved = %d, want %d", got, hostErrDenied)
	}
	if got := hostTCPRead(ctx, nil, 0, 0, 1, 100); got != hostErrDenied {
		t.Errorf("tcp_read on the old address after the setting moved = %d, want %d", got, hostErrDenied)
	}
	tcpConns.CloseAll(pluginID)
	res = runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "openonly", "host": "192.168.1.50", "port": fxPort, "connect_timeout_ms": 2000,
	})
	if res["open_code"] != float64(hostErrDenied) {
		t.Errorf("open to the old address = %v, want denied", res["open_code"])
	}
	tcpConns.CloseAll(pluginID)
	res = runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "roundtrip", "host": "192.168.1.60", "port": echoPort,
		"connect_timeout_ms": 2000, "read_timeout_ms": 2000, "send": "ping",
	})
	if res["open_code"] != float64(0) || res["read_data"] != "ping" {
		t.Errorf("new address: open_code = %v read_data = %v, want a round trip", res["open_code"], res["read_data"])
	}
}

func TestHTTPSettingBoundGrant(t *testing.T) {
	guest := buildHostfnGuest(t)
	cert, pool := egressTestCert(t)
	var tlsHits, tillHits atomic.Int32
	till := httptest.NewServer(hitCounter(&tillHits, "till"))
	defer till.Close()
	mux := http.NewServeMux()
	mux.Handle("/ok", hitCounter(&tlsHits, "ok"))
	mux.HandleFunc("/to-other-lan", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.0.0.5/ok", http.StatusFound)
	})
	mux.HandleFunc("/to-self", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	tlsSrv := httptest.NewUnstartedServer(mux)
	tlsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	tlsAddr := tlsSrv.Listener.Addr().String()
	tillPort, _ := strconv.Atoi(mustPort(t, till.URL))

	env := &egressEnv{
		dns: map[string][]string{
			"erp.example.com": {"192.168.1.50"},
			"api.example.com": {"93.184.216.34"},
			"localhost":       {"127.0.0.1"},
		},
		route: map[string]string{
			"192.168.1.50": tlsAddr, "10.0.0.5": tlsAddr, "93.184.216.34": tlsAddr,
		},
		tillPort: tillPort,
	}
	const bound = "net:@setting:endpoint_url"

	type tc struct {
		name       string
		perms      []string
		setting    string
		url        string
		wantStatus int
		noHit      *atomic.Int32
	}
	cases := []tc{
		{name: "configured LAN ERP", perms: []string{bound}, setting: "https://192.168.1.50/hook", url: "https://192.168.1.50/ok", wantStatus: 200},
		{name: "configured LAN ERP by name", perms: []string{bound}, setting: "https://erp.example.com/hook", url: "https://erp.example.com/ok", wantStatus: 200},
		{name: "setting normalised (case, trailing dot)", perms: []string{bound}, setting: "https://ERP.Example.COM./hook", url: "https://erp.example.com/ok", wantStatus: 200},
		{name: "another LAN host refused", perms: []string{bound, "net:*"}, setting: "https://192.168.1.50/hook", url: "https://10.0.0.5/ok", noHit: &tlsHits},
		{name: "redirect from the configured host to another LAN host refused", perms: []string{bound, "net:*"}, setting: "https://192.168.1.50/hook", url: "https://192.168.1.50/to-other-lan"},
		{name: "redirect within the configured host allowed", perms: []string{bound}, setting: "https://192.168.1.50/hook", url: "https://192.168.1.50/to-self", wantStatus: 200},
		{name: "public host not granted by the setting alone", perms: []string{bound}, setting: "https://192.168.1.50/hook", url: "https://api.example.com/ok", noHit: &tlsHits},
		{name: "public host still reachable under net:*", perms: []string{bound, "net:*"}, setting: "https://192.168.1.50/hook", url: "https://api.example.com/ok", wantStatus: 200},
		{name: "till's own port refused even when configured", perms: []string{bound}, setting: "http://localhost:" + strconv.Itoa(tillPort) + "/", url: "http://localhost:" + strconv.Itoa(tillPort) + "/x", noHit: &tillHits},
		{name: "unparseable setting grants nothing", perms: []string{bound}, setting: "192.168.1.50", url: "https://192.168.1.50/ok", noHit: &tlsHits},
		{name: "exact net grant compared case-insensitively", perms: []string{"net:ERP.example.com"}, url: "https://erp.example.com/ok", wantStatus: 200},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			pluginID := fmt.Sprintf("com.test.netbound%d", i)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
			}
			if c.setting != "" {
				setPluginSetting(t, d, pluginID, "endpoint_url", c.setting)
			}
			w := NewWasmRuntime(t.TempDir())
			w.httpClient = env.client(pool)
			if err := w.load(pluginID, "1.0.0", guest); err != nil {
				t.Fatalf("load: %v", err)
			}
			w.hasNet[pluginID] = true
			var before int32
			if c.noHit != nil {
				before = c.noHit.Load()
			}
			res := runGuest(t, w, d, pluginID, c.url)
			if c.wantStatus != 0 {
				if res["http_status"] != float64(c.wantStatus) {
					t.Fatalf("http_status = %v (code %v), want %d", res["http_status"], res["http_code"], c.wantStatus)
				}
				return
			}
			if res["http_code"] != float64(hostErrDenied) {
				t.Fatalf("http_code = %v (status %v), want %d (denied)", res["http_code"], res["http_status"], hostErrDenied)
			}
			if c.noHit != nil && c.noHit.Load() != before {
				t.Fatal("refused request still reached the target server")
			}
		})
	}

	// Changing the setting moves the grant.
	t.Run("setting change moves the grant", func(t *testing.T) {
		d := hostfnTestDB(t)
		const pluginID = "com.test.netboundmove"
		seedPlugin(t, d, pluginID)
		grantPerm(t, d, pluginID, "storage")
		grantPerm(t, d, pluginID, bound)
		setPluginSetting(t, d, pluginID, "endpoint_url", "https://192.168.1.50/hook")
		w := NewWasmRuntime(t.TempDir())
		w.httpClient = env.client(pool)
		if err := w.load(pluginID, "1.0.0", guest); err != nil {
			t.Fatalf("load: %v", err)
		}
		w.hasNet[pluginID] = true
		if res := runGuest(t, w, d, pluginID, "https://192.168.1.50/ok"); res["http_status"] != float64(200) {
			t.Fatalf("before: %v", res)
		}
		setPluginSetting(t, d, pluginID, "endpoint_url", "https://10.0.0.5/hook")
		if res := runGuest(t, w, d, pluginID, "https://192.168.1.50/ok"); res["http_code"] != float64(hostErrDenied) {
			t.Fatalf("old host after the move: %v, want denied", res)
		}
		if res := runGuest(t, w, d, pluginID, "https://10.0.0.5/ok"); res["http_status"] != float64(200) {
			t.Fatalf("new host after the move: %v", res)
		}
	})
}
