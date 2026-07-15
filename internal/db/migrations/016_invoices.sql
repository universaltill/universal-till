-- 016: VAT invoices + credit notes (G31, docs: architecture/invoicing.md).
-- An invoice is immutable evidence: buyer/seller identity and totals are
-- SNAPSHOTS, never joins. Numbering is gapless per series (= the till's
-- receipt prefix), pinned by the unique index.
CREATE TABLE IF NOT EXISTS invoices (
    id                  TEXT PRIMARY KEY,
    series              TEXT NOT NULL,              -- '' on the primary, 'T2-' on a replica
    invoice_no          INTEGER NOT NULL,           -- gapless within the series
    display_no          TEXT NOT NULL UNIQUE,       -- e.g. INV-000042 / T2-INV-000007
    kind                TEXT NOT NULL DEFAULT 'invoice' CHECK (kind IN ('invoice','credit_note')),
    sale_id             TEXT NOT NULL,
    original_invoice_id TEXT,                       -- credit notes point at their invoice
    customer_name       TEXT NOT NULL,
    customer_address    TEXT NOT NULL DEFAULT '',
    customer_vat_no     TEXT NOT NULL DEFAULT '',
    seller_json         TEXT NOT NULL,              -- {name,address,vat_no} at issue time
    net_total           INTEGER NOT NULL,           -- minor units
    tax_total           INTEGER NOT NULL,
    gross_total         INTEGER NOT NULL,
    vat_breakdown_json  TEXT NOT NULL,              -- [{rate_bp,net,tax,gross}]
    issued_at           TEXT NOT NULL,
    issued_by           TEXT NOT NULL,
    FOREIGN KEY (sale_id)             REFERENCES sales (id),
    FOREIGN KEY (original_invoice_id) REFERENCES invoices (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_invoices_series_no ON invoices (series, invoice_no);
-- One invoice and at most one credit note per sale.
CREATE UNIQUE INDEX IF NOT EXISTS ux_invoices_sale_kind ON invoices (sale_id, kind);
CREATE INDEX IF NOT EXISTS idx_invoices_issued ON invoices (issued_at);
