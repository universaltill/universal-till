-- 054_shadow_customer_documents.sql — ADR-0124, ut-docs#3169.
-- A per-market data property: whether a till that is NOT the shop's
-- system of record (a shadow pilot) may produce documents the customer
-- sees or receives (receipts, reprints, the post-tender receipt view).
-- 'forbidden' is for markets that require certified invoicing software, where
-- the shop's existing certified system must be the only source of such
-- documents. Core enforces it in generic code (fiscal.CustomerDocuments);
-- the market fact lives only here and in data.builtinCountryDefaults,
-- which TestBuiltinDefaultsMatchMigrationSeed pins to this file.
--
-- Not operator-editable: CountrySettingsRepo.Upsert never takes it from a
-- caller, Delete restores a builtin row's value, admin sync ratchets it,
-- and the compiled builtin value is a read-time floor (a missing or
-- re-defaulted row cannot make a forbidden market allowed).
--
-- Additive: 001_init.sql and 051_builtin_country_pt.sql stay frozen
-- (ADR-0100). Describes behaviour only; claims no compliance (ADR-0040).
ALTER TABLE country_settings ADD COLUMN shadow_customer_documents TEXT NOT NULL DEFAULT 'allowed'
    CHECK (shadow_customer_documents IN ('allowed', 'forbidden'));

UPDATE country_settings SET shadow_customer_documents = 'forbidden' WHERE code = 'PT';
