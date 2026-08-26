-- 073: default_locale on country_settings (ut-docs#1027).
--
-- The setup wizard prefilled currency/tax from the chosen country but never
-- derived store.locale — a German shop ran the compiled-in en-US default.
-- country_settings (migration 041) already IS this product's per-country
-- defaults table; this adds the one column it was missing rather than a
-- parallel hardcoded map that could drift from it.
--
-- BCP-47 tag, blank for "no opinion" (OTHER, and any operator-created
-- country nobody has set one for) — a blank value means "leave store.locale
-- alone", the same "don't touch what's there" contract currency/tax already
-- use when a caller field is empty. Setting an uninstalled locale here is
-- deliberately safe: I18n.T() (internal/config/i18n.go) already falls back
-- to "en" for any key missing from a not-yet-installed language-pack
-- overlay, so this can point at a language whose pack isn't installed yet
-- without breaking anything in the meantime.

ALTER TABLE country_settings ADD COLUMN default_locale TEXT NOT NULL DEFAULT '';

UPDATE country_settings SET default_locale = 'en-GB' WHERE code = 'GB';
UPDATE country_settings SET default_locale = 'fa-IR' WHERE code = 'IR';
UPDATE country_settings SET default_locale = 'en-US' WHERE code = 'US';
UPDATE country_settings SET default_locale = 'de-DE' WHERE code = 'DE';
UPDATE country_settings SET default_locale = 'fr-FR' WHERE code = 'FR';
UPDATE country_settings SET default_locale = 'es-ES' WHERE code = 'ES';
UPDATE country_settings SET default_locale = 'it-IT' WHERE code = 'IT';
UPDATE country_settings SET default_locale = 'nl-NL' WHERE code = 'NL';
UPDATE country_settings SET default_locale = 'tr-TR' WHERE code = 'TR';
UPDATE country_settings SET default_locale = 'ar-AE' WHERE code = 'AE';
UPDATE country_settings SET default_locale = 'ar-SA' WHERE code = 'SA';
UPDATE country_settings SET default_locale = 'en-IN' WHERE code = 'IN';
UPDATE country_settings SET default_locale = 'ur-PK' WHERE code = 'PK';
-- OTHER keeps '' — no country-specific opinion, matches its blank currency.
