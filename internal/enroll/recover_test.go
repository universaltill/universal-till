package enroll

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0222 §4.3: the till side of the recover exchange, against a fake cloud
// that rebuilds and verifies the signed message the way ut-cloud will.

const (
	recStore  = "11111111-2222-4333-8444-555555555555"
	recNonce  = "nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn"
	recNewTok = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// fakeRecoverCloud serves /api/v1/stores/devices/recover{/challenge}.
type fakeRecoverCloud struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	authHdr []string
	// answer for the recover step; zero = a good 200 after verifying
	status      int
	body        string
	retryAfter  string
	challengeSt int // non-zero: the challenge step answers this status
	failVerify  atomic.Bool
	registers   atomic.Int32
	verified    atomic.Int32
	recoverHits atomic.Int32
	answerStore string      // override store_id in the 200
	dateHdr     string      // the answer's Date header (the cloud's clock); "" = the server's own
	dropRecover atomic.Bool // the recover step loses its answer (connection closed)
}

func newFakeRecoverCloud(t *testing.T) *fakeRecoverCloud {
	f := &fakeRecoverCloud{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		f.registers.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/stores/devices/recover/challenge", func(w http.ResponseWriter, r *http.Request) {
		f.note(r)
		if f.challengeSt != 0 {
			w.WriteHeader(f.challengeSt)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"challenge_id":"6f1c2b9e-3d4a-4e5f-8a7b-0c1d2e3f4a5b","nonce":"` + recNonce + `","expires_at":"2026-12-01T00:05:00Z"},"error":null}`)) // fixed-date:ok signed-message fixture, never compared with the clock
	})
	mux.HandleFunc("/api/v1/stores/devices/recover", func(w http.ResponseWriter, r *http.Request) {
		f.note(r)
		f.recoverHits.Add(1)
		var req struct {
			StoreID     string `json:"store_id"`
			DeviceID    string `json:"device_id"`
			ChallengeID string `json:"challenge_id"`
			ApprovalKey struct {
				Kid       string `json:"kid"`
				PublicKey string `json:"public_key"`
			} `json:"approval_key"`
			Sig string `json:"sig"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		pub, _ := base64.RawURLEncoding.DecodeString(req.ApprovalKey.PublicKey)
		msg, _ := approvalkey.RecoverMessage(req.StoreID, req.DeviceID, req.ChallengeID, recNonce, "2026-12-01T00:05:00Z", req.ApprovalKey.PublicKey) // fixed-date:ok signed-message fixture, never compared with the clock
		sig, _ := base64.RawURLEncoding.DecodeString(req.Sig)
		if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, msg, sig) || f.failVerify.Load() {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"data":null,"error":{"code":"recover_refused","message":"refused"}}`))
			return
		}
		f.verified.Add(1)
		if f.dropRecover.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				c, _, _ := hj.Hijack()
				_ = c.Close()
			}
			return
		}
		if f.dateHdr != "" {
			w.Header().Set("Date", f.dateHdr)
		}
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
			return
		}
		store := req.StoreID
		if f.answerStore != "" {
			store = f.answerStore
		}
		_, _ = w.Write([]byte(`{"data":{"store_id":"` + store + `","device_id":"` + req.DeviceID + `","device_token":"` + recNewTok + `","role":"main","main_till":true,"recovered_at":"2026-11-26T22:00:00Z"},"error":null}`)) // fixed-date:ok signed-message fixture, never compared with the clock
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRecoverCloud) note(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	f.authHdr = append(f.authHdr, r.Header.Get("Authorization"))
}

// recoverTill boots an enrolled till holding a dead token and its own key.
func recoverTill(t *testing.T, f *fakeRecoverCloud, mutate func(*config.Config)) (*config.Config, *countingKV) {
	t.Helper()
	orig := paths.DataDir()
	t.Cleanup(func() { paths.Init(orig) })
	paths.Init(t.TempDir())
	if _, err := approvalkey.Shared().LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	cfg := freshConfig(f.srv.URL)
	if mutate != nil {
		mutate(cfg)
	}
	resetState()
	kv := newCountingKV()
	for k, v := range map[string]string{
		keyDeviceID: rotOwnDevice, keyStoreID: recStore, keyMerchantID: recStore,
		keyToken: rotLegacy, keyPublicKey: strings.Repeat("cd", 32), keyDeviceRegistered: rotOwnDevice,
	} {
		_ = kv.fakeKV.Set(context.Background(), k, v)
	}
	initForTest(t, cfg, kv)
	t.Cleanup(resetState)
	return cfg, kv
}

func identitySnapshot(kv *countingKV) map[string]string {
	kv.fakeKV.mu.Lock()
	defer kv.fakeKV.mu.Unlock()
	out := map[string]string{}
	for k, v := range kv.fakeKV.m {
		if (strings.HasPrefix(k, "marketplace.") || strings.HasPrefix(k, "till_identity.")) && !strings.HasPrefix(k, "till_identity.heal.") {
			out[k] = v
		}
	}
	return out
}

// AC1 (till side): a dead token plus a proven key gets a new credential for the
// SAME device and store, written once, after the cloud's 200, with no
// Authorization header and no register call.
func TestRecover_HealsAndAdoptsTheTokenOnce(t *testing.T) {
	f := newFakeRecoverCloud(t)
	cfg, kv := recoverTill(t, f, nil)
	logs := captureLog(t)

	out, err := Recover(context.Background(), cfg, kv)
	if err != nil || !out.Healed {
		t.Fatalf("Recover = %+v, %v; want healed", out, err)
	}
	if n := kv.setsOf(keyToken); n != 1 {
		t.Fatalf("%s written %d times, want exactly 1", keyToken, n)
	}
	if got := kv.get(keyToken); got != recNewTok {
		t.Fatalf("saved token != the cloud's")
	}
	if got := Effective(cfg).Marketplace.MerchantToken; got != recNewTok {
		t.Fatalf("live token not adopted")
	}
	if kv.get(keyDeviceID) != rotOwnDevice || kv.get(keyStoreID) != recStore {
		t.Fatal("device or store id changed")
	}
	for _, h := range f.authHdr {
		if h != "" {
			t.Fatalf("an Authorization header was sent: %q", h)
		}
	}
	if f.registers.Load() != 0 {
		t.Fatal("register was called (AC7)")
	}
	if strings.Contains(logs.String(), recNewTok) || strings.Contains(logs.String(), recNonce) {
		t.Fatalf("token or nonce reached the log:\n%s", logs.String())
	}
}

// AC8: every refusal class leaves marketplace.* and till_identity.* byte for byte as
// they were, and nothing is written at all. AC7: and never registers.
func TestRecover_EveryRefusalClassLeavesTheIdentityUntouched(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		chal       int
		wantCode   string
		wantReason string
		transient  bool
		wantUntil  bool
	}{
		{name: "quarantined", status: 409, body: `{"error":{"code":"key_quarantined"},"data":{"usable_from":"2026-12-01T10:00:00Z"}}`, wantCode: "key_quarantined", wantUntil: true}, // fixed-date:ok signed-message fixture, never compared with the clock
		{name: "key conflict", status: 409, body: `{"error":{"code":"key_conflict"}}`, wantCode: "key_conflict"},
		{name: "role conflict", status: 409, body: `{"error":{"code":"role_conflict"}}`, wantCode: "role_conflict"},
		{name: "disabled", status: 409, body: `{"error":{"code":"recover_disabled"}}`, wantCode: "recover_disabled"},
		{name: "limit with date", status: 409, body: `{"error":{"code":"recover_limit"},"data":{"retry_after":"2026-12-02T10:00:00Z"}}`, wantCode: "recover_limit", wantUntil: true}, // fixed-date:ok signed-message fixture, never compared with the clock
		{name: "merging", status: 409, body: `{"error":{"code":"store_merging"}}`, wantCode: "store_merging", transient: true},
		{name: "replaced", status: 409, body: `{"error":{"code":"device_revoked"},"data":{"reason":"replaced","replaced_by":"Back till"}}`, wantCode: "device_revoked", wantReason: "replaced"},
		{name: "moved", status: 409, body: `{"error":{"code":"device_revoked"},"data":{"reason":"moved","moved_to":"Other shop"}}`, wantCode: "device_revoked", wantReason: "moved"},
		{name: "removed", status: 409, body: `{"error":{"code":"device_revoked"},"data":{"reason":"removed"}}`, wantCode: "device_revoked", wantReason: "removed"},
		{name: "store deleted", status: 409, body: `{"error":{"code":"device_revoked"},"data":{"reason":"store_deleted"}}`, wantCode: "device_revoked", wantReason: "store_deleted"},
		{name: "rate limited", status: 429, body: `{}`, retryAfter: "120", wantCode: "rate_limited", transient: true, wantUntil: true},
		{name: "server error", status: 503, body: `{"error":{"code":"auth_unavailable"}}`, wantCode: "auth_unavailable", transient: true, wantUntil: true}, // a 5xx on /recover may hide a commit: 30 s floor
		{name: "old cloud", chal: 404, wantCode: "feature_absent"},
		{name: "garbage 200", status: 200, body: `not json`, wantCode: "bad_answer"},
		{name: "short token", status: 200, body: `{"data":{"store_id":"` + recStore + `","device_id":"` + rotOwnDevice + `","device_token":"short"}}`, wantCode: "bad_answer"},
		{name: "other device", status: 200, body: `{"data":{"store_id":"` + recStore + `","device_id":"till-other","device_token":"` + recNewTok + `"}}`, wantCode: "bad_answer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeRecoverCloud(t)
			f.status, f.body, f.retryAfter, f.challengeSt = c.status, c.body, c.retryAfter, c.chal
			cfg, kv := recoverTill(t, f, nil)
			before := identitySnapshot(kv)
			setsBefore := kv.setsOf(keyToken)
			totalBefore := healTotalSets(kv)

			out, err := Recover(context.Background(), cfg, kv)
			if out.Healed {
				t.Fatalf("healed on a refusal: %+v", out)
			}
			if err != nil {
				t.Fatalf("a refusal is an outcome, not an error: %v", err)
			}
			if out.Code != c.wantCode || out.Reason != c.wantReason || out.Transient != c.transient || out.Until.IsZero() == c.wantUntil {
				t.Fatalf("outcome = %+v, want code %q reason %q transient %v until %v", out, c.wantCode, c.wantReason, c.transient, c.wantUntil)
			}
			after := identitySnapshot(kv)
			if len(after) != len(before) {
				t.Fatalf("identity keys changed: %v -> %v", before, after)
			}
			for k, v := range before {
				if after[k] != v {
					t.Fatalf("identity key %s changed", k)
				}
			}
			if kv.setsOf(keyToken) != setsBefore || healTotalSets(kv) != totalBefore {
				t.Fatal("a refusal wrote to settings")
			}
			if Effective(cfg).Marketplace.MerchantToken != rotLegacy {
				t.Fatal("live token changed")
			}
			if f.registers.Load() != 0 {
				t.Fatal("register was called")
			}
		})
	}
}

func healTotalSets(kv *countingKV) int {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	n := 0
	for _, v := range kv.sets {
		n += v
	}
	return n
}

// A 200 naming another shop is dropped, like ErrReconnectOtherShop.
func TestRecover_AnswerForAnotherShopIsDropped(t *testing.T) {
	f := newFakeRecoverCloud(t)
	f.answerStore = "99999999-2222-4333-8444-555555555555"
	cfg, kv := recoverTill(t, f, nil)
	out, _ := Recover(context.Background(), cfg, kv)
	if out.Healed || out.Code != "bad_answer" {
		t.Fatalf("outcome = %+v", out)
	}
	if kv.get(keyToken) != rotLegacy {
		t.Fatal("token replaced")
	}
}

// A first recover_refused is retried once with a fresh challenge; a second is
// final for the round.
func TestRecover_RefusedIsRetriedOnceThenFinal(t *testing.T) {
	f := newFakeRecoverCloud(t)
	f.failVerify.Store(true)
	cfg, kv := recoverTill(t, f, nil)
	out, _ := Recover(context.Background(), cfg, kv)
	if out.Code != "recover_refused" || out.Healed {
		t.Fatalf("outcome = %+v", out)
	}
	if n := f.recoverHits.Load(); n != 2 {
		t.Fatalf("recover posted %d times, want 2 (one retry)", n)
	}
}

func TestCanRecover(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		setup  func(t *testing.T, cfg *config.Config, kv *countingKV)
		mutate func(*config.Config)
		want   bool
		reason string
	}{
		{name: "ready", want: true},
		{name: "no key", setup: func(t *testing.T, _ *config.Config, _ *countingKV) { paths.Init(t.TempDir()) }, reason: CanRecoverNoProof},
		{name: "stopped", setup: func(_ *testing.T, _ *config.Config, kv *countingKV) { _ = kv.Set(ctx, KeyStopped, "true") }, reason: CanRecoverStopped},
		{name: "env pinned token", mutate: func(c *config.Config) { c.Marketplace.MerchantToken = "env-pinned-token-0001" }, reason: CanRecoverPinned},
		{name: "demo", mutate: func(c *config.Config) { c.Demo = true }, reason: CanRecoverDemo},
		{name: "never register", mutate: func(c *config.Config) { c.NeverRegister = true }, reason: CanRecoverDemo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeRecoverCloud(t)
			cfg, kv := recoverTill(t, f, c.mutate)
			if c.setup != nil {
				c.setup(t, cfg, kv)
			}
			ok, why := CanRecover(ctx, cfg, kv)
			if ok != c.want || why != c.reason {
				t.Fatalf("CanRecover = %v %q, want %v %q", ok, why, c.want, c.reason)
			}
			if !ok {
				out, _ := Recover(ctx, cfg, kv)
				if out.Healed || out.Code != "cannot_recover" || f.recoverHits.Load() != 0 {
					t.Fatalf("Recover went ahead: %+v", out)
				}
			}
		})
	}
}

// A till with no store or token has nothing to heal.
func TestCanRecover_NothingHeldIsRefused(t *testing.T) {
	f := newFakeRecoverCloud(t)
	cfg, kv := recoverTill(t, f, nil)
	_ = kv.Set(context.Background(), keyToken, "")
	mu.Lock()
	cur.Token = ""
	mu.Unlock()
	if ok, why := CanRecover(context.Background(), cfg, kv); ok || why != CanRecoverNoIdentity {
		t.Fatalf("CanRecover = %v %q", ok, why)
	}
}

// AC7: recover.go never reaches register(): no call to any function that
// registers, reconnects, pairs or starts a new shop.
func TestRecoverSourceNeverRegisters(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "recover.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"register": true, "registerLocked": true, "registerDevice": true, "registerDeviceID": true,
		"Reconnect": true, "StartAsNewStore": true, "RegisterNow": true, "EnsureRegistered": true, "Pair": true, "resetIdentityForPair": true}
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && banned[id.Name] {
			t.Errorf("recover.go mentions %s", id.Name)
		}
		return true
	})
}

var _ = time.Second

// A fresh pairing is a new identity: the saved heal state goes and the engine
// is told (design §4.4).
func TestResetIdentityForPairClearsHealState(t *testing.T) {
	kv := newFakeKV()
	ctx := context.Background()
	for _, k := range []string{data.CredHealStateKey, data.CredHealReasonKey, data.CredHealSinceKey, data.CredHealHealsKey, data.CredHealLastHealedAtKey} {
		_ = kv.Set(ctx, k, "x")
	}
	var told atomic.Int32
	SetIdentityResetHook(func() { told.Add(1) })
	t.Cleanup(func() { SetIdentityResetHook(nil); resetState() })
	resetIdentityForPair(ctx, kv, false, "till-new")
	for _, k := range []string{data.CredHealStateKey, data.CredHealReasonKey, data.CredHealSinceKey, data.CredHealHealsKey, data.CredHealLastHealedAtKey} {
		if kv.get(k) != "" {
			t.Fatalf("%s not cleared", k)
		}
	}
	if told.Load() != 1 {
		t.Fatalf("engine told %d times, want 1", told.Load())
	}
}

// Review M5: the till waits "until minus the answer's Date header" on ITS clock,
// so a till whose clock is far behind or ahead does not wait a wrong time, and
// no wait is longer than 73 hours.
func TestRecover_WaitIsMeasuredAgainstTheCloudsDateHeader(t *testing.T) {
	cases := []struct {
		name, usableFrom string
		want             time.Duration
	}{
		{"ten hours ahead of the cloud's clock", "2031-06-15T22:00:00Z", 10 * time.Hour},
		{"capped at 73 hours", "2031-06-25T12:00:00Z", 73 * time.Hour},
		{"already past", "2031-06-15T11:00:00Z", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeRecoverCloud(t)
			f.status = 409
			f.body = `{"error":{"code":"key_quarantined"},"data":{"usable_from":"` + c.usableFrom + `"}}`
			f.dateHdr = "Sun, 15 Jun 2031 12:00:00 GMT" // fixed-date:ok a cloud clock far from the till's
			cfg, kv := recoverTill(t, f, nil)
			start := time.Now()
			out, _ := Recover(context.Background(), cfg, kv)
			if out.Code != "key_quarantined" || out.Until.IsZero() {
				t.Fatalf("outcome = %+v", out)
			}
			got := out.Until.Sub(start)
			if got < c.want-5*time.Second || got > c.want+5*time.Second {
				t.Fatalf("wait = %s, want %s (till-local)", got, c.want)
			}
		})
	}
}

// Review M8: a recover whose answer was lost may have rotated the token; the
// retry waits at least 30 seconds (three lost answers lock a till out for a day).
// A lost challenge answer costs nothing and has no such floor.
func TestRecover_LostRecoverAnswerWaitsAtLeast30Seconds(t *testing.T) {
	f := newFakeRecoverCloud(t)
	f.dropRecover.Store(true)
	cfg, kv := recoverTill(t, f, nil)
	start := time.Now()
	out, _ := Recover(context.Background(), cfg, kv)
	if !out.Transient || out.Code != "transport" {
		t.Fatalf("outcome = %+v, want a transient transport failure", out)
	}
	if w := out.Until.Sub(start); w < 30*time.Second || w > 35*time.Second {
		t.Fatalf("floor = %s, want 30s", w)
	}
	f2 := newFakeRecoverCloud(t)
	cfg2, kv2 := recoverTill(t, f2, nil)
	f2.srv.Close()
	out2, _ := Recover(context.Background(), cfg2, kv2)
	if !out2.Transient || !out2.Until.IsZero() {
		t.Fatalf("a lost challenge answer: %+v, want transient with no floor", out2)
	}
}
