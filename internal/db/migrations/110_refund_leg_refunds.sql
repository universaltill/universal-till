-- 110_refund_leg_refunds.sql — ut-docs#6649: what each completed refund sent
-- back to each ORIGINAL payment leg (the allocation core handed the payment
-- plugin and the plugin approved). Core nets these when it allocates the next
-- refund of the same sale, so a leg is never asked for more than it has left.
-- Till-local (like card_readers): written only after the provider approved and
-- the return was recorded; a sale refunded before this table existed simply has
-- no rows and falls back to netting the method's earlier refunds in payment
-- order. Append-only (ADR-0100); holds ids and minor-unit amounts only.
CREATE TABLE IF NOT EXISTS refund_leg_refunds (
  id                  TEXT PRIMARY KEY,
  original_sale_id    TEXT NOT NULL,
  original_payment_id TEXT NOT NULL,
  return_sale_id      TEXT NOT NULL,
  amount              INTEGER NOT NULL CHECK (amount > 0),
  created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_refund_leg_refunds_sale ON refund_leg_refunds (original_sale_id);
