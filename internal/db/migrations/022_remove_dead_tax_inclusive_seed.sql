-- ut-docs#12: 001_init.sql seeded pos.tax_inclusive, but nothing ever read
-- that key — the save and load paths (and pages/common's KeyTaxInclusive)
-- all use store.tax_inclusive. 001 is released and append-only, so the dead
-- row is removed here instead of edited out of the seed.
DELETE FROM settings WHERE key = 'pos.tax_inclusive';
