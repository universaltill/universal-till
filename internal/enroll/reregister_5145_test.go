package enroll

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#5145: the cloud asks a keyed device to prove its approval key
// (409 key_required + challenge) before it re-issues the credential.

type regReq struct {
	StoreName     string `json:"store_name"`
	DeviceID      string `json:"device_id"`
	ExpectStoreID string `json:"expect_store_id"`
	ApprovalKey   *struct {
		PublicKey  string `json:"public_key"`
		Reregister *struct {
			Challenge string `json:"challenge"`
			Sig       string `json:"sig"`
		} `json:"reregister"`
	} `json:"approval_key"`
}

// scriptedCloud answers register by calling script with the 1-based call
// number and the decoded request; it records every request.
type scriptedCloud struct {
	mu   sync.Mutex
	reqs []regReq
	srv  *httptest.Server
}

func newScriptedCloud(t *testing.T, script func(n int, r regReq, w http.ResponseWriter)) *scriptedCloud {
	t.Helper()
	c := &scriptedCloud{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req regReq
		_ = json.Unmarshal(b, &req)
		c.mu.Lock()
		c.reqs = append(c.reqs, req)
		n := len(c.reqs)
		c.mu.Unlock()
		script(n, req, w)
	})
	mux.HandleFunc("/ui/api/signing-key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"algorithm": "ed25519", "public_key_hex": strings.Repeat("ab", 32)}})
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *scriptedCloud) calls() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.reqs) }
func (c *scriptedCloud) req(i int) regReq {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs[i]
}

func writeCode(w http.ResponseWriter, status int, code string, data map[string]any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": map[string]any{"code": code, "message": "x"}})
}

func writeOK(w http.ResponseWriter, token string) {
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"store_id": "store-abc", "merchant_id": "store-abc", "token": token}})
}

// proofVerifies: the request carries a re-register proof for challenge by the
// request's own key, over the shared message.
func proofVerifies(t *testing.T, r regReq, challenge string) bool {
	t.Helper()
	if r.ApprovalKey == nil || r.ApprovalKey.Reregister == nil || r.ApprovalKey.Reregister.Challenge != challenge {
		return false
	}
	pub, err := base64.RawURLEncoding.DecodeString(r.ApprovalKey.PublicKey)
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(r.ApprovalKey.Reregister.Sig)
	if err != nil {
		return false
	}
	msg, err := approvalkey.ReregisterMessage(r.DeviceID, r.ApprovalKey.PublicKey, challenge)
	return err == nil && ed25519.Verify(pub, msg, sig)
}

// Test 20: one signed retry carrying the cloud's challenge; a second refusal
// sets device_register_key_refused, makes no third call, silences the lazy
// path, and a 200 clears it.
func TestRegisterAnswersKeyRequiredWithExactlyOneSignedRetry(t *testing.T) {
	resetState()
	cloud := newScriptedCloud(t, func(n int, r regReq, w http.ResponseWriter) {
		if r.ApprovalKey == nil || r.ApprovalKey.Reregister == nil {
			writeCode(w, 409, "key_required", map[string]any{"challenge": "chal-1"})
			return
		}
		if !proofVerifies(t, r, "chal-1") {
			writeCode(w, 409, "key_required", map[string]any{"challenge": "chal-2"})
			return
		}
		writeOK(w, "tok-new")
	})
	kv := newFakeKV()
	cfg := freshConfig(cloud.srv.URL)
	initForTest(t, cfg, kv)
	if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
		t.Fatalf("RegisterNow: %v", err)
	}
	if cloud.calls() != 2 {
		t.Fatalf("calls = %d, want exactly 2 (plain, then one signed retry)", cloud.calls())
	}
	if kv.get(keyToken) != "tok-new" || !CurrentStatus().Registered {
		t.Fatalf("token not stored: %q", kv.get(keyToken))
	}
}

