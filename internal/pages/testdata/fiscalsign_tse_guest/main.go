//go:build wasip1

// Test guest for ut-docs#585 (fiscal.sign.ask contract v1.1.0): a fiscal
// signing plugin that answers "approved" WITH the optional `tse` evidence
// object — the §6 KassenSichV receipt data points — so the e2e tender tests
// can prove core parses, persists and renders the evidence through the REAL
// wazero runtime. Deliberately a NEW fixture next to testdata/fiscalsign_guest
// (which stays bare-approved: other tests depend on its minimal shape, and a
// bare approval remaining valid is itself part of the 1.1.0 contract).
// This is NOT a real signer (no fiskaly, no network, fixed canned values):
// it exists purely to exercise core's evidence plumbing.
package main

import "fmt"

func main() {
	fmt.Print(`{"status":"approved","tse":{` +
		`"transaction_number":4711,` +
		`"signature_counter":12345,` +
		`"serial_number":"TSE-TEST-SERIAL-1",` +
		`"start_time":"2026-08-15T10:31:00Z",` +
		`"log_time":"2026-08-15T10:31:02Z",` +
		`"signature":"TESTSIGBASE64==",` +
		`"signature_algorithm":"ecdsa-plain-SHA256"}}`)
}
