-- 119_item_image_gallery.sql — ut-docs#7101, ADR-0218 (image gallery).
-- item_images was one thumbnail per item (016: unique (item_id, role)). A gallery
-- needs several rows per item, so the all-roles unique index becomes three
-- partial ones: still exactly ONE thumbnail per item (the race 016 closed stays
-- closed; every reader keeps `role='thumbnail' LIMIT 1` and sees the primary),
-- one gallery row per position, one variant-gallery row per position.
-- No table rebuild is needed (an index change plus two additive columns), so
-- existing rows, the 042/047 sell-screen version triggers and foreign keys are
-- untouched and no data can be lost. role 'gallery' = item images at position
-- 2.., 'variant' = a variant's images (variant_id set), position 1 = its primary.
-- alt is the picture's alt text (empty = none).
DROP INDEX IF EXISTS ux_item_images_thumbnail_once;

CREATE UNIQUE INDEX IF NOT EXISTS ux_item_images_thumbnail_once
  ON item_images (item_id) WHERE role = 'thumbnail';

ALTER TABLE item_images ADD COLUMN alt TEXT NOT NULL DEFAULT '';
ALTER TABLE item_images ADD COLUMN variant_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS ux_item_images_gallery_pos
  ON item_images (item_id, sort_order) WHERE role = 'gallery';

CREATE UNIQUE INDEX IF NOT EXISTS ux_item_images_variant_pos
  ON item_images (item_id, variant_id, sort_order) WHERE role = 'variant';
