package enroll

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#7098, ADR-0222 §9: a till paired again with a fresh code of the
// shop that already knows it keeps its device id, proving itself with a
// signature over the hash of the code. Anything the cloud refuses with the
// code unspent falls back to today's flow: a fresh id, the same code. The
// identity is written only after a 200.

const rejoinSeedHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// useApprovalKey puts a known key at the data root for one test; "" removes
// it and "garbage" writes an unusable file.
func useApprovalKey(t *testing.T, seedHex string) {
	t.Helper()
	p := paths.Data("secrets", "approval-ed25519.key")
	prev, prevErr := os.ReadFile(p)
	t.Cleanup(func() {
		if prevErr == nil {
			_ = os.WriteFile(p, prev, 0o600)
		} else {
			_ = os.Remove(p)
		}
	})
	_ = os.Remove(p)
	switch seedHex {
	case "":
		return
	case "garbage":
		if err := os.MkdirAll(paths.Data("secrets"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Data("secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, seed, 0o600); err != nil {
		t.Fatal(err)
	}
}

type pairStep struct {
	status int
	code   string // error.code on a non-200
	raw    string // verbatim body, when set
	drop   bool   // close the connection without an answer
	answer map[string]any
}

// rejoinCloud scripts the answers to successive POST /pair calls and keeps
// every request body in order. /version advertises rejoin (and pair_move)
// only when asked.
type rejoinCloud struct {
	srv *httptest.Server

	mu         sync.Mutex
	steps      []pairStep
	bodies     []map[string]any
	rejoin     string // /version rejoin
	pairMove   string // /version pair_move
	version404 bool
}

func newRejoinCloud(t *testing.T, steps ...pairStep) *rejoinCloud {
	t.Helper()
	c := &rejoinCloud{steps: steps, rejoin: rejoinCapability}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		rj, pm, nf := c.rejoin, c.pairMove, c.version404
		c.mu.Unlock()
		if nf {
			http.NotFound(w, r)
			return
		}
		out := map[string]any{"version": "test"}
		if rj != "" {
			out["rejoin"] = rj
		}
		if pm != "" {
			out["pair_move"] = pm
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/v1/stores/pair", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		n := len(c.bodies)
		step := pairStep{status: http.StatusOK}
		if n <= len(c.steps) {
			step = c.steps[n-1]
		} else if len(c.steps) > 0 {
			step = c.steps[len(c.steps)-1]
		}
		c.mu.Unlock()
		if step.drop {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if step.status == 0 {
			step.status = http.StatusOK
		}
		w.WriteHeader(step.status)
		switch {
		case step.raw != "":
			_, _ = w.Write([]byte(step.raw))
		case step.status != http.StatusOK:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]string{"code": step.code, "message": "nope"}})
		default:
			ans := step.answer
			if ans == nil {
				ans = map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "device_id": body["device_id"], "device_token": pairToken}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": ans, "error": nil})
		}
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *rejoinCloud) sent() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.bodies...)
}

// bootRejoin boots the old, paired till (device pairOldDev, store
// pairOldStore) pointed at the cloud, with the known approval key.
func bootRejoin(t *testing.T, c *rejoinCloud, replica bool, syncedStore string) (*orderedKV, *config.Config) {
	t.Helper()
	useApprovalKey(t, rejoinSeedHex)
	return bootForPair(t, c.srv.URL, replica, syncedStore)
}

func pubOfSeed(t *testing.T) ed25519.PublicKey {
	t.Helper()
	seed, _ := hex.DecodeString(rejoinSeedHex)
	return ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
}

func rejoinBlockOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	rj, ok := body["rejoin"].(map[string]any)
	if !ok {
		t.Fatalf("no rejoin block in %v", body)
	}
	return rj
}

// assertIdentityUntouched: nothing about the identity was written, and the
// old one is still the live one.
func assertIdentityUntouched(t *testing.T, kv *orderedKV, cfg *config.Config) {
	t.Helper()
	for _, k := range []string{keyDeviceID, keyToken, keyStoreID, keyMerchantID, keyEnrolledAt, keyPairedAt, keyDeviceRegistered} {
		if w := kv.writesOf(k); len(w) != 0 {
			t.Fatalf("%s was written (%q) before a 200", k, w)
		}
	}
	if kv.get(keyDeviceID) != pairOldDev || kv.get(keyToken) != pairOldToken || kv.get(keyStoreID) != pairOldStore {
		t.Fatalf("identity changed: device=%q token=%q store=%q", kv.get(keyDeviceID), kv.get(keyToken), kv.get(keyStoreID))
	}
	m := Effective(cfg).Marketplace
	if m.DeviceID != pairOldDev || m.MerchantToken != pairOldToken || m.StoreID != pairOldStore {
		t.Fatalf("Effective changed: %+v", m)
	}
}

