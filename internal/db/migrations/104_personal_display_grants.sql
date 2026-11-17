-- ut-docs#3440 (personal display, part 3/3): grant the personal_display
-- action that migration 061 registered. Admin, manager and super_admin may
-- pick their own theme and sale-screen layout on /my-display; cashier is
-- deliberately left ungranted (sale-only, ut-docs#3079) — an admin can grant
-- it in Users → Permissions ("Personal display").
--
-- INSERT OR IGNORE against PRIMARY KEY (role, action): a shop that already
-- set or revoked this grant on /users/permissions keeps its choice.
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
INSERT OR IGNORE INTO role_permissions (role, action, granted) VALUES
    ('admin', 'personal_display', 1),
    ('manager', 'personal_display', 1),
    ('super_admin', 'personal_display', 1);
