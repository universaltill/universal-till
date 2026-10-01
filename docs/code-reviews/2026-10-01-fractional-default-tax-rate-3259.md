# Review: fractional default tax rate (ut-docs#3259)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54
- **Author model:** Opus 5.5 (Dev subagent). **Reviewer:** Fable (independent subagent, separate worktree).
- **Branch:** `fix/3259-fractional-default-tax-rate`

## What shipped

`store.tax_rate` (the shop's default VAT rate) could only hold a whole percent. It was parsed with `strconv.Atoi` everywhere and the setup wizard rounded `country_settings` basis points (CH 8.1 % became 8 %). Now:

- **New parser.** `internal/taxrate.ParsePercent` is the inverse of `taxrate.FormatPercent`:
  - integer-exact, with no float;
  - accepts `.` or `,`;
  - at most two decimals, 0–100 %.
- **Basis points end to end.** `common.RuntimeState.TaxRatePct` is now `TaxRateBP`, and `config.Locales.TaxRate` is now `TaxRateBP`. The engine sites (`engineConfigFor`, both `init.go` sites) pass basis points straight through, so there is no `*100` left anywhere.
- **Storage format.** The key and its percent format are unchanged:
  - a whole rate is written as `"19"`, byte-identical to earlier builds;
  - a fractional rate is written as `"8.1"`.
- **Setup wizard.** It renders `FormatPercent(bp)` with no rounding and parses the posted `tax_rate_pct` with `ParsePercent`.
- **Settings endpoints.** `/api/settings/upsert` and the legacy `taxRatePct` form both validate with `ParsePercent`. They refuse bad input with the existing `taxcodes.err.invalid_rate` message and store the normalised value (`"8,10"` → `"8.1"`). The legacy form now also has a 100 % cap; before, it had no upper bound.
- **Latent boot clobber, fixed.** `app.Run` calls `LoadRuntimeConfig` and then `SaveRuntimeConfig` on every boot. Before this change, a stored `"8.1"` failed `Atoi`, so the next boot wrote `UT_TAX_RATE`'s `"20"` over the shop's rate. `SaveRuntimeConfig` now leaves `store.tax_rate` alone when the stored value can't be parsed, or can't be read at all (review finding 1). It logs a WARN when that happens.
- **`UT_TAX_RATE`.** It accepts decimals now. An invalid value falls back to 20 %; before, it silently became 0 %.
- **Help.** `web/help/{en,de}/country-settings.md` no longer say the wizard rounds to a whole percent. The ar, fa and tr versions never said so.
- **README.** The `UT_TAX_RATE` note is updated.

## Findings

| # | Severity | Finding | Resolution |
|---|---|---|---|
| 1 | should-fix | `storedTaxRate` treated a read **error** as "empty". A transient read failure at boot would still write the default over the real rate. | **Fixed.** A read error now skips the key and logs a WARN. |
| 2 | nit | An unreadable `UT_TAX_RATE` now gives 20 % instead of 0 %. | Accepted as an improvement, since the README documents 20 as the default. Recorded here. |
| 3 | nit | A legacy stored value above 100 (the old `taxRatePct` path had no upper bound) silently falls back to the default. | **Fixed:** `SaveRuntimeConfig` now logs a WARN naming the value. The rate stays untouched, so an admin can see it and re-set it. |
| 4 | nit | The help prose switched example rate mid-sentence ("8.5" … "8.1"). | **Fixed** in en and de. |
| 5 | nit | A comment line in `setup_page.go` was too long. | **Fixed** (reflowed). |
| 6 | nit | `country_settings_page.go`'s `parsePercentAsBP`/`formatBPAsPercent` duplicate the grammar. | Pre-existing. Left as is, to keep this change focused. |
| 7 | info | An additional till on an older build can't parse `"8.1"` and falls back to its config default. | Bounded: its boot save writes only locally and is overwritten on the next admin-bundle pull; `saveStateThrough` diffs base and new state, so it never pushes `"20"` to the main till; and additional tills follow the main till's version (#2732). Documented at `RuntimeState.TaxRateBP`. |

## Verification

- **TDD claims re-verified by the reviewer in a separate worktree:**
  - With `runtime.go` reverted to the `main` logic, the boot round-trip tests fail.
  - With `ParsePercent` changed to refuse fractions, these fail: the upsert fractional test, `TestSetupWizardFractionalTaxRate` and the boot test.
- **Mechanically edited tests:** the reviewer checked the ~75 files. No assertion was weakened, and the 21.5 % wizard prefill test is tighter (`'21.5'`, not `'22'`).
- **Reviewer's grep:** no remaining reader of `TaxRatePct`, `Locales.TaxRate` or `detectedTaxRatePct` in Go, templates, `web/public`, `e2e/`, `scripts/` or `plugins/`. No ut-cloud code reads or writes `store.tax_rate`.
- **`make docs-shots`:** run for real (120/120 passed). The prose change only alters the en `country-settings` topic hash. Every PNG re-rendered with environment-only pixel noise and no screen change, so the PNGs were restored and only `manifest.json` from the generator is committed.
- **Full gate after the last edit:** gofmt, build, vet, `go test ./...`, golangci-lint, and every guard in `ci.yml`'s `build` job.

## Not looked at visually

The only UI change is the value inside the wizard's hidden `tax_rate_pct` input and the tile `data-tax` attribute. No visible surface changed, so no screenshots were reviewed.

## Verdict

Safe to merge.

## Deferred

- Finding 6: dedupe `country_settings_page.go`'s percent parser and formatter onto `internal/taxrate`.
- A visible Settings field for the default rate. None exists today, so adding one is a UX feature, not part of this fix.
