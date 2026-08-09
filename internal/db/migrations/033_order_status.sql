-- Order lifecycle status (universaltill/ut-docs#526): the ONE central model
-- the Kitchen Display (#516), pagers (#517) and customer order tracking
-- (#528/#527) will all key off later.
--
-- sales.order_status is the CURRENT state: '' (the default) means "no
-- kitchen-status tracking has touched this sale" — every existing row and
-- every shop that never uses the feature is completely unaffected. Non-empty
-- values are new/preparing/ready/collected/cancelled (internal/pos
-- order_status.go owns the vocabulary and the forward-only conflict rule;
-- writes go through POSRepo.ApplyOrderStatus, never a bare UPDATE).
--
-- order_status_events is the append-only journal: one row per APPLIED status
-- change with actor + timestamp (who/when) — dropped stale writes (the
-- offline multi-till conflict rule: a write may only move the status forward)
-- never journal. Kept as its own small table rather than audit_log rows so
-- "history for this receipt" stays a cheap indexed lookup for the future KDS.
-- receipt_no (not sale id) is the key, matching how kitchen tickets and the
-- one-tap endpoint address orders. Timestamps are RFC3339 TEXT, same as every
-- other table here.
ALTER TABLE sales ADD COLUMN order_status TEXT NOT NULL DEFAULT '';
ALTER TABLE sales ADD COLUMN order_status_updated_at TEXT;

CREATE TABLE order_status_events (
    id         TEXT PRIMARY KEY,
    receipt_no TEXT NOT NULL,
    status     TEXT NOT NULL,
    actor_id   TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_order_status_events_receipt ON order_status_events(receipt_no, created_at);
