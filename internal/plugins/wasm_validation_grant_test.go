package plugins

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// ut-docs#3226 (ADR-0132 card 4/5, ADR-0121 amendment): net:validation:<host>.
// A PAdES-B-LTA seal needs OCSP/CRL/TSA data, served almost always over plain
// http:// and occasionally larger than http_request's 256 KiB cap. The
// validation grant relaxes exactly those two things — scheme and cap — for
// the one host the manifest names, and nothing else: no wildcard form, no
// non-public address (it is NOT an exact grant at dial time), and an ordinary
// net:<host> / net:* grant keeps today's https-only, 256 KiB behaviour.

func TestParseValidationPermission(t *testing.T) {
	cases := []struct {
		perm     string
		wantHost string
		wantIs   bool
		wantErr  bool
	}{
		{perm: "net:*"},
		{perm: "net:crl.example.com"},
		{perm: "net:@setting:endpoint_url"},
		{perm: "storage"},
		{perm: "net:validation:crl.example.com", wantHost: "crl.example.com", wantIs: true},
		{perm: "net:validation:CRL.Example.COM.", wantHost: "crl.example.com", wantIs: true},
		{perm: "net:validation:93.184.216.40", wantHost: "93.184.216.40", wantIs: true},
		{perm: "net:validation:[2001:db8::1]", wantHost: "2001:db8::1", wantIs: true},
		{perm: "net:validation:", wantIs: true, wantErr: true},
		{perm: "net:validation:*", wantIs: true, wantErr: true},
		{perm: "net:validation:*.example.com", wantIs: true, wantErr: true},
		{perm: "net:validation: crl.example.com", wantIs: true, wantErr: true},
		{perm: "net:validation:crl.example.com:80", wantIs: true, wantErr: true},
		{perm: "net:validation:http://crl.example.com", wantIs: true, wantErr: true},
		{perm: "net:validation:crl.example.com/path", wantIs: true, wantErr: true},
		{perm: "net:validation:@setting:url", wantIs: true, wantErr: true},
		{perm: "net:validation:a b", wantIs: true, wantErr: true},
		{perm: "net:validation:[2001:db8::1", wantIs: true, wantErr: true},
		{perm: "net:validation:2001:db8::1]", wantIs: true, wantErr: true},
		{perm: "net:validation:[crl.example.com", wantIs: true, wantErr: true},
		{perm: "net:validation:[]", wantIs: true, wantErr: true},
		{perm: "net:validation:.", wantIs: true, wantErr: true},
		{perm: "net:validation:crl\u00a0example.com", wantIs: true, wantErr: true},
	}
	for _, c := range cases {
		host, is, err := ParseValidationPermission(c.perm)
		if is != c.wantIs || (err != nil) != c.wantErr {
			t.Errorf("%q: is=%v err=%v, want is=%v err=%v", c.perm, is, err, c.wantIs, c.wantErr)
			continue
		}
		if !c.wantErr && host != c.wantHost {
			t.Errorf("%q: host=%q, want %q", c.perm, host, c.wantHost)
		}
	}
}

func TestParseManifestValidationPermissions(t *testing.T) {
	const head = `{"id":"com.test.validation","name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","permissions":`
	ok := []string{
		`["net:validation:crl.example.com"]`,
		`["net:validation:crl.example.com","net:validation:ocsp.example.com","net:tsa.example.com"]`,
	}
	for _, p := range ok {
		if _, err := ParseManifest(strings.NewReader(head + p + `}`)); err != nil {
			t.Errorf("%s: unexpected error %v", p, err)
		}
	}
	bad := []string{
		`["net:validation:"]`,
		`["net:validation:*"]`,
		`["net:validation:*.example.com"]`,
		`["net:validation:crl.example.com:80"]`,
	}
	for _, p := range bad {
		if _, err := ParseManifest(strings.NewReader(head + p + `}`)); err == nil {
			t.Errorf("%s: manifest accepted, want refusal", p)
		}
	}
}

