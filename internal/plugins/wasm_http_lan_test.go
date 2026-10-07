package plugins

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0121 §2/§3 (ut-docs#3156): http:lan and the always-refused cloud
// metadata addresses.
//
//   - 169.254.169.254 and fd00:ec2::254 are refused at dial time whatever the
//     grant — an exact net:/tcp: grant, http:lan, both — in every spelling
//     (IPv4-mapped, NAT64, 6to4).
//   - http:lan lets a plugin use plain http to a non-loopback host it holds
//     an EXACT grant for (net:<host>, net:@setting:<key>) or that is the host
//     of one of its own `type: "endpoint"` settings — and a request admitted
//     as plain http only because of http:lan must land on a non-public IP.

// withInstalledManifest points paths at a fresh data root and writes
// manifest.json for pluginID's active version (1.0.0, as seedPlugin
// records it), the file InstalledManifest reads.
func withInstalledManifest(t *testing.T, pluginID, manifestJSON string) {
	t.Helper()
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	dir := filepath.Join(root, "plugins", pluginID, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifestJSON), 0o600); err != nil {
		t.Fatal(err)
	}
}

func endpointManifest(pluginID string) string {
	return `{"id":"` + pluginID + `","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm",
		"settings":[{"key":"erp_url","type":"endpoint"},{"key":"note"}]}`
}

func TestIsCloudMetadataIP(t *testing.T) {
	yes := []string{
		"169.254.169.254",
		"::ffff:169.254.169.254", // IPv4-mapped
		"64:ff9b::a9fe:a9fe",     // NAT64 of 169.254.169.254
		"2002:a9fe:a9fe::1",      // 6to4 of 169.254.169.254
		"fd00:ec2::254",
		"FD00:EC2:0:0:0:0:0:254",
	}
	no := []string{"169.254.169.253", "169.254.1.1", "10.0.0.1", "8.8.8.8", "fd00:ec2::253", "::1", "127.0.0.1"}
	for _, s := range yes {
		if !isCloudMetadataIP(net.ParseIP(s)) {
			t.Errorf("isCloudMetadataIP(%s) = false, want true", s)
		}
	}
	for _, s := range no {
		if isCloudMetadataIP(net.ParseIP(s)) {
			t.Errorf("isCloudMetadataIP(%s) = true, want false", s)
		}
	}
}

// The dialer refuses a metadata address even when the caller holds the
// exact grant — the one thing that unlocks every other non-public address.
func TestEgressDialerRefusesCloudMetadataEvenWhenExact(t *testing.T) {
	d := (&egressEnv{}).dialer()
	for _, s := range []string{"169.254.169.254", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::", "fd00:ec2::254"} {
		err := d.check("h", net.ParseIP(s), 80, egressPolicy{exact: true})
		if !errors.Is(err, errEgressDenied) {
			t.Errorf("check(%s, exact) = %v, want egress denied", s, err)
		}
	}
	// Ordinary link-local stays reachable with the exact grant.
	if err := d.check("h", net.ParseIP("169.254.10.10"), 80, egressPolicy{exact: true}); err != nil {
		t.Errorf("exact link-local refused: %v", err)
	}
}

// A host admitted as plain http only because of http:lan is LAN-only.
func TestEgressDialerLANOnly(t *testing.T) {
	d := (&egressEnv{}).dialer()
	if err := d.check("h", net.ParseIP("93.184.216.34"), 80, egressPolicy{exact: true, lanOnly: true}); !errors.Is(err, errEgressDenied) {
		t.Errorf("lanOnly public IP = %v, want denied", err)
	}
	if err := d.check("h", net.ParseIP("192.168.1.20"), 80, egressPolicy{exact: true, lanOnly: true}); err != nil {
		t.Errorf("lanOnly LAN IP refused: %v", err)
	}
	if err := d.check("h", net.ParseIP("93.184.216.34"), 443, egressPolicy{}); err != nil {
		t.Errorf("ordinary public IP refused: %v", err)
	}
}

