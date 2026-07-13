package ai

import "testing"

func TestFromEnvDefaultsToHaiku(t *testing.T) {
	t.Setenv("UT_AI_API_KEY", "")
	t.Setenv("UT_AI_MODEL", "")
	cfg := FromEnv()
	if cfg.Model != "claude-haiku-4-5" {
		t.Fatalf("default model = %q, want claude-haiku-4-5", cfg.Model)
	}
	if cfg.APIKey != "" {
		t.Fatalf("expected empty key")
	}
}

func TestNoKeyMeansDisabled(t *testing.T) {
	if New(Config{}).Enabled() {
		t.Fatal("service without key must be disabled")
	}
	var nilSvc *Service
	if nilSvc.Enabled() {
		t.Fatal("nil service must be disabled")
	}
	if !New(Config{APIKey: "k", Model: "claude-haiku-4-5"}).Enabled() {
		t.Fatal("service with key must be enabled")
	}
}
