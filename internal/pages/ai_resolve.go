package pages

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// AIPluginID is the marketplace plugin that switches AI on for a shop
// (docs: architecture/ai-plugin.md). No plugin and no env = AI invisible.
const AIPluginID = "com.universaltill.integration-ai"

// aiService resolves the AI backend per request:
//  1. Deps.AI when set (tests / explicit injection),
//  2. the AI plugin's settings when it is installed + active,
//  3. UT_AI_* env (the dev/low-level override),
//  4. disabled.
//
// Two tiny queries on AI routes only — never on the sale path — so plugin
// install/enable/disable/settings changes apply immediately, no reloads.
func aiService(ctx context.Context, d *common.Deps) *ai.Service {
	if d.AI != nil {
		return d.AI
	}
	return ai.New(resolveAIConfig(ctx, d))
}

// resolveAIConfig is steps 2–4 of aiService's order as a plain ai.Config —
// split out so tests can assert which backend/model/key a settings
// combination resolves to (ai.Service deliberately keeps its provider
// private). An installed-but-unconfigured plugin falls through to the env
// override, the same shape the endpoint-empty branch has always had.
func resolveAIConfig(ctx context.Context, d *common.Deps) ai.Config {
	repo := data.NewPluginRepo(d.Db)
	if active, err := repo.PluginActive(ctx, AIPluginID); err == nil && active {
		if cfg, ok := aiPluginConfig(ctx, repo); ok {
			return cfg
		}
	}
	return ai.FromEnv()
}

// aiPluginConfig builds the backend config from the AI plugin's settings
// rows (ADR-0085). ok=false means "installed but not usably configured" —
// self-hosted with no endpoint, or the hosted provider with no key.
func aiPluginConfig(ctx context.Context, repo *data.PluginRepo) (ai.Config, bool) {
	var provider, endpoint, visionModel, askModel, apiKey string
	if rows, err := repo.ListPluginSettings(ctx, AIPluginID); err == nil {
		for _, row := range rows {
			var v string
			if json.Unmarshal([]byte(row.ValueJSON), &v) != nil {
				v = strings.Trim(row.ValueJSON, `"`)
			}
			v = strings.TrimSpace(v)
			switch row.Key {
			case "provider":
				provider = v
			case "endpoint":
				endpoint = v
			case "vision_model":
				visionModel = v
			case "ask_model":
				askModel = v
			case "api_key":
				// Arrives already opened from its sealed row (ADR-0082
				// ListPluginSettings); an unopenable row lists as "".
				apiKey = v
			}
		}
	}
	// ADR-0085 Decision 2, fail-safe direction (extended by ut-docs#1791):
	// ONLY the exact values "claude" or "openai" select a hosted vendor.
	// Unset, "self_hosted", a typo, a different case, or any other value
	// this build doesn't recognize all take the self-hosted branch below, so
	// a misconfiguration can only ever fall back to the shop's own hardware,
	// never forward to a paid API with whatever key happens to be stored.
	// Three outcomes now share that posture: self_hosted (default) stays
	// Ollama; claude gets identify only (no ask loop yet, ut-docs#1792);
	// openai gets both identify and ask (ut-docs#1791) — everything else
	// falls through to self-hosted exactly like the unset case.
	if provider == "claude" {
		if apiKey == "" {
			// The shop chose a hosted vendor but hasn't entered its key:
			// "not configured", same as an empty endpoint below — never
			// "use the leftover Ollama endpoint instead".
			return ai.Config{}, false
		}
		if visionModel == "" {
			visionModel = ai.DefaultClaudeModel
		}
		// vision_model doubles as the Claude model name (same meaning, new
		// value space) rather than a fourth setting. No AskModel: the claude
		// provider has no ask loop yet, so Ask-your-till hides itself
		// (Service.CanAsk) — the existing degrade path, reached a new way.
		return ai.Config{Provider: "claude", APIKey: apiKey, Model: visionModel}, true
	}
	if provider == "openai" {
		if apiKey == "" {
			// Same fail-safe posture as claude: "not configured", never
			// "use the leftover Ollama endpoint instead".
			return ai.Config{}, false
		}
		if visionModel == "" {
			visionModel = ai.DefaultOpenAIModel
		}
		if askModel == "" {
			askModel = ai.DefaultOpenAIModel
		}
		// vision_model/ask_model are reused exactly as they are for ollama —
		// same meaning ("which model this capability uses"), new value
		// space — rather than adding a fourth/fifth setting. Unlike claude,
		// openai's tool-calling ask loop is real (ut-docs#1791), so
		// Ask-your-till stays available on this provider.
		return ai.Config{Provider: "openai", APIKey: apiKey, Model: visionModel, AskModel: askModel}, true
	}
	cfg := ai.Config{Provider: "ollama", Endpoint: endpoint, Model: visionModel, AskModel: askModel}
	if cfg.Model == "" {
		cfg.Model = "llama3.2-vision"
	}
	if cfg.AskModel == "" {
		cfg.AskModel = "llama3.2"
	}
	return cfg, cfg.Endpoint != ""
}