func TestRegisterSecondKeyRequiredIsRefusedAndQuietsTheLazyPath(t *testing.T) {
	resetState()
	mode := "refuse"
	cloud := newScriptedCloud(t, func(n int, r regReq, w http.ResponseWriter) {
		if mode == "ok" {
			writeOK(w, "tok-new")
			return
		}
		writeCode(w, 409, "key_required", map[string]any{"challenge": fmt.Sprintf("chal-%d", n)})
	})
	kv := newFakeKV()
	cfg := freshConfig(cloud.srv.URL)
	initForTest(t, cfg, kv)
	_, err := RegisterNow(context.Background(), cfg, kv)
	if !IsKeyRefused(err) {
		t.Fatalf("err = %v, want key refused", err)
	}
	if cloud.calls() != 2 {
		t.Fatalf("calls = %d, want 2 and no third", cloud.calls())
	}
	if _, err := time.Parse(time.RFC3339, kv.get(keyRegisterKeyRefused)); err != nil {
		t.Fatalf("device_register_key_refused = %q: %v", kv.get(keyRegisterKeyRefused), err)
	}
	if KeyRefusedAt(context.Background(), kv).IsZero() {
		t.Fatal("KeyRefusedAt zero")
	}
	for i := 0; i < 3; i++ {
		EnsureRegistered(context.Background(), cfg, kv)
	}
	if cloud.calls() != 2 {
		t.Fatalf("lazy path called the cloud %d more times after a key refusal", cloud.calls()-2)
	}
	// A deliberate Register now still tries, and a 200 clears the mark.
	mode = "ok"
	if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
		t.Fatal(err)
	}
	if kv.get(keyRegisterKeyRefused) != "" || !KeyRefusedAt(context.Background(), kv).IsZero() {
		t.Fatalf("mark survived a 200: %q", kv.get(keyRegisterKeyRefused))
	}
}

// A key_required with no (or an empty) challenge is a contract problem: the
// till says "update", signs nothing, retries nothing.
func TestRegisterKeyRequiredWithoutChallengeIsUpdateNeeded(t *testing.T) {
	resetState()
	for name, data := range map[string]map[string]any{"none": nil, "empty": {"challenge": ""}, "newline": {"challenge": "a\nb"},
		"over max length": {"challenge": strings.Repeat("c", maxChallengeLen+1)}} {
		t.Run(name, func(t *testing.T) {
			resetState()
			cloud := newScriptedCloud(t, func(_ int, _ regReq, w http.ResponseWriter) { writeCode(w, 409, "key_required", data) })
			kv := newFakeKV()
			cfg := freshConfig(cloud.srv.URL)
			initForTest(t, cfg, kv)
			_, err := RegisterNow(context.Background(), cfg, kv)
			if !IsUpdateNeeded(err) || cloud.calls() != 1 {
				t.Fatalf("err = %v calls = %d", err, cloud.calls())
			}
		})
	}
}

func registeredTill(t *testing.T, cloudURL string, kv *fakeKV) *config.Config {
	t.Helper()
	ctx := context.Background()
	for k, v := range map[string]string{keyStoreID: "store-abc", keyMerchantID: "store-abc", keyToken: "tok-old", keyDeviceID: "till-dev-1", keyDeviceRegistered: "till-dev-1"} {
		_ = kv.Set(ctx, k, v)
	}
	cfg := freshConfig(cloudURL)
	initForTest(t, cfg, kv)
	if !CurrentStatus().Registered {
		t.Fatal("till not registered")
	}
	return cfg
}

// Test 21.
func TestReconnectSendsExpectStoreIDAndReplacesTheTokenInPlace(t *testing.T) {
	resetState()
	cloud := newScriptedCloud(t, func(_ int, r regReq, w http.ResponseWriter) {
		if r.ApprovalKey == nil || r.ApprovalKey.Reregister == nil {
			writeCode(w, 409, "key_required", map[string]any{"challenge": "chal-1"})
			return
		}
		if !proofVerifies(t, r, "chal-1") {
			writeCode(w, 409, "key_required", map[string]any{"challenge": "chal-2"})
			return
		}
		writeOK(w, "tok-new")
	})
	kv := newFakeKV()
	cfg := registeredTill(t, cloud.srv.URL, kv)
	if _, err := Reconnect(context.Background(), cfg, kv); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	for i := 0; i < cloud.calls(); i++ {
		if got := cloud.req(i).ExpectStoreID; got != "store-abc" {
			t.Fatalf("call %d expect_store_id = %q", i, got)
		}
		if cloud.req(i).DeviceID != "till-dev-1" {
			t.Fatalf("device id changed: %q", cloud.req(i).DeviceID)
		}
	}
	if kv.get(keyToken) != "tok-new" || kv.get(keyDeviceID) != "till-dev-1" || kv.get(keyStoreID) != "store-abc" {
		t.Fatalf("token/device/store = %q/%q/%q", kv.get(keyToken), kv.get(keyDeviceID), kv.get(keyStoreID))
	}
}

