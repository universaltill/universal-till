-- 040: reset-transactions archives instead of deletes (ADR-0042, ut-docs#187).
-- POSRepo.ResetTransactionHistory no longer destroys transactional data: every
-- row it clears is moved into a *_archive twin, tagged with one reset_batches
-- row per reset event, and is restorable whole-batch as long as the till has
-- not traded since (see reset_archive_repo.go). report_archive is NOT part of
-- this mechanism (ADR-0040 §9) — it is a separate retained legal record with
-- its own retention pruning.
--
-- Archive tables are column-identical to their live counterparts (001_init,
-- 002_held_sales, 016_invoices + every later ALTER: 015 till_id, 019
-- tip_amount, 024 service_charge_amount, 026 order_type, 033 order_status/
-- order_status_updated_at, 035 print-failure stamps) plus reset_batch_id,
-- with three deliberate relaxations:
--   * no FKs to live tables (an archived row must not pin — or be broken
--     by — live rows that can change or vanish after the reset; the
--     sale_id/sale_line_id links only make sense WITHIN the same batch);
--   * no PRIMARY KEY / UNIQUE constraints (a shop that resets, trades, and
--     resets again legitimately archives the same receipt_no/display_no in
--     two different batches — cross-batch uniqueness must not be enforced);
--   * NOT NULL and single-table CHECK constraints are kept.
--
-- sale_line_modifiers gets an archive twin too: it is not in the reset
-- code's explicit DELETE list, but its ON DELETE CASCADE from sale_lines
-- means a reset destroys it just the same — ADR-0042 §1's rule is one
-- archive table per table reset touches, and "destroys nothing" would be
-- silently false without it.

CREATE TABLE IF NOT EXISTS reset_batches (
    id          TEXT PRIMARY KEY,
    created_at  TEXT NOT NULL,
    actor_id    TEXT,
    sales_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sales_archive (
    id              TEXT NOT NULL,
    receipt_no      TEXT NOT NULL,
    status          TEXT NOT NULL,
    sale_type       TEXT NOT NULL,
    tender_type     TEXT NOT NULL,
    offline         INTEGER NOT NULL,
    sync_status     TEXT NOT NULL,
    sync_attempts   INTEGER NOT NULL,
    sync_next_attempt_at TEXT,
    sync_last_error TEXT,
    register_id     TEXT,
    cashier_id      TEXT,
    customer_id     TEXT,
    currency        TEXT NOT NULL,
    subtotal        INTEGER NOT NULL,
    discount_total  INTEGER NOT NULL,
    tax_total       INTEGER NOT NULL,
    total           INTEGER NOT NULL,
    rounding        INTEGER NOT NULL,
    note            TEXT,
    created_at      TEXT NOT NULL,
    completed_at    TEXT,
    voided_at       TEXT,
    till_id         TEXT NOT NULL,
    service_charge_amount INTEGER NOT NULL,
    order_type      TEXT NOT NULL,
    order_status    TEXT NOT NULL,
    order_status_updated_at TEXT,
    kitchen_print_failed_at TEXT,
    receipt_print_failed_at TEXT,
    reset_batch_id  TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_sales_archive_batch ON sales_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS sale_lines_archive (
    id               TEXT NOT NULL,
    sale_id          TEXT NOT NULL,
    line_no          INTEGER NOT NULL,
    item_id          TEXT,
    variant_id       TEXT,
    name_snapshot    TEXT NOT NULL,
    sku_snapshot     TEXT,
    barcode_snapshot TEXT,
    quantity         REAL NOT NULL,
    unit_price       INTEGER NOT NULL,
    line_discount    INTEGER NOT NULL,
    tax_rate_bp      INTEGER NOT NULL,
    tax_amount       INTEGER NOT NULL,
    total_before_tax INTEGER NOT NULL,
    total_after_tax  INTEGER NOT NULL,
    reset_batch_id   TEXT NOT NULL REFERENCES reset_batches (id),
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_sale_lines_archive_batch ON sale_lines_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS sale_line_modifiers_archive (
    id                   TEXT NOT NULL,
    sale_line_id         TEXT NOT NULL,
    group_id             TEXT,
    option_id            TEXT,
    group_name_snapshot  TEXT NOT NULL,
    option_name_snapshot TEXT NOT NULL,
    price_delta_minor    INTEGER NOT NULL,
    reset_batch_id       TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_sale_line_modifiers_archive_batch ON sale_line_modifiers_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS sale_discounts_archive (
    id             TEXT NOT NULL,
    sale_id        TEXT NOT NULL,
    line_id        TEXT,
    type           TEXT NOT NULL,
    value          INTEGER NOT NULL,
    amount         INTEGER NOT NULL,
    reason         TEXT,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_sale_discounts_archive_batch ON sale_discounts_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS sale_links_archive (
    id               TEXT NOT NULL,
    sale_id          TEXT NOT NULL,
    original_sale_id TEXT NOT NULL,
    reason           TEXT,
    reset_batch_id   TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_sale_links_archive_batch ON sale_links_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS payments_archive (
    id             TEXT NOT NULL,
    sale_id        TEXT NOT NULL,
    method_id      TEXT NOT NULL,
    amount         INTEGER NOT NULL,
    currency       TEXT NOT NULL,
    reference      TEXT,
    change_given   INTEGER NOT NULL,
    paid_at        TEXT NOT NULL,
    tip_amount     INTEGER NOT NULL,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_payments_archive_batch ON payments_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS invoices_archive (
    id                  TEXT NOT NULL,
    series              TEXT NOT NULL,
    invoice_no          INTEGER NOT NULL,
    display_no          TEXT NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('invoice','credit_note')),
    sale_id             TEXT NOT NULL,
    original_invoice_id TEXT,
    customer_name       TEXT NOT NULL,
    customer_address    TEXT NOT NULL,
    customer_vat_no     TEXT NOT NULL,
    seller_json         TEXT NOT NULL,
    net_total           INTEGER NOT NULL,
    tax_total           INTEGER NOT NULL,
    gross_total         INTEGER NOT NULL,
    vat_breakdown_json  TEXT NOT NULL,
    issued_at           TEXT NOT NULL,
    issued_by           TEXT NOT NULL,
    reset_batch_id      TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_invoices_archive_batch ON invoices_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS held_sales_archive (
    id             TEXT NOT NULL,
    label          TEXT NOT NULL,
    total_minor    INTEGER NOT NULL,
    line_count     INTEGER NOT NULL,
    payload        TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_held_sales_archive_batch ON held_sales_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS shifts_archive (
    id             TEXT NOT NULL,
    register_id    TEXT NOT NULL,
    cashier_id     TEXT NOT NULL,
    opened_at      TEXT NOT NULL,
    closed_at      TEXT,
    opening_cash   INTEGER NOT NULL,
    closing_cash   INTEGER,
    expected_cash  INTEGER,
    note           TEXT,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_shifts_archive_batch ON shifts_archive (reset_batch_id);

CREATE TABLE IF NOT EXISTS stock_movements_archive (
    id             TEXT NOT NULL,
    item_id        TEXT,
    variant_id     TEXT,
    location_id    TEXT NOT NULL,
    sale_line_id   TEXT,
    type           TEXT NOT NULL,
    quantity       REAL NOT NULL,
    cost_price     INTEGER,
    created_at     TEXT NOT NULL,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id),
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_stock_movements_archive_batch ON stock_movements_archive (reset_batch_id);
