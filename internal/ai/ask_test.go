package ai

import (
	"context"
	"encoding/json"
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
