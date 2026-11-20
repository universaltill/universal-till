-- 111_payment_attempts.sql — ut-docs#6455 (ADR-0198 §2.3, §6.8): core's own
-- record of one card-present payment attempt. Written BEFORE the customer
-- taps, so a crash, a restart or a lost reply still leaves a row the till
-- can settle against the provider (session_status). Till-local: never in the
-- LAN journal, a snapshot or the cloud roster.
--
-- No card data (ADR-0127): only the provider's PaymentIntent id and, once the
-- broker reports them, the card brand and last four digits. amount/currency/
-- key are fixed when the row is written and are never taken from the page.
--
-- Bounded: one row per attempt (the sale rate); settled rows older than 90
-- days are deleted when a new attempt begins; pending/unknown never are.
CREATE TABLE IF NOT EXISTS payment_attempts (
  idempotency_key TEXT PRIMARY KEY,
  provider        TEXT NOT NULL,
  amount_minor    INTEGER NOT NULL CHECK (amount_minor > 0),
  currency        TEXT NOT NULL,
  state           TEXT NOT NULL DEFAULT 'pending'
                  CHECK (state IN ('pending','approved','declined','cancelled','unknown','resolved_not_charged')),
  pi_id           TEXT NOT NULL DEFAULT '',
  card_brand      TEXT NOT NULL DEFAULT '',
  last4           TEXT NOT NULL DEFAULT '',
  reason          TEXT NOT NULL DEFAULT '',
  tender_ref      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  deadline_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_payment_attempts_state ON payment_attempts(state, updated_at);
