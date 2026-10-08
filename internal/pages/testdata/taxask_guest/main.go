//go:build wasip1

// Test guest for ut-docs#368's regression coverage: a minimal tax plugin
// that answers every tax.rate.ask with a fixed override, so the two-till
// sync test can prove tax "resumes working normally" through the REAL wazero
// runtime after the broken plugin self-heals — not just that a row flipped.
// Built on the Go guest SDK (ADR-0121 F4, ut-docs#3951).
package main

import "github.com/universaltill/universal-till/sdk/plugin"

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(plugin.Event) (any, error) { return []byte(`{"rate_bp":900}`), nil },
	})
}
