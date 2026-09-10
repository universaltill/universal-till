-- 025_modifier_group_links.sql — ADR-0090 (ut-docs#2013, product owner:
-- "one modifier should be able to use on more than one item"). Additive
-- only: item_modifier_groups.item_id and its FOREIGN KEY/index are left
-- completely untouched, on purpose — see the ADR's "hard constraint"
-- section (migration 003's already-paid-for lesson: PRAGMA foreign_keys is
-- pinned ON with no in-transaction toggle, so rebuilding a PARENT table
-- like item_modifier_groups inside a migration transaction would cascade-
-- delete its item_modifier_options children before any rename ever runs).
--
-- Mirrors option_sets/item_option_sets (020_option_sets.sql, ut-docs#1900)
-- exactly: the group stays the identity/config record (name, required,
-- min_select, max_select, is_active), this table records which items use
-- it. required/min_select/max_select deliberately stay group-only — one
-- shared rule set, not per-item divergence, per the ADR. sort_order is
-- per-link (same reasoning as item_option_sets.axis_order): a shared group
-- can sit in a different position within two different items' own lists.
--
-- IF NOT EXISTS / INSERT OR IGNORE throughout, same reason as 021/023: this
-- repo's fiscal_signing_keys_{rename,split}_test.go rewinds the ledger and
-- re-runs every later migration against an already-migrated file
-- (openAtPreMigrationSchema), and db.go's runner only supplies replay
-- idempotence for ADD COLUMN itself — so every statement here must be
-- re-appliable on its own. Pinned by internal/db's
-- TestMigration025_BackfillsOneLinkPerExistingGroup.
CREATE TABLE IF NOT EXISTS item_modifier_group_links (
    item_id     TEXT NOT NULL,
    group_id    TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id)  REFERENCES items (id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES item_modifier_groups (id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_item_modifier_group_links_group
    ON item_modifier_group_links (group_id);

-- Backfill: one link per existing group, to the item it already belonged
-- to, carrying its current sort_order across unchanged — every existing
-- shop's exact current single-item membership and ordering is preserved
-- with zero visible change. OR IGNORE keeps a replay from duplicating (the
-- PK is (item_id, group_id)).
INSERT OR IGNORE INTO item_modifier_group_links (item_id, group_id, sort_order)
SELECT item_id, id, sort_order FROM item_modifier_groups;

-- The table joins sync_admin_repo.go's adminTables (ADR-0090 §3), so it
-- needs the same three sync_admin_version bump triggers every other entry
-- got in 023 — that file's header: "a table added to adminTables later
-- needs its own three triggers in a NEW migration". Pinned by
-- TestSyncAdminVersion_EveryAdminTableHasTriggers.
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_group_links_ins AFTER INSERT ON item_modifier_group_links
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_group_links_upd AFTER UPDATE ON item_modifier_group_links
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_group_links_del AFTER DELETE ON item_modifier_group_links
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
