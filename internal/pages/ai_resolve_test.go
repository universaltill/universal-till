package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0085 (ut-docs#1708): aiService's resolution order — AI plugin
// settings > UT_AI_* env > disabled — gains a `provider` setting that lets a
// shop opt into the hosted Claude backend with its OWN key. These tests run
// against a real migrated DB with real plugin/settings rows (the api_key is
// written through the ADR-0082 seal/open path, exactly as the settings page
// writes it), never a mocked repository.
//
// The env is cleared in every test so the FromEnv fallback is a known
// quantity: whatever the plugin settings resolve to is what we assert on.

func clearAIEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"UT_AI_PROVIDER", "UT_AI_ENDPOINT", "UT_AI_MODEL", "UT_AI_ASK_MODEL", "UT_AI_API_KEY"} {
		t.Setenv(k, "")
	}
}

// seedAIPluginRows installs the AI plugin in the given active state —
// plugin_catalog + plugins rows only (the plugin is runtime:none, it has no
// files), the same shape seedForPages uses for p1. plugin_settings has a
// foreign key to plugins, so this must precede any setAISetting.
func seedAIPluginRows(t *testing.T, db *sql.DB, active bool) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO plugin_catalog(id,version,name,description,runtime,entrypoint,package_url,sha256,author,website,tags_json,is_deprecated,min_pos_version,api_version,published_at) VALUES(?,'1.1.0','AI Assistant','desc','none','','url','sha','Universal Till','site','[]',0,'1.0.0','1',datetime('now'))`, AIPluginID); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	isActive := 0
	if active {
		isActive = 1
	}
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,entrypoint,runtime,is_active) VALUES(?,'AI Assistant','1.1.0','','none',?)`, AIPluginID, isActive); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
}

// newAIResolveDeps opens a real migrated DB with the AI plugin installed in
// the given active state and the UT_AI_* env cleared.
func newAIResolveDeps(t *testing.T, active bool) *common.Deps {
	t.Helper()
	clearAIEnv(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedAIPluginRows(t, db, active)
	return &common.Deps{Db: db}
}

// setAISetting writes one AI plugin setting the way the settings page does:
// JSON-string encoded, global scope, sealed at rest when declaredSecret
// (api_key is `type: "secret"` in the plugin manifest, ADR-0082).
func setAISetting(t *testing.T, dp *common.Deps, key, value string, declaredSecret bool) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.NewPluginRepo(dp.Db).UpsertPluginSettingScoped(context.Background(), AIPluginID, key, string(raw), "global", declaredSecret); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

// Pins the pre-ADR-0085 behavior so the provider branch can't regress it:
// an installed-but-disabled plugin contributes nothing, and the UT_AI_* env
// remains the developer override underneath.
func TestAIResolve_PluginInactiveFallsBackToEnv(t *testing.T) {
	dp := newAIResolveDeps(t, false)
	setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)
	setAISetting(t, dp, "provider", "claude", false)
	setAISetting(t, dp, "api_key", "sk-ant-shop", true)

	if cfg := resolveAIConfig(t.Context(), dp); cfg.Provider != "" {
		t.Fatalf("inactive plugin + no env: provider %q, want disabled", cfg.Provider)
	}
	if aiService(t.Context(), dp).Enabled() {
		t.Fatal("inactive plugin + no env must resolve to a disabled service")
	}

	t.Setenv("UT_AI_ENDPOINT", "http://env.local:11434")
	cfg := resolveAIConfig(t.Context(), dp)
	if cfg.Provider != "ollama" || cfg.Endpoint != "http://env.local:11434" {
		t.Fatalf("inactive plugin + env endpoint: got %+v, want the env ollama config", cfg)
	}
	if !aiService(t.Context(), dp).Enabled() {
		t.Fatal("env override must still enable the service when the plugin is inactive")
	}
}

// Every installation that predates the provider setting (no `provider` row at
// all) and every one that explicitly says self_hosted must resolve to the
// exact Ollama config they got before — ADR-0085 Non-goals: byte-for-byte.
func TestAIResolve_UnsetOrSelfHostedProviderKeepsOllama(t *testing.T) {
	for name, provider := range map[string]string{"unset": "", "self_hosted": "self_hosted"} {
		t.Run(name, func(t *testing.T) {
			dp := newAIResolveDeps(t, true)
			setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)
			if provider != "" {
				setAISetting(t, dp, "provider", provider, false)
			}
			// A stray key must not flip a self-hosted shop to a hosted vendor.
			setAISetting(t, dp, "api_key", "sk-ant-left-over", true)

			cfg := resolveAIConfig(t.Context(), dp)
			want := ai.Config{Provider: "ollama", Endpoint: "http://ollama.local:11434", Model: "llama3.2-vision", AskModel: "llama3.2"}
			if cfg != want {
				t.Fatalf("got %+v, want %+v", cfg, want)
			}
			svc := aiService(t.Context(), dp)
			if !svc.Enabled() || !svc.CanAsk() {
				t.Fatalf("self-hosted with an endpoint must be enabled with ask: enabled=%v canAsk=%v", svc.Enabled(), svc.CanAsk())
			}
		})
	}
}

