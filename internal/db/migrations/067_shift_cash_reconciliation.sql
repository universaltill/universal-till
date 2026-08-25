-- 067: cash-drawer reconciliation at shift close (ut-docs#1006, German
-- pilot). Two additive, nullable columns on shifts:
--   new_float      — the drawer's carry-forward float after close: counted
--                    closing_cash minus any skim-to-safe recorded with the
--                    close. NULL on shifts closed before this feature (or by
--                    older code); readers fall back to closing_cash then.
--   count_protocol — optional denomination-count JSON blob recorded at
--                    close, a flat object keyed by denomination in minor
--                    units mapping to piece count, e.g.
--                    {"5000":2,"100":13}. NULL when no count was recorded.
-- Append-only: 001-066 untouched. No TSE/fiscal gating is added for the
-- skim adjustment itself — whether cash adjustments get the ADR-0048
-- fiscal hard-gate is ut-docs#998's open question, not decided here.
ALTER TABLE shifts ADD COLUMN new_float INTEGER;
ALTER TABLE shifts ADD COLUMN count_protocol TEXT;

-- The reset-archive twin (040, ADR-0042) copies shifts rows column-by-
-- column on a go-live reset and restores them the same way — without the
-- same two columns there, a reset would silently drop the recorded new
-- float / count protocol and a restore would bring shifts back incomplete.
ALTER TABLE shifts_archive ADD COLUMN new_float INTEGER;
ALTER TABLE shifts_archive ADD COLUMN count_protocol TEXT;
