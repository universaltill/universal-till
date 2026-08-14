-- 045: extend the #554 action catalog with `import_export` and
-- `issue_reporting` (ut-docs#713, the "Import / issue-report / menu / misc
-- pages" #555 split successor — the most heterogeneous of the five
-- successors, per its own card text).
--
-- `import_export`: internal/pages/import_page.go's catalog import page,
-- CSV/ZIP export, export-save-to-disk, and the commit-import POST — four
-- sites moving catalog data in or out of the till in bulk, a distinct risk
-- shape from data_management (044: destroy/restore/export the till's OWN
-- stored transactional/customer data) and from settings (a config edit) —
-- closer to a supply-chain-facing operation than either, so it gets its
-- own action rather than overloading one of those two. This is the new
-- action the card's own text anticipated ("import/export specifically...
-- a new import_export action").
--
-- internal/pages/import_dispatch.go's POST /api/data/import (plugin-import
-- dispatch, not catalog-specific — it hands the upload to whichever
-- import-type plugin entry the caller names, over arbitrary declared
-- entities) is grouped under import_export too, alongside its sibling
-- import_page.go sites, rather than data_management like its documented
-- mirror POST /api/data/export (044) — a real judgment call flagged by
-- independent review (ut-docs#713) as worth reconsidering, not obviously
-- wrong: tracked as a follow-up card rather than blocking this one, since
-- both actions are seeded identically to manager/admin/super_admin today
-- and the split is inert either way.
--
-- `issue_reporting`: internal/pages/issue_report_page.go's "report an
-- issue" panel, its nav chip, and the bundle-upload API (ADR-0022) — a
-- support-facing capture flow (typed/spoken description + optional screen
-- recording), unrelated to any existing catalog action. New action rather
-- than folding into settings: reporting a bug is not a configuration
-- change, and a future custom role might reasonably get one without the
-- other.
--
-- Everything else in #713's file list (menu.html's manager-only nav tile
-- visibility, the /backoffice landing page and its "/" redirect, and the
-- reports/ask AI endpoint) reuses the existing `settings`/`reports`
-- actions respectively — see the call sites' own comments for why. No new
-- catalog entry needed for those.
--
-- Additive: seeded identically to 039/042/043/044's pattern (manager/
-- admin/super_admin granted, cashier denied) so no existing till's access
-- changes.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('import_export'),
    ('issue_reporting');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin')
  AND a.action IN ('import_export', 'issue_reporting');
