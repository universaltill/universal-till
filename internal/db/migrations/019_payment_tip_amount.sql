-- 019: tip_amount on payments (docs/germany-pos-parity-backlog.md, "Tips:
-- SumUp reader -> till auto-sync"). Card-terminal-driven tipping (SumUp
-- Solo reader prompts the customer, Cloud API returns the tip on the
-- transaction result) has nowhere to land today. tip_amount is metadata
-- on the payment row, NOT part of sale.total/subtotal/tax_total -- a tip
-- is gratuity riding on top of a card charge, not merchandise revenue,
-- and is often accounted for separately (payroll/tax). It intentionally
-- does not participate in CompleteSale's payment-coverage check.
ALTER TABLE payments ADD COLUMN tip_amount INTEGER NOT NULL DEFAULT 0;
