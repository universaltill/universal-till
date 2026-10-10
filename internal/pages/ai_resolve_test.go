package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/bgremove"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Background removal (ut-docs#3126, ai-product-photo.md §4) resolves per
// request: AI plugin image settings (when the plugin is active and
// image_endpoint is set) > UT_AI_IMAGE_* env > off. The plugin's text
// engine runs in its own WASM module (ut-docs#2851), so core resolves only
// the image half. These tests run against a real migrated DB with real
// plugin/settings rows, never a mocked repository.
//
// The env is cleared in every test so the FromEnv fallback is a known
// quantity: whatever the plugin settings resolve to is what we assert on.

func clearAIEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"UT_AI_IMAGE_PROVIDER", "UT_AI_IMAGE_ENDPOINT", "UT_AI_IMAGE_MODEL"} {
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
// the given active state and the UT_AI_IMAGE_* env cleared.
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
// (ADR-0082).
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

// Deps.AI (test/explicit injection) short-circuits everything.
func TestAIResolve_InjectedServiceWins(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "image_endpoint", "http://rembg.local:7000", false)
	injected := &bgremove.Service{}
	dp.AI = injected
	if got := cutoutService(t.Context(), dp); got != injected {
		t.Fatal("Deps.AI must be returned as-is ahead of plugin settings")
	}
}

func TestAIResolve_ImageFromPlugin(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "image_provider", "self_hosted", false)
	setAISetting(t, dp, "image_endpoint", "http://rembg.local:7000", false)
	setAISetting(t, dp, "image_model", "u2netp", false)

	cfg := resolveImageConfig(t.Context(), dp)
	want := bgremove.ImageConfig{Provider: "self_hosted", Endpoint: "http://rembg.local:7000", Model: "u2netp"}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
	if !cutoutService(t.Context(), dp).CanCutout() {
		t.Fatal("image configured: CanCutout must be true")
	}
}

// Rows written before the image settings were reconciled: no image_provider
// row and no image_model — the manifest defaults apply. Text settings rows
// (the plugin's own WASM engine reads them) are ignored by core.
func TestAIResolve_ImageManifestDefaultsIgnoreTextSettings(t *testing.T) {
	dp := newAIResolveDeps(t, true)
	setAISetting(t, dp, "endpoint", "http://ollama.local:11434", false)
	setAISetting(t, dp, "provider", "claude", false)
	setAISetting(t, dp, "image_endpoint", "http://rembg.local:7000", false)
	cfg := resolveImageConfig(t.Context(), dp)
	want := bgremove.ImageConfig{Provider: "self_hosted", Endpoint: "http://rembg.local:7000", Model: bgremove.DefaultImageModel}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

// Resolution order: plugin image settings > UT_AI_IMAGE_* env > off.
func TestAIResolve_ImageResolutionOrder(t *testing.T) {
	// Off: nothing anywhere.
	dp := newAIResolveDeps(t, true)
	if cfg := resolveImageConfig(t.Context(), dp); cfg != (bgremove.ImageConfig{}) {
		t.Fatalf("no plugin image, no env: got %+v, want off", cfg)
	}
	if cutoutService(t.Context(), dp).CanCutout() {
		t.Fatal("no image config anywhere: CanCutout must be false")
	}

	// Env only (plugin image_endpoint empty — the manifest default).
	setAISetting(t, dp, "image_provider", "self_hosted", false)
	setAISetting(t, dp, "image_endpoint", "", false)
	setAISetting(t, dp, "image_model", bgremove.DefaultImageModel, false)
	t.Setenv("UT_AI_IMAGE_ENDPOINT", "http://env-rembg.local:7000")
	envWant := bgremove.ImageConfig{Provider: "self_hosted", Endpoint: "http://env-rembg.local:7000", Model: bgremove.DefaultImageModel}
	if cfg := resolveImageConfig(t.Context(), dp); cfg != envWant {
		t.Fatalf("empty plugin image_endpoint: got %+v, want env %+v", cfg, envWant)
	}

	// Plugin wins over env.
	setAISetting(t, dp, "image_endpoint", "http://plugin-rembg.local:7000", false)
	if cfg := resolveImageConfig(t.Context(), dp); cfg.Endpoint != "http://plugin-rembg.local:7000" {
		t.Fatalf("plugin image_endpoint must win over env, got %+v", cfg)
	}

	// Inactive plugin contributes nothing: env again.
	if _, err := dp.Db.Exec(`UPDATE plugins SET is_active=0 WHERE id=?`, AIPluginID); err != nil {
		t.Fatal(err)
	}
	if cfg := resolveImageConfig(t.Context(), dp); cfg != envWant {
		t.Fatalf("inactive plugin: got %+v, want env %+v", cfg, envWant)
	}
}

// Fail-safe (ADR-0126 §7): an image_provider this build has no adapter for
// is OFF — never a hosted call, and not a silent fall-through to the env
// image config either (the shop chose something; running a different
// service than it chose would be wrong in either direction).
func TestAIResolve_ImageUnknownProviderIsOff(t *testing.T) {
	for _, p := range []string{"remove.bg", "photoroom", "claude", "openai", "Self_Hosted", "self-hosted"} {
		t.Run(p, func(t *testing.T) {
			dp := newAIResolveDeps(t, true)
			t.Setenv("UT_AI_IMAGE_ENDPOINT", "http://env-rembg.local:7000")
			setAISetting(t, dp, "image_provider", p, false)
			setAISetting(t, dp, "image_endpoint", "http://rembg.local:7000", false)
			cfg := resolveImageConfig(t.Context(), dp)
			if cfg.Endpoint == "http://env-rembg.local:7000" {
				t.Fatalf("unknown provider fell through to env: %+v", cfg)
			}
			if cutoutService(t.Context(), dp).CanCutout() {
				t.Fatalf("image_provider=%q must resolve to off", p)
			}
			// Even with an empty plugin endpoint, an explicit unknown
			// provider stays off rather than picking up the env service.
			setAISetting(t, dp, "image_endpoint", "", false)
			if cutoutService(t.Context(), dp).CanCutout() {
				t.Fatalf("image_provider=%q with empty endpoint must stay off, not use env", p)
			}
		})
	}
}

// A model off the licence-checked allow-list (rembg's own bria-rmbg default
// among them) turns the capability off — not the env service instead.
func TestAIResolve_ImageModelOffAllowListIsOff(t *testing.T) {
	for _, m := range []string{"bria-rmbg", "bria-rmbg-2.0", "isnet-general-use"} {
		t.Run(m, func(t *testing.T) {
			dp := newAIResolveDeps(t, true)
			t.Setenv("UT_AI_IMAGE_ENDPOINT", "http://env-rembg.local:7000")
			setAISetting(t, dp, "image_provider", "self_hosted", false)
			setAISetting(t, dp, "image_endpoint", "http://rembg.local:7000", false)
			setAISetting(t, dp, "image_model", m, false)
			if cutoutService(t.Context(), dp).CanCutout() {
				t.Fatalf("image_model=%q must resolve to off", m)
			}
		})
	}
}
