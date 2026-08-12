-- Kitchen/receipt print outcome per sale (ut-docs#517a): NULL = last known
-- attempt (if any) succeeded, or no attempt has been made — every existing
-- row and every shop that never triggers a failure is unaffected. A
-- non-null value is the RFC3339 timestamp of the most recent FAILED
-- attempt; cleared back to NULL the next time that print path succeeds — the
-- post-tender auto-print, or a manual reprint (POST /api/print/kitchen, POST
-- /api/print/receipt/{receiptNo}, the Journal's reprint button). The existing
-- audit_log 'kitchen_print_failed'/'print_failed' rows already cover the
-- historical trail; this column only answers "is the CURRENT state
-- failed," read on every /ui/orders poll.
ALTER TABLE sales ADD COLUMN kitchen_print_failed_at TEXT;
ALTER TABLE sales ADD COLUMN receipt_print_failed_at TEXT;
