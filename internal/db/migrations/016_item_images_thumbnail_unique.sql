-- ut-docs#1871: item_images had no uniqueness constraint on (item_id, role),
-- so SetItemThumbnail's non-atomic UPDATE-then-INSERT could race into two
-- role='thumbnail' rows for the same item under two concurrent requests
-- (e.g. two POSTs to /api/catalog/item/image landing in the same short
-- window) -- any reader doing SELECT ... LIMIT 1 with no ORDER BY then
-- answers nondeterministically.
--
-- Dedup first (independent review finding): CREATE UNIQUE INDEX IF NOT
-- EXISTS only guards against the index itself already existing, not
-- against pre-existing data that violates it -- and this migration exists
-- specifically because the race it closes may already have produced
-- duplicate rows on a live till. An unguarded CREATE UNIQUE INDEX against
-- such a database would fail this migration outright, dropping the till
-- into read-only safe mode (ADR-0075) with no self-service repair. Keep
-- the highest-rowid (most recently written -- i.e. the newest photo, the
-- one a real operator upload would have produced last) row per
-- (item_id, role) and delete the rest before the index is created.
DELETE FROM item_images
WHERE rowid NOT IN (
  SELECT MAX(rowid) FROM item_images GROUP BY item_id, role
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_item_images_thumbnail_once
  ON item_images (item_id, role);
