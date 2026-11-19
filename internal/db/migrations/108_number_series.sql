-- 108_number_series.sql -- ut-docs#6663: stored counters for the permanent
-- numbers (receipt, invoice) and the order-number pointer. Until now the next
-- receipt/invoice number was MAX(...)+1 over the LIVE table, so "Clear
-- transaction history" (rows move to *_archive) restarted the sequence and
-- repeated legal numbers. A row here is the last number issued in one series;
-- it is seeded lazily on first use from live + archive rows (never from
-- nothing), and is deliberately NOT in the reset table list.
--   kind:   receipt | invoice | order | counter_order
--   series: the till receipt prefix ('' on a legacy main till)
--   period: order pointer only -- the EOD boundary it was last reset for
CREATE TABLE IF NOT EXISTS number_series (
  kind   TEXT    NOT NULL,
  series TEXT    NOT NULL,
  last   INTEGER NOT NULL,
  period TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (kind, series)
);

-- Open-order skip lookup for the wrapping order number: the newest sale
-- holding a number, newest first.
CREATE INDEX IF NOT EXISTS idx_sales_display_no ON sales(display_no, created_at);
