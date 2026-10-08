//go:build wasip1

// Test guest for ut-docs#675 (fiscal.sign.ask, ADR-0044): a minimal fiscal
// signing plugin that answers every fiscal.sign.ask with "approved", so the
// e2e tender tests can prove a normally-signing till completes a sale with
// no unsigned_fiscal_signing marker — through the REAL wazero runtime, same
// shape as testdata/taxask_guest. This is NOT a real signer (no fiskaly, no
// network): it exists purely to exercise core's dispatch/outcome plumbing.
// Built on the Go guest SDK (ADR-0121 F4, ut-docs#3951).
package main

import "github.com/universaltill/universal-till/sdk/plugin"

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(plugin.Event) (any, error) {
			return []byte(`{"status":"approved"}`), nil
		},
	})
}
