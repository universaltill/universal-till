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
func TestShopContextView(t *testing.T) {
	v, ok := LookupCoreView("shop.context.v1")
	if !ok {
		t.Fatal("shop.context.v1 not registered")
	}
	if v.Permission != "view:sales" {
		t.Fatalf("permission = %q, want view:sales", v.Permission)
	}
	args, err := v.ParseArgs(nil)
	if err != nil || len(args) != 0 {
		t.Fatalf("ParseArgs(nil) = %v, %v; want no arguments", args, err)
	}
	if _, err := v.ParseArgs([]byte(`{"days":1}`)); err == nil {
		t.Fatal("shop.context.v1 accepted an argument")
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
}

// Without a provider the view fails (view_query -3) rather than guess two
// decimals — the very bug the view exists to fix.
func TestShopContextView_NoProviderFails(t *testing.T) {
	prev := SetCoreViewShopContext(nil)
	t.Cleanup(func() { SetCoreViewShopContext(prev) })
	v, _ := LookupCoreView("shop.context.v1")
	_, err := RunCoreView(context.Background(), nil, v, map[string]int{}, CoreViewMaxResult)
	if err == nil || !strings.Contains(err.Error(), "no shop context provider") {
		t.Fatalf("err = %v, want the missing-provider error", err)
	}
}