// AC1
func TestPairRejoinKeepsTheDeviceID(t *testing.T) {
	cloud := newRejoinCloud(t)
	kv, cfg := bootRejoin(t, cloud, false, pairOldStore)

	st, err := Pair(context.Background(), cfg, kv, pairCode)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	sent := cloud.sent()
	if len(sent) != 1 {
		t.Fatalf("%d /pair calls, want exactly 1", len(sent))
	}
	b := sent[0]
	if b["device_id"] != pairOldDev {
		t.Fatalf("device_id = %v, want the current id %s", b["device_id"], pairOldDev)
	}
	if _, has := b["move"]; has {
		t.Fatal("rejoin was sent with move")
	}
	rj := rejoinBlockOf(t, b)
	ak, _ := rj["approval_key"].(map[string]any)
	pub := pubOfSeed(t)
	wantPub := base64.RawURLEncoding.EncodeToString(pub)
	if rj["device_id"] != pairOldDev || ak["public_key"] != wantPub || ak["kid"] != approvalkey.KID(pub) {
		t.Fatalf("rejoin block = %v", rj)
	}
	hash, ok := approvalkey.RejoinCodeHash(pairCode)
	if !ok {
		t.Fatal("test code does not hash")
	}
	msg, err := approvalkey.RejoinMessage(pairOldDev, wantPub, hash)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(rj["sig"].(string))
	if err != nil || !ed25519.Verify(pub, msg, sig) {
		t.Fatalf("the rejoin signature does not verify (%v)", err)
	}
	if kv.get(keyDeviceID) != pairOldDev || kv.get(keyDeviceRegistered) != pairOldDev {
		t.Fatalf("device_id = %q registered = %q, want %s", kv.get(keyDeviceID), kv.get(keyDeviceRegistered), pairOldDev)
	}
	if kv.get(keyToken) != pairToken || kv.get(keyStoreID) != pairStore {
		t.Fatalf("token/store not adopted: %q %q", kv.get(keyToken), kv.get(keyStoreID))
	}
	if got := Effective(cfg).Marketplace.DeviceID; got != pairOldDev || !st.Registered {
		t.Fatalf("Effective device = %q, status %+v", got, st)
	}
}

// AC2: a name change never changes identity.
func TestPairRejoinUnderANewNameKeepsTheDeviceID(t *testing.T) {
	cloud := newRejoinCloud(t)
	kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
	_ = kv.fakeKV.Set(context.Background(), "till.name", "Phone, upstairs")

	if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	sent := cloud.sent()
	if len(sent) != 1 || sent[0]["device_id"] != pairOldDev || sent[0]["device_name"] != "Phone, upstairs" {
		t.Fatalf("sent %v, want one call as %s under the new name", sent, pairOldDev)
	}
}

// AC3 (B6): every refusal that leaves the code unspent falls back once.
func TestPairRejoinRefusedFallsBackToANewDevice(t *testing.T) {
	for _, code := range []string{"rejoin_refused", "device_exists", "device_revoked"} {
		t.Run(code, func(t *testing.T) {
			cloud := newRejoinCloud(t, pairStep{status: http.StatusConflict, code: code}, pairStep{status: http.StatusOK})
			kv, cfg := bootRejoin(t, cloud, false, pairOldStore)

			if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
				t.Fatalf("Pair: %v", err)
			}
			sent := cloud.sent()
			if len(sent) != 2 {
				t.Fatalf("%d /pair calls, want 2", len(sent))
			}
			if sent[0]["code"] != pairCode || sent[1]["code"] != pairCode {
				t.Fatalf("codes %v / %v, want the same code twice", sent[0]["code"], sent[1]["code"])
			}
			if _, has := sent[1]["rejoin"]; has {
				t.Fatal("the fallback carries a rejoin block")
			}
			id, _ := sent[1]["device_id"].(string)
			if !strings.HasPrefix(id, "till-") || id == pairOldDev {
				t.Fatalf("fallback device_id = %q, want a fresh till-<uuid>", id)
			}
			if kv.get(keyDeviceID) != id || kv.get(keyDeviceRegistered) != id || kv.get(keyToken) != pairToken {
				t.Fatalf("fresh identity not adopted: device=%q token=%q", kv.get(keyDeviceID), kv.get(keyToken))
			}
			if kv.get(keyPairedAt) == "" {
				t.Fatal("paired_at not recorded")
			}
		})
	}
}

