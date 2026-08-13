# Review — per-country settings framework (ut-docs#659)

## Summary

Moves per-jurisdiction defaults out of a compile-time Go slice
(`internal/pages/setup_page.go`'s `setupCountries`) into an
admin-manageable `country_settings` table, and adds the `archive_min_days`
retention field the product owner asked for on ut-docs#635:

> "The archive deletion for each country should be set in the country
> setting, this mean we need to have setting for each country like currency,
> archive min time etc... also it is possible for admin to delete those
> configs. these settings will set when we install the till for the first
> time for the shop."

First of three successors split from #635 (with #660 wizard integration and
#661 the archive-purge gate). Deliberately does **not** change the wizard —
that is #660 — so this diff is a pure addition plus a drift guard.

## The ADR constraint that changed the design mid-flight

The Architect pass originally proposed **per-jurisdiction statutory floors**
(a German 10-year GoBD floor, a different UK one). Re-reading ADR-0040
before writing code ruled that out, and ADRs are binding (ADR-0007):

> "10 years is applied as a **single global floor**; this ADR **does not
> encode per-jurisdiction retention minimums** (they differ, e.g. DE vs UK)
> — a future amendment can lower the floor per-region if a market needs it,
> but nothing here currently raises or asserts a jurisdiction-specific
> compliance claim."

> "**No GoBD/§147 AO or other compliance certification is claimed anywhere
> in code, UI copy, or commit messages** ... a compliance claim is the
> product owner/legal's call."

So the original design was wrong twice: ADR-0040's 10 years is **global,
not German**, and seeding a per-country statutory table would be exactly the
jurisdiction-specific compliance claim the ADR reserves for the product
owner and legal.

**What shipped instead:** every country seeds at the same 3650-day floor.
Raising is allowed, lowering below the floor is refused. The column is
per-country, so a genuinely per-jurisdiction value becomes data entry rather
than a rebuild — once an ADR-0040 amendment authorises it. Flagged on #635
and #659 for the product owner; it blocks nothing.

The UI copy was written to match: the help topic and locale strings say
"the minimum shown" and "records you may still be required to produce", and
name no statute or jurisdiction anywhere.

## Two behaviours enforced in the repository, not the handler

Both would be trivially bypassable if they lived in the settings form:

1. **The retention floor** — `validateCountrySetting` refuses any `Upsert`
   below `GlobalArchiveMinDays`, so no API caller, plugin or future code
   path can drop a shop under it. Tested directly against the repo, and
   again through the HTTP layer.
2. **Delete on a builtin country restores its defaults** rather than
   removing the row. Taken literally, "admins can delete those configs"
   would let a shop delete the row its retention window is read from and
   land on *no* configured minimum for a real jurisdiction. Restoring keeps
   the capability (the customisation is gone) while guaranteeing #660 and
   #661 always have a value to read. Operator-created countries still delete
   outright — we ship no opinion about those.

`is_builtin` is derived from the shipped defaults on every write, never
taken from the caller — otherwise a caller could mark its own row builtin
and change what `Delete` does to it.

## Drift guards (the real risk in this change)

The shipped defaults necessarily exist twice — migration 041 seeds a fresh
DB, and `builtinCountryDefaults` is what `Delete` restores to. A migration
cannot call Go and a restore cannot re-run a migration, so instead of
pretending there is one source of truth, three tests make divergence
impossible:

- `TestBuiltinDefaultsMatchMigrationSeed` — Go defaults vs the seeded table,
  field by field.
- `TestSeededRetentionMatchesGlobalFloorConstant` — the SQL literal `3650`
  vs the Go constant.
- `TestSetupCountriesMatchCountrySettingsDefaults` (in `internal/pages`) —
  the wizard's still-live slice vs the new defaults, including the
  percent→basis-point conversion. **Delete this file with the slice when
  #660 lands**; it is called out in its own header comment.

Without the third, #659 and the wizard would be two live copies of the same
data until #660, and an edit to one would silently change what a real shop
is prefilled with.

## Found in self-review, fixed before commit

- **Country code was unvalidated** while doubling as an HTML element `id`
  (the admin page binds each row's inputs to its form via `form="cs-<code>"`).
  A code with whitespace or punctuation would not have been an XSS —
  `html/template` escapes it — but would have silently broken the form
  association, producing a row whose Save button does nothing. Now
  restricted to `A-Z0-9`, max 8 chars, refused at the repository.
- **Tax rate had no upper bound.** Capped at 10000 bp (100%); an unbounded
  rate corrupts every downstream price calculation.
- **First UI draft duplicated every value** as both a read-only column and
  an edit input in the same row, overflowing the table (visible in the first
  screenshot run). Rewritten so the row *is* the form — no read-only column
  can disagree with the field beside it.
- **`parsePercentAsBP` rounds rather than truncates** — `8.5%` lands on
  850 bp, not 849, which a bare `int64()` conversion would have produced.
  Pinned by `TestParsePercentAsBPRounding`.

## Repo rules checked

- Repository pattern: all SQL in `internal/data` / `internal/db`
  (`guard-data-access.sh` green).
- Basis points for rates, not `money.Money` — per the repo's money rule.
  Percent exists only at the form boundary.
- i18n: 23 new keys added to **all four** locale files (en/tr/fa/ar), all
  1364 keys; no hardcoded user-facing strings (`guard-i18n.sh` green).
- RTL verified from the real Arabic screenshot: layout mirrors, numerals
  stay LTR.
- Manual topic shipped in the same branch in all four locales, with
  regenerated screenshots (`guard-help-topics.sh`, `guard-docs-shots.sh`
  green). `country-settings` added to `AUTH_TILL_TOPICS` in the docs-shots
  spec — it is manager-gated with no `UT_AUTH=off` bypass, the same reason
  `users`/`translations`/`kitchen-stations` are there.
- Migration is append-only (041, after 040).

## Verification

- `go build ./...` clean.
- New tests pass under `-race`: 6 repo tests, 3 drift tests, 6 page tests.
- All six guards green.
- Real driven run: `make docs-shots` renders the page in all four locales;
  the English and Arabic captures were inspected directly, not just counted
  as passing.

## Pre-existing failures on `main` — NOT from this change

`go test ./... -race` reports six failures. All six reproduce identically on
a clean stash of `main`, verified by stashing this branch and re-running the
same test names:

- `TestFirstBootSetupThenLogin`
- `TestSetupWizardRendersShopTypeStep`
- `TestSetupPageLoadsHTMXForTheJoinForm`
- `TestSetupPageRendersDiscoveryAffordance`
- `TestInventoryReplicaBannerNeverLinksAcrossDevices`
- `TestCatalogReplicaBannerNeverLinksAcrossDevices` (`internal/pages/catalog`)

Filed separately — `main` is currently red and that should not be discovered
one card at a time.

## Independent review status

The pipeline's Reviewer step calls for an independent different-model pass
before commit. This session is operating under a standing instruction not to
spawn subagents unprompted, so that pass has **not** been run — this document
is a self-review. Recorded honestly rather than claimed; `/code-review` on
the branch would close the gap.
