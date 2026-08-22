-- 057: extend the #554 action catalog with `tax_code_management`
-- (ut-docs#259) -- the tax-code management UI's own gating action: create,
-- edit (name/rate/takeaway rate) and activate/deactivate a tax code. Same
-- precedent as 043's plugin_management (a dedicated action per subsystem,
-- rather than overloading `settings`) -- editing what tax rate an item's
-- price is computed against is a materially higher-risk operation than a
-- generic settings edit. Additive: seeded identically to 043's pattern
-- (manager/admin/super_admin granted, cashier denied) so no existing till's
-- access changes.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('tax_code_management');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin')
  AND a.action IN ('tax_code_management');
