package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#2769 slice 2, ADR-0116 D3: a replica gets its OWN cloud
// credential without any credential crossing the LAN. The main till's
// devices/register answer may carry a one-time redeem code; the main till
// relays only that code in its vouch answer, and the replica redeems it
// itself, directly with the cloud (POST /v1/stores/devices/redeem,
// unauthenticated — the code is the credential). These tests pin both ends.

const (
	rdmCode    = "c0dec0dec0dec0dec0dec0dec0dec0dec0dec0dec0dec0dec0dec0dec0dec0de"
	rdmToken   = "70ce70ce70ce70ce70ce70ce70ce70ce70ce70ce70ce70ce70ce70ce70ce70ce"
	rdmStore   = "store-abc"
	rdmReplica = "till-replica-own"
)

// --- main-till side: relaying the redeem code ----------------------------

// newRegisterCloud is a fake cloud whose devices/register answers with the
// given raw body (status 200).
func newRegisterCloud(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/devices/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer store-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func registerAnswer(code string) string {
	data := map[string]any{"store_id": rdmStore, "device_id": "till-replica", "device_count": 2}
	if code != "" {
		data["redeem_code"] = code
		data["redeem_expires_at"] = "2026-10-02T10:10:00Z"
	}
	raw, _ := json.Marshal(map[string]any{"data": data, "error": nil})
	return string(raw)
}

func vouchFromCloud(t *testing.T, body string) Vouch {
	t.Helper()
	resetState()
	srv := newRegisterCloud(t, body)
	cfg := mainTillCfg(srv.URL)
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keyDeviceRegistered, mainDeviceID)
	initForTest(t, cfg, kv)
	v, err := VouchForReplica(context.Background(), cfg, ReplicaRequest{TillID: "till-row-2", DeviceID: "till-replica", DeviceName: "Back office", Version: "0.23.0"})
	if err != nil {
		t.Fatalf("vouch: %v", err)
	}
	return v
}

