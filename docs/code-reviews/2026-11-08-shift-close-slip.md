# 2026-11-08 — Print a "Shift close" slip from the close result (ut-docs#5841)

**Card:** universaltill/ut-docs#5841 (X report 4b, split from #5798; spec
`ut-docs/reference/design-5780-x-report.md` §9 row 3, Q6).
**Lane:** `lane:cloud-54`. Built by Sonnet; reviewed by Opus 5.5 (different model).

## What shipped

- `POST /api/shifts/{id}/close-slip/print` (`internal/pages/shift_close_slip_print.go`),
  registered beside the X print. A shift that is open, unknown or has an empty id gets
  404 `shift_not_closed` before any PIN is asked. After that it uses the same `reports`
  gate and manager-PIN elevation as the X print. If no printer is set or the print fails,
  it returns an inline 502 and writes no audit row. A successful print writes one
  `shift_slip_printed` audit row (`entity_type=shift`, `{register_id}`, approver when
  elevated). The route is demo-denied.
- `POSRepo.ShiftCloseSlip` (`internal/data/x_report_repo.go`) returns the closed window's
  `ShiftXReport` without the **at-close** skim. `XAdjustment.AtClose` uses the same
  marker as the Z cash reconciliation, so the Cash block sums to the stored
  `expected_cash`. The close block prints the stored Expected, Counted, Variance,
  Skim to safe and New float, and never recomputes them. A legacy close with a NULL
  `new_float` falls back to Counted, so no skim is printed.
- `buildShiftSlipDoc` prints the title "SHIFT CLOSE", the X figures and the drawer count
  at close. It has no Z number, series, barcode, QR or logo, and no drawer kick.
  `buildXReportDoc` was split into shared helpers. A golden test written before the
  split proves the X paper is byte-identical.
- The close result HTML has a "Print shift slip" button and an `aria-live` message, and
  `XR_TARGETS` gains `shift-slip-print-msg`. The JSON close response gains `shift_id`.
- 10 new `en.json` keys with `en.meta.json` metadata. Help: a "Printing a shift slip"
  section in `web/help/en/reports.md`. ut-docs `architecture/end-of-day-report.md` has a
  matching note (ut-docs branch `docs/5841-shift-close-slip`).

## Findings (Opus review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | major | `web/help/img/manifest.json` was not refreshed after the help/pages/template edits. The docs-shots guard only warns on PRs, so `main` would have gone red after merge. | **Fixed:** ran `make docs-shots` and committed only the manifest (`Docs-Shots-Unchanged: true`, as in c557db1). The PNG diffs were sandbox Chromium/font noise; the close result is not screenshotted. |
| m1 | minor | No test covered a **mid-shift** skim. A mutation that dropped every skim survived. | **Fixed:** `TestShiftCloseSlip_KeepsMidShiftSkim`. With that mutation applied, the new test fails ("want payout + top-up + mid-shift skim"); with the code restored, it passes. |
| m2 | minor | A shift closed by code that predates the `at_close` marker would show its close-time skim in both blocks. | Accepted: it is the same rule as the Z reconciliation (#1146), and no real shops exist yet. |
| n | nit | `ShiftCloseSlip` reads the shift row a second time; the golden path ignores the `os.Getwd` error; "not closed" also covers an unknown id; the live regions are nested. | Accepted; the X print shares the last two patterns. |

Checked and fine: money signs and sums, 404 ordering (it reveals only "closed or not"
to a signed-in user, the same as the X print), audit only after success, HTML escaping of
the shift id, no cwd-relative writes, every i18n key has meta, help prose is accurate.

## Rebase onto main (after review)

`main` gained ut-docs#5857 (bd4d623), which dropped `xPaperText` because
`internal/print` now strips control runes on every paper. The slip followed:
names are passed through raw, and the doc-level control-byte test became
`TestBuildShiftSlipDoc_ControlRunesInFreeTextNeverReachPrinter`, a byte test
through `print.Render`/`RenderText` modelled on #5857's X test. The docs-shots
manifest was regenerated with `write-manifest.js`.

## Extra verification

- Tester: 19 mutations, every one red. Full `go test ./...` green. A real driven run on a
  throwaway till with a fake TCP ESC/POS printer: close with counted 104.50 and
  skim 50.00 printed Expected £101.20, Counted £104.50, Variance +£3.30,
  Skim -£50.00 and New float £54.50, all equal to the stored row. One audit row per
  print. Printer off gave an inline 502 and no row. Screenshots at 1024x600 and 360px
  (en) and in ar (RTL). New e2e spec `e2e/tests/shifts-close-slip-5841.spec.ts`.
- UX gate: PASS. The button is 153x51 px, visible without scrolling at 1024x600, and
  mirrors in RTL. No modal; status, lock and exit stay reachable.
- Reviewer re-verified the at-close exclusion, printer-off and demo-route TDD claims, and
  the golden test against origin/main's code.

Pre-flight: help topic updated (web/help/en/reports.md); docs-shots manifest updated
(no screenshot shows the close result); reference/architecture updated
(ut-docs architecture/end-of-day-report.md); en.meta.json updated for the 10 keys;
README n/a (no claim changed); plan/price, ADR, plugin taxonomy n/a.

## Deferred (Backlog cards)

- Reprint a shift slip from Recent Shifts history rows (the slip is only on the close result).
- `/shifts` scrolls sideways at 360px because of the Recent-shifts table (predates this
  card): already covered by ut-docs#5782 (universal-till#2426), no new card.

**Verdict:** approve.
