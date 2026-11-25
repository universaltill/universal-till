# Review — boot reconcile closes a tap leg whose attempt is already settled (ut-docs#7027)

Date: 2026-11-25 · Lane: `lane:cloud-41` · Branch: `fix/7027-boot-settled-tap-leg`
Paired doc PR: ut-docs `docs/7027-adr-0136-boot-settled-leg` (ADR-0136 9.2/9.5 clarification, no state-table change).

## What shipped
- `ReconcilePaymentLegs`: an `unknown`/`asked` leg whose `payment_attempts` row is decided
  (`resolved_not_charged`, `cancelled`, `declined`, or `approved` + `charged_no_sale`) gets
  `warned`/`till_restarted` → `resolved`/`attempt_settled` in one transaction, an Info line and no
  "refund it there" Warn. Everything else (open attempt, `approved` without `charged_no_sale`,
  no attempt row, read failure, `reversing`, `captured`) behaves as before. New `Closed` count.
- `PaymentLegJournalRepo.AppendChain` (all-or-none, stale-`Seq` refused); `Append`'s checks moved
  into a shared `appendTx` (behaviour unchanged); reason `attempt_settled` allowed only on
  `warned → resolved`.
- Docs: `docs/data-model.md` journal paragraph, `web/help/en/payments.md` step 4, docs-shots
  surface hash (`leg_reconcile.go` renders no pixel).

## Process
BA → Architect (Opus) → Dev (Sonnet, TDD) → Tester (independent) → Review (Opus, independent).
UX skipped: no template/CSS/string change.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| T1 | blocker (gate) | Tester: `run-docs-shots-guard.sh` failed — surface record stale for `leg_reconcile.go` | fixed (hash refreshed, `Docs-Shots-Unchanged: true`) |
| R1 | low | Boot vs live manager settle race: boot reads the attempt while still open, manager settles concurrently, boot then writes `warned` → stale row again (same race existed in the old `warnLeg` path; clearable by Mark as handled) | deferred → ut-docs#7039 (take `tapLegInFlight` around get+append) |
| R2 | nit | "tap/phone leg" in the ADR is keyed in code on "an attempt row exists" | fixed (comment) |
| R3 | nit | `AppendChain` ignores a step's `At` | fixed (doc comment) |
| R4 | nit | help prose narrower than behaviour (declined/cancelled taps) | accepted (prose accurate) |

## Verification
- Fail-on-main re-verified by Tester and Reviewer separately: 5 closing pages tests fail on
  origin/main (`chain = [asked unknown warned]`, `Closed:0`); journal and listing assertions each
  bite with the counter check stripped; 5 mutations all caught.
- Gate (Tester): gofmt, build, vet, `go test ./... -count=1` (128 packages ok), golangci-lint 0
  issues, 184/188 build-job script lines ok (3 = T1, fixed; 1 = shellcheck-version, no shellcheck
  binary here). `-race`: `internal/data` journal/attempt tests ok; the whole `internal/data`
  package under `-race` hit the 10 min default timeout on this 4-core box, and its only failure
  was the timing test `TestStockUploadPendingAndHasSendable_Fast…` (310 ms budget under the race
  detector; file untouched by this change). Targeted `internal/pages` tests under `-race`; full
  pages `-race` exceeds this container's budget (CI runs it).
- Driven run: real binary, real restarts, real `POST /api/pay/attempts/attention/resolve`. On the
  branch the settled legs end `resolved/attempt_settled`, `/ui/settings/payment-attention` shows
  only the open tap; a third boot writes nothing. Same script on origin/main lists all three
  (bug reproduced). Limit: no signed test tap plugin, so the legs were seeded through the repos.

Pre-flight: help topic updated (payments.md step 4); docs-shots n/a (no pixel; hash refreshed);
reference n/a (no contract change); README n/a; en.json n/a (no new key); ADR clarified in the
paired ut-docs PR (header line, no index change); follow-ups → ut-docs#7039.

Verdict: **ship.**