// validationGrantMatch is exact-host only and is not satisfied by — nor does
// it satisfy — the ordinary net: forms.
func TestValidationGrantMatch(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		perms     []string
		host      string
		wantValid bool
		wantExact bool // netGrantMatch's answer: the validation grant must never widen it
	}{
		{name: "declared host", perms: []string{"net:validation:crl.example.com"}, host: "crl.example.com", wantValid: true},
		{name: "normalised (case, trailing dot)", perms: []string{"net:validation:CRL.example.com."}, host: "crl.EXAMPLE.com", wantValid: true},
		{name: "another host", perms: []string{"net:validation:crl.example.com"}, host: "ocsp.example.com"},
		{name: "not a suffix wildcard", perms: []string{"net:validation:example.com"}, host: "crl.example.com"},
		{name: "plain net:<host> is not a validation grant", perms: []string{"net:crl.example.com"}, host: "crl.example.com", wantExact: true},
		{name: "net:* is not a validation grant", perms: []string{"net:*"}, host: "crl.example.com"},
		{name: "net:@setting is not a validation grant", perms: []string{"net:@setting:endpoint_url"}, host: "crl.example.com", wantExact: true},
		{name: "malformed validation:* matches nothing", perms: []string{"net:validation:*"}, host: "crl.example.com"},
		{name: "malformed empty host matches nothing", perms: []string{"net:validation:"}, host: ""},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			pluginID := fmt.Sprintf("com.test.valgrant%d", i)
			seedPlugin(t, d, pluginID)
			setPluginSetting(t, d, pluginID, "endpoint_url", "https://crl.example.com/x")
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
			}
			got, err := validationGrantMatch(ctx, d, pluginID, c.host)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.wantValid {
				t.Errorf("validationGrantMatch = %v, want %v", got, c.wantValid)
			}
			exact, _, err := netGrantMatch(ctx, d, pluginID, c.host)
			if err != nil {
				t.Fatal(err)
			}
			if exact != c.wantExact {
				t.Errorf("netGrantMatch exact = %v, want %v", exact, c.wantExact)
			}
		})
	}
	// A revoked validation grant grants nothing.
	d := hostfnTestDB(t)
	seedPlugin(t, d, "com.test.valrevoked")
	grantPerm(t, d, "com.test.valrevoked", "net:validation:crl.example.com")
	revokePerm(t, d, "com.test.valrevoked", "net:validation:crl.example.com")
	if got, _ := validationGrantMatch(ctx, d, "com.test.valrevoked", "crl.example.com"); got {
		t.Error("revoked validation grant still matches")
	}
}

