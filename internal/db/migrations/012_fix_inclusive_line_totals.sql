-- Repair sale lines written by the pre-fix engine under tax-INCLUSIVE
-- pricing: total_after_tax had the line's tax added ON TOP of the
-- already-inclusive price (and total_before_tax held the gross).
-- Inclusive sales are recognisable by their header math:
--   total = subtotal - discount_total   (exclusive adds tax_total on top)
-- Correct values: after-tax = the gross line amount (old before-tax),
-- before-tax = gross - tax. Zero-tax rows are unaffected (no-op update).
UPDATE sale_lines
SET total_after_tax  = total_before_tax,
    total_before_tax = total_before_tax - tax_amount
WHERE sale_id IN (
    SELECT id FROM sales
    WHERE total = subtotal - discount_total AND tax_total > 0
);
