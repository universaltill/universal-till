-- 051_builtin_country_pt.sql — ut-docs#2963 (Portugal track, #2578).
-- Adds PT to the builtin country_settings rows: EUR, the 23% mainland IVA
-- default, VAT-inclusive pricing and the pt-PT locale, at ADR-0040's global
-- archive floor. Mirrors data.builtinCountryDefaults, which
-- TestBuiltinDefaultsMatchMigrationSeed pins to this row.
--
-- Additive rather than an edit to 001_init.sql's seed: 001 is frozen
-- (ADR-0100) and verifyAppliedMigrations hard-fails any checksum drift.
--
-- The mainland rate is only a wizard prefill. Azores and Madeira rates and
-- exemption reasons are ut-docs#2961; nothing here enables or claims fiscal
-- compliance for Portugal (ADR-0040) and PT has no fiscal hard gate yet
-- (ut-docs#2958).
--
-- updated_at is the seed's epoch, like every other builtin row, so the row
-- never outranks an operator's own edit in admin sync.
--
-- A till whose operator already created a custom PT country keeps every
-- value they chose. The row is only promoted to builtin, which is what
-- CountrySettingsRepo.Upsert would do on its next save anyway (Delete then
-- restores the shipped defaults instead of removing the row), and a blank
-- name_key gains the locale key, as validateCountrySetting would fill it.
-- Re-running is a no-op.
INSERT INTO country_settings
    (code, name_key, currency, currency_symbol, tax_rate_bp, tax_inclusive, archive_min_days, is_builtin, updated_at, default_locale)
VALUES
    ('PT', 'setup.country.pt', 'EUR', '€', 2300, 1, 3650, 1, '1970-01-01T00:00:00Z', 'pt-PT')
ON CONFLICT(code) DO UPDATE SET
    is_builtin = 1,
    name_key   = CASE WHEN country_settings.name_key = '' THEN excluded.name_key ELSE country_settings.name_key END;
