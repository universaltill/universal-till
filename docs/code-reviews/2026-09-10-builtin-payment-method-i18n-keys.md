# Code review: built-in payment method names resolve through T (ut-docs#2021)

**Date:** 2026-09-10
**Card:** ut-docs#2021 — "Built-in payment method names ('Cash', 'Card', …)
stay untranslated on a non-English till"
**Complexity:** easy (Dev: Sonnet inline; Review: fresh-context Sonnet subagent)

## What shipped

The three built-in payment methods (`cash`/`card`/`gift`, seeded by
`001_init.sql`) stored literal English text in `payment_methods.name`.
`PaymentMethod.Name` renders through `T` at render time (ut-docs#2015),
which passes plain text through unchanged when it isn't a known translator
key — so the built-ins never translated on a non-English till (confirmed
live: the Arabic sell-screen screenshot still showed `Cash` in English).

- New additive migration `022_builtin_payment_method_i18n_keys.sql`:
  repoints `cash`/`card`/`gift`'s `name` at `tender.cash`/`tender.card`/
  `tender.gift_card` respectively. Scoped to `plugin_id IS NULL AND name =
  '<exact shipped literal>'` — idempotent, and never touches a
  plugin-synced or already-migrated row. Deliberately does **not** edit
  `001_init.sql` — editing it would change its checksum and
  `verifyAppliedMigrations` (`internal/db/db.go`) hard-fails every
  already-migrated device on checksum drift (`idempotentRerunVersions` is
  empty; version 1 isn't allowlisted) — the same trap 004/015/017 already
  document. Because this migration runs on every install (fresh or
  existing) after `001_init.sql`'s seed, one additive `UPDATE` covers both
  cases.
- New locale key `tender.gift_card` added to all 4 core locale files
  (`web/locales/{en,ar,fa,tr}.json`).
- Updated five existing tests across `internal/data` and `internal/plugins`
  that hardcoded the old literal built-in names ("Cash"/"Gift Card") as
  expected values or as name-collision-test fixtures, since the built-in's
  `payment_methods.name` is now a translator key, not literal text.
- Two new regression tests in `internal/data/pos_repo_batch8_lookups_test.go`:
  `TestPOSRepo_BuiltinPaymentMethods_HaveI18nKeyNames` (direct assertion on
  all three built-ins) and `TestPOSRepo_BuiltinPaymentMethodI18nMigration_Idempotent`
  (idempotency + exact-literal scoping).
- Doc-comment update on `PaymentMethod.Name` (`internal/data/pos_repo.go`).

## Independent review

A fresh-context Sonnet subagent reviewed the diff in an isolated worktree
(`Agent(isolation: "worktree")`, per model routing for `complexity:easy`).

**Commands run against the actual reviewed commit** — all clean:
`go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...` (full
module), `golangci-lint run ./...`, `shellcheck scripts/ci/*.sh`,
`bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-migration-version-collision.sh`.

**TDD re-verification (independent, not taken on the implementer's word):**
moved `022_builtin_payment_method_i18n_keys.sql` out of the migrations
directory — `TestPOSRepo_ListActivePaymentMethods_SeededVoucherMethod` and
`TestPOSRepo_BuiltinPaymentMethods_HaveI18nKeyNames` both failed with clear
messages naming the literal `"Cash"`/`"Gift Card"` still present. Restored
the migration — both passed again.

**Findings:**
- **Blocker: none.**
- **Should-fix, out of scope for this diff** — filed as new Backlog cards
  rather than absorbed into this PR:
  - ut-docs#2030 — `internal/db.Open()`'s SQLite DSN is built from an
    unescaped path; a `#`/`?`/`%` in the data directory silently
    mis-parses the URI and drops every pragma (FK enforcement, WAL,
    busy-timeout). Pre-existing, unrelated to this diff (reproduces
    identically on `main`), production-severity.
  - ut-docs#2031 — the `voucher` payment method's name (seeded separately
    by `015_voucher_payment_method.sql`) is still the literal `'Voucher'`,
    same class of bug as this card, deliberately excluded from this
    migration's scope (documented in its own header comment).
- **Non-issues checked and cleared:** migration idempotency/scoping
  correctness; checksum-drift reasoning for not editing `001_init.sql`;
  migration version `022` genuinely free; ar/fa/tr translations for
  `tender.gift_card` judged natural and correct; broad search across
  `internal/` and `e2e/` for other literal `"Cash"`/`"Card"`/`"Gift Card"`
  comparisons found nothing else needing a change (receipts build labels
  from `.Type`/`.Method`, never `.Name`); every template rendering
  `PaymentMethod.Name` already wraps it in `{{ T .Name }}`; e2e specs
  locate the cash button by rendered text (`"Cash"`), which is unchanged
  in English; the five updated collision tests are still genuinely
  load-bearing (verified via the TDD re-verification above, since
  breaking the migration breaks their assertions too); no real
  client/shop name or secret-shaped literal in the diff; no help-topic or
  screenshot update needed (no page/screen structure changed).

## Verified beyond automated tests

Direct end-to-end check that `T` actually resolves the affected keys per
locale (not just that the DB row holds the right key string), via a
throwaway same-package test against `internal/config.NewI18n` loading the
real `web/locales` directory:

```
en / tender.cash       => "Cash"
en / tender.card       => "Card"
en / tender.gift_card  => "Gift Card"
ar / tender.cash       => "نقد"
ar / tender.card       => "بطاقة"
ar / tender.gift_card  => "بطاقة الهدايا"
fa / tender.cash       => "نقدی"
fa / tender.card       => "کارت"
fa / tender.gift_card  => "کارت هدیه"
tr / tender.cash       => "Nakit"
tr / tender.card       => "Kart"
tr / tender.gift_card  => "Hediye Kartı"
```

English is byte-identical to before (existing `tender.cash`/`tender.card`
values), confirming no regression for English tills; ar/fa/tr now resolve
to real translated text instead of the previous English pass-through.

## Safe-to-merge verdict

**Yes.** Correct, well-scoped, well-tested (including two new load-bearing
regression tests, independently re-verified fail-then-pass), and every
CI-blocking guard plus the full test suite passes clean on the reviewed
commit.

## Deferred

- ut-docs#2030 (DSN path-escaping bug, unrelated pre-existing finding).
- ut-docs#2031 (voucher payment method's own untranslated name, explicitly
  out of this card's scope).
- Lang-pack follow-up: `tender.gift_card` is a brand-new core locale key
  (not yet existing on any branch), so per the reviewer skill's
  lang-pack-drift guidance this merges core first — the same lane owns
  landing the `ut-plugin-language-{de,es}` follow-up in this same cycle.
