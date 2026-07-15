package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// InvoiceRepo persists VAT invoices and credit notes (G31, docs:
// architecture/invoicing.md). Numbering is gapless per series — allocated
// as MAX+1 inside the insert transaction and pinned by
// ux_invoices_series_no.
type InvoiceRepo struct{ db *sql.DB }

func NewInvoiceRepo(db *sql.DB) *InvoiceRepo { return &InvoiceRepo{db: db} }

type InvoiceRow struct {
	ID                string
	Series            string
	InvoiceNo         int64
	DisplayNo         string
	Kind              string // invoice | credit_note
	SaleID            string
	OriginalInvoiceID string
	CustomerName      string
	CustomerAddress   string
	CustomerVATNo     string
	SellerJSON        string
	NetTotal          int64
	TaxTotal          int64
	GrossTotal        int64
	VATBreakdownJSON  string
	IssuedAt          string
	IssuedBy          string
}

// InvoiceInput is everything the caller decides; numbering is ours.
type InvoiceInput struct {
	Series            string // till receipt prefix ('' on the primary)
	Kind              string
	SaleID            string
	OriginalInvoiceID string
	CustomerName      string
	CustomerAddress   string
	CustomerVATNo     string
	SellerJSON        string
	NetTotal          int64
	TaxTotal          int64
	GrossTotal        int64
	VATBreakdownJSON  string
	IssuedAt          string
	IssuedBy          string
}

// Create allocates the next number in the series and inserts the invoice.
// Allocation happens INSIDE one INSERT…SELECT — a single write statement,
// so there is no reader→writer upgrade window for two concurrent issues
// to deadlock in; a lost race surfaces as a UNIQUE violation and is
// retried.
func (r *InvoiceRepo) Create(ctx context.Context, in InvoiceInput) (InvoiceRow, error) {
	var orig any
	if in.OriginalInvoiceID != "" {
		orig = in.OriginalInvoiceID
	}
	id := uuid.NewString()
	var lastErr error
	for range 3 {
		_, err := r.db.ExecContext(ctx, `
INSERT INTO invoices (id, series, invoice_no, display_no, kind, sale_id,
  original_invoice_id, customer_name, customer_address, customer_vat_no,
  seller_json, net_total, tax_total, gross_total, vat_breakdown_json,
  issued_at, issued_by)
SELECT ?, ?, n, ? || 'INV-' || printf('%06d', n), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
FROM (SELECT COALESCE(MAX(invoice_no), 0) + 1 AS n FROM invoices WHERE series = ?)`,
			id, in.Series, in.Series, in.Kind, in.SaleID,
			orig, in.CustomerName, in.CustomerAddress, in.CustomerVATNo,
			in.SellerJSON, in.NetTotal, in.TaxTotal, in.GrossTotal,
			in.VATBreakdownJSON, in.IssuedAt, in.IssuedBy, in.Series)
		if err == nil {
			row, ok, err := r.ByID(ctx, id)
			if err != nil || !ok {
				return InvoiceRow{}, fmt.Errorf("read back invoice: %w", err)
			}
			return row, nil
		}
		lastErr = err
		// UNIQUE(sale_id, kind) means "already invoiced" — never retry that.
		if strings.Contains(err.Error(), "sale_id") {
			break
		}
	}
	return InvoiceRow{}, fmt.Errorf("insert invoice: %w", lastErr)
}

const invoiceCols = `id, series, invoice_no, display_no, kind, sale_id,
COALESCE(original_invoice_id, ''), customer_name, customer_address,
customer_vat_no, seller_json, net_total, tax_total, gross_total,
vat_breakdown_json, issued_at, issued_by`

func scanInvoice(row *sql.Row) (InvoiceRow, bool, error) {
	var v InvoiceRow
	err := row.Scan(&v.ID, &v.Series, &v.InvoiceNo, &v.DisplayNo, &v.Kind,
		&v.SaleID, &v.OriginalInvoiceID, &v.CustomerName, &v.CustomerAddress,
		&v.CustomerVATNo, &v.SellerJSON, &v.NetTotal, &v.TaxTotal,
		&v.GrossTotal, &v.VATBreakdownJSON, &v.IssuedAt, &v.IssuedBy)
	if err == sql.ErrNoRows {
		return InvoiceRow{}, false, nil
	}
	if err != nil {
		return InvoiceRow{}, false, fmt.Errorf("scan invoice: %w", err)
	}
	return v, true, nil
}

// BySale finds a sale's invoice or credit note.
func (r *InvoiceRepo) BySale(ctx context.Context, saleID, kind string) (InvoiceRow, bool, error) {
	return scanInvoice(r.db.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices WHERE sale_id = ? AND kind = ?`, saleID, kind))
}

// ByDisplayNo loads an invoice by its printed number.
func (r *InvoiceRepo) ByDisplayNo(ctx context.Context, displayNo string) (InvoiceRow, bool, error) {
	return scanInvoice(r.db.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices WHERE display_no = ?`, displayNo))
}

// ByID loads an invoice by primary key.
func (r *InvoiceRepo) ByID(ctx context.Context, id string) (InvoiceRow, bool, error) {
	return scanInvoice(r.db.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices WHERE id = ?`, id))
}

// InvoiceListItem is one row of the invoices page / accountant export,
// joined with the sale's receipt number.
type InvoiceListItem struct {
	DisplayNo     string
	Kind          string
	IssuedAt      string
	CustomerName  string
	CustomerVATNo string
	ReceiptNo     string
	NetTotal      int64
	TaxTotal      int64
	GrossTotal    int64
}

// List returns invoices issued in [from, to] (RFC3339 date prefixes are
// fine — comparison is lexicographic on the stored RFC3339 strings),
// newest first. Empty bounds mean open-ended.
func (r *InvoiceRepo) List(ctx context.Context, from, to string) ([]InvoiceListItem, error) {
	if to != "" {
		to += "￿" // include the whole final day for date-only bounds
	} else {
		to = "￿"
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT i.display_no, i.kind, i.issued_at, i.customer_name, i.customer_vat_no,
       s.receipt_no, i.net_total, i.tax_total, i.gross_total
FROM invoices i JOIN sales s ON s.id = i.sale_id
WHERE i.issued_at >= ? AND i.issued_at <= ?
ORDER BY i.issued_at DESC, i.display_no DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list invoices: %w", err)
	}
	defer rows.Close()
	var out []InvoiceListItem
	for rows.Next() {
		var it InvoiceListItem
		if err := rows.Scan(&it.DisplayNo, &it.Kind, &it.IssuedAt, &it.CustomerName,
			&it.CustomerVATNo, &it.ReceiptNo, &it.NetTotal, &it.TaxTotal, &it.GrossTotal); err != nil {
			return nil, fmt.Errorf("scan invoice list: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
