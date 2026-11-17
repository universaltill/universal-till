# Review: shop text and issue/top-up date on gift voucher slips (ut-docs#6179)

**Change.**
- New setting `receipt.voucher_slip_text`, a textarea under Footer in Settings → Receipt designer (`web/ui/pages/receipt_designer.html`). It allows up to 4 lines and 160 runes; line feeds are not counted and an inner blank line counts as a line.
- One write-side rule, `normalizeVoucherSlipText` (`internal/pages/voucher_slip_text.go`), does the following: forces valid UTF-8; converts CR/CRLF/U+2028/U+2029 to LF; turns tab into a space; drops C0/DEL/C1 and U+202A–U+202E; trims each line; drops blank lines at the edges. It refuses over-long text and never truncates.
- The rule applies on all three write paths. Each refuses with 422 and stores nothing:
  - designer Save (`receipt_designer.go`), checked before any value is built, so an additional till never contacts the main till;
  - the main till applying a forwarded save (`sync_settings.go`);
  - the raw `/api/settings/upsert` (`settings_page.go`), checked before the elevation gate.
- The preview uses a lenient form of the same rule (`lenientVoucherSlipText`) and shows a sample slip under the sample receipt only while text is set.
- Every slip build path gets an "Issued <date>" line, or "Topped up <date>" on a top-up slip. The date comes from the stored `created_at`, converted to local time, in the shop's date order with Latin digits. The paths are:
  - tender and journal reprint (`buildReceiptDocIn` → `voucherSlips`);
  - voucher-page reprint (`voucherSlipFor`), which prints no Issued line for an imported voucher with no issuing sale, and keeps "Reprinted" after the date;
  - browser tender (`receiptBrowserSlips`, which reads the dates back from the committed rows);
  - browser voucher-page (`browserVoucherSlipFor`).
- `data.VoucherTopUpsInSale` now returns the top-up row's `created_at`. It is a select column only; there is no migration.
- `print.VoucherSlip` gained `Date` and `Text`, rendered in this order: code, date, Note, blank line, centred word-wrapped text, blank line, rule. `print.Doc.VoucherSlipText` and `VoucherBlock.SlipDate` are ignored by `Render`/`RenderText`, so the receipt itself is unchanged.
- `osk.js`: a textarea with `data-osk-enter="newline"` types a line break on ↵ instead of submitting.
- New keys: 5 in `en.json` + `en.meta.json`. `receipt.vouchers.issued_on` and `receipt.vouchers.topped_up_on` are `review: legal` with `max_length` 42. The three `designer.*` keys are `standard`.
- Help updated: `designer.md`, `printing.md` and `vouchers.md`. `designer.png` was regenerated in en/ar/fa/tr.
- ut-docs branch `docs/6179-voucher-slip-text` updates `architecture/receipt-designer.md` and `architecture/receipt-printing.md`.

**Pipeline.** BA, Architect, UX and Dev were Sonnet. Tester was an independent Sonnet subagent: 27 mutations, 2 survivors, 125 e2e passed, and a live ESC/POS byte capture. Reviewer was an independent Opus 5.5 subagent.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | low (test gap, from Tester) | ESC/POS word-wrap of the shop text (`print/voucher_slip.go` `RenderVoucherSlip`) was never asserted. Replacing `wrapWords(l, Width)` with `[]string{l}` left every test green. | **Fixed**: added `TestRenderVoucherSlip_TextWrapsESC`. It checks that a 59-column sentence becomes exactly 8 words + 4 words, then the next line, with every row ≤ `Width` inside the centred block. Red under the mutation (worktree), green on HEAD. |
| R2 | low (test gap, from Tester) | Date-before-Note order was asserted only for ESC/POS. Swapping the two in `RenderVoucherSlipText` survived. | **Fixed**: added `TestRenderVoucherSlipText_DateBeforeNote`, which checks this order: code < date, Note directly after date, text last. Red with the swap, green on HEAD. |
| R3 | info | The sanitiser drops bidi embeddings/overrides (U+202A–202E) but not the isolates (U+2066–2069). | Accepted. Isolates can't reorder text outside themselves, the browser slip renders each line `dir="auto"` in html/template, and on thermal paper they are unprintable (encoded away or `?`). |
| R4 | info | `voucher_slip_text.go` writes U+2028/U+2029 as literal characters inside a `strings.NewReplacer` call rather than as `\u` escapes. | Accepted. The behaviour is correct and pinned by `TestNormalizeVoucherSlipText`. A readability nit only. |
| R5 | info | Test fixture "Reprinted 06/11/2026" is a dd/mm date inside the 60-day window. | Accepted. `guard-fixed-dates` passes, and no real-clock check reads it (Tester noted the same). |

