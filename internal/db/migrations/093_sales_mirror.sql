-- 093_sales_mirror.sql — ut-docs#5074 (ADR-0011 and ADR-0114, "Amendment
-- 2026-10-26"; design ut-docs reference/design-5074-mirror-sales-to-replicas.md
-- §3; wire contract reference/contracts/pos-lan-sales-mirror.md).
--
-- Every additional till keeps a READ-ONLY copy of the shop's sales and day
-- closes, so any of them can be promoted with nothing lost. This migration
-- adds only the storage and the main till's change log; nothing reads or
-- serves it yet.
--
-- Ships DARK and INERT. The shop setting sales_mirror.enabled has no row on
-- any till today, so it is off. The change-log triggers below are installed
-- but are no-ops while it is off: their WHEN clause reads that settings row
-- first (one primary-key lookup) and writes nothing. No table here is
-- written on the sale path while the setting is off. Pinned by
-- internal/pos/sales_mirror_log_test.go (TestSalesMirrorLog_Off*).
--
-- 1. The copy (mirror_*) — written only on an additional till, only by
--    internal/data/sales_mirror_repo.go. It is NEVER the sales table: no
--    report, Z report, stock figure, receipt counter, cloud upload or
--    journal push reads these tables, which is how "never double counted"
--    holds by construction. scripts/ci/guard-sales-mirror-isolation.sh keeps
--    every reader inside that one repository. No foreign key to the
--    catalogue, users, customers or registers, so a copied row never fails
--    on a row this till lacks; internal keys only, cascading from
--    mirror_sales. receipt_no is indexed but NOT unique: a copy never fails.
-- 2. The change log (sales_mirror_log, sales_mirror_acks) — main till. A
--    trigger writes one pointer row in the SAME transaction as the sale or
--    close it observes (the 023 sync_admin_version precedent). Once the
--    setting is on, a trigger that failed would abort that sale; the body is
--    one INSERT into a table whose every other column has a default, so only
--    a corrupt database can make it fail. A replica
--    (sync.primary_url set) and a till with its own kill switch
--    (sync.sales_mirror_off = '1') log nothing.
-- 3. mirror_state — this till's own cursor, epoch and pause state; purely
--    local, written in the same transaction as each applied page.
--
-- Safe on a database that already has data: CREATE ... IF NOT EXISTS only,
-- no backfill, no change to any existing table or row. Re-appliable (the
-- fiscal_signing_keys_* tests rewind the ledger and re-run later files).

