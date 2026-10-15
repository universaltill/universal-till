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

// ut-docs#3096: enrolment never sends a placeholder name. A real name in
// the config still counts when store.name holds the migration-seeded
// "My Store".
func TestRegisterNowNeverSendsPlaceholderName(t *testing.T) {
	resetState()
	var gotBody map[string]any
	srv := registerTestServer(t, &gotBody)
	kv := newFakeKV()
	_ = kv.Set(context.Background(), StoreNameSettingsKey, "My Store")
	cfg := freshConfig(srv.URL)
	cfg.StoreName = "Corner Shop"

	if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
		t.Fatalf("RegisterNow: %v", err)
	}
	if got := gotBody["store_name"]; got != "Corner Shop" {
		t.Fatalf("store_name = %q, want %q", got, "Corner Shop")
	}
	if !CurrentStatus().Registered {
		t.Fatal("registration did not complete")
	}
}

// ut-docs#4272: a till whose shop has no real name (blank, the migration-
// seeded "My Store", or the cloud's own placeholder) never calls the
// anonymous /stores/register: the cloud would create an unnamed shop the
// owner cannot recognise. RegisterNow says why (ErrStoreUnnamed) and the
// till stays unregistered until the shop is named.
func TestRegisterNowRefusesUnnamedShop(t *testing.T) {
	cases := []struct {
		name, setting, cfgName string
	}{
		{"seeded default, default cfg", "My Store", "My Store"},
		{"blank setting, blank cfg", "   ", ""},
		{"unset, default cfg", "", "my store"},
		{"cloud default in setting", "Universal Till store", "My Store"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetState()
			srv, registerCalls := testMarketplace(t, 0)
			kv := newFakeKV()
			if c.setting != "" {
				_ = kv.Set(context.Background(), StoreNameSettingsKey, c.setting)
			}
			cfg := freshConfig(srv.URL)
			cfg.StoreName = c.cfgName

			if !AwaitingShopName(context.Background(), cfg, kv) {
				t.Fatal("AwaitingShopName = false for an unnamed, unregistered till")
			}
			_, err := RegisterNow(context.Background(), cfg, kv)
			if !errors.Is(err, ErrStoreUnnamed) {
				t.Fatalf("RegisterNow err = %v, want ErrStoreUnnamed", err)
			}
			if *registerCalls != 0 {
				t.Fatalf("/stores/register called %d times for an unnamed shop", *registerCalls)
			}
			if CurrentStatus().Registered {
				t.Fatal("unnamed till reads as registered")
			}
			// EnsureRegistered (the plugin-install path) swallows it the same way.
			EnsureRegistered(context.Background(), cfg, kv)
			if *registerCalls != 0 {
				t.Fatalf("EnsureRegistered called /stores/register %d times for an unnamed shop", *registerCalls)
			}
		})
	}
}

// Once the shop is named (the wizard or Settings writes store.name), the
// very next attempt registers with that name.
func TestRegisterNowRegistersOnceShopIsNamed(t *testing.T) {
	resetState()
	var gotBody map[string]any
	srv := registerTestServer(t, &gotBody)
	kv := newFakeKV()
	_ = kv.Set(context.Background(), StoreNameSettingsKey, "My Store")
	cfg := freshConfig(srv.URL)
	cfg.StoreName = "My Store"

	if _, err := RegisterNow(context.Background(), cfg, kv); !errors.Is(err, ErrStoreUnnamed) {
		t.Fatalf("first RegisterNow err = %v, want ErrStoreUnnamed", err)
	}
	_ = kv.Set(context.Background(), StoreNameSettingsKey, "Corner Café")
	if AwaitingShopName(context.Background(), cfg, kv) {
		t.Fatal("AwaitingShopName = true after the shop was named")
	}
	if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
		t.Fatalf("RegisterNow after naming: %v", err)
	}
	if got := gotBody["store_name"]; got != "Corner Café" {
		t.Fatalf("store_name = %v, want %q", got, "Corner Café")
	}
	if !CurrentStatus().Registered {
		t.Fatal("registration did not complete")
	}
	_ = kv.Set(context.Background(), StoreNameSettingsKey, "My Store")
	if AwaitingShopName(context.Background(), cfg, kv) {
		t.Fatal("AwaitingShopName = true for a registered till")
	}
}
