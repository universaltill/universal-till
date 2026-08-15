//go:build wasip1

// Test guest for ut-docs#675 (fiscal.sign.ask, ADR-0044): a minimal fiscal
// signing plugin that answers every fiscal.sign.ask with "approved", so the
// e2e tender tests can prove a normally-signing till completes a sale with
// no unsigned_fiscal_signing marker — through the REAL wazero runtime, same
// shape as testdata/taxask_guest. This is NOT a real signer (no fiskaly, no
// network): it exists purely to exercise core's dispatch/outcome plumbing.
package main

import "fmt"

func main() {
	fmt.Print(`{"status":"approved"}`)
}
