-- 059: extend the #554 action catalog with `stock_location_management`
-- (ut-docs#903, follow-up from #901's independent review). locations_page.go
-- and registers_page.go both gated on the generic `settings` action --
-- correct at the time (#901 just needed the raw IsManager() check migrated
-- onto canPerform so UT_AUTH=off worked, and reusing `settings` was the
-- smallest fix for that) but semantically broad: `settings` also covers
-- settings_page.go, receipt_designer.go, print_api.go and menu_page.go's
-- tile visibility generally, so a super_admin editing that one row in
-- role_permissions (runtime-editable, permission_settings_page.go) moved
-- stock-location/register administration in lockstep with every other
-- settings-gated surface, with no way to grant or withhold it
-- independently -- something a raw IsManager() check could never do. One
-- combined action for both pages (not two), matching how this codebase
-- already treats locations and registers as one admin surface (same
-- multitill.md help topic, same menu section) -- same precedent as 043's
-- plugin_management: a dedicated action per subsystem rather than
-- overloading `settings` for everything manager-only. Additive: seeded
-- identically to 039/043's pattern (manager/admin/super_admin granted,
-- cashier denied) so no existing till's access changes.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('stock_location_management');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin')
  AND a.action IN ('stock_location_management');
