package plugins

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// demoMode switches the process-wide netaccess demo switch on for one test.
func demoMode(t *testing.T) {
	t.Helper()
	prev := netaccess.Demo()
	netaccess.SetDemo(true)
	t.Cleanup(func() { netaccess.SetDemo(prev) })
}

// ADR-0113 §1.6 (ut-docs#2795): on the demo till a plugin's http_request
// answers "permission denied" even with the net: grant that would otherwise
// let it through, and nothing reaches the target.
func TestHostHTTPRequestDeniedInDemoMode(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.httpdemo"

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "net:127.0.0.1")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true

	demoMode(t)
	res := runGuest(t, w, d, pluginID, srv.URL+"/ping")
	if res["http_code"] != float64(hostErrDenied) {
		t.Fatalf("http_code = %v (status %v), want %d (permission denied)", res["http_code"], res["http_status"], hostErrDenied)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server saw %d request(s) from a plugin on the demo till, want 0", n)
	}
}

// The same for tcp_open: a granted tcp:<host>:<port> is still denied on the
// demo till, and the device never sees a connection.
func TestHostTCPOpenDeniedInDemoMode(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpdemo"

	host, port, accepts, _ := startTCPFixture(t, nil)
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, fmt.Sprintf("tcp:%s:%d", host, port))
	w := newTCPTestRuntime(t, guest, pluginID)

	demoMode(t)
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "openonly", "host": host, "port": port,
		"connect_timeout_ms": 1000,
	})
	if res["open_code"] != float64(hostErrDenied) {
		t.Fatalf("open_code = %v, want %d (permission denied)", res["open_code"], hostErrDenied)
	}
	if n := accepts.Load(); n != 0 {
		t.Fatalf("device saw %d connection(s) from a plugin on the demo till, want 0", n)
	}
}

// ADR-0113 §1.7: only bundled plugins run on the demo till, and they are
// still Ed25519-verified before they load — demo mode never skips or relaxes
// verification. With demo mode on, the real verifier still accepts the
// genuinely signed fixture and still rejects a tampered copy of it and the
// right manifest under the wrong key.
func TestManifestSignatureStillVerifiedInDemoMode(t *testing.T) {
	demoMode(t)

	verifier, err := NewManifestVerifier(marketplaceTestPublicKeyHex)
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	res, err := verifier.VerifyManifest("testdata/marketplace_signed_manifest.json")
	if err != nil || !res.SignatureVerified {
		t.Fatalf("genuine signed manifest did not verify in demo mode: err=%v verified=%v", err, res != nil && res.SignatureVerified)
	}

	raw, err := os.ReadFile("testdata/marketplace_signed_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["name"] = fmt.Sprint(m["name"]) + " (tampered)"
	tampered, _ := json.Marshal(m)
	tamperedPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := verifier.VerifyManifest(tamperedPath); err == nil && res.SignatureVerified {
		t.Fatal("tampered manifest verified in demo mode — verification was skipped or relaxed")
	}

	wrong, err := NewManifestVerifier("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := wrong.VerifyManifest("testdata/marketplace_signed_manifest.json"); err == nil && res.SignatureVerified {
		t.Fatal("manifest verified under the wrong key in demo mode")
	}
}
