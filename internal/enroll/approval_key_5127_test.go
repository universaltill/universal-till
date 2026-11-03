package enroll

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#5127 (design §2, UT-1a): pair and register bodies carry
// approval_key {kid, public_key, pop}, so the cloud pins the key in the
// transaction that mints the token.

type approvalKeyWire struct {
	KID       string `json:"kid"`
	PublicKey string `json:"public_key"`
	Pop       string `json:"pop"`
}

func requireValidApprovalKey(t *testing.T, rawBody []byte, wantDevice string) {
	t.Helper()
	var body struct {
		DeviceID    string           `json:"device_id"`
		ApprovalKey *approvalKeyWire `json:"approval_key"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("body: %v (%s)", err, rawBody)
	}
	k := body.ApprovalKey
	if k == nil {
		t.Fatalf("no approval_key in %s", rawBody)
	}
	if body.DeviceID != wantDevice {
		t.Fatalf("device_id = %q, want %q", body.DeviceID, wantDevice)
	}
	pub, err := base64.RawURLEncoding.DecodeString(k.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public_key %q: %v", k.PublicKey, err)
	}
	if k.KID != approvalkey.KID(pub) {
		t.Fatalf("kid %q does not match the public key", k.KID)
	}
	sig, err := base64.RawURLEncoding.DecodeString(k.Pop)
	if err != nil {
		t.Fatal(err)
	}
	// Before the cloud assigns a store the till has none to name: "".
	if !ed25519.Verify(pub, approvalkey.PopMessage(body.DeviceID, "", k.PublicKey), sig) {
		t.Fatal("the proof of possession does not verify")
	}
}

func TestPostPairCarriesApprovalKey(t *testing.T) {
	var mu sync.Mutex
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		raw = b
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "device_token": pairToken}})
	}))
	defer srv.Close()
	if _, _, _, err := postPair(context.Background(), srv.URL+"/api", pairCode, "till-new", "", "dev"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	requireValidApprovalKey(t, raw, "till-new")
}

func TestPairDoesNotBlockOnAnUnusableApprovalKey(t *testing.T) {
	p := paths.Data("secrets", "approval-ed25519.key")
	prev, prevErr := os.ReadFile(p)
	if err := os.MkdirAll(paths.Data("secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if prevErr == nil {
			_ = os.WriteFile(p, prev, 0o600)
		} else {
			_ = os.Remove(p)
		}
	})
	cloud := newFakePairCloud(t)
	if _, _, _, err := postPair(context.Background(), cloud.srv.URL+"/api", pairCode, "till-new", "", "dev"); err != nil {
		t.Fatalf("pairing must go on without a key: %v", err)
	}
	body, _ := cloud.last()
	if _, ok := body["approval_key"]; ok {
		t.Fatal("approval_key sent from an unusable key file")
	}
	if got, _ := os.ReadFile(p); string(got) != "short" {
		t.Fatal("unusable key file was overwritten")
	}
}

func TestRegisterCarriesApprovalKey(t *testing.T) {
	resetState()
	t.Cleanup(resetState)
	var mu sync.Mutex
	var raw []byte
	srv, _ := testMarketplace(t, 0)
	// Wrap: capture the register body, then forward.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/v1/stores/register") {
			mu.Lock()
			raw = b
			mu.Unlock()
		}
		req, _ := http.NewRequest(r.Method, srv.URL+r.URL.Path, strings.NewReader(string(b)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	kv := newFakeKV()
	cfg := freshConfig(proxy.URL)
	initForTest(t, cfg, kv)
	EnsureRegistered(context.Background(), cfg, kv)
	mu.Lock()
	defer mu.Unlock()
	if raw == nil {
		t.Fatal("register was not called")
	}
	requireValidApprovalKey(t, raw, kv.get(keyDeviceID))
}
