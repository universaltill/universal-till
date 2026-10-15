-- 079_till_tls_pinned.sql — ut-docs#2737 (ADR-0114 §7, "Enforcement"): the
-- main till records, per enrolled till, that the till has authenticated a
-- sync request over pinned LAN TLS. From then on that till's bearer is
-- refused on plain HTTP (pages.syncTill): a pinned till never sends plain
-- HTTP (ut-docs#4092), so a plain request carrying its bearer is a stolen
-- bearer or a downgrade attempt.
--
-- Its own table, not a column on tills: tills is admin-synced whole-row
-- (sync_admin_repo.go), so a new column would reach every joined till and
-- every mixed-version rollout. This is main-till bookkeeping, never synced
-- (sync_admin_repo.go's not-synced list). A till's record goes with its row
-- (revoke, ON DELETE CASCADE); a re-join is a new tills row, so it starts
-- unpinned.
CREATE TABLE IF NOT EXISTS till_tls_pinned (
    till_id   TEXT PRIMARY KEY REFERENCES tills(id) ON DELETE CASCADE,
    pinned_at TEXT NOT NULL
);