func TestHTTPValidationGrant(t *testing.T) {
	guest := buildHostfnGuest(t)
	cert, pool := egressTestCert(t)

	var plainHits, tlsHits, tillHits atomic.Int32
	plainMux := http.NewServeMux()
	plainMux.Handle("/ok", hitCounter(&plainHits, "crl"))
	plainMux.HandleFunc("/to-self", func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	plainMux.HandleFunc("/to-other-http", func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		http.Redirect(w, r, "http://other.example.com/ok", http.StatusFound)
	})
	plainMux.HandleFunc("/to-ocsp-http", func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		http.Redirect(w, r, "http://ocsp.example.com/ok", http.StatusFound)
	})
	plainMux.HandleFunc("/to-lan", func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		http.Redirect(w, r, "http://10.0.0.5/ok", http.StatusFound)
	})
	plainMux.HandleFunc("/to-metadata", func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	plain := httptest.NewServer(plainMux)
	defer plain.Close()
	plainAddr := plain.Listener.Addr().String()

	tlsMux := http.NewServeMux()
	tlsMux.Handle("/ok", hitCounter(&tlsHits, "ok"))
	tlsMux.HandleFunc("/to-crl-http", func(w http.ResponseWriter, r *http.Request) {
		tlsHits.Add(1)
		http.Redirect(w, r, "http://crl.example.com/ok", http.StatusFound)
	})
	tlsSrv := httptest.NewUnstartedServer(tlsMux)
	tlsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	tlsAddr := tlsSrv.Listener.Addr().String()

	till := httptest.NewServer(hitCounter(&tillHits, "till"))
	defer till.Close()
	tillPort, _ := strconv.Atoi(mustPort(t, till.URL))

	env := &egressEnv{
		dns: map[string][]string{
			"crl.example.com":      {"93.184.216.40"},
			"ocsp.example.com":     {"93.184.216.41"},
			"other.example.com":    {"93.184.216.42"},
			"api.example.com":      {"93.184.216.34"},
			"meta.example.com":     {"169.254.169.254"},
			"lan.example.com":      {"10.0.0.5"},
			"loop.example.com":     {"127.0.0.1"},
			"sub.crl.example.com":  {"93.184.216.43"},
			"crl-till.example.com": {"127.0.0.1"},
		},
		// Every address lands on a real test server, so a wrongly-allowed
		// dial shows up as a hit. Port 80 → plain; 443 → TLS (by IP).
		route: map[string]string{
			"93.184.216.40": plainAddr, "93.184.216.41": plainAddr, "93.184.216.42": plainAddr,
			"93.184.216.43": plainAddr, "93.184.216.34": tlsAddr,
			"169.254.169.254": plainAddr, "10.0.0.5": plainAddr,
		},
		tillPort: tillPort,
	}
	const v = "net:validation:crl.example.com"
	tillURL := "http://crl-till.example.com:" + strconv.Itoa(tillPort) + "/x"

	type tc struct {
		name       string
		perms      []string
		url        string
		wantStatus int
		wantCode   int32
		noHit      *atomic.Int32
	}
	cases := []tc{
		// Scheme: the relaxation, and the no-widening regression guards.
		{name: "plain http to a validation-granted host", perms: []string{v}, url: "http://crl.example.com/ok", wantStatus: 200},
		{name: "plain http with an ordinary net:<host> grant still refused", perms: []string{"net:crl.example.com"}, url: "http://crl.example.com/ok", wantCode: hostErrInvalid, noHit: &plainHits},
		{name: "plain http under net:* still refused", perms: []string{"net:*"}, url: "http://crl.example.com/ok", wantCode: hostErrInvalid, noHit: &plainHits},
		{name: "plain http to an ordinary host while another is validation-granted", perms: []string{v, "net:ocsp.example.com"}, url: "http://ocsp.example.com/ok", wantCode: hostErrInvalid, noHit: &plainHits},
		{name: "https to a validation-granted host", perms: []string{"net:validation:api.example.com"}, url: "https://api.example.com/ok", wantStatus: 200},
		// Host: exact only.
		{name: "validation grant does not cover a different host (https)", perms: []string{v}, url: "https://api.example.com/ok", wantCode: hostErrDenied, noHit: &tlsHits},
		{name: "validation grant does not cover a different host (http)", perms: []string{v}, url: "http://other.example.com/ok", wantCode: hostErrInvalid, noHit: &plainHits},
		{name: "validation grant is not a suffix wildcard", perms: []string{"net:validation:example.com"}, url: "http://sub.crl.example.com/ok", wantCode: hostErrInvalid, noHit: &plainHits},
		{name: "no permission at all", perms: nil, url: "http://crl.example.com/ok", wantCode: hostErrInvalid, noHit: &plainHits},
		// Dial-time policy unchanged: public addresses only.
		{name: "validation host resolving to the metadata address", perms: []string{"net:validation:meta.example.com"}, url: "http://meta.example.com/latest", wantCode: hostErrDenied, noHit: &plainHits},
		{name: "validation grant on the metadata literal", perms: []string{"net:validation:169.254.169.254"}, url: "http://169.254.169.254/latest", wantCode: hostErrDenied, noHit: &plainHits},
		{name: "validation host resolving to a LAN address", perms: []string{"net:validation:lan.example.com"}, url: "http://lan.example.com/ok", wantCode: hostErrDenied, noHit: &plainHits},
		{name: "validation grant on a LAN literal", perms: []string{"net:validation:10.0.0.5"}, url: "http://10.0.0.5/ok", wantCode: hostErrDenied, noHit: &plainHits},
		{name: "validation host resolving to loopback", perms: []string{"net:validation:loop.example.com"}, url: "http://loop.example.com:" + mustPort(t, plain.URL) + "/ok", wantCode: hostErrDenied, noHit: &plainHits},
		// ut-docs#3226 review F1: an ordinary exact grant on the SAME host
		// must not make the validation request exact — that would be plain
		// http to the LAN, ADR-0121's unbuilt http:lan (#3156).
		{name: "validation and ordinary exact grant on the same LAN host: plain http refused", perms: []string{"net:validation:lan.example.com", "net:lan.example.com"}, url: "http://lan.example.com/ok", wantCode: hostErrDenied, noHit: &plainHits},
		{name: "validation and ordinary exact grant on the same LAN literal: plain http refused", perms: []string{"net:validation:10.0.0.5", "net:10.0.0.5"}, url: "http://10.0.0.5/ok", wantCode: hostErrDenied, noHit: &plainHits},
		{name: "till's own port refused even with validation and exact grants", perms: []string{"net:validation:crl-till.example.com", "net:crl-till.example.com"}, url: tillURL, wantCode: hostErrDenied, noHit: &tillHits},
		// Redirects re-checked per hop.
		{name: "redirect within the validation host", perms: []string{v}, url: "http://crl.example.com/to-self", wantStatus: 200},
		{name: "redirect to another validation-granted host over http", perms: []string{v, "net:validation:ocsp.example.com"}, url: "http://crl.example.com/to-ocsp-http", wantStatus: 200},
		{name: "redirect to an ungranted host refused", perms: []string{v}, url: "http://crl.example.com/to-other-http", wantCode: hostErrDenied},
		{name: "redirect over http to a host with only an ordinary grant refused", perms: []string{v, "net:ocsp.example.com"}, url: "http://crl.example.com/to-ocsp-http", wantCode: hostErrDenied},
		{name: "redirect over http to a host under only net:* refused", perms: []string{v, "net:*"}, url: "http://crl.example.com/to-other-http", wantCode: hostErrDenied},
		{name: "redirect to a LAN literal refused", perms: []string{v, "net:*"}, url: "http://crl.example.com/to-lan", wantCode: hostErrDenied},
		{name: "redirect to the metadata address refused", perms: []string{v, "net:*", "net:validation:169.254.169.254"}, url: "http://crl.example.com/to-metadata", wantCode: hostErrDenied},
		{name: "https host redirecting to plain http on a validation host", perms: []string{"net:api.example.com", v}, url: "https://api.example.com/to-crl-http", wantStatus: 200},
		{name: "https host redirecting to plain http without a validation grant refused", perms: []string{"net:api.example.com", "net:crl.example.com"}, url: "https://api.example.com/to-crl-http", wantCode: hostErrDenied},
	}
	// One runtime, one compiled guest for every case (compiling per case
	// dominated this test's time under -race); each case gets a fresh
	// database, so grants never leak between cases.
	const pluginID = "com.test.valegress"
	w := NewWasmRuntime(t.TempDir())
	w.httpClient = env.client(pool)
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
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

// readHTTPResponseBody picks the cap by the host that finally answered and
// truncates at it. Tested here, below the guest: an over-8-MiB round trip
// through a real wasm guest (~11 MiB of base64 JSON) cannot finish inside
// the 10s event deadline under -race. TestHTTPValidationResponseCap keeps
// the end-to-end proof with 1 MiB bodies.
func TestReadHTTPResponseBodyCap(t *testing.T) {
	grants := &egressGrants{exact: map[string]bool{}}
	grants.addValidation("crl.example.com")
	answeredBy := func(host string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "http://"+host+"/x", nil)
	}
	cases := []struct {
		name    string
		req     *http.Request
		size    int
		wantLen int
	}{
		{name: "validation host, 9 MiB → 8 MiB", req: answeredBy("crl.example.com"), size: 9 << 20, wantLen: validationResponseCap},
		{name: "validation host, one byte over the cap", req: answeredBy("crl.example.com"), size: validationResponseCap + 1, wantLen: validationResponseCap},
		{name: "validation host, exactly the cap", req: answeredBy("crl.example.com"), size: validationResponseCap, wantLen: validationResponseCap},
		{name: "validation host normalised (case, trailing dot)", req: answeredBy("CRL.Example.com."), size: 1 << 20, wantLen: 1 << 20},
		{name: "ordinary host, 9 MiB → 256 KiB", req: answeredBy("api.example.com"), size: 9 << 20, wantLen: httpResponseCap},
		{name: "redirected from the validation host to an ordinary one → 256 KiB", req: answeredBy("cdn.example.com"), size: 1 << 20, wantLen: httpResponseCap},
		{name: "no request recorded → 256 KiB", req: nil, size: 1 << 20, wantLen: httpResponseCap},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{Body: io.NopCloser(bytes.NewReader(make([]byte, c.size))), Request: c.req}
			got, err := readHTTPResponseBody(resp, grants)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != c.wantLen {
				t.Fatalf("read %d bytes, want %d", len(got), c.wantLen)
			}
		})
	}
	if validationResponseCap != 8<<20 {
		t.Fatalf("validationResponseCap = %d, want 8 MiB", validationResponseCap)
	}
}

