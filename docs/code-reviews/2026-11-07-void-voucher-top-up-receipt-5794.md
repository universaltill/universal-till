# Review — a top-up of a since-voided gift voucher prints no scannable code on the receipt (ut-docs#5794)

Date: 2026-11-07 · Lane: lane:cloud-54 · Complexity: easy
Built by: orchestrator, Opus 5.5 ([no-subagent: one-file fix, one switch in `customerReceiptDoc`]) · Tested by: Sonnet (separate Tester subagent, throwaway worktree) · Reviewed by: Sonnet (fresh subagent, a different model from the author)

## What shipped

- `internal/pages/print_api.go` `customerReceiptDoc`: a top-up block is marked void
  from the ledger row (`Reversed` → "VOID - top-up taken back", unchanged) **or**, when
  not reversed, from the voucher's current status (`VoucherVoid` → the issued block's
  existing `receipt.vouchers.void`, "VOID - not valid"). The ESC/POS renderer already
  prints the code as text, never as a CODE128 symbol, when `Void` is set — so a reprint
  no longer hands out a scannable code for a void voucher. Same condition as the slip
  (#5793). No new i18n key.
- `web/help/en/printing.md`: the reprint paragraph says a voucher voided since the sale
  prints marked, its number as text.

## Tests (seen failing first)

- `TestPrintReceiptIn_TopUpOfVoidVoucherNoSlip` pinned the old behaviour ("receipt block
  must stay as today"); now asserts the void notice, the code still as text, and exactly
  one CODE128 symbol (`GS k 73`) fewer than the same receipt with the voucher active.
  Fails on `main`'s `print_api.go` (`Void:` empty), passes with the fix — re-verified by
  the Tester in a separate worktree.
- `TestPrintReceiptIn_TopUpReversedNoSlip` (Tester gap): now asserts the exact
  "VOID - top-up taken back" text, so the reversed-first precedence is pinned.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor (Tester) | Reversed-top-up test only checked `Void != ""`, not which notice | **Fixed** — exact text asserted |

Reviewer: no findings — precedence (reversed before void) keeps existing bytes; used-up
vouchers keep their symbol (only `status = 'void'` counts, as for issued blocks); other
readers of `VoucherTopUpsInSale` untouched; no other test pins these bytes.

## Gate

`gofmt`, `go build ./...`, `go test ./...` (every package ok except `internal/plugins`, which hit the 600 s default timeout in this container — untouched by this change, left to CI), `golangci-lint run ./internal/pages/...` (0 issues),
every guard in `ci.yml`'s build job — all green, except `guard-deadcode-baseline.sh`
(local deadcode binary built with go1.26 vs the repo's go1.27; fails identically on `main`)
and `guard-shellcheck-version.sh` (no shellcheck in this container) — both left to CI.

Pre-flight: help topic updated (printing.md); screenshots n/a (printed receipt, no screen change); en.json / i18n-meta n/a (no new keys); reference/ n/a; README n/a; review record — this file; follow-ups — none.
