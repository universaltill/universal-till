package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#3523, ADR-0116 D5/D6: Settings → Cloud → "Pair with a shop". The
// owner's my. mints a short pairing code; the till clears exactly its own
// cloud identity, mints a fresh device id and posts the code to
// POST /v1/stores/pair (unauthenticated — the code is the credential). The
// cloud answers with store_id, merchant_id and this device's own token. A
// replica refuses an answer for any store but its main till's. These tests
// pin both the wire call and the identity handling.

const (
	pairCode     = "K7QX-M2RP"
	pairToken    = "9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b9a1b"
	pairStore    = "store-paired"
	pairMerchant = "merchant-paired"
	pairOldDev   = "till-old-device"
	pairOldToken = "0ld70ce0ld70ce0ld70ce0ld70ce0ld70ce0ld70ce0ld70ce0ld70ce0ld70ce"
	pairOldStore = "store-old"
)

// fakePairCloud serves POST /api/v1/stores/pair. Set status/answer/raw to
// shape the reply; every request is captured.
type fakePairCloud struct {
	srv   *httptest.Server
	calls atomic.Int32

	mu       sync.Mutex
	status   int
	answer   map[string]any // data, on 200 (nil → a valid answer)
	raw      string         // when set, written verbatim instead
	errOut   string         // error.code, on non-200
	lastBody map[string]string
	hadAuth  bool
}

func newFakePairCloud(t *testing.T) *fakePairCloud {
	t.Helper()
	c := &fakePairCloud{status: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/pair", func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.lastBody = body
		_, c.hadAuth = r.Header["Authorization"]
		status, answer, raw, errOut := c.status, c.answer, c.raw, c.errOut
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch {
		case raw != "":
			_, _ = w.Write([]byte(raw))
		case status != http.StatusOK:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]string{"code": errOut, "message": "nope"}})
		default:
			if answer == nil {
				answer = map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "token": pairToken}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": answer, "error": nil})
		}
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakePairCloud) set(f func(c *fakePairCloud)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
}

func (c *fakePairCloud) last() (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastBody, c.hadAuth
}

// orderedKV records every write in order, so "cleared before the new value
// landed" is provable.
type orderedKV struct {
	*fakeKV
	mu     sync.Mutex
	writes [][2]string
}

func newOrderedKV() *orderedKV { return &orderedKV{fakeKV: newFakeKV()} }

func (o *orderedKV) Set(ctx context.Context, key, value string) error {
	o.mu.Lock()
	o.writes = append(o.writes, [2]string{key, value})
	o.mu.Unlock()
	return o.fakeKV.Set(ctx, key, value)
}

// writesOf is every value written to key, in order.
func (o *orderedKV) writesOf(key string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, w := range o.writes {
		if w[0] == key {
			out = append(out, w[1])
		}
	}
	return out
}

// bootForPair boots a till holding an old, broken cloud identity (no
// background loop: no endpoint at boot), then points cfg at the cloud.
// replicaStore != "" makes it a replica whose synced store id is that
// value.
func bootForPair(t *testing.T, endpoint string, replica bool, syncedStore string) (*orderedKV, *config.Config) {
	t.Helper()
	resetState()
	t.Cleanup(resetState)
	kv := newOrderedKV()
	seed := map[string]string{
		keyDeviceID:         pairOldDev,
		keyDeviceRegistered: pairOldDev,
		keyToken:            pairOldToken,
		keyEnrolledAt:       "2026-01-01T00:00:00Z",
		keyPublicKey:        pinnedKey,
		"till.name":         "Front counter",
	}
	if syncedStore != "" {
		seed[keyStoreID] = syncedStore
		seed[keyMerchantID] = syncedStore
	}
	if replica {
		seed[keySyncPrimaryURL] = "http://127.0.0.1:1"
		seed[keySyncTillID] = "till-row-2"
		seed[keyDeviceTillID] = "till-row-2"
		seed["sync.till_name"] = "Back office"
	}
	for k, v := range seed {
		_ = kv.fakeKV.Set(context.Background(), k, v)
	}
	cfg := &config.Config{}
	initForTest(t, cfg, kv)
	cfg.Marketplace.EndpointURL = endpoint + "/api"
	return kv, cfg
}

