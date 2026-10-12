# Review — Promotions: percent prefill and list value follow the locale's decimal separator (ut-docs#4097)

**Date:** 2026-10-12 · **Lane:** lane:cloud-41 · **Built by:** Sonnet (Dev subagent) · **Reviewed by:** Opus 5.5 (fresh-context subagent) — card `complexity:easy`.

## What shipped

Promotions page (`internal/pages/promotions_page.go`):

- `newPromotionView(p, locale)`. `renderPromotions` reads `httpx.RequestLocale(r)` once and passes it in.
- For a percent promotion, the edit-input prefill (`ValuePercent`) goes through `httpx.LocalizeMajor`, and the list column (`ValueDisplay`) is that value plus `%`. A de/tr/es/pt/fr till shows and prefills `7,50`; en/ar/fa keep `7.50`. This follows the #4086 `buildTaxOverrideRows` precedent.
- The amount branch is unchanged: the template already wraps its prefill in `majorlocal`.
- There are no new locale keys and no template change. The help topic (`web/help/*/promotions.md`) already says that either separator is accepted, so the manual needs no change.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor (test quality) | `TestPromotionsPageEdit_PercentCommaPrefillRoundTrips` passes with or without the fix: the save path already accepted a comma (#2954). | Kept as a regression guard for editing with a comma; until now only create was covered. It is not counted as a TDD test. |
| 2 | nit | `RequestLocale(r)` was called once per row. | Fixed: it is read once before the loop. |
| 3 | nit | The `%` sign is always a suffix with no space. fr writes `7,50 %` and tr writes `%7,50`. | Accepted. This is pre-existing everywhere percent is shown, and it is a non-goal on the card. |
| 4 | note | The `bpPercent` template func (sale-screen discount label, receipt) and the invoice/print VAT percent are still dot-only. `print_api.go` also has a hardcoded "Total incl. VAT". | Out of scope; filed as ut-docs#4176 (Backlog). |

No blockers. The reviewer checked that every localized value matches `PercentPatternLocal` (`[0-9]+([.,][0-9]{1,2})?`) in every locale: `LocalizeMajor` only swaps the separator and adds no grouping, and values are capped at 0–100 %. Recurring-bug checks (`os.MkdirAll`, cwd-relative paths) don't apply, because the change has no file writes and no file paths.

## TDD re-verification

- **Dev:** `TestPromotionsPageList_ValuePercentIsLocaleAware` failed on the old code with `promotions_page_test.go:445: lang=de: expected value="7,50" and 7,50%, got: …`.
- **Reviewer:** in an isolated worktree, mutated the fix to pass `"en"` to `LocalizeMajor`. The same test failed with the same line. With the fix restored, all 23 `Promotion` tests pass.

## Verified beyond unit tests (driven run)

- Booted a throwaway till (temp data dir, auth off, :8095) and created `PCT750` (7.5 %) through `POST /api/promotions`.
- **Headless Chromium**, looking at each of these:
  - de 1024×600: list `7,50%`, prefill `7,50`.
  - en 1024×600: `7.50%` / `7.50`.
  - fa 1024×600: `dir=rtl`, `7.50%` / `7.50`.
  - de 360×800: phone card shows `7,50%` / `7,50`.
- The input's `checkValidity()` is true in all four. The page has no horizontal overflow.
- Clicking **Save** in de with the prefill unchanged → redirect, and the value is still 750 bp (en shows `7.50`).
- **Screenshots looked at:** de 1024, fa 1024, de 360.
- **Not checked:** the dark theme; real touch hardware.
- **Pre-existing and not caused by this change:** at 1024×600 the promotions table scrolls inside its card, so the customer/edit columns are clipped until you scroll.

## Gate

`gofmt -l .` is clean. `go build ./...`, `go vet ./internal/pages/`, `go test ./internal/pages/ ./internal/httpx/` and the full `go test ./...` all pass. `guard-data-access.sh` and `guard-i18n.sh` pass. The e2e specs `phone-promotions-3653`, `percent-comma-2954` and `osk-decimal-admin-fields-1275` (see the PR).

## Verdict

Safe to merge.
