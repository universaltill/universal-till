package pages

import (
	"fmt"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// currencyDisplayExampleMinor is the sample amount the Settings picker shows
// each mode with (12.50 in a two-decimal currency, 1,250 in a rial).
const currencyDisplayExampleMinor = 1250

// currencyDisplayOption is one row of the Currency display select.
type currencyDisplayOption struct {
	Value    string
	Label    string // "Symbol (£12.50)": translated name plus the live example
	Example  string // the example alone, for the line under the select
	Selected bool
}

// currencyDisplayOptions builds the select for the shop's currency: the
// examples come from the registry resolver, never from per-currency code, so
// "Automatic" shows whatever auto resolves to for THIS currency (ut-docs#5410).
// The example is isolated (LRI/PDI) in an RTL locale so the digits and code
// keep their order inside the surrounding RTL label; no digits live in the
// translated strings.
func currencyDisplayOptions(currentMode, locale string) []currencyDisplayOption {
	current := common.ClampCurrencyDisplay(currentMode)
	c := httpx.CurrencyByCode(httpx.ActiveCurrency().Code)
	rtl := httpx.IsRTL(locale)
	out := make([]currencyDisplayOption, 0, 3)
	for _, m := range []struct{ value, key string }{
		{httpx.CurrencyDisplayAuto, "settings.currency_display.auto"},
		{httpx.CurrencyDisplaySymbol, "settings.currency_display.symbol"},
		{httpx.CurrencyDisplayCode, "settings.currency_display.code"},
	} {
		ex := httpx.FormatMoneyWith(currencyDisplayExampleMinor, c, locale, true, m.value)
		if rtl {
			ex = "⁦" + ex + "⁩"
		}
		out = append(out, currencyDisplayOption{
			Value:    m.value,
			Label:    fmt.Sprintf("%s (%s)", httpx.T(locale, m.key), ex),
			Example:  ex,
			Selected: m.value == current,
		})
	}
	return out
}
