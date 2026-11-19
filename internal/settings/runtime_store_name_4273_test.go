package settings

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#4273: migration 075 blanks the seeded "My Store", and app.Run's
// boot round-trip (LoadRuntimeConfig then SaveRuntimeConfig) must not write
// the UT_STORE_NAME default straight back over it: Load ignores a blank
// store.name, so cfg.StoreName is still config.DefaultStoreName at Save.
func TestBootRoundTrip_DoesNotReseedPlaceholderStoreName(t *testing.T) {
	for _, placeholder := range []string{config.DefaultStoreName, "", "  ", "Universal Till store"} {
		t.Run(placeholder, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			cfg := baseCfg()
			cfg.StoreName = placeholder

			s.LoadRuntimeConfig(ctx, cfg)
			if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
				t.Fatalf("SaveRuntimeConfig: %v", err)
			}

			v, ok, err := s.Get(ctx, "store.name")
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("store.name row gone; want it kept blank (shop_name_notice shows only for a stored key)")
			}
			if v != "" {
				t.Fatalf("store.name after boot = %q, want blank", v)
			}
		})
	}
}

// A real name stays put through the boot round-trip, and a real
// UT_STORE_NAME is still adopted while the shop has none — today's
// behaviour, unchanged.
func TestBootRoundTrip_KeepsRealStoreNames(t *testing.T) {
	ctx := context.Background()

	s := newTestStore(t)
	if err := s.Set(ctx, "store.name", "Corner Cafe"); err != nil {
		t.Fatal(err)
	}
	cfg := baseCfg()
	s.LoadRuntimeConfig(ctx, cfg)
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Get(ctx, "store.name"); v != "Corner Cafe" {
		t.Fatalf("stored name after boot = %q, want %q", v, "Corner Cafe")
	}

	s = newTestStore(t)
	cfg = baseCfg()
	cfg.StoreName = "Env Bakery" // UT_STORE_NAME
	s.LoadRuntimeConfig(ctx, cfg)
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Get(ctx, "store.name"); v != "Env Bakery" {
		t.Fatalf("UT_STORE_NAME after boot = %q, want %q", v, "Env Bakery")
	}
}

// ut-docs#5894: a cfg.StoreName the shop-name check refuses (Init already
// ignores such a UT_STORE_NAME; this is the belt-and-braces layer) is never
// written over a blank store.name.
func TestBootRoundTrip_DoesNotWriteRefusedStoreName(t *testing.T) {
	for _, bad := range []string{"Shop‮", "Shop\U000E0100"} {
		ctx := context.Background()
		s := newTestStore(t)
		cfg := baseCfg()
		cfg.StoreName = bad
		s.LoadRuntimeConfig(ctx, cfg)
		if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if v, _, _ := s.Get(ctx, "store.name"); v != "" {
			t.Fatalf("store.name after boot with %q = %q, want blank", bad, v)
		}
	}
}

// A refused name already stored (a cloud directive wrote it) is loaded into
// cfg, refused at Save and so left byte-identical: repairing it is not the
// boot round-trip's job.
func TestBootRoundTrip_LeavesStoredRefusedNameUntouched(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	const stored = "Shop\U000E0100"
	if err := s.Set(ctx, "store.name", stored); err != nil {
		t.Fatal(err)
	}
	cfg := baseCfg()
	s.LoadRuntimeConfig(ctx, cfg)
	if err := s.SaveRuntimeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Get(ctx, "store.name"); v != stored {
		t.Fatalf("stored name after boot = %q, want %q", v, stored)
	}
}
