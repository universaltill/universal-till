-- 091_stock_upload_outbox.sql — ADR-0169 §8, ut-docs#4763: the main till's
-- on-hand stock upload. The cloud keeps a read-only StockLevelMirror of the
-- till's stock (the till masters it, ADR-0036) so the allocation engine can
-- push `stock.set` to sales channels; this is the till's side of that.
--
-- stock_upload_outbox holds one mark per inventory row (item or variant, at a
-- location) whose quantity changed since its last accepted upload. Triggers
-- write the marks IN THE SAME TRANSACTION as the inventory write, so no stock
-- change (a sale, a receipt, a count, an import, a directive, the LAN sync's
-- corrections) can commit without one, and nothing on the sale path calls the
-- network. A re-mark replaces the row's earlier mark, so the table never holds
-- more rows than inventory does (no depth cap is needed), and each mark gets a
-- fresh `seq` from the table's AUTOINCREMENT: the till's monotonic movement
-- sequence. The cloud applies a row only when its seq is newer than the one it
-- holds, which makes a retried or duplicated upload change nothing.
--
-- The mark carries no quantity: the uploader reads the inventory row and the
-- mark in one statement, so a quantity and the seq it is sent under always
-- agree. A mark whose item or variant has no cloud link yet (cloud_links,
-- migration 082) waits; the mark is dropped only when the cloud answers it.
-- Marks are written on every till, replica or not; only the main till of a
-- cloud-master store ever sends them (internal/stockupload checks at send
-- time, so a replica promoted to main uploads its marks).
--
-- A deleted inventory row (an item deleted) leaves no mark: the cloud's item
-- is deleted by the catalogue sync, which takes the mirror row out of use.
--
-- stock_upload_state holds this till's stream id: a UUID that identifies the
-- sequence. If the till's sequence ever restarts below what the cloud has
-- (a restore from a backup), the uploader sees a `stale` answer whose stored
-- seq is ahead of its own, rotates the stream id and re-marks everything, and
-- the cloud replaces the rows of the new stream (it accepts a stream only if
-- its started_at is newer than the stored stream's).
--
-- Both tables are main-till bookkeeping, never LAN-synced
-- (sync_admin_repo.go's not-synced list). Quantities stay REAL here (the
-- existing inventory type); the upload converts to integer thousandths at the
-- DB boundary.
CREATE TABLE IF NOT EXISTS stock_upload_outbox (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id     TEXT,
    variant_id  TEXT,
    location_id TEXT NOT NULL,
    marked_at   TEXT NOT NULL,
    -- attempts / retry_after: a row the cloud answered `unknown_item` is kept
    -- (its item may reach the cloud's catalogue a moment later) and retried
    -- after a growing delay, then dropped. A re-mark (a new stock change)
    -- replaces the row, so both reset.
    attempts    INTEGER NOT NULL DEFAULT 0,
    retry_after TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_stock_upload_outbox_key
    ON stock_upload_outbox (COALESCE(item_id, ''), COALESCE(variant_id, ''), location_id);

CREATE TABLE IF NOT EXISTS stock_upload_state (
    one       INTEGER PRIMARY KEY CHECK (one = 1),
    stream_id TEXT NOT NULL,
    -- started_at: when this stream began (UTC, RFC 3339). The cloud orders
    -- streams by it: a request of an older stream never overwrites a newer
    -- one's rows (a delayed pre-rotation request cannot flip the mirror back).
    started_at TEXT NOT NULL,
    -- rotated_at: when this till last rotated its stream ('' = never). Rotations
    -- are rate-limited (internal/stockupload) so a cloud that keeps answering
    -- `older_stream` (a cloned disk, a clock that jumped) cannot make the till
    -- re-send everything over and over.
    rotated_at TEXT NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO stock_upload_state (one, stream_id, started_at)
SELECT 1, lower(substr(h, 1, 8) || '-' || substr(h, 9, 4) || '-4' || substr(h, 14, 3) || '-a' || substr(h, 18, 3) || '-' || substr(h, 21, 12)),
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM (SELECT hex(randomblob(16)) AS h);

-- Every existing inventory row is owed to the cloud once.
INSERT OR IGNORE INTO stock_upload_outbox (item_id, variant_id, location_id, marked_at)
SELECT item_id, variant_id, location_id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now') FROM inventory;

CREATE TRIGGER IF NOT EXISTS trg_stock_upload_inventory_ins AFTER INSERT ON inventory
BEGIN
  DELETE FROM stock_upload_outbox
   WHERE COALESCE(item_id, '') = COALESCE(NEW.item_id, '')
     AND COALESCE(variant_id, '') = COALESCE(NEW.variant_id, '')
     AND location_id = NEW.location_id;
  INSERT INTO stock_upload_outbox (item_id, variant_id, location_id, marked_at)
  VALUES (NEW.item_id, NEW.variant_id, NEW.location_id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
END;

CREATE TRIGGER IF NOT EXISTS trg_stock_upload_inventory_upd
AFTER UPDATE OF quantity ON inventory
WHEN OLD.quantity IS NOT NEW.quantity
BEGIN
  DELETE FROM stock_upload_outbox
   WHERE COALESCE(item_id, '') = COALESCE(NEW.item_id, '')
     AND COALESCE(variant_id, '') = COALESCE(NEW.variant_id, '')
     AND location_id = NEW.location_id;
  INSERT INTO stock_upload_outbox (item_id, variant_id, location_id, marked_at)
  VALUES (NEW.item_id, NEW.variant_id, NEW.location_id, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
END;