func TestVouchForReplicaRelaysRedeemCode(t *testing.T) {
	v := vouchFromCloud(t, registerAnswer(rdmCode))
	if v.RedeemCode != rdmCode {
		t.Fatalf("relayed redeem code = %q, want the cloud's code", v.RedeemCode)
	}
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"redeem_code":"`+rdmCode+`"`) {
		t.Fatalf("vouch JSON lacks the redeem code: %s", raw)
	}
	if strings.Contains(string(raw), "store-token") {
		t.Fatalf("vouch answer carries the store token: %s", raw)
	}
}

// No code in the cloud's answer (a device that already holds a credential,
// a legacy-token main till, or an old cloud) → nothing relayed, and the
// field is omitted from the wire.
func TestVouchForReplicaWithoutCodeRelaysNone(t *testing.T) {
	for name, body := range map[string]string{
		"no code":  registerAnswer(""),
		"empty":    "",
		"non-JSON": "OK",
	} {
		t.Run(name, func(t *testing.T) {
			v := vouchFromCloud(t, body)
			if v.RedeemCode != "" {
				t.Fatalf("relayed redeem code = %q, want none", v.RedeemCode)
			}
			raw, _ := json.Marshal(v)
			if strings.Contains(string(raw), "redeem_code") {
				t.Fatalf("vouch JSON names a redeem code with none to relay: %s", raw)
			}
		})
	}
}

func TestVouchForReplicaDropsMalformedRedeemCode(t *testing.T) {
	for name, code := range map[string]string{
		"whitespace": "c0dec0dec0dec0de c0dec0dec0dec0de",
		"newline":    "c0dec0dec0dec0de\nc0dec0dec0dec0de",
		"too short":  "c0de",
		"too long":   strings.Repeat("c", 1025),
		// the cloud's /redeem caps code at 128 characters (review nit 5)
		"over /redeem's cap": strings.Repeat("c", 129),
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			v := vouchFromCloud(t, registerAnswer(code))
			if v.RedeemCode != "" {
				t.Fatalf("relayed a malformed redeem code %q", v.RedeemCode)
			}
			if !strings.Contains(logs.String(), "[WARN]") {
				t.Fatalf("a malformed redeem code was dropped silently:\n%s", logs.String())
			}
			if strings.Contains(logs.String(), code) {
				t.Fatalf("the malformed redeem code reached the log:\n%s", logs.String())
			}
		})
	}
}

// The vouch answer may carry a one-time redeem code, never a credential
// (#2730's invariant, ADR-0116 D3): no field of Vouch is token-shaped.
func TestVouchHasNoCredentialField(t *testing.T) {
	typ := reflect.TypeOf(Vouch{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
		for _, bad := range []string{"token", "credential", "secret", "bearer", "password"} {
			if strings.Contains(name, bad) {
				t.Fatalf("Vouch field %s (%s) is credential-shaped", f.Name, f.Tag.Get("json"))
			}
		}
	}
	raw, _ := json.Marshal(Vouch{StoreID: rdmStore, DeviceID: "d", RedeemCode: rdmCode})
	if strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Fatalf("vouch JSON carries a token-ish field: %s", raw)
	}
}

// --- replica side: redeeming the code itself -----------------------------

// fakeRedeemCloud serves POST /api/v1/stores/devices/redeem. status/answer
// configure the reply; every request is captured.
type fakeRedeemCloud struct {
	srv    *httptest.Server
	calls  atomic.Int32
	status int
	answer map[string]any // data, on 200
	errOut string         // error.code, on non-200

	mu       sync.Mutex
	lastBody map[string]string
	lastAuth string
	hadAuth  bool
}

func newFakeRedeemCloud(t *testing.T) *fakeRedeemCloud {
	t.Helper()
	c := &fakeRedeemCloud{status: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/devices/redeem", func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.lastBody = body
		_, c.hadAuth = r.Header["Authorization"]
		c.lastAuth = r.Header.Get("Authorization")
		status, answer, errOut := c.status, c.answer, c.errOut
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]string{"code": errOut, "message": "nope"}})
			return
		}
		if answer == nil {
			answer = map[string]any{"store_id": body["store_id"], "device_id": body["device_id"], "device_token": rdmToken}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": answer, "error": nil})
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakeRedeemCloud) last() (map[string]string, bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastBody, c.hadAuth, c.lastAuth
}

// bootReplica boots a post-#2730 replica (own device id, no token) with
// no background loop (no endpoint in the boot config). storeID is the
// replica's synced marketplace.store_id ("" = not synced yet). envToken,
// when set, pins the token the way UT_MARKETPLACE_MERCHANT_TOKEN does.
func bootReplica(t *testing.T, storeID, envToken string) (*countingKV, *config.Config) {
	t.Helper()
	resetState()
	t.Cleanup(resetState)
	kv := newCountingKV()
	for k, v := range map[string]string{
		"sync.primary_url": "http://127.0.0.1:1",
		"sync.bearer":      "replica-bearer",
		"sync.till_id":     "till-row-2",
		keyDeviceID:        rdmReplica,
		keyDeviceTillID:    "till-row-2",
		keyPublicKey:       pinnedKey,
	} {
		_ = kv.fakeKV.Set(context.Background(), k, v)
	}
	if storeID != "" {
		_ = kv.fakeKV.Set(context.Background(), keyStoreID, storeID)
		_ = kv.fakeKV.Set(context.Background(), keyMerchantID, storeID)
	}
	boot := &config.Config{Marketplace: config.MarketplaceConfig{MerchantToken: envToken}}
	initForTest(t, boot, kv)
	return kv, boot
}

func replicaMarket(endpoint string) config.MarketplaceConfig {
	return config.MarketplaceConfig{EndpointURL: endpoint + "/api"}
}

func vouchWithCode() Vouch {
	return Vouch{StoreID: rdmStore, DeviceID: rdmReplica, RedeemCode: rdmCode}
}

func assertNoSecretsLogged(t *testing.T, logs *syncBuffer) {
	t.Helper()
	out := logs.String()
	for _, s := range []string{rdmCode, rdmToken} {
		if strings.Contains(out, s) {
			t.Fatalf("a secret reached the log:\n%s", out)
		}
	}
}

func TestReplicaRedeemsCodeForOwnCredential(t *testing.T) {
	for name, synced := range map[string]string{"store id synced": rdmStore, "store id not synced yet": ""} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			cloud := newFakeRedeemCloud(t)
			kv, boot := bootReplica(t, synced, "")
			m := replicaMarket(cloud.srv.URL)
			if CurrentStatus().Registered {
				t.Fatal("precondition: replica without a token counts as registered")
			}

			if err := applyVouch(context.Background(), m, kv, vouchWithCode()); err != nil {
				t.Fatalf("applyVouch: %v", err)
			}

			if n := cloud.calls.Load(); n != 1 {
				t.Fatalf("redeem calls = %d, want 1", n)
			}
			body, hadAuth, auth := cloud.last()
			if hadAuth {
				t.Fatalf("redeem sent an Authorization header (%q); the code is the only credential", auth)
			}
			want := map[string]string{"store_id": rdmStore, "device_id": rdmReplica, "code": rdmCode}
			if !reflect.DeepEqual(body, want) {
				t.Fatalf("redeem body = %v, want %v", body, want)
			}
			if got := kv.get(keyToken); got != rdmToken {
				t.Fatalf("persisted %s = %q, want the redeemed credential", keyToken, got)
			}
			if got := kv.get(keyDeviceRegistered); got != rdmReplica {
				t.Fatalf("device_registered = %q, want %q", got, rdmReplica)
			}
			if got := Effective(boot).Marketplace.MerchantToken; got != rdmToken {
				t.Fatalf("effective token = %q, want the redeemed credential without a restart", got)
			}
			if sid, tok := currentStoreAuth(m); sid != rdmStore || tok != rdmToken {
				t.Fatalf("currentStoreAuth = (%q, %q), want (%q, the redeemed credential)", sid, tok, rdmStore)
			}
			st := CurrentStatus()
			if !st.Registered || st.StoreID != rdmStore {
				t.Fatalf("status = %+v, want registered under %s", st, rdmStore)
			}
			if synced == "" {
				// The store id is shop-wide and owned by the admin sync:
				// adopted in memory only.
				if n := kv.setsOf(keyStoreID); n != 0 {
					t.Fatalf("%s written %d times, want 0 (admin sync owns it)", keyStoreID, n)
				}
			}
			assertNoSecretsLogged(t, logs)
		})
	}
}

// End to end through Init: the vouch loop picks the code out of the main
// till's answer and redeems it.
func TestReplicaLoopRedeemsRelayedCode(t *testing.T) {
	logs := captureLog(t)
	resetState()
	t.Cleanup(resetState)
	fastRetries(t)
	cloud := newFakeRedeemCloud(t)
	primary := newFakePrimary(t, 0, 0)
	primary.redeemCode = rdmCode
	kv := newFakeKV()
	for k, v := range map[string]string{
		"sync.primary_url": primary.srv.URL,
		"sync.bearer":      "replica-bearer",
		"sync.till_id":     "till-row-2",
		keyStoreID:         rdmStore,
		keyPublicKey:       pinnedKey,
	} {
		_ = kv.Set(context.Background(), k, v)
	}
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: cloud.srv.URL + "/api"}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	Init(ctx, cfg, kv, &wg)
	waitFor(t, "redeemed credential", func() bool { return kv.get(keyToken) == rdmToken })
	if got := Effective(cfg).Marketplace.MerchantToken; got != rdmToken {
		t.Fatalf("effective token = %q, want the redeemed credential", got)
	}
	assertNoSecretsLogged(t, logs)
}

// The cloud's answer must name this till's own device and the vouched
// store, and carry a well-formed token — anything else is ignored.
func TestReplicaIgnoresRedeemAnswerForOtherDeviceOrStore(t *testing.T) {
	for name, answer := range map[string]map[string]any{
		"other device":    {"store_id": rdmStore, "device_id": "till-other", "device_token": rdmToken},
		"other store":     {"store_id": "store-other", "device_id": rdmReplica, "device_token": rdmToken},
		"malformed token": {"store_id": rdmStore, "device_id": rdmReplica, "device_token": "bad token\n"},
		"no token":        {"store_id": rdmStore, "device_id": rdmReplica},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			cloud := newFakeRedeemCloud(t)
			cloud.answer = answer
			kv, boot := bootReplica(t, rdmStore, "")
			if err := applyVouch(context.Background(), replicaMarket(cloud.srv.URL), kv, vouchWithCode()); err != nil {
				t.Fatalf("applyVouch: %v", err)
			}
			if n := kv.setsOf(keyToken); n != 0 {
				t.Fatalf("%s written %d times, want 0", keyToken, n)
			}
			if got := Effective(boot).Marketplace.MerchantToken; got != "" {
				t.Fatalf("effective token = %q, want none", got)
			}
			if kv.get(keyDeviceRegistered) != rdmReplica {
				t.Fatal("vouch marker not persisted")
			}
			assertNoSecretsLogged(t, logs)
		})
	}
}

// The replica's own synced store id disagrees with the store the main till
// vouched under: never redeem (the code would bind it to another shop).
func TestReplicaSkipsRedeemOnStoreMismatch(t *testing.T) {
	logs := captureLog(t)
	cloud := newFakeRedeemCloud(t)
	kv, _ := bootReplica(t, "store-other", "")
	if err := applyVouch(context.Background(), replicaMarket(cloud.srv.URL), kv, vouchWithCode()); err != nil {
		t.Fatalf("applyVouch: %v", err)
	}
	if n := cloud.calls.Load(); n != 0 {
		t.Fatalf("redeem calls = %d, want 0", n)
	}
	if kv.get(keyToken) != "" {
		t.Fatal("token persisted")
	}
	if !strings.Contains(logs.String(), "[WARN]") {
		t.Fatalf("store mismatch skipped silently:\n%s", logs.String())
	}
	assertNoSecretsLogged(t, logs)
}

func TestReplicaSkipsRedeemWithoutStoreOrEndpoint(t *testing.T) {
	cloud := newFakeRedeemCloud(t)
	kv, _ := bootReplica(t, rdmStore, "")
	v := vouchWithCode()
	v.StoreID = ""
	if err := applyVouch(context.Background(), replicaMarket(cloud.srv.URL), kv, v); err != nil {
		t.Fatalf("applyVouch (no store): %v", err)
	}
	if err := applyVouch(context.Background(), config.MarketplaceConfig{}, kv, vouchWithCode()); err != nil {
		t.Fatalf("applyVouch (no endpoint): %v", err)
	}
	if n := cloud.calls.Load(); n != 0 {
		t.Fatalf("redeem calls = %d, want 0", n)
	}
}

// UT_MARKETPLACE_MERCHANT_TOKEN pins the bearer: the replica never redeems
// (the credential could not be used), so the code stays unused.
func TestReplicaSkipsRedeemWhenEnvPinsToken(t *testing.T) {
	logs := captureLog(t)
	cloud := newFakeRedeemCloud(t)
	kv, boot := bootReplica(t, rdmStore, "env-pinned-token-0001")
	for i := 0; i < 2; i++ {
		if err := applyVouch(context.Background(), replicaMarket(cloud.srv.URL), kv, vouchWithCode()); err != nil {
			t.Fatalf("applyVouch: %v", err)
		}
	}
	if n := cloud.calls.Load(); n != 0 {
		t.Fatalf("redeem calls = %d, want 0 while the env pins the token", n)
	}
	if got := Effective(boot).Marketplace.MerchantToken; got != "env-pinned-token-0001" {
		t.Fatalf("effective token = %q, want the env-pinned one", got)
	}
	if n := strings.Count(logs.String(), "UT_MARKETPLACE_MERCHANT_TOKEN"); n != 1 {
		t.Fatalf("want exactly one env-pin warning, got %d:\n%s", n, logs.String())
	}
	assertNoSecretsLogged(t, logs)
}

// A refused redeem (raced by a LAN eavesdropper, an expired code, a
// rate limit, a cloud failure) keeps the current credential and never fails
// the vouch itself.
func TestReplicaRedeemRefusedKeepsCredentialAndVouch(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{http.StatusConflict, "already_redeemed"},
		{http.StatusForbidden, "invalid_code"},
		{http.StatusConflict, "device_exists"},
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusInternalServerError, "redeem_failed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			logs := captureLog(t)
			cloud := newFakeRedeemCloud(t)
			cloud.status, cloud.errOut = tc.status, tc.code
			kv, boot := bootReplica(t, rdmStore, "")
			if err := applyVouch(context.Background(), replicaMarket(cloud.srv.URL), kv, vouchWithCode()); err != nil {
				t.Fatalf("applyVouch = %v, want nil (a redeem failure never fails the vouch)", err)
			}
			if n := cloud.calls.Load(); n != 1 {
				t.Fatalf("redeem calls = %d, want 1", n)
			}
			if kv.get(keyDeviceRegistered) != rdmReplica {
				t.Fatal("vouch marker not persisted")
			}
			if n := kv.setsOf(keyToken); n != 0 {
				t.Fatalf("%s written %d times, want 0", keyToken, n)
			}
			if got := Effective(boot).Marketplace.MerchantToken; got != "" {
				t.Fatalf("effective token = %q, want none", got)
			}
			if !strings.Contains(logs.String(), tc.code) {
				t.Fatalf("the cloud's error code %q is not in the log:\n%s", tc.code, logs.String())
			}
			assertNoSecretsLogged(t, logs)
		})
	}
}

// Network failure: same — keep the credential, vouch still recorded.
func TestReplicaRedeemNetworkErrorKeepsVouch(t *testing.T) {
	logs := captureLog(t)
	kv, _ := bootReplica(t, rdmStore, "")
	if err := applyVouch(context.Background(), replicaMarket("http://127.0.0.1:1"), kv, vouchWithCode()); err != nil {
		t.Fatalf("applyVouch: %v", err)
	}
	if kv.get(keyDeviceRegistered) != rdmReplica || kv.get(keyToken) != "" {
		t.Fatalf("registered=%q token=%q", kv.get(keyDeviceRegistered), kv.get(keyToken))
	}
	assertNoSecretsLogged(t, logs)
}

// Review fixes (ut-docs#2769 slice 2).

// stubVouch is a VouchSource that always answers v.
type stubVouch struct{ v Vouch }

func (s stubVouch) RequestVouch(context.Context, string, string) (Vouch, error) { return s.v, nil }

// A transient redeem failure (network, 429, 5xx) must not park the replica
// for the 6-hour vouch interval: the 10-minute code expires unused, so the
// next vouch (which brings a fresh code) comes on the short backoff. A
// refusal (403/409) is final for this code and keeps the long interval.
func TestReplicaAttemptBacksOffAfterTransientRedeemFailure(t *testing.T) {
	for _, tc := range []struct {
		status   int
		code     string
		wantWait time.Duration
		wantOK   bool
	}{
		{http.StatusInternalServerError, "redeem_failed", replicaRetryDelays[0], false},
		{http.StatusTooManyRequests, "rate_limited", replicaRetryDelays[0], false},
		{http.StatusForbidden, "invalid_code", vouchInterval, true},
		{http.StatusConflict, "already_redeemed", vouchInterval, true},
		{http.StatusOK, "", vouchInterval, true},
	} {
		t.Run(fmt.Sprint(tc.status, tc.code), func(t *testing.T) {
			cloud := newFakeRedeemCloud(t)
			cloud.status, cloud.errOut = tc.status, tc.code
			kv, _ := bootReplica(t, rdmStore, "")
			wait, ok := replicaAttempt(context.Background(), replicaMarket(cloud.srv.URL), kv, stubVouch{vouchWithCode()}, 0)
			if wait != tc.wantWait || ok != tc.wantOK {
				t.Fatalf("replicaAttempt = (%v, %v), want (%v, %v)", wait, ok, tc.wantWait, tc.wantOK)
			}
			if kv.get(keyDeviceRegistered) != rdmReplica {
				t.Fatal("vouch marker not persisted")
			}
		})
	}
}

// The store this replica already uses in memory (set by an earlier
// redemption before the admin sync delivered marketplace.store_id) also
// guards against a code for another shop's store (review minor 2).
func TestReplicaSkipsRedeemOnInMemoryStoreMismatch(t *testing.T) {
	cloud := newFakeRedeemCloud(t)
	kv, _ := bootReplica(t, "", "")
	mu.Lock()
	cur.StoreID = "store-other-shop"
	mu.Unlock()
	if err := applyVouch(context.Background(), replicaMarket(cloud.srv.URL), kv, vouchWithCode()); err != nil {
		t.Fatalf("applyVouch: %v", err)
	}
	if n := cloud.calls.Load(); n != 0 {
		t.Fatalf("redeem calls = %d, want 0 for another store", n)
	}
}

// A vouch answer landing after POST /api/sync/promote (no longer a
// replica) does not redeem, like the #2753 via-main state (review nit 6).
func TestPromotedTillDoesNotRedeemLateVouch(t *testing.T) {
	cloud := newFakeRedeemCloud(t)
	kv, _ := bootReplica(t, rdmStore, "")
	_ = kv.Set(context.Background(), "sync.primary_url", "")
	if err := applyVouch(context.Background(), replicaMarket(cloud.srv.URL), kv, vouchWithCode()); err != nil {
		t.Fatalf("applyVouch: %v", err)
	}
	if n := cloud.calls.Load(); n != 0 {
		t.Fatalf("redeem calls = %d, want 0 after promotion", n)
	}
}
