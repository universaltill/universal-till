package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestUxVoucherTxRedemptionOnceRejectsDuplicateRow proves the schema-level
// idempotency backstop ADR-0084 Decision 2 adds (migration 012,
// ut-docs#1716): a SECOND 'redemption' voucher_transactions row for the same
// (voucher_id, sale_id) is rejected by the DB itself — not merely by the
// app-level VoucherRedemptionRecorded pre-check in
// ReserveVoucherRedemption / pos.CompleteSale, which is a check-then-act
// that a writer bypassing the repository could race past. Same shape as
// TestUxPluginSettingsGlobalRejectsDuplicateRow: literal INSERTs straight at
// the table, so the constraint — not any Go code — is what refuses.
//
// The index is PARTIAL (WHERE type = 'redemption'): an 'issue' row for the
// same pair must still coexist with a redemption row, and two redemption
// rows with a NULL sale_id (a pre-#1053 ledger row with no soft sale
// reference) never collide, because SQLite treats NULLs as distinct in a
// unique index.
func TestUxVoucherTxRedemptionOnceRejectsDuplicateRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ux-voucher-tx-redemption-once.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.DB.Exec(`INSERT INTO vouchers (id, original_amount, balance, currency) VALUES ('GS-IDX', 1000, 1000, 'EUR')`); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-1', 'GS-IDX', 'sale-A', 'redemption', 300)`); err != nil {
		t.Fatalf("seed first redemption row: %v", err)
	}

	_, err = d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-2', 'GS-IDX', 'sale-A', 'redemption', 300)`)
	if err == nil {
		t.Fatal("expected a second 'redemption' row for the same (voucher_id, sale_id) to be rejected by ux_voucher_tx_redemption_once")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed: voucher_transactions.voucher_id, voucher_transactions.sale_id") {
		t.Fatalf("insert failed for an unexpected reason, want the ux_voucher_tx_redemption_once constraint: %v", err)
	}

	// Partial: an 'issue' row for the same pair, and a redemption for the
	// same voucher under a DIFFERENT sale, are both still fine.
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-3', 'GS-IDX', 'sale-A', 'issue', 1000)`); err != nil {
		t.Fatalf("an 'issue' row for the same (voucher_id, sale_id) must not be caught by the partial index: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-4', 'GS-IDX', 'sale-B', 'redemption', 200)`); err != nil {
		t.Fatalf("a redemption under a different sale must still insert: %v", err)
	}
	// NULL sale_id rows never collide with each other.
	for _, id := range []string{"tx-5", "tx-6"} {
		if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES (?, 'GS-IDX', NULL, 'redemption', 1)`, id); err != nil {
			t.Fatalf("NULL sale_id redemption row %s must insert (NULLs are distinct in a unique index): %v", id, err)
		}
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-IDX' AND sale_id = 'sale-A' AND type = 'redemption'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("redemption rows for (GS-IDX, sale-A) = %d, want 1 (the rejected insert must not have landed)", n)
	}
}
