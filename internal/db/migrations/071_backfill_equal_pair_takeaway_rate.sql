-- 071: backfill (ut-docs#1013 second review round). A tax code hand-created
-- via the tax-code management UI BEFORE this card's fix to parseTaxCodeForm
-- could hold an explicit takeaway_rate_basis_points equal to its own
-- rate_basis_points (a hand-typed no-op, e.g. 7%/7% for food) -- every code
-- created after the fix, and every code the CSV-import path has ever
-- created (ut-docs#536), instead stores NULL for that case. The mismatch is
-- not cosmetic: FindOrCreateTaxCode's (rate, takeaway) lookup can't match a
-- legacy hand-created code with an explicit equal pair against a later
-- import's canonicalized (rate, NULL) query, so re-importing or migrating
-- that shop's own catalog silently creates a duplicate tax code and
-- abandons the merchant's original one -- the exact bug the fix closed for
-- new writes only.
--
-- Safe to backfill unconditionally: takeaway_rate_basis_points has no
-- effect on any charged rate (that comes from pos.effectiveTaxRateBP via
-- the installed TaxRateAsker plugin, keyed on the line's tax_code_id and
-- tax code's rate_basis_points/takeaway_rate_basis_points pairing at
-- lookup time, never compared for equality elsewhere) -- it is consulted
-- only for display, the FindOrCreateTaxCode grouping key itself, CSV
-- export, and a plugin-settings placeholder suggestion. Collapsing an
-- explicit equal pair to NULL changes none of those in any way a merchant
-- would notice, and matches what re-saving the same row through the
-- (now-fixed) edit form would already do.
UPDATE tax_codes
SET takeaway_rate_basis_points = NULL
WHERE takeaway_rate_basis_points = rate_basis_points;
