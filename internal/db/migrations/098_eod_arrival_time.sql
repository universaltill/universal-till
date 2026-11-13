-- 098_eod_arrival_time.sql — ut-docs#5769 (ADR-0066 amendment): when the
-- main till recorded a no-sale open / cleared basket journaled from another
-- till. NULL = this till's own row, or a row stored before 098: both count by
-- created_at (the Z reads COALESCE(received_at, created_at)). No backfill —
-- an old row's arrival time is unknown.
ALTER TABLE no_sale_events ADD COLUMN received_at TEXT;
ALTER TABLE no_sale_events_archive ADD COLUMN received_at TEXT;
ALTER TABLE cleared_baskets ADD COLUMN received_at TEXT;
ALTER TABLE cleared_baskets_archive ADD COLUMN received_at TEXT;

-- The Z windows sales on datetime(COALESCE(completed_at, created_at)) (the
-- arrival time, saleArrivalCol); without an expression index every window
-- read scans all sales while holding the write lock. Must match the query
-- expression exactly. no_sale_events / cleared_baskets are drained into the
-- archive by each Z, so they stay small and need none.
CREATE INDEX IF NOT EXISTS idx_sales_status_arrival_dt
  ON sales (status, datetime(COALESCE(completed_at, created_at)));
