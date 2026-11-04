-- 096_channel_order_actions.sql — ADR-0169 §9, ut-docs#4768 (slice D): the main
-- till's outbox of staff actions on channel orders (accept, pick, pack, ship,
-- cancel), uploaded to POST /api/v1/stores/channel/orders/actions
-- (reference/contracts/channel-order-action.md). A tap writes a row here and an
-- audit_log row in one transaction; it never calls the network and never changes
-- channel_orders.status or stock: the order changes only when a revision is
-- applied (the answer's `current`, or a channel_order_update directive).
-- action_id is minted on the till (UUID v4), never reused: the cloud's
-- idempotency key. seq orders the upload. base_revision is rebased to the
-- previous applied answer's revision for the same order (contract "One in flight
-- per order"). state: pending (to send) | refused (answered refused, kept to show
-- the reason until dismissed) | noted (applied ship/cancel whose channel takes no
-- write-back, kept to show that note) | blocked (the cloud refused this till:
-- main_till_required, store_mismatch, device_credential_required; kept until
-- staff dismiss). No customer data: carrier and tracking are shop-entered
-- shipment data. Main-till bookkeeping, never LAN-synced.
CREATE TABLE IF NOT EXISTS channel_order_actions (
    seq               INTEGER PRIMARY KEY AUTOINCREMENT,
    action_id         TEXT NOT NULL UNIQUE,
    order_id          TEXT NOT NULL REFERENCES channel_orders(id) ON DELETE CASCADE,
    connection_id     TEXT NOT NULL,
    external_order_id TEXT NOT NULL,
    action            TEXT NOT NULL CHECK (action IN ('accept','pick','pack','ship','cancel')),
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
    answered_at       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_channel_order_actions_order ON channel_order_actions(order_id, seq);
CREATE INDEX IF NOT EXISTS idx_channel_order_actions_state ON channel_order_actions(state, seq);
