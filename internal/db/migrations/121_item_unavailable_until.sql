-- 121_item_unavailable_until.sql — ADR-0215 decision 2 (out of stock / "86"),
-- ADR-0201 §2, ut-docs#6583 (S7a, till side).
--
-- items.unavailable_until: the neutral "not available" field. '' = available;
-- otherwise the UTC RFC 3339 instant at which the item becomes available
-- again (the far-future sentinel 9999-12-31T23:59:59Z means "until staff turn
-- it back on"). It is a hint for connected channels only: nothing in the sale
-- path reads it, so it never blocks checkout.
ALTER TABLE items ADD COLUMN unavailable_until TEXT NOT NULL DEFAULT '';

-- delivery_signals: the main till's outbox of NEUTRAL availability signals
-- (kind availability.set | service.set, payload as ADR-0215 decision 9),
-- queued by a staff change (audited) or by an expiry, and uploaded to the
-- cloud with the check-in. The till is the truth: a provider-side unsnooze is
-- reconciled back to the till's state (the tick re-emits current_state).
-- state: pending | sent. Main-till bookkeeping, never LAN-synced. No customer
-- data.
CREATE TABLE IF NOT EXISTS delivery_signals (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    signal_id    TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL CHECK (kind IN ('availability.set','service.set')),
    payload_json TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    actor_id     TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','sent')),
    sent_at      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_delivery_signals_state ON delivery_signals(state, seq);
