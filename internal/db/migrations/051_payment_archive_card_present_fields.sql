-- 051: mirror 050's card-present payment fields onto payments_archive
-- (ut-docs#543). 040_reset_archive.sql's own header comment requires every
-- later ALTER to payments to be mirrored here -- without this, a shop's
-- "Clear transaction history" (Settings -> Data) would silently drop a
-- payment's masked PAN / auth code / terminal ID / trace ID on archive,
-- and RestoreResetBatch could never bring them back.
ALTER TABLE payments_archive ADD COLUMN masked_pan TEXT;
ALTER TABLE payments_archive ADD COLUMN auth_code TEXT;
ALTER TABLE payments_archive ADD COLUMN terminal_id TEXT;
ALTER TABLE payments_archive ADD COLUMN trace_id TEXT;
