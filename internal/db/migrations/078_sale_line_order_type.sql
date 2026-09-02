-- ut-docs#1181 / ADR-0073: order type becomes authoritative on each sale
-- LINE (one sale may mix dine-in and takeaway lines, each taxed for its own
-- mode); sales.order_type becomes a derived summary ('' | 'takeaway' |
-- 'mixed'). Additive, append-only.
--
-- Backfill: every historic sale is uniform by construction (the old model
-- had one order type per sale), so each existing line inherits its sale
-- header's value. Archived lines are backfilled from sales_archive, joined
-- on BOTH sale id and reset_batch_id — an id can legitimately recur across
-- batches (ADR-0042 archives never rewrite ids), so sale id alone is not a
-- key there. Without this backfill an upgraded till's entire takeaway
-- history would read as dine-in line by line.
ALTER TABLE sale_lines ADD COLUMN order_type TEXT NOT NULL DEFAULT '';
ALTER TABLE sale_lines_archive ADD COLUMN order_type TEXT NOT NULL DEFAULT '';

UPDATE sale_lines
SET order_type = 'takeaway'
WHERE order_type = ''
  AND sale_id IN (SELECT id FROM sales WHERE order_type = 'takeaway');

-- Historic RETURNS never carried a header (the pre-ADR-0073 refund path
-- left sales.order_type = '' on a return), so backfill their lines from the
-- ORIGINAL sale via sale_links, and derive the return's own header the way
-- CompleteSale now does. Without this, the refund pool — keyed per mode
-- from now on — would never see that a takeaway unit was already returned,
-- and a fully-refunded historic takeaway sale could be refunded again.
UPDATE sale_lines
SET order_type = 'takeaway'
WHERE order_type = ''
  AND sale_id IN (
    SELECT k.sale_id FROM sale_links k
    JOIN sales o ON o.id = k.original_sale_id
    JOIN sales r ON r.id = k.sale_id
    WHERE o.order_type = 'takeaway' AND r.sale_type = 'return' AND r.order_type = ''
  );
UPDATE sales
SET order_type = 'takeaway'
WHERE sale_type = 'return' AND order_type = ''
  AND id IN (
    SELECT k.sale_id FROM sale_links k
    JOIN sales o ON o.id = k.original_sale_id
    WHERE o.order_type = 'takeaway'
  );

UPDATE sale_lines_archive
SET order_type = 'takeaway'
WHERE order_type = ''
  AND EXISTS (
    SELECT 1 FROM sales_archive sa
    WHERE sa.id = sale_lines_archive.sale_id
      AND sa.reset_batch_id = sale_lines_archive.reset_batch_id
      AND sa.order_type = 'takeaway'
  );
-- Same return-through-original rule for the archive, batch-scoped.
UPDATE sale_lines_archive
SET order_type = 'takeaway'
WHERE order_type = ''
  AND EXISTS (
    SELECT 1 FROM sale_links_archive k
    JOIN sales_archive o ON o.id = k.original_sale_id AND o.reset_batch_id = k.reset_batch_id
    JOIN sales_archive r ON r.id = k.sale_id AND r.reset_batch_id = k.reset_batch_id
    WHERE k.sale_id = sale_lines_archive.sale_id
      AND k.reset_batch_id = sale_lines_archive.reset_batch_id
      AND o.order_type = 'takeaway' AND r.sale_type = 'return' AND r.order_type = ''
  );
UPDATE sales_archive
SET order_type = 'takeaway'
WHERE sale_type = 'return' AND order_type = ''
  AND EXISTS (
    SELECT 1 FROM sale_links_archive k
    JOIN sales_archive o ON o.id = k.original_sale_id AND o.reset_batch_id = k.reset_batch_id
    WHERE k.sale_id = sales_archive.id AND k.reset_batch_id = sales_archive.reset_batch_id
      AND o.order_type = 'takeaway'
  );
