-- 115_payment_leg_journal.sql — ut-docs#6653 (ADR-0136 Decision 9): a durable,
-- append-only record of every electronic payment leg, written BEFORE the
-- provider is asked, so a crash or restart between capture and sale can no
-- longer lose track of money. One row per state change of one leg, keyed by
-- the leg's authorize request id and an ask counter (a retry after an
-- unknown outcome or a decline re-uses the id and bumps ask). The current
-- state of a leg is its newest row (highest seq). Rows are never UPDATEd (the
-- trigger aborts any UPDATE); only the retention sweep deletes, and only
-- chains whose newest state is terminal (declined, settled, reversed,
-- resolved) and older than 90 days. warned and reverse_failed are never swept.
--
-- Till-local: never in the LAN journal, a snapshot, a bundle or the cloud
-- roster; it travels in a full backup only. Core's own table; it does not
-- touch payment_attempts (migration 111, ADR-0198).
--
-- No card number, expiry or CVV is stored. transaction_ref is the provider's
-- own transaction reference (like payment_attempts.pi_id); a reversal needs
-- it in full. amount_minor is the exact captured amount including any
-- reader-reported tip (tip_minor is that tip), reader_ref the provider reader.
-- Bounded: at most about five rows per ask of an electronic leg.
CREATE TABLE IF NOT EXISTS payment_leg_journal (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id      TEXT NOT NULL,
  ask             INTEGER NOT NULL DEFAULT 1 CHECK (ask >= 1),
  state           TEXT NOT NULL
                  CHECK (state IN ('asked','captured','unknown','declined','settled','reversing','reversed','reverse_failed','warned','resolved')),
  method_id       TEXT NOT NULL,
  amount_minor    INTEGER NOT NULL CHECK (amount_minor >= 0),
  currency        TEXT NOT NULL,
  tip_minor       INTEGER NOT NULL DEFAULT 0,
  reader_ref      TEXT NOT NULL DEFAULT '',
  transaction_ref TEXT NOT NULL DEFAULT '',
  sale_id         TEXT NOT NULL DEFAULT '',
  reason          TEXT NOT NULL DEFAULT '',
  at              TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pleg_req ON payment_leg_journal(request_id, seq);
CREATE INDEX IF NOT EXISTS idx_pleg_state ON payment_leg_journal(state, seq);
CREATE TRIGGER IF NOT EXISTS pleg_no_update BEFORE UPDATE ON payment_leg_journal
BEGIN
  SELECT RAISE(ABORT, 'payment_leg_journal is append-only');
END;
