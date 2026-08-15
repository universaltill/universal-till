-- 047: `permission_management` action for the role→action grant-matrix
-- editor itself (ut-docs#556, split (c) of #520).
--
-- Seeded to ('super_admin') ONLY — deliberately NOT the manager-inclusive
-- pattern 039/042/043/044/045 follow, and not even the admin+super_admin
-- pattern 046 uses. #520's own Architect prep is explicit: "super_admin is
-- meant to be the only role that can edit the role→action grant matrix
-- itself." A manager or admin holding this action could grant themselves
-- (or anyone) any other permission in the catalog, which would make
-- super_admin's exclusivity over the matrix meaningless.
--
-- Nothing in the codebase creates a super_admin-role user yet (see #555's
-- tracking comment, and internal/pages/audit_page_test.go's own note) —
-- this card's editor is therefore unreachable in production until a
-- separate card adds a super_admin promotion path. Tracked as a follow-up
-- (see the #556 close-out comment), not blocking this card: the schema,
-- Can() check and editor are all independently correct and testable
-- (auth.WithUser sets an arbitrary role in tests) without that path
-- existing yet, same as audit_page.go and backoffice_page.go's existing
-- super_admin-gated surfaces already ship today.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('permission_management');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, 'permission_management', 1
FROM roles r
WHERE r.role IN ('super_admin');
