# Review: Gift vouchers search: case folding beyond ASCII, filter kept in the URL (ut-docs#4257)

Date: 2026-10-18 · Branch `fix/4257-voucher-search-unicode-url` · Card complexity: easy
Built by the lane:cloud-41 session. Dev was a Sonnet subagent. The Tester-phase fixes (`base.html` guard, trigger change, e2e spec) were written by the session's Opus 5.5. The planned Fable review was refused (usage limit), so the independent review fell back to a fresh-context Sonnet subagent (MODEL-ROUTING's fallback).

## What shipped
- `internal/data/voucher_repo.go`: `ListVouchers` keeps the status filter in SQL and matches the search text in Go with `strings.ToLower`, the same fold as `FoldItemName`.
  - It scans newest first and stops at `Limit` once enough rows match.
  - `MÜLLER` now finds "Müller". SQLite's `LIKE` folds ASCII only.
  - `%`/`_` still match themselves.
  - No migration and no folded column: a till's voucher table is small.
  - `likeContains` was removed because nothing else used it.
- `internal/pages/vouchers_page.go`: for an htmx request, `GET /ui/vouchers/list` sets `HX-Replace-Url: /vouchers[?q=&status=]`, built from the normalised filter.
  - Back from a voucher and reload both keep the search.
  - The URL is replaced, not pushed, so typing adds no history entries.
  - The export link and the address share `voucherFilterParams`.
- `web/ui/pages/vouchers.html`:
  - The Export row renders only when the list has rows.
  - The form trigger is now `input delay:300ms, submit` (no `change`); see the findings.
- `web/ui/layouts/base.html`: a narrow shared guard. A response that carries `HX-Replace-Url`/`HX-Push-Url` and whose requesting element has left the page is dropped (`htmx:beforeOnLoad` cancelled), so a late response cannot rewrite the address of a page the operator already left. Only the vouchers fragment sends these headers today.
- Help `web/help/{en,de,tr,fa,ar}/vouchers.md`, step 1: search ignores case, accented letters included, and the search and status stay in the page address. `make docs-shots` regenerated the four vouchers screenshots (the empty state no longer shows Export) and the manifest.
- Tests:
  - `TestListVouchers_SearchIsUnicodeCaseInsensitive`
  - `TestVouchersPage_FragmentReplacesURLWithNormalizedFilter`
  - `TestVouchersPage_ExportRowOnlyWhenThereAreVouchers`
  - `TestVouchersPage_UnicodeSearchFromURLKeepsInputValue`
  - e2e `e2e/tests/vouchers-filter-url-4257.spec.ts`

## Findings
- **Major, found by the Tester's driven run and fixed.** The search form's `change` trigger fired one more list request when the search box lost focus.
  - Tapping a voucher does exactly that. That request's outerHTML swap replaced the tapped boosted link mid-navigation.
  - The detail page then rendered without its own URL, so Back broke. This already happened on `main`, where the URL stayed `/vouchers`; the new header made it visible.
  - Fix: drop `change`. The select fires `input`, and `osk.js` and `app.js` dispatch `input`.
  - Defence in depth: the `base.html` guard.
  - Proved red then green by the e2e spec.
- **Minor, fixed.** The help text and the doc comment promised case-insensitivity "in any language". `strings.ToLower` has no locale rules: Turkish dotless ı, final sigma, ß↔ss and NFD-vs-NFC input don't meet. The wording now says "accented letters included". Locale-aware folding is a follow-up card.
- **Minor, accepted.** A search scans every voucher row, with the `sales` join, at most once per 300 ms debounce. That costs a few ms for thousands of rows. Revisit only if a till reaches tens of thousands of vouchers.
- **Nit, accepted.** An in-flight `vouchers-imported` refresh overtaken by a search swap is dropped by the guard. That is harmless because the search response is newer.
- **Nit, accepted.** Export is also hidden on a no-match search. An empty CSV is useless, and the help doesn't promise the button there.
- **Out of scope.** Item 3 of the card (shared table-card labels breaking mid-word at 360px) stays on #3881.

## Verified
- The reviewer re-ran each TDD claim in a separate worktree. Reverting each production change turned its test red with the claimed message, and restoring it turned the test green:
  - the `ListVouchers` body
  - the `HX-Replace-Url` block
  - the template `{{ if }}`
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues) all clean. `go test ./internal/data/` and the full `go test ./internal/pages/` pass.
- Every guard in `ci.yml`'s build job passes locally except two that are tooling-limited here, which CI runs:
  - `guard-deadcode-baseline`: the `deadcode` tool was built with Go 1.26 and the repo needs 1.27. `guard-deadcode-baseline-growth` passes, and this diff only removes code.
  - `guard-shellcheck-version`: no shellcheck binary is installed.
  - The docs-shots guard passes after regeneration.
- e2e: `vouchers-filter-url-4257` and `vouchers-page-4251` pass in Chromium. The 4257 spec checks:
  - an import and a `MÜLLER …` search matching "Müller …"
  - the URL carrying `q`
  - history length unchanged while typing
  - the detail URL, Back and reload keeping the filter
  - the status select filtering and updating the URL
- Looked at screenshots:
  - `/vouchers` at 1024×600 with a no-match search: Export hidden, empty-filtered message.
  - `/vouchers` at 360px in fa with `MÜLLER` typed: RTL correct, no sideways scroll.
  - The regenerated en manual screenshot.
- **Not checked:**
  - real touch hardware (OSK tap → blur ordering was reasoned from `osk.js`, not observed on a Pi)
  - WebKitGTK (select `input` relies on the spec)
  - de rendering in the e2e till (no de pack installed there, #4629)

## Verdict
Safe to merge.

## Deferred
- A Backlog card for locale-aware search folding (Turkish ı/İ, NFC) shared by vouchers, customers and the item search. The customer and item searches still use ASCII-only `LIKE`.
