-- 072_default_register_follows_till_name.sql — universaltill/ut-docs#4022:
-- the register setup seeds (id 'reg-default') used to be called "Default
-- Register", a stock English name the shop never chose, shown on the
-- Registers page next to registers named after their tills. EnsureRegister
-- now names it after the till (till.name), else the shop (store.name); this
-- renames an installed till's seeded register the same way.
--
-- Data only, frozen once merged (ADR-0100). Touches nothing a manager
-- already renamed (only the exact seeded name), never a placeholder shop
-- name (config.IsPlaceholderStoreName's two defaults), and never a name
-- another register holds (registers.name is UNIQUE). registers is
-- primary-wins synced; a joined till's copy gets the same answer (its
-- till.name/store.name are the main till's, shop-wide) and is overwritten
-- by the main till's row on the next admin pull anyway. Re-running it is a
-- no-op: after the first pass the name is no longer 'Default Register'.
UPDATE registers
SET name = (SELECT TRIM(value) FROM settings WHERE key = 'till.name')
WHERE id = 'reg-default' AND name = 'Default Register'
  AND (SELECT TRIM(value) FROM settings WHERE key = 'till.name') <> ''
  AND NOT EXISTS (SELECT 1 FROM registers other WHERE other.name = (SELECT TRIM(value) FROM settings WHERE key = 'till.name'));

UPDATE registers
SET name = (SELECT TRIM(value) FROM settings WHERE key = 'store.name')
WHERE id = 'reg-default' AND name = 'Default Register'
  AND (SELECT TRIM(value) FROM settings WHERE key = 'store.name') <> ''
  AND LOWER((SELECT TRIM(value) FROM settings WHERE key = 'store.name')) NOT IN ('my store', 'universal till store')
  AND NOT EXISTS (SELECT 1 FROM registers other WHERE other.name = (SELECT TRIM(value) FROM settings WHERE key = 'store.name'));
