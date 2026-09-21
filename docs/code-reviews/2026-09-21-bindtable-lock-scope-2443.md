# Code review: BindTable lock-scope fix (ut-docs#2443)

**Date:** 2026-09-21
**Card:** ut-docs#2443 — follow-up finding S1 from the independent review of
ut-docs#2434 (`docs/code-reviews/2026-09-19-self-order-table-bind-race-2434.md`)
**Complexity:** medium — Sonnet built, Opus reviewed (two rounds)

## What shipped

`SessionBasketManager.BindTable` (`internal/pos/session_manager.go`) used to
call `mover.SetTable`/`svc.SetTable` while holding the manager's own `m.mu`.
`Service.SetTable` triggers `recomputeTotals`, which can invoke a blocking
plugin tax/charge-policy ask (up to ~10s for a net/tcp-permitted plugin on a
cache miss) — and every self-order table's `BindTable`/`Get` call needs
`m.mu`, so holding it across that ask serialized every OTHER guest's
concurrent table request behind this one ask.

Fix: `sessionBasket` gains a `tableID` field — the manager's own busy-check
record, read and written under `m.mu` alone, with no `Service` call
involved. `BindTable`'s busy-check loop scans this field instead of calling
`sb.svc.TableID()`. The claim is recorded on `sessionBasket` inside one
lock/unlock, `SetTable` then runs unlocked, and a short re-lock afterward
writes back whatever the Service actually ended up holding (`svc.TableID()`,
never blindly the requested `tableID` — `SetTable` can silently no-op for an
all-takeaway basket). On the mint path (`mover == nil`, the common
first-ever-scan case), the fresh `*Service` itself is now built via
`m.factory()` BEFORE `m.mu.Lock()` — `m.factory()` installs the
tax/charge-policy askers, and installing either synchronously triggers the
same kind of blocking ask, so building it under the lock would have
defeated the fix for its most common path.

## Independent review — round 1

Opus subagent, fresh context (no prior discussion in context), given only
the card background and told to review adversarially.

**Verdict: has a real bug.** The first draft's core lock-scope move (moving
`SetTable` outside the lock, recording the claim via `sessionBasket.tableID`)
was structurally sound for the atomicity property it was supposed to
preserve — two concurrent binds could not both win the same table, verified
by tracing the mint path's single continuous critical section. But it missed
the fix's own goal on the common path, and introduced one new correctness
regression:

- **M1** (real, fixed): `m.factory()` was still called INSIDE `m.mu.Lock()`
  on the mint path. Since the production factory
  (`internal/pages/init.go:299-304`) installs both askers, and installing
  either synchronously recomputes totals (`AskChargePolicy` unconditionally
  when an asker is present), the mint path — a first-ever table scan, the
  most common case — still held `m.mu` across a blocking ask. The new
  doc comment claiming `SetTable` was "the first mutating call" was
  factually wrong for this reason. Fixed: build the `*Service` before
  `m.mu.Lock()` whenever `mover == nil`; the rare fallback (`mover` provided
  but its session evicted concurrently) still builds it under the lock,
  accepted as genuinely rare, not the common path this card is about.
