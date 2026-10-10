package pages

import (
	"context"
	"database/sql"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// shop.context.v1 and .v2 (ut-docs#4034, #4045) read the same facts the built-in Ask
// prompt did (removed in ut-docs#2851): the live currency from httpx's registry, so its
// decimals are the ones the till formats money with, the default locale and
// the shop name — plus this till's own name as it reports it to the cloud.
// data cannot import httpx or enroll, so the view's source lives here.
func init() { data.SetCoreViewShopContext(shopContextView) }

func shopContextView(ctx context.Context, db *sql.DB) (data.ShopContextRow, error) {
	kv := settings.NewStore(db)
	store, _, err := kv.Get(ctx, "store.name")
	if err != nil {
		return data.ShopContextRow{}, err
	}
	// The shop's stored currency, as LoadState reads it, resolved through the
	// same registry the till formats with. Only when none is stored does it
	// take the live value: before Init publishes it (a schedule tick at
	// boot), httpx still holds its GBP default (ut-docs#4034 review).
	cur := httpx.ActiveCurrency()
	code, ok, err := kv.Get(ctx, common.KeyCurrency)
	if err != nil {
		return data.ShopContextRow{}, err
	}
	if ok && strings.TrimSpace(code) != "" {
		cur = httpx.CurrencyByCode(code)
	}
	return data.ShopContextRow{
		StoreName:        strings.TrimSpace(store),
		TillName:         enroll.DeviceName(ctx, kv), // "" also when unreadable
		CurrencyCode:     cur.Code,
		CurrencyDecimals: cur.Decimals,
		Locale:           httpx.DefaultLocale(),
	}, nil
}
