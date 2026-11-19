//go:build bundledplugins

package plugins_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
	"github.com/universaltill/universal-till/internal/secrets"
)

// ut-docs#6627: the real bundled Stripe and SumUp wasm, installed through
// bundled.Sync (App Store channel) with the declared net:<host> permission
// granted at install, reach their provider host when authorizing, and stop
// reaching it once the shop revokes that permission. The provider is a local
// TLS listener verified against a test root (no InsecureSkipVerify); only DNS
// and the final dial are stubbed, so the production permission gate and the
// egressDialer checks run. The key is an obviously fake test value.

// lockedBuf is a concurrency-safe log sink for logging.CaptureForTest.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type seenRequest struct {
	method, path, auth string
}

// fakeProvider is a TLS server whose certificate covers host, plus a record
// of every request it received.
type fakeProvider struct {
	addr  string
	roots *x509.CertPool
	mu    sync.Mutex
	seen  []seenRequest
}

// route answers one request the way the provider would: status and JSON body.
type route func(r *http.Request) (int, string)

func newFakeProvider(t *testing.T, host string, answer route) *fakeProvider {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake-provider"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{roots: x509.NewCertPool()}
	f.roots.AddCert(leaf)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, seenRequest{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		f.mu.Unlock()
		status, body := answer(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	f.addr = srv.Listener.Addr().String()
	return f
}

func (f *fakeProvider) requests() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.seen...)
}

