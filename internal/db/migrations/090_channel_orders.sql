-- 090_channel_orders.sql — ADR-0169 §9, ut-docs#4768: orders placed at a
-- sales channel (a web shop, a marketplace) that the cloud's channel host
-- took and queued to the main till as a `channel_order` /
-- `channel_order_update` directive (reference/contracts/channel-order-v1.json, schema_version 1).
--
-- A channel order is NOT a till sale: no sales / payments row, so it is in
-- no drawer, no Z report and no fiscal journal (owner question 6). The
-- order is taken and paid at the channel; the till only records it and
-- moves stock with stock_movements rows of type `channel_sale` (out) and
-- `channel_return` (units the channel says went back on the shelf).
--
-- No customer data is stored: the payload carries none (name, e-mail,
-- phone, address) — only the customer's country for display. Money is
-- integer minor units (internal/money).
--
-- Idempotent: the order key is (connection_id, external_order_id) and the
-- cloud-issued `revision` is the only ordering key — an equal or lower
-- revision is ignored, so a re-delivered directive moves no stock twice.
-- Per line, qty_applied / restocked_applied record the units of sale and of
-- return the till has already moved into stock, so every revision moves only
-- the difference: -(qty - qty_applied) + (restocked_quantity - restocked_applied)
-- (sales-channel-v1.md §5.1). A line the till could not match keeps both at 0.
--
-- Main-till record, never synced (sync_admin_repo.go's not-synced list).
CREATE TABLE IF NOT EXISTS channel_orders (
    id                    TEXT PRIMARY KEY,
    connection_id         TEXT NOT NULL,
    plugin_key            TEXT NOT NULL,
    external_order_id     TEXT NOT NULL,
    external_version      TEXT NOT NULL DEFAULT '',
    revision              INTEGER NOT NULL,
    last_directive_id     TEXT NOT NULL DEFAULT '',
    order_number          TEXT NOT NULL DEFAULT '',
    placed_at             TEXT NOT NULL,
    currency              TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'new',
    payment_state         TEXT NOT NULL DEFAULT '',
    shipping_minor        INTEGER NOT NULL DEFAULT 0,
    shipping_tax_bp       INTEGER NOT NULL DEFAULT 0,
    shipping_tax_included INTEGER NOT NULL DEFAULT 0,
    order_discount_minor  INTEGER NOT NULL DEFAULT 0,
    tax_added_minor       INTEGER NOT NULL DEFAULT 0,
    refund_total_minor    INTEGER NOT NULL DEFAULT 0,
    total_minor           INTEGER NOT NULL DEFAULT 0,
    customer_country      TEXT NOT NULL DEFAULT '',
    currency_mismatch     INTEGER NOT NULL DEFAULT 0,
    total_mismatch        INTEGER NOT NULL DEFAULT 0,
    -- furthest rank reached on new/accepted/picked/packed/shipped, so a
    -- partial refund never lets fulfilment regress (sales-channel-v1.md §5.1)
    fulfilment_rank       INTEGER NOT NULL DEFAULT 0,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    UNIQUE (connection_id, external_order_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_orders_placed ON channel_orders(placed_at DESC);

CREATE TABLE IF NOT EXISTS channel_order_lines (
    id                  TEXT PRIMARY KEY,
    order_id            TEXT NOT NULL REFERENCES channel_orders(id) ON DELETE CASCADE,
    line_id             TEXT NOT NULL,
    item_id             TEXT,
    variant_id          TEXT,
    location_id         TEXT,
    sku                 TEXT NOT NULL DEFAULT '',
    name                TEXT NOT NULL DEFAULT '',
    qty                 INTEGER NOT NULL,
    unit_price_minor    INTEGER NOT NULL DEFAULT 0,
    tax_bp              INTEGER NOT NULL DEFAULT 0,
    tax_included        INTEGER NOT NULL DEFAULT 0,
    discount_line_minor INTEGER NOT NULL DEFAULT 0,
    refunded_quantity   INTEGER NOT NULL DEFAULT 0,
    restocked_quantity  INTEGER NOT NULL DEFAULT 0,
    unmatched           INTEGER NOT NULL DEFAULT 0,
    qty_applied         INTEGER NOT NULL DEFAULT 0,
    restocked_applied   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (order_id, line_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_order_lines_order ON channel_order_lines(order_id);
