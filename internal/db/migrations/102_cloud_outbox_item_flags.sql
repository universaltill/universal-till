-- 102_cloud_outbox_item_flags.sql — ut-docs#6082/#6202 (contract
-- cloud-catalog-changes.md 1.1.0): an item's is_weighed, age_restricted and
-- net quantity (net_quantity_value + net_quantity_unit, which always travel
-- together) now upload to the cloud like the other mastered item fields.
--
-- 1. The item update trigger (084) watched only the fields of contract
--    1.0.0, so an edit of one of these four columns left no outbox mark.
--    It is recreated with them added; its WHEN gating (cloud/adopting
--    catalogue mode, main till only, no suppress row) and body are
--    084's, unchanged. 084 itself stays frozen (ADR-0100).
--
-- 2. Upload bases (cloud_links.base, 084) pulled by an applier-4 build lack
--    the four fields, and an upload of an edit compares each field with
--    its base entry. They are filled in from the local row, as seedBases
--    does for a base-less link: those local values are what the till last
--    pulled, so there is no window between this upgrade and the applier-5
--    re-pull in which an edit has no base to differ from (it would upload
--    nothing, and the re-pull would overwrite it). A base that already has
--    the fields, a damaged (non-JSON) or NULL base, and a link whose item
--    is gone are left alone; re-running changes nothing.
DROP TRIGGER IF EXISTS trg_cloud_outbox_items_upd;

CREATE TRIGGER trg_cloud_outbox_items_upd
AFTER UPDATE OF name, description, sku, base_price, category_id, tax_code_id, is_active, is_weighed, age_restricted,
    net_quantity_value, net_quantity_unit ON items
WHEN (SELECT value FROM settings WHERE key = 'entitlement.catalog_master') IN ('cloud', 'adopting')
 AND COALESCE((SELECT TRIM(value) FROM settings WHERE key = 'sync.primary_url'), '') = ''
 AND NOT EXISTS (SELECT 1 FROM cloud_outbox_suppress)
BEGIN
  INSERT INTO cloud_outbox (entity, local_id, created_at)
  SELECT 'item', NEW.id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  WHERE NOT EXISTS (SELECT 1 FROM cloud_outbox WHERE entity = 'item' AND local_id = NEW.id AND sealed_at IS NULL);
END;

UPDATE cloud_links
   SET base = (SELECT json_set(cloud_links.base,
                      '$.is_weighed', json(CASE WHEN i.is_weighed THEN 'true' ELSE 'false' END),
                      '$.age_restricted', json(CASE WHEN i.age_restricted THEN 'true' ELSE 'false' END),
                      '$.net_quantity_value', i.net_quantity_value,
                      '$.net_quantity_unit', i.net_quantity_unit)
                 FROM items i WHERE i.id = cloud_links.local_id)
 WHERE entity = 'item'
   AND base IS NOT NULL
   AND json_valid(base)
   AND json_type(base, '$.is_weighed') IS NULL
   AND EXISTS (SELECT 1 FROM items i WHERE i.id = cloud_links.local_id);
