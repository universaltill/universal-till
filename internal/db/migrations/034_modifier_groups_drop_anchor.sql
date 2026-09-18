-- 034_modifier_groups_drop_anchor.sql — ADR-0101 (ut-docs#2399): a
-- modifier group is a SHOP-WIDE entity. item_modifier_groups loses its
-- item_id "anchor" column (001_init.sql: NOT NULL, ON DELETE CASCADE onto
-- items), which is what made "a modifier can only be created attached to a
-- catalog item" true at the schema level. Membership has been read through
-- item_modifier_group_links since 025 (ADR-0090) and through
-- category_modifier_group_links since 031 (ADR-0094); the anchor was only
-- ever kept because ADR-0090 believed this table could not be rebuilt.
-- The table KEEPS its name (item_modifier_groups) — ~35 queries, 023's
-- trigger names, sync_admin_repo.go's adminTables and ut-cloud's snapshot
-- code all name it; the item_ prefix is historical from here on.
--
-- Why a rebuild (create replacement, copy, drop, rename): SQLite has no
-- DROP COLUMN for a column that carries a FOREIGN KEY or an index, and
-- every file under this directory is frozen the moment it merges (ADR-0100,
-- shipped_migrations_test.go) — so 001 cannot be edited, and this file is
-- the only way to change the shape. 018 (ADR-0088) is the established
-- rebuild pattern for a LEAF table; this file applies it to a PARENT.
--
-- ORDER MATTERS — this is the whole safety argument (ADR-0101 Context):
-- internal/db/db.go pins PRAGMA foreign_keys = ON on every connection, and
-- applyMigration runs this file inside one transaction. Under those two
-- facts, 003_kitchen_station_display_flag.sql proved that DROP TABLE on an
-- FK PARENT whose children still reference it performs an implicit
-- DELETE FROM that fires every child's ON DELETE CASCADE — every option,
-- link and opt-out row would silently vanish. ADR-0090 read that as "the
-- parent can never be rebuilt". It only forbids dropping the parent WHILE
-- children point at it. So:
--
--   1. Create item_modifier_groups_new (identical to 001 minus item_id, its
--      FK and its index) and copy the group rows in. The copy names the
--      surviving columns only — NEVER item_id — so re-running this file
--      against an already-migrated database (openAtPreMigrationSchema's
--      replay contract, which every migration here must honour) reads the
--      anchor-free table and the rebuild is a no-op in effect, exactly as
--      018 replays today.
--   2. Rebuild EACH of the four children — item_modifier_options,
--      item_modifier_group_links, category_modifier_group_links,
--      item_modifier_group_opt_outs — the 018 way, identical columns/PK/
--      CHECK/FKs except `REFERENCES item_modifier_groups_new (id)`. Every
--      one of them is a LEAF (grep `REFERENCES item_modifier_` in this
--      directory finds only their four clauses onto the groups table;
--      nothing references any of THEM), so dropping the old child cascades
--      into nothing and violates nothing. Recreate its index and its three
--      trg_sync_admin_version_* triggers (023's contract: three per
--      adminTables entry) — DROP TABLE took both with it.
--   3. DROP TABLE item_modifier_groups — now CHILDLESS, so its implicit
--      DELETE FROM cascades into nothing. Its own three triggers and
--      idx_item_modifier_groups_item (the anchor's index) go with it.
--   4. ALTER TABLE item_modifier_groups_new RENAME TO item_modifier_groups.
--      SQLite >= 3.26 with legacy_alter_table OFF (the default;
--      modernc.org/sqlite ships 3.4x) rewrites every child's
--      `REFERENCES item_modifier_groups_new` to the new name as part of the
--      rename — the same behaviour 018's own header documents relying on.
--      Recreate the parent's three triggers.
--
-- sale_line_modifiers (and its archive) carries no FOREIGN KEY on group_id
-- or option_id and snapshots names/prices at sale time (ADR-0090 Context),
-- so historical sales are untouched by any of this.
--
-- Pinned by migration_034_modifier_groups_drop_anchor_test.go: a real
-- pre-034 database (001..033 through the real runner, seeded with anchored
-- groups, options, item links, category links and opt-outs) survives the
-- upgrade byte-for-byte, the replay leaves it unchanged, every FK points
-- at item_modifier_groups, PRAGMA foreign_key_check is clean, all fifteen
-- sync triggers exist, and DeleteGroup still cascades.

-- ---------------------------------------------------------------------
-- 1. The anchor-free parent, and the group rows.
-- ---------------------------------------------------------------------
CREATE TABLE item_modifier_groups_new (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    required    INTEGER NOT NULL DEFAULT 0,
    min_select  INTEGER NOT NULL DEFAULT 0,
    max_select  INTEGER NOT NULL DEFAULT 1,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    is_active   INTEGER NOT NULL DEFAULT 1,
    CHECK (min_select >= 0 AND max_select >= min_select)
);

INSERT INTO item_modifier_groups_new (id, name, required, min_select, max_select, sort_order, is_active)
SELECT id, name, required, min_select, max_select, sort_order, is_active
FROM item_modifier_groups;

-- ---------------------------------------------------------------------
-- 2a. item_modifier_options (001_init.sql shape).
-- ---------------------------------------------------------------------
CREATE TABLE item_modifier_options_new (
    id                TEXT PRIMARY KEY,
    group_id          TEXT NOT NULL,
    name              TEXT NOT NULL,
    price_delta_minor INTEGER NOT NULL DEFAULT 0,
    sort_order        INTEGER NOT NULL DEFAULT 0,
    is_active         INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (group_id) REFERENCES item_modifier_groups_new (id) ON DELETE CASCADE,
    CHECK (price_delta_minor >= 0)
);

INSERT INTO item_modifier_options_new (id, group_id, name, price_delta_minor, sort_order, is_active)
SELECT id, group_id, name, price_delta_minor, sort_order, is_active
FROM item_modifier_options;

DROP TABLE item_modifier_options;
ALTER TABLE item_modifier_options_new RENAME TO item_modifier_options;

CREATE INDEX IF NOT EXISTS idx_item_modifier_options_group
    ON item_modifier_options (group_id);

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_options_ins AFTER INSERT ON item_modifier_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_options_upd AFTER UPDATE ON item_modifier_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_options_del AFTER DELETE ON item_modifier_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

-- ---------------------------------------------------------------------
-- 2b. item_modifier_group_links (025_modifier_group_links.sql shape).
-- ---------------------------------------------------------------------
CREATE TABLE item_modifier_group_links_new (
    item_id     TEXT NOT NULL,
    group_id    TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id)  REFERENCES items (id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES item_modifier_groups_new (id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, group_id)
);

INSERT INTO item_modifier_group_links_new (item_id, group_id, sort_order)
SELECT item_id, group_id, sort_order
FROM item_modifier_group_links;

DROP TABLE item_modifier_group_links;
ALTER TABLE item_modifier_group_links_new RENAME TO item_modifier_group_links;

CREATE INDEX IF NOT EXISTS idx_item_modifier_group_links_group
    ON item_modifier_group_links (group_id);

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

-- ---------------------------------------------------------------------
-- 2c. category_modifier_group_links (031_category_modifier_group_links.sql).
-- ---------------------------------------------------------------------
CREATE TABLE category_modifier_group_links_new (
    category_id TEXT NOT NULL,
    group_id    TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (category_id) REFERENCES categories (id) ON DELETE CASCADE,
    FOREIGN KEY (group_id)    REFERENCES item_modifier_groups_new (id) ON DELETE CASCADE,
    PRIMARY KEY (category_id, group_id)
);

INSERT INTO category_modifier_group_links_new (category_id, group_id, sort_order)
SELECT category_id, group_id, sort_order
FROM category_modifier_group_links;

DROP TABLE category_modifier_group_links;
ALTER TABLE category_modifier_group_links_new RENAME TO category_modifier_group_links;

CREATE INDEX IF NOT EXISTS idx_category_modifier_group_links_group
    ON category_modifier_group_links (group_id);

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

-- ---------------------------------------------------------------------
-- 2d. item_modifier_group_opt_outs (031_category_modifier_group_links.sql).
-- ---------------------------------------------------------------------
CREATE TABLE item_modifier_group_opt_outs_new (
    item_id  TEXT NOT NULL,
    group_id TEXT NOT NULL,
    FOREIGN KEY (item_id)  REFERENCES items (id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES item_modifier_groups_new (id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, group_id)
);

INSERT INTO item_modifier_group_opt_outs_new (item_id, group_id)
SELECT item_id, group_id
FROM item_modifier_group_opt_outs;

DROP TABLE item_modifier_group_opt_outs;
ALTER TABLE item_modifier_group_opt_outs_new RENAME TO item_modifier_group_opt_outs;

CREATE INDEX IF NOT EXISTS idx_item_modifier_group_opt_outs_group
    ON item_modifier_group_opt_outs (group_id);

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

-- ---------------------------------------------------------------------
-- 3 + 4. The old parent is childless now: drop it, rename the replacement
-- (the rename rewrites the four children's REFERENCES clauses), recreate
-- the parent's own three triggers.
-- ---------------------------------------------------------------------
DROP TABLE item_modifier_groups;
ALTER TABLE item_modifier_groups_new RENAME TO item_modifier_groups;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_groups_ins AFTER INSERT ON item_modifier_groups
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_groups_upd AFTER UPDATE ON item_modifier_groups
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_groups_del AFTER DELETE ON item_modifier_groups
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
