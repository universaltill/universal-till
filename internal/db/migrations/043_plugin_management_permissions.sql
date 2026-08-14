-- 043: extend the #554 action catalog with `plugin_management` (ut-docs#706,
-- another #555 split successor). Plugin install/enable/disable/uninstall/
-- update/rollback/import/export, permission grant/revoke, trust-level
-- changes, the plugin settings editor, the marketplace store lifecycle
-- (download/install/delete-download), and the self-update apply/check/
-- schedule endpoints all gate on this one action rather than reusing
-- `settings`: installing or granting a permission to a plugin is a
-- materially higher-risk operation than a generic settings edit (it can
-- change what code runs on the till), and #709 already established the
-- precedent of a dedicated action per subsystem rather than overloading
-- `settings` for everything manager-only. Additive: seeded identically to
-- 039/042's pattern (manager/admin/super_admin granted, cashier denied) so
-- no existing till's access changes.
--
-- The three self-update endpoints (POST /api/update/apply, /api/update/check,
-- /api/settings/update-schedule) are folded in here too even though they
-- govern the till APPLICATION's own binary self-update, not plugin
-- lifecycle — #706 review flagged this as a judgment call: a future custom
-- role granted "manage plugins" would also silently get "apply a new POS
-- build to this till." Inert today (no custom-role feature exists yet). A
-- future card is free to split these onto their own `app_update` action;
-- behaviorally identical either way while every role holding one holds the
-- other, same accepted-judgment-call precedent as 042's eod_report note.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('plugin_management');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin')
  AND a.action IN ('plugin_management');
