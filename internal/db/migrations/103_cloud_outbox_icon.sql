-- 103_cloud_outbox_icon.sql — ut-docs#6071 (contract
-- cloud-catalog-changes.md 1.2.0): an item's and a category's icon now
-- upload to the cloud like the other mastered fields, so the cloud masters
-- it and stops serving "" over a till icon.
--
-- 1. The item (102) and category (084) update triggers watched only the
--    fields of the earlier contract minors, so an icon edit left no outbox
--    mark. Both are recreated with icon added; their WHEN gating
--    (cloud/adopting catalogue mode, main till only, no suppress row) and
--    body are unchanged. 084 and 102 themselves stay frozen (ADR-0100).
--
-- 2. Seeding (the promotion recipe of the changes contract, 1.2.0). The
--    cloud never wrote icon, so it holds "" for every row, whatever the
--    till shows. Each linked row's upload base gets icon "" (the cloud's
--    value), and each linked row whose local icon is set gets an outbox
--    mark, only where the trigger would mark (cloud/adopting mode, main
--    till). The first upload then sends before "" and the cloud applies
--    it; until it is answered the icon is a pending till edit, and the
--    pull's field-wise apply keeps it. No ApplierVersion bump: a re-pull
--    would overwrite the till icons with the cloud's "".
--
--    The marks are written BEFORE the bases: they select the rows whose
--    base has no icon key yet, so running them second would find none.
--    Run again, neither statement finds a base without the key: a no-op.
--    A damaged (non-JSON) or NULL base (an applier-3 link, which the
--    pull's base seeding handles) and a link whose row is gone are left
--    alone.
DROP TRIGGER IF EXISTS trg_cloud_outbox_items_upd;

CREATE TRIGGER trg_cloud_outbox_items_upd
AFTER UPDATE OF name, description, sku, base_price, category_id, tax_code_id, is_active, is_weighed, age_restricted,
    net_quantity_value, net_quantity_unit, icon ON items
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = NEW.id AND sealed_at IS NULL);
END;

DROP TRIGGER IF EXISTS trg_cloud_outbox_categories_upd;

CREATE TRIGGER trg_cloud_outbox_categories_upd
AFTER UPDATE OF name, parent_id, sort_order, is_active, icon ON categories
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'category', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'category' AND local_id = NEW.id AND sealed_at IS NULL);
END;

INSERT INTO cloud_outbox (entity, local_id, created_at)
SELECT 'item', i.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  FROM items i
  JOIN cloud_links l ON l.entity = 'item' AND l.local_id = i.id
 WHERE COALESCE(i.icon, '') <> ''
   AND l.base IS NOT NULL
   AND json_valid(l.base)
   AND json_type(l.base, '$.icon') IS NULL
   AND (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
   AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox o WHERE o.entity = 'item' AND o.local_id = i.id AND o.sealed_at IS NULL);

INSERT INTO cloud_outbox (entity, local_id, created_at)
SELECT 'category', c.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  FROM categories c
  JOIN cloud_links l ON l.entity = 'category' AND l.local_id = c.id
 WHERE COALESCE(c.icon, '') <> ''
   AND l.base IS NOT NULL
   AND json_valid(l.base)
   AND json_type(l.base, '$.icon') IS NULL
   AND (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
   AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
   AND NOT EXISTS (SELECT 1 FROM cloud_outbox o WHERE o.entity = 'category' AND o.local_id = c.id AND o.sealed_at IS NULL);

UPDATE cloud_links
   SET base = json_set(base, '$.icon', '')
 WHERE entity IN ('item', 'category')
   AND base IS NOT NULL
   AND json_valid(base)
   AND json_type(base, '$.icon') IS NULL;