CREATE TABLE IF NOT EXISTS mirror_sales (
    id                          TEXT PRIMARY KEY,
    src_seq                     INTEGER NOT NULL,
    epoch                       TEXT NOT NULL,
    origin_till_id              TEXT NOT NULL,
    origin_till_name            TEXT NOT NULL DEFAULT '',
    receipt_no                  TEXT NOT NULL,
    display_no                  TEXT,
    status                      TEXT NOT NULL,
    sale_type                   TEXT NOT NULL,
    tender_type                 TEXT NOT NULL,
    order_type                  TEXT NOT NULL DEFAULT '',
    register_id                 TEXT,
    cashier_id                  TEXT,
    customer_id                 TEXT,
    table_id                    TEXT,
    currency                    TEXT NOT NULL,
    subtotal                    INTEGER NOT NULL,
    discount_total              INTEGER NOT NULL,
    tax_total                   INTEGER NOT NULL,
    total                       INTEGER NOT NULL,
    rounding                    INTEGER NOT NULL,
    service_charge_amount       INTEGER NOT NULL,
    service_charge_tax_basis_bp INTEGER NOT NULL,
    voucher_issue_total         INTEGER NOT NULL,
    note                        TEXT,
    offline                     INTEGER NOT NULL,
    created_at                  TEXT NOT NULL,
    completed_at                TEXT,
    voided_at                   TEXT,
    local_date                  TEXT NOT NULL,
    voided_local_date           TEXT,
    received_at                 TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mirror_sales_local_date ON mirror_sales (local_date);
CREATE INDEX IF NOT EXISTS idx_mirror_sales_origin_created ON mirror_sales (origin_till_id, created_at);
CREATE INDEX IF NOT EXISTS idx_mirror_sales_receipt ON mirror_sales (receipt_no);

CREATE TABLE IF NOT EXISTS mirror_sale_lines (
    id                TEXT PRIMARY KEY,
    sale_id           TEXT NOT NULL REFERENCES mirror_sales(id) ON DELETE CASCADE,
    line_no           INTEGER NOT NULL,
    item_id           TEXT,
    variant_id        TEXT,
    name_snapshot     TEXT NOT NULL,
    sku_snapshot      TEXT,
    barcode_snapshot  TEXT,
    quantity          REAL NOT NULL,
    unit_price        INTEGER NOT NULL,
    line_discount     INTEGER NOT NULL,
    tax_rate_bp       INTEGER NOT NULL,
    tax_amount        INTEGER NOT NULL,
    total_before_tax  INTEGER NOT NULL,
    total_after_tax   INTEGER NOT NULL,
    order_type        TEXT NOT NULL DEFAULT '',
    refund_of_line_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_mirror_sale_lines_sale ON mirror_sale_lines (sale_id);

CREATE TABLE IF NOT EXISTS mirror_sale_line_modifiers (
    id                   TEXT PRIMARY KEY,
    sale_line_id         TEXT NOT NULL REFERENCES mirror_sale_lines(id) ON DELETE CASCADE,
    group_id             TEXT,
    option_id            TEXT,
    group_name_snapshot  TEXT NOT NULL,
    option_name_snapshot TEXT NOT NULL,
    price_delta_minor    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mirror_sale_line_modifiers_line ON mirror_sale_line_modifiers (sale_line_id);

CREATE TABLE IF NOT EXISTS mirror_payments (
    id            TEXT PRIMARY KEY,
    sale_id       TEXT NOT NULL REFERENCES mirror_sales(id) ON DELETE CASCADE,
    method_id     TEXT NOT NULL,
    amount        INTEGER NOT NULL,
    currency      TEXT NOT NULL,
    change_given  INTEGER NOT NULL,
    tip_amount    INTEGER NOT NULL,
    tip_recipient TEXT NOT NULL DEFAULT 'employee',
    paid_at       TEXT NOT NULL,
    local_date    TEXT NOT NULL DEFAULT '',
    voucher_id    TEXT,
    reference     TEXT,
    masked_pan    TEXT,
    auth_code     TEXT,
    terminal_id   TEXT,
    trace_id      TEXT
);
CREATE INDEX IF NOT EXISTS idx_mirror_payments_sale ON mirror_payments (sale_id);

CREATE TABLE IF NOT EXISTS mirror_sale_charges (
    sale_id      TEXT NOT NULL REFERENCES mirror_sales(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    key          TEXT NOT NULL,
    label        TEXT NOT NULL DEFAULT '',
    amount_minor INTEGER NOT NULL,
    tax_basis_bp INTEGER NOT NULL DEFAULT 0,
    base         TEXT NOT NULL DEFAULT 'net_lines',
    PRIMARY KEY (sale_id, seq)
);

CREATE TABLE IF NOT EXISTS mirror_sale_discounts (
    id      TEXT PRIMARY KEY,
    sale_id TEXT NOT NULL REFERENCES mirror_sales(id) ON DELETE CASCADE,
    line_id TEXT,
    type    TEXT NOT NULL,
    value   INTEGER NOT NULL,
    amount  INTEGER NOT NULL,
    reason  TEXT
);
CREATE INDEX IF NOT EXISTS idx_mirror_sale_discounts_sale ON mirror_sale_discounts (sale_id);

CREATE TABLE IF NOT EXISTS mirror_sale_links (
    id               TEXT PRIMARY KEY,
    sale_id          TEXT NOT NULL REFERENCES mirror_sales(id) ON DELETE CASCADE,
    original_sale_id TEXT NOT NULL,
    reason           TEXT
);
CREATE INDEX IF NOT EXISTS idx_mirror_sale_links_sale ON mirror_sale_links (sale_id);

CREATE TABLE IF NOT EXISTS mirror_closes (
    origin_till_id   TEXT NOT NULL,
    kind             TEXT NOT NULL,
    period           TEXT NOT NULL,
    src_id           TEXT NOT NULL,
    origin_till_name TEXT NOT NULL DEFAULT '',
    z_number         INTEGER,
    prev_z_number    INTEGER,
    prev_closed_at   TEXT,
    first_receipt    TEXT,
    last_receipt     TEXT,
    content_json     TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    src_seq          INTEGER NOT NULL,
    epoch            TEXT NOT NULL,
    received_at      TEXT NOT NULL,
    PRIMARY KEY (origin_till_id, kind, period)
);
CREATE INDEX IF NOT EXISTS idx_mirror_closes_created ON mirror_closes (created_at);

CREATE TABLE IF NOT EXISTS mirror_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sales_mirror_log (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    op             TEXT NOT NULL,
    ref_id         TEXT NOT NULL,
    origin_till_id TEXT NOT NULL DEFAULT '',
    logged_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_sales_mirror_log_logged_at ON sales_mirror_log (logged_at);

CREATE TABLE IF NOT EXISTS sales_mirror_acks (
    till_id   TEXT PRIMARY KEY,
    epoch     TEXT NOT NULL,
    acked_seq INTEGER NOT NULL,
    acked_at  TEXT NOT NULL
);

-- The gate, identical in every trigger: SQLite stops evaluating an AND at
-- the first false term, so with the setting off (no row, today's state
-- everywhere) each trigger costs at most ONE settings primary-key lookup
-- (after the in-row status test on sales) and writes nothing.
CREATE TRIGGER IF NOT EXISTS trg_sales_mirror_log_sale_ins AFTER INSERT ON sales
WHEN NEW.status IN ('completed', 'voided')
 AND (SELECT value FROM settings WHERE key = 'sales_mirror.enabled') = '1'
 AND TRIM(COALESCE((SELECT value FROM settings WHERE key = 'sync.primary_url'), '')) = ''
 AND COALESCE((SELECT value FROM settings WHERE key = 'sync.sales_mirror_off'), '0') <> '1'
BEGIN
  INSERT INTO sales_mirror_log (op, ref_id, origin_till_id) VALUES ('sale', NEW.id, NEW.till_id);
END;

-- Only the columns a copy shows or that change the record's meaning, and
-- only when one actually changed (IS NOT: NULL-safe), so the frequent
-- order_status / print-failure / sync bookkeeping writes never log.
-- customer_id is here so an erasure on the main till reaches every copy.
CREATE TRIGGER IF NOT EXISTS trg_sales_mirror_log_sale_upd
AFTER UPDATE OF status, till_id, created_at, local_date, voided_at, voided_local_date, completed_at, total, customer_id, note, receipt_no ON sales
WHEN NEW.status IN ('completed', 'voided')
 AND (SELECT value FROM settings WHERE key = 'sales_mirror.enabled') = '1'
 AND (OLD.status IS NOT NEW.status OR OLD.till_id IS NOT NEW.till_id
      OR OLD.created_at IS NOT NEW.created_at OR OLD.local_date IS NOT NEW.local_date
      OR OLD.voided_at IS NOT NEW.voided_at OR OLD.voided_local_date IS NOT NEW.voided_local_date
      OR OLD.completed_at IS NOT NEW.completed_at OR OLD.total IS NOT NEW.total
      OR OLD.customer_id IS NOT NEW.customer_id OR OLD.note IS NOT NEW.note
      OR OLD.receipt_no IS NOT NEW.receipt_no)
 AND TRIM(COALESCE((SELECT value FROM settings WHERE key = 'sync.primary_url'), '')) = ''
 AND COALESCE((SELECT value FROM settings WHERE key = 'sync.sales_mirror_off'), '0') <> '1'
BEGIN
  INSERT INTO sales_mirror_log (op, ref_id, origin_till_id) VALUES ('sale', NEW.id, NEW.till_id);
END;

CREATE TRIGGER IF NOT EXISTS trg_sales_mirror_log_close_ins AFTER INSERT ON report_archive
WHEN (SELECT value FROM settings WHERE key = 'sales_mirror.enabled') = '1'
 AND TRIM(COALESCE((SELECT value FROM settings WHERE key = 'sync.primary_url'), '')) = ''
 AND COALESCE((SELECT value FROM settings WHERE key = 'sync.sales_mirror_off'), '0') <> '1'
BEGIN
  INSERT INTO sales_mirror_log (op, ref_id, origin_till_id) VALUES ('close', NEW.id, '');
END;

-- A revoked or removed till leaves the prune minimum at once (design §4.3):
-- its acknowledgement goes in the same transaction as its tills row, from
-- every path that deletes one (revoke, a cloud tombstone). With no ack row
-- (the setting was never on) this deletes nothing.
CREATE TRIGGER IF NOT EXISTS trg_sales_mirror_acks_till_del AFTER DELETE ON tills
BEGIN
  DELETE FROM sales_mirror_acks WHERE till_id = OLD.id;
END;
