package data

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// shop.context.v1 (ut-docs#4034): the shop facts a plugin needs to read the
// sales views' minor units — currency code and decimals — plus the shop's
// and this till's names and the default locale. One row, no arguments.
// shop.context.v2 (ut-docs#4045) is the same row under the narrower,
// non-★ view:shop, so a price-only plugin needs no sales grant; v1 keeps
// view:sales until no signed plugin lists it.
func TestShopContextView(t *testing.T) {
	for name, perm := range map[string]string{"shop.context.v1": "view:sales", "shop.context.v2": "view:shop"} {
		t.Run(name, func(t *testing.T) {
			v, ok := LookupCoreView(name)
			if !ok {
				t.Fatalf("%s not registered", name)
			}
			if v.Permission != perm {
				t.Fatalf("permission = %q, want %s", v.Permission, perm)
			}
			args, err := v.ParseArgs(nil)
			if err != nil || len(args) != 0 {
				t.Fatalf("ParseArgs(nil) = %v, %v; want no arguments", args, err)
			}
			if _, err := v.ParseArgs([]byte(`{"days":1}`)); err == nil {
				t.Fatalf("%s accepted an argument", name)
			}

			prev := SetCoreViewShopContext(func(context.Context, *sql.DB) (ShopContextRow, error) {
				return ShopContextRow{StoreName: "Kissa", TillName: "Bar", CurrencyCode: "JPY", CurrencyDecimals: 0, Locale: "ja"}, nil
			})
			t.Cleanup(func() { SetCoreViewShopContext(prev) })
			out, err := RunCoreView(context.Background(), nil, v, args, CoreViewMaxResult)
			if err != nil {
				t.Fatal(err)
			}
			want := `[{"store_name":"Kissa","till_name":"Bar","currency_code":"JPY","currency_decimals":0,"locale":"ja"}]`
			if string(out) != want {
				t.Fatalf("result:\n got  %s\n want %s", out, want)
			}
		})
	}
}

// Without a provider the view fails (view_query -3) rather than guess two
// decimals — the very bug the view exists to fix.
func TestShopContextView_NoProviderFails(t *testing.T) {
	prev := SetCoreViewShopContext(nil)
	t.Cleanup(func() { SetCoreViewShopContext(prev) })
	for _, name := range []string{"shop.context.v1", "shop.context.v2"} {
		v, _ := LookupCoreView(name)
		_, err := RunCoreView(context.Background(), nil, v, map[string]int{}, CoreViewMaxResult)
		if err == nil || !strings.Contains(err.Error(), name+": no shop context provider") {
			t.Fatalf("%s: err = %v, want the missing-provider error naming the view", name, err)
		}
	}
}
