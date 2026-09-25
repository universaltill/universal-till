-- 044_held_sales_tombstones.sql — ADR-0093 Amendment B (universaltill/
-- ut-docs#2712, "resume needs an atomic claim, not a read-then-restore").
-- One row per held sale the PRIMARY has deleted recently: written by both
-- primary-side deletions (POST /api/sync/held-sales/delete and the new
-- POST /api/sync/held-sales/claim, plus the primary till's own resume) via
-- HeldSalesRepo.DeleteAndTombstone / ClaimAndTombstone. It is the proof
-- "this id was resolved, and when" that a replica's stale copy needs:
-- without it, a copy left primary_synced = 0 because the push's REPLY was
-- lost (the primary did apply it) is indistinguishable from a genuine
-- offline park, and resuming it tenders the same order a second time.
--
-- Short-lived by design: rows older than 24h are pruned opportunistically
-- on every write (no cron). A till offline for longer finds no tombstone and
-- trusts its own local row, exactly as before -- the TTL is what keeps this
-- additive to ADR-0093 Decision 4's offline-first guarantee. deleted_at is
-- datetime('now') UTC text, the same shape every other timestamp here uses.
--
-- till (independent review of #2712) is WHO resolved it: the enrolled
-- replica's tills.id when the deletion came in over /claim or /delete, ''
-- for the primary till's own resume. A tombstone only ever contradicts a
-- copy held by a DIFFERENT till: after a resume the same order is re-parked
-- under the same id (ut-docs#1918), and when that re-park alone fell back to
-- local-only (primary briefly unreachable) the re-parking till's copy is
-- the newest one in the shop -- its own earlier tombstone must not refuse
-- it and throw the order away. So /claim answers known=true only for a
-- tombstone some OTHER till wrote (HeldSalesRepo.ClaimAndTombstone).
--
-- Per-database and never synced (sync_admin_repo.go's nonAdminTables): it
-- only means anything on the primary, which is the one database every
-- replica's /claim is answered from.
CREATE TABLE IF NOT EXISTS held_sales_tombstones (
    id         TEXT PRIMARY KEY,
    deleted_at TEXT NOT NULL,
    till       TEXT NOT NULL DEFAULT ''
);
