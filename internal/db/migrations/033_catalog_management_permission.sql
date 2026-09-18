-- ut-docs#2395 (P0 hotfix): ut-docs#2312 introduced the catalog_management
-- permission action by EDITING the already-applied baseline 001_init.sql
-- (adding the action row and three role_permissions grants). The boot-time
-- ledger guard (verifyAppliedMigrations, ADR-0074 Decision 3) records a
-- statement-level checksum of every applied file and refuses to start when
-- the on-disk file no longer matches — so every till that had applied 001
-- as shipped in v0.1.0…v0.18.0 was bricked at boot by v0.19.0–v0.19.2.
--
-- 001_init.sql is restored to exactly what v0.18.0 shipped; THIS appended
-- migration is what actually delivers the permission. It is idempotent
-- (INSERT OR IGNORE; permission_actions has PRIMARY KEY (action) and
-- role_permissions has PRIMARY KEY (role, action), exactly the columns
-- written here), so:
--   * a till upgrading from <= v0.18.0 gets the rows here, for the first
--     time;
--   * a fresh v0.19.0–v0.19.2 install already has the rows from its
--     edited 001 and this is a no-op — db.go's acceptedPriorChecksums
--     re-stamps its version-1 ledger row so it boots on the restored file.
--
-- What the permission gates (carried over from #2312's comment):
-- catalog_management gates the Designer (/designer, /api/buttons/*), the
-- sell-screen tile long-press sheet (/ui/pos/tile-sheet), and every
-- mutating /api/catalog/* route (item/variant/modifier-group/option-set/
-- barcode CRUD) — none of which carried ANY permission check before that
-- card, so a cashier could reorder/remove quick buttons and edit/deactivate
-- catalog items. Seeded identically to tax_code_management and
-- stock_location_management (#903): manager/admin/super_admin granted,
-- cashier not — an existing till's manager/admin keeps working with no
-- behaviour change; only a cashier session is newly denied.
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
INSERT OR IGNORE INTO permission_actions (action) VALUES ('catalog_management');
INSERT OR IGNORE INTO role_permissions (role, action, granted) VALUES
    ('admin', 'catalog_management', 1),
    ('manager', 'catalog_management', 1),
    ('super_admin', 'catalog_management', 1);
