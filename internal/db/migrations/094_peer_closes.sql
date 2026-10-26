-- 094_peer_closes.sql — ut-docs#5074 M7 (design §3.2, D9): the additional
-- tills' own day closes, held by the main till so it can relay every close
-- of the shop to every additional till's read-only copy, and a promoted till
-- keeps them all.
--
-- peer_closes is NOT report_archive: the main till's own Z chain, numbers
-- and reports never read it. Only internal/data/sales_mirror_repo.go touches
-- it (scripts/ci/guard-sales-mirror-isolation.sh). An additional till pushes
-- its closes here only while the shop setting sales_mirror.enabled is on
-- (POST /api/sync/closes), so on every till today the table stays empty.
--
-- The change-log trigger follows 093's gate exactly: it writes nothing unless
-- the setting is on, this till is the main till, and its own kill switch is
-- off. A close is immutable once archived, so only inserts are logged.
--
-- Safe on a database that already has data: new table and trigger only.

CREATE TABLE IF NOT EXISTS peer_closes (
    till_id        TEXT NOT NULL,
    id             TEXT NOT NULL,
    kind           TEXT NOT NULL,
    period         TEXT NOT NULL,
    z_number       INTEGER,
    prev_z_number  INTEGER,
    prev_closed_at TEXT,
    first_receipt  TEXT,
    last_receipt   TEXT,
    content_json   TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    received_at    TEXT NOT NULL,
    PRIMARY KEY (till_id, kind, period)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_peer_closes_till_id ON peer_closes (till_id, id);
CREATE INDEX IF NOT EXISTS idx_peer_closes_created ON peer_closes (created_at);

CREATE TRIGGER IF NOT EXISTS trg_sales_mirror_log_peer_close_ins AFTER INSERT ON peer_closes
WHEN (SELECT value FROM settings WHERE key = 'sales_mirror.enabled') = '1'
 AND TRIM(COALESCE((SELECT value FROM settings WHERE key = 'sync.primary_url'), '')) = ''
 AND COALESCE((SELECT value FROM settings WHERE key = 'sync.sales_mirror_off'), '0') <> '1'
BEGIN
  INSERT INTO sales_mirror_log (op, ref_id, origin_till_id) VALUES ('close', NEW.id, NEW.till_id);
END;