// The response cap end to end, through a real guest: 256 KiB for every
// ordinary grant (pinned here — no test held it before) and a body over the
// cap truncated at it; a validation-granted host's body read whole past
// 256 KiB. The 8 MiB truncation itself is TestReadHTTPResponseBodyCap's.
func TestHTTPValidationResponseCap(t *testing.T) {
	guest := buildHostfnGuest(t)

	const oneMiB = 1 << 20
	sized := func(n int) http.HandlerFunc {
		body := bytes.Repeat([]byte{'c'}, n)
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/pkix-crl")
			_, _ = w.Write(body)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/small", sized(100<<10))
	mux.Handle("/1mib", sized(oneMiB))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	env := &egressEnv{
		dns:   map[string][]string{"crl.example.com": {"93.184.216.40"}},
		route: map[string]string{"93.184.216.40": addr},
	}
	// The ordinary-grant baseline goes over plain http to loopback (the one
	// plain-http case an ordinary grant allows) so it hits the same server.
	loopURL := "http://localhost:" + mustPort(t, srv.URL)
	env.dns["localhost"] = []string{"127.0.0.1"}

	cases := []struct {
		name    string
		perms   []string
		url     string
		wantLen float64
	}{
		{name: "ordinary grant: under the 256 KiB cap", perms: []string{"net:localhost"}, url: loopURL + "/small", wantLen: 100 << 10},
		{name: "ordinary grant: 1 MiB body capped at 256 KiB", perms: []string{"net:localhost"}, url: loopURL + "/1mib", wantLen: httpResponseCap},
		{name: "ordinary grant alongside a validation grant for another host: still 256 KiB", perms: []string{"net:localhost", "net:validation:crl.example.com"}, url: loopURL + "/1mib", wantLen: httpResponseCap},
		{name: "validation grant: 1 MiB body read whole", perms: []string{"net:validation:crl.example.com"}, url: "http://crl.example.com/1mib", wantLen: oneMiB},
	}
	// One runtime, one compiled guest for every case (compiling per case
	// dominated this test's time under -race); each case gets a fresh
	// database, so grants never leak between cases.
	const pluginID = "com.test.valcap"
	w := NewWasmRuntime(t.TempDir())
	w.httpClient = env.client(nil)
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
			}
			res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "http_len", "url": c.url})
			if res["http_status"] != float64(200) {
				t.Fatalf("http_status = %v (code %v), want 200", res["http_status"], res["http_code"])
			}
			if res["body_len"] != c.wantLen {
				t.Fatalf("body_len = %v, want %v", res["body_len"], c.wantLen)
			}
		})
	}
}

// ut-docs#3226 review F2: marketplace install and import verify the manifest
// through VerifyManifest, not ParseManifest — the validation-grant check must
// run there too.
func TestVerifyManifest_RefusesMalformedValidationGrant(t *testing.T) {
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	base := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"wasm","entrypoint":"./plugin.wasm","canonical_type":"integration","device_arch":"any","permissions":`
	write := func(perms string) string {
		p := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(p, []byte(base+perms+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, bad := range []string{`["net:validation:*"]`, `["net:validation:"]`, `["net:validation:[2001:db8::1"]`, `["net:validation:crl.example.com:80"]`} {
		if _, err := mv.VerifyManifest(write(bad)); err == nil || !strings.Contains(err.Error(), "net:validation:") {
			t.Errorf("%s: VerifyManifest should refuse, got %v", bad, err)
		}
	}
	if _, err := mv.VerifyManifest(write(`["net:validation:crl.example.com"]`)); err != nil {
		t.Errorf("valid validation grant refused: %v", err)
	}
}
