# Review — takeaway-rate overrides: out-of-range stored entries and localized prefill (ut-docs#4086)

**Date:** 2026-10-10 · **Lane:** lane:cloudsession · **Built by:** Sonnet (Dev subagent) · **Reviewed by:** Opus 5.5 (fresh-context subagent) — card `complexity:easy`.

## What shipped

Plugins → ⚙ Settings, typed takeaway-rate overrides editor (`internal/pages/plugin_settings_page.go`, `web/ui/pages/plugin_settings.html`):

- `buildTaxOverrideRows` now takes the request locale. A stored entry outside 1..`taxrate.MaxBP` bp — active row or orphan — renders a **blank** input plus a per-row notice, "Invalid takeaway rate: <stored>%" (existing key `plugins.settings.takeaway.invalid_rate`, so no new locale key and no language-pack follow-ups). The input gets `aria-invalid` + `aria-describedby`.
  - Before: an orphan `-500` prefilled "-5", which failed the input pattern and blocked the whole form. `0`/`15000` gave a 400 for the whole form. An active row with `bp<=0` rendered blank with no notice, and the next save deleted it silently.
- Every percent the editor renders (override, placeholder, dine-in) goes through `httpx.LocalizeMajor`. A comma-locale till prefills `7,5`, and `ParsePercentBP` reads it back unchanged.
- `parseTaxOverrides` is unchanged: a blank field still removes the entry. That is now visible (notice + help), not silent.
- Help: `web/help/{en,de,tr,ar,fa}/plugins.md` step 4 gains one sentence. Structure is unchanged.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Template-literal `": "` and `"%"` around the stored value: fa/ar get the Latin `%`/colon. | Accepted. This is the cost of reusing the translated key instead of adding a format key that every pack would need. |
| 2 | nit | `id`/`aria-describedby` are built from the tax code id. An id with a space would be an invalid HTML id. | Accepted. Tax code ids are internal slugs, and both attributes are escaped the same way, so they stay paired. |
| 3 | nit | Help says "leave it blank to remove it". The field is already blank, so any save removes the entry. | Accepted. The wording is accurate: a save with the field left blank removes it. |
| 4 | nit | GET test: `strings.Contains(body, "takeaway_pct_")` is always true. | Fixed: condition dropped. |

The reviewer found no blockers. It checked the round-trip for every valid bp in en/ar/fa/tr and in the pack locales (de/es/fr/pt): `LocalizeMajor` only swaps the separator and adds no grouping, so the result always matches `PercentPatternLocal`. Recurring-bug checks (file write without `MkdirAll`, cwd-relative path) don't apply here.

## TDD re-verification

The tests can't run against the pre-fix production code, because the signature and field change means they don't compile. The reviewer therefore ran single-point mutations in an isolated worktree. Each one failed at least one new test:

- ignoring the locale;
- prefilling invalid values again;
- dropping the template notice;
- `bp>0` → `bp>=0`;
- `<=MaxBP` → `<MaxBP`.

After the production code was restored, all five new tests pass. The Dev phase also saw them fail first (compile failure, then four assertion failures on a partial implementation).

## Verified beyond unit tests (driven run)

- Booted a throwaway till (temp data dir, auth off) and seeded a plugin with overrides `{"tax_19":0,"tax_7":750,"tax_gone":-500,"tax_big":15000}`.
- **Headless Chromium:**
  - Every takeaway input is empty or valid (`checkValidity()` true).
  - `tr` prefills `7,5`, `en`/`fa` prefill `7.5`. `fa` renders `dir=rtl` with the notices aligned correctly.
  - Clicking **Save settings** with the bad entries present → POST 200, "Settings saved". Before the fix the browser blocked the submit.
- **Screenshots looked at:** en 1024×600, fa 1024×600, en 360×800.
- **Not checked:** the de pack's own wording for the help sentence (pack not in this session); the dark theme; real touch hardware.
- **Pre-existing, not caused by this change:** at 360px the page overflows by 27px horizontally, with or without invalid entries. Filed as ut-docs#4098.

## Gate

- `gofmt`, `go build ./...`, `go vet`, full `go test ./...`, `golangci-lint`.
- Every guard in `ci.yml`'s build job. Exceptions:
  - `guard-deadcode-baseline.sh`: tool limit in this container (x/tools v0.48 can't load go1.27 packages). The change adds no functions.
  - `guard-shellcheck-version.sh`: no shellcheck binary here. No shell files changed.
- CI runs both. `guard-docs-shots.sh` was missed in the first local pass and CI caught it, because the help topic changed. `make docs-shots` re-ran all 120 shots: every PNG came back byte-identical, so only `manifest.json`'s hashes changed.

## Deferred

- ut-docs#4097 — promotions page has the same dot-on-comma-locale prefill.
- ut-docs#4098 — 360px overflow on the plugin settings page.

**Verdict:** safe to merge.
