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

## Pre-existing failures — NOT from this change, and NOT a red `main`

`go test ./...` reports six failures locally. All six reproduce identically
on a clean stash of `main`, verified by stashing this branch and re-running
the same test names:

- `TestFirstBootSetupThenLogin`
- `TestSetupWizardRendersShopTypeStep`
- `TestSetupPageLoadsHTMXForTheJoinForm`
- `TestSetupPageRendersDiscoveryAffordance`
- `TestInventoryReplicaBannerNeverLinksAcrossDevices`
- `TestCatalogReplicaBannerNeverLinksAcrossDevices` (`internal/pages/catalog`)

**First read of this was wrong** — "main is red" — and CI contradicted it:
`ci` was green on `main` at 17:22 today. Chased it down rather than leaving
two incompatible facts standing.

Actual cause: they depend on the developer's **OS locale**. ut-docs#590 gave
`GET /setup` a language-detection redirect (`setup_page.go:137`); these tests
request `/setup` with no `?lang=` and no cookie and expect a rendered page,
but on a machine with a locale set they get `303 → /setup?lang=en`.
Demonstrated directly:

```
LANG= LC_ALL= go test -run TestSetupPageRendersDiscoveryAffordance   → ok
LANG=en_GB.UTF-8 go test -run TestSetupPageRendersDiscoveryAffordance → FAIL
```

CI passes because the runner container has no `LANG`, so detection never
fires. `setup_detect.go` already exposes `osLocaleEnv`/`osTimezoneName` as
(its own words) "swappable seams" — these tests just don't use them.

Filed as ut-docs#662 with the root cause and the fix, not as a vague
"tests are failing".

## Independent review status

The pipeline's Reviewer step calls for an independent different-model pass
before commit. This session is operating under a standing instruction not to
spawn subagents unprompted, so that pass has **not** been run — this document
is a self-review. Recorded honestly rather than claimed; `/code-review` on
the branch would close the gap.

## Independent review — run 2026-08-13, Scrum Master cycle (Opus, fresh context)

The gap above is now closed. A cold cloud pipeline cycle picked this PR up
under step 0c ("finish stale reviewed PRs first") and, because this
document disclosed no independent pass had happened, ran a genuinely
independent review (Opus, worktree-isolated, no prior reasoning carried
in) before allowing the merge.

**Verdict: SAFE TO MERGE AFTER FIXES.** The reviewer confirmed the core
engineering independently — floor genuinely enforced in the repository on
every `Upsert`, delete semantics correct, `is_builtin` actually derived
rather than trusted, drift guards real (verified by deliberately breaking
the DE seed and watching both drift tests fail for the right reason),
migration properly additive, i18n complete with real (not machine-literal)
translations across all four locales, ADR-0040 respected (no
per-jurisdiction claim, single global floor). It also independently
re-verified the "six pre-existing failures are locale-dependent, not
mine" claim by reproducing one directly. Full detail in the review
subagent's own report (not duplicated here); the three blocking findings
it raised, and what changed in response:

- **B1 — the manual and UI copy described behaviour that doesn't exist
  yet.** `archive_min_days` has zero consumers today: `reportRetentionCutoff`
  (the code that actually prunes `report_archive`) doesn't read it, and the
  wizard still reads the old hardcoded `setupCountries` slice (that's
  #660). The shipped copy said otherwise ("Changes apply to shops using
  that country", "the smallest number of days... before anyone can
  permanently delete it"). **Fixed**: reworded `countrysettings.intro`,
  `countrysettings.retention_help` (all 4 locales) and all four
  `country-settings.md` help topics to say plainly that these are
  foundation values not yet wired to a running shop or to actual pruning,
  and that raising the number today changes nothing yet. No compliance
  claim was ever made (the reviewer confirmed that independently too) —
  this fix is about the *foundation* claim, not a compliance one.
- **B2 — three independent spellings of ADR-0040's "single global floor"
  with nothing tying them together**: `data.GlobalArchiveMinDays` (3650),
  `reportRetentionCutoff`'s `AddDate(-10,0,0)` (~3652–3653 real days
  depending on leap years), `maxReportArchiveExportRangeDays` (3660,
  unrelated coincidence). **Fixed**: kept `reportRetentionCutoff` on
  calendar years (deliberately, not a fixed day count — see the comment
  now on it: a fixed day count would make the enforced window a few days
  *shorter* than the promised floor in some years, which is the wrong
  direction to fix this in), and added
  `TestReportRetentionCutoffNeverShorterThanGlobalArchiveMinDays`, which
  asserts across four spread-out `now` dates (including 2100, not a
  Gregorian leap year, and spans crossing 2/3 leap days) that the actual
  prune window is never shorter than `data.GlobalArchiveMinDays` days.
  Re-pointed `maxReportArchiveExportRangeDays` at the same constant
  (`data.GlobalArchiveMinDays + 10`) so it's one spelling with a stated
  offset, not a third independent number.
- **B3 — the `is_builtin` anti-spoof claim was asserted in prose but
  untested.** The existing test only ever left `IsBuiltin` at its zero
  value, so it would have passed identically even if `Upsert` copied the
  caller's flag straight through. **Fixed**: added
  `TestUpsertIgnoresCallerSuppliedIsBuiltin`, which sets
  `IsBuiltin: true` on a non-builtin code and asserts both that it's
  stored as `false` and that `Delete` still removes the row outright (not
  restore) — and verified the test actually catches the bug by
  temporarily making `Upsert` trust `c.IsBuiltin`, confirming the new test
  fails with the expected message, then reverting.

Also merged `main` into the branch (it was behind by the fix/532 merge)
and re-ran the full gate after: `go build`/`go vet` clean; `go test ./...`
green with `LANG` unset; all guards green including `guard-docs-shots.sh`
(re-ran `make docs-shots` after the copy edits — only the 4
`country-settings` screenshots actually needed retaking; the run also
re-encoded several unrelated topics' PNGs with no content change, which
were reverted before commit rather than committed as diff noise, per the
precedent this same PR's first draft already set).

Non-blocking findings from the same pass (reflected-error-key allowlist,
one dead template field, one unused locale key, an `err` shadowing bug in
`Delete`'s builtin-restore audit path, no upper bound on
`archive_min_days`, `parsePercentAsBP`'s `Inf`/`NaN` handling relying on a
downstream range check rather than its own, a silent no-op delete on a
nonexistent custom code, a generic error message for a rejected country
code, and the shipped `en`/`ar` screenshots being visually truncated at
the docs-shots viewport width) are real but not blocker-class — logged as
a new Backlog card
(`Country settings page: minor hardening + truncated manual screenshots
follow-ups from ut-docs#659 review`) rather than gating this merge on
them.
