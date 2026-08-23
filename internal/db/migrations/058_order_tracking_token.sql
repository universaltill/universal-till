-- Customer order tracking via QR (universaltill/ut-docs#527), building on
-- #526's order-status backbone (033_order_status.sql).
--
-- sales.tracking_token is an unguessable capability: 16 bytes of crypto/rand,
-- hex-encoded, minted lazily by POSRepo.EnsureOrderTrackingToken when the
-- self-order kiosk's confirmation screen renders its tracking QR. NULL (the
-- default) means no QR was ever issued for the sale — every existing row and
-- every cashier-lane sale stays untouched. The partial unique index enforces
-- one-token-one-sale without forcing a value onto rows that never get one.
--
-- sales_archive gets the column too, in THIS migration (independent review):
-- 040_reset_archive.sql's header states archive tables are column-identical
-- to their live counterparts "plus every later ALTER", and 056_sale_table_id
-- set the precedent of mirroring in the same migration rather than leaving a
-- follow-up (055 was the reviewer catch that established the rule). Without
-- it, ResetTransactionHistory's explicit column list silently drops the token
-- on "Clear transaction history", so a restored batch's tracking QRs are dead
-- — an ADR-0042 "destroys nothing" violation. No unique index on the archive:
-- 040's second stated relaxation is no PRIMARY KEY / UNIQUE constraints, so a
-- shop that resets, trades and resets again can archive across batches.
ALTER TABLE sales ADD COLUMN tracking_token TEXT;
CREATE UNIQUE INDEX idx_sales_tracking_token ON sales(tracking_token) WHERE tracking_token IS NOT NULL;
ALTER TABLE sales_archive ADD COLUMN tracking_token TEXT;