func TestPairRejoinFallbackThatFailsLeavesTheIdentityUntouched(t *testing.T) {
	cloud := newRejoinCloud(t, pairStep{status: http.StatusConflict, code: "rejoin_refused"}, pairStep{status: http.StatusForbidden, code: "invalid_code"})
	kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err == nil {
		t.Fatal("Pair succeeded on a refused fallback")
	}
	if n := len(cloud.sent()); n != 2 {
		t.Fatalf("%d /pair calls, want 2 (one fallback, no loop)", n)
	}
	assertIdentityUntouched(t, kv, cfg)
}

func TestPairRejoinOtherRefusalsDoNotFallBack(t *testing.T) {
	for name, step := range map[string]pairStep{
		"invalid_code":    {status: http.StatusForbidden, code: "invalid_code"},
		"store_merging":   {status: http.StatusConflict, code: "store_merging"},
		"rate_limited":    {status: http.StatusTooManyRequests, code: "rate_limited"},
		"invalid_request": {status: http.StatusBadRequest, code: "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newRejoinCloud(t, step)
			kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
			if _, err := Pair(context.Background(), cfg, kv, pairCode); err == nil {
				t.Fatal("Pair succeeded")
			}
			if n := len(cloud.sent()); n != 1 {
				t.Fatalf("%d /pair calls, want exactly 1", n)
			}
			assertIdentityUntouched(t, kv, cfg)
		})
	}
}

// AC4
func TestPairWithoutAUsableApprovalKeySendsNoRejoin(t *testing.T) {
	for _, key := range []string{"", "garbage"} {
		name := key
		if name == "" {
			name = "no file"
		}
		t.Run(name, func(t *testing.T) {
			cloud := newRejoinCloud(t)
			kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
			useApprovalKey(t, key)
			if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
				t.Fatalf("Pair: %v", err)
			}
			sent := cloud.sent()
			if len(sent) != 1 {
				t.Fatalf("%d /pair calls, want 1", len(sent))
			}
			if _, has := sent[0]["rejoin"]; has {
				t.Fatal("rejoin sent without a usable key")
			}
			if id, _ := sent[0]["device_id"].(string); id == pairOldDev || !strings.HasPrefix(id, "till-") {
				t.Fatalf("device_id = %q, want a fresh id", id)
			}
		})
	}
}

func TestPairWithAMalformedCodeSendsNoRejoin(t *testing.T) {
	cloud := newRejoinCloud(t)
	kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
	if _, err := Pair(context.Background(), cfg, kv, "K7QX-M2R0"); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if _, has := cloud.sent()[0]["rejoin"]; has {
		t.Fatal("rejoin sent for a code the cloud cannot normalise")
	}
}

func TestPairWithoutTheRejoinCapabilitySendsNoRejoin(t *testing.T) {
	for name, set := range map[string]func(c *rejoinCloud){
		"no field":    func(c *rejoinCloud) { c.rejoin = "" },
		"other value": func(c *rejoinCloud) { c.rejoin = "v2" },
		"404":         func(c *rejoinCloud) { c.version404 = true },
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newRejoinCloud(t)
			set(cloud)
			kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
			if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
				t.Fatalf("Pair: %v", err)
			}
			sent := cloud.sent()
			if len(sent) != 1 {
				t.Fatalf("%d /pair calls, want 1", len(sent))
			}
			if _, has := sent[0]["rejoin"]; has {
				t.Fatal("rejoin sent to a cloud that does not advertise it")
			}
			if sent[0]["device_id"] == pairOldDev {
				t.Fatal("the old id was sent without a rejoin proof")
			}
		})
	}
}

