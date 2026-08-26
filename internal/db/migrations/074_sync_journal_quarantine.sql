-- 074: LAN-sync journal poison-entry quarantine (ut-docs#1127, ADR-0065).
-- applyJournal (internal/pages/sync_sales.go) previously rejected the WHOLE
-- pushed batch on ANY error, and the replica's own push loop
-- (syncPushTick) never advances sync.push_cursor past a failed batch -- one
-- permanently-failing entry (an unknown voucher on a redemption replay, a
-- colliding operator-supplied voucher code on an issue replay) wedged that
-- replica's entire subsequent replication forever, with no operator-visible
-- signal beyond a generic rejected-push log line.
--
-- This table is the queryable, durable record of a quarantined entry: the
-- Problems-panel Warnf (logging.Recent(), same plumbing as
-- warnIfStockNegative/warnIfVoucherOverdrawn already use) is the immediate
-- operator signal, but that ring buffer is in-memory and capped at 50
-- entries (internal/logging.recentCap) -- unlike a force-applied Problem
-- (ADR-0036 point 3), a quarantined entry never writes a sales row at all,
-- so there is no other durable record of it anywhere once the log entry is
-- evicted or the till restarts. payload_json keeps the full journal entry
-- so a future manual-replay path (ADR-0065 "Not decided here") has
-- everything it needs without re-deriving it.
--
-- Deliberately NOT added to reset_archive_repo.go's resetArchiveTables:
-- like tills/sync.* settings, this is sync operational metadata, not shop
-- transaction history -- a quarantined entry never became a sale, so there
-- is no transaction to archive.
CREATE TABLE IF NOT EXISTS sync_journal_quarantine (
    id             TEXT PRIMARY KEY,
    till_id        TEXT NOT NULL,
    sale_id        TEXT NOT NULL,
    receipt_no     TEXT NOT NULL,
    reason         TEXT NOT NULL,
    payload_json   TEXT NOT NULL,
    quarantined_at TEXT NOT NULL,
    UNIQUE (sale_id)
);
CREATE INDEX IF NOT EXISTS idx_sync_journal_quarantine_till ON sync_journal_quarantine (till_id);