func TestEndpointSettingHostMatch(t *testing.T) {
	ctx := context.Background()
	d := hostfnTestDB(t)
	const pluginID = "com.test.endpointmatch"
	seedPlugin(t, d, pluginID)
	withInstalledManifest(t, pluginID, endpointManifest(pluginID))
	setPluginSetting(t, d, pluginID, "erp_url", "http://ERP.example.lan:8080/api")
	setPluginSetting(t, d, pluginID, "note", "http://other.example.lan/")

	cases := []struct {
		host string
		want bool
	}{
		{"erp.example.lan", true},
		{"ERP.example.lan.", true},
		{"other.example.lan", false}, // declared, but not type endpoint
		{"api.example.com", false},
	}
	for _, c := range cases {
		got, err := endpointSettingHostMatch(ctx, d, pluginID, c.host)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("endpointSettingHostMatch(%s) = %v, want %v", c.host, got, c.want)
		}
	}
	// An endpoint value that is not a valid endpoint URL grants nothing.
	setPluginSetting(t, d, pluginID, "erp_url", "http://user:pw@erp.example.lan/")
	if got, _ := endpointSettingHostMatch(ctx, d, pluginID, "erp.example.lan"); got {
		t.Error("invalid endpoint URL still matched")
	}
	// No manifest on disk → no endpoint settings, no error.
	if got, err := endpointSettingHostMatch(ctx, d, "com.test.nomanifest", "erp.example.lan"); got || err != nil {
		t.Errorf("no manifest: got %v, %v", got, err)
	}
}