- **M2** (real, fixed): two goroutines sharing the SAME `ownToken` (a
  double-tap from one guest's browser against two different tables) could
  write their `sessionBasket.tableID` claims (under the lock) in one order
  and execute their unlocked `SetTable` calls in the OPPOSITE order,
  permanently desyncing `sb.tableID` from `svc.TableID()` — reopening the
  exact double-bind ut-docs#2434 closed, via a different path this fix
  introduced. Fixed: after the unlocked `SetTable` call, re-acquire `m.mu`
  and write `sb.tableID = sb.svc.TableID()` — the actual post-call value —
  re-verifying the session wasn't evicted first. Round 2 stress-tested this
  300 rounds with two goroutines racing on one `ownToken`: zero desyncs.
- **M3** (real, fixed by the same write-back as M2): `Service.SetTable`
  silently no-ops for an all-takeaway basket (`hasDineInLine`'s guard).
  Recording the claim unconditionally as the *requested* `tableID` (rather
  than the Service's actual resulting `TableID()`) could permanently strand
  a phantom claim if that guard is ever reached from a table-bound self-order
  session. Round 2 reproduced this directly with a takeaway-basket mover and
  confirmed the write-back correctly ends at `""`, not the stranded value.
- **M4** (real, fixed): `TestSessionBasketManager_BindTable_StaleResumeAfterCompetingBindSeesBusy`
  was left calling `aSvc.SetTable(...)` directly to seed session A, bypassing
  the manager's own `tableID` bookkeeping entirely — proven empirically (by
  round 1) to make the test's staleness assertion pass for the wrong reason
  (it still passed with the clock advance removed, i.e. with A deliberately
  NOT stale). Fixed: bind A through `m.BindTable(...)` in setup. Round 2
  re-ran that exact mutation against the fixed test and confirmed it now
  fails as it should.
- **N4** (nit, fixed): the mover branch called the caller-supplied `mover`
  directly rather than `sb.svc` (the manager's own tracked record) —
  hardens an invariant the method should enforce itself rather than merely
  assume of a well-behaved caller.
- **N5** (nit, fixed): the new `slowChargeAsker`-based regression test could
  leak a goroutine forever if a `t.Fatal` fired before the explicit release
  call ran. Fixed with an idempotent `releaseNow()` (`sync.Once`-guarded)
  plus a `t.Cleanup(asker.releaseNow)` registered right after each asker is
  created.
- Round 1 also flagged that the new regression test
  (`TestSessionBasketManager_BindTable_SlowChargePolicyAskDoesNotBlockOtherTables`)
  only exercised the mover path, not the mint path where M1 actually lived —
  added `TestSessionBasketManager_BindTable_MintPathFactoryAskDoesNotHoldLock`,
  which uses `m.Get` on an unrelated, pre-existing session as a
  lock-contention probe while a mint's own factory-triggered ask is
  deliberately stuck.

## Independent review — round 2

Round 1 found a blocker-class issue (M2 reopened a double-bind bug — a
data-integrity issue at checkout, two `kiosk_counter_orders` rows for one
table), earning a second round per this pipeline's own review-depth rule.
Scoped to verifying the round-1 fixes, not a full re-review. A fresh Opus
subagent, no prior context.

**Verdict: safe to merge.** Verified M1-M4 as FIXED — not by re-reading the
comments, but by tracing control flow and, for M2-M4, by direct
experimentation (a 300-round same-`ownToken` race stress test for M2, a
constructed takeaway-basket mover for M3, and re-running round 1's own
"remove the clock advance" mutation against the repaired M4 test to confirm
it now fails for the right reason). `gofmt`/`go vet`/`go build`/the full
`internal/pos` race suite all clean throughout both rounds.

Four new, non-blocking findings from round 2's own read of the fix:

1. A busy-scan on the mint path (`mover == nil`) now unconditionally builds
   and discards a `*Service` before checking whether the table is actually
   free — a wasted (unlocked, so non-blocking to others) factory call,
   including its own charge-policy ask, on every busy mint scan. Not a
   correctness bug; the plugin ask has a per-generation cache so it's
   usually warm, but the guest waiting to be told "table busy" now
   sometimes waits on a plugin round-trip first. Left as a documented
   trade-off, not fixed here — genuinely lower priority than the four
   correctness findings this fix exists for.
2. The type's own Locking doc comment lost a still-true warning that
   `SetConfig` has the identical blocking-ask exposure `BindTable` used to
   (its `Service.SetConfig` call can trigger the same kind of blocking
   plugin ask, under `m.mu`, serializing every live session behind it) —
   restored, out of this card's scope to fix.
3. `sessionBasket.tableID`'s "kept eventually consistent" claim only holds
   for changes `BindTable` itself makes; nothing re-syncs if a session's
   table is cleared some other way (e.g. an order-type change reaching
   `applyTablePolicyLocked`). Today that's blocked only by a clamp in a
   different package (`self_order_shop.go`'s takeaway-toggle guard,
   pinned by `TestSelfOrderShop_TableCheckout_TakeawayToggleCannotUnbindTable`)
   — nothing in `internal/pos` itself enforces it. Documented inline with a
   cross-reference; not fixed, since fixing it would mean this package
   reaching into a policy decision that currently, correctly, lives
   elsewhere.
4. A couple of small nits (a `sb != nil` vs. `== sb` inconsistency between
   the mint and mover write-back checks, and the rare mover-eviction
   fallback calling the caller's `factory()` under `mu` with no deferred
   unlock, unlike every sibling method) — cosmetic/defensive-depth only,
   left as-is.

Findings 1 and 3 are worth a human's call on whether to file as their own
follow-up cards; not filed automatically by this cycle, since round 2's own
verdict was "safe to merge" and neither blocks that verdict.

## What was verified beyond the automated reviews

- `gofmt -l`, `go vet ./internal/pos/...`, and `golangci-lint run
  ./internal/pos/...` (0 issues) on the changed files.
- `go test ./internal/pos/... -race -v`: all 248 tests pass (6 of them
  `BindTable`-specific, including the two new regression tests), 0 races,
  ~145s wall time, run twice (once after round-1 fixes, once after the two
  round-2 doc touch-ups) with identical results.
- `go test ./internal/pages/... -run "SelfOrder|Table" -race`: the caller
  side (`bindSelfOrderTableSession` in `self_order_page.go`, unchanged by
  this diff — `BindTable`'s signature and return contract are the same) —
  202 sub-results across the module, 0 failures, confirming nothing calling
  through the old locking assumptions broke.

## Related

ut-docs#2434 (parent bug this is a follow-up of), ut-docs#2261 (ADR-0103,
the original `SessionBasketManager` design).
