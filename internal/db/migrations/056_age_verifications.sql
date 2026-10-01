-- 056_age_verifications.sql — universaltill/ut-docs#3340 (age-restricted
-- sales, due-diligence support): the append-only record of every ID-check
-- outcome a cashier recorded at the till for an items.age_restricted item
-- (055_items_age_restricted.sql). The shop's own refusals/acceptances log —
-- evidence of what the till prompted and what staff answered, NOT a legal
-- compliance determination (web/help/*/age-restricted-sales.md says so).
--
-- outcome is one of 'accepted' (staff checked ID and allowed the sale) or
-- 'refused' (staff refused the sale) — internal/pos.AgeVerification
-- {Accepted,Refused}, the same fixed vocabulary the CHECK constraint below
-- enforces at the schema level, mirroring 035_shrinkage_events.sql's
-- reason_category CHECK.
--
-- sale_id is the sale the check was recorded with. Rows are written in the
-- SAME transaction as the sale itself (pos.CompleteSale), using the real
-- new sales.id — a verification is never recorded before its sale exists.
-- Nullable and with no ON DELETE action (deliberately no CASCADE): this is
-- a historical record and must not silently disappear with its sale row.
--
-- item_id/item_name: item_name is DENORMALIZED (a snapshot at the time of
-- the check), same convention as shrinkage_events.item_name and
-- sale_lines.name_snapshot — the item can be renamed or deleted later and
-- this row must still read sensibly. item_id carries no ON DELETE CASCADE
-- for the same reason as shrinkage_events.item_id.
--
-- cashier_id is the user who answered the ID-check prompt. Same column
-- name and FOREIGN KEY as sales.cashier_id (001_init.sql), since the
-- verifier is always the signed-in till operator (or the seeded 'system'
-- user with auth off) — the same identity the sale row itself records.
--
-- created_at is TEXT, app-supplied at insert time (no DB-side DEFAULT),
-- matching shrinkage_events.created_at (035) and audit_log's own
-- insert-time-supplied convention: the sale and its verification rows carry
-- identical timestamps.
--
-- CREATE TABLE/INDEX IF NOT EXISTS throughout, same replay-safety
-- convention as every migration since 021.
CREATE TABLE IF NOT EXISTS age_verifications (
    id         TEXT PRIMARY KEY,
    sale_id    TEXT,
    item_id    TEXT,
    item_name  TEXT NOT NULL,
    outcome    TEXT NOT NULL CHECK (outcome IN ('accepted', 'refused')),
    cashier_id TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (sale_id)    REFERENCES sales (id),
    FOREIGN KEY (item_id)    REFERENCES items (id),
    FOREIGN KEY (cashier_id) REFERENCES users (id)
);

CREATE INDEX IF NOT EXISTS idx_age_verifications_created ON age_verifications (created_at);
CREATE INDEX IF NOT EXISTS idx_age_verifications_sale ON age_verifications (sale_id);
