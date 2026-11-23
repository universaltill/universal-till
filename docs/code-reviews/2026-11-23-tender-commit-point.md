# Review record: tender commit point (ut-docs#6923)

Scope: BA items 1, 3, 4 (item 2, last-sale receipt on resync, is ut-docs#6925).

## Change
- `pos.Service`: `BeginAbandon/EndAbandon` (counter) and
  `BeginTenderCommit/EndTenderCommit`. Commit stops new abandons, drains
  admitted ones (bounded by `tenderDrainTimeout`), then re-checks the attempt id.
- `completeTender`: commit point replaces the last `abandonedUnder()` check;
  released right after the sale's `engine.Reset()` and on every error path.
- Abandon routes wrapped in `abandonOp` (reset, hold, resume) or gated inline
  (`/open-orders/resume`, `/open-orders/counter/open`, redirect to
  `pos.tender.in_flight`). Authorize-phase abandons (#6602) are unchanged.
- Kiosk checkout claims its basket engine (`eng.TryBeginTender`) instead of
  `selfOrderCheckoutInFlight`; type renamed `keyedInFlight` (tap legs).
- i18n: no new key; meta description of `pos.tender.in_flight` broadened.

## Risks noted
- A slow abandon (cross-till resume claim) can hold the drain for up to 10s;
  the tender then refuses as abandoned and reverses captured legs (safe side).
- A new abandon route must call `BeginAbandon`; reviewer greps
  `parkCurrentBasket(|resumeHeldSale(|ResetAndSnapshotIf(` callers.
- Table-session sweep can still evict a session engine mid kiosk checkout
  (unchanged, out of scope).

## Independent review
Author: Sonnet 5.5. Reviewer: Opus 5.5 (different model). Scope: `git diff`
+ untracked `keyed_inflight.go`, `tender_commit_point_test.go`; ut-docs
`reference/payment-provider-contract.md` (one sentence, covered by this record).

Checked: `s.mu` is never held while the commit drains (no deadlock with
`TryBeginTender`/edits; abandon handlers never call `BeginTenderCommit` or wait
on `tenderInFlight`); flag/drain are set atomically under `mu`, so no abandon
slips between check and `CompleteSale`; `defer endCommit()` covers the
`CompleteSale` error and panic paths; `abandonOp` defers `EndAbandon`. Every
caller of `parkCurrentBasket`/`resumeHeldSale`/`ResetAndSnapshotIf` is behind
the gate (reset, hold, resume, open-orders resume, counter/open). Tap
`tapUnlock` rotation runs under `TryBeginTender`, so it cannot meet a commit.
At the commit point the stash is already settled, so an ungated reset (kiosk
landing) hands off no captured legs. Kiosk checkout now claims `eng` (KioskEngine
or the session engine), never `d.Engine`; no kiosk route uses `BeginBasketEdit`,
so `TryBeginTender`'s drain never waits there.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| 1 | should-fix | Help `payments.md` still said a clear/park during a payment always sends the payment back; now refused once the sale is being saved (user-visible notice). | Fixed: one sentence in `web/help/en/payments.md`. |
| 2 | nit | `tap_pending.go` comment still called the key set "the kiosk's". | Fixed. |
| 3 | nit | Kiosk landing reset (`self_order_page.go`) and the table-session sweep are not gated by `BeginAbandon`. | Accepted: stash is empty at commit, no park, so no money/record harm; out of scope per design. |
| 4 | nit | `TestTenderCommit_AdmittedAbandonThatResetsNothingLetsCommit` uses a 20 ms sleep, so it may not always exercise the wait. | Accepted: `WaitsForAdmittedAbandonThenRefuses` pins the wait deterministically. |
| 5 | info | A slow admitted abandon (cross-till resume claim) can hold the commit up to `tenderDrainTimeout`, then the tender refuses and reverses (safe side). | Accepted (noted above). |

Verification (reviewer, run personally): TDD re-check in a scratch copy, not the
shared checkout: removing the `committing` refusal in `BeginAbandon` fails all
six `TestCommitPoint_*Refused` tests (200 / parked-and-resumed redirect instead
of 409 / in-flight redirect); dropping `defer endCommit()` fails
`TestCommitPoint_ReleasedOnCompleteSaleError`. Restored tree passes:
`go test -race` on `TestCommitPoint_|TestSelfOrderCheckout_` (x3) and
`internal/pos` tender/abandon tests (x3); `go vet`, `go build ./...`, gofmt,
golangci-lint (pages, pos: 0 issues); guards kiosk-engine, i18n, i18n-meta,
data-access, fixed-dates, core-neutral, help-topics, compliance-claims green.
One early run of the targeted pages set failed once and did not reproduce in
7 further runs (likely the concurrent Tester run in the same checkout).

UX: the refusal reuses the existing non-modal `tender-in-flight` notice (sale
screen) and the open-orders `?err=` banner; no new key, no modal. No device
run was possible: the window is the milliseconds of the sale write, only
reachable through the test hook.

Pre-flight: till user-visible behaviour: help `web/help/en/payments.md` updated,
`make docs-shots` n/a (no visual change), `reference/payment-provider-contract.md`
updated (ut-docs); `en.json` key: n/a (none new), `en.meta.json` description
updated; README n/a; ADR n/a (follows #6602/#6909/#6919 design); review record: this file.

Verdict: approve. Deferred: none new (item 2, last-sale receipt on resync, is ut-docs#6925).
