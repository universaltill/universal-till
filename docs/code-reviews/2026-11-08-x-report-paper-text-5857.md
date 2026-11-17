# Review — X report: drop xPaperText, byte-level control-rune test (ut-docs#5857)

**Date:** 2026-11-08 · **Branch:** `fix/5857-x-report-drop-xpapertext`
**Author model:** Sonnet (dev subagent) · **Tester + Reviewer:** Opus 5.5 (independent subagent, separate worktree)

Pre-flight: web/help n/a (no user-visible change; paper output identical apart from the stricter #5847 cleaning of invalid UTF-8, U+2028/2029 and bidi overrides) · reference/ n/a (no doc names xPaperText; behaviour already documented by #5847 in ut-docs `architecture/receipt-printing.md`) · README n/a · en.json/i18n-meta n/a (no keys) · mirrors/ADR n/a · review record: this file · follow-ups: none.

## What shipped
- `internal/pages/shift_x_report_print.go`: removed `xPaperText` (control rune → space, X report only) and its three call sites (register name, cashier name, adjustment reason), plus the now-unused `unicode` import. Since #5847 (universal-till#2443) `print.encodeText` and the plain-text renderers clean every paper, so the helper was redundant.
- `internal/pages/shift_x_report_doc_test.go`: removed `TestBuildXReportDoc_FreeTextHasNoControlBytes`, which asserted the `print.Doc` strings themselves held no control runes — no longer true by design (cleaning happens at render time).
- New `internal/pages/shift_x_report_print_control_5857_test.go`, in the #5847 EOD-test style: the full drawer kick `ESC p 0 25 0xFA` plus a forged `\nTOTAL 0.00` planted in register name, cashier name and an adjustment reason. For each charset (`""`, utf8, ascii, cp858, win1250): `print.Render` carries no `ESC p` and the same newline count as a clean twin; `print.RenderText` (what `PrintDoc` sends to `lp` in system mode) carries no control rune but its own `\n` and the same newline count.

## Review findings
- **R1 (low, test strength) — fixed.** The text-path check only looked for ESC; a regression letting `\f` (page eject on lp), NUL or `\x19` through would pass. Widened to any `unicode.IsControl` rune except `\n`.
- **R2 (info) — accepted.** On the ascii charset the ESC/POS checks cannot fail (the ascii arm already maps < 0x20 to `?`); same limit as the #5847 EOD test. The text path still bites for ascii.
- **R3 (info) — accepted.** Text-path checks repeat per charset although `RenderText` ignores it; harmless.

### Path coverage (no finding)
`buildXReportDoc`'s only caller is the print handler → `print.PrintDoc` → `PrintDocWithSlips`: system mode → `RenderText` (calls `paperDoc` first), network/USB → `Render` (every text line through `encodeText` → `paperText`), off → nothing. The on-screen X report partial is html/template with auto-escaping. `footerRow`/`clip` measure the dirty text before cleaning; safe, as `paperText` preserves display width.

## Verification
- Dev TDD: new test passed on the old code (characterisation); old doc-level test failed once `xPaperText` was removed (`control byte 0xa on paper in ... Front\nTill`) and was replaced.
- Reviewer re-verified in a throwaway worktree: `paperText` made a no-op → `drawer-kick bytes reached the printer x3`, `36 newlines, clean report has 33`, text-path failures on every charset (ascii: text path only). `paperDoc` removed from `RenderText` only → all 5 charsets fail on `control rune '\x1b' reached the plain-text report` while the ESC/POS checks stay green. Restored → PASS.
- Gates: gofmt clean, `go build ./...`, `go vet` pages/print, `go test ./internal/print -race`, `go test ./internal/pages -timeout 20m` (pass, 364s), X-report subset with `-race`, golangci-lint 0 issues, guard-fixed-dates, guard-data-access, guard-i18n, guard-core-neutral, guard-mail-addresses, guard-kiosk-engine and others pass. `guard-deadcode-baseline.sh` could not run in the sandbox (deadcode built with go1.26 vs go1.27 module); the diff only removes a function, CI runs it.

## Verdict
Approve (one low test-strength finding fixed in review).
