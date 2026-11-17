# Review — no-sale drawer opens on the shift (ut-docs#5755)

**Date:** 2026-11-07 · **Branch:** `feat/5755-shift-no-sale-count`
**Author model:** Sonnet (dev subagent) · **Reviewer model:** Opus 5.5 (independent subagent)

## What shipped

- `POSRepo.NoSaleForShift(ctx, shiftID)` in `internal/data/no_sale_repo.go`
  returns one shift's no-sale total and its per-cashier breakdown. It reuses
  `noSaleOperators` from #5753, so the grouping, order and name fallback are
  the same.
  - The window uses `SumCashPaymentsForShift`'s rule. The event must be on
    the same register, at or after `opened_at` and, once the shift is
    closed, at or before `closed_at`. Both bounds are inclusive. An open
    shift has no upper bound.
  - Both sides of each comparison go through `datetime(...)`, so a legacy
    `YYYY-MM-DD HH:MM:SS` `opened_at` compares correctly against an
    RFC 3339 `created_at`.
  - An event with no `register_id` matches no shift, but it still counts on
    the EOD.
- `/shifts` Current Shift card:
  - A "No sale (drawer opens)" row shows the total, including 0.
  - When the count is above 0, a Cashier | Opens table lists each cashier
    ("Unattributed" when no actor was recorded).
  - If the query fails, the row is left out. The page never shows a false 0
    and never fails.
- `POST /api/shifts/close`:
  - JSON gains `no_sale_count` and `no_sale_operators`, both `omitempty`.
  - The HTML success message gains one `<br>`-separated line, only when the
    count is above 0. The line is escaped.
  - The read is best-effort and runs after the close is committed. A
    failure is logged and the line is left out.
  - The count is never added to expected cash, variance, skim, new float or
    the close audit payload.
- One new key, `shifts.close_no_sale_opens`, with its `en.meta.json` entry.
  Two existing `reports.eod.*` key descriptions now mention their reuse on
  the Shifts page. A help paragraph was added to `web/help/en/reports.md`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor (Tester O1) | In ar and fa, the counts rendered with Latin digits (`{{ .NoSaleCount }}` / `{{ .Count }}` in `shifts.html`, `%d` in the close line), while the money and dates on the same card use the locale's digits. | **Fixed (TDD):** the template now uses `{{ qty … }}`. The close line formats the count with `httpx.FormatQty(float64(n), locale)` into a `%s` placeholder. The key is new in this branch, so changing its verb needs no new key. Two new tests, `TestShiftsPage_NoSaleCountsUseLocaleDigits` (ar: ٣ total, Alice ٢) and `TestCloseShift_HTMLNoSaleLineUsesLocaleDigits` (fa: ۳), failed before the fix with Latin digits and pass after it. |