func assertPairSecretsNotLogged(t *testing.T, logs *syncBuffer) {
	t.Helper()
	out := logs.String()
	for _, s := range []string{pairCode, pairToken, pairOldToken} {
		if strings.Contains(out, s) {
			t.Fatalf("a secret reached the log:\n%s", out)
		}
	}
}

// --- postPair -------------------------------------------------------------

func TestPostPairSuccess(t *testing.T) {
	cloud := newFakePairCloud(t)
	store, merchant, token, err := postPair(context.Background(), cloud.srv.URL+"/api/", pairCode, "till-new", "Front counter", "0.30.0")
	if err != nil {
		t.Fatalf("postPair: %v", err)
	}
	if store != pairStore || merchant != pairMerchant || token != pairToken {
		t.Fatalf("postPair = (%q, %q, %q), want the cloud's answer", store, merchant, token)
	}
	body, hadAuth := cloud.last()
	if hadAuth {
		t.Fatal("pair sent an Authorization header; the code is the only credential")
	}
	want := map[string]string{"code": pairCode, "device_id": "till-new", "device_name": "Front counter", "version": "0.30.0"}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("pair body[%s] = %q, want %q (body %v)", k, body[k], v, body)
		}
	}
	if len(body) != len(want) {
		t.Fatalf("pair body = %v, want exactly %v", body, want)
	}
}

func TestPostPairRejectsBadAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		answer map[string]any
		raw    string
	}{
		"malformed JSON":    {raw: `{"data":`},
		"missing store_id":  {answer: map[string]any{"merchant_id": pairMerchant, "token": pairToken}},
		"missing merchant":  {answer: map[string]any{"store_id": pairStore, "token": pairToken}},
		"malformed token":   {answer: map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "token": "short"}},
		"token with spaces": {answer: map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "token": "9a1b9a1b 9a1b9a1b9a1b9a1b"}},
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newFakePairCloud(t)
			cloud.set(func(c *fakePairCloud) { c.answer, c.raw = tc.answer, tc.raw })
			_, _, token, err := postPair(context.Background(), cloud.srv.URL+"/api", pairCode, "till-new", "", "dev")
			if !errors.Is(err, errPairAnswerRejected) {
				t.Fatalf("postPair err = %v, want errPairAnswerRejected", err)
			}
			if token != "" {
				t.Fatalf("postPair returned a token %q with an error", token)
			}
			if strings.Contains(err.Error(), pairCode) || strings.Contains(err.Error(), pairToken) {
				t.Fatalf("error carries a secret: %v", err)
			}
		})
	}
}

func TestPostPairNon200WithErrorCode(t *testing.T) {
	cloud := newFakePairCloud(t)
	cloud.set(func(c *fakePairCloud) { c.status, c.errOut = http.StatusForbidden, "code_invalid" })
	_, _, _, err := postPair(context.Background(), cloud.srv.URL+"/api", pairCode, "till-new", "", "dev")
	var re *redeemError
	if !errors.As(err, &re) || re.status != http.StatusForbidden || re.code != "code_invalid" {
		t.Fatalf("postPair err = %#v, want redeemError{403, code_invalid}", err)
	}
	if strings.Contains(err.Error(), pairCode) {
		t.Fatalf("error carries the code: %v", err)
	}
}

func TestPostPairNon200WithGarbageBody(t *testing.T) {
	for name, raw := range map[string]string{
		"not JSON":       "<html>bad gateway</html>",
		"unbounded code": `{"error":{"code":"Robert'); DROP TABLE--"}}`,
		"code over 64":   `{"error":{"code":"` + strings.Repeat("a", 65) + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newFakePairCloud(t)
			cloud.set(func(c *fakePairCloud) { c.status, c.raw = http.StatusBadGateway, raw })
			_, _, _, err := postPair(context.Background(), cloud.srv.URL+"/api", pairCode, "till-new", "", "dev")
			var re *redeemError
			if !errors.As(err, &re) || re.status != http.StatusBadGateway || re.code != "(no error code)" {
				t.Fatalf("postPair err = %#v, want redeemError{502, (no error code)}", err)
			}
		})
	}
}

// --- Pair -----------------------------------------------------------------

