package pages

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/universaltill/universal-till/internal/bgremove"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// AIPluginID is the marketplace AI Assistant plugin (docs:
// architecture/ai-plugin.md). Its WASM module runs camera identify and "Ask
// your till" itself (ut-docs#2851); core only reads its image_* settings for
// host-side background removal (ut-docs#3126).
const AIPluginID = "com.universaltill.integration-ai"

// cutoutService resolves the background-removal backend per request:
//  1. Deps.AI when set (tests / explicit injection),
//  2. the AI plugin's image settings when it is installed + active and
//     configures them,
//  3. UT_AI_IMAGE_* env (the dev/low-level override),
//  4. off.
//
// Two tiny queries on the cutout route only — never on the sale path — so
// plugin install/enable/disable/settings changes apply immediately.
func cutoutService(ctx context.Context, d *common.Deps) *bgremove.Service {
	if d.AI != nil {
		return d.AI
	}
	return bgremove.New(resolveImageConfig(ctx, d))
}

// resolveImageConfig is steps 2–4 of cutoutService's order as a plain
// bgremove.ImageConfig — split out so tests can assert which
// provider/endpoint/model a settings combination resolves to.
func resolveImageConfig(ctx context.Context, d *common.Deps) bgremove.ImageConfig {
	repo := data.NewPluginRepo(d.Db)
	if active, err := repo.PluginActive(ctx, AIPluginID); err == nil && active {
		if cfg, ok := aiPluginConfig(ctx, repo); ok {
			return cfg
		}
	}
	return bgremove.FromEnv()
}

// aiPluginConfig reads the AI plugin's image_* settings rows. ok reports
// whether the plugin's settings decide background removal — see
// aiPluginImageConfig.
func aiPluginConfig(ctx context.Context, repo *data.PluginRepo) (bgremove.ImageConfig, bool) {
	var imageProvider, imageEndpoint, imageModel string
	if rows, err := repo.ListPluginSettings(ctx, AIPluginID); err == nil {
		for _, row := range rows {
			var v string
			if json.Unmarshal([]byte(row.ValueJSON), &v) != nil {
				v = strings.Trim(row.ValueJSON, `"`)
			}
			v = strings.TrimSpace(v)
			switch row.Key {
			case "image_provider":
				imageProvider = v
			case "image_endpoint":
				imageEndpoint = v
			case "image_model":
				imageModel = v
			}
		}
	}
	return aiPluginImageConfig(imageProvider, imageEndpoint, imageModel)
}

// aiPluginImageConfig decides the background-removal config from the
// plugin's image_* settings (ut-docs#3126, ai-product-photo.md §4).
//
//   - image_endpoint set: the plugin decides. An empty image_provider row
//     means the manifest default "self_hosted"; an empty image_model means
//     bgremove.DefaultImageModel. The values are passed through as entered,
//     so bgremove.New applies the fail-safes: any provider other than the
//     exact "self_hosted" is off, and a model off the licence-checked
//     allow-list (bria-rmbg among them) is off.
//   - image_provider explicitly set to anything other than "self_hosted":
//     the plugin decides OFF even with no endpoint. Fail-safe choice: the
//     shop picked a provider this build can't run, so neither that nor a
//     different service from the UT_AI_IMAGE_* env runs in its place.
//   - otherwise (no endpoint, self_hosted or unset): ok=false — the plugin
//     doesn't configure the capability and the env override applies.
func aiPluginImageConfig(provider, endpoint, model string) (bgremove.ImageConfig, bool) {
	if provider == "" {
		provider = bgremove.ImageProviderSelfHosted
	}
	if provider != bgremove.ImageProviderSelfHosted {
		return bgremove.ImageConfig{Provider: provider, Endpoint: endpoint, Model: model}, true
	}
	if endpoint == "" {
		return bgremove.ImageConfig{}, false
	}
	if model == "" {
		model = bgremove.DefaultImageModel
	}
	return bgremove.ImageConfig{Provider: provider, Endpoint: endpoint, Model: model}, true
}
