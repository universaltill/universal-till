//go:build wasip1

// Test guest for ADR-0077 Decision 3 (ut-docs#1520): a fiscal signing plugin
// answering fiscal.sign.reconcile.ask through the REAL wazero runtime, so
// the e2e sweep test can prove the ".ask"-suffixed event is dispatched
// blocking/value-returning with no runtime dispatch-mode changes, and that
// core's two-tier check consumes the answer end-to-end. Echoes the request's
// started_tx_id back as tx_id (what a real signer does after retrieving THAT
// transaction) alongside canned §6 KassenSichV evidence. This is NOT a real
// signer (no fiskaly, no network): it exists purely to exercise core's
// reconcile plumbing. Any other event writes nothing; a request without
// started_tx_id gets "not-found" — the honest answer for a transaction it
// never started. Built on the Go guest SDK (ADR-0121 F4, ut-docs#3951).
package main

import "github.com/universaltill/universal-till/sdk/plugin"

func reconcile(ev plugin.Event) (any, error) {
	var ask struct {
		SaleID      string `json:"sale_id"`
		StartedTxID string `json:"started_tx_id"`
	}
	_ = ev.Decode(&ask)
	if ask.StartedTxID == "" {
		return []byte(`{"status":"not-found"}`), nil
	}
	return []byte(`{"status":"confirmed","tx_id":"` + ask.StartedTxID + `","tx_revision":2,"tse":{` +
		`"transaction_number":4712,` +
		`"signature_counter":12346,` +
		`"serial_number":"TSE-TEST-SERIAL-1",` +
		`"start_time":"2026-08-15T10:31:00Z",` +
		`"log_time":"2026-08-15T10:31:02Z",` +
		`"signature":"RECONCILEDSIGBASE64==",` +
		`"signature_algorithm":"ecdsa-plain-SHA256"}}`), nil
}

func main() {
	plugin.Run(plugin.Handlers{"fiscal.sign.reconcile.ask": reconcile})
}
