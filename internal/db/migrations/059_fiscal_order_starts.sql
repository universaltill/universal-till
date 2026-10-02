-- 059_fiscal_order_starts.sql — ADR-0138 Decision 3 (ut-docs#3310).
--
-- Best-effort persistence for the fiscal.order.start round trip: when a
-- subscribed signer answers a gastro order capture (a first park of a
-- held/table order, or a pay-at-counter kiosk checkout) with
-- {"status":"acknowledged","tx_id":"…","tx_revision":1} before the
-- background dispatch goroutine is abandoned, core records the identifier
-- here, keyed by the order's OWN existing id (the held sale's id, or the
-- counter order's id — which is also its held sale's id) and tagged with
-- order_kind ('held' | 'counter'). No new id space is introduced. The later
-- fiscal.sign.start / fiscal.sign.ask dispatches for a sale tendered from
-- that order echo order_id so a signer or auditor can join the order's
-- Bestellung-V1 transaction to its eventual Kassenbeleg. Absence of a row is
-- the common, honest degraded case — never an error, never backfilled.
-- First write wins (INSERT … ON CONFLICT DO NOTHING): an order's capture
-- record is never silently overwritten.
--
-- Shipped as an ADDITIVE migration, never an edit to an existing file:
-- every merged migration is frozen (ADR-0100) and verifyAppliedMigrations
-- hard-fails an already-migrated database on any checksum drift — the trap
-- 005_fiscal_sign_starts.sql's own header documents.
--
-- CREATE TABLE IF NOT EXISTS, same replay-safety convention as every
-- migration since 021.
CREATE TABLE IF NOT EXISTS fiscal_order_starts (
    order_id     TEXT PRIMARY KEY,
    order_kind   TEXT NOT NULL,
    tx_id        TEXT NOT NULL,
    tx_revision  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
