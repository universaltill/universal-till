-- 058_sale_lines_item_indexes.sql — ut-docs#3317 (my.: delete an item that
-- was never sold). The catalog snapshot now reports ever_sold per item
-- (POSRepo.EverSoldItemIDs) and the delete_item directive counts an item's
-- sales (POSRepo.DeleteUnusedItem). Both look sale lines up by item and by
-- variant; without these indexes every lookup scans the whole sales
-- history, on every check-in. Indexes only: no row changes, safe to replay.
CREATE INDEX IF NOT EXISTS idx_sale_lines_item ON sale_lines (item_id);
CREATE INDEX IF NOT EXISTS idx_sale_lines_variant ON sale_lines (variant_id);
CREATE INDEX IF NOT EXISTS idx_sale_lines_archive_item ON sale_lines_archive (item_id);
CREATE INDEX IF NOT EXISTS idx_sale_lines_archive_variant ON sale_lines_archive (variant_id);
