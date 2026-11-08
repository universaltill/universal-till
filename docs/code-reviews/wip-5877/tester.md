# Tester ut-docs#5877 (shift slip reprint from /shifts History)

## e2e (Playwright, default project, --trace off, preinstalled /opt/pw-browsers/chromium)
`cd e2e && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci` (only @playwright/test 1.61.1 pkg, no browsers), then
`npx playwright test --project=default --trace off tests/shifts-slip-history-5877.spec.ts tests/shifts-close-slip-5841.spec.ts`
-> 6/6 passed (3 new + 3 existing 5841). No test changes needed.

## Mutation checks (all restored; git diff of product files byte-identical to pre-tester diff)
| Mutation | Caught by |
|---|---|
| M1 drop "Reprinted" Meta line | TestBuildShiftSlipDoc_ReprintLineOnlyWhenReprint, ReprintPrintsMarkerAndAuditsReprint, ReprintManagerPINKeepsMarker |
| M2 reprint := true (always reprint) | NoReprintFieldNoMarker, ReprintValueOtherThan1Ignored, ReprintElevationTargetsRow... |
| M2b read reprint via r.FormValue (query accepted) | NOT caught -> added TestShiftCloseSlipPrint_ReprintInQueryIgnored (passes on real code, fails on mutant) |
| M3 handler drops per-row elevation target | ReprintElevationTargetsRowAndCarriesHidden, RowTargetFromDBNotInput |
| M4 PIN retry drops hidden reprint=1 | ReprintElevationTargetsRowAndCarriesHidden |
| M5 audit payload drops reprint | ReprintPrintsMarkerAndAuditsReprint, ReprintManagerPINKeepsMarker |
| M6 page SlipSfx constant | TestShiftsPage_HistoryRowsHavePrintSlipButtonOnClosedOnly |
| T1 template hx-target shared #shift-slip-print-msg | Go page test + e2e tests 1,2 |
| T2 template drops hx-vals reprint | Go page test + e2e test 1 (body lacks reprint=1) |
| T3 template drops data-xr-target | e2e test 2 (dropped connection) only |
| T4 button rendered on open rows | Go page test + e2e test 3 |

Test added by Tester (only change): internal/pages/shift_close_slip_print_test.go `TestShiftCloseSlipPrint_ReprintInQueryIgnored`.

## Driven run
Built binary, throwaway UT_DATA_DIR (seed_demo), UT_AUTH=off, 127.0.0.1:8191 (same env as run-till.sh). Opened+closed 2 shifts (one with +2.50 variance), opened a 3rd; clicked a row's Print slip. Killed till, data dir removed.
- shifts-1024.png (1024x600, dsf 1, light, en): History has 7th unlabeled column; "Print slip" buttons 113x46 at end of each closed row; clicked row shows red "⚠ Print failed. Check the receipt printer in Settings → Receipt printer, then tap Print shift slip again." wrapped under its own button (max 14rem), second row untouched; no horizontal scroll (scrollWidth 1024 = clientWidth); no page errors.
- shifts-rtl.png (ar, dir=rtl, 1024x600): table mirrored, Print slip column at the inline-end (left) edge, error wraps under the clicked (second) row's button only, open shift row ("مفتوحة" tag) has no button, no horizontal scroll. New keys show in English (ar/fa packs lack them - expected, Weblate).
Not looked at: dark theme, de/fa longest strings, phone/portrait, kiosk Pi, real printer success path (no printer; success text covered by 5841 spec + Go tests), manager-PIN prompt in browser (Go-only).

## Findings
- UX nit (not fixed): row failure text says "tap Print shift slip again" but the row button is labelled "Print slip". Consider a row-specific failure key or aligning the label.
- "Reprinted <stamp>" uses x.GeneratedAt (built at print time) - fine, matches the design.

## Gates
gofmt clean; go vet ./internal/pages ok; guards data-access, i18n, i18n-meta, rtl-logical-css, no-showmodal, help-topics, fixed-dates: pass.
go test ./internal/pages/... -race: subpackages catalogsync, common (416s), itemsnav, settingsnav ok. The root package ran with -timeout 40m and hit the timeout: 0 DATA RACE warnings, 0 --- FAIL lines, and the test running at that moment was an unrelated one (TestFiscalGate_RefundFailingTSEBlockedWithoutOverride, 0s). The Makefile's own `test-race-pages` gives this package 60m; on this 4-core box there was no time for that rerun. Touched tests under race (`-run 'Shift|Slip|XReport'`): ok (72s). Full log: race-pages.txt.
