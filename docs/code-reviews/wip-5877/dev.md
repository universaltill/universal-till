# Dev note ut-docs#5877
Files: internal/pages/shift_close_slip_print.go (reprint flag, shiftSlipRowSuffix, row target+hidden, reprint line, audit reprint=true), shifts_page.go (shiftHistoryRow), web/ui/pages/shifts.html (7th col, row button, isXRTarget), web/public/app.css, web/locales/en.json, web/i18n-meta/en.meta.json (3 keys), web/help/en/reports.md, e2e/tests/shifts-slip-history-5877.spec.ts (new, not run), tests in shift_close_slip_doc_test.go / shift_close_slip_print_test.go / shifts_page_test.go.
Red first: new tests failed to compile (buildShiftSlipDoc 4-arg, shiftSlipRowSuffix undefined); page-test fixture later needed users row (my test bug).
Gates: gofmt clean; go build ok; guard-i18n, i18n-meta, rtl, no-showmodal, help-topics, fixed-dates all pass; targeted Shift|Slip tests pass; full ./internal/pages result: see handback.
Deviations: none of substance; path uses `.ID | urlquery` per design (ids are uuids). No review record written (orchestrator/reviewer).
