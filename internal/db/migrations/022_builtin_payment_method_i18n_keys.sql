-- 022_builtin_payment_method_i18n_keys.sql — ut-docs#2021 (built-in payment
-- method names stay untranslated on a non-English till).
--
-- ut-docs#2015 made PaymentMethod.Name resolve through T at render time
-- (web/ui/pages/index.html, web/ui/partials/self_order_payment_picker.html)
-- so a PLUGIN's payment entry label can be translated via its own locales/
-- overlay. That's a no-op for the three built-ins seeded by 001_init.sql:
-- their name column holds literal plain text ("Cash", "Card", "Gift
-- Card"), and T passes plain text through unchanged by design — confirmed
-- live, the Arabic sell-screen screenshot still shows "Cash" in English.
--
-- Fix: point the built-ins' name column at the SAME translator keys the
-- quick-pay button's own hardcoded fallback already uses
-- ({{ T "tender.cash" }} / {{ T "tender.card" }}, index.html), plus a new
-- "tender.gift_card" key (web/locales/{en,ar,fa,tr}.json) for the third.
--
-- Shipped as an ADDITIVE migration rather than an edit to 001_init.sql's
-- seed: editing 001_init.sql changes its checksum, and
-- internal/db/db.go's verifyAppliedMigrations hard-fails an already-
-- migrated database on any checksum drift (idempotentRerunVersions is
-- empty; version 1 is not allowlisted) — bricking every device that
-- already migrated, including the pilot install. 004/015/017 all document
-- this same trap. Because this runs on every install (fresh or existing)
-- after 001_init.sql has already seeded the literal rows, one additive
-- UPDATE covers both cases — no separate edit to the seed data is needed.
--
-- Scoped to plugin_id IS NULL (built-ins only — a plugin-synced row's name
-- is a manifest-contract label, untouched here, per ut-docs#2015) and to
-- the exact literal text 001_init.sql shipped, so this is idempotent and
-- never clobbers a row that isn't the unmodified built-in (there is no
-- rename-payment-method feature today, but this stays a no-op if one ever
-- writes a different name here). Does not touch the 'voucher' row
-- (015_voucher_payment_method.sql already seeds it with the translator-
-- key-shaped literal "Voucher" — a pre-existing separate gap, not this
-- card's scope) or the 'gift' row's id/type — ut-docs#1832 called
-- reinterpreting the legacy 'gift' row's semantics out of scope, but this
-- only changes how its NAME resolves for display, not its identity or
-- behaviour.
UPDATE payment_methods SET name = 'tender.cash'
  WHERE id = 'cash' AND plugin_id IS NULL AND name = 'Cash';
UPDATE payment_methods SET name = 'tender.card'
  WHERE id = 'card' AND plugin_id IS NULL AND name = 'Card';
UPDATE payment_methods SET name = 'tender.gift_card'
  WHERE id = 'gift' AND plugin_id IS NULL AND name = 'Gift Card';
