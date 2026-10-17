-- 084_cloud_outbox.sql — ut-docs#4464 (ADR-0162 §5): the outbox of a
-- cloud-master store's main till. Every local write to the uploadable part
-- of the mastered set (contract ut-docs reference/contracts/
-- cloud-catalog-changes.md 1.0.0: items with their item-level barcodes,
-- categories, tax codes) leaves an outbox row IN THE SAME TRANSACTION, so
-- no edit can commit without one; internal/catalogpull uploads them before
-- each pull.
--
-- The INSERT/UPDATE/DELETE paths onto these tables are many (screens,
-- imports, directives, the additional tills' write-through, backfills), so
-- triggers write the rows, as 065 stamps updated_at. A trigger marks the
-- row dirty — (entity, local_id) and the time of the first edit — and the
-- uploader turns a mark into a change when it seals it: the fields whose
-- local value differs from the row's base (the values the till last
-- adopted from the cloud, cloud_links.base). Repeated edits before an
-- upload share one open mark (the partial unique index), so the cloud
-- sees the net change; a sealed (in-flight) mark is never reopened — a
-- later edit opens a new one. Sealed marks upload in seal order
-- (seal_seq): a row referenced by another (a new category, its new item)
-- is sealed, and so sent, first.
--
-- A trigger fires only when
--   * the cached catalogue mode is cloud or adopting (ADR-0162 §1/§6:
--     entitlement.catalog_master, per-till, written by the check-in);
--   * this till is the main till (no sync.primary_url: an additional
--     till's edits are written through to the main till and marked there;
--     its LAN-pulled copies are never marked);
--   * no cloud_outbox_suppress row exists. The pull (and the upload's
--     answer) inserts that row first in its own write transaction and
--     deletes it before commit: SQLite has one writer, so no other
--     transaction can ever see it, and the cloud's own rows are never
--     marked as till edits.
--
-- Both tables are main-till bookkeeping, never LAN-synced (sync_admin_repo
-- not-synced list). No depth cap: a till-side import (ADR-0161) may mark
-- 20 000 rows; they upload 100 per call. Age is the only bound (30 days →
-- uploaded as stale, logged by the cloud).
CREATE TABLE IF NOT EXISTS cloud_outbox (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    entity     TEXT    NOT NULL,
    local_id   TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    change_id  TEXT,
    body       TEXT,
    sealed_at  TEXT,
    seal_seq   INTEGER
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_cloud_outbox_open
    ON cloud_outbox (entity, local_id) WHERE sealed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_cloud_outbox_local
    ON cloud_outbox (entity, local_id);

CREATE INDEX IF NOT EXISTS idx_cloud_outbox_seal
    ON cloud_outbox (seal_seq);

CREATE TABLE IF NOT EXISTS cloud_outbox_suppress (
    one INTEGER PRIMARY KEY CHECK (one = 1)
);

-- base: the JSON of the uploadable fields the till last adopted from the
-- cloud for this row (NULL until a pull or an upload answer sets it).
-- echo_version: the version of the till's own accepted upload; the feed's
-- row at that version is the echo and is not applied again.
ALTER TABLE cloud_links ADD COLUMN base TEXT;
ALTER TABLE cloud_links ADD COLUMN echo_version INTEGER NOT NULL DEFAULT 0;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_items_ins AFTER INSERT ON items
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = NEW.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_items_upd
AFTER UPDATE OF name, description, sku, base_price, category_id, tax_code_id, is_active ON items
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = NEW.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_items_del AFTER DELETE ON items
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', OLD.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = OLD.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_item_barcodes_ins AFTER INSERT ON item_barcodes
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', NEW.item_id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = NEW.item_id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_item_barcodes_upd
AFTER UPDATE OF barcode, item_id, is_primary ON item_barcodes
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', NEW.item_id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = NEW.item_id AND sealed_at IS NULL);
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', OLD.item_id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE OLD.item_id <> NEW.item_id
    AND NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = OLD.item_id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_item_barcodes_del AFTER DELETE ON item_barcodes
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', OLD.item_id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = OLD.item_id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_categories_ins AFTER INSERT ON categories
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'category', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'category' AND local_id = NEW.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_categories_upd
AFTER UPDATE OF name, parent_id, sort_order, is_active ON categories
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'category', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'category' AND local_id = NEW.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_categories_del AFTER DELETE ON categories
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'category', OLD.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'category' AND local_id = OLD.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_tax_codes_ins AFTER INSERT ON tax_codes
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'tax_code', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'tax_code' AND local_id = NEW.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_tax_codes_upd
AFTER UPDATE OF name, rate_basis_points, takeaway_rate_basis_points, is_active ON tax_codes
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'tax_code', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'tax_code' AND local_id = NEW.id AND sealed_at IS NULL);
END;

CREATE TRIGGER IF NOT EXISTS trg_cloud_outbox_tax_codes_del AFTER DELETE ON tax_codes
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'tax_code', OLD.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'tax_code' AND local_id = OLD.id AND sealed_at IS NULL);
END;
