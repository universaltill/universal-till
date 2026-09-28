-- ut-docs#3079 (security, P1): a cashier could open the stock page
-- (/inventory, /ui/inventory/stock-table) and — worse — POST goods-in
-- (/api/inventory/receipt) and absolute stock overrides
-- (/api/inventory/override), none of which carried ANY permission check.
-- The owner's standing rule: a cashier is sale-only; every edit/report is
-- admin/manager or a role an admin explicitly granted.
--
-- This migration introduces the stock_management action that gates those
-- routes (and the stock rail entry / sell-screen stock link). Seeded
-- identically to catalog_management (033) and tax_code_management:
-- manager/admin/super_admin granted, cashier not — an existing till's
-- manager/admin keeps working with no behaviour change; only a cashier
-- session is newly denied. An admin can grant it to cashier in
-- Users → Permissions ("Stock & inventory").
--
-- Idempotent (INSERT OR IGNORE; permission_actions has PRIMARY KEY (action)
-- and role_permissions has PRIMARY KEY (role, action)).
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
INSERT OR IGNORE INTO permission_actions (action) VALUES ('stock_management');
INSERT OR IGNORE INTO role_permissions (role, action, granted) VALUES
    ('admin', 'stock_management', 1),
    ('manager', 'stock_management', 1),
    ('super_admin', 'stock_management', 1);