// Same unchanged branch, the other way: self-hosted with no endpoint is
// "not configured" and falls through to the env override (today's shape).
func TestAIResolve_SelfHostedWithoutEndpointFallsThroughToEnv(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "endpoint", "", false)
	if aiService(t.Context(), dp).Enabled() {
		t.Fatal("self-hosted with an empty endpoint and no env must be disabled")
	}
	t.Setenv("UT_AI_ENDPOINT", "http://env.local:11434")
	if cfg := resolveAIConfig(t.Context(), dp); cfg.Provider != "ollama" || cfg.Endpoint != "http://env.local:11434" {
		t.Fatalf("empty plugin endpoint must fall through to the env override, got %+v", cfg)
	}
}

// ADR-0085 Decision 2: provider=claude with a key resolves to the existing
// claudeProvider — the key arrives already opened from its sealed row, the
// model defaults to the same value the UT_AI_* env path uses, and an
// explicit vision_model overrides it. Claude wins over a sibling Ollama
// endpoint the shop may still have configured from before the switch.
func TestAIResolve_ClaudeWithKeySelectsClaude(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "provider", "claude", false)
	setAISetting(t, dp, "api_key", "sk-ant-shop-own-key", true)
	setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)

	cfg := resolveAIConfig(t.Context(), dp)
	want := ai.Config{Provider: "claude", APIKey: "sk-ant-shop-own-key", Model: ai.DefaultClaudeModel}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
	svc := aiService(t.Context(), dp)
	if !svc.Enabled() {
		t.Fatal("claude with a key must be enabled")
	}
	// The hosted backend has no ask loop today (internal/ai/ask_test.go pins
	// CanAsk false for claude): Ask-your-till stays hidden rather than
	// erroring — the existing degrade path, reached a new way.
	if svc.CanAsk() {
		t.Fatal("claude has no ask loop yet — CanAsk must be false so the UI hides Ask-your-till")
	}

	setAISetting(t, dp, "vision_model", "claude-sonnet-4-5", false)
	if cfg := resolveAIConfig(t.Context(), dp); cfg.Model != "claude-sonnet-4-5" || cfg.Provider != "claude" {
		t.Fatalf("vision_model must name the claude model, got %+v", cfg)
	}
}

// Whitespace around an otherwise-exact "claude" is a stray space, not a
// different provider — aiPluginConfig trims every setting value the same
// way endpoint/vision_model/ask_model already were before this card, so
// " claude " DOES select the hosted vendor (review finding, ut-docs#1708:
// pin this deliberately rather than leaving it as an untested side effect
// of the shared trim, since it's the one input that resolves forward to a
// paid API without being byte-identical to the literal ADR-0085 wording).
func TestAIResolve_WhitespacePaddedClaudeStillSelectsHosted(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "provider", "  claude  ", false)
	setAISetting(t, dp, "api_key", "sk-ant-shop-own-key", true)

	cfg := resolveAIConfig(t.Context(), dp)
	want := ai.Config{Provider: "claude", APIKey: "sk-ant-shop-own-key", Model: ai.DefaultClaudeModel}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

// provider=claude with no key is "not configured", not "use Ollama instead":
// the shop chose a hosted vendor, so silently running its catalog against a
// leftover Ollama endpoint would do something it didn't ask for. It falls
// through to the env override exactly like the empty-endpoint branch, and
// with no env that means disabled.
func TestAIResolve_ClaudeWithoutKeyIsDisabledNotOllama(t *testing.T) {
	for name, seedKey := range map[string]bool{"no_row": false, "empty_row": true} {
		t.Run(name, func(t *testing.T) {
			dp := newAIResolveDeps(t, true)
			setAISetting(t, dp, "provider", "claude", false)
			setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)
			if seedKey {
				setAISetting(t, dp, "api_key", "", true)
			}
			if cfg := resolveAIConfig(t.Context(), dp); cfg.Provider != "" {
				t.Fatalf("claude without a key resolved to %+v, want disabled (never the sibling ollama endpoint)", cfg)
			}
			if aiService(t.Context(), dp).Enabled() {
				t.Fatal("claude without a key must be a disabled service")
			}
		})
	}
}

