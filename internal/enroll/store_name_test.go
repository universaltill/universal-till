package enroll

import (
	"context"
	"errors"
	"testing"
)

// errKV fails every read of one key, so a test can prove a broken settings
// read never fails registration.
type errKV struct {
	*fakeKV
	failKey string
}

func (e errKV) Get(ctx context.Context, key string) (string, bool, error) {
	if key == e.failKey {
		return "", false, errors.New("settings read failed")
	}
	return e.fakeKV.Get(ctx, key)
}

// ut-docs#2776: the setup wizard saves the shop name to the store.name
// setting but never updates the in-memory cfg.StoreName (loaded once at
// boot), and registers in the same request. Registration must send the name
// the owner typed, not the "My Store" env default — otherwise every fresh
// install creates another cloud shop called "My Store".
func TestRegisterNowSendsShopNameFromSettings(t *testing.T) {
	resetState()
	var gotBody map[string]any
	srv := registerTestServer(t, &gotBody)

	kv := newFakeKV()
	if err := kv.Set(context.Background(), StoreNameSettingsKey, "  Corner Café  "); err != nil {
		t.Fatalf("seed name: %v", err)
	}
	cfg := freshConfig(srv.URL)
	cfg.StoreName = "My Store" // the boot-time default the wizard never refreshes

	if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
		t.Fatalf("RegisterNow: %v", err)
	}
	if got := gotBody["store_name"]; got != "Corner Café" {
		t.Fatalf("store_name = %v, want %q", got, "Corner Café")
	}
}

// With no usable store.name setting (unset, blank, or an unreadable
// settings store) registration keeps today's behaviour: cfg.StoreName.
func TestRegisterNowFallsBackToConfigStoreName(t *testing.T) {
	cases := []struct {
		name string
		kv   func() Settings
	}{
		{"unset", func() Settings { return newFakeKV() }},
		{"blank", func() Settings {
			kv := newFakeKV()
			_ = kv.Set(context.Background(), StoreNameSettingsKey, "   ")
			return kv
		}},
		{"read error", func() Settings { return errKV{fakeKV: newFakeKV(), failKey: StoreNameSettingsKey} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetState()
			var gotBody map[string]any
			srv := registerTestServer(t, &gotBody)
			cfg := freshConfig(srv.URL)

			if _, err := RegisterNow(context.Background(), cfg, c.kv()); err != nil {
				t.Fatalf("RegisterNow: %v", err)
			}
			if got := gotBody["store_name"]; got != "Corner Shop" {
				t.Fatalf("store_name = %v, want cfg.StoreName %q", got, "Corner Shop")
			}
			if !CurrentStatus().Registered {
				t.Fatal("registration did not complete")
			}
		})
	}
}

// ut-docs#3096: enrolment never sends a placeholder name. With store.name
// still the migration-seeded "My Store" and no real name in the config
// either, registration sends a blank name (the cloud then marks the shop as
// unnamed) instead of creating yet another cloud shop called "My Store".
// Registration itself still goes ahead — it must never block a plugin install.
func TestRegisterNowNeverSendsPlaceholderName(t *testing.T) {
	cases := []struct {
		name, setting, cfgName, want string
	}{
		{"seeded default, default cfg", "My Store", "My Store", ""},
		{"seeded default, real env name", "My Store", "Corner Shop", "Corner Shop"},
		{"cloud default in setting", "Universal Till store", "My Store", ""},
		{"unset, default cfg", "", "my store", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetState()
			var gotBody map[string]any
			srv := registerTestServer(t, &gotBody)
			kv := newFakeKV()
			if c.setting != "" {
				_ = kv.Set(context.Background(), StoreNameSettingsKey, c.setting)
			}
			cfg := freshConfig(srv.URL)
			cfg.StoreName = c.cfgName

			if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
				t.Fatalf("RegisterNow: %v", err)
			}
			if got := gotBody["store_name"]; got != c.want {
				t.Fatalf("store_name = %q, want %q", got, c.want)
			}
			if !CurrentStatus().Registered {
				t.Fatal("registration did not complete")
			}
		})
	}
}