func TestPairMainTillAdoptsPairedIdentity(t *testing.T) {
	logs := captureLog(t)
	cloud := newFakePairCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)

	st, err := Pair(context.Background(), cfg, kv, "  "+pairCode+"\n")
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}

	body, _ := cloud.last()
	newID := body["device_id"]
	if !strings.HasPrefix(newID, "till-") || newID == pairOldDev {
		t.Fatalf("paired device_id = %q, want a fresh till-<uuid>", newID)
	}
	if body["code"] != pairCode {
		t.Fatalf("sent code = %q, want the trimmed code", body["code"])
	}
	if body["device_name"] != "Front counter" {
		t.Fatalf("sent device_name = %q, want the till name", body["device_name"])
	}
	for key, want := range map[string]string{
		keyStoreID:          pairStore,
		keyMerchantID:       pairMerchant,
		keyToken:            pairToken,
		keyDeviceID:         newID,
		keyDeviceRegistered: newID,
		keyPublicKey:        pinnedKey, // ADR-0116 D5: the pinned signing key is kept
	} {
		if got := kv.get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if kv.get(keyEnrolledAt) == "" || kv.get(keyEnrolledAt) == "2026-01-01T00:00:00Z" {
		t.Fatalf("enrolled_at = %q, want a fresh timestamp", kv.get(keyEnrolledAt))
	}
	// The old identity was cleared before the new one landed.
	for key, first := range map[string]string{keyToken: "", keyDeviceRegistered: "", keyEnrolledAt: "", keyStoreID: "", keyMerchantID: ""} {
		w := kv.writesOf(key)
		if len(w) < 2 || w[0] != first {
			t.Fatalf("%s writes = %q, want a clear first, then the paired value", key, w)
		}
	}
	if !st.Registered || st.StoreID != pairStore || st.DeviceID != newID {
		t.Fatalf("status = %+v, want registered as %s / %s", st, pairStore, newID)
	}
	// Used from the very next request, without a restart.
	m := Effective(cfg).Marketplace
	if m.MerchantToken != pairToken || m.DeviceID != newID || m.ClientID != pairMerchant || m.StoreID != pairStore {
		t.Fatalf("effective marketplace = %+v, want the paired identity", m)
	}
	assertPairSecretsNotLogged(t, logs)
}

// A refused code still leaves the till with its old identity cleared and a
// fresh device id: the flow only runs on a till that needs pairing, and a
// half-old identity is worse than none (ADR-0116 D5 step 1).
func TestPairRefusedLeavesIdentityCleared(t *testing.T) {
	logs := captureLog(t)
	cloud := newFakePairCloud(t)
	cloud.set(func(c *fakePairCloud) { c.status, c.errOut = http.StatusForbidden, "code_invalid" })
	kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)

	st, err := Pair(context.Background(), cfg, kv, pairCode)
	if err == nil {
		t.Fatal("Pair succeeded on a refused code")
	}
	if st.Registered {
		t.Fatalf("status = %+v after a refused pair, want not registered", st)
	}
	if kv.get(keyToken) != "" || kv.get(keyStoreID) != "" || kv.get(keyEnrolledAt) != "" {
		t.Fatalf("old identity survived: token=%q store=%q enrolled_at=%q", kv.get(keyToken), kv.get(keyStoreID), kv.get(keyEnrolledAt))
	}
	if id := kv.get(keyDeviceID); id == pairOldDev || id == "" {
		t.Fatalf("device_id = %q, want a fresh one", id)
	}
	if kv.get(keyPublicKey) != pinnedKey {
		t.Fatal("the pinned signing key was cleared")
	}
	if tok := Effective(cfg).Marketplace.MerchantToken; tok != "" {
		t.Fatal("the old token is still in use after the identity was cleared")
	}
	if sid := Effective(cfg).Marketplace.StoreID; sid != "" {
		t.Fatalf("Effective store id = %q after a refused pair, want \"\" (stale startup copy %q)", sid, pairOldStore)
	}
	if sid, _ := currentStoreAuth(cfg.Marketplace); sid != "" {
		t.Fatalf("currentStoreAuth store id = %q after a refused pair, want \"\" (stale startup copy %q)", sid, pairOldStore)
	}
	assertPairSecretsNotLogged(t, logs)
}

func TestPairRejectsEmptyOrOverlongCodeWithoutTouchingIdentity(t *testing.T) {
	for name, code := range map[string]string{"empty": "   ", "over 128": strings.Repeat("A", 129)} {
		t.Run(name, func(t *testing.T) {
			cloud := newFakePairCloud(t)
			kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
			if _, err := Pair(context.Background(), cfg, kv, code); err == nil {
				t.Fatal("Pair accepted an invalid code")
			}
			if n := cloud.calls.Load(); n != 0 {
				t.Fatalf("pair calls = %d, want 0", n)
			}
			if kv.get(keyToken) != pairOldToken || kv.get(keyDeviceID) != pairOldDev {
				t.Fatal("an invalid code cleared the identity")
			}
		})
	}
}

