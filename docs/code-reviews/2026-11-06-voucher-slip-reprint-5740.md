# Review — "Print voucher slip" on the voucher page (ut-docs#5740)

Date: 2026-11-06 · Branch: `feat/5740-voucher-slip-reprint` (reviewed as `review/5740`, WIP snapshot f94d1d6)
Built by Sonnet; reviewed independently by Opus 5.5 (different model, fresh context, own worktree).

## What shipped
- `POST /vouchers/{id}/print-slip` (`internal/pages/vouchers_page.go`): reprints ONE gift-voucher slip
  on this till's receipt printer — no receipt, no drawer kick, whatever `printer.voucher_slip` says.
  Same gate as `/adjust` (`canPerform "settings"` + a named session user unless `UT_AUTH=off`); denied in
  demo mode (`demoDeniedRoutes`). Printability (status `active`, balance > 0) is judged on the main
  till's row when an additional till reaches it, else the local row (`voucherForPage`, extracted from the
  detail handler). Outcome is an inline HTMX fragment: 200 done / 404 / 409 not printable / 409 no
  printer / 451 ADR-0124 refused / 502 print failed.
- Audit `voucher/<id>/slip_printed` `{ok}` for every attempt that reached the printer, `{ok:false,
  refused:"shadow_customer_documents"}` when ADR-0124 suppression refuses it.
- `printVoucherSlipIn` / `voucherSlipFor` (`internal/pages/voucher_slip_reprint.go`): slip built from the
  current receipt design (store name, header, logo, charset) in the shop's language, plus a centred
  "Reprinted <date>" line; `issuedVoucherBlock` extracted from `buildReceiptDocIn` (byte-identical for the
  receipt — same locale/charset/keys).
- `print.PrintVoucherSlip` (one ESC/POS job with one feed+cut, or one `lp` job; nothing when off);
  `VoucherSlip.Note` (empty = #4247 slip unchanged, pinned by `TestRenderVoucherSlip_NoNoteUnchanged`).
- Template: button (inline form, `hx-disabled-elt`, busy indicator, `aria-live` slot — no dialog), the
  "original value" note when balance ≠ original, a "Receipt printer" link instead when no printer.
- i18n: 7 keys + meta; help `web/help/en/vouchers.md` new section, `printing.md` cross-ref; README
  voucher paragraph; docs-shots `manifest.json` hash refresh. ut-docs `architecture/receipt-printing.md`
  Scope bullet in a separate ut-docs PR (branch `docs/5740-voucher-slip-reprint`).

## Findings
| # | Sev | Where | Outcome |
|---|---|---|---|
| 1 | medium | `web/i18n-meta/en.meta.json` `receipt.vouchers.reprinted` set `review: standard` with a `review_exception` (the repo's first). The key is in the legal `receipt` namespace and prints on a document handed to the customer; a "reprint/copy" marker is exactly the wording some regimes regulate. ADR-0160 §4 is deny-by-default and a human approval only delays translations — an exception isn't justified | fixed: `review: legal`, exception removed (guard-i18n-meta green) |
| 2 | low | `vouchers_page.go` print-slip handler wrote both audit rows with `r.Context()`: an operator leaving the page mid-print (slow/stuck printer, up to the 10 s print timeout) cancels it, the insert fails silently and a slip that may already be on paper goes unaudited. The receipt path uses `recordPrintFailureCtx` for this reason | fixed: audit inserts use `context.WithoutCancel(r.Context())` + 5 s timeout; new `TestVoucherSlipReprint_AuditSurvivesClientCancel` (fake `lp` cancelled mid-job) failed before, passes after |
| 3 | info | Under ADR-0124 suppression the page still shows the button (`printerConfig` stays enabled); pressing it returns the 451 "not available in shadow mode" line and prints nothing | accepted — same as the journal's Print receipt; refusal is server-side at the choke point |
| 4 | info | The "original value" note compares the shown (local) row's balance, printability uses the judged (main-till) row; on a lagging additional till the note can be stale | accepted — informational text, the main-till balance on the slip is #5744 item 8 |
| 5 | info | No idempotency on repeated presses | accepted — a manager reprint is deliberate; button disabled in flight, Tester's double-tap produced exactly 1 job |

Checked and clean: no raw SQL outside `internal/data` (tests only); amounts formatted via
`FormatMoneyPrint` from int64 minor units at the print boundary; no new file writes (no `MkdirAll`
concern) and no new cwd-relative paths (logo via existing `receiptLogoPath`); template output escaped
(`html/template`, `url.PathEscape` on hrefs, `html.EscapeString` in fragments); no `showModal`; logical
CSS only (`margin-block-start`); checkout untouched (separate page, 10 s print timeout, failure inline).

## Extra verification
- `go build ./...`, `go vet` (pages/print/auth), `gofmt -l` clean; `golangci-lint run ./...` 0 issues.
- `go test -race` ./internal/print, ./internal/auth ok; ./internal/pages full package with `-race
  -timeout 40m` (exceeds Go's default 10 min under race on this box — not a hang) — see verdict line; all
  slip/voucher/demo tests `-race` ok after the fixes.
- All `ci.yml` build-job guards run: green, except `guard-deadcode-baseline` (local deadcode built with
  go1.26 vs go.mod 1.27.1) and `guard-shellcheck-version` (no shellcheck binary) — environment only, no
  shell or dead-code surface in this diff.
- TDD claims re-verified personally (revert → fail → restore → pass): askPrimary=false in the POST →
  `ReplicaJudgesMainTillRow` fails (200 on a main-till-void voucher); ADR-0124 check removed from
  `printVoucherSlipIn` → `Suppressed` fails (200, printed); session-id gate removed → `Forbidden` fails.
- Tester (tester.md): 8/8 production mutations caught. Driven run on the real binary + Chromium with a
  FIFO device printer: kiosk 1024×600 and windowed (51 px button above the fold, status bar not inert),
  360 px phone (no sideways scroll), part-spent note, 3 s slow printer + double tap → exactly 1 job and 1
  audit row, success / print failure / no printer / void / used-up / voided-in-another-tab (409, nothing
  printed), de and fa-RTL layout, keyboard, a11y roles; slip bytes: one cut, no drawer kick, no receipt
  text, "Reprinted" line.

Pre-flight: help topic `web/help/en/vouchers.md` + `printing.md` updated; `make docs-shots` — manifest
hash updated (PNG churn from a newer local Chromium reverted, guard is hash-based); `reference/`
→ ut-docs `architecture/receipt-printing.md` updated in a separate ut-docs PR; README updated; new
`en.json` keys → `en.meta.json` updated; plan/price, platform claim, ADR, plugin type, my./admin UI n/a
(none touched); follow-ups → cards below (#5744, #5748, #5749). After the review fix to `vouchers_page.go` the manifest surface hash was refreshed with `e2e/tests-docs/write-manifest.js` (backend-only change, no pixel change).

## Verdict
Approve with the two fixes above applied.

## Deferred
- #5744 items (balance row on the slip — item 8 —, top-up slips, other non-goals listed there).
- Screen-vs-print date format split (screen DD/MM, paper MM/DD via `FormatDateLatin` "en"; the receipt
  has it too) — Tester observation, filed as ut-docs#5748.
- Focus lands on `<body>` after an HTMX response replaces the in-flight state; restore focus to the
  `hx-disabled-elt` element in `app.js` (shared, UX A6) — filed as ut-docs#5749.
