-- 031_category_modifier_group_links.sql — ADR-0094 (ut-docs#1915, product
-- owner: "assign multiple modifiers to a category and all the items in the
-- category will inherit them, or can override or add something to them").
-- Extends ADR-0090's item-level sharing (025) with a second, independent
-- attachment target — the item's category — plus a per-item opt-out.
--
-- Additive only: two new link tables, no ALTER/DROP on any existing table.
-- Unlike 025 there is NO backfill (nothing today expresses a category-level
-- attachment, so there is nothing to migrate forward) and NO anchor problem
-- (ADR-0094 Decision 1): a modifier group's identity still anchors to
-- exactly one ITEM via item_modifier_groups.item_id, untouched here — a
-- category is only ever an additional attachment target, never a group's
-- sole owner, so hard-deleting a category cascades away only that
-- category's attachment rows, never the group row. Neither table below holds
-- any group identity of its own; both are pure (parent, group) presence rows.
--
-- item_modifier_group_opt_outs is presence-only (ADR-0094 Decision 2): a row
-- means "this item declined this CATEGORY-inherited group"; opting back in
-- is deleting the row. It has no effect on a group the item is DIRECTLY
-- linked to via item_modifier_group_links — that's what
-- UnlinkGroupFromItemUnlessLastLink is for. Resolution (ADR-0094 Decision 3)
-- happens at read time in ModifierRepo.ResolveGroupsForItem, never as a
-- per-item snapshot, so editing a category's groups takes effect immediately.
--
-- IF NOT EXISTS throughout, same reason as 021/023/025: this repo's
-- fiscal_signing_keys_{rename,split}_test.go rewinds the ledger and re-runs
-- every later migration against an already-migrated file
-- (openAtPreMigrationSchema), and db.go's runner only supplies replay
-- idempotence for ADD COLUMN itself — so every statement here must be
-- re-appliable on its own. Pinned by internal/db's
-- TestMigration031_CreatesBothTablesAndIsReplaySafe.
CREATE TABLE IF NOT EXISTS category_modifier_group_links (
    category_id TEXT NOT NULL,
    group_id    TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (category_id) REFERENCES categories (id) ON DELETE CASCADE,
    FOREIGN KEY (group_id)    REFERENCES item_modifier_groups (id) ON DELETE CASCADE,
    PRIMARY KEY (category_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_category_modifier_group_links_group
    ON category_modifier_group_links (group_id);

CREATE TABLE IF NOT EXISTS item_modifier_group_opt_outs (
    item_id  TEXT NOT NULL,
    group_id TEXT NOT NULL,
    FOREIGN KEY (item_id)  REFERENCES items (id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES item_modifier_groups (id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_item_modifier_group_opt_outs_group
    ON item_modifier_group_opt_outs (group_id);

-- Both tables join sync_admin_repo.go's adminTables (ADR-0094 §4), so each
-- needs the same three sync_admin_version bump triggers every other entry
-- got in 023/025 — that file's header: "a table added to adminTables later
-- needs its own three triggers in a NEW migration". Pinned by
-- TestSyncAdminVersion_EveryAdminTableHasTriggers.
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_category_modifier_group_links_ins AFTER INSERT ON category_modifier_group_links
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_category_modifier_group_links_upd AFTER UPDATE ON category_modifier_group_links
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_category_modifier_group_links_del AFTER DELETE ON category_modifier_group_links
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_group_opt_outs_ins AFTER INSERT ON item_modifier_group_opt_outs
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_group_opt_outs_upd AFTER UPDATE ON item_modifier_group_opt_outs
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_group_opt_outs_del AFTER DELETE ON item_modifier_group_opt_outs
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
