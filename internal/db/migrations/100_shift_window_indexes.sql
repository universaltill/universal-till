-- 100_shift_window_indexes.sql — ut-docs#5796: the shift window (cash
-- takings at CloseShift, the X report) filters one register's rows by
-- datetime(completed_at) / datetime(created_at) (shiftWindow, pos_repo.go).
-- Before this, those statements planned on (status) alone and walked every
-- completed sale the till ever had. The index expressions must match the
-- query text exactly (an expression index is only used on an exact match).
CREATE INDEX IF NOT EXISTS idx_sales_register_completed_dt
  ON sales (register_id, datetime(completed_at));
CREATE INDEX IF NOT EXISTS idx_cleared_baskets_register_created_dt
  ON cleared_baskets (register_id, datetime(created_at));
