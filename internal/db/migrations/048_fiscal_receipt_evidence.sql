-- ut-docs#2880 (fiscal-sign-ask.md 1.10.0): the generic, country-neutral
-- `receipt` object a fiscal.sign.ask signer may return with "approved" —
-- the QR payload the receipt must carry, in the signer's own format (for
-- Germany: fiskaly SIGN DE's qr_code_data, the DSFinV-K "V0;…" string,
-- passed through verbatim by ut-plugin-tax-de), plus up to 10 extra lines
-- to print under the fiscal block (lines_json: a JSON array of strings).
--
-- Replaces core's provisional, self-invented "UT-TSE-V0|…" QR (the removed
-- buildTSEQRPayload): the receipt view and the ESC/POS print render the QR
-- from this stored payload, and a reprint reads it back — never re-derived.
-- No row = no QR, never a placeholder.
--
-- Keyed 1:1 on sale_id like fiscal_tse_signatures / fiscal_device_receipts
-- (no FK to sales, same as those), first write wins (ON CONFLICT DO
-- NOTHING in internal/data/fiscal_repo.go), and classified per-till in
-- internal/data/sync_admin_repo.go's nonAdminTables alongside them.
CREATE TABLE IF NOT EXISTS fiscal_receipt_evidence (
    sale_id    TEXT PRIMARY KEY,
    qr_payload TEXT NOT NULL DEFAULT '',
    lines_json TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
