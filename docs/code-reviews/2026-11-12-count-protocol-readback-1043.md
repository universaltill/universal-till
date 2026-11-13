# Review: closed shift's denomination count shown again (ut-docs#1043)

Date: 2026-11-12 · Lane: lane:cloud-41 · Complexity: medium · Built by Sonnet, reviewed by Opus 5.5

## What shipped

Gap 2 of ut-docs#1043. Gap 1, the currency-aware grid, was already on `main`.

Before this change, `shifts.count_protocol` was written at shift close and never read back. Now:

- **`pos.ReadCountProtocol`** is a pure parser. It reads only canonical positive integer minor-unit keys. Bad keys, bad values, overflow and malformed JSON are dropped and never echoed. Lines are ordered highest denomination first.
- **`POSRepo.ShiftCount`** is read-only. An unknown id gives `sql.ErrNoRows`; an open shift gives `ErrShiftNotClosed`.
- **`GET /ui/shifts/{id}/count`** returns an HTML fragment, or `{data,error}` JSON on `Accept: application/json`.
  - Responses: 400 for an oversized id, 404 for an unknown or open shift.
  - It writes nothing. It has no PIN: cashiers already see Counted, Expected and Variance in history (design D1).
  - It gets a demo-mode allow entry.
- **`/shifts` history:** each closed row gets a "Show count" button, which loads into one shared `#shift-count` panel under the table.
  - The panel scrolls into view and focus moves to its heading. Hide returns focus.
  - Errors and offline failures show inline, through `XR_TARGETS`.
- 14 `shifts.count_detail.*` keys in `en.json` and `en.meta.json`.
- One CSS rule, using logical properties.
- Help: a "Seeing the count again" paragraph in `web/help/en/reports.md`.

## Findings (independent Opus review of 796553c)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The mismatch note showed on a partial read. `Total` leaves out the dropped entries, so it could say the count differs when it did not. | **Fixed**: `!rd.Partial` added to the condition, plus a test. The test is red without the fix and green with it (verified by the orchestrator). |
| 2 | Low | `TestShiftCountDetail_RTLNoPhysicalStyles` could never fail (the fragment has no `style=`). | **Fixed**: test removed. `guard-rtl-logical-css.sh` is the real check. |
| 3 | Low | Every row's button had the same accessible name. | **Fixed**: `aria-describedby` points at the row's closed-at cell. |
| 4 | Nit | A duplicate JSON key: the last value wins, with no flag. | **Accepted**: unreachable through the write gate. |
| 5 | Nit | The cashier test bypasses the auth middleware. | **Fixed**: a comment states its scope (handler has no role/PIN check; `/ui/` stays behind the session, `auth.isExempt` unchanged). |
| 6 | Nit (Tester) | The `tfoot` divider spans only the label cell. This comes from the shared `.x-report-num` style. | **Accepted**: cosmetic, same as the X report. |

The reviewer re-verified a TDD claim: with the bad-key drop removed, 8 `TestReadCountProtocol` cases fail.

The reviewer also checked these and found them fine:
- XSS: only numbers are rendered.
- pieces×denomination overflow: guarded.
- JPY with 0 decimals.
- id cap and parameterised SQL.
- htmx `empty` / `focus` steps.

## Verification beyond unit tests (Tester)

- **Mutations:** each turned the named test or the e2e red:
  - parser order reversed;
  - bad-key drop removed;
  - open-shift refusal removed;
  - button removed (e2e 4/4 red).
- **Go tests:**
  - `go test ./...`: 108 packages ok.
  - `internal/plugins` hit the 10-minute timeout on the 4-core sandbox. This change touches nothing there; CI confirms it.
  - `-race` on pos/pages/data `Count|Shift`: ok.
- **Driven run:** a real till, closing a shift with 4 denominations.
  - Lines are listed highest first; the total and Counted both read £76.40.
  - Focus lands on the heading. No console errors.
  - The empty case shows "No denomination count was recorded".
  - At 360×640 nothing scrolls sideways and the button is 138×51.
  - ar renders right-to-left.
  - Screenshots at 1024×600 (en, ar) and 360×640 were looked at.
- **e2e:** `e2e/tests/shifts-count-detail-1043.spec.ts` passes 4/4, and passes together with `shifts-close-slip-5841`.

## Gates

On the final tree:
- gofmt clean, `go build ./...` and `go vet` ok.
- golangci-lint: 0 issues.
- Guards all pass: i18n, i18n-meta, rtl-logical-css, data-access, help-topics, no-showmodal, no-native-confirm, fixed-dates, wip-commits.

Pre-flight: help topic `web/help/en/reports.md` updated; docs-shots n/a (no existing shot covers `/shifts` history); `reference/` n/a (no ut-docs page describes the count read side); README n/a; en.json keys → en.meta.json updated (English only, ADR-0160); ADR / plan / plugin rows n/a; review record this file.

## Deferred

- After a shift open/close swaps the region at 360px, the history table overflows. This existed before and is already ut-docs#5782 item 3.
- `ValidCountProtocol` accepts non-numeric keys on write. The read side tolerates them. Write-side hardening gets its own card.

Verdict: **approve** (after fixes 1, 2, 3, 5).