func TestHTTPLANEgress(t *testing.T) {
	guest := buildHostfnGuest(t)
	cert, pool := egressTestCert(t)

	var lanHits, pubHits, metaHits atomic.Int32
	lanMux := http.NewServeMux()
	lanMux.Handle("/ok", hitCounter(&lanHits, "lan"))
	lanMux.HandleFunc("/to-public-http", func(w http.ResponseWriter, r *http.Request) {
		lanHits.Add(1)
		http.Redirect(w, r, "http://public.example.com/ok", http.StatusFound)
	})
	lanMux.HandleFunc("/to-lan2-http", func(w http.ResponseWriter, r *http.Request) {
		lanHits.Add(1)
		http.Redirect(w, r, "http://lan2.example.com/ok", http.StatusFound)
	})
	lan := httptest.NewServer(lanMux)
	defer lan.Close()
	pub := httptest.NewServer(hitCounter(&pubHits, "public"))
	defer pub.Close()
	meta := httptest.NewServer(hitCounter(&metaHits, "meta"))
	defer meta.Close()
	tlsSrv := httptest.NewUnstartedServer(hitCounter(&pubHits, "public-tls"))
	tlsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	tlsSrv.StartTLS()
	defer tlsSrv.Close()

	env := &egressEnv{
		dns: map[string][]string{
			"lan.example.com":    {"192.168.1.20"},
			"lan2.example.com":   {"192.168.1.21"},
			"erp.example.com":    {"192.168.1.22"},
			"public.example.com": {"93.184.216.34"},
			"puberp.example.com": {"93.184.216.35"},
			"meta.example.com":   {"169.254.169.254"},
		},
		route: map[string]string{
			"192.168.1.20": lan.Listener.Addr().String(), "192.168.1.21": lan.Listener.Addr().String(),
			"192.168.1.22":    lan.Listener.Addr().String(),
			"93.184.216.34":   pub.Listener.Addr().String(),
			"93.184.216.35":   tlsSrv.Listener.Addr().String(),
			"169.254.169.254": meta.Listener.Addr().String(),
			"fd00:ec2::254":   meta.Listener.Addr().String(),
		},
	}
	// The hostfn guest's own events; one compiled guest for every case.
	const pluginID = "com.test.httplan"
	withInstalledManifest(t, pluginID, endpointManifest(pluginID))
	w := NewWasmRuntime(t.TempDir())
	w.httpClient = env.client(pool)
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	type tc struct {
		name       string
		perms      []string
		erpURL     string
		url        string
		wantStatus int
		wantCode   int32
		noHit      *atomic.Int32
	}
	cases := []tc{
		{name: "plain http to a LAN host with net:<host> + http:lan", perms: []string{"net:lan.example.com", "http:lan"}, url: "http://lan.example.com/ok", wantStatus: 200},
		{name: "plain http to a LAN literal with net:<ip> + http:lan", perms: []string{"net:192.168.1.20", "http:lan"}, url: "http://192.168.1.20/ok", wantStatus: 200},
		{name: "plain http to a LAN host without http:lan refused (scheme)", perms: []string{"net:lan.example.com"}, url: "http://lan.example.com/ok", wantCode: hostErrInvalid, noHit: &lanHits},
		{name: "http:lan alone does not grant a host", perms: []string{"http:lan"}, url: "http://lan.example.com/ok", wantCode: hostErrInvalid, noHit: &lanHits},
		{name: "http:lan with only net:* is not an exact grant", perms: []string{"net:*", "http:lan"}, url: "http://lan.example.com/ok", wantCode: hostErrInvalid, noHit: &lanHits},
		{name: "plain http under http:lan to a name resolving to a public IP refused at dial", perms: []string{"net:public.example.com", "http:lan"}, url: "http://public.example.com/ok", wantCode: hostErrDenied, noHit: &pubHits},
		{name: "endpoint-setting host + http:lan reaches the LAN", perms: []string{"http:lan"}, erpURL: "http://erp.example.com/", url: "http://erp.example.com/ok", wantStatus: 200},
		{name: "endpoint-setting host without http:lan not granted (http)", perms: nil, erpURL: "http://erp.example.com/", url: "http://erp.example.com/ok", wantCode: hostErrInvalid, noHit: &lanHits},
		{name: "endpoint-setting host without http:lan not granted (https)", perms: []string{"storage"}, erpURL: "https://puberp.example.com/", url: "https://puberp.example.com/ok", wantCode: hostErrDenied, noHit: &pubHits},
		{name: "endpoint-setting host admitted only by http:lan is LAN-only (https to a public IP)", perms: []string{"http:lan"}, erpURL: "https://puberp.example.com/", url: "https://puberp.example.com/ok", wantCode: hostErrDenied, noHit: &pubHits},
		{name: "net:* still reaches a public endpoint-setting host when http:lan is also held", perms: []string{"net:*", "http:lan"}, erpURL: "https://puberp.example.com/", url: "https://puberp.example.com/ok", wantStatus: 200},
		{name: "a non-endpoint setting is not a grant", perms: []string{"http:lan"}, erpURL: "", url: "http://erp.example.com/ok", wantCode: hostErrInvalid, noHit: &lanHits},
		{name: "metadata literal refused with net:<ip> + http:lan", perms: []string{"net:169.254.169.254", "http:lan"}, url: "http://169.254.169.254/latest/meta-data/", wantCode: hostErrDenied, noHit: &metaHits},
		{name: "name resolving to metadata refused with net:<host> + http:lan", perms: []string{"net:meta.example.com", "http:lan"}, url: "http://meta.example.com/latest", wantCode: hostErrDenied, noHit: &metaHits},
		{name: "IPv4-mapped metadata literal refused with exact grant + http:lan", perms: []string{"net:[::ffff:169.254.169.254]", "http:lan"}, url: "http://[::ffff:169.254.169.254]/latest", wantCode: hostErrDenied, noHit: &metaHits},
		{name: "AWS IPv6 metadata refused with exact grant + http:lan", perms: []string{"net:[fd00:ec2::254]", "http:lan"}, url: "http://[fd00:ec2::254]/latest", wantCode: hostErrDenied, noHit: &metaHits},
		{name: "redirect from a LAN http:lan host to a public plain-http URL refused", perms: []string{"net:lan.example.com", "net:public.example.com", "http:lan"}, url: "http://lan.example.com/to-public-http", wantCode: hostErrDenied, noHit: &pubHits},
		{name: "redirect from a LAN http:lan host to another granted LAN host", perms: []string{"net:lan.example.com", "net:lan2.example.com", "http:lan"}, url: "http://lan.example.com/to-lan2-http", wantStatus: 200},
		{name: "redirect to a LAN host without its exact grant refused", perms: []string{"net:lan.example.com", "net:*", "http:lan"}, url: "http://lan.example.com/to-lan2-http", wantCode: hostErrDenied},
		// net:validation: keeps its own semantics: public-only, even with
		// http:lan and an exact grant on the same host.
		{name: "validation + exact + http:lan on a LAN host: still public-only", perms: []string{"net:validation:lan.example.com", "net:lan.example.com", "http:lan"}, url: "http://lan.example.com/ok", wantCode: hostErrDenied, noHit: &lanHits},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			for _, p := range c.perms {
				if p != "storage" {
					grantPerm(t, d, pluginID, p)
				}
			}
			if c.erpURL != "" {
				setPluginSetting(t, d, pluginID, "erp_url", c.erpURL)
			}
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
			if res["http_code"] != float64(c.wantCode) {
				t.Fatalf("http_code = %v (status %v), want %d", res["http_code"], res["http_status"], c.wantCode)
			}
			if c.noHit != nil && c.noHit.Load() != before {
				t.Fatalf("refused request still reached the target server")
			}
		})
	}
}