func TestReconnectRefusalsLeaveTheTillExactlyAsItWas(t *testing.T) {
	for name, tc := range map[string]struct {
		answer func(w http.ResponseWriter)
		check  func(error) bool
	}{
		"key_required twice": {func(w http.ResponseWriter) { writeCode(w, 409, "key_required", map[string]any{"challenge": "c"}) }, IsKeyRefused},
		"store_not_reusable": {func(w http.ResponseWriter) { writeCode(w, 409, "store_not_reusable", nil) }, IsStoreNotReusable},
		"429":                {func(w http.ResponseWriter) { writeCode(w, 429, "rate_limited", nil) }, IsRateLimited},
		"device_live": {func(w http.ResponseWriter) {
			writeCode(w, 409, "device_live", map[string]any{"usable_from": time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339)})
		}, func(err error) bool { _, ok := IsDeviceLive(err); return ok }},
	} {
		t.Run(name, func(t *testing.T) {
			resetState()
			cloud := newScriptedCloud(t, func(_ int, _ regReq, w http.ResponseWriter) { tc.answer(w) })
			kv := newFakeKV()
			cfg := registeredTill(t, cloud.srv.URL, kv)
			_, err := Reconnect(context.Background(), cfg, kv)
			if err == nil || !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
			if kv.get(keyToken) != "tok-old" || kv.get(keyStoreID) != "store-abc" || kv.get(keyDeviceID) != "till-dev-1" || !CurrentStatus().Registered {
				t.Fatalf("till changed: token=%q store=%q device=%q registered=%v", kv.get(keyToken), kv.get(keyStoreID), kv.get(keyDeviceID), CurrentStatus().Registered)
			}
		})
	}
}

func TestReconnectNeedsARegisteredMainTill(t *testing.T) {
	resetState()
	cloud := newScriptedCloud(t, func(_ int, _ regReq, w http.ResponseWriter) { writeOK(w, "x") })
	kv := newFakeKV()
	cfg := freshConfig(cloud.srv.URL)
	initForTest(t, cfg, kv)
	if _, err := Reconnect(context.Background(), cfg, kv); !errors.Is(err, ErrReconnectUnavailable) {
		t.Fatalf("not registered: %v", err)
	}
	resetState()
	kv2 := newFakeKV()
	cfg2 := registeredTill(t, cloud.srv.URL, kv2)
	_ = kv2.Set(context.Background(), keySyncPrimaryURL, "http://main.local")
	if _, err := Reconnect(context.Background(), cfg2, kv2); !errors.Is(err, ErrReconnectUnavailable) {
		t.Fatalf("replica: %v", err)
	}
	if cloud.calls() != 0 {
		t.Fatalf("calls = %d", cloud.calls())
	}
}

// Test 24: the identity wipe runs first, then a first-time register under the
// new device id; a failed register leaves the ordinary not-registered state.
func TestStartAsNewStoreWipesIdentityThenRegistersFirstTime(t *testing.T) {
	resetState()
	cloud := newScriptedCloud(t, func(_ int, _ regReq, w http.ResponseWriter) { writeOK(w, "tok-fresh") })
	kv := newFakeKV()
	cfg := registeredTill(t, cloud.srv.URL, kv)
	_ = kv.Set(context.Background(), keyRegisterKeyRefused, time.Now().UTC().Format(time.RFC3339))
	if _, err := StartAsNewStore(context.Background(), cfg, kv); err != nil {
		t.Fatalf("StartAsNewStore: %v", err)
	}
	r := cloud.req(0)
	if r.DeviceID == "till-dev-1" || !strings.HasPrefix(r.DeviceID, "till-") || r.ExpectStoreID != "" {
		t.Fatalf("register body: device %q expect %q", r.DeviceID, r.ExpectStoreID)
	}
	if kv.get(keyDeviceID) != r.DeviceID || kv.get(keyToken) != "tok-fresh" || kv.get(keyRegisterKeyRefused) != "" {
		t.Fatalf("state: device %q token %q refused %q", kv.get(keyDeviceID), kv.get(keyToken), kv.get(keyRegisterKeyRefused))
	}
}

