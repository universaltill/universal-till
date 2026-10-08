//go:build wasip1

// Test guest for ut-docs#3633's regression coverage: a payment plugin whose
// required credential setting is NOT configured must decline, and the host
// must turn that decline into a fail-closed tender (402, no sale, basket
// kept). Mirrors the real shape of ut-plugin-payment-stripe /
// ut-plugin-payment-sumup: on ".authorize" it reads its own "secret_key"
// setting via the settings_get host function and exits non-zero when the
// setting is missing (negative host code) or empty. With a non-empty value
// it approves. Exercised through the real compiled module + wazero runtime
// by internal/pages' TestTenderHandler_RealWasmGuest* tests, not a fake Go
// handler.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{"*": func(plugin.Event) (any, error) {
		v, err := plugin.SettingsGet("secret_key")
		if err != nil || v == "" {
			// Not found (negative host code) or empty: unconfigured -> decline.
			var e *plugin.Error
			code := int32(0)
			if errors.As(err, &e) {
				code = e.Code
			}
			fmt.Fprintf(os.Stderr, "unsetsecret_guest: secret_key not configured (settings_get=%d)\n", code)
			return []byte(`{"approved":false,"error":"secret_key not configured"}` + "\n"), plugin.ExitCode(2)
		}
		return []byte(`{"approved":true}` + "\n"), nil
	}})
}