// AC5
func TestPairRejoinTransportAnd5xxLeaveTheIdentityUntouched(t *testing.T) {
	for name, step := range map[string]pairStep{
		"connection closed": {drop: true},
		"503":               {status: http.StatusServiceUnavailable, code: "pair_failed"},
		"malformed token":   {answer: map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "device_token": "short"}},
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newRejoinCloud(t, step)
			kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
			if _, err := Pair(context.Background(), cfg, kv, pairCode); err == nil {
				t.Fatal("Pair succeeded")
			}
			if n := len(cloud.sent()); n != 1 {
				t.Fatalf("%d /pair calls, want 1", n)
			}
			assertIdentityUntouched(t, kv, cfg)
		})
	}
}

// ADR-0222 §5: the till refuses an answer naming another device.
func TestPairAnswerForAnotherDeviceIsRejected(t *testing.T) {
	cloud := newRejoinCloud(t, pairStep{answer: map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "device_id": "till-someone-else", "device_token": pairToken}})
	kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
	_, err := Pair(context.Background(), cfg, kv, pairCode)
	if !errors.Is(err, errPairAnswerRejected) {
		t.Fatalf("err = %v, want errPairAnswerRejected", err)
	}
	assertIdentityUntouched(t, kv, cfg)
}

// AC7a: move and rejoin never share a request.
func TestPairMoveNeverCarriesRejoin(t *testing.T) {
	cloud := newRejoinCloud(t,
		pairStep{status: http.StatusConflict, code: deviceExistsCode}, // the move leg: "this is your own shop"
		pairStep{status: http.StatusOK})
	cloud.pairMove = pairMoveCapability
	kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	sent := cloud.sent()
	if len(sent) != 2 {
		t.Fatalf("%d /pair calls, want 2 (move, rejoin)", len(sent))
	}
	if sent[0]["move"] != true {
		t.Fatalf("first call is not the move: %v", sent[0])
	}
	if _, has := sent[0]["rejoin"]; has {
		t.Fatal("the move request carries a rejoin block")
	}
	if _, has := sent[1]["move"]; has {
		t.Fatal("the rejoin request carries move")
	}
	rejoinBlockOf(t, sent[1])
	if sent[1]["device_id"] != pairOldDev || kv.get(keyDeviceID) != pairOldDev {
		t.Fatalf("device id changed after a rejoin that followed a move leg")
	}
}

// AC7b
func TestPairReplicaRejoinForAnotherStoreIsRefused(t *testing.T) {
	cloud := newRejoinCloud(t, pairStep{answer: map[string]any{"store_id": "store-other", "merchant_id": pairMerchant, "device_id": pairOldDev, "device_token": pairToken}})
	kv, cfg := bootRejoin(t, cloud, true, pairOldStore)
	_, err := Pair(context.Background(), cfg, kv, pairCode)
	if !errors.Is(err, ErrPairStoreMismatch) {
		t.Fatalf("err = %v, want ErrPairStoreMismatch", err)
	}
	assertIdentityUntouched(t, kv, cfg)
}

func TestPairRejoinDoesNotLogSecrets(t *testing.T) {
	logs := captureLog(t)
	cloud := newRejoinCloud(t, pairStep{status: http.StatusConflict, code: "rejoin_refused"}, pairStep{status: http.StatusOK})
	kv, cfg := bootRejoin(t, cloud, false, pairOldStore)
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
		t.Fatal(err)
	}
	hash, _ := approvalkey.RejoinCodeHash(pairCode)
	st := approvalkey.NewAt(paths.Data("secrets", "approval-ed25519.key"))
	_, _, sig, err := st.SignRejoin(pairOldDev, hash)
	if err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, s := range []string{pairCode, "K7QXM2RP", pairToken, pairOldToken, hash, sig} {
		if strings.Contains(out, s) {
			t.Fatalf("a secret reached the log (%q):\n%s", s, out)
		}
	}
}

// The slice boundary: the wizard's first_till pairing still takes a fresh id.
func TestPairFirstTillSendsNoRejoin(t *testing.T) {
	useApprovalKey(t, rejoinSeedHex)
	resetState()
	t.Cleanup(resetState)
	cloud := newRejoinCloud(t)
	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = cloud.srv.URL + "/api"
	if _, err := PairFirstTill(context.Background(), cfg, pairCode, "Till", ""); err != nil {
		t.Fatalf("PairFirstTill: %v", err)
	}
	sent := cloud.sent()
	if len(sent) != 1 {
		t.Fatalf("%d /pair calls, want 1", len(sent))
	}
	if _, has := sent[0]["rejoin"]; has {
		t.Fatal("PairFirstTill sent a rejoin block")
	}
}
