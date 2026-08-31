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
// src/taxrate.Resolve/ParseOverrides.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"
)

//go:wasmimport ut settings_get
func settingsGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut log_write
func logWrite(ptr, n uint32)

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func logf(format string, args ...any) {
	msg := []byte(fmt.Sprintf(format, args...))
	p, n := ptrOf(msg)
	logWrite(p, n)
}

// callBuf runs a data-returning host call honoring the buffer ABI (grow +
// retry once if the first buffer was too small) — same pattern as the real
// sibling plugins.
func callBuf(fn func(dstPtr, dstCap uint32) int32) ([]byte, int32) {
	buf := make([]byte, 8192)
	p, c := ptrOf(buf)
	n := fn(p, c)
	if n < 0 {
		return nil, n
	}
	if int(n) > len(buf) {
		buf = make([]byte, n)
		p, c = ptrOf(buf)
		n = fn(p, c)
		if n < 0 {
			return nil, n
		}
		if int(n) > len(buf) {
			n = int32(len(buf))
		}
	}
	return buf[:n], n
}

func setting(key string) string {
	kb := []byte(key)
	out, code := callBuf(func(dp, dc uint32) int32 {
		kp, kl := ptrOf(kb)
		return settingsGet(kp, kl, dp, dc)
	})
	if code < 0 {
		return ""
	}
	return string(out)
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &ev)
	if ev.Type != "tax.rate.ask" {
		os.Exit(0)
	}

	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	_ = json.Unmarshal(raw, &wrapper)
	var ask struct {
		TaxCodeID string `json:"tax_code_id"`
		OrderType string `json:"order_type"`
	}
	_ = json.Unmarshal(wrapper.Payload, &ask)

	// Mirrors taxrate.Resolve: dine-in never consults the setting; takeaway
	// looks up the tax code's configured override.
	if ask.OrderType != "takeaway" {
		os.Exit(0)
	}
	ovRaw := strings.TrimSpace(setting("takeaway_rate_overrides"))
	overrides := map[string]int{}
	if ovRaw != "" {
		if err := json.Unmarshal([]byte(ovRaw), &overrides); err != nil {
			logf("taxask_overrides_guest: takeaway_rate_overrides is not valid JSON: %v", err)
			os.Exit(0)
		}
	}
	bp, ok := overrides[ask.TaxCodeID]
	if !ok || bp <= 0 {
		os.Exit(0) // no opinion — the line stays on its own rate
	}
	fmt.Print(string(mustJSON(map[string]int{"rate_bp": bp})))
	os.Exit(0)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
