package cloudsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0163 amendment 2026-10-16 (ut-docs#4539): after a successful sync
// the tick calls Hooks.ShopCurrency, once, with the block already cached —
// the hook reconciles the till's currency from that cache.
func TestTickCallsShopCurrencyHookAfterCachingBlock(t *testing.T) {
	d := openMigratedDB(t, "shop_currency_hook.db")
	cloud := &fakeCloud{entitlement: json.RawMessage(`{"plan":"shop","subscription_status":"active","currency":"EUR","currency_decimals":2}`)}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	calls := 0
	seen := ""
	hooks := Hooks{ShopCurrency: func(ctx context.Context) {
		calls++
		seen, _ = entitlement.ShopCurrency(ctx, data.NewSettingsRepo(d.DB))
	}}
	if err := Tick(context.Background(), testCfg(srv.URL), d.DB, hooks); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ShopCurrency hook called %d times, want 1", calls)
	}
	if seen != "EUR" {
		t.Fatalf("hook saw cached shop currency %q, want EUR (block must be cached first)", seen)
	}
}

// A failed sync never reconciles: the cache may be stale.
func TestTickSkipsShopCurrencyHookWhenSyncFails(t *testing.T) {
	d := openMigratedDB(t, "shop_currency_hook_fail.db")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	calls := 0
	hooks := Hooks{ShopCurrency: func(context.Context) { calls++ }}
	if err := Tick(context.Background(), testCfg(srv.URL), d.DB, hooks); err == nil {
		t.Fatal("Tick against a failing cloud returned nil")
	}
	if calls != 0 {
		t.Fatalf("ShopCurrency hook called %d times after a failed sync, want 0", calls)
	}
}

// A nil hook (unsupported) is fine.
func TestTickNilShopCurrencyHook(t *testing.T) {
	d := openMigratedDB(t, "shop_currency_hook_nil.db")
	cloud := &fakeCloud{entitlement: json.RawMessage(`{"plan":"shop","subscription_status":"active","currency":"EUR"}`)}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	if err := Tick(context.Background(), testCfg(srv.URL), d.DB, Hooks{}); err != nil {
		t.Fatalf("Tick: %v", err)
	}
}

// A lapsed store's 402 plan_required carries the block too, and the till
// caches it (cachePlanRequired): the cloud did answer, so the hook runs —
// on a refused GET check-in and on a refused POST alike (review M3).
func TestTickCallsShopCurrencyHookAfterCached402(t *testing.T) {
	const block = `{"plan":"local","subscription_status":"lapsed","currency":"EUR","currency_decimals":2}`
	for _, tc := range []struct {
		name       string
		checkinRaw int // GET answer; 0 = 402
	}{
		{name: "checkin_402"},
		{name: "sync_402_after_unusable_checkin", checkinRaw: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetTillAuth(t)
			d := openMigratedDB(t, "shop_currency_hook_402_"+tc.name+".db")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && tc.checkinRaw != 0 {
					w.WriteHeader(tc.checkinRaw)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"data":{"entitlement":` + block + `},"error":{"code":"plan_required","message":"x"}}`))
			}))
			defer srv.Close()
			calls := 0
			seen := ""
			hooks := Hooks{ShopCurrency: func(ctx context.Context) {
				calls++
				seen, _ = entitlement.ShopCurrency(ctx, data.NewSettingsRepo(d.DB))
			}}
			if err := Tick(context.Background(), testCfg(srv.URL), d.DB, hooks); err == nil {
				t.Fatal("Tick on a 402 returned nil")
			}
			if calls != 1 || seen != "EUR" {
				t.Fatalf("hook calls = %d, saw %q; want 1 call after the 402's block (EUR) was cached", calls, seen)
			}
		})
	}
}

// A 402 with no block (an older cloud, a proxy page) caches nothing: no hook.
func TestTickSkipsShopCurrencyHookOn402WithoutBlock(t *testing.T) {
	resetTillAuth(t)
	d := openMigratedDB(t, "shop_currency_hook_402_noblock.db")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	calls := 0
	hooks := Hooks{ShopCurrency: func(context.Context) { calls++ }}
	_ = Tick(context.Background(), testCfg(srv.URL), d.DB, hooks)
	if calls != 0 {
		t.Fatalf("hook called %d times after a block-less 402, want 0", calls)
	}
}
