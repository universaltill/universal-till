package enroll

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
)

// ut-docs#5127 UT-1b: a till that belongs to a shop moves into the shop a
// my. code belongs to, keeping its device id, once the cloud advertises
// pair_move "approval-v1"; the move waits for the till's own signed
// approval. The fake cloud below verifies the signature with the key the
// till reported on the pair, exactly as ut-cloud does.

const (
	moveID     = "0b1c7e52-6a58-4f0e-9d3e-2f5f1f6a7b01"
	moveTarget = "store-target"
	moveNonce  = "ZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXp7fH1-f4CBgoM"
	moveToken  = "7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e"
)

// moveExp is the move offer's expiry, always in the future: a fixed date here
// broke main once it passed (ut-docs#5471).
var moveExp = time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)

type fakeMoveCloud struct {
	srv *httptest.Server

	mu            sync.Mutex
	capability    string // /version pair_move
	pairStatus    int
	pairErr       string
	pairBody      map[string]any
	pairAuth      string
	pubKey        ed25519.PublicKey
	approvalCode  int
	approvalErr   string
	approvalData  map[string]any
	decided       []string // decisions whose signature verified
	badSignatures int
	pairCalls     atomic.Int32
	approvalCalls atomic.Int32
}

func pendingBlock(source string) map[string]any {
	return map[string]any{"move_id": moveID, "source_store": source, "target_store": moveTarget,
		"target_name": "Corner Café", "nonce": moveNonce, "expires_at": moveExp}
}

