-- 035_shrinkage_events.sql — universaltill/ut-docs#1465 (G41): structured
-- void/comp/waste management with shrinkage reporting. Distinct from a
-- refund (G27, sale_links + sales.sale_type='return') — this table records
-- a PRE-tender basket-line removal, before any sale row exists at all, so
-- it cannot live on sales/sale_lines.
--
-- reason_category is one of 'void' (mis-ring/order change), 'comp'
-- (goodwill zero-charge to the customer) or 'waste' (spoilage/kitchen
-- mistake/dropped item) — internal/pos.ShrinkageReason{Void,Comp,Waste},
-- the same fixed vocabulary the CHECK constraint below enforces at the
-- schema level. A CHECK on a constrained text column mirrors sale_lines'
-- own item_id/variant_id CHECK in 001_init.sql — the closest existing
-- precedent for "the DB itself refuses a value outside a fixed set".
--
-- item_id/item_name/sku: item_name is DENORMALIZED (a snapshot at the time
-- of the event), same convention as sale_lines.name_snapshot/sku_snapshot
-- in 001_init.sql — the item can be renamed or deleted later, and this
-- row must still read sensibly. item_id therefore carries NO ON DELETE
-- CASCADE (deliberately unlike sale_lines' own FOREIGN KEY (item_id)
-- REFERENCES items (id), which has no cascade either, for the same
-- reason: a shrinkage event is a historical record, not a live reference,
-- and must survive the referenced item's deletion).
--
-- unit_price_minor/extended_value_minor are money (internal/money.Money)
-- stored as raw minor-unit integers at this DB boundary, same convention
-- as sale_lines.unit_price/total_before_tax. extended_value_minor is
-- precomputed (quantity × unit price, money.Money.MulQty) so reporting
-- queries never need to redo the multiplication.
--
-- actor_id is the session user who initiated the removal; approver_id is
-- NULL unless a manager PIN was actually used to elevate past the
-- void_comp_waste permission gate (internal/pages/elevation.go's
-- checkOrElevate) — NULL means the session user already held the
-- permission directly. Neither carries a FOREIGN KEY to users(id): unlike
-- shifts/sales (a live operational reference), a shrinkage event is an
-- append-only historical record that must survive a later user deletion,
-- same reasoning as audit_log.actor_id's own nullable, unconstrained
-- column in 001_init.sql.
--
-- register_id mirrors sales.register_id (TEXT, REFERENCES registers(id),
-- no ON DELETE — 001_init.sql) — the till that recorded the event.
--
-- created_at is TEXT, app-supplied at insert time (no DB-side DEFAULT),
-- matching kiosk_counter_orders.created_at (026) and audit_log's own
-- insert-time-supplied convention (POSRepo.InsertAudit's createdAt
-- parameter) rather than sales/shifts' `DEFAULT (datetime('now'))` —
-- InsertShrinkageEvent takes createdAt explicit, same signature style as
-- InsertAudit, so both rows from one /api/pos/remove request carry
-- identical timestamps.
--
-- CREATE TABLE IF NOT EXISTS / INSERT OR IGNORE throughout, same
-- replay-safety convention as every migration since 021: this repo's
-- fiscal_signing_keys_{rename,split}_test.go-style tests rewind the ledger
-- and re-run every later migration against an already-migrated database.
CREATE TABLE IF NOT EXISTS shrinkage_events (
    id                   TEXT PRIMARY KEY,
    reason_category      TEXT NOT NULL CHECK (reason_category IN ('void', 'comp', 'waste')),
    item_id              TEXT,
    item_name            TEXT NOT NULL,
    sku                  TEXT,
    quantity             REAL NOT NULL,
    unit_price_minor     INTEGER NOT NULL,
    extended_value_minor INTEGER NOT NULL,
    actor_id             TEXT,
    approver_id          TEXT,
    note                 TEXT,
    order_type           TEXT NOT NULL DEFAULT '',
    register_id          TEXT REFERENCES registers (id),
    created_at           TEXT NOT NULL,
    FOREIGN KEY (item_id) REFERENCES items (id)
);

CREATE INDEX IF NOT EXISTS idx_shrinkage_events_created ON shrinkage_events (created_at);
CREATE INDEX IF NOT EXISTS idx_shrinkage_events_reason ON shrinkage_events (reason_category);

-- void_comp_waste (ut-docs#1465): gates the reason-picker/manager-approval
-- step on a non-zero-value /api/pos/remove — same seeding pattern as
-- 033_catalog_management_permission.sql. cashier is deliberately NOT
-- granted: that's the whole point of the card — a cashier gets the
-- manager-PIN elevation prompt (checkOrElevate), a manager/admin/
-- super_admin can act directly with just the reason picker, no PIN.
INSERT OR IGNORE INTO permission_actions (action) VALUES ('void_comp_waste');
INSERT OR IGNORE INTO role_permissions (role, action, granted) VALUES
    ('admin', 'void_comp_waste', 1),
    ('manager', 'void_comp_waste', 1),
    ('super_admin', 'void_comp_waste', 1);
