package entitlement

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ADR-0163 amendment 2026-10-16 (ut-docs#4539): a my.-born store's block
// carries the shop's currency and decimals. They are cached with the block;
// absent, empty or malformed caches "no shop currency" and never rejects
// the block.

func TestBlockShopCurrencyParsedAndCached(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	const base = `"plan":"shop","subscription_status":"active"`
	for _, tc := range []struct {
		name, body, code, decimals string
	}{
		{"present", `{` + base + `,"currency":"EUR","currency_decimals":2}`, "EUR", "2"},
		{"zero decimals", `{` + base + `,"currency":"JPY","currency_decimals":0}`, "JPY", "0"},
		{"four decimals", `{` + base + `,"currency":"CLF","currency_decimals":4}`, "CLF", "4"},
		{"lowercase and padded", `{` + base + `,"currency":" eur ","currency_decimals":2}`, "EUR", "2"},
		{"absent (till-born store / older cloud)", `{` + base + `}`, "", ""},
		{"null", `{` + base + `,"currency":null,"currency_decimals":null}`, "", ""},
		{"empty string", `{` + base + `,"currency":"","currency_decimals":2}`, "", "2"},
		{"wrong type code", `{` + base + `,"currency":978,"currency_decimals":2}`, "", "2"},
		{"wrong type decimals", `{` + base + `,"currency":"EUR","currency_decimals":"2"}`, "EUR", ""},
		{"two letters", `{` + base + `,"currency":"EU"}`, "", ""},
		{"four letters", `{` + base + `,"currency":"EURO"}`, "", ""},
		{"digits", `{` + base + `,"currency":"E1R"}`, "", ""},
		{"non-ASCII letters", `{` + base + `,"currency":"ÉUR"}`, "", ""},
		{"decimals negative", `{` + base + `,"currency":"EUR","currency_decimals":-1}`, "EUR", ""},
		{"decimals too large", `{` + base + `,"currency":"EUR","currency_decimals":5}`, "EUR", ""},
		{"decimals fractional", `{` + base + `,"currency":"EUR","currency_decimals":2.5}`, "EUR", ""},
		{"object", `{` + base + `,"currency":{"code":"EUR"},"currency_decimals":[2]}`, "", ""},
	} {
		var b Block
		if err := json.Unmarshal([]byte(tc.body), &b); err != nil {
			t.Fatalf("%s: unmarshal rejected the block: %v", tc.name, err)
		}
		kv, err := b.Values(now)
		if err != nil {
			t.Fatalf("%s: Values: %v", tc.name, err)
		}
		if got, ok := kv[KeyShopCurrency]; !ok || got != tc.code {
			t.Errorf("%s: cached shop currency = %q (present=%v), want %q", tc.name, got, ok, tc.code)
		}
		if got, ok := kv[KeyShopCurrencyDecimals]; !ok || got != tc.decimals {
			t.Errorf("%s: cached shop decimals = %q (present=%v), want %q", tc.name, got, ok, tc.decimals)
		}
		code, ok := ShopCurrency(context.Background(), mapReader(kv))
		if code != tc.code || ok != (tc.code != "") {
			t.Errorf("%s: ShopCurrency = (%q, %v), want (%q, %v)", tc.name, code, ok, tc.code, tc.code != "")
		}
	}
}

// The shop currency never lets an otherwise invalid block through.
func TestBlockShopCurrencyDoesNotBypassPlanValidation(t *testing.T) {
	b := Block{Plan: "gold", SubscriptionStatus: "active", Currency: json.RawMessage(`"EUR"`)}
	if _, err := b.Values(time.Now()); err == nil {
		t.Fatal("Values accepted an unknown plan just because currency was set")
	}
}

func TestShopCurrencyReaderFailSafe(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]Reader{
		"no cache":     mapReader{},
		"empty":        mapReader{KeyShopCurrency: ""},
		"garbage":      mapReader{KeyShopCurrency: "euro"},
		"reader error": errReader{},
	} {
		if code, ok := ShopCurrency(ctx, r); ok || code != "" {
			t.Errorf("%s: ShopCurrency = (%q, %v), want (\"\", false)", name, code, ok)
		}
	}
	if code, ok := ShopCurrency(ctx, mapReader{KeyShopCurrency: "gbp"}); !ok || code != "GBP" {
		t.Errorf("lowercase cache: ShopCurrency = (%q, %v), want (GBP, true)", code, ok)
	}
}
