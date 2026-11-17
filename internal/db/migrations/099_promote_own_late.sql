-- 099_promote_own_late.sql — ut-docs#5777 (ADR-0066 amendment rule 1): a
-- till's OWN sale that the old main till applied only after that till's last
-- close reached no Z once this till was promoted. The old main till's
-- recording time for it arrives in the sales copy's feed but was thrown away
-- (own sales are never stored in mirror_sales), so it is kept here, and
-- stamped onto the own row at promote.
--
-- sales.recorded_at: NULL for every row but a promote's own-late sale (and
-- every row stored before 099, which has no recorded time). The Z windows on
-- COALESCE(recorded_at, completed_at, created_at) (saleArrivalCol); the row's
-- created_at / completed_at / local_date stay untouched (rule 4). No backfill:
-- an old own sale's arrival time is unknown.
ALTER TABLE sales ADD COLUMN recorded_at TEXT;
ALTER TABLE sales_archive ADD COLUMN recorded_at TEXT;

-- The previous main till's apply time of a sale this till made, kept by the
-- copy's apply. Own sales stay out of mirror_sales (they live in sales), so
-- this is one small row per own sale inside the copy's window, pruned with it.
-- Local-only, like the mirror_* tables: never synced, never in an admin bundle.
CREATE TABLE IF NOT EXISTS mirror_sale_arrivals (
  id           TEXT PRIMARY KEY,
  completed_at TEXT NOT NULL,
  local_date   TEXT NOT NULL DEFAULT ''
);

-- The window index of 098 follows the new arrival expression exactly (an
-- expression index is only used when the query expression matches).
DROP INDEX IF EXISTS idx_sales_status_arrival_dt;
CREATE INDEX IF NOT EXISTS idx_sales_status_recorded_dt
  ON sales (status, datetime(COALESCE(recorded_at, completed_at, created_at)));
