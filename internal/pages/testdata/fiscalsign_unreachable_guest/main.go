//go:build wasip1

// Test guest for ut-docs#675 (fiscal.sign.ask, ADR-0044): a fiscal signing
// plugin whose backend is "down" — it answers every ask with the declared
// unreachable status, so the e2e tests can prove the proceed-and-declare
// path (sale completes anyway, journal marker, receipt outage notice,
// operator Problem, background retry queued) through the REAL wazero
// runtime. Counterpart of testdata/fiscalsign_guest.
package main

import "fmt"

func main() {
	fmt.Print(`{"status":"unreachable"}`)
}
