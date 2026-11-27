-- 120_delivery_channel_orders.sql — ADR-0215 (delivery platforms are channel
-- plugins of kind "delivery"), ut-docs#6581 (S3): the till side of a delivery
-- order. Additive on channel_orders / channel_order_lines, plus a rebuilt
-- channel_order_actions whose action list now holds the delivery steps.
--
-- A delivery order is still NOT a till sale (no sales/payments row, no drawer,
-- Z or fiscal journal) and stores NO customer name, phone, address or e-mail;
-- `note` is the customer's free-text instruction (allergy text), which the
-- cloud schema bounds to 500 bytes and the till only shows, prints and never
-- logs. Money stays integer minor units.
--
-- order_kind: 'webshop' (every pre-existing row) or 'delivery'. A delivery
-- order walks new -> accepted | rejected -> preparing -> ready -> collected
-- (+ cancelled / refunds as inbound facts), using fulfilment_rank 0..4 as
-- new, accepted, preparing, ready, collected.
-- late_cancel: the platform cancelled it at or after `preparing`: food was
-- already made, so a manager is told (never refused: a cancel is a fact).
-- reject_reason: set when the till itself rejects the order at intake (an
-- unmapped line under the "reject" policy); '' otherwise. While set, no
-- stock moves for the order.
-- fees_json: informational customer-side fees [{kind, amount_minor}], outside
-- total_minor (ADR-0215 decision 8); modifiers_json on a line: [{external_id,
-- name, qty, price_delta_minor}].
ALTER TABLE channel_orders ADD COLUMN order_kind TEXT NOT NULL DEFAULT 'webshop';
ALTER TABLE channel_orders ADD COLUMN service TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN accept_by TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN scheduled_start_at TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN confirm_at TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN prep_minutes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channel_orders ADD COLUMN courier_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN pickup_code TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN tip_minor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channel_orders ADD COLUMN fees_json TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN note TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN late_cancel INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channel_orders ADD COLUMN reject_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN status_changed_at TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_channel_orders_kind_status ON channel_orders(order_kind, status);

ALTER TABLE channel_order_lines ADD COLUMN note TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_order_lines ADD COLUMN modifiers_json TEXT NOT NULL DEFAULT '';

-- channel_order_actions: the CHECK on `action` cannot be altered in SQLite, so
-- the table is rebuilt (105's columns unchanged) with the delivery actions
-- added (reject, preparing, ready, collected, prep_time) and two new columns:
-- reject_reason (reject) and prep_minutes (accept, prep_time). Rows are kept.
CREATE TABLE IF NOT EXISTS channel_order_actions_new (
    seq               INTEGER PRIMARY KEY AUTOINCREMENT,
    action_id         TEXT NOT NULL UNIQUE,
    order_id          TEXT NOT NULL REFERENCES channel_orders(id) ON DELETE CASCADE,
    connection_id     TEXT NOT NULL,
    external_order_id TEXT NOT NULL,
    action            TEXT NOT NULL CHECK (action IN ('accept','pick','pack','ship','cancel','reject','preparing','ready','collected','prep_time')),
    base_revision     INTEGER NOT NULL CHECK (base_revision >= 1),
    created_at        TEXT NOT NULL,
    staff_id          TEXT NOT NULL DEFAULT '',
    carrier           TEXT NOT NULL DEFAULT '',
    tracking_number   TEXT NOT NULL DEFAULT '',
    tracking_url      TEXT NOT NULL DEFAULT '',
    cancel_reason     TEXT NOT NULL DEFAULT '',
    cancel_restock    INTEGER NOT NULL DEFAULT 0,
    state             TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','refused','noted','blocked')),
    result_code       TEXT NOT NULL DEFAULT '',
    writeback         TEXT NOT NULL DEFAULT '',
    answered_at       TEXT NOT NULL DEFAULT '',
    reject_reason     TEXT NOT NULL DEFAULT '',
    prep_minutes      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO channel_order_actions_new (seq, action_id, order_id, connection_id, external_order_id, action, base_revision, created_at,
    staff_id, carrier, tracking_number, tracking_url, cancel_reason, cancel_restock, state, result_code, writeback, answered_at)
SELECT seq, action_id, order_id, connection_id, external_order_id, action, base_revision, created_at,
    staff_id, carrier, tracking_number, tracking_url, cancel_reason, cancel_restock, state, result_code, writeback, answered_at
FROM channel_order_actions;
DROP TABLE channel_order_actions;
ALTER TABLE channel_order_actions_new RENAME TO channel_order_actions;
CREATE INDEX IF NOT EXISTS idx_channel_order_actions_order ON channel_order_actions(order_id, seq);
CREATE INDEX IF NOT EXISTS idx_channel_order_actions_state ON channel_order_actions(state, seq);
