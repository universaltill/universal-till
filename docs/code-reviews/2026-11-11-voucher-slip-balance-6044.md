# Review: "Balance on <date>" row on a reprinted gift-voucher slip (ut-docs#6044)

- **Card:** ut-docs#6044 (item 8 split from ut-docs#5744; follow-up of #5740 "Print slip").
- **Branch:** `feat/6044-voucher-slip-balance-v2` (`feat/6044-voucher-slip-balance` / universal-till#2485 carries a commit with the wrong author and was closed)
- **Pipeline:** BA, Architect (session model), UX gate, Dev (Sonnet) plus a fix round (Sonnet), Tester (separate agent), Reviewer (Opus 5.5, fresh context, separate agent). Unattended cloud cycle, lane `lane:cloud-54`.

Pre-flight: web/help topic: updated (`web/help/en/vouchers.md`, the slip contents and the new note; `make docs-shots` re-run after CI's docs-shots guard flagged the topic and surface hashes; no vouchers PNG changed, and the byte-noise PNGs of unrelated topics were restored from `main`; `manifest.json` refreshed). `reference/`: n/a (no reference page describes the reprinted slip). README: n/a. en.json/en.meta: updated. Added `receipt.vouchers.balance_on_label` (review legal, max_length 22, the placeholder may be reordered) and `vouchers.print_slip.balance_unknown_note`. Removed `vouchers.print_slip.original_value_note`, which had no other users and no baseline entry. English only (ADR-0160). ADR: n/a (follows the main-till judgement of ut-docs#4416 and the top-up slip balance of ut-docs#5793). Plan/price, platform claim, plugin taxonomy: n/a. Follow-ups: deferred → ut-docs Backlog card (L1–L3 below). Review record: this file.

## What shipped

- `voucherForPage` (`internal/pages/vouchers_page.go`) returns `voucherPageRows`, which carries a `BalanceKnown` flag. It is true on a main or standalone till, and on an additional till only when the main till answered. Unreachable, an error, a malformed answer or a 404 all count as unknown.
- `voucherSlipFor` (`internal/pages/voucher_slip_reprint.go`) adds a "Balance on <date>" row with the current balance (`FormatMoneyPrint`, date with `FormatDateLatin`), but only when the balance is known and differs from the original value. The value row keeps the original value.
- On an additional till that can't reach the main till, the slip still prints, without the balance row, so no stale balance is ever printed (orchestrator decision, option A in the BA note). The audit row gains `balance_printed`.
- `print.VoucherBlock` gains `BalanceLabel` and `BalanceAmount`. Both renderers (ESC/POS and paper text) print them as a `kvRow`, so only the label can be clipped, never the amount. Top-up slips keep using the old `Balance` field and print exactly as before.
- On the detail page, the note "The main till can't be reached, so the slip shows the original value only…" shows only when the balance is unknown.

## Findings and resolution

| # | Sev | From | Finding | Resolution |
|---|---|---|---|---|
| U1 | must | UX | The single line "Balance on %s: %s" was clipped at 42 columns with the amount last, so a long translation could print a cut, wrong amount | Fixed: separate label and amount fields printed as a `kvRow`; `TestRenderVoucherSlip_BalanceRowKeepsAmountWhole` |
| U2 | must | UX | The old note text gave no cause or next step now that it only shows when the balance is unknown | Fixed: new key with the cause and the next step; old key removed |
| T1 | major (test) | Tester | `TestVoucherSlipFor_BalanceLine` never ran under `fa`, because the mux reset the locale, so the Latin-digit check was a false pass | Fixed: the locale is set after the mux is built and asserted |
| L1 | low | Reviewer | When the main till answers 404, the note says "can't be reached" | Deferred → Backlog card |
| L2 | low | Reviewer | If the main till drops between page load and Print, the slip omits the row with no warning in the result message | Deferred → Backlog card |
| L3 | low | Reviewer | The audit row doesn't record the printed amount, and `balance_printed` is computed on a failed print too | Deferred → Backlog card |
| L4 | low | Reviewer | The fixed 42-column width wraps on 58 mm font A. This is true of every existing `kvRow`; the de/fa/tr help lags English | Pre-existing, out of scope (Weblate for help) |

Verdict: **APPROVE** (Opus), with no blocking findings.

## Verification

- The reviewer re-checked four TDD claims in a throwaway worktree. Each named test failed with the fix broken and passed with it restored: main-till balance on an additional till, the note condition, a 404 counted as unknown, and the amount kept whole.
- The Tester made 8 deliberate breakages, and each was caught by at least one test.
- **Driven run** (built binary, seeded data, printer output to a file, Playwright):
  - An untouched voucher prints with no balance row and the page shows no note.
  - A partly used voucher (25.00 → 10.00) prints "Balance on 11/11/2026" with 10.00.
  - A topped-up voucher prints 35.50.
  - On an additional till with the main till unreachable, the slip prints with no row and the page shows the note.
  - The audit `balance_printed` values were false, true, true, false for these four cases.
  - Screens checked: `/vouchers/{id}` at 1024×700 (en) and 360px (fa, RTL).
- **Gates:** gofmt, `go build ./...`, and go vet on pages/print pass. `go test ./internal/print/...` passes, as does `go test ./internal/pages/ -run 'Voucher|Slip|TopUp|Receipt'`. The full `internal/pages` suite passes without `-race` (424s). With `-race` on the 4-core container, `internal/pages` hit the timeout in an unrelated sync-join test, with no failure or race reported before that point. golangci-lint reports 0 issues. The touched-file guards all pass: i18n, i18n-meta, fixed-dates, help-*, compliance-claims, competitor-naming, rtl-logical-css, data-access, no-showmodal, core-neutral and mail-addresses.
- Not checked: a real 80 mm or 58 mm printer. That needs a device check on the owner's hardware, which belongs to the remaining items of #5744.
