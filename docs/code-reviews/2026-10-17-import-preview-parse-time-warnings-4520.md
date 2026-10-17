# 2026-10-17 — Import Preview shows every parse-time warning (ut-docs#4520)

## What shipped
- `parseTimeImportWarnings(T, it)` in `internal/pages/import_page.go`
  composes the non-blocking warnings catimport already knows at parse
  time, in display order: unresolved/too-large `.bkp` photo (`ImageIssue`),
  dropped barcode (`BarcodeIssue`, only when no barcode parsed), unread
  tax and takeaway tax rate, renumbered reused item number (`SKUIssue`),
  invalid net quantity.
- **Preview:** a non-skipped row with any of these renders as a warned
  row (⚠ `row-warn`) carrying the commit path's translated text joined
  with "; ". Before this, only `SKUIssue` (#1235) did, and the rest showed
  a plain "ok". Warned rows already go in front of the 200-row display cap.
- **Commit:** the loop appends the same composer's output, so Preview and
  commit can't drift. Commit-only warnings (photo decode/save, barcode
  attach conflict, takeaway-only-no-dine-in, stock) stay in the loop.
- Help: one sentence in the "Fixing problem rows" bullet of
  `web/help/{en,de,tr,fa,ar}/catalog.md`. The structure is unchanged, so
  `guard-help-drift` stays green.
- No new locale keys, no SQL, no schema change.

## Tests
- `internal/pages/import_preview_warnings_test.go`:
  - Preview handler tests per issue type (tax, takeaway tax, net quantity,
    barcode with a narrowed symbology set, `.bkp` dangling image). Each
    asserts the warning sits on the named row's `<tr class="row-warn">`
    and that the Preview writes nothing.
  - A clean-row control.
  - A unit test pinning the composer's order and the parsed-barcode guard.
  - A Preview-vs-commit test asserting the identical warning text,
    reported exactly once.
- Red first: with a nil composer stub, the four Preview subtests, the
  `.bkp` image test and the unit test failed; the clean-row control
  passed.
- `e2e/tests/catalog-import-preview-warnings-4520.spec.ts`: real browser,
  Turkish locale, at 1024×600 and 360×800. Warned rows carry ⚠ and the
  translated text, the warned background differs from a clean row, there
  is no horizontal page overflow, and screenshots are taken.

## Review
Independent review on a different model from the author (author Opus 5.5).
Fable was routed first but refused for lack of usage credits, so the
review ran on Sonnet, per MODEL-ROUTING's fallback. The reviewer ran
build/vet/tests and re-verified the TDD claim: reverting the Preview
branch to the SKUIssue-only logic failed all new Preview tests, and
restoring it made them pass. Verdict: safe to merge.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | In the commit summary, a row with both an unresolved photo and a barcode attach conflict now lists the conflict first | Accepted: same content, nothing duplicated or lost; the two branches stay mutually exclusive with `BarcodeIssue` |
| 2 | minor | Help named only the "no enabled barcode type" and "photo not found" cases; `BarcodeIssueDuplicateItemNumber` and `ImageIssueTooLarge` are surfaced too | Fixed in all five locales |
| 3 | minor | No test tied the commit loop to the composer | Fixed: `TestImport_PreviewAndCommitShowTheSameParseTimeWarning` |
| 4 | nit | Takeaway case asserted only the raw value `xyz` | Fixed: asserts the translated tax text |

## Verified beyond automated tests
- Driven Playwright run, looked at both screenshots. At 1024×600, the
  warned rows are tinted with ⚠ and the full reason wraps inside the
  status column. At 360px, the table is in stacked-card layout: warned
  cards are tinted, and the status (⚠ + reason) sits behind each card's
  existing **More** toggle. That makes colour the only signal on a
  collapsed phone card. The behaviour predates this change and belongs to
  the shared table pattern, so it is filed as ut-docs#4583 rather than
  widening this PR.
- Not checked: real touch hardware (no interaction changed), and
  dark-theme/RTL screenshots (no CSS changed; the row-warn tokens are
  shared and RTL-safe).

## Gate
`gofmt`, `go build ./...`, `golangci-lint` (0 issues), every guard in
ci.yml's build job, and `go test -race ./...`.
`guard-deadcode-baseline` and `guard-shellcheck-version` could not run in
this container: the deadcode binary was built with Go 1.26 for a Go 1.27
module, and shellcheck isn't installed. Neither guard is touched by this
diff, and CI runs both.