// The fail-safe ADR-0085 gap 3 requires, and the test that must fail against
// a naive "anything that isn't self_hosted is hosted" implementation: only
// the exact values "claude" or "openai" may select a paid vendor (ut-docs#1791
// added the second). A typo, a different case, or any other string resolves
// to the self-hosted path — Ollama when an endpoint exists, disabled when
// not — even when a key is sitting right there in api_key.
func TestAIResolve_UnrecognizedProviderNeverSelectsHosted(t *testing.T) {
	for _, provider := range []string{"Claude", "CLAUDE", "claud", "anthropic", "hosted", "claude-haiku-4-5", "Openai", "OPENAI", "open_ai", "gpt"} {
		t.Run(provider, func(t *testing.T) {
			dp := newAIResolveDeps(t, true)
			setAISetting(t, dp, "provider", provider, false)
			setAISetting(t, dp, "api_key", "sk-ant-should-never-be-used", true)

			// With an endpoint: the shop's own Ollama, never Claude.
			setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)
			cfg := resolveAIConfig(t.Context(), dp)
			if cfg.Provider != "ollama" || cfg.APIKey != "" {
				t.Fatalf("provider=%q resolved to %+v, want the self-hosted ollama config with no key", provider, cfg)
			}

			// Without one: disabled — not Claude just because a key exists.
			setAISetting(t, dp, "endpoint", "", false)
			cfg = resolveAIConfig(t.Context(), dp)
			if cfg.Provider != "" || cfg.APIKey != "" {
				t.Fatalf("provider=%q with no endpoint resolved to %+v, want disabled", provider, cfg)
			}
			if aiService(t.Context(), dp).Enabled() {
				t.Fatalf("provider=%q with no endpoint must be a disabled service, never a hosted one", provider)
			}
		})
	}
}

// ut-docs#1791: provider=openai with a key resolves to the new openai
// backend — key already opened from its sealed row, vision_model AND
// ask_model both default to ai.DefaultOpenAIModel (one model covers both
// capabilities for OpenAI, unlike Ollama's split), and either default is
// independently overridable. OpenAI wins over a sibling Ollama endpoint the
// shop may still have configured from before the switch.
func TestAIResolve_OpenAIWithKeySelectsOpenAI(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "provider", "openai", false)
	setAISetting(t, dp, "api_key", "sk-openai-shop-own-key", true)
	setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)

	cfg := resolveAIConfig(t.Context(), dp)
	want := ai.Config{Provider: "openai", APIKey: "sk-openai-shop-own-key", Model: ai.DefaultOpenAIModel, AskModel: ai.DefaultOpenAIModel}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
	svc := aiService(t.Context(), dp)
	if !svc.Enabled() {
		t.Fatal("openai with a key must be enabled")
	}
	// Unlike claude, openai's ask loop is real (ut-docs#1791) — Ask-your-till
	// must NOT hide itself for this provider.
	if !svc.CanAsk() {
		t.Fatal("openai has a real ask loop — CanAsk must be true")
	}

	setAISetting(t, dp, "vision_model", "gpt-4o", false)
	if cfg := resolveAIConfig(t.Context(), dp); cfg.Model != "gpt-4o" || cfg.AskModel != ai.DefaultOpenAIModel || cfg.Provider != "openai" {
		t.Fatalf("vision_model must override independently of ask_model, got %+v", cfg)
	}

	setAISetting(t, dp, "ask_model", "gpt-4o-mini-2024-07-18", false)
	if cfg := resolveAIConfig(t.Context(), dp); cfg.AskModel != "gpt-4o-mini-2024-07-18" || cfg.Model != "gpt-4o" {
		t.Fatalf("ask_model must override independently of vision_model, got %+v", cfg)
	}
}

// provider=openai with no key is "not configured", not "use Ollama instead" —
// same fail-safe posture as the claude branch.
func TestAIResolve_OpenAIWithoutKeyIsDisabledNotOllama(t *testing.T) {
	for name, seedKey := range map[string]bool{"no_row": false, "empty_row": true} {
		t.Run(name, func(t *testing.T) {
			dp := newAIResolveDeps(t, true)
			setAISetting(t, dp, "provider", "openai", false)
			setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)
			if seedKey {
				setAISetting(t, dp, "api_key", "", true)
			}
			if cfg := resolveAIConfig(t.Context(), dp); cfg.Provider != "" {
				t.Fatalf("openai without a key resolved to %+v, want disabled (never the sibling ollama endpoint)", cfg)
			}
			if aiService(t.Context(), dp).Enabled() {
				t.Fatal("openai without a key must be a disabled service")
			}
		})
	}
}

// Deps.AI (test/explicit injection) still short-circuits everything.
func TestAIResolve_InjectedServiceWins(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "provider", "claude", false)
	setAISetting(t, dp, "api_key", "sk-ant-shop", true)
	injected := &ai.Service{}
	dp.AI = injected
	if got := aiService(t.Context(), dp); got != injected {
		t.Fatal("Deps.AI must be returned as-is ahead of plugin settings")
	}
}
