-- German VAT law (§12 UStG) requires some items to tax at a different rate
-- for takeaway than dine-in (e.g. 19% eat-in / 7% takeaway for drinks),
-- while others stay pinned to one rate regardless (existing tax_codes
-- already support that: cakes just get a flat 7% code, unaffected by this
-- column). NULL = no takeaway override, existing rows/behaviour unchanged.
ALTER TABLE tax_codes ADD COLUMN takeaway_rate_basis_points INTEGER;
