package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0153 "Who is asking" (ut-docs#4143): a job started from a ui action
// keeps the action's operator — the same request id, so the turn's
// tool-call ceiling spans both — and a session check that turns false once
// that session is signed out.
func TestAgentJob_KeepsAskOperatorAndSessionCheck_4143(t *testing.T) {
	h := newJobHarness(t)
	h.d.AuthSvc = auth.NewService(h.d.Db)
	if _, err := h.d.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('mgr-1','mgr-1','mgr-1','x','manager')`); err != nil {
		t.Fatal(err)
	}
	token, err := h.d.AuthSvc.CreateSession(context.Background(), "mgr-1")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var askOp, jobOp agentaction.Operator
	var live func(context.Context) bool
	done := make(chan struct{})
	bus := plugins.SharedBus(h.d.Db)
	bus.ResetSubscribers()
	if _, err := bus.SubscribeWithHandler(t.Context(), viewPluginID, []string{pluginActionAskEvent, viewJobEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			op, _ := plugins.AskOperatorFrom(ctx)
			mu.Lock()
			defer mu.Unlock()
			if ev.Type == pluginActionAskEvent {
				askOp = op
				return json.RawMessage(`{"job":{"event":"` + viewJobEvent + `"}}`), nil
			}
			jobOp = op
			live, _ = plugins.AskSessionLiveFrom(ctx)
			close(done)
			return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`), nil
		}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"_action": {"identify"}, "note": {"tea"}}
	req := httptest.NewRequest(http.MethodPost, "/plugin/views", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	req = auth.WithUser(req, auth.User{ID: "mgr-1", Role: "manager"})
	req = req.WithContext(auth.WithSessionKey(req.Context(), token))
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("action = %d %s", rec.Code, rec.Body.String())
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if askOp.Request == "" || jobOp != askOp {
		t.Fatalf("job operator %+v, want the ask's %+v", jobOp, askOp)
	}
	if live == nil {
		t.Fatal("job carries no session check")
	}
	if !live(context.Background()) {
		t.Fatal("session check false while signed in")
	}
	// A role change mid-job ends it too: the job acted for a manager.
	if _, err := h.d.Db.Exec(`UPDATE users SET role = 'cashier' WHERE id = 'mgr-1'`); err != nil {
		t.Fatal(err)
	}
	if live(context.Background()) {
		t.Fatal("session check still true after the operator's role changed")
	}
	if _, err := h.d.Db.Exec(`UPDATE users SET role = 'manager' WHERE id = 'mgr-1'`); err != nil {
		t.Fatal(err)
	}
	if !live(context.Background()) {
		t.Fatal("session check false with the role restored")
	}
	h.d.AuthSvc.Logout(context.Background(), token)
	if live(context.Background()) {
		t.Fatal("session check still true after sign-out")
	}
}
