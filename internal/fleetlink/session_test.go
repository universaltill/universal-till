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
