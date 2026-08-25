-- Sequential, chained Z-number for report_archive rows (ut-docs#1080,
-- split from ut-docs#1009). Additive only -- does not change
-- report_archive's existing (kind, period) write-once identity or its
-- period-keying semantics (calendar-date, ADR-0057), which stay exactly
-- as-is. z_number/prev_z_number/prev_closed_at chain each close to its
-- predecessor. first_receipt/last_receipt promote EODReport's existing
-- (but JSON-buried) receipt range to queryable columns for a future
-- accounting-export consumer (ut-docs#1036) without parsing content_json.
ALTER TABLE report_archive ADD COLUMN z_number INTEGER;
ALTER TABLE report_archive ADD COLUMN prev_z_number INTEGER;
ALTER TABLE report_archive ADD COLUMN prev_closed_at TEXT;
ALTER TABLE report_archive ADD COLUMN first_receipt TEXT;
ALTER TABLE report_archive ADD COLUMN last_receipt TEXT;

-- Scoped per `kind` (this till has one EOD sequence globally -- no
-- shop/till column exists on report_archive; same partial-unique-index
-- pattern as idx_sales_tracking_token in migration 058). Partial (WHERE
-- z_number IS NOT NULL) so pre-migration legacy rows, which get NULL,
-- never collide with each other or with real numbers.
CREATE UNIQUE INDEX IF NOT EXISTS ux_report_archive_kind_znumber
    ON report_archive (kind, z_number) WHERE z_number IS NOT NULL;
