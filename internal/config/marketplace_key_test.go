package config

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// ADR-0158: a key compiled into the binary is the only marketplace key —
// UT_MARKETPLACE_PUBLIC_KEY is ignored when one is set.
func TestInitCompiledMarketplaceKeyWinsOverEnv(t *testing.T) {
	unsetForTest(t, configEnvKeys)
	compiled := strings.Repeat("ab", 32)
	orig := buildinfo.MarketplacePublicKey
	buildinfo.MarketplacePublicKey = compiled
	t.Cleanup(func() { buildinfo.MarketplacePublicKey = orig })

	t.Setenv("UT_MARKETPLACE_PUBLIC_KEY", strings.Repeat("cd", 32))
	cfg, err := Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if cfg.Marketplace.PublicKey != compiled {
		t.Fatalf("PublicKey = %q, want the compiled key %q", cfg.Marketplace.PublicKey, compiled)
	}
}

// A source build (no compiled key) keeps reading the env var.
func TestInitEnvMarketplaceKeyWithoutCompiledKey(t *testing.T) {
	unsetForTest(t, configEnvKeys)
	orig := buildinfo.MarketplacePublicKey
	buildinfo.MarketplacePublicKey = ""
	t.Cleanup(func() { buildinfo.MarketplacePublicKey = orig })

	env := strings.Repeat("cd", 32)
	t.Setenv("UT_MARKETPLACE_PUBLIC_KEY", env)
	cfg, err := Init()
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if cfg.Marketplace.PublicKey != env {
		t.Fatalf("PublicKey = %q, want the env key", cfg.Marketplace.PublicKey)
	}
}
