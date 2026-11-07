# Review — journal reprint records reprinted voucher slips (ut-docs#5783)

Date: 2026-11-07 · Lane: `lane:cloud-54` · Card: universaltill/ut-docs#5783 (split from #5744 item 7)

## What shipped

- `internal/pages/print_api.go`: `printReceiptIn` returns `(slips int, err error)` — the number of gift voucher
  slips (#4247) in the job it built (0 when refused, no printer, doc error, setting off or no issued vouchers;
  the count is kept when the job was built but the transport/send failed). The reprint handler
  (`POST /api/print/receipt/{receiptNo}`) adds `voucher_slips: n` to the `receipt_reprint` audit row when n > 0,
  on both `ok:true` and `ok:false` rows. The refused `shadow_customer_documents` row is unchanged.
  `printReceiptFn` (auto-print after tender/refund) goes through a 3-line `printReceiptNoSlips` wrapper, so its
  error-only signature and every test stub reassigning it stay as they were.
- `web/help/en/printing.md` "Reprinting a receipt": says the audit log records how many slips were printed again.
- Tests: new `internal/pages/reprint_slip_audit_5783_test.go` (setting on → `voucher_slips:2`; print failure →
  `ok:false` + `voucher_slips:2`; setting off → no key); existing `printReceiptIn` callers assert the count.

## Process

Dev (Sonnet subagent, TDD: red `audit = {"ok":true}, want ok true and voucher_slips 2`, then green) →
Tester (Sonnet subagent: four deliberate breaks of `print_api.go`, each caught; added the print-failure test) →
independent review (Opus 5.5 subagent, isolated worktree).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | `printing.md` didn't say a reprint's slips are recorded, while `vouchers.md` does for the stand-alone slip | fixed |
| 2 | nit | doc comment said "sent but failed"; a `NewTransport` failure is built-but-not-sent and still reports the count | fixed (wording) |

Reviewer confirmed: count equals `len` of the exact slice passed to `PrintDocWithSlips` (voided and top-up
vouchers excluded by `voucherSlips`); no other production caller's behaviour changed; no reference doc lists
`receipt_reprint` fields (ut-docs `reference/`, `guides/`, `docs/`, help `reports.md` checked).

## Verification

- TDD re-verified by the reviewer: `audit["voucher_slips"] = slips` → `_ = slips` fails both positive tests
  with the claimed message; restored → pass.
- `gofmt -l` clean, `go build ./...`, `go vet ./...`, full `go test ./... -count=1` (Tester; `internal/plugins`
  hit the default 10 min timeout when run with everything, passed alone in 620 s — untouched, slow wasm package),
  `internal/pages` full package re-run after the fixes.
- Every guard in `ci.yml`'s build job run locally: all pass except `guard-deadcode-baseline` (local deadcode
  binary built with go1.26 vs go1.27 module — fails identically on `main`) and `guard-shellcheck-version`
  (no shellcheck in this container); both left to CI.
- No UI surface changed (audit JSON only) — no screenshots or e2e needed; help prose only, no docs-shots.

Pre-flight: help topic updated (`printing.md`); docs-shots n/a (no screen changed); en.json/meta n/a (no key);
README n/a; reference/ n/a (no page lists receipt_reprint fields); follow-ups: none new (rest of #5744 stays there).

Verdict: approve.
