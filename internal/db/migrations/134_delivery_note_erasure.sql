-- 124_delivery_note_erasure.sql — ADR-0215 Decision 9, legal review
-- 2026-11-28 R1..R9 (ut-docs#6988): the till erases the customer's note on a
-- delivery order itself, on its own clock, with no cloud call.
--
-- terminal_at: when the order first reached a terminal state (collected,
--   rejected, cancelled, refunded, expired, failed, or rejected by the till at
--   intake). Written once and never moved, so a later refund fact does not
--   restart the retention clock. '' while the order is still live.
-- note_present / note_chars: audit facts that survive erasure, length only.
--   Never the text and never a hash of it (R9).
-- note_viewed_at: first time staff opened the note (R9).
-- note_erased_at / note_erased_by: when and why the order note and every line
--   note were blanked: job_cap | job_backstop | redact | cloud_end (R9).
-- The accepting staff member and time are the order's `accept` row in
-- channel_order_actions; they follow the order's own retention.
ALTER TABLE channel_orders ADD COLUMN terminal_at TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN note_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channel_orders ADD COLUMN note_chars INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channel_orders ADD COLUMN note_viewed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN note_erased_at TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_orders ADD COLUMN note_erased_by TEXT NOT NULL DEFAULT '';

UPDATE channel_orders SET note_present = 1, note_chars = length(note) WHERE note <> '';
UPDATE channel_orders
   SET terminal_at = CASE WHEN status_changed_at <> '' THEN status_changed_at ELSE updated_at END
 WHERE order_kind = 'delivery' AND (reject_reason <> '' OR status IN ('collected','rejected','cancelled','refunded','expired','failed'));
