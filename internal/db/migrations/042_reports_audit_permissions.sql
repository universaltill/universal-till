-- 042: extend the #554 action catalog with `reports` and `audit` — the
-- first two new actions consumed since 039 (#555's split successors start
-- pointing real call sites at Auth.Can(); #709, the reports/EOD/audit/
-- journal successor, needs actions the original 7 didn't cover: EOD's own
-- run/print/range/settings/report-retention/archive-export sites reuse the
-- existing `eod_report` action, but the reports page, my-reports, the
-- journal's invoicing-link gate, and the audit trail don't fit any of the
-- 7 catalog entries). Additive: seeded identically to 039's pattern
-- (manager/admin/super_admin granted, cashier denied) so no existing
-- till's access changes.
--
-- Two `eod_report` sites are a judgment call, not an obvious fit (#709
-- review): /api/settings/report-retention and /api/reports/archive/export
-- are report-retention/export operations, not EOD generation specifically
-- — kept under `eod_report` because they live on the same reports/EOD
-- settings panel, not because they're semantically pure EOD. A future
-- card is free to split them onto `reports` instead; behaviorally
-- identical today since every role holding one holds the other.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('reports'),
    ('audit');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin')
  AND a.action IN ('reports', 'audit');
