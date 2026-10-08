//go:build wasip1

// Test guest for ut-docs#1351's regression coverage: a minimal tax plugin
// that answers tax.rate.ask the exact way ut-plugin-tax-de does — by reading
// its merchant-configured `takeaway_rate_overrides` setting through the REAL
// settings_get host function (buffer ABI and all) and returning
// overrides[tax_code_id] only for orderType == "takeaway". Unlike
// testdata/taxask_guest (a fixed answer, no host calls), this exercises the
// full host boundary the Germany pilot depends on: hostSettingsGet →
// PluginRepo.GetPluginSetting → the JSON-string unwrap → the guest's own
// parse — the chain a mocked pos.TaxRateAsker (fakeTaxAsker) bypasses
// entirely. Logic mirrors ut-plugin-tax-de/src/main.go handleTaxRateAsk +
// src/taxrate.Resolve/ParseOverrides. Built on the Go guest SDK (ADR-0121
// F4, ut-docs#3951): plugin.SettingsGet does the buffer-ABI grow-and-retry.
package main

import (
	"encoding/json"
	"strings"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// setting returns the plain setting value, or "" when the host reports any
// error (unset, denied, …).
func setting(key string) string {
	v, err := plugin.SettingsGet(key)
	if err != nil {
		return ""
	}
	return v
}

func taxRateAsk(ev plugin.Event) (any, error) {
	var ask struct {
		TaxCodeID string `json:"tax_code_id"`
		OrderType string `json:"order_type"`
	}
	_ = ev.Decode(&ask)

	// Mirrors taxrate.Resolve: dine-in never consults the setting; takeaway
	// looks up the tax code's configured override.
	if ask.OrderType != "takeaway" {
		return nil, nil
	}
	ovRaw := strings.TrimSpace(setting("takeaway_rate_overrides"))
	overrides := map[string]int{}
	if ovRaw != "" {
		if err := json.Unmarshal([]byte(ovRaw), &overrides); err != nil {
			plugin.Logf("taxask_overrides_guest: takeaway_rate_overrides is not valid JSON: %v", err)
			return nil, nil
		}
	}
	bp, ok := overrides[ask.TaxCodeID]
	if !ok || bp <= 0 {
		return nil, nil // no opinion — the line stays on its own rate
	}
	return map[string]int{"rate_bp": bp}, nil
}

func main() {
	plugin.Run(plugin.Handlers{"tax.rate.ask": taxRateAsk})
}
