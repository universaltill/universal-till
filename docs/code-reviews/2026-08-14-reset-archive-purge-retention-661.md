# Review: gate reset-archive purge on the country retention window (ut-docs#661)

**Date**: 2026-08-14
**Card**: universaltill/ut-docs#661 — "Gate reset-archive purge on the active country's archive_min_days retention window"
**Complexity**: medium
**Reviewer model**: fresh-context Opus subagent, worktree-isolated (per this card's `complexity:medium` tier — see `scrum-master` skill's model routing)

## What shipped

ADR-0042 §3 shipped **no** delete-archive endpoint at all, deliberately,
pending a retention decision (ut-docs#635 → #659 supplied
`country_settings.archive_min_days` as real per-country data). This card
wires the two together — the actual permanent-purge path for an archived
"reset transactions" batch.

- `internal/data/reset_archive_repo.go`: new `POSRepo.DeleteResetBatch`
  and its helper `resolveArchiveMinDays`. A batch with `sales_count == 0`
  (no completed sale) purges unconditionally; a batch with `sales_count >
  0` is refused with `*ArchiveWithinRetentionWindowError` (naming the date
  it becomes purgeable) until the shop's active country's
  `archive_min_days` has elapsed since `reset_batches.created_at` — falling
  back to `GlobalArchiveMinDays` (ADR-0040's 3650-day floor) when no
  country is configured or the configured code has no `country_settings`
  row. Runs inside one transaction; the gate is enforced in the repository,
  not only the handler.
- `internal/data/reset_archive_delete_test.go` (new): boundary test either
  side of a shortened country window, no-trading-history bypass, outside-
  window delete, unknown batch, unknown-country fallback.
- `internal/pages/data_api.go`: new manager-gated `POST
  /api/data/reset-archives/{id}/purge`, typed `PURGE` confirmation, same
  error-mapping shape as the sibling `.../restore` handler (`404`/`409`),
  localised messages via `httpx.T`.
- `internal/pages/data_api_test.go`: handler-level tests mirroring the
  repo-level ones, plus one exercising the full stack (handler →
  `DeleteResetBatch` → `country_settings`) against a real migrated DB.
- `internal/pages/common/state_test.go`: `TestStoreCountrySettingsKeyMatchesCommon`
  guards `data.StoreCountrySettingsKey` against drifting from
  `common.KeyCountry` — duplicated rather than imported because
  `internal/pages/common` already imports `internal/data`, so the reverse
  import would cycle.
- `web/ui/pages/settings.html`: a second confirm-input + "Delete
  permanently" button per archive row, wired the same way as the existing
  Restore control.
- `web/locales/{en,ar,fa,tr}.json`: 10 new `settings.data.archives_purge_*`
  keys, including the `%s`-interpolated retained-until message.
- `web/help/{en,ar,fa,tr}/{reports,country-settings}.md`: documented the
  new purge capability and corrected the country-settings page's claim
  that archive retention "does not yet control when archives are actually
  deleted."
- `ut-docs/adr/0042-reset-transactions-archive-not-delete.md`: an addendum
  recording that §3's delete-archive path now exists.

TDD-first: the boundary test backdates a real `reset_batches.created_at`
row and asserts refusal 1 day inside a shortened window and success 1 day
past it; the handler-level i18n test asserts the actual interpolated
retained-until date, not just "contains a year."

## Independent review (fresh-context Opus, worktree-isolated)

**Verdict: safe to merge.** Ran `go build`, `go vet`, targeted and
`-race` tests for `internal/data`/`internal/pages`/`internal/pages/common`,
and all five guards — all green. Confirmed the retention arithmetic
(`archivedAt.AddDate(0,0,minDays)` vs `time.Now().UTC().Before(...)`, both
UTC, no DST hazard), transaction safety (matches the sibling
`RestoreResetBatch`/`ResetTransactionHistory` shape — defer-rollback,
commit last, refusal paths touch nothing), parameterized SQL, and the JS
event-delegation split between the restore and purge controls are all
correct.

Findings, all addressed in this diff before merge:

1. **should-fix — ar/fa/tr `country-settings.md` intro paragraphs were
   left stale and self-contradictory.** The initial commit fixed the
   "Good to know" bullet in all four locales but only the intro paragraph
   in `en` — the other three still said "not yet connected to your shop"
   three lines above a bullet now saying the opposite. Fixed: all four
   intros rewritten to match `en`'s current text.
2. **should-fix — `resolveArchiveMinDays`'s doc comment had the safety
   argument backwards.** It called the global floor "a lower bound every
   jurisdiction must meet or exceed" and claimed the fallback "never
   under-protects" — but `validateCountrySetting` enforces every real row
   to be **at or above** the floor, so a country raised above it is the
   one case the fallback protects *less* than the truth. Fixed: comment
   corrected to state the floor is the minimum admissible value, not the
   maximum, and that it's still the right fallback because it's the one
   value guaranteed valid for an unconfigured/unknown country.
3. **should-fix — the `sales_count == 0` bypass was documented as "no
   trading history exists to protect," overstating what the code checks.**
   A zero-sales batch can still hold archived shifts (cash-book data),
   stock movements (cost prices), or held-sale payloads — none gated by
   the window. The judgement itself (mirroring the product owner's #187
   framing, which was specifically about sales) is defensible and not a
   blocker, but the doc comment, the ADR-0042 addendum, and the help-topic
   prose all overclaimed "nothing sensitive." Fixed: all three reworded to
   name the exposure precisely — gated on completed sales, not on the
   batch being empty.
4. **nit — the handler test's i18n assertion was vacuous.**
   `newDataAPITestDeps` never called `httpx.InitI18n`, so `httpx.T` fell
   back to the raw key and the unconsumed `%s` still happened to contain
   the digits "20" (from `%!(EXTRA ...)`), passing a test meant to prove a
   real date was interpolated — order-dependent on some other test file
   having wired i18n first, and incapable of failing on a broken
   translation. Fixed: `newDataAPITestDeps` now loads real i18n
   (`config.NewI18n` + `httpx.InitI18n`, same pattern as
   `backup_api_test.go`), and the assertion checks the actual computed
   retained-until date plus the absence of an unconsumed format verb.
5. **nit — `ErrArchiveWithinRetentionWindow` was named like a sentinel
   error value** (`Err*`) while it's a struct type carrying data, meant to
   be caught with `errors.As`, not `errors.Is` — inconsistent with the Go
   convention this file's own three sentinels (`ErrShopHasTradedSinceReset`
   etc.) follow. Fixed: renamed to `ArchiveWithinRetentionWindowError`
   across all three files that reference it.

