-- 038_sales_aggregate_uploads.sql — universaltill/ut-docs#2535 (ADR-0111,
-- ADR-0111): the till-side upload ledger for the cloud sales-aggregate
-- endpoint (POST /v1/stores/sales-aggregates).
--
-- One row per (business_date, till_id) rollup the cloud has ACCEPTED
-- (HTTP 200): content_hash is the sha256 of the exact JSON body that was
-- sent, so internal/cloudsync re-sends a day only when its rollup changed
-- (a late refund, a void, a replica's journaled sale) and never re-sends an
-- unchanged day. A row is written only after a 200 — a 402
-- (subscription_inactive), a 5xx or a dead network leaves the previous hash
-- (or no row) in place, so the next tick retries. This is the offline
-- queue: the sales rows themselves are the source, this table only records
-- what the cloud already holds.
--
-- business_date is sales.local_date (YYYY-MM-DD, the same calendar-day
-- semantics as the Z-report's EndOfDay); till_id is the resolved rollup key
-- (sales.till_id, else register_id, else this till's own identity).
-- uploaded_at is RFC3339 UTC. Rows older than the 14-day lookback are
-- pruned by the uploader. Nothing here is money or personal data.
CREATE TABLE IF NOT EXISTS sales_aggregate_uploads (
    business_date TEXT NOT NULL,
    till_id       TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    uploaded_at   TEXT NOT NULL,
    PRIMARY KEY (business_date, till_id)
);
