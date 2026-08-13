-- 039: role_permissions groundwork (split (a) of #520) — action catalog +
-- role_permissions table. Additive/inert: no call site consumes this yet
-- (that's #555), so no existing till's access changes when this lands.
-- (Originally landed as 038 in PR #313, but PR #311 — merged moments
-- earlier — independently also claimed version 038 for an unrelated
-- migration; different filenames meant no merge conflict flagged it, but
-- both registered as schema_migrations.version=38, so main's CI broke on
-- a UNIQUE constraint violation. Renumbered here to the version actually
-- free on main; see docs/code-reviews/2026-08-13-role-permissions-groundwork-554.md.)
--
-- roles is kept as its own table from day one (Architect prep on #520) so
-- a future custom-role card is additive, not a rewrite. users.role stays
-- the plain TEXT column it already is (cashier|manager|admin) — SQLite
-- can't add a real FK to an existing column without rebuilding the table,
-- and that rebuild isn't this card's job — but every value it can hold is
-- one of these rows, and super_admin is now a recognized role alongside
-- cashier/manager/admin.
CREATE TABLE IF NOT EXISTS roles (
    role TEXT PRIMARY KEY -- cashier|manager|admin|super_admin
);

INSERT OR IGNORE INTO roles (role) VALUES ('cashier'), ('manager'), ('admin'), ('super_admin');

-- Fixed action catalog. New actions get a new INSERT OR IGNORE in a later
-- migration (append-only), not an edit here.
CREATE TABLE IF NOT EXISTS permission_actions (
    action TEXT PRIMARY KEY
);

INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('refund'),
    ('eod_report'),
    ('cash_adjustment'),
    ('price_override'),
    ('void'),
    ('user_management'),
    ('settings');

CREATE TABLE IF NOT EXISTS role_permissions (
    role    TEXT NOT NULL REFERENCES roles(role),
    action  TEXT NOT NULL REFERENCES permission_actions(action),
    granted INTEGER NOT NULL DEFAULT 0, -- 0/1; absent row also means denied
    PRIMARY KEY (role, action)
);

-- Seed grants that exactly replicate today's isManagerOrAuthOff gate:
-- manager/admin/super_admin get every catalog action, cashier gets none
-- (no rows inserted for cashier — Can() treats "no row" as denied).
INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin');
