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
	repo := data.NewPluginRepo(d.Db)
	if active, err := repo.PluginActive(ctx, AIPluginID); err == nil && active {
		cfg := ai.Config{Provider: "ollama"}
		if rows, err := repo.ListPluginSettings(ctx, AIPluginID); err == nil {
			for _, row := range rows {
				var v string
				if json.Unmarshal([]byte(row.ValueJSON), &v) != nil {
					v = strings.Trim(row.ValueJSON, `"`)
				}
				switch row.Key {
				case "endpoint":
					cfg.Endpoint = strings.TrimSpace(v)
				case "vision_model":
					cfg.Model = strings.TrimSpace(v)
				case "ask_model":
					cfg.AskModel = strings.TrimSpace(v)
				}
			}
		}
		if cfg.Model == "" {
			cfg.Model = "llama3.2-vision"
		}
		if cfg.AskModel == "" {
			cfg.AskModel = "llama3.2"
		}
		if cfg.Endpoint != "" {
			return ai.New(cfg)
		}
	}
	return ai.New(ai.FromEnv())
}
