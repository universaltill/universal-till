package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0153 "Who is asking", amendment 2026-10-15 (ut-docs#4444): the
// camera identify job is an operator request. Core stamps the signed-in
// cashier and a session check on it, read-only, so an ai.agent plugin's
// catalog reads work for that operator and end with the session.
func TestPluginIdentify_JobCarriesReadOnlyOperator_4444(t *testing.T) {
	h := newIdentifyHarness(t)
	h.d.AuthSvc = auth.NewService(h.d.Db)
	if _, err := h.d.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('csh-1','csh-1','csh-1','x','cashier')`); err != nil {
		t.Fatal(err)
	}
	token, err := h.d.AuthSvc.CreateSession(context.Background(), "csh-1")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var op agentaction.Operator
	var hasOp, readOnly bool
	var live func(context.Context) bool
	done := make(chan struct{})
	h.jmu.Lock()
	h.handler = func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		mu.Lock()
		op, hasOp = plugins.AskOperatorFrom(ctx)
		live, _ = plugins.AskSessionLiveFrom(ctx)
		readOnly = plugins.AskReadOnlyFrom(ctx)
		mu.Unlock()
		close(done)
		return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`), nil
	}
	h.jmu.Unlock()

	body, ct := multipartPost(t, nil, []uploadPart{{field: "photo", filename: "capture.jpg", content: jpegBytes(2048)}})
	req := httptest.NewRequest(http.MethodPost, identifyRoute, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	req = auth.WithUser(req, auth.User{ID: "csh-1", Role: "cashier"})
	req = req.WithContext(auth.WithSessionKey(req.Context(), token))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("identify start = %d %s", rec.Code, rec.Body.String())
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !hasOp || op.UserID != "csh-1" || op.Role != "cashier" || op.Session == "" || op.Request == "" {
		t.Fatalf("identify job operator = %+v (stamped %v), want the signed-in cashier", op, hasOp)
	}
	if !readOnly {
		t.Fatal("identify job operator is not read-only: it could propose agent writes")
	}
	if live == nil || !live(context.Background()) {
		t.Fatal("identify job has no live session check while signed in")
	}
	h.d.AuthSvc.Logout(context.Background(), token)
	if live(context.Background()) {
		t.Fatal("session check still true after sign-out")
	}
}

// Without a signed-in session (UT_AUTH=off, a test harness) the identify
// job carries no operator at all, so an ai.agent plugin's reads are
// refused there, as in every other context without one.
func TestPluginIdentify_NoSessionNoOperator_4444(t *testing.T) {
	h := newIdentifyHarness(t)
	var mu sync.Mutex
	var hasOp, hasLive bool
	done := make(chan struct{})
	h.jmu.Lock()
	h.handler = func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		mu.Lock()
		_, hasOp = plugins.AskOperatorFrom(ctx)
		_, hasLive = plugins.AskSessionLiveFrom(ctx)
		mu.Unlock()
		close(done)
		return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`), nil
	}
	h.jmu.Unlock()
	h.start()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if hasOp || hasLive {
		t.Fatalf("unsigned identify job: operator %v, session check %v; want neither", hasOp, hasLive)
	}
}