func newFakeMoveCloud(t *testing.T) *fakeMoveCloud {
	t.Helper()
	c := &fakeMoveCloud{capability: pairMoveCapability, pairStatus: http.StatusAccepted, approvalCode: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		cp := c.capability
		c.mu.Unlock()
		out := map[string]any{"version": "test"}
		if cp != "" {
			out["pair_move"] = cp
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/v1/stores/pair", func(w http.ResponseWriter, r *http.Request) {
		c.pairCalls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.pairBody = body
		c.pairAuth = r.Header.Get("Authorization")
		if ak, ok := body["approval_key"].(map[string]any); ok {
			if pk, err := base64.RawURLEncoding.DecodeString(ak["public_key"].(string)); err == nil {
				c.pubKey = pk
			}
		}
		status, errCode := c.pairStatus, c.pairErr
		c.mu.Unlock()
		w.WriteHeader(status)
		switch {
		case status == http.StatusAccepted:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"status": pendingApprovalStatus, "move_id": moveID, "expires_at": moveExp,
				"target":   map[string]any{"store_id": moveTarget, "name": "Corner Café"},
				"approval": pendingBlock(pairOldStore),
			}})
		case status == http.StatusOK:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"store_id": pairStore, "merchant_id": pairMerchant, "device_token": pairToken, "device_id": body["device_id"]}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]string{"code": errCode}})
		}
	})
	mux.HandleFunc("POST /api/v1/stores/move-approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		c.approvalCalls.Add(1)
		var req struct{ Decision, KeyID, Signature string }
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		req.Decision, req.KeyID, req.Signature = raw["decision"], raw["key_id"], raw["signature"]
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.approvalCode != http.StatusOK {
			w.WriteHeader(c.approvalCode)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"usable_from": "2026-11-05T10:00:00Z"}, "error": map[string]string{"code": c.approvalErr}})
			return
		}
		// Rebuild the bytes from the cloud's own record (design §3).
		msg, _ := approvalkey.ApprovalMessage(approvalkey.Approval{Decision: req.Decision, MoveID: r.PathValue("id"),
			DeviceID: pairOldDev, SourceStore: pairOldStore, TargetStore: moveTarget, Nonce: moveNonce, ExpiresAt: moveExp})
		sig, _ := base64.RawURLEncoding.DecodeString(req.Signature)
		if r.Header.Get("Authorization") != "Bearer "+pairOldToken || c.pubKey == nil ||
			req.KeyID != approvalkey.KID(c.pubKey) || !ed25519.Verify(c.pubKey, msg, sig) {
			c.badSignatures++
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "approval_invalid"}})
			return
		}
		c.decided = append(c.decided, req.Decision)
		if req.Decision == MoveDecline {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "declined", "move_id": moveID}})
			return
		}
		data := c.approvalData
		if data == nil {
			data = map[string]any{"store_id": moveTarget, "merchant_id": moveTarget, "device_id": pairOldDev,
				"device_token": moveToken, "moved": map[string]any{"from_store": pairOldStore, "merge_job": "job-1"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakeMoveCloud) set(f func(c *fakeMoveCloud)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
}

func TestPairMoveSendsMoveWithCurrentIdentityAndHoldsPending(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	_, err := Pair(context.Background(), cfg, kv, pairCode)
	var pend *MovePendingError
	if !errors.As(err, &pend) {
		t.Fatalf("Pair err = %v, want *MovePendingError", err)
	}
	if pend.Move.MoveID != moveID || pend.Move.TargetName != "Corner Café" || pend.Move.ExpiresRaw != moveExp {
		t.Fatalf("pending = %+v", pend.Move)
	}
	cloud.mu.Lock()
	body, auth := cloud.pairBody, cloud.pairAuth
	cloud.mu.Unlock()
	if body["move"] != true || body["device_id"] != pairOldDev || auth != "Bearer "+pairOldToken {
		t.Fatalf("move request = %v auth %q, want move:true with the current device id and token", body, auth)
	}
	// Nothing was wiped: still in S with its token and device id.
	st := CurrentStatus()
	if !st.Registered || st.DeviceID != pairOldDev || Effective(cfg).Marketplace.StoreID != pairOldStore || Effective(cfg).Marketplace.MerchantToken != pairOldToken {
		t.Fatalf("after a pending move the till left its shop: %+v", st)
	}
	if w := kv.writesOf(keyToken); len(w) != 0 {
		t.Fatalf("token was written during a pending move: %v", w)
	}
	if got := PendingMoves(cfg); len(got) != 1 || got[0].MoveID != moveID {
		t.Fatalf("PendingMoves = %+v", got)
	}
}

func TestPairWithoutCapabilityPairsAsBefore(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	cloud.set(func(c *fakeMoveCloud) { c.capability, c.pairStatus = "", http.StatusOK })
	kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	cloud.mu.Lock()
	body, auth := cloud.pairBody, cloud.pairAuth
	cloud.mu.Unlock()
	if _, has := body["move"]; has || auth != "" {
		t.Fatalf("a cloud without pair_move got a move request: %v auth %q", body, auth)
	}
	if CurrentStatus().DeviceID == pairOldDev {
		t.Fatal("an ordinary re-pair kept the old device id")
	}
}

func TestPairMoveRefusalLeavesTillUntouched(t *testing.T) {
	for _, code := range []string{"move_shop_has_tills", "move_needs_owner", "store_merging", "move_pending"} {
		t.Run(code, func(t *testing.T) {
			cloud := newFakeMoveCloud(t)
			cloud.set(func(c *fakeMoveCloud) { c.pairStatus, c.pairErr = http.StatusConflict, code })
			kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
			_, err := Pair(context.Background(), cfg, kv, pairCode)
			var ref *MoveRefusedError
			if !errors.As(err, &ref) || ref.Code != code {
				t.Fatalf("err = %v, want MoveRefusedError %s", err, code)
			}
			if cloud.pairCalls.Load() != 1 {
				t.Fatalf("pair calls = %d, want 1 (no fallback pairing)", cloud.pairCalls.Load())
			}
			if Effective(cfg).Marketplace.MerchantToken != pairOldToken || CurrentStatus().DeviceID != pairOldDev {
				t.Fatal("a refused move changed the identity")
			}
			if len(kv.writesOf(keyToken)) != 0 || len(kv.writesOf(keyDeviceID)) != 0 {
				t.Fatal("a refused move wrote identity keys")
			}
		})
	}
}

func TestPairMoveUnauthorizedFallsBackOnlyWithoutHistory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		history  bool
		hooked   bool
		wantLost bool
	}{
		{"history", true, true, true},
		{"no history", false, true, false},
		{"no hook fails closed", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cloud := newFakeMoveCloud(t)
			cloud.set(func(c *fakeMoveCloud) { c.pairStatus, c.pairErr = http.StatusUnauthorized, moveUnauthorizedCode })
			kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
			if tc.hooked {
				SetMoveHooks(MoveHooks{HasTradingHistory: func(context.Context) (bool, error) { return tc.history, nil }})
			}
			_, err := Pair(context.Background(), cfg, kv, pairCode)
			if tc.wantLost {
				if !errors.Is(err, ErrMoveSourceLost) {
					t.Fatalf("err = %v, want ErrMoveSourceLost", err)
				}
				if CurrentStatus().DeviceID != pairOldDev {
					t.Fatal("identity changed")
				}
				return
			}
			// The fallback posts again, without move (the fake still
			// answers 401, so it fails, but as an ordinary pairing).
			if cloud.pairCalls.Load() != 2 {
				t.Fatalf("pair calls = %d, want the move then the ordinary pairing", cloud.pairCalls.Load())
			}
			cloud.mu.Lock()
			_, moved := cloud.pairBody["move"]
			cloud.mu.Unlock()
			if moved {
				t.Fatal("the fallback was still a move")
			}
		})
	}
}