// Cloud metadata via tcp_open: refused even with the exact tcp grant.
func TestHostTCPRefusesCloudMetadataEvenWithExactGrant(t *testing.T) {
	guest := buildTCPGuest(t)
	fxHost, fxPort, accepts := echoFixture(t)
	fxAddr := net.JoinHostPort(fxHost, strconv.Itoa(fxPort))
	env := &egressEnv{
		dns:   map[string][]string{"meta.example.com": {"169.254.169.254"}},
		route: map[string]string{"169.254.169.254": fxAddr, "::ffff:169.254.169.254": fxAddr},
	}
	withTCPEgress(t, env)
	for i, host := range []string{"169.254.169.254", "::ffff:169.254.169.254", "meta.example.com"} {
		t.Run(host, func(t *testing.T) {
			d := hostfnTestDB(t)
			pluginID := fmt.Sprintf("com.test.tcpmeta%d", i)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			grantPerm(t, d, pluginID, "tcp:"+net.JoinHostPort(host, strconv.Itoa(fxPort)))
			w := newTCPTestRuntime(t, guest, pluginID)
			before := accepts()
			res := runTCPGuest(t, w, d, pluginID, map[string]any{
				"mode": "openonly", "host": host, "port": fxPort, "connect_timeout_ms": 2000,
			})
			if res["open_code"] != float64(hostErrDenied) {
				t.Fatalf("open_code = %v, want %d", res["open_code"], hostErrDenied)
			}
			if accepts() != before {
				t.Fatal("metadata dial reached the fixture")
			}
		})
	}
}

// TestAuditDeclaredDenialOnlyForDeclaredPermission: a plain-http refusal for
// a plugin that never declared http:lan writes no audit row (a poll loop
// with a mistyped http:// URL must not flood audit_log); one that declared
// it but is not granted is audited (review finding, ut-docs#3156).
func TestAuditDeclaredDenialOnlyForDeclaredPermission(t *testing.T) {
	ctx := context.Background()
	d := hostfnTestDB(t)
	const pluginID = "com.test.auditlan"
	seedPlugin(t, d, pluginID)
	count := func() int {
		t.Helper()
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'permission_denied' AND entity_id = ? AND data_json LIKE '%http:lan%'`, pluginID).Scan(&n); err != nil {
			t.Fatalf("count audit rows: %v", err)
		}
		return n
	}
	s := &hostState{pluginID: pluginID, db: d}
	auditDeclaredDenial(ctx, s, permHTTPLAN)
	if n := count(); n != 0 {
		t.Fatalf("undeclared http:lan wrote %d audit rows, want 0", n)
	}
	if err := data.NewPluginRepo(d).InsertPluginPermissions(ctx, nil, pluginID, []string{permHTTPLAN}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	auditDeclaredDenial(ctx, s, permHTTPLAN)
	if n := count(); n != 1 {
		t.Fatalf("declared-but-ungranted http:lan wrote %d audit rows, want 1", n)
	}
}
