package data

import (
	"context"
	"database/sql"
	"fmt"
)

// FiscalTSESignature is one signed sale's §6 KassenSichV TSE evidence
// (ut-docs#585) — the optional `tse` object a fiscal.sign.ask plugin may
// return alongside "approved" (contract fiscal-sign-ask.md v1.1.0), stored
// verbatim in fiscal_tse_signatures (migration 048). Timestamps stay the
// RFC3339 strings the plugin sent; the signature stays base64. These are
// evidence of what the TSE said, not values core derives or normalizes.
type FiscalTSESignature struct {
	SaleID             string
	TransactionNumber  int64
	SignatureCounter   int64
	SerialNumber       string
	StartTime          string
	LogTime            string
	Signature          string
	SignatureAlgorithm string
	// CreatedAt is stamped by the DB on insert; zero on the way in.
	CreatedAt string
}

// RecordFiscalTSESignature stores one sale's TSE evidence. Idempotent by
// design (INSERT ... ON CONFLICT DO NOTHING on the sale_id primary key): a
// duplicated bookkeeping call for the same sale never errors, duplicates, or
// overwrites the first recorded evidence.
func (r *POSRepo) RecordFiscalTSESignature(ctx context.Context, sig FiscalTSESignature) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO fiscal_tse_signatures
	(sale_id, transaction_number, signature_counter, serial_number, start_time, log_time, signature, signature_algorithm)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(sale_id) DO NOTHING
`, sig.SaleID, sig.TransactionNumber, sig.SignatureCounter, sig.SerialNumber,
		sig.StartTime, sig.LogTime, sig.Signature, sig.SignatureAlgorithm)
	if err != nil {
		return fmt.Errorf("insert fiscal_tse_signatures: %w", err)
	}
	return nil
}

// GetFiscalTSESignature loads the TSE evidence recorded for a sale.
// (nil, false, nil) when none exists — not an error: both receipt render
// paths treat "no evidence" as "no TSE block", never placeholder text.
func (r *POSRepo) GetFiscalTSESignature(ctx context.Context, saleID string) (*FiscalTSESignature, bool, error) {
	var sig FiscalTSESignature
	err := r.db.QueryRowContext(ctx, `
SELECT sale_id, transaction_number, signature_counter, serial_number, start_time, log_time, signature, signature_algorithm, created_at
FROM fiscal_tse_signatures
WHERE sale_id = ?
`, saleID).Scan(&sig.SaleID, &sig.TransactionNumber, &sig.SignatureCounter, &sig.SerialNumber,
		&sig.StartTime, &sig.LogTime, &sig.Signature, &sig.SignatureAlgorithm, &sig.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("select fiscal_tse_signatures: %w", err)
	}
	return &sig, true, nil
}
