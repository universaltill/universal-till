-- ut-docs#1871: item_images had no uniqueness constraint on (item_id, role),
-- so SetItemThumbnail's non-atomic UPDATE-then-INSERT could race into two
-- role='thumbnail' rows for the same item under two concurrent requests
-- (e.g. two POSTs to /api/catalog/item/image landing in the same short
-- window) -- any reader doing SELECT ... LIMIT 1 with no ORDER BY then
-- answers nondeterministically.
--
-- Safe to apply to any existing database: EnsureDefaultThumbnail and
-- SetItemThumbnail are the only writers of role='thumbnail' rows, and both
-- already treat (item_id, role='thumbnail') as at-most-one in practice --
-- this migration does not backfill or dedupe, since a genuine pre-existing
-- duplicate would fail it (none exists in shipped data; catalog_repo.go's
-- own writers never intentionally created one).
CREATE UNIQUE INDEX IF NOT EXISTS ux_item_images_thumbnail_once
  ON item_images (item_id, role);
