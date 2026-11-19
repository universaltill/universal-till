# Z window: a shift closed in the same second as the Z run (ut-docs#5831)

Branch `fix/5831-z-close-whole-second` (worktree `ar/5831`). Batch review of five cards
(#5770 #5776 #5831 #5852 #5908) by an independent Fable 5.2 reviewer on the integration
branch; builds on the earlier in-card review B1/M1 (stamp writers under the write lock;
skip the bound wait for a refused Z).

## What shipped
Z run and archive bound handled to the whole second, so a shift or sale committed in the
same second as the Z lands in exactly one Z; cleared-basket / voucher writers stamp their
row under the write lock; the Z reconciliation includes a shift closed in the Z's second.

## Findings
| # | Finding | Severity | Result |
|---|---|---|---|
| 1 | #5770's `CloseShift`/`OpenShift` hoisted the clock read out of the write transaction, breaking this card's "stamp under the lock" guarantee for shift close | blocker (cross-card) | fixed on #5770's branch (stamp inside the tx + regression test using `HoldWriteLock`); this PR merges first |
| 9 | A close at 23:59:59.x archives with period end at 00:00:00 next day | minor | accepted: scheduler and once-per-day guard use `-1 second`; display only |
| - | X report whole-second truncation; manual + scheduled Z racing across midnight | minor | already Backlog items from the earlier review |

## Verification
- `go build ./...`, `go vet ./internal/...` clean.
- Money-adjacent (PAYMENTS) first: `go test ./internal/data/` and `./internal/pos/` green;
  `./internal/pages/` full suite green (6 min) on the batch branch; narrow EOD/Z/voucher/
  cleared-basket/no-sale runs green on this branch.
- `guard-data-access.sh`, `guard-i18n.sh` green. docs-shots regenerated in the ut-docshots
  Linux container (reports topic hash in `manifest.json`; the reports PNGs render identically,
  unrelated plugins PNG noise discarded).
- Pre-flight: help topic `reports.md` updated; manifest updated; en.json keys n/a; README n/a; ADR n/a.

## Verdict
Approve. Deferred: none beyond the existing Backlog items above.
