//go:build wasip1

// Test guest for ut-docs#3633's regression coverage: a minimal payment
// plugin that answers payment.<key>.authorize the way ut-plugin-payment-
// stripe/-sumup do when they are installed but not yet set up — it reads its
// credential setting (stripe_secret_key) through the REAL settings_get host
// function and declines (exit 2) when the setting is missing or empty,
// approves (exit 0) only when a value is present. The host-side test drives
// it through the real POST /api/pos/tender handler to prove an unconfigured
// payment plugin can never complete a sale or print a receipt.
// Built on the Go guest SDK (ADR-0121 F4, ut-docs#3951): plugin.SettingsGet
// honours the buffer ABI's grow-and-retry, so a long value never reads as
// empty.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// authorize's verdict depends only on the setting, never on the event.
func authorize(plugin.Event) (any, error) {
	v, err := plugin.SettingsGet("stripe_secret_key")
	if err != nil || strings.TrimSpace(v) == "" {
		fmt.Fprintln(os.Stderr, "payment plugin not configured: stripe_secret_key is unset")
		return nil, plugin.ExitCode(2)
	}
	return []byte("{\"approved\":true}\n"), nil
}

func main() {
	plugin.Run(plugin.Handlers{"*": authorize})
}