// Each UT_MARKETPLACE_* identity pin would win over the paired identity
// (Effective), so Pair refuses before the single-use code is spent and
// before anything is cleared. The store-id pin is the ut-docs#3523 Tester
// regression: without it a main till adopted the paired store's token under
// the pinned store id and reported success.
func TestPairRefusedWhenEnvPinsIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		env string
		set func(m *config.MarketplaceConfig)
	}{
		"merchant token": {"UT_MARKETPLACE_MERCHANT_TOKEN", func(m *config.MarketplaceConfig) { m.MerchantToken = pairOldToken }},
		"client id":      {"UT_MARKETPLACE_CLIENT_ID", func(m *config.MarketplaceConfig) { m.ClientID = "merchant-pinned" }},
		"device id":      {"UT_MARKETPLACE_DEVICE_ID", func(m *config.MarketplaceConfig) { m.DeviceID = pairOldDev }},
		"store id":       {"UT_MARKETPLACE_STORE_ID", func(m *config.MarketplaceConfig) { m.StoreID = pairOldStore }},
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newFakePairCloud(t)
			resetState()
			t.Cleanup(resetState)
			// Init reads the store pin from the environment; the others from
			// cfg, which only the environment can have filled at boot.
			if tc.env == "UT_MARKETPLACE_STORE_ID" {
				t.Setenv(tc.env, pairOldStore)
			}
			kv := newOrderedKV()
			cfg := &config.Config{}
			tc.set(&cfg.Marketplace)
			initForTest(t, cfg, kv)
			cfg.Marketplace.EndpointURL = cloud.srv.URL + "/api"
			before := len(kv.writes)

			_, err := Pair(context.Background(), cfg, kv, pairCode)
			if err == nil {
				t.Fatalf("Pair ran although %s pins the identity", tc.env)
			}
			if n := cloud.calls.Load(); n != 0 {
				t.Fatalf("pair calls = %d, want 0 (the single-use code must not be burnt)", n)
			}
			if after := len(kv.writes); after != before {
				t.Fatalf("Pair wrote %d settings before refusing: %q", after-before, kv.writes[before:])
			}
			if m := Effective(cfg).Marketplace; m.MerchantToken == pairToken {
				t.Fatalf("the paired token is in use under store_id %q", m.StoreID)
			}
		})
	}
}

// On a replica the store pin is not a refusal: the pinned store is the one
// the answer must match (as in redeem), so the identity stays whole — and a
// pinned store also counts as known when the admin sync has not run yet.
func TestPairReplicaWithEnvPinnedStore(t *testing.T) {
	for name, tc := range map[string]struct {
		pin     string
		wantErr error
	}{
		"pin matches the code's store": {pin: pairStore},
		"pin is another store":         {pin: pairOldStore, wantErr: ErrPairStoreMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newFakePairCloud(t)
			t.Setenv("UT_MARKETPLACE_STORE_ID", tc.pin)
			kv, cfg := bootForPair(t, cloud.srv.URL, true, "")
			cfg.Marketplace.StoreID = tc.pin

			_, err := Pair(context.Background(), cfg, kv, pairCode)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Pair err = %v, want %v", err, tc.wantErr)
			}
			m := Effective(cfg).Marketplace
			if m.StoreID != tc.pin {
				t.Fatalf("effective store = %q, want the pinned %q", m.StoreID, tc.pin)
			}
			if m.MerchantToken == pairToken && m.StoreID != pairStore {
				t.Fatalf("split identity: token for %q in use under store_id %q", pairStore, m.StoreID)
			}
			if tc.wantErr == nil && m.MerchantToken != pairToken {
				t.Fatal("the paired credential was not adopted for the pinned store")
			}
		})
	}
}

