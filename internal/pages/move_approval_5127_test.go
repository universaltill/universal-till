package pages

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5127 UT-1b (design-5127 §5, §7 tests 12–16): the till-side
// approval of a move into another shop. A cashier never sees or reaches it;
// a manager approves only with the target's name typed AND a fresh PIN;
// a wrong PIN signs and sends nothing; three wrong PINs decline it; offline
// nothing is signed. The stub cloud verifies the signature with this till's
// own approval key, as ut-cloud does.

const (
	mvStore  = "store-src"
	mvTarget = "store-tgt"
	mvDevice = "till-move-dev"
	mvToken  = "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a"
	mvNew    = "6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b6b"
	mvNonce  = "bm9uY2Utbm9uY2Utbm9uY2U"
	mvExp    = "2026-11-03T09:30:00Z"
	mvPIN    = "135790"
	mvName   = "Corner Café"
)

type moveStubCloud struct {
	srv     *httptest.Server
	calls   atomic.Int32
	mu      sync.Mutex
	decided []string
	bad     int
	// pairRefuse, when set, is the 409 code the move request gets.
	pairRefuse string
}

func newMoveStubCloud(t *testing.T) *moveStubCloud {
	t.Helper()
	c := &moveStubCloud{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/stores/move-approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		k, err := approvalkey.Shared().LoadOrCreate()
		msg, _ := approvalkey.ApprovalMessage(approvalkey.Approval{Decision: req["decision"], MoveID: r.PathValue("id"),
			DeviceID: mvDevice, SourceStore: mvStore, TargetStore: mvTarget, Nonce: mvNonce, ExpiresAt: mvExp})
		sig, _ := base64.RawURLEncoding.DecodeString(req["signature"])
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil || r.Header.Get("Authorization") != "Bearer "+mvToken ||
			req["key_id"] != approvalkey.KID(k.Public().(ed25519.PublicKey)) || !ed25519.Verify(k.Public().(ed25519.PublicKey), msg, sig) {
			c.bad++
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "approval_invalid"}})
			return
		}
		c.decided = append(c.decided, req["decision"])
		if req["decision"] == "decline" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "declined"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"store_id": mvTarget, "merchant_id": mvTarget,
			"device_id": mvDevice, "device_token": mvNew, "moved": map[string]any{"from_store": mvStore, "merge_job": "j1"}}})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"test","pair_move":"approval-v1"}`))
	})
	mux.HandleFunc("POST /api/v1/stores/pair", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		refuse := c.pairRefuse
		c.mu.Unlock()
		if body["move"] != true || body["device_id"] != mvDevice || r.Header.Get("Authorization") != "Bearer "+mvToken {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if refuse != "" {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": refuse}})
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "pending_approval", "move_id": mvPairID,
			"approval": map[string]any{"move_id": mvPairID, "source_store": mvStore, "target_store": mvTarget,
				"target_name": mvName, "nonce": mvNonce, "expires_at": mvExp}}})
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

// mvID is a fresh move id per test: enroll keeps ended moves (a locked one
// is never re-opened), so tests must not share one.
var mvID, mvPairID string

// moveApprovalDeps boots an enrolled main till in mvStore, against the stub
// cloud, with one pending move into mvTarget and a manager PIN.
func moveApprovalDeps(t *testing.T) (*http.ServeMux, *common.Deps, *moveStubCloud) {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	mvID, mvPairID = uuid.NewString(), uuid.NewString()
	_, _, d := newFullAuthDeps(t)
	cloud := newMoveStubCloud(t)
	ctx := context.Background()
	for k, v := range map[string]string{
		"marketplace.device_id":         mvDevice,
		"marketplace.store_id":          mvStore,
		"marketplace.merchant_id":       mvStore,
		"marketplace.token":             mvToken,
		"marketplace.device_registered": mvDevice,
	} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	d.Cfg.Marketplace.EndpointURL = cloud.srv.URL + "/api"
	enroll.Init(ctx, d.Cfg, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() {
		enroll.ApplySyncMoves(json.RawMessage(`[]`), nil)
		enroll.Init(context.Background(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	})
	hash, err := auth.HashPIN(mvPIN)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.NewAuthRepo(d.Db).SetUserPIN(ctx, mgrUser.ID, hash); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]any{map[string]any{"move_id": mvID, "source_store": mvStore, "target_store": mvTarget,
		"target_name": mvName, "nonce": mvNonce, "expires_at": mvExp}})
	enroll.ApplySyncMoves(b, nil)
	mux := http.NewServeMux()
	registerMoveApproval(mux, d)
	return mux, d, cloud
}

func postMove(mux *http.ServeMux, user *auth.User, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/cloud/move/"+mvID, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestMoveNotice_ManagerOnlyNeverOnSaleScreen(t *testing.T) {
	mux, _, _ := moveApprovalDeps(t)
	rec := getWithUser(mux, "/ui/move-approval-notice", &mgrUser)
	body := rec.Body.String()
	if !strings.Contains(body, `href="/settings/cloud/move/`+mvID+`"`) || !strings.Contains(body, mvName) || !strings.Contains(body, `role="status"`) {
		t.Fatalf("manager notice = %s", body)
	}
	if strings.Contains(body, "<dialog") || strings.Contains(body, "showModal") {
		t.Fatal("the notice must never be a modal")
	}
	if rec := getWithUser(mux, "/ui/move-approval-notice", &cashUser); rec.Body.Len() != 0 {
		t.Fatalf("a cashier got the notice: %s", rec.Body.String())
	}
	if rec := getWithUserFromPage(mux, "/ui/move-approval-notice", &mgrUser, "/"); rec.Body.Len() != 0 {
		t.Fatalf("the sale screen got the notice: %s", rec.Body.String())
	}
}

func TestMovePage_CashierRefusedManagerSeesRoute(t *testing.T) {
	mux, _, _ := moveApprovalDeps(t)
	if rec := getWithUser(mux, "/settings/cloud/move/"+mvID, &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET = %d", rec.Code)
	}
	rec := getWithUser(mux, "/settings/cloud/move/"+mvID, &mgrUser)
	body := rec.Body.String()
	for _, want := range []string{mvName, `name="target_name"`, `value="approve"`, `value="decline"`, "Support never asks you"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page lacks %q:\n%s", want, body)
		}
	}
	rec = getWithUser(mux, "/settings/cloud/move/11111111-2222-4333-8444-555555555555", &mgrUser)
	if !strings.Contains(rec.Body.String(), "no longer waiting") {
		t.Fatalf("unknown move page = %s", rec.Body.String())
	}
}

func TestMoveDecision_CashierRefusedNothingSent(t *testing.T) {
	mux, _, cloud := moveApprovalDeps(t)
	rec := postMove(mux, &cashUser, "decision=approve&target_name="+mvName+"&override_pin="+mvPIN)
	if rec.Code != http.StatusForbidden || cloud.calls.Load() != 0 {
		t.Fatalf("cashier POST = %d, cloud calls %d", rec.Code, cloud.calls.Load())
	}
}

func TestMoveApprove_NeedsTypedNameThenPIN(t *testing.T) {
	mux, _, cloud := moveApprovalDeps(t)
	rec := postMove(mux, &mgrUser, "decision=approve&target_name=Wrong+Shop")
	if !strings.Contains(rec.Body.String(), "doesn&#39;t match") || isElevationPrompt(rec) {
		t.Fatalf("wrong name = %s", rec.Body.String())
	}
	rec = postMove(mux, &mgrUser, "decision=approve&target_name=corner+caf%C3%A9")
	if !isElevationPrompt(rec) {
		t.Fatalf("no PIN: want the elevation prompt, got %s", rec.Body.String())
	}
	if cloud.calls.Load() != 0 {
		t.Fatal("something was sent before a PIN")
	}
}

func TestMoveApprove_WrongPINsSendNothingThenDecline(t *testing.T) {
	mux, d, cloud := moveApprovalDeps(t)
	for i := 1; i < enroll.MaxWrongMovePINs; i++ {
		rec := postMove(mux, &mgrUser, "decision=approve&target_name="+mvName+"&override_pin=000000")
		if !isElevationPrompt(rec) || cloud.calls.Load() != 0 {
			t.Fatalf("wrong PIN %d: prompt=%v calls=%d", i, isElevationPrompt(rec), cloud.calls.Load())
		}
	}
	rec := postMove(mux, &mgrUser, "decision=approve&target_name="+mvName+"&override_pin=000000")
	body := rec.Body.String()
	if !strings.Contains(body, "Too many wrong PINs") || !strings.Contains(body, `id="move-actions" hx-swap-oob="true"`) {
		t.Fatalf("locked answer = %s", body)
	}
	cloud.mu.Lock()
	decided := append([]string(nil), cloud.decided...)
	cloud.mu.Unlock()
	if len(decided) != 1 || decided[0] != "decline" {
		t.Fatalf("cloud decisions = %v, want one automatic decline", decided)
	}
	if enroll.Effective(d.Cfg).Marketplace.StoreID != mvStore {
		t.Fatal("a locked move moved the till")
	}
}

func TestMoveApprove_ValidPINSignsAndMoves(t *testing.T) {
	mux, d, cloud := moveApprovalDeps(t)
	rec := postMove(mux, &mgrUser, "decision=approve&target_name="+mvName+"&override_pin="+mvPIN)
	body := rec.Body.String()
	if !strings.Contains(body, "Moved into") || !strings.Contains(body, mvName) {
		t.Fatalf("approve answer = %s", body)
	}
	cloud.mu.Lock()
	bad, decided := cloud.bad, append([]string(nil), cloud.decided...)
	cloud.mu.Unlock()
	if bad != 0 || len(decided) != 1 || decided[0] != "approve" {
		t.Fatalf("cloud: bad=%d decided=%v", bad, decided)
	}
	m := enroll.Effective(d.Cfg).Marketplace
	if m.StoreID != mvTarget || m.DeviceID != mvDevice || m.MerchantToken != mvNew {
		t.Fatalf("identity after move: store %s device %s", m.StoreID, m.DeviceID)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'move_approved' AND entity_id = ?`, mvID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
}

