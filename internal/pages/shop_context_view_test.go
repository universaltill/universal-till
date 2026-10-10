package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// shop.context.v1 (ut-docs#4034) runs on the provider this package installs:
// the shop's currency with the decimals the till formats it with (JPY: 0,
// never a guessed 2), the shop name, THIS till's own name — a joined till's
// sync.till_name, not the main till's till.name — and the default locale.
func TestShopContextView_RealProvider(t *testing.T) {
	restore := httpx.SnapshotStateForTests()
	t.Cleanup(restore)
	// httpx's unpublished default: before Init publishes the shop's
	// currency (a schedule tick at boot) the view must still read the
	// stored one, not report GBP/2 (review finding).
	httpx.InitCurrency("")
	httpx.SetDefaultLocale("de")

	dbo, err := db.Open(testsupport.MigratedDBFile(t, "shop-context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := data.NewSettingsRepo(dbo.DB)
	set := func(kv map[string]string) {
		t.Helper()
		for k, v := range kv {
			if err := repo.Set(ctx, k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	// shop.context.v2 (ut-docs#4045, under view:shop) reads the same row
	// from the same provider.
	run := func(want string) {
		t.Helper()
		for _, name := range []string{"shop.context.v1", "shop.context.v2"} {
			v, ok := data.LookupCoreView(name)
			if !ok {
				t.Fatalf("%s not registered", name)
			}
			out, err := data.RunCoreView(ctx, dbo.DB, v, map[string]int{}, data.CoreViewMaxResult)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != want {
				t.Fatalf("%s result:\n got  %s\n want %s", name, out, want)
			}
		}
	}

	set(map[string]string{
		"store.currency":   "JPY",
		"store.name":       "  Kissa Ramen ",
		"till.name":        "Main counter",
		"sync.primary_url": "http://10.0.0.2:8080",
		"sync.till_name":   "Bar",
	})
	run(`[{"store_name":"Kissa Ramen","till_name":"Bar","currency_code":"JPY","currency_decimals":0,"locale":"de"}]`)

	// Unset names are "", not English placeholder text.
	set(map[string]string{"store.currency": "EUR", "store.name": "", "till.name": "", "sync.primary_url": "", "sync.till_name": ""})
	run(`[{"store_name":"","till_name":"","currency_code":"EUR","currency_decimals":2,"locale":"de"}]`)

	// No stored currency: the live one the till formats with.
	set(map[string]string{"store.currency": ""})
	httpx.InitCurrency("IRT")
	run(`[{"store_name":"","till_name":"","currency_code":"IRT","currency_decimals":0,"locale":"de"}]`)
}
