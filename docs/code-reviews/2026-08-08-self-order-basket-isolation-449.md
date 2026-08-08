# Code review: self-order kiosk basket isolation + `pos.Service` concurrency fix (ut-docs#449)

**Date:** 2026-08-08
**Card:** [ut-docs#449](https://github.com/universaltill/ut-docs/issues/449)
**Complexity:** hard
**Author (Dev):** isolated Fable subagent
**Reviewer:** independent Opus subagent, isolated worktree

## What shipped

`/api/self-order/*` — an intentionally auth-exempt kiosk API surface
(ADR-0020), reachable by any client on the till's LAN — shared ONE
process-level `pos.Service` instance with the cashier's own basket, with
zero synchronization anywhere in the package. Two real defects followed:

1. **Cross-basket mutation**: any LAN client could call
   `scan`/`line`/`remove`/`order-type` against whatever basket the till
   currently held, including a cashier's in-progress sale.
2. **Active data loss, not just a theoretical race**: `GET /self-order` —
   reachable "regardless of the current display.mode setting" per its own
   comment — called `Reset()` unconditionally on that same shared
   instance. Simply landing on the kiosk page on a till currently running
   as a cashier register wiped the cashier's live basket.

## Design decision

Three options were on the table (BA/Architect write-up,
`ut-docs#449`'s own acceptance criteria): (a) a per-kiosk-session basket
fully separate from the cashier's, (b) a lightweight pairing/token
scheme, (c) a mutex-only fix + kiosk/cashier mode exclusivity.

**Chosen: (a), minimally — split `pos.Service` into two instances**
(`common.Deps.Engine` for the cashier, a new `common.Deps.KioskEngine`
for the kiosk), **plus a `sync.Mutex` inside `pos.Service` itself** so
each instance is independently safe under real concurrent access.

- **(b) rejected**: this product has exactly one physical self-order
  kiosk surface per till — no multi-device pairing concept exists
  anywhere in the codebase. A token/session layer would solve a problem
  this product doesn't have yet, and adds a mechanism (lifecycle,
  expiry, storage) a future Architect would need to carry forward for no
  present benefit.
- **(c) alone rejected**: confirmed by reading the code that mode
  exclusivity does not close the gap — `GET /self-order`'s unconditional
  `Reset()` on the shared engine doesn't ask the till's display mode
  first. Splitting the instance is what actually stops it.

**No ADR**: bounded to `internal/pos` and its two callers
(`internal/pages/init.go`, `self_order_*.go`) — no new cross-cutting
mechanism future unrelated work needs to stay consistent with. Rationale
recorded here and in the PR description per the card's own acceptance
criterion.

## Changes

- `internal/pos/service.go`, `internal/pos/hold.go`: added an unexported,
  non-reentrant `mu sync.Mutex`. Every exported method locks exactly once
  at its own top; five identified reentrant call chains (`Scan`→`ScanQty`,
  `UpdateLine`→`Remove`, `UpdateLineByKey`→`RemoveLine`, `Restore`→`Reset`,
  `Restore`→`SetCustomer`) were rerouted through new unexported lock-free
  cores (`scanQty`, `removeLocked`, `removeLineLocked`, `resetLocked`,
  `setCustomerLocked`) so the lock is never acquired twice on one call
  stack — a plain `Mutex` deadlocks (hangs, not fails) on re-entry.
  `ResolveBase` stays deliberately unlocked (reads only `s.resolver`,
  written once at construction, never reassigned).
- `internal/pos/service.go`: `Scan`/`ScanQty`/`ScanQtyWithResult`/
  `AddLineWithModifiers`/`SetOrderType` used to `return &s.basket` — a
  live pointer into state the next locked call rewrites out from under a
  reader once the lock releases. Now return `basketCopyLocked()`, a
  private copy; `recomputeTotals` always allocates a fresh `Lines` slice,
  so the copy is safe to read after unlock.
- `internal/pos/concurrency_test.go` (new): six goroutines × 1000
  iterations racing every mutating/reading method against one `*Service`,
  deliberately exercising all five reentrant chains, run under `-race`.
- `internal/pages/common/deps.go`: new `KioskEngine *pos.Service` field.
- `internal/pages/init.go`: constructs a second `pos.Service` for the
  kiosk at boot, sharing the single `pluginTaxRateAsker` (self-locked,
  keyed only by item/tax-code/rate/order-type — nothing basket-specific,
  safe and cache-efficient to share) and the stateless resolver.
- `internal/pages/self_order_page.go`, `self_order_shop.go`: every
  `d.Engine.*` call → `d.KioskEngine.*`.
- `internal/pages/pos_api.go`: `completeTender` now takes the engine to
  reset as an explicit parameter (cashier tender passes `d.Engine`, kiosk
  checkout passes `d.KioskEngine`) instead of hardcoding `d.Engine` — a
  call chain the original design missed; left as-is it would have reset
  the *cashier's* basket on a kiosk checkout.
- `internal/pages/pos_modifiers_api.go`, `self_order_shop.go`:
  `resolveAndValidateModifiers` (shared by the cashier and kiosk
  customization flows) now also takes the caller's engine explicitly, so
  the kiosk's modifier-resolution path is consistently `KioskEngine` too
  (found in review — harmless in practice since `ResolveBase` is
  read-only and both engines share one resolver, but it contradicted the
  split's own invariant).
- `internal/pages/settings_page.go` (2 sites), `setup_page.go` (1 site),
  `init.go`'s `rederiveSettings`: tax/service-charge `SetConfig` calls now
  propagate to both engines — left alone, a settings change would leave
  the kiosk silently charging stale rates after the split.
- `internal/pages/update_api.go`: the unattended auto-update guard
  (`if d.Engine.Basket().ItemCount() > 0 { return }`) only ever checked
  the cashier's basket after the split, so an in-progress kiosk order was
  no longer protected from a mid-order restart — this diff extends the
  guard to `d.KioskEngine` too (found in review; see below).
- `internal/pages/self_order_shop_test.go`: fixture now wires two
  separate engines exactly like production `Init`; existing kiosk
  assertions moved to `dp.KioskEngine`; new
  `TestSelfOrder_KioskAndCashierBasketsAreIsolated` pins the exact
  pre-#449 defect (an anonymous kiosk request must never read/mutate the
  cashier's basket, and `GET /self-order` must reset only the kiosk
  basket); `TestSelfOrderShop_CheckoutHappyPath` gained an assertion that
  a kiosk checkout leaves `dp.Engine` untouched, pinning the
  `completeTender(engine)` parameter against a silent regression back to
  a hardcoded `d.Engine`.

## TDD

Both new/adapted regression tests were confirmed failing for the right
reason before the fix, verified independently **twice** — once by the
orchestrator, once again by the isolated-worktree reviewer:

- **Concurrency**: reverting only `service.go`/`hold.go` to pre-fix and
  running `TestServiceConcurrentMutations` under `-race` produced real
  `DATA RACE` reports and an actual `panic: index out of range` (not a
  soft failure) both times it was reproduced. Restoring the fix passes
  clean. The reviewer additionally proved the test catches a
  *reintroduced* double-lock as a hang (not a pass), on two different
  chains (`Restore`→`Reset`, `UpdateLine`→`Remove`).
- **Isolation**: `TestSelfOrder_KioskAndCashierBasketsAreIsolated` and the
  handler-rename step were built test-first; before the rename, the
  existing kiosk tests failed with empty baskets for the right reason
  (handlers still writing to the cashier engine).

## Independent review

Isolated-worktree Opus subagent, independent of the Fable subagent that
wrote the fix. Re-derived the locking pattern, the pointer-aliasing fix,
and every deviation from the original design doc from scratch (fresh
greps, not trusting any handed-over list); ran the full gate itself;
personally reproduced both TDD claims. Verdict: **yes-with-fixes-required**.

Findings, all resolved before merge:

1. **Blocker — `update_api.go`'s auto-update guard regression** (above):
   a genuine data-loss-class gap this diff itself introduced (an
   unattended update could silently destroy a customer's in-progress
   kiosk order). Fixed.
2. **Blocker — `gofmt` violation** on `common/deps.go` introduced by a
   comment edit. Fixed.
3. Non-blocker, folded in: `pos_modifiers_api.go`'s
   `resolveAndValidateModifiers` still took an implicit `d.Engine` for
   its `ResolveBase` call on the kiosk path — harmless (read-only,
   shared resolver) but inconsistent with the split. Fixed by threading
   the engine through explicitly.
4. Non-blocker, folded in: no test pinned that `completeTender`'s new
   `engine` parameter is actually wired correctly per call site — a
   regression to a hardcoded `d.Engine` would have silently passed. Added
   an assertion.
5. Non-blocker, folded in: a doc-comment on `completeTender` had two
   separate lede sentences from two authorship passes. Merged into one.
6. **Informational, not fixed — accepted as-is**: `Service.mu` is now
   held across a blocking WASM tax-plugin call inside `recomputeTotals`
   (confirmed no deadlock — `internal/plugins` never imports
   `internal/pos`), so requests against one engine now serialize behind
   a ~90ms-worst-case plugin ask where they previously ran unserialized.
   Acceptable for this product's single-operator-per-till traffic
   pattern; noted here as a starting point for a future latency
   investigation if one is ever needed.

Everything else — locking correctness across all 30 exported `Service`
methods (fresh grep, no missed method, no double-lock), the
`basketCopyLocked` deep-enough-copy argument, `SetConfig` propagation at
all four real call sites, the manual (no user-visible behavior changed,
confirmed by checking every `ToastMessage` write goes through a value
copy, not a `*Basket` from the now-copied methods), and standard checks
(no real shop name, no secret-shaped literal, repository pattern/money/
i18n/offline-first untouched) — passed clean on both the implementer's
and the reviewer's independent passes.

## What was verified beyond automated tests

- Full gate (`go build`, `go vet`, `go test ./... -race -count=1`, all
  four guards) run independently three times: once by the Dev subagent,
  once by the orchestrator against the Dev subagent's output, once more
  by the isolated-worktree Opus reviewer from a fresh checkout of the
  same commit — all green.
- `gofmt -l` checked explicitly (not part of this repo's CI, but a real
  regression the diff introduced) and fixed.
- Manual re-derivation, by the reviewer, that no `internal/pages` handler
  still reaches the wrong engine (`grep -rln "d\.Engine\."` re-run fresh,
  not trusted from the design doc).

## Deferred / follow-up (not this diff)

- Filed as a new Backlog card: extending the same review rigor to any
  other place a future kiosk-facing feature might reach `d.Engine`
  instead of `d.KioskEngine` — the split relies on developer discipline
  going forward; a lint/grep-based CI guard is a reasonable follow-up but
  out of scope for this fix.

## Verdict

**Safe to merge.** Core mechanism (mutex + basket-copy fix + engine
split) independently verified correct by two passes on two different
models; both blockers found in review are fixed and re-gated; the one
informational finding is a deliberate, documented tradeoff, not a defect.