| 2 | nit | No-sale events record the **till's own** register (`tillRegisterIDBestEffort`). A shift opened from the picker on a different register therefore sees none of this till's opens. | **Accepted:** this matches the specified register-match rule and the way cash takings are attributed. The picker preselects the till's own register (#940). |
| 3 | nit | `datetime()` truncates to whole seconds. If a shift closes and the next one opens in the same second on the same register, an open stamped in that second could count on both shifts. | **Accepted:** this needs a close and a reopen within one second. The value is informational only and involves no money. `SumCashPaymentsForShift` has the same inclusive-close rule. |
| 4 | nit | On the open-shift card, the total row and the breakdown come from one query, so they always agree (unlike #5753's two reads). | **No change needed.** |

Checked and found clean:
- **SQL window.** `datetime()` is applied to both sides. Both bounds are inclusive. A NULL or `''` `closed_at` leaves the window unbounded. The register must match exactly, so a NULL `register_id` never matches (`=` against NULL is never true). An unknown shift id returns `(0, nil, nil)`. The data test covers before, at-open, at-close, after, other register, no register, a legacy-format `opened_at` and a second shift.
- **Per-cashier attribution.** It uses the #5753 grouping. The "no actor" row renders as Unattributed.
- **Error paths.** A failed query never blocks the close or the page. Each path has a mutation-proven test (Tester M7, M9 to M12).
- **HTML escaping.** The close line is `html.EscapeString`'d. The card's names go through `html/template`.
- **JSON.** Tags are snake_case. `NoSaleOperator` already carries `cashier_id`, `display_name` and `count`.
- **Repository pattern.** Raw SQL appears only in `internal/data`.
- **Not money.** The counts are `int`. The tests assert that expected cash and variance are unchanged and that the audit payload carries no no-sale data.
- **CSS.** Only logical properties are used (`text-align:end`, `margin-block-start`).
- **Dates.** Every test time derives from `time.Now()` or `iwAnchor()`.
- **The two recurring bugs.** There are no file writes and no cwd-relative paths, so neither applies.
- **Test data.** No real shop or person names appear.

## Extra verification

- **Tester's TDD re-verification.** With production reverted, the data
  tests fail to compile and 5 of the 9 page tests fail. The other 4 assert
  the unchanged or absent behaviour.
- **Tester's mutation run.** All 12 mutations were killed: text compare,
  `<=`→`<`, `>=`→`>`, the register match dropped, the close bound dropped,
  the error wrap dropped, a false 0 on error, the breakdown gated on OK, the
  line shown at 0, a 500 on error, `omitempty` dropped, and the total row
  made unconditional. Conclusion: no test passes falsely.
- **Tester's `-race` runs.** The card's tests ran clean under `-race`. Two
  failures are pre-existing and unrelated: the pages package's 10-minute
  default timeout, and a 100 ms timing test in `stock_upload_repo`.
- **Tester's real driven run** (auth on, emulated ESC/POS sink that
  recorded 6 drawer kicks). It used real `POST /api/pos/no-sale` calls,
  including Bob with a manager PIN, plus one Unattributed open inserted
  directly. Closes went through the UI form.
  - Screenshots are in `scratchpad/5755/shots/`:
    - the 0 state at 1024×600 and 360×740
    - 4 opens in en, ar and fa, at 1024 and 360
    - kiosk 1024×600 with an emulated touch scroll and the OSK
    - close results with 4, 1 and 0 opens
  - No horizontal scroll on load at 360. RTL mirrors correctly.
  - The e2e shifts specs passed (6 passed).
- **Reviewer re-verified the new locale-digit tests**: red before the fix
  (Latin `3`), green after.
- **Gates on the working tree after the fix.**
  - `gofmt -l .` printed nothing. `go build ./...` and
    `go vet ./internal/data/ ./internal/pages/` passed.
  - `golangci-lint run ./internal/pages/ ./internal/data/` reported 0 issues.
  - `go test ./internal/data/` passed. `go test ./internal/pages/ -run 'Shift|NoSale'` passed.
  - The full `go test ./internal/pages/ -timeout 30m` passed (ok, 284s).
  - These guards returned rc 0: data-access, i18n, i18n-meta,
    rtl-logical-css, help-topics, fixed-dates and page-http-error.

Pre-flight:
- Till user-visible behaviour → the `web/help/en/reports.md` topic was updated (the shift-close section).
- docs-shots are n/a because the topic has no shifts screenshot.
- `reference/` is n/a because no reference page lists the Current Shift card fields or the close-response fields.
- README is n/a because it is not stale.
- New `en.json` key → `en.meta.json` updated (English only, ADR-0160). The two reused keys' descriptions were extended.
- The plan, platform, ADR and plugin rows are n/a.
- This file is the review record.
- Follow-ups are listed under Deferred below.

## Verdict

Approve. Safe to merge once the finding-1 fix is committed with the WIP.

## Deferred (Backlog cards)

- A real X report, a mid-shift read that includes the no-sale count.
- A no-sale column in the Shifts History table.
- The Current Shift "Cashier" row shows the raw cashier id, not the name. This is pre-existing.
- At 1024×600 the close form sits below History, so the operator has to
  scroll to reach Close and to see its result. This is pre-existing.
- At 360px, after an HTMX region refresh (shift open or close), the History
  table overflows to 608px because the responsive-table transform isn't
  re-applied. This is pre-existing (Tester O2).
- The archived EOD detail's no-sale table (`reports_tab_eod.html`, #5753)
  still prints raw Latin integers. It should get the same `{{ qty }}` fix as
  finding 1.