### Checked and not a problem
- **Repository pattern**: SQL changed only in `internal/data/voucher_repo.go`, and `guard-data-access` passes.
- **No migration edits.**
- **XSS**: the designer textarea and the browser slip both render through html/template, and the 422 body is `html.EscapeString`'d. The preview response is `text/plain`.
- **Idempotence**: the normalisation round-trip is asserted for every accepted case.
- **Runes, not bytes**: confirmed by mutation (below).
- **Wide characters**: `wrapWords` and `center` use `displayWidth`. CJK and combining-mark cases are covered by `TestRenderVoucherSlipText_TextWraps`, and the new R1 test covers the ESC path.
- **Time zone**: the stored instant is converted with `.In(time.Local)`, and legacy `datetime('now')` is parsed as UTC. Confirmed by mutation (below).
- **Reprints keep the original date**: journal reprint reads the stored row (`TestPrintReceiptIn_VoucherSlipTextAndDates`).
- **Byte-identical output when unset**: the goldens `print/testdata/voucher_slip_6179_*` were rendered against the pre-change renderer (Tester), and `TestPrintReceiptIn_UnsetTextAndNoDateAddsNothing` covers the unset case.
- **`x_report_doc.golden`**: regenerated legitimately. The only change is the new zero field `VoucherSlipText:[]string(nil)` in the `%#v` dump, with no content change.
- **Forwarded save on an additional till**: refused locally with no call to the main till, and validated again on the main till for version skew (`TestReceiptDesigner_SaveVoucherSlipText_AdditionalTill`, `TestSyncSettingsApply_VoucherSlipTextValidated`).
- **The setting is not on `allowedRemoteTillSettingKeys`**, so there is no fourth, unvalidated cloud write path. Remote setting is deferred to #5744.
- **No file writes**, so the `os.MkdirAll` / `paths.Data` bug class cannot occur.
- **No `showModal`, no native confirm.** The 422 shows inline in `#rd-msg`.
- **Logical CSS only**: `margin-block-*`, `min-inline-size`.
- **No hardcoded user strings**, including inline script.

## Verification
- `gofmt -l .` produced no output. `go build ./...`, `go vet ./internal/...` and `golangci-lint run ./...` all passed with 0 issues.
- `go test -race ./internal/print/... ./internal/pages/... ./internal/data/...` was run with the two new tests in place. It reported 0 data races, and `print` and the `pages/*` subpackages passed. Two results did not pass under `-race`, and neither is related to this change:
  - `internal/pages` hit the 40-minute timeout (race slowdown).
  - In `internal/data`, two wall-clock perf budgets failed: `TestStockUpload…` (a sale waited 1.05s; Pending took 406ms).

  Re-run results:
  - `go test ./internal/data/` without race passes (56s).
  - `go test -race ./internal/data/ -run Voucher` passes.
  - `go test -race ./internal/pages/ -run 'Voucher|Receipt|Slip|Designer|SyncSettings|SettingsUpsert|XReport|Tender'` passes (455s).
  - `go test ./internal/pages/` in full without race passes (433s).
- **TDD claims re-verified by mutation**, in a separate `git worktree` that was removed afterwards. All 5 mutations were killed:
  1. Rune count replaced with byte count → `TestNormalizeVoucherSlipText` fails ("160 a-umlaut … over the limit").
  2. Normalisation removed from `/api/sync/settings/apply` → `TestSyncSettingsApply_VoucherSlipTextValidated` fails (200, want 422).
  3. `slipDateLineIn` ignoring tz → `TestSlipDateLineIn` fails (de/en-GB/en-US 23:30Z → wrong day).
  4. The R1 ESC wrap mutation → the new test fails.
  5. The R2 order swap → the new test fails.
- **Guards**, all rc=0: data-access, i18n, i18n-meta (base origin/main), rtl-logical-css, help-topics, help-breadcrumbs, help-terms, help-drift, fixed-dates, no-showmodal, no-native-confirm, compliance-claims, competitor-naming, core-neutral, mail-addresses, no-inline-handlers, osk-loaded, page-http-error, card-data-schema, kiosk-engine, autofill-suppression, deadcode-baseline-growth.
- **Tester evidence relied on, not re-run**: 125 e2e passed (including `osk-enter-newline-6179.spec.ts` and the 6152/4247/4251/4252 voucher specs). Also a live fake-printer ESC/POS capture: rows ≤ 42 columns, the text appears on slips only, and the journal reprint was byte-identical to the first print.
- **Not verified**: real thermal printer, Pi WebKitGTK OSK, docs-shots determinism, WebKit phone lane.

Pre-flight: web/help topic updated (designer.md, printing.md, vouchers.md); docs-shots updated (designer.png en/ar/fa/tr + manifest; the extra slip-preview shot was not added, as it needs a harness change); `reference/`/architecture updated (ut-docs branch docs/6179-voucher-slip-text: architecture/receipt-designer.md, receipt-printing.md); en.json → en.meta.json updated (5 keys, receipt.* `legal`); README n/a (no README text covers slip contents); plan/price n/a; ADR n/a (a setting within the existing receipt-designer design, no new decision); plugin/manifest/host-function mirrors n/a; ut-cloud mirrors n/a (no signing-contract surface touched); remote (my.) setting / expiry / copy count deferred → #5744; OSK ↵ submitting single-line designer fields: fixed independently by #6180 (universal-till#2536, merged before this PR; rebased onto it, osk.js merges cleanly — the newline opt-in runs before its implicit-submit rule); review record updated (this file).

**Verdict.** Approve. R1 and R2 are fixed with tests (only test files changed). R3–R5 are accepted info. Deferred: #5744. (#6180 landed separately first; this branch is rebased onto it.)