func TestStartAsNewStoreFailureLeavesAnOrdinaryUnregisteredTill(t *testing.T) {
	resetState()
	cloud := newScriptedCloud(t, func(_ int, _ regReq, w http.ResponseWriter) { http.Error(w, "boom", 500) })
	kv := newFakeKV()
	cfg := registeredTill(t, cloud.srv.URL, kv)
	if _, err := StartAsNewStore(context.Background(), cfg, kv); err == nil {
		t.Fatal("want the register failure")
	}
	if CurrentStatus().Registered || kv.get(keyToken) != "" || kv.get(keyStoreID) != "" || kv.get(keyDeviceID) == "till-dev-1" || kv.get(keyDeviceID) == "" {
		t.Fatalf("state: registered=%v token=%q store=%q device=%q", CurrentStatus().Registered, kv.get(keyToken), kv.get(keyStoreID), kv.get(keyDeviceID))
	}
	// Ordinary not-registered: the Register now path works afterwards.
	if _, err := RegisterNow(context.Background(), cfg, kv); err == nil || cloud.calls() != 2 {
		t.Fatalf("follow-up register err=%v calls=%d", err, cloud.calls())
	}
}

func TestStartAsNewStoreRefusesAReplica(t *testing.T) {
	resetState()
	cloud := newScriptedCloud(t, func(_ int, _ regReq, w http.ResponseWriter) { writeOK(w, "x") })
	kv := newFakeKV()
	cfg := registeredTill(t, cloud.srv.URL, kv)
	_ = kv.Set(context.Background(), keySyncPrimaryURL, "http://main.local")
	if _, err := StartAsNewStore(context.Background(), cfg, kv); !errors.Is(err, ErrReconnectUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if kv.get(keyToken) != "tok-old" || cloud.calls() != 0 {
		t.Fatal("a refused call changed the till")
	}
}

// Test 25 (L3 + 429).
func TestDeviceLiveUsableFromIsCappedAndPastKeepsTheBackoff(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		usable   time.Time
		min, max time.Duration
	}{
		"far future capped":  {time.Now().Add(100000 * time.Hour), 72 * time.Hour, 73*time.Hour + time.Minute},
		"past keeps 1 h":     {time.Now().Add(-5 * time.Hour), 59 * time.Minute, 61 * time.Minute},
		"within cap as sent": {time.Now().Add(30 * time.Hour), 29 * time.Hour, 31 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			kv := newFakeKV()
			noteRegisterOutcome(ctx, kv, newRegisterHTTPError(409, deviceLiveBody(tc.usable.UTC().Format(time.RFC3339))))
			got, _ := time.Parse(time.RFC3339, kv.get(keyRegisterBlockedUntil))
			if d := time.Until(got); d < tc.min || d > tc.max {
				t.Fatalf("blocked for %v, want %v..%v", d, tc.min, tc.max)
			}
		})
	}
}

func TestRateLimitedSetsNoBlock(t *testing.T) {
	kv := newFakeKV()
	err := newRegisterHTTPError(429, `{"error":{"code":"rate_limited"}}`)
	if !IsRateLimited(err) || IsRateLimited(errors.New("x")) {
		t.Fatal("IsRateLimited")
	}
	noteRegisterOutcome(context.Background(), kv, err)
	if kv.get(keyRegisterBlockedUntil) != "" || kv.get(keyRegisterKeyRefused) != "" {
		t.Fatal("a 429 must not set a block")
	}
}

// ut-docs#5145 (v2 M5): an unnamed shop is reconnected under its blank name,
// never renamed after its own store id.
func TestReconnectDoesNotNameAnUnnamedShopAfterItsID(t *testing.T) {
	resetState()
	cloud := newScriptedCloud(t, func(_ int, r regReq, w http.ResponseWriter) {
		if r.ApprovalKey == nil || r.ApprovalKey.Reregister == nil {
			writeCode(w, 409, "key_required", map[string]any{"challenge": "chal-1"})
			return
		}
		writeOK(w, "tok-new")
	})
	kv := newFakeKV()
	cfg := registeredTill(t, cloud.srv.URL, kv)
	cfg.StoreName = ""
	_ = kv.Set(context.Background(), StoreNameSettingsKey, "")
	if _, err := Reconnect(context.Background(), cfg, kv); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	for i := 0; i < cloud.calls(); i++ {
		if got := cloud.req(i).StoreName; got != "" {
			t.Fatalf("call %d store_name = %q, want blank", i, got)
		}
	}
}
