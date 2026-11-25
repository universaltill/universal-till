-- 117_fiscal_sign_journal.sql — ut-docs#7021 (ADR-0044 D1 amendment): one row
-- per sale/return the till asked a fiscal signer to sign, written BEFORE the
-- fiscal.sign.ask is sent. If the sale then fails to save (CompleteSale
-- error) the row keeps the signer's answer (TSE evidence, receipt object) as
-- 'signed_no_sale', so a transaction the TSE signed is never left with no
-- durable trace. A committed sale deletes its 'asked' row; boot turns an
-- 'asked' row with no sale into 'warned'. The row never triggers a re-sign
-- (ADR-0056).
--
-- Till-local: never in the LAN journal, a snapshot, a bundle or the cloud
-- roster; it travels in a full backup only. No card data (ADR-0127): amounts,
-- ids and the signer's evidence only. Bounded: a row outlives its sale only
-- when the sale failed after signing.
CREATE TABLE IF NOT EXISTS fiscal_sign_journal (
  sale_id       TEXT PRIMARY KEY,
  sale_type     TEXT NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('asked','signed_no_sale','warned')),
  outcome       TEXT NOT NULL DEFAULT '',
  amount_minor  INTEGER NOT NULL,
  currency      TEXT NOT NULL,
  actor_id      TEXT NOT NULL DEFAULT '',
  evidence_json TEXT NOT NULL DEFAULT '',
  receipt_json  TEXT NOT NULL DEFAULT '',
  reason        TEXT NOT NULL DEFAULT '',
  asked_at      TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fsj_state ON fiscal_sign_journal(state, asked_at);
