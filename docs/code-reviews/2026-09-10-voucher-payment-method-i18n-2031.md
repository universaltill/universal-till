# 2026-09-10 — Built-in `voucher` payment method name i18n (ut-docs#2031)

## What shipped

ut-docs#2021 fixed the three `001_init.sql`-seeded built-in payment
methods (`cash`/`card`/`gift`) to carry translator keys
(`tender.cash`/`tender.card`/`tender.gift_card`) instead of literal
English text in `payment_methods.name`, so `PaymentMethod.Name` (rendered
through `T` at render time, per ut-docs#2015) actually translates on a
non-English till. That fix's own migration (`022_builtin_payment_method_
i18n_keys.sql`) deliberately left the `voucher` payment method out of
scope — it's seeded separately by `015_voucher_payment_method.sql`
(ut-docs#1832) and still carried the literal `'Voucher'` string, so it had
the identical untranslated-on-a-non-English-till bug.

### Fix

A new additive migration, `024_voucher_payment_method_i18n_key.sql`,
`UPDATE`s the `voucher` row's `name` from the literal `'Voucher'` to a new
`tender.voucher` translator key — scoped to `id = 'voucher' AND plugin_id
IS NULL AND name = 'Voucher'`, mirroring 022's own idempotent, tightly-
scoped pattern (never edits the already-shipped 015 migration, per the
checksum-drift trap 004/015/017/022 all document). Added the
`tender.voucher` key to `web/locales/{en,ar,fa,tr}.json`, each with a
distinct translation from `tender.gift_card` (not copy-pasted — fa in
particular initially risked reusing the exact `tender.gift_card` string,
caught and changed to a coupon-flavored word during implementation).

Updated `internal/data/pos_repo_batch8_lookups_test.go`:
`TestPOSRepo_ListActivePaymentMethods_SeededVoucherMethod`'s assertion now
expects `tender.voucher` instead of the literal; `TestPOSRepo_
BuiltinPaymentMethods_HaveI18nKeyNames` now also covers the `voucher` row;
and a new `TestPOSRepo_VoucherPaymentMethodI18nMigration_Idempotent` mirrors
the existing cash/card idempotency test for this migration specifically —
confirming a non-matching row (`gift`, hand-seeded with a different name)
is left untouched and a second run of the `UPDATE` is a true no-op.

## Independent review

Fresh-context Sonnet subagent, isolated worktree (`complexity:easy` →
Sonnet review per model-routing rules — a different, clean-context
instance that never saw the implementation reasoning). **Verdict: PASS,
safe to merge, no blocking findings.** All three acceptance criteria
verified directly against the diff (migration WHERE-clause scoping,
locale key presence/distinctness, and the new/updated tests). Two
informational (non-blocking) notes only: the fa/tr translations use
coupon/gift-check-flavored wording rather than a literal "voucher"
transliteration (consistent with this codebase's existing translation
style, not something CI or a review can adjudicate) and this review
record itself was still pending at review time (this document).

## What was verified beyond automated tests

- **Independently TDD re-verified, twice** (once during Dev/Tester, once
  again by the independent reviewer in its own isolated worktree): the
  new migration file was moved out of the way, the affected tests were
  re-run and confirmed to **FAIL** with the exact literal-string mismatch
  the bug produces (`Name = "Voucher", want translator key
  "tender.voucher"`), then the file was restored and the tests confirmed
  to **PASS** again. Both runs did this atomically (no turn/tool-call
  boundary left the repo in the reverted state).
- Confirmed via `grep` that the migration's SQL and every touched test
  never reference the `gift` row by anything other than as an untouched
  control case — `gift`'s id/type/name are asserted unchanged.
- Confirmed the diff touches no file under `web/ui/**` — no UI surface
  changed (the fix is migration + locale JSON + Go test only), so neither
  the UX-guidelines checklist nor a `web/help/` manual update applies;
  the English-language display text is unchanged (`en.json`'s
  `tender.voucher` value is `"Voucher"`, identical to before), only
  non-English locales are affected.
- `gofmt -l .` (no output), `go vet ./...`, `go build ./...` (both clean),
  `golangci-lint run ./internal/data/...` (0 issues), full `go test ./...`
  (55+ packages, 0 failures, run twice — once during Dev/Tester, once
  independently by the reviewer), `scripts/ci/guard-data-access.sh`
  (change confined to `internal/data`/`internal/db`, allowed locations for
  raw SQL), `scripts/ci/guard-i18n.sh` (1618+ template keys resolve, all
  locales match `en.json`'s key set, no duplicates), `scripts/ci/
  guard-migration-version-collision.sh` (024 is unique).
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Safe-to-merge verdict

**Yes.** No money, UI, help-topic, compliance-wording, plugin-signing,
kiosk-engine or Android surface touched, so no other `CLAUDE.md` guard
applies beyond the ones already run above.

## Explicitly deferred

Nothing — this closes the specific gap ut-docs#2021 explicitly scoped out
(the `voucher` row), with no further known built-in payment method left
carrying an untranslated literal name.