Accepted without a diff change, noted here instead:

- **nit — the refusal names a date but the gate is an instant.** A batch
  archived at 14:30 stays refused until 14:30 on the named date, not from
  midnight. Real but low-severity UX polish; a Backlog card is more
  appropriate than expanding this diff.
- **nit — no UI affordance shows a batch isn't purgeable yet.** The
  Delete-permanently button renders unconditionally even though, at the
  10-year global floor, it will refuse for every real shop for a decade.
  `ListResetBatches` already returns what's needed to disable it
  server-side; tracked as a follow-up rather than expanded here, since it
  is a real feature addition (a computed per-row state), not a defect in
  what shipped.
- **nit — fallback diverges from `common.LoadState`'s own "GB" default**
  the moment an admin raises GB's retention above the floor (identical
  today, since GB seeds at exactly the floor). Noted in
  `resolveArchiveMinDays`'s comment (fix 2 above) rather than a behaviour
  change, since matching an unconfigured shop's *implicit* country to its
  *explicit* one is a design question, not a bug in this card's own scope.

Neither of the two UI/UX nits nor the date-truncation nit are money/tax/
data-loss/security class, so per this pipeline's "a second review round
must be earned by a blocker-class finding" rule, no second Opus pass was
run after applying findings 1–5 — the fixes were re-verified with the
targeted `-race` suite and all five guards, all green.

## Verified beyond automated tests

- Manually reasoned through the boundary test's arithmetic independently
  of the test itself (see finding-free section above) rather than trusting
  a passing assertion alone.
- Confirmed via `git grep` that `ErrArchiveWithinRetentionWindow` had
  exactly three references before the rename, so the rename is complete
  and didn't miss a call site.
- Re-ran `bash scripts/ci/guard-help-topics.sh` after the locale-doc fixes
  to confirm no locale still diverges from `en`'s topic set.

## Deferred / follow-ups (new Backlog cards, not chased in this diff)

- Truncate the retained-until comparison to date granularity so the named
  date is accurate from midnight, not from the original archive time.
- Show per-row purge eligibility (retained-until date, disabled control)
  in the Settings → Data UI instead of a control that predictably refuses
  for a decade on every real shop.

## Safe to merge

Yes. Repository-layer enforcement, transaction handling, error mapping,
i18n, and RTL-safe markup are all correct; the five findings above were
documentation/test-quality issues, all fixed in this same diff, not
behavioural gaps in what ships.
