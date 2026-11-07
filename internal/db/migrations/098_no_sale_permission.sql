-- 098_no_sale_permission.sql — ut-docs#3560: a dedicated no_sale permission
-- for the sale screen's "No sale" (open cash drawer) button, split from
-- cash_adjustment (skims/payouts). A role holding it opens the drawer with no
-- PIN; any other session is asked for a manager's PIN.
--
-- Mirror only: every role that holds cash_adjustment today also gets no_sale,
-- so nobody loses PIN-less drawer access on upgrade (custom ADR-0128 roles
-- included) and a shop that revoked cash_adjustment from a role is not
-- widened. A fresh database gets exactly admin, manager and super_admin from
-- 001_init's cash_adjustment grants; cashier is not granted, an admin can in
-- Users -> Permissions.
--
-- Idempotent (role_permissions has PRIMARY KEY (role, action)); the 023
-- triggers bump sync_admin_version so the check-in report and the replica
-- bundle pick the new action up. Migrations are frozen the moment they merge
-- (ADR-0100).
INSERT OR IGNORE INTO permission_actions (action) VALUES ('no_sale');
INSERT OR IGNORE INTO role_permissions (role, action, granted)
    SELECT role, 'no_sale', 1 FROM role_permissions
    WHERE action = 'cash_adjustment' AND granted = 1;
