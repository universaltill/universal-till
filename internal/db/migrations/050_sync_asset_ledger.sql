-- ut-docs#2785: the replica's record of which asset files it downloaded
-- from the main till (internal/pages/sync_assets.go). Only a file listed
-- here — and still exactly as downloaded (size + mtime) — may ever be
-- pruned when the main's complete manifest stops listing it; a photo
-- uploaded on the replica itself never gets a row. unreferenced_since
-- (unix seconds) is when the main's manifest first stopped listing the
-- path; NULL while it is still listed. unreferenced_misses counts the
-- consecutive complete manifests that have missed it (a prune needs two, as
-- well as the wall-clock grace, so a clock jump alone never prunes); a
-- relisting resets both. Till-local, never synced.
CREATE TABLE IF NOT EXISTS sync_asset_ledger (
    scope              TEXT    NOT NULL,
    path               TEXT    NOT NULL,
    size               INTEGER NOT NULL,
    mod                INTEGER NOT NULL,
    unreferenced_since INTEGER,
    unreferenced_misses INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, path)
);
