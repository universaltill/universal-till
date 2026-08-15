-- 048: per-sale TSE signing evidence (ut-docs#585, contract
-- fiscal-sign-ask.md v1.1.0).
--
-- One row per SIGNED sale: the §6 KassenSichV receipt data points a
-- fiscal.sign.ask plugin may return alongside "approved" — TSE transaction
-- number, signature counter, TSE serial number, transaction start/log time,
-- the signature itself (base64) and the signing algorithm identifier. Both
-- receipt render paths (the inline HTML receipt and the ESC/POS print) read
-- this row to print the evidence; no row means the sale was completed with
-- no evidence returned (a bare {"status":"approved"} signer, a zero-plugin
-- till, or an unsigned/declared sale) and the receipt renders no TSE block.
--
-- sale_id is the PRIMARY KEY: at most one evidence row per sale, and the
-- repository writes with INSERT ... ON CONFLICT DO NOTHING so a duplicated
-- tender bookkeeping call or a background-retry re-sign never duplicates or
-- overwrites the first recorded evidence (the signature core first proved).
--
-- Field values are stored exactly as the plugin returned them (strings for
-- the RFC3339 timestamps and base64 signature, integers for the two
-- counters) — this is evidence of what the TSE said, not data core derives.
-- No FK on sale_id: evidence written by the background retry can land for a
-- sale row that predates this table, and the audit_log markers this feature
-- pairs with (unsigned_fiscal_signing / fiscal_signing_resolved) are keyed
-- the same loose way.
CREATE TABLE IF NOT EXISTS fiscal_tse_signatures (
    sale_id             TEXT PRIMARY KEY,
    transaction_number  INTEGER NOT NULL DEFAULT 0,
    signature_counter   INTEGER NOT NULL DEFAULT 0,
    serial_number       TEXT NOT NULL DEFAULT '',
    start_time          TEXT NOT NULL DEFAULT '',
    log_time            TEXT NOT NULL DEFAULT '',
    signature           TEXT NOT NULL,
    signature_algorithm TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);
