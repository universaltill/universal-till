package data

import (
	"context"
	"database/sql"
	"fmt"

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
func (r *InvoiceRepo) Create(ctx context.Context, in InvoiceInput) (InvoiceRow, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return InvoiceRow{}, fmt.Errorf("create invoice: %w", err)
	}
	defer tx.Rollback()

	var next int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(invoice_no), 0) + 1 FROM invoices WHERE series = ?`,
		in.Series).Scan(&next); err != nil {
		return InvoiceRow{}, fmt.Errorf("allocate invoice no: %w", err)
	}
	display := fmt.Sprintf("%sINV-%06d", in.Series, next)

	row := InvoiceRow{
		ID: uuid.NewString(), Series: in.Series, InvoiceNo: next, DisplayNo: display,
		Kind: in.Kind, SaleID: in.SaleID, OriginalInvoiceID: in.OriginalInvoiceID,
		CustomerName: in.CustomerName, CustomerAddress: in.CustomerAddress,
		CustomerVATNo: in.CustomerVATNo, SellerJSON: in.SellerJSON,
		NetTotal: in.NetTotal, TaxTotal: in.TaxTotal, GrossTotal: in.GrossTotal,
		VATBreakdownJSON: in.VATBreakdownJSON, IssuedAt: in.IssuedAt, IssuedBy: in.IssuedBy,
	}
	var orig any
	if row.OriginalInvoiceID != "" {
		orig = row.OriginalInvoiceID
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO invoices (id, series, invoice_no, display_no, kind, sale_id,
  original_invoice_id, customer_name, customer_address, customer_vat_no,
  seller_json, net_total, tax_total, gross_total, vat_breakdown_json,
  issued_at, issued_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.Series, row.InvoiceNo, row.DisplayNo, row.Kind, row.SaleID,
		orig, row.CustomerName, row.CustomerAddress, row.CustomerVATNo,
		row.SellerJSON, row.NetTotal, row.TaxTotal, row.GrossTotal,
		row.VATBreakdownJSON, row.IssuedAt, row.IssuedBy); err != nil {
		return InvoiceRow{}, fmt.Errorf("insert invoice: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return InvoiceRow{}, fmt.Errorf("commit invoice: %w", err)
	}
	return row, nil
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
