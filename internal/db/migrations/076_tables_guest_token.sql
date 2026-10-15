-- 076_tables_guest_token.sql — ut-docs#4337 (ADR-0159 §1): each dining
-- table gets a guest token, 16 bytes of crypto/rand as lowercase hex. The
-- table QR will encode <web base>/t/<token> (ut-docs#4338), the main till
-- reports {token, label} for its enabled tables in the device report, and
-- a cloud guest_order directive names its table by this token, never by
-- the table id.
--
-- Nullable: rows that predate this migration get a token on first need
-- (POSRepo.GuestTables, when the device report is built); a new table gets
-- one at CreateTable. UNIQUE where set, so a token names one table only.
-- The column travels with the tables row in the LAN admin bundle (SELECT *),
-- so a promoted additional till keeps reporting the same tokens.
--
-- Replay-safe: the runner skips an ADD COLUMN whose column already exists
-- (ut-docs#1412), and the index is IF NOT EXISTS.
ALTER TABLE tables ADD COLUMN guest_token TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_tables_guest_token ON tables(guest_token) WHERE guest_token IS NOT NULL;
