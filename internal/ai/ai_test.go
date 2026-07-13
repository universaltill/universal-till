package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Setenv("UT_AI_PROVIDER", "")
	t.Setenv("UT_AI_ENDPOINT", "")
	t.Setenv("UT_AI_MODEL", "")
	t.Setenv("UT_AI_API_KEY", "")
}

func TestFromEnvInfersProvider(t *testing.T) {
	clearEnv(t)
	if cfg := FromEnv(); cfg.Provider != "" {
		t.Fatalf("no env → provider %q, want disabled", cfg.Provider)
	}

	t.Setenv("UT_AI_ENDPOINT", "http://localhost:11434")
	cfg := FromEnv()
	if cfg.Provider != "ollama" || cfg.Model != "llama3.2-vision" {
		t.Fatalf("endpoint → %q/%q, want ollama/llama3.2-vision", cfg.Provider, cfg.Model)
	}

	clearEnv(t)
	t.Setenv("UT_AI_API_KEY", "k")
	cfg = FromEnv()
	if cfg.Provider != "claude" || cfg.Model != "claude-haiku-4-5" {
		t.Fatalf("key → %q/%q, want claude/claude-haiku-4-5", cfg.Provider, cfg.Model)
	}
}

func TestNewDisabledWithoutBackend(t *testing.T) {
	if New(Config{}).Enabled() {
		t.Fatal("no backend must be disabled")
	}
	if New(Config{Provider: "ollama"}).Enabled() {
		t.Fatal("ollama without endpoint must be disabled")
	}
	if New(Config{Provider: "claude"}).Enabled() {
		t.Fatal("claude without key must be disabled")
	}
	var nilSvc *Service
	if nilSvc.Enabled() {
		t.Fatal("nil service must be disabled")
	}
	if !New(Config{Provider: "ollama", Endpoint: "http://x", Model: "m"}).Enabled() {
		t.Fatal("ollama with endpoint must be enabled")
	}
}

func TestOllamaIdentifyAndHallucinationFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "test-model" {
			t.Errorf("model = %v", req["model"])
		}
		content, _ := json.Marshal(IdentifyResult{Matches: []Candidate{
			{ItemID: "itm001", Confidence: "high"},
			{ItemID: "made-up", Confidence: "low"},
		}})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": string(content)},
		})
	}))
	defer srv.Close()

	svc := New(Config{Provider: "ollama", Endpoint: srv.URL, Model: "test-model"})
	res, err := svc.Identify(context.Background(), []byte{1}, "image/jpeg",
		[]CatalogItem{{ID: "itm001", SKU: "S1", Name: "Milk"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 || res.Matches[0].ItemID != "itm001" {
		t.Fatalf("hallucinated id must be filtered; got %+v", res.Matches)
	}
}
