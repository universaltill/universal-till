-- Relax tills.bearer_hash to nullable (ut-docs#405).
--
-- #405 adds "tills" to the D4 admin-bundle sync (adminTables in
-- sync_admin_repo.go) so every replica gets a read-only copy of the shop's
-- till roster — needed so the sync chip can show the primary's real name
-- instead of a bare count, and so a replica's /tills page has something to
-- show at all (today ListTills() on a replica returns nothing, because
-- this table was never synced).
--
-- bearer_hash is the sync-auth secret for that row's till and must never
-- leave the primary — a replica must never learn another till's bearer
-- hash, or even its own (a replica never validates incoming sync bearers
-- itself, so it has no use for the column). DumpAdmin's redactCols
-- mechanism (adminTables in sync_admin_repo.go — stronger than the
-- existing skipCols, already used for payment_methods.plugin_id) strips it
-- out of every dumped row AND force-sets it to NULL on every apply, even
-- on a row that already existed locally with a real value: a replica can
-- already hold every till's real bearer_hash from day one via the
-- enrolment snapshot (a full-DB copy, ut-docs#368), so merely never
-- SETTING a new value (skipCols's weaker "leave it alone" semantics) would
-- let that pre-existing real secret sit there forever, untouched by every
-- subsequent pull. Either way, every row a replica upserts via ApplyAdmin
-- ends up with bearer_hash = NULL — the NOT NULL constraint would reject
-- that outright, so it has to go. SQLite treats every NULL as distinct
-- under a UNIQUE index, so this can never let two real bearer hashes
-- collide on the primary, where the column is still always populated by
-- TillsRepo.InsertTill.
--
-- SQLite has no ALTER COLUMN — table rebuild is the standard way to drop a
-- constraint. No FK references tills (verified: no other table declares
-- REFERENCES tills), so no child-table fixups are needed.
CREATE TABLE tills_new (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    bearer_hash  TEXT UNIQUE,
    enrolled_at  TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen_at TEXT
);

INSERT INTO tills_new (id, name, bearer_hash, enrolled_at, last_seen_at)
    SELECT id, name, bearer_hash, enrolled_at, last_seen_at FROM tills;

DROP TABLE tills;

ALTER TABLE tills_new RENAME TO tills;
