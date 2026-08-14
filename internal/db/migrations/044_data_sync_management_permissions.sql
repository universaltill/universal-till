-- 044: extend the #554 action catalog with `data_management` and
-- `sync_management` (ut-docs#707, another #555 split successor — the
-- Data/sync/device-pairing pages sweep).
--
-- `data_management`: internal/pages/data_api.go's reset-transactions,
-- reset-archives list/restore/purge, GDPR customer search/erase, catalog
-- cleanup preview/apply, and data/export, plus internal/pages/backup_api.go's
-- local backup/restore (all of it: "Manager/admin only throughout" per that
-- file's own doc comment) — one action, since every one of these is the same
-- shape of operation (destroy/restore/export the till's own stored data),
-- same precedent as 043's plugin_management grouping a whole subsystem
-- under one action rather than one-per-endpoint.
--
-- `sync_management`: internal/pages/sync_api.go's Tills page, enrol-token,
-- revoke, promote, join; the LAN discovery scan gate (managerGate, shared —
-- see below); internal/pages/pairing_api.go's pending pair-request list;
-- internal/pages/pending_pairings.go's pending-pairings card UI. These are
-- multi-till/device-pairing administration, a different risk shape from
-- data_management (network/topology, not stored-data destruction), so a
-- separate action rather than folding into data_management or settings.
--
-- Scope note: api_gates.go's `managerGate` is the ONE gate function behind
-- both discovery_api.go's /api/sync/discover-primaries AND
-- pairing_join.go's /api/sync/pair-start + /api/sync/pair-status —
-- pairing_join.go isn't in #707's named file list, but converting the
-- shared gate function necessarily converts its call sites too; leaving it
-- on isManagerOrAuthOff while its sibling routes moved to Can() would split
-- one logical gate into two different mechanisms silently. All three now
-- gate on sync_management as one coherent group.
--
-- Additive: seeded identically to 039/042/043's pattern (manager/admin/
-- super_admin granted, cashier denied) so no existing till's access changes.
INSERT OR IGNORE INTO permission_actions (action) VALUES
    ('data_management'),
    ('sync_management');

INSERT OR IGNORE INTO role_permissions (role, action, granted)
SELECT r.role, a.action, 1
FROM roles r
CROSS JOIN permission_actions a
WHERE r.role IN ('manager', 'admin', 'super_admin')
  AND a.action IN ('data_management', 'sync_management');
