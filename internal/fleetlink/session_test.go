package fleetlink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A Session runs another protocol's message set (ADR-0117's cloud link)
// over this package's transport and peer loop: our own hello payload goes
// out first, the far side's raw hello and the declared one-way types come
// back through the handlers, Notify queues a frame, and End reports the
// far side's close code.
func TestSessionCarriesAForeignMessageSet(t *testing.T) {
	type gotReq struct {
		auth, origin, query string
	}
	reqs := make(chan gotReq, 1)
	frames := make(chan Envelope, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs <- gotReq{r.Header.Get("Authorization"), r.Header.Get("Origin"), r.URL.RawQuery}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		send := func(typ string, payload any) {
			m, _ := newMessage("c-"+typ, typ, "", payload)
			b, _ := Encode(m)
			_ = ws.Write(ctx, websocket.MessageText, b)
		}
		send(TypeHello, map[string]any{"link_version": 7, "live_view": true})
		send("nudge", map[string]any{"link_version": 8, "scopes": []string{"directives"}})
		send("mystery", nil) // not declared one-way: answered unknown_type, not delivered
		for i := 0; i < 3; i++ {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			env, _ := Decode(b)
			frames <- env
			if env.Type == "sale" {
				break
			}
		}
		_ = ws.Close(websocket.StatusCode(4011), "not_main_till")
	}))
	defer srv.Close()

	var mu sync.Mutex
	var helloRaw json.RawMessage
	var oneWay []string
	s, err := DialSession(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/link?store_id=s1", "cred-1", fastConfig(), SessionHandlers{
		Hello:   func(context.Context) any { return map[string]any{"device_id": "dev-1", "link_version": 3} },
		OneWay:  []string{"nudge", "live_view"},
		OnHello: func(raw json.RawMessage) { mu.Lock(); helloRaw = raw; mu.Unlock() },
		OnMessage: func(env Envelope) {
			mu.Lock()
			oneWay = append(oneWay, env.Type)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("DialSession: %v", err)
	}
	r := <-reqs
	if r.auth != "Bearer cred-1" || r.origin != "" || r.query != "store_id=s1" {
		t.Fatalf("upgrade request = %+v, want the bearer in the header, no Origin, the query kept", r)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.Run() }()

	first := <-frames
	if first.Type != TypeHello || !strings.Contains(string(first.Payload), `"device_id":"dev-1"`) {
		t.Fatalf("first frame = %s %s, want our own hello payload", first.Type, first.Payload)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(oneWay)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Notify("sale", map[string]any{"id": "s-1"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end after the far side closed")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(string(helloRaw), `"link_version":7`) {
		t.Fatalf("OnHello raw = %s, want the far side's own hello payload", helloRaw)
	}
	if len(oneWay) != 1 || oneWay[0] != "nudge" {
		t.Fatalf("one-way frames delivered = %v, want [nudge] only", oneWay)
	}
	if end := s.End(); end.RemoteCode != 4011 || end.PeerBye != "" {
		t.Fatalf("End = %+v, want RemoteCode 4011 and no bye", end)
	}
}

// Close says bye with the given reason before closing, and a far-side bye
// is reported by End.
func TestSessionByeBothWays(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if env, _ := Decode(b); env.Type == TypeBye {
				got <- string(env.Payload)
				return
			}
		}
	}))
	defer srv.Close()
	s, err := DialSession(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), "c", fastConfig(), SessionHandlers{
		Hello: func(context.Context) any { return map[string]any{} },
	})
	if err != nil {
		t.Fatalf("DialSession: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.Run() }()
	s.Close(ByeShutdown)
	select {
	case p := <-got:
		if !strings.Contains(p, `"shutdown"`) {
			t.Fatalf("bye payload = %s, want reason shutdown", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no bye reached the far side")
	}
	<-done

	// Far-side bye.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		m, _ := newMessage("c-1", TypeBye, "", ByePayload{Reason: "deploying"})
		b, _ := Encode(m)
		_ = ws.Write(context.Background(), websocket.MessageText, b)
		time.Sleep(200 * time.Millisecond)
		_ = ws.CloseNow()
	}))
	defer srv2.Close()
	s2, err := DialSession(context.Background(), "ws"+strings.TrimPrefix(srv2.URL, "http"), "c", fastConfig(), SessionHandlers{
		Hello: func(context.Context) any { return map[string]any{} },
	})
	if err != nil {
		t.Fatalf("DialSession: %v", err)
	}
	s2.Run()
	if end := s2.End(); end.PeerBye != "deploying" {
		t.Fatalf("End = %+v, want PeerBye deploying", end)
	}
}