func TestPairMoveUnavailableOrSameShopFallsBack(t *testing.T) {
	for _, code := range []string{moveUnavailableCode, deviceExistsCode} {
		t.Run(code, func(t *testing.T) {
			cloud := newFakeMoveCloud(t)
			cloud.set(func(c *fakeMoveCloud) { c.pairStatus, c.pairErr = http.StatusConflict, code })
			kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
			_, _ = Pair(context.Background(), cfg, kv, pairCode)
			if cloud.pairCalls.Load() != 2 {
				t.Fatalf("pair calls = %d, want a fallback ordinary pairing", cloud.pairCalls.Load())
			}
		})
	}
}

func TestReplicaNeverMoves(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	cloud.set(func(c *fakeMoveCloud) { c.pairStatus = http.StatusOK })
	kv, cfg := bootForPair(t, cloud.srv.URL, true, pairOldStore)
	_, _ = Pair(context.Background(), cfg, kv, pairCode)
	cloud.mu.Lock()
	_, moved := cloud.pairBody["move"]
	cloud.mu.Unlock()
	if moved {
		t.Fatal("a replica asked for a move")
	}
	if PairWouldMove(context.Background(), cfg, kv) {
		t.Fatal("PairWouldMove true on a replica")
	}
}

func pendingTill(t *testing.T) (*fakeMoveCloud, *orderedKV, func() []PendingMove, func(string) (MoveResult, error)) {
	t.Helper()
	cloud := newFakeMoveCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	if _, err := Pair(context.Background(), cfg, kv, pairCode); err == nil {
		t.Fatal("want a pending move")
	}
	return cloud, kv, func() []PendingMove { return PendingMoves(cfg) },
		func(d string) (MoveResult, error) { return DecideMove(context.Background(), cfg, kv, moveID, d) }
}

func TestApproveSignsAndAdoptsTheNewShopKeepingTheDeviceID(t *testing.T) {
	cloud, kv, list, decide := pendingTill(t)
	res, err := decide(MoveApprove)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.StoreID != moveTarget {
		t.Fatalf("result = %+v", res)
	}
	if cloud.badSignatures != 0 || len(cloud.decided) != 1 || cloud.decided[0] != MoveApprove {
		t.Fatalf("cloud saw bad=%d decided=%v", cloud.badSignatures, cloud.decided)
	}
	st := CurrentStatus()
	if st.DeviceID != pairOldDev || st.StoreID != moveTarget || !st.Registered {
		t.Fatalf("status after move = %+v, want same device in the target shop", st)
	}
	if got, _, _ := kv.Get(context.Background(), keyToken); got != moveToken {
		t.Fatal("the target credential was not persisted")
	}
	if got, _, _ := kv.Get(context.Background(), keyDeviceID); got != pairOldDev {
		t.Fatalf("device id = %q, want kept", got)
	}
	if len(list()) != 0 {
		t.Fatal("the move is still pending")
	}
	if e, ok := EndedMoveByID(moveID); !ok || e.Status != "moved" {
		t.Fatalf("ended = %+v %v", e, ok)
	}
}

func TestApproveResetsUploadLedger(t *testing.T) {
	_, _, _, decide := pendingTill(t)
	var reset atomic.Int32
	SetMoveHooks(MoveHooks{ResetUploadLedger: func(context.Context) error { reset.Add(1); return nil }})
	if _, err := decide(MoveApprove); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if reset.Load() != 1 {
		t.Fatalf("ledger resets = %d, want 1", reset.Load())
	}
}

func TestDeclineEndsTheMoveAndKeepsTheShop(t *testing.T) {
	cloud, _, list, decide := pendingTill(t)
	if _, err := decide(MoveDecline); err != nil {
		t.Fatalf("decline: %v", err)
	}
	if len(cloud.decided) != 1 || cloud.decided[0] != MoveDecline {
		t.Fatalf("decided = %v", cloud.decided)
	}
	if CurrentStatus().StoreID == moveTarget || len(list()) != 0 {
		t.Fatal("decline moved the till or kept the move")
	}
}

