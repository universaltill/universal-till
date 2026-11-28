package data

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
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
			dbo, err := db.Open(testsupport.MigratedDBFile(t, "shop-ctx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer dbo.Close()
			if err := NewSettingsRepo(dbo.DB).Set(context.Background(), "store.country", " de "); err != nil {
				t.Fatal(err)
			}
			prevNow := coreViewNow
			coreViewNow = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local) }
			t.Cleanup(func() { coreViewNow = prevNow })
			out, err := RunCoreView(context.Background(), dbo.DB, v, args, CoreViewMaxResult)
			if err != nil {
				t.Fatal(err)
			}
			want := `[{"store_name":"Kissa","till_name":"Bar","currency_code":"JPY","currency_decimals":0,"locale":"ja"}]`
			if name == "shop.context.v2" {
				want = `[{"store_name":"Kissa","till_name":"Bar","currency_code":"JPY","currency_decimals":0,"locale":"ja","country":"DE","business_date":"2026-10-09"}]`
			}
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

// shop.context.v2 (ut-docs#7231): country is "" when unset, and business_date
// follows reports.business_day_start in the till's local time zone — an
// instant before the boundary belongs to the previous date.
func TestShopContextV2_CountryUnsetAndBusinessDate(t *testing.T) {
	prev := SetCoreViewShopContext(func(context.Context, *sql.DB) (ShopContextRow, error) {
		return ShopContextRow{CurrencyCode: "GBP", CurrencyDecimals: 2}, nil
	})
	t.Cleanup(func() { SetCoreViewShopContext(prev) })
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "shop-ctx-date.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	v2, _ := LookupCoreView("shop.context.v2")
	v1, _ := LookupCoreView("shop.context.v1")
	prevNow := coreViewNow
	t.Cleanup(func() { coreViewNow = prevNow })

	cases := []struct {
		start string
		now   time.Time
		want  string
	}{
		{"", time.Date(2026, 10, 9, 0, 0, 0, 0, time.Local), "2026-10-09"},
		{"", time.Date(2026, 10, 9, 23, 59, 59, 0, time.Local), "2026-10-09"},
		{"04:00", time.Date(2026, 10, 9, 3, 59, 0, 0, time.Local), "2026-10-08"},
		{"04:00", time.Date(2026, 10, 9, 4, 0, 0, 0, time.Local), "2026-10-09"},
		{"04:30", time.Date(2026, 10, 9, 4, 29, 0, 0, time.Local), "2026-10-08"},
		{"bogus", time.Date(2026, 10, 9, 1, 0, 0, 0, time.Local), "2026-10-09"},
	}
	for _, c := range cases {
		if err := NewSettingsRepo(dbo.DB).Set(ctx, "reports.business_day_start", c.start); err != nil {
			t.Fatal(err)
		}
		now := c.now
		coreViewNow = func() time.Time { return now }
		out, err := RunCoreView(ctx, dbo.DB, v2, map[string]int{}, CoreViewMaxResult)
		if err != nil {
			t.Fatal(err)
		}
		want := `[{"store_name":"","till_name":"","currency_code":"GBP","currency_decimals":2,"locale":"","country":"","business_date":"` + c.want + `"}]`
		if string(out) != want {
			t.Fatalf("start %q now %v:\n got  %s\n want %s", c.start, c.now, out, want)
		}
		// v1 never carries them.
		o1, err := RunCoreView(ctx, dbo.DB, v1, map[string]int{}, CoreViewMaxResult)
		if err != nil || strings.Contains(string(o1), "country") || strings.Contains(string(o1), "business_date") {
			t.Fatalf("v1 = %s, %v; want no country/business_date", o1, err)
		}
	}
}