// A replica that has not synced its store from its main till cannot match
// any answer, so the outcome is known before the call: Pair refuses without
// clearing the identity, minting a device id or burning the code
// (ut-docs#3523 Tester finding).
func TestPairReplicaWithoutSyncedStoreDoesNotBurnCode(t *testing.T) {
	logs := captureLog(t)
	cloud := newFakePairCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, true, "")
	before := len(kv.writes)

	st, err := Pair(context.Background(), cfg, kv, pairCode)
	if !errors.Is(err, ErrPairReplicaNoStore) {
		t.Fatalf("Pair err = %v, want ErrPairReplicaNoStore", err)
	}
	if n := cloud.calls.Load(); n != 0 {
		t.Fatalf("pair calls = %d, want 0 (the single-use code must not be burnt)", n)
	}
	if after := len(kv.writes); after != before {
		t.Fatalf("Pair wrote %d settings before refusing: %q", after-before, kv.writes[before:])
	}
	if kv.get(keyToken) != pairOldToken || kv.get(keyDeviceID) != pairOldDev {
		t.Fatalf("token/device = %q/%q, want the old identity untouched", kv.get(keyToken), kv.get(keyDeviceID))
	}
	if st.Registered {
		t.Fatalf("status = %+v, want not registered", st)
	}
	assertPairSecretsNotLogged(t, logs)
}

func TestPairReplicaMatchingStore(t *testing.T) {
	logs := captureLog(t)
	cloud := newFakePairCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, true, pairStore)

	st, err := Pair(context.Background(), cfg, kv, pairCode)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	body, _ := cloud.last()
	newID := body["device_id"]
	if body["device_name"] != "Back office" {
		t.Fatalf("sent device_name = %q, want the replica's sync till name", body["device_name"])
	}
	if kv.get(keyStoreID) != pairStore || kv.get(keyMerchantID) != pairStore {
		t.Fatalf("store/merchant = %q/%q, want the synced %q kept", kv.get(keyStoreID), kv.get(keyMerchantID), pairStore)
	}
	// Store-level keys are the admin sync's on a replica: never written.
	if w := kv.writesOf(keyStoreID); len(w) != 0 {
		t.Fatalf("store_id written on a replica: %q", w)
	}
	if w := kv.writesOf(keyMerchantID); len(w) != 0 {
		t.Fatalf("merchant_id written on a replica: %q", w)
	}
	if kv.get(keyToken) != pairToken || kv.get(keyDeviceID) != newID || newID == pairOldDev {
		t.Fatalf("token/device = %q/%q, want the paired credential under a fresh device", kv.get(keyToken), kv.get(keyDeviceID))
	}
	// The fresh device id is marked as minted for this till, so the next
	// boot's copied-identity repair leaves it alone.
	if kv.get(keyDeviceTillID) != "till-row-2" {
		t.Fatalf("device_till_id = %q, want this till's sync id", kv.get(keyDeviceTillID))
	}
	if !st.Registered || st.StoreID != pairStore {
		t.Fatalf("status = %+v, want registered under %s", st, pairStore)
	}
	if sid := Effective(cfg).Marketplace.StoreID; sid != pairStore {
		t.Fatalf("Effective store id = %q, want the replica's synced %q kept", sid, pairStore)
	}
	assertPairSecretsNotLogged(t, logs)
}

// A replica refuses an answer for any store but its main till's (the
// not-yet-synced case never reaches the cloud: see
// TestPairReplicaWithoutSyncedStoreDoesNotBurnCode).
func TestPairReplicaStoreMismatchRefused(t *testing.T) {
	logs := captureLog(t)
	cloud := newFakePairCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, true, pairOldStore)

	st, err := Pair(context.Background(), cfg, kv, pairCode)
	if !errors.Is(err, ErrPairStoreMismatch) {
		t.Fatalf("Pair err = %v, want ErrPairStoreMismatch", err)
	}
	if kv.get(keyStoreID) != pairOldStore || kv.get(keyMerchantID) != pairOldStore {
		t.Fatalf("store/merchant = %q/%q, want %q unchanged", kv.get(keyStoreID), kv.get(keyMerchantID), pairOldStore)
	}
	if kv.get(keyToken) == pairToken {
		t.Fatal("the other store's credential was adopted")
	}
	if tok := Effective(cfg).Marketplace.MerchantToken; tok == pairToken {
		t.Fatal("the other store's credential is in use")
	}
	if st.Registered {
		t.Fatalf("status = %+v, want not registered", st)
	}
	if !strings.Contains(logs.String(), pairStore) {
		t.Fatalf("the mismatch warning does not name the paired store:\n%s", logs.String())
	}
	assertPairSecretsNotLogged(t, logs)
}
