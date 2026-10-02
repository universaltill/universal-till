-- 061_user_display_settings.sql — ut-docs#3149 (personal display, part 1/3).
--
-- Per-operator overrides of display-only settings (the theme, the sell
-- screen's browsing mode). Keyed by (user_id, key), where key is the same
-- string the till-wide settings table uses for that setting (theme,
-- sale.browsing_mode), so common.Deps.ResolvedTheme/ResolvedBrowsingMode can
-- fall through from a personal value to the till/shop value for the same key.
--
-- Absence of a row is the normal case: the operator uses the till/shop
-- value. Rows are only honoured when the operator's role is granted the
-- personal_display permission action, which this migration REGISTERS but
-- grants to no role (part 3 of #3149 adds the grants). With no grant,
-- auth.Service.Can returns false for everyone, so the table is inert.
--
-- FK to users(id) in the same table-level, no-cascade style as sessions and
-- age_verifications: operators are deactivated (users.is_active), never
-- deleted, so there is nothing to cascade.
--
-- Admin-synced (sync_admin_repo.go's adminTables), deliberately UNLIKE the
-- till-wide theme key, which ut-docs#2783 made per-station: this is a
-- per-OPERATOR preference, and the point of it is that it follows the
-- person to every till in the shop (a shared till, a user switch). The
-- admin bundle (DumpAdmin) carries the whole table with no per-row filter,
-- which is the intended scope: "replicated to every till of THIS shop" —
-- the same scope users itself already has. It is never visible to another
-- shop, and read-side isolation between operators is the resolver's own
-- user_id scoping, not the sync layer's. Like every adminTables entry the
-- main till's bundle is authoritative (ApplyAdmin prunes rows it doesn't
-- carry), so the write path (part 2) must write on — or through to — the
-- main till, the same as other shop-wide admin state.
--
-- Being a brand-new table, its three sync_admin_version triggers ship here
-- with it (same as 025/031), not in a separate migration — pinned by
-- TestSyncAdminVersion_EveryAdminTableHasTriggers.
--
-- IF NOT EXISTS / INSERT OR IGNORE throughout: same replay-safety
-- convention as every migration since 021.
CREATE TABLE IF NOT EXISTS user_display_settings (
    user_id TEXT NOT NULL,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (user_id, key),
    FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_user_display_settings_ins AFTER INSERT ON user_display_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_user_display_settings_upd AFTER UPDATE ON user_display_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_user_display_settings_del AFTER DELETE ON user_display_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

-- Registered only — no role_permissions grant (part 3 adds those).
INSERT OR IGNORE INTO permission_actions (action) VALUES ('personal_display');
