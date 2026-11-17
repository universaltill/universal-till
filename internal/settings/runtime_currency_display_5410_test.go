package settings_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5410: app.Run does Load then Save on every boot; the shop's
// currency display choice must survive that round trip, and a never-set
// shop boots as auto.
func TestBootRoundTrip_PreservesCurrencyDisplay(t *testing.T) {
	ctx := context.Background()
	s := newStore3259(t)

	cfg := cfg3259()
	s.LoadRuntimeConfig(ctx, cfg)
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Get(ctx, common.KeyCurrencyDisplay); v != "auto" {
		t.Fatalf("never-set shop boots with %s = %q, want auto", common.KeyCurrencyDisplay, v)
	}

	if err := s.Set(ctx, common.KeyCurrencyDisplay, "code"); err != nil {
		t.Fatal(err)
	}
	cfg = cfg3259()
	s.LoadRuntimeConfig(ctx, cfg)
	if cfg.Locales.CurrencyDisplay != "code" {
		t.Fatalf("Load CurrencyDisplay = %q, want code", cfg.Locales.CurrencyDisplay)
	}
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Get(ctx, common.KeyCurrencyDisplay); v != "code" {
		t.Fatalf("after boot round trip %s = %q, want code", common.KeyCurrencyDisplay, v)
	}

	// A stored value this build does not know boots as auto.
	if err := s.Set(ctx, common.KeyCurrencyDisplay, "bogus"); err != nil {
		t.Fatal(err)
	}
	cfg = cfg3259()
	s.LoadRuntimeConfig(ctx, cfg)
	if cfg.Locales.CurrencyDisplay != "auto" {
		t.Fatalf("bogus stored value loaded as %q, want auto", cfg.Locales.CurrencyDisplay)
	}
}
