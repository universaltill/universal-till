-- 050: card-present payment reconciliation fields (ut-docs#543).
--
-- A locally-attached card terminal (e.g. a future ZVT integration,
-- ut-docs#515) needs somewhere to record what it charged: masked PAN
-- (scheme + last 4 digits only -- never the full PAN), the terminal's
-- auth/approval code, its terminal ID, and the transaction's trace ID.
-- All four are optional, provider-agnostic reconciliation metadata on a
-- payment row -- same shape as 019_payment_tip_amount.sql's tip_amount:
-- NULL/empty for every existing payment method (cash, Stripe, SumUp,
-- QR-pay, demo), populated only by a future card-present integration.
ALTER TABLE payments ADD COLUMN masked_pan TEXT;
ALTER TABLE payments ADD COLUMN auth_code TEXT;
ALTER TABLE payments ADD COLUMN terminal_id TEXT;
ALTER TABLE payments ADD COLUMN trace_id TEXT;