func TestDecisionRefusalsAreTyped(t *testing.T) {
	for _, tc := range []struct {
		status   int
		code     string
		terminal bool
	}{
		{http.StatusGone, "move_expired", true},
		{http.StatusConflict, "move_cancelled", true},
		{http.StatusConflict, "move_key_quarantined", false},
		{http.StatusConflict, "move_key_conflict", false},
		{http.StatusForbidden, "approval_invalid", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			cloud, _, list, decide := pendingTill(t)
			cloud.set(func(c *fakeMoveCloud) { c.approvalCode, c.approvalErr = tc.status, tc.code })
			_, err := decide(MoveApprove)
			var ref *MoveRefusedError
			if !errors.As(err, &ref) || ref.Code != tc.code {
				t.Fatalf("err = %v", err)
			}
			if tc.code == "move_key_quarantined" && ref.UsableFrom != "2026-11-05T10:00:00Z" {
				t.Fatalf("usable_from = %q", ref.UsableFrom)
			}
			if got := len(list()) == 0; got != tc.terminal {
				t.Fatalf("pending dropped = %v, want %v", got, tc.terminal)
			}
			if CurrentStatus().DeviceID != pairOldDev || CurrentStatus().StoreID == moveTarget {
				t.Fatal("a refused decision changed the identity")
			}
		})
	}
}

func TestDecideOfflineSendsNothingAndKeepsTheMove(t *testing.T) {
	cloud, _, list, decide := pendingTill(t)
	cloud.srv.Close()
	_, err := decide(MoveApprove)
	if !errors.Is(err, ErrMoveOffline) {
		t.Fatalf("err = %v, want ErrMoveOffline", err)
	}
	if len(list()) != 1 {
		t.Fatal("an offline approval dropped the move")
	}
}

func TestDecideUnknownMove(t *testing.T) {
	cloud, _, _, decide := pendingTill(t)
	resetMoves()
	_, err := decide(MoveApprove)
	if !errors.Is(err, ErrMoveUnknown) {
		t.Fatalf("err = %v", err)
	}
	if cloud.approvalCalls.Load() != 0 {
		t.Fatal("an unknown move reached the cloud")
	}
}

func TestApplySyncMoves(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	_, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

	// Absent field: nothing changes.
	ApplySyncMoves(nil, nil)
	if len(PendingMoves(cfg)) != 0 {
		t.Fatal("pending from nothing")
	}
	other := pendingBlock("store-someone-else")
	other["move_id"] = "11111111-2222-4333-8444-555555555555"
	bad := pendingBlock(pairOldStore)
	bad["move_id"] = "11111111-2222-4333-8444-666666666666"
	bad["nonce"] = "has\nnewline"
	ApplySyncMoves(raw([]any{pendingBlock(pairOldStore), other, bad}), nil)
	got := PendingMoves(cfg)
	if len(got) != 1 || got[0].MoveID != moveID {
		t.Fatalf("PendingMoves = %+v, want only the valid move out of this till's shop", got)
	}
	// Present but empty: the list is authoritative.
	ApplySyncMoves(raw([]any{}), nil)
	if len(PendingMoves(cfg)) != 0 {
		t.Fatal("an empty pending_moves kept the move")
	}
	// A cancel is never silent.
	ApplySyncMoves(raw([]any{pendingBlock(pairOldStore)}), nil)
	ApplySyncMoves(raw([]any{}), raw([]any{map[string]string{"move_id": moveID, "status": "cancelled"}}))
	if e, ok := EndedMoveByID(moveID); !ok || e.Status != "cancelled" || e.TargetName != "Corner Café" {
		t.Fatalf("ended = %+v %v", e, ok)
	}
}

func TestWrongPINsLockAndDeclineTheMove(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	kv, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	_, _ = Pair(context.Background(), cfg, kv, pairCode)
	for i := 1; i < MaxWrongMovePINs; i++ {
		if RecordWrongMovePIN(moveID) {
			t.Fatalf("locked after %d wrong PINs", i)
		}
	}
	if !RecordWrongMovePIN(moveID) {
		t.Fatal("not locked at the cap")
	}
	LockMove(context.Background(), cfg, kv, moveID)
	if len(cloud.decided) != 1 || cloud.decided[0] != MoveDecline {
		t.Fatalf("decided = %v, want an automatic decline", cloud.decided)
	}
	if e, ok := EndedMoveByID(moveID); !ok || e.Status != "locked" {
		t.Fatalf("ended = %+v", e)
	}
	// A later sync that still lists it does not bring it back.
	b, _ := json.Marshal([]any{pendingBlock(pairOldStore)})
	ApplySyncMoves(b, nil)
	if len(PendingMoves(cfg)) != 0 {
		t.Fatal("a locked move came back")
	}
}

