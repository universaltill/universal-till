-- 067_tills_role.sql — ut-docs#2781: every enrolled till has a role,
-- 'additional' (works offline, rings up sales like any other till — what
-- every till enrolled before this migration is, hence the default) or
-- 'satellite' (kiosk, table-QR or order station that needs the main till).
-- Until ut-docs#1154 a satellite has no runtime behaviour of its own: the
-- role labels the till on the Tills page and gates its device profile
-- (POST /api/settings/display-mode).
--
-- pending_pairings.requested_role is the role a replica asked to join as on
-- the approve-to-pair queue; the manager's approval overwrites it with the
-- final choice, and /api/sync/enroll enrols the till as that.
--
-- tills is an admin-synced table dumped whole-row (sync_admin_repo.go), so
-- the column reaches every joined till's roster. 023's tills UPDATE trigger
-- deliberately fires only on name/enrolled_at changes (last_seen_at is
-- touched on every sync call), so a role change needs its own trigger or
-- the generation-keyed bundle cache would keep serving the old role. Same
-- WHEN-gated shape: writing the same role back is not a change.
-- IF NOT EXISTS for the same re-applicability reason as 023.
ALTER TABLE tills ADD COLUMN role TEXT NOT NULL DEFAULT 'additional' CHECK (role IN ('additional', 'satellite'));

ALTER TABLE pending_pairings ADD COLUMN requested_role TEXT NOT NULL DEFAULT 'additional' CHECK (requested_role IN ('additional', 'satellite'));

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tills_role_upd AFTER UPDATE OF role ON tills
WHEN OLD.role IS NOT NEW.role
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