// A refused upgrade surfaces as *DialError with the status and Retry-After.
func TestDialSessionRefusalIsDialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := DialSession(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), "c", fastConfig(), SessionHandlers{
		Hello: func(context.Context) any { return nil },
	})
	de, ok := err.(*DialError)
	if !ok || de.Status != http.StatusServiceUnavailable || de.RetryAfter != 2*time.Minute {
		t.Fatalf("err = %#v, want *DialError 503 with RetryAfter 2m", err)
	}
}

// A Session answers the request types its Requests map names (ADR-0155
// §1): the handler runs off the reader goroutine, a Reply result names the
// reply's own type (sale_detail for sale_detail_request) with reply_to the
// request's id, a *RemoteError result is the error reply, and an
// undeclared type stays unknown_type.
func TestSessionAnswersDeclaredRequests(t *testing.T) {
	replies := make(chan Envelope, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		send := func(id, typ string, payload any) {
			m, _ := newMessage(id, typ, "", payload)
			b, _ := Encode(m)
			_ = ws.Write(ctx, websocket.MessageText, b)
		}
		send("c-h", TypeHello, map[string]any{})
		send("c-1", "thing_request", map[string]any{"id": "a"})
		send("c-2", "thing_request", map[string]any{"id": "missing"})
		send("c-3", "plain_request", nil)
		send("c-4", "other_request", nil)
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if env, _ := Decode(b); env.ReplyTo != "" {
				replies <- env
			}
		}
	}))
	defer srv.Close()

	s, err := DialSession(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), "c", fastConfig(), SessionHandlers{
		Hello: func(context.Context) any { return map[string]any{} },
		Requests: map[string]RequestHandler{
			"thing_request": func(_ context.Context, _ string, payload json.RawMessage) (any, error) {
				var p struct{ ID string }
				_ = json.Unmarshal(payload, &p)
				if p.ID == "missing" {
					return nil, &RemoteError{Code: "not_found"}
				}
				return Reply{Type: "thing", Payload: map[string]string{"id": p.ID}}, nil
			},
			"plain_request": func(context.Context, string, json.RawMessage) (any, error) {
				return map[string]bool{"ok": true}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("DialSession: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.Run() }()
	defer func() { s.Close(""); <-done }()

	got := map[string]Envelope{}
	for len(got) < 4 {
		select {
		case e := <-replies:
			got[e.ReplyTo] = e
		case <-time.After(3 * time.Second):
			t.Fatalf("replies so far: %+v", got)
		}
	}
	if e := got["c-1"]; e.Type != "thing" || string(e.Payload) != `{"id":"a"}` {
		t.Fatalf("reply to c-1 = %s %s, want type thing with the handler's payload", e.Type, e.Payload)
	}
	if e := got["c-2"]; e.Type != TypeError || string(e.Payload) != `{"code":"not_found","retryable":false}` {
		t.Fatalf("reply to c-2 = %s %s, want error not_found", e.Type, e.Payload)
	}
	// A plain result keeps ADR-0114's reply-of-the-request's-type rule.
	if e := got["c-3"]; e.Type != "plain_request" || string(e.Payload) != `{"ok":true}` {
		t.Fatalf("reply to c-3 = %s %s, want the request's own type", e.Type, e.Payload)
	}
	if e := got["c-4"]; e.Type != TypeError || !strings.Contains(string(e.Payload), CodeUnknownType) {
		t.Fatalf("reply to c-4 = %s %s, want unknown_type", e.Type, e.Payload)
	}
}