func TestMoveSecretsNotLogged(t *testing.T) {
	logs := captureLog(t)
	_, _, _, decide := pendingTill(t)
	_, _ = decide(MoveApprove)
	assertPairSecretsNotLogged(t, logs)
	if strings.Contains(logs.String(), moveToken) || strings.Contains(logs.String(), moveNonce) {
		t.Fatal("the new token or the nonce reached the log")
	}
}

func TestMoveCapabilityIsCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"pair_move":"approval-v1"}`))
	}))
	t.Cleanup(srv.Close)
	resetMoves()
	t.Cleanup(resetMoves)
	for i := 0; i < 3; i++ {
		if !MoveCapable(context.Background(), srv.URL+"/api") {
			t.Fatal("not capable")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("version fetched %d times, want 1", hits.Load())
	}
	now := time.Now().Add(moveCapTTL + time.Second)
	moveNow = func() time.Time { return now }
	t.Cleanup(func() { moveNow = time.Now })
	MoveCapable(context.Background(), srv.URL+"/api")
	if hits.Load() != 2 {
		t.Fatal("a stale answer was trusted")
	}
}

// Review finding (2026-11-03): a later sync block for a move id the till
// already holds may not change what will be signed. The approval page shows
// the first-seen target; the signature must cover exactly that record, so a
// sync between display and approve cannot swap source, target, nonce or
// expiry under the manager's typed name. Only the display name may follow.
func TestPendingMoveRecordIsImmutablePerID(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	_, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

	ApplySyncMoves(raw([]any{pendingBlock(pairOldStore)}), nil)

	// The owner renamed the shop: the same record, a new display name.
	renamed := pendingBlock(pairOldStore)
	renamed["target_name"] = "Corner Café (renamed)"
	ApplySyncMoves(raw([]any{renamed}), nil)
	p, ok := PendingMoveByID(cfg, moveID)
	if !ok || p.TargetName != "Corner Café (renamed)" {
		t.Fatalf("the display name should follow the cloud; got %+v %v", p, ok)
	}

	// A different record under the same id: ignored whole, name included.
	swapped := pendingBlock(pairOldStore)
	swapped["target_store"] = "store-attacker"
	swapped["nonce"] = "b3RoZXItbm9uY2U"
	swapped["expires_at"] = time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	swapped["target_name"] = "Totally Legit Shop"
	ApplySyncMoves(raw([]any{swapped}), nil)
	p, ok = PendingMoveByID(cfg, moveID)
	if !ok {
		t.Fatal("the move went away")
	}
	if p.TargetStore != moveTarget || p.Nonce != moveNonce || p.ExpiresRaw != moveExp || p.TargetName != "Corner Café (renamed)" {
		t.Fatalf("record changed under a known move id: %+v", p)
	}
}

// Review finding (2026-11-03): the pending and ended maps are a till cache
// and must be bounded, whatever a sync answer carries.
func TestSyncMovesAreBounded(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	_, cfg := bootForPair(t, cloud.srv.URL, false, pairOldStore)
	var pend, end []any
	for i := 0; i < 3*maxPendingMoves; i++ {
		b := pendingBlock(pairOldStore)
		b["move_id"] = fmt.Sprintf("11111111-2222-4333-8444-%012d", i)
		pend = append(pend, b)
	}
	for i := 0; i < 3*maxEndedMoves; i++ {
		end = append(end, map[string]string{"move_id": fmt.Sprintf("22222222-2222-4333-8444-%012d", i), "status": "cancelled"})
	}
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	ApplySyncMoves(raw(pend), raw(end))
	if n := len(PendingMoves(cfg)); n > maxPendingMoves {
		t.Fatalf("%d pending moves kept, cap is %d", n, maxPendingMoves)
	}
	if n := len(EndedMoves()); n > maxEndedMoves {
		t.Fatalf("%d ended moves kept, cap is %d", n, maxEndedMoves)
	}
	// endMove itself also keeps the cap: every pending move ending locally
	// may not grow the ended set past it.
	for _, p := range PendingMoves(cfg) {
		endMove(p.MoveID, "", "declined")
	}
	if n := len(EndedMoves()); n > maxEndedMoves {
		t.Fatalf("%d ended moves after local ends, cap is %d", n, maxEndedMoves)
	}
}

// resetMoves clears the move state between tests (resetState calls it).
func resetMoves() {
	resetMoveState()
	SetMoveHooks(MoveHooks{})
}
