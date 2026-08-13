-- 041: per-country settings (ut-docs#635 → ut-docs#659).
--
-- Moves the compile-time `setupCountries` slice (internal/pages/setup_page.go)
-- into data an admin can manage, and adds the `archive_min_days` retention
-- field the product owner asked for ("we need to have setting for each country
-- like currency, archive min time etc", ut-docs#635).
--
-- This is per-JURISDICTION default data, deliberately separate from the
-- `settings` table: `settings` holds THIS shop's own choices (including which
-- country it is in, via common.KeyCountry), one row per key. A shop points at
-- a country_settings row; it does not own it.
--
-- The currency/tax values below are copied verbatim from setupCountries so the
-- move is behaviour-preserving — the wizard must prefill exactly what it
-- prefilled before. Tax is stored in BASIS POINTS (repo rule: basis-point
-- rates stay int64); the wizard's percent UI converts at the boundary.
--
-- RETENTION, deliberately uniform (ADR-0040):
--   ADR-0040 applies 10 years as a SINGLE GLOBAL FLOOR and explicitly states it
--   "does not encode per-jurisdiction retention minimums (they differ, e.g. DE
--   vs UK)", and that no jurisdiction-specific compliance claim is made in code
--   or UI. So every row seeds at the same 3650 days. Per-country retention
--   VALUES need an ADR-0040 amendment plus the product owner's/legal's
--   sign-off — the column exists so that becomes data entry, not a rebuild.
--   Keep this in sync with data.GlobalArchiveMinDays (a test asserts it).
--
-- name_key is the locale key the wizard already renders these by
-- (setup.country.*), carried over so the wizard can drop its own list entirely
-- (ut-docs#660) without hardcoding names anywhere.
--
-- currency_symbol is seeded only where the symbol is unambiguous; blank means
-- "fall back to the currency code", which is the existing behaviour when no
-- symbol is configured. It is display-only and carries no fiscal meaning.

CREATE TABLE IF NOT EXISTS country_settings (
    code             TEXT PRIMARY KEY,
    name_key         TEXT NOT NULL,
    currency         TEXT NOT NULL DEFAULT '',
    currency_symbol  TEXT NOT NULL DEFAULT '',
    tax_rate_bp      INTEGER NOT NULL DEFAULT 0 CHECK (tax_rate_bp >= 0),
    tax_inclusive    INTEGER NOT NULL DEFAULT 1 CHECK (tax_inclusive IN (0,1)),
    archive_min_days INTEGER NOT NULL CHECK (archive_min_days >= 0),
    is_builtin       INTEGER NOT NULL DEFAULT 0 CHECK (is_builtin IN (0,1)),
    updated_at       TEXT NOT NULL
);

INSERT OR IGNORE INTO country_settings
    (code, name_key, currency, currency_symbol, tax_rate_bp, tax_inclusive, archive_min_days, is_builtin, updated_at)
VALUES
    ('GB',    'setup.country.gb',    'GBP', '£', 2000, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('IR',    'setup.country.ir',    'IRT', '',  1000, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('US',    'setup.country.us',    'USD', '$',    0, 0, 3650, 1, '1970-01-01T00:00:00Z'),
    ('DE',    'setup.country.de',    'EUR', '€', 1900, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('FR',    'setup.country.fr',    'EUR', '€', 2000, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('ES',    'setup.country.es',    'EUR', '€', 2100, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('IT',    'setup.country.it',    'EUR', '€', 2200, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('NL',    'setup.country.nl',    'EUR', '€', 2100, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('TR',    'setup.country.tr',    'TRY', '₺', 2000, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('AE',    'setup.country.ae',    'AED', '',   500, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('SA',    'setup.country.sa',    'SAR', '',  1500, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('IN',    'setup.country.in',    'INR', '₹', 1800, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('PK',    'setup.country.pk',    'PKR', '',  1800, 1, 3650, 1, '1970-01-01T00:00:00Z'),
    ('OTHER', 'setup.country.other', '',    '',     0, 1, 3650, 1, '1970-01-01T00:00:00Z');
