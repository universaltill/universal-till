-- 067_tills_role.sql — ut-docs#2781: a joined till has a manager-assigned
-- role, chosen on the main till before the till is created (pairing-code
-- card or the approve-to-pair card) and changeable later on the Tills page.
--
-- role: 'additional' (the default — every till enrolled before this
-- migration, and today's behaviour) or 'satellite'. Until ut-docs#1154 lands
-- a satellite behaves exactly like ADR-0020's counter-pay kiosk: order
-- capture only, pay at the counter, never a register or back office, never
-- records a sale or takes a card payment alone (ADR-0086). The CHECK keeps
-- any other value out at the storage layer.
--
-- tills is an admin-synced table dumped whole-row (sync_admin_repo.go's
-- adminTables), so the column reaches every joined till with no new sync
-- wiring; each till reads its OWN row after a pull to learn its role
-- (pages.reconcileOwnTillRole). One thing does NOT ride along: migration
-- 023's tills UPDATE trigger deliberately fires only when name or
-- enrolled_at changes (last_seen_at is touched on every sync call), so a
-- role change alone would never move sync_admin_version and the cached
-- admin bundle would keep serving the old role. The trigger below bumps the
-- generation when — and only when — role really changes (IS NOT: NULL-safe,
-- and a no-op re-save never churns the bundle). IF NOT EXISTS for the same
-- re-appliability reason as 023's own triggers.
ALTER TABLE tills ADD COLUMN role TEXT NOT NULL DEFAULT 'additional' CHECK (role IN ('additional', 'satellite'));

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tills_role_upd AFTER UPDATE OF role ON tills
WHEN OLD.role IS NOT NEW.role
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
