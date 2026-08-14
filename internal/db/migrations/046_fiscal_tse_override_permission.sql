-- 046: `fiscal_tse_override` action for the German TSE hard gate's owner
-- override (ADR-0048 Decision 3, ut-docs#715).
--
-- Gates POST /api/fiscal/tse-override (internal/pages/fiscal_api.go) — the
-- time-boxed, typed-acknowledgement, audit-logged override that lets an
-- owner keep trading while a CONFIGURED TSE is failing — and the generic
-- settings editor's writes to the fiscal.* posture keys
-- (internal/pages/settings_page.go's upsert handler).
--
-- Seeded to ('admin', 'super_admin') ONLY — deliberately NOT the
-- manager-inclusive pattern every earlier action grant (039/042/043/044/
-- 045) follows. "Owner" maps to the till's admin role (ADR-0048: an
-- engineering mapping, not a new RBAC role), and this must not silently
-- become manager-or-above: a manager's PIN authenticates fine through the
-- shared PIN machinery but is refused by the handler's own role check.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('fiscal_tse_override');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, 'fiscal_tse_override', 1
FROM roles r
WHERE r.role IN ('admin', 'super_admin');
