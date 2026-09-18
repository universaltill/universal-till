package data

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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

// FiscalDeviceWindow is the ÖKC evidence recorded in one EOD window.
type FiscalDeviceWindow struct {
	Serial string `json:"serial"` // from the latest receipt in window
	Maker  string `json:"maker"`
	// ZNos is every distinct z_no seen in the window, ascending — more
	// than one entry means the device Z-closed mid-window (e.g. the shop
	// ran the till's own EOD later than the device's own daily close).
	ZNos      []int64 `json:"z_nos"`
	MaliFis   int     `json:"mali_fis"`
	IadeFisi  int     `json:"iade_fisi"`
	BilgiFisi int     `json:"bilgi_fisi"`
	// Other counts any receipt_kind value besides the three above (free
	// text from the plugin — see FiscalDeviceReceipt.ReceiptKind).
	Other int `json:"other"`
	Total int `json:"total"`
	// TillOKCTenders is the till's OWN count of payments tendered on the
	// "okc" method for sales in the same window (completed sales and
	// returns alike — the same status='completed' scope
	// dateRangeSummaryInstant's Methods breakdown uses, so the two figures
	// are directly comparable) — what the accountant reconciles the
	// device's own receipt count against.
	//
	// The two counts are NOT always directly comparable even when they
	// disagree, so a mismatch is a prompt to look, never a verdict by
	// itself:
	//
	//  (a) A zero-total RETURN writes no payments row at all —
	//      refundPayments returns nil when refundTotal is zero
	//      (ut-docs#1561) — but payment.okc.refund is still dispatched and
	//      is fail-closed (ut-docs#1788): it records a device receipt
	//      (iade fişi) even though there was nothing to tender. That is a
	//      permanent, structural +1 on the device side that has nothing to
	//      do with a bookkeeping mistake.
	//  (b) A sale tendered on "okc" and later moved off
	//      status='completed' (voided or refunded via UpdateSaleStatus)
	//      drops out of this count on the till side, but its
	//      fiscal_device_receipts row is never retracted — the device
	//      already printed it. That is the till's own later bookkeeping
	//      changing the picture, not the device disagreeing with anything.
	TillOKCTenders int `json:"till_okc_tenders"`
}

// Empty reports whether this window has no evidence at all — no device
// receipts AND no till tenders on the device. This is deliberately NOT the
// same thing as Total == TillOKCTenders (which would be true, and print as
// a false "MATCH", for a window where the fiscal-device plugin simply never
// ran): callers use Empty to render an explicit "no activity" state instead
// of a reconciliation verdict for a period with nothing to reconcile.
func (w FiscalDeviceWindow) Empty() bool {
	return w.Total == 0 && w.TillOKCTenders == 0 && len(w.ZNos) == 0
}

// FiscalDeviceWindow reports the ÖKC device's evidence for [from, to) —
// windowed on fiscal_device_receipts.created_at via instantWindow, the
// same half-open close-to-close semantics EndOfDayInstant uses — alongside
// the till's own count of "okc"-method tenders in the SAME window, read
// from payments/sales so the two are for one, identical period.
//
// methodID is taken as a parameter, not a package constant, because
// internal/data cannot import internal/fiscal (fiscal.MethodKeyOKC lives
// there); callers in internal/pages pass fiscal.MethodKeyOKC.
func (r *POSRepo) FiscalDeviceWindow(ctx context.Context, methodID string, from, to time.Time) (FiscalDeviceWindow, error) {
	var win FiscalDeviceWindow

	fwin, fargs := instantWindow("created_at", from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT serial, maker, receipt_kind, z_no
FROM fiscal_device_receipts
WHERE `+fwin+`
ORDER BY datetime(created_at) ASC, rowid ASC
`, fargs...)
	if err != nil {
		return win, fmt.Errorf("select fiscal_device_receipts window: %w", err)
	}
	defer rows.Close()

	zSeen := map[int64]bool{}
	for rows.Next() {
		var serial, maker, kind string
		var zNo int64
		if err := rows.Scan(&serial, &maker, &kind, &zNo); err != nil {
			return win, fmt.Errorf("scan fiscal_device_receipts window: %w", err)
		}
		// Overwritten every row, in ascending created_at order, so what
		// survives the loop is the LATEST receipt's serial/maker.
		win.Serial = serial
		win.Maker = maker
		if !zSeen[zNo] {
			zSeen[zNo] = true
			win.ZNos = append(win.ZNos, zNo)
		}
		switch kind {
		case "mali_fis":
			win.MaliFis++
		case "iade_fisi":
			win.IadeFisi++
		case "bilgi_fisi":
			win.BilgiFisi++
		default:
			win.Other++
		}
		win.Total++
	}
	if err := rows.Err(); err != nil {
		return win, fmt.Errorf("scan fiscal_device_receipts window: %w", err)
	}
	sort.Slice(win.ZNos, func(i, j int) bool { return win.ZNos[i] < win.ZNos[j] })

	// Till's own tender count on the device method, same window, same
	// status='completed' scope as dateRangeSummaryInstant's Methods
	// breakdown (sale_type distinguishes a sale from a return; both carry
	// status='completed') so this is directly comparable to win.Total.
	twin, targs := instantWindow("s.created_at", from, to)
	args := append([]any{methodID}, targs...)
	err = r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE p.method_id = ? AND s.status = 'completed' AND `+twin, args...).Scan(&win.TillOKCTenders)
	if err != nil {
		return win, fmt.Errorf("count till okc tenders: %w", err)
	}
	return win, nil
}
