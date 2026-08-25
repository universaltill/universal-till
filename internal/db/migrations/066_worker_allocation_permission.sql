-- 066: extend the action catalog with `worker_allocation` (ADR-0063,
-- ut-docs#964) — gates recording a worker payout (tip/service-charge
-- distribution) and exporting the received-vs-allocated report on the new
-- "tips" reports tab. Same 042/057 pattern: manager/admin/super_admin
-- granted, cashier denied — this is a manager-recorded statutory record
-- (UK Employment (Allocation of Tips) Act 2023), not a cashier-facing
-- action. Additive, append-only: 065 (the worker_allocations table itself)
-- is untouched.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('worker_allocation');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin')
  AND a.action IN ('worker_allocation');
