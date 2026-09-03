package data

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// FiscalDeviceReceipt is the evidence a fiscal *device* plugin returns when
// it has taken a sale's payment and printed the legal receipt itself — the
// Turkish YN ÖKC pattern (Law No. 3100; docs/arch/turkey-fiscal-compliance.md
// §1.1), where the certified device, not the till, is the fiscal record.
// It is the optional `fiscal_device` object on a payment.<key>.authorize /
// payment.<key>.refund answer (internal/fiscal.DeviceEvidence), stored
// verbatim in fiscal_device_receipts: what the device said, never values
// core derives. One row per sale (the device issues exactly one receipt per
// sale; a split tender across the device and another method is refused by
// the plugin, see plugins/tax-tr/README.md).
//
// Deliberately a sibling of FiscalTSESignature rather than a widening of
// it: a German TSE signs a receipt the till prints, a Turkish ÖKC prints
// the receipt itself — the two evidence shapes share no field but sale_id.
type FiscalDeviceReceipt struct {
	SaleID string
	// DeviceKind names the device class ("okc" — Turkish YN ÖKC — is the
	// only kind today; a future market's device gets its own value).
	DeviceKind string
	// Maker is the device manufacturer / TSM operator as the plugin names
	// it ("beko", "pavo", "hugin", "ingenico", "sim" for the simulator).
	Maker string
	// Serial is the device's own serial / terminal id (GİB registers a
	// YN ÖKC by this).
	Serial string
	// ReceiptNo is the device-issued receipt number (fiş no) — the number
	// the customer's legal receipt carries. Required: evidence without it
	// is not evidence (see fiscal.DeviceEvidence.Valid).
	ReceiptNo string
	// ReceiptKind is what the device printed: "mali_fis" (fiscal receipt),
	// "bilgi_fisi" (information slip, e.g. an invoice-documented sale),
	// "iade_fisi" (refund slip). Free text from the plugin, not validated
	// beyond non-empty defaulting.
	ReceiptKind string
	// ZNo is the device's current daily (Z) report counter at issue time.
	ZNo int64
	// IssuedAt is the device's own timestamp for the receipt, as sent.
	IssuedAt string
	// CreatedAt is stamped by the DB on insert; zero on the way in.
	CreatedAt string
}

// RecordFiscalDeviceReceipt persists a device receipt for a sale.
// Idempotent on sale_id (first write wins): the device already printed —
// a retry must never overwrite what was recorded as printed.
func (r *POSRepo) RecordFiscalDeviceReceipt(ctx context.Context, rec FiscalDeviceReceipt) error {
	kind := rec.DeviceKind
	if kind == "" {
		kind = "okc"
	}
	receiptKind := rec.ReceiptKind
	if receiptKind == "" {
		receiptKind = "mali_fis"
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO fiscal_device_receipts
	(sale_id, device_kind, maker, serial, receipt_no, receipt_kind, z_no, issued_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(sale_id) DO NOTHING
`, rec.SaleID, kind, rec.Maker, rec.Serial, rec.ReceiptNo, receiptKind, rec.ZNo, rec.IssuedAt)
	if err != nil {
		return fmt.Errorf("insert fiscal_device_receipts: %w", err)
	}
	return nil
}

const fiscalDeviceReceiptColumns = `sale_id, device_kind, maker, serial, receipt_no, receipt_kind, z_no, issued_at, created_at`

func scanFiscalDeviceReceipt(row *sql.Row) (*FiscalDeviceReceipt, bool, error) {
	var rec FiscalDeviceReceipt
	err := row.Scan(&rec.SaleID, &rec.DeviceKind, &rec.Maker, &rec.Serial, &rec.ReceiptNo,
		&rec.ReceiptKind, &rec.ZNo, &rec.IssuedAt, &rec.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("select fiscal_device_receipts: %w", err)
	}
	return &rec, true, nil
}

// GetFiscalDeviceReceipt loads the device receipt recorded for a sale.
// (nil, false, nil) when none exists — not an error: the receipt renderers
// treat "no evidence" as "no block", never placeholder text.
func (r *POSRepo) GetFiscalDeviceReceipt(ctx context.Context, saleID string) (*FiscalDeviceReceipt, bool, error) {
	return scanFiscalDeviceReceipt(r.db.QueryRowContext(ctx, `
SELECT `+fiscalDeviceReceiptColumns+`
FROM fiscal_device_receipts
WHERE sale_id = ?
`, saleID))
}

// LatestFiscalDeviceReceipt returns the most recently recorded device
// receipt on this till — the status page's "last receipt from the device"
// line. (nil, false, nil) when the device has never answered.
func (r *POSRepo) LatestFiscalDeviceReceipt(ctx context.Context) (*FiscalDeviceReceipt, bool, error) {
	return scanFiscalDeviceReceipt(r.db.QueryRowContext(ctx, `
SELECT `+fiscalDeviceReceiptColumns+`
FROM fiscal_device_receipts
ORDER BY created_at DESC, rowid DESC
LIMIT 1
`))
}

// CountFiscalDeviceReceiptsSince counts device receipts recorded at or
// after since (compared in UTC against the DB's own datetime('now')
// stamp) — the status page's "receipts today" figure, windowed on the same
// business-day boundary reports/EOD use.
func (r *POSRepo) CountFiscalDeviceReceiptsSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fiscal_device_receipts WHERE created_at >= ?
`, since.UTC().Format("2006-01-02 15:04:05")).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count fiscal_device_receipts: %w", err)
	}
	return n, nil
}