func TestBundledProviders_AuthorizeReachesProviderHostOnlyWhileNetGranted(t *testing.T) {
	cases := []struct {
		name, id, host, event string
		apiKey                string            // the configured fake key; must arrive as the Bearer token
		settings              map[string]string // key → plain value
		secretKeys            []string
		registerKeys          []string // settings the manifest scopes per till
		answer                route
		pathPrefix            string
		wantMethod            string
	}{
		{
			name: "stripe", apiKey: "sk_test_fake", id: "com.universaltill.payment-stripe", host: "api.stripe.com",
			event:      "payment.stripe.authorize",
			settings:   map[string]string{"stripe_secret_key": "sk_test_fake"},
			secretKeys: []string{"stripe_secret_key"},
			answer: func(*http.Request) (int, string) {
				return http.StatusOK, `{"id":"pi_fake_1","status":"succeeded"}`
			},
			pathPrefix: "/v1/payment_intents", wantMethod: http.MethodPost,
		},
		{
			name: "sumup", apiKey: "sup_sk_fake", id: "com.universaltill.payment-sumup", host: "api.sumup.com",
			event: "payment.sumup.authorize",
			settings: map[string]string{
				"sumup_api_key": "sup_sk_fake", "sumup_merchant_code": "MFAKE0001",
				"sumup_reader_id": "rdr_fake_1", "currency": "GBP",
				"sumup_affiliate_key": "sup_aff_fake", "sumup_app_id": "com.example.fake",
			},
			secretKeys: []string{"sumup_api_key"}, registerKeys: []string{"sumup_reader_id"},
			// The reader checkout starts with a POST, then the plugin polls the
			// Transactions list for the client_transaction_id until a terminal status.
			answer: func(r *http.Request) (int, string) {
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/checkout"):
					return http.StatusOK, `{"data":{"client_transaction_id":"txn_fake_1"}}`
				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2.1/merchants/MFAKE0001/transactions"):
					return http.StatusOK, `[{"id":"txn_fake_1","status":"SUCCESSFUL"}]`
				}
				return http.StatusOK, `{}`
			},
			pathPrefix: "/v0.1/merchants/MFAKE0001/readers/", wantMethod: http.MethodPost,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !bundled.Available() {
				t.Fatal("bundledplugins build without the embedded set")
			}
			t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore))
			dir := t.TempDir()
			paths.Init(dir)
			prevDir := paths.DataDir()
			prev := secrets.Default()
			secrets.SetDefault(secrets.NewKeyStoreAt(dir+"/secrets/plugin_settings_key.bin", nil))
			t.Cleanup(func() { secrets.SetDefault(prev); paths.Init(prevDir) })
			d, err := db.Open(dir + "/unitill-pos.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			ctx := context.Background()

			if changed, err := bundled.Sync(ctx, d.DB); err != nil || !changed {
				t.Fatalf("Sync = %v, %v", changed, err)
			}
			repo := data.NewPluginRepo(d.DB)
			perm := "net:" + c.host
			if ok, exists, err := repo.CheckPermission(ctx, c.id, perm); err != nil || !exists || !ok {
				t.Fatalf("%s not granted at install: granted=%v exists=%v err=%v", perm, ok, exists, err)
			}
			secret := map[string]bool{}
			for _, k := range c.secretKeys {
				secret[k] = true
			}
			scopeOf := map[string]string{}
			for _, k := range c.registerKeys {
				scopeOf[k] = "register"
			}
			for k, v := range c.settings {
				raw, _ := json.Marshal(v)
				scope := "global"
				if s, ok := scopeOf[k]; ok {
					scope = s
				}
				if err := repo.UpsertPluginSettingScoped(ctx, c.id, k, string(raw), scope, secret[k]); err != nil {
					t.Fatalf("set %s: %v", k, err)
				}
			}
			if err := repo.SetPluginActive(ctx, nil, c.id, true); err != nil {
				t.Fatal(err)
			}

			fake := newFakeProvider(t, c.host, c.answer)
			const publicIP = "93.184.216.34" // a public address (TestIsPublicIP): the documentation nets are refused as non-public
			bundled.Register()
			t.Cleanup(func() { plugins.SetBundledCodeVerifier(nil) })
			rt := plugins.NewWasmRuntime(paths.Plugins())
			plugins.SetTestEgressForTest(rt,
				map[string][]string{c.host: {publicIP}},
				map[string]string{publicIP: fake.addr}, fake.roots)
			t.Cleanup(func() { rt.Close(ctx) })
			rt.Sync(ctx, d.DB)

			authorize := func() (json.RawMessage, error) {
				payload, _ := json.Marshal(map[string]any{"amount": 1000, "currency": "GBP"})
				cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()
				return rt.HandleEvent(cctx, c.id, plugins.Event{ID: "ev-egress", Type: c.event, Timestamp: time.Now(), Payload: payload})
			}

			// (a) Granted: the authorize call reaches the provider host.
			res, err := authorize()
			if err != nil {
				t.Fatalf("authorize with %s granted: %v", perm, err)
			}
			var out struct {
				Outcome string `json:"outcome"`
			}
			if jerr := json.Unmarshal(res, &out); jerr != nil || out.Outcome != "approved" {
				t.Fatalf("authorize with %s granted = %s (%v), want outcome approved", perm, res, jerr)
			}
			got := fake.requests()
			if len(got) == 0 {
				t.Fatalf("%s authorize never reached %s with %s granted", c.id, c.host, perm)
			}
			first := got[0]
			if first.method != c.wantMethod || !strings.HasPrefix(first.path, c.pathPrefix) {
				t.Errorf("first request = %s %s, want %s %s...", first.method, first.path, c.wantMethod, c.pathPrefix)
			}
			// The configured key, and only it, is what reaches the provider.
			for _, r := range got {
				if r.auth != "Bearer "+c.apiKey {
					t.Errorf("%s %s Authorization = %q, want %q", r.method, r.path, r.auth, "Bearer "+c.apiKey)
				}
			}

			// (b) Revoked: the same call no longer reaches the host. The plugin
			// stays loaded and runs (its decline exit code, not "module not
			// loaded"), and the runtime logs the egress denial for this host.
			if err := repo.SetPermission(ctx, c.id, perm, false); err != nil {
				t.Fatal(err)
			}
			rt.Sync(ctx, d.DB)
			before := len(fake.requests())
			var logs lockedBuf
			restore := logging.CaptureForTest(&logs)
			res, err = authorize()
			restore()
			if after := len(fake.requests()); after != before {
				t.Fatalf("%s reached %s %d time(s) after %s was revoked", c.id, c.host, after-before, perm)
			}
			if err == nil {
				t.Fatalf("authorize with %s revoked succeeded (%s), want a decline", perm, res)
			}
			if strings.Contains(err.Error(), "module not loaded") {
				t.Fatalf("plugin unloaded after revoke, not an egress failure: %v", err)
			}
			if !strings.Contains(err.Error(), "exit_code(2)") {
				t.Errorf("revoked authorize error = %v, want the plugin's decline exit (exit_code(2))", err)
			}
			log := logs.String()
			if want := "plugin egress denied: host " + c.host; !strings.Contains(log, want) {
				t.Errorf("revoked authorize log lacks %q:\n%s", want, log)
			}
			if c.name == "stripe" && !strings.Contains(log, "DECLINED 1000 gbp (network_error)") {
				t.Errorf("stripe revoked decline is not network_error:\n%s", log)
			}
		})
	}
}
