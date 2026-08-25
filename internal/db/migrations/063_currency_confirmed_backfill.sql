-- 063: backfill store.currency_confirmed for installs that finished setup
-- before this key existed (ut-docs#970).
--
-- ut-docs#970 found that importing a catalogue prices every row in whatever
-- currency httpx.ActiveCurrency() reports — and a fresh till defaults to GBP
-- with nothing distinguishing "the operator chose GBP" from "nobody has ever
-- looked". internal/pages/common.KeyCurrencyConfirmed ("store.currency_
-- confirmed") is the new signal the import handler gates on before
-- committing; going forward it's set explicitly by the setup wizard, the
-- Settings currency picker, and the import confirmation prompt itself.
--
-- An install that already finished setup before this key existed has no row
-- for it yet, and would otherwise hit the new confirmation prompt on its
-- very next import even though it has been running correctly configured for
-- some time. setup.completed = 'true' is not a perfect proxy (the wizard's
-- currency field is optional, so a shop could in principle have completed
-- setup without ever touching currency) — but for an install that has
-- already been trading, re-asking would be a pure regression with no
-- practical benefit, and a wrong-currency install would have shown wrong
-- prices in the UI all along, not just on the next import. This is a one-time
-- compatibility backfill, not a substitute for the gate itself, which stays
-- in force for every install this doesn't apply to.
INSERT INTO settings (key, value)
SELECT 'store.currency_confirmed', 'true'
FROM settings
WHERE key = 'setup.completed' AND value = 'true'
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'store.currency_confirmed');
