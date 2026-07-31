package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func askService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{Provider: "ollama", Endpoint: srv.URL, Model: "vision", AskModel: "text"})
}

func testTools(t *testing.T, gotArgs *map[string]any) []AskTool {
	t.Helper()
	return []AskTool{{
		Name:        "sales_by_day",
		Description: "daily sales",
		Params:      map[string]any{"type": "object"},
		Run: func(_ context.Context, args map[string]any) (any, error) {
			*gotArgs = args
			return []map[string]any{{"day": "2026-07-14", "total": 12345}}, nil
		},
	}}
}

func TestAskToolLoop(t *testing.T) {
	var gotArgs map[string]any
	call := 0
	svc := askService(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		var req struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "text" {
			t.Errorf("ask used model %q, want the ask model", req.Model)
		}
		switch call {
		case 1:
			if len(req.Tools) != 1 {
				t.Errorf("tools not sent: %+v", req.Tools)
			}
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"sales_by_day","arguments":{"days":7}}}]}}`))
		case 2:
			// The tool result must have come back as a role:"tool" message.
			last := req.Messages[len(req.Messages)-1]
			if last["role"] != "tool" || !strings.Contains(last["content"].(string), "12345") {
				t.Errorf("tool result not in messages: %+v", last)
			}
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"You took £123.45 today."}}`))
		default:
			t.Fatalf("unexpected extra round %d", call)
		}
	})
	if !svc.CanAsk() {
		t.Fatal("ollama service should support ask")
	}
	answer, err := svc.Ask(context.Background(), "how did we do today?", ShopContext{StoreName: "Test", CurrencyCode: "GBP", CurrencyDecimals: 2}, testTools(t, &gotArgs))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(answer, "123.45") {
		t.Errorf("answer = %q", answer)
	}
	if gotArgs["days"] != float64(7) {
		t.Errorf("tool args = %+v", gotArgs)
	}
}

func TestAskLoopBounded(t *testing.T) {
	svc := askService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"sales_by_day","arguments":{}}}]}}`))
	})
	var gotArgs map[string]any
	if _, err := svc.Ask(context.Background(), "loop forever", ShopContext{}, testTools(t, &gotArgs)); err == nil {
		t.Fatal("unbounded tool loop should error")
	}
}

func TestAskUnknownToolRecovers(t *testing.T) {
	call := 0
	svc := askService(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"nope","arguments":{}}}]}}`))
			return
		}
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1]
		if !strings.Contains(last["content"].(string), "unknown tool") {
			t.Errorf("model not told about unknown tool: %+v", last)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Sorry, I cannot check that."}}`))
	})
	var gotArgs map[string]any
	if _, err := svc.Ask(context.Background(), "q", ShopContext{}, testTools(t, &gotArgs)); err != nil {
		t.Fatalf("Ask should recover from unknown tool: %v", err)
	}
}

func TestCanAsk(t *testing.T) {
	if (&Service{}).CanAsk() {
		t.Error("disabled service must not offer ask")
	}
	if New(Config{Provider: "claude", APIKey: "k", Model: "m"}).CanAsk() {
		t.Error("claude provider has no ask loop yet — must report false")
	}
}

// Ask's guards: no question, a provider without the tool loop, a chat-layer
// failure, an empty final answer, and a tool whose result can't be
// serialised — all plain errors or model-visible "error:" strings.
func TestAskGuards(t *testing.T) {
	svc := askService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"hi"}}`))
	})
	if _, err := svc.Ask(context.Background(), "", ShopContext{}, nil); err == nil {
		t.Fatal("empty question must error")
	}

	claude := New(Config{Provider: "claude", APIKey: "k", Model: "m"})
	if _, err := claude.Ask(context.Background(), "q", ShopContext{}, nil); err == nil {
		t.Fatal("provider without a tool loop must error")
	}
}

func TestAskChatFailures(t *testing.T) {
	var gotArgs map[string]any

	svc := askService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("overloaded"))
	})
	if _, err := svc.Ask(context.Background(), "q", ShopContext{}, testTools(t, &gotArgs)); err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("chat http error must surface, got %v", err)
	}

	svc = askService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	if _, err := svc.Ask(context.Background(), "q", ShopContext{}, testTools(t, &gotArgs)); err == nil || !strings.Contains(err.Error(), "parse ollama response") {
		t.Fatalf("chat parse error must surface, got %v", err)
	}

	svc = askService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"   "}}`))
	})
	if _, err := svc.Ask(context.Background(), "q", ShopContext{}, testTools(t, &gotArgs)); err == nil || !strings.Contains(err.Error(), "empty model response") {
		t.Fatalf("blank final answer must error, got %v", err)
	}
}

// A tool that errors, and a tool whose result json.Marshal rejects, both go
// back to the model as "error:" text so it can recover — the loop never
// aborts on a tool problem.
func TestAskToolFailuresReturnToModel(t *testing.T) {
	failing := []AskTool{
		{
			Name: "boom", Description: "always fails", Params: map[string]any{"type": "object"},
			Run: func(context.Context, map[string]any) (any, error) { return nil, fmt.Errorf("db locked") },
		},
		{
			Name: "unserialisable", Description: "bad result", Params: map[string]any{"type": "object"},
			Run: func(context.Context, map[string]any) (any, error) { return func() {}, nil },
		},
	}
	call := 0
	svc := askService(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"boom","arguments":{}}},{"function":{"name":"unserialisable","arguments":{}}}]}}`))
			return
		}
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		toolMsgs := req.Messages[len(req.Messages)-2:]
		for i, m := range toolMsgs {
			if m["role"] != "tool" || !strings.HasPrefix(m["content"].(string), "error:") {
				t.Errorf("tool failure %d not reported to model: %+v", i, m)
			}
		}
		if !strings.Contains(toolMsgs[0]["content"].(string), "db locked") {
			t.Errorf("tool error text lost: %+v", toolMsgs[0])
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Sorry, the data is unavailable."}}`))
	})
	answer, err := svc.Ask(context.Background(), "q", ShopContext{}, failing)
	if err != nil {
		t.Fatalf("loop must recover from tool failures: %v", err)
	}
	if !strings.Contains(answer, "unavailable") {
		t.Fatalf("answer = %q", answer)
	}
}

// Endpoint problems (a malformed URL, an unreachable server) surface as
// errors from the ask loop's chat layer too.
func TestAskEndpointFailures(t *testing.T) {
	var gotArgs map[string]any
	bad := New(Config{Provider: "ollama", Endpoint: "http://bad url", Model: "v", AskModel: "t"})
	if _, err := bad.Ask(context.Background(), "q", ShopContext{}, testTools(t, &gotArgs)); err == nil {
		t.Fatal("malformed endpoint must error")
	}
	down := New(Config{Provider: "ollama", Endpoint: "http://127.0.0.1:1", Model: "v", AskModel: "t"})
	if _, err := down.Ask(context.Background(), "q", ShopContext{}, testTools(t, &gotArgs)); err == nil {
		t.Fatal("unreachable endpoint must error")
	}
}
