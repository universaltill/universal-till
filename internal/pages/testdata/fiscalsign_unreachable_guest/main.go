//go:build wasip1

// Test guest for ut-docs#675 (fiscal.sign.ask, ADR-0044): a fiscal signing
// plugin whose backend is "down" — it answers every ask with the declared
// unreachable status, so the e2e tests can prove the proceed-and-declare
// path (sale completes anyway, journal marker, receipt outage notice,
// operator Problem — permanent, never re-signed, ADR-0056/ut-docs#839)
// through the REAL wazero runtime. Counterpart of testdata/fiscalsign_guest.
// Built on the Go guest SDK (ADR-0121 F4, ut-docs#3951).
package main

import "github.com/universaltill/universal-till/sdk/plugin"

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(plugin.Event) (any, error) {
			return []byte(`{"status":"unreachable"}`), nil
		},
	})
}