func TestMoveDecline_ValidPIN(t *testing.T) {
	mux, d, cloud := moveApprovalDeps(t)
	rec := postMove(mux, &mgrUser, "decision=decline&override_pin="+mvPIN)
	if !strings.Contains(rec.Body.String(), "Declined") {
		t.Fatalf("decline answer = %s", rec.Body.String())
	}
	if cloud.calls.Load() != 1 || enroll.Effective(d.Cfg).Marketplace.StoreID != mvStore {
		t.Fatal("decline did not stay in the shop")
	}
}

type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no route to host")
}

func TestMoveOffline_NothingSignedOrAsked(t *testing.T) {
	mux, d, cloud := moveApprovalDeps(t)
	d.NetReach = netreach.New(netreach.Options{Endpoint: "https://cloud.example.test/api", Client: &http.Client{Transport: failTransport{}}})
	deadline := time.Now().Add(5 * time.Second)
	for d.NetReach.Status() != netreach.Unreachable {
		if time.Now().After(deadline) {
			t.Fatal("probe never completed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	page := getWithUser(mux, "/settings/cloud/move/"+mvID, &mgrUser).Body.String()
	if !strings.Contains(page, "Connect to the internet") || !strings.Contains(page, "disabled") {
		t.Fatalf("offline page = %s", page)
	}
	rec := postMove(mux, &mgrUser, "decision=approve&target_name="+mvName+"&override_pin="+mvPIN)
	if !strings.Contains(rec.Body.String(), "Connect to the internet") || isElevationPrompt(rec) || cloud.calls.Load() != 0 {
		t.Fatalf("offline POST = %s (calls %d)", rec.Body.String(), cloud.calls.Load())
	}
}

// Settings → Pair with a shop on a till that belongs to a shop, against a
// cloud that offers moves: the confirmation says MOVE, the request keeps the
// identity, and the 202 opens the approval page.
func TestEnrolPair_MoveConfirmsThenOpensApprovalPage(t *testing.T) {
	mux, d, _ := moveApprovalDeps(t)
	registerSettings(mux, d)
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {"K7QX-M2RP"}}, &mgrUser)
	if body := rec.Body.String(); !strings.Contains(body, "Move this till into another shop?") || !strings.Contains(body, "confirm_replace") {
		t.Fatalf("confirmation = %s", body)
	}
	rec = postForm(mux, "/api/enrol/pair", url.Values{"code": {"K7QX-M2RP"}, "confirm_replace": {"1"}}, &mgrUser)
	if got := rec.Header().Get("HX-Redirect"); got != "/settings/cloud/move/"+mvPairID {
		t.Fatalf("HX-Redirect = %q, body %s", got, rec.Body.String())
	}
	m := enroll.Effective(d.Cfg).Marketplace
	if m.StoreID != mvStore || m.DeviceID != mvDevice || m.MerchantToken != mvToken {
		t.Fatal("a pending move changed the identity")
	}
	if _, ok := enroll.PendingMoveByID(d.Cfg, mvPairID); !ok {
		t.Fatal("the pending move is not held")
	}
}

func TestEnrolPair_MoveRefusalIsTranslatedAndChangesNothing(t *testing.T) {
	mux, d, cloud := moveApprovalDeps(t)
	registerSettings(mux, d)
	cloud.mu.Lock()
	cloud.pairRefuse = "move_shop_has_tills"
	cloud.mu.Unlock()
	rec := postForm(mux, "/api/enrol/pair", url.Values{"code": {"K7QX-M2RP"}, "confirm_replace": {"1"}}, &mgrUser)
	if body := rec.Body.String(); !strings.Contains(body, "has other tills") {
		t.Fatalf("refusal = %s", body)
	}
	if m := enroll.Effective(d.Cfg).Marketplace; m.StoreID != mvStore || m.MerchantToken != mvToken {
		t.Fatal("a refused move changed the identity")
	}
}
