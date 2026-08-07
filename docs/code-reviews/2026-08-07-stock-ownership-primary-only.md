# 2026-08-07 — Stock ownership is exclusively the primary till's (ut-docs#404)

- **Branch:** `fix/404-stock-ownership-primary-only`
- **Ticket:** universaltill/ut-docs#404 — field report, a real two-till
  shop: a replica sold the last unit of an item 21s after another till
  had already sold it, taking stock to `-1`.
- **Design:** `ut-docs` ADR-0036 (amends ADR-0011 §3), merged before this
  branch started, per ADR-0007 document-first.
- **Dev:** Fable subagent (complexity:hard tier). **Independent review:**
  Opus subagent, deliberately not Fable (a model reviewing its own build
  shares its blind spots). Findings triaged and fixed by the orchestrator
  (Sonnet) below; TDD claims re-verified personally, not on trust.

## What shipped

Stock has exactly one owner — the primary/back-office till. Replicas
cache and report; they never gate a sale on their own locally-read stock
figure (that figure is what produced the field report's oversell: the
30s sync tick can't keep a replica's copy current).

- `internal/pages/pos_api.go` (cashier tender) and
  `internal/pages/self_order_shop.go` (self-order kiosk): on a replica
  (`d.SyncPrimaryURL(ctx)` non-empty), `AllowNegativeInventory` is forced
  `true` regardless of the shop setting or a per-request override — the
  local `cur+qtyDelta<0` gate in `internal/pos/sales.go` is never
  meaningfully enforced on that path. The primary's own direct-sale path
  is byte-for-byte unchanged.
- `internal/pages/sync_sales.go`: `applyJournal` (primary-side journal
  replay, already force-`true` since ADR-0011 shipped) and the
  primary's own direct-sale path (`pos_api.go`) both call the new
  `warnIfStockNegative` after a sale commits — any line whose resulting
  level is negative, and which *wasn't already* negative before this
  sale (a genuine transition, not a repeat), gets one `logging.L().Warnf`
  naming the item and the level. `logging.Recent()` already feeds the
  back-office Problems panel — no new plumbing needed.
- `internal/pages/refund_page.go` and `internal/pages/inventory_api.go`:
  a replica's return also nudges the push loop (a return is a journaled
  sale like any other, ADR-0011 D3), for parity with the tender path.
- Immediate push: `internal/pages/common/deps.go` gained
  `Deps.SyncPushNow`/`RequestSyncPush()` (nil-safe, non-blocking,
  capacity-1 channel), and `internal/pages/sync_admin.go`'s
  `runSyncLoop` gained a `kick <-chan struct{}` select arm. A completed
  local sale on a replica nudges the existing 30s-ticker push loop
  (`internal/pages/sync_sales.go`'s `StartSyncPush`) for one immediate
  attempt — fire-and-forget, never blocking checkout (ADR-0003) — instead
  of spawning a per-sale goroutine, which keeps push attempts serialized
  on the cursor and joined by the existing shutdown-drain WaitGroup
  (ut-docs#153).
- `internal/pos/sales.go`: `AllowNegativeInventory`'s doc comment states
  the primary-only semantics, and records why the field stays shared
  (force-true) rather than being split into two fields per ADR-0036's own
  "should be removed, not skipped" consequence note — see Finding 6 below.
- `internal/logging/logging.go`: new test-only `ResetRecent()`.
- Docs: `ut-docs/architecture/lan-sync.md` D3/D3b updated; the
  `multitill.md` manual topic gained a step on the new stock behaviour,
  in all four locales (`web/help/{en,ar,fa,tr}/multitill.md`) — no
  screenshot regen needed for the prose itself (the underlying screens
  are unchanged), but `make docs-shots` still had to run in full because
  the surface hash also covers every non-test `internal/pages/**.go` file
  this diff touches; the resulting screenshot diffs are the same
  known-incidental class already accepted in PR #213/#214 (unrelated
  dynamic content in `alerts`/`designer`/`sell`, not this change).

## Independent review (Opus subagent) — findings and outcomes

The reviewer did not write the implementation, re-derived every factual
claim against the actual code rather than trusting comments, personally
mutation-tested the two headline regression tests (revert → fail with
the right symptom → restore → pass), and ran the full gate + `-race`
itself. **1 blocking, 6 should-fix, 3 nitpicks — all ten resolved:**

1. **Blocking, fixed:** `internal/pos/sales.go` was left gofmt-dirty by
   the doc-comment insertion (HEAD was clean; this diff broke it, and the
   repo's CI has no gofmt step to have caught it). `gofmt -w`.
2. **Should-fix, fixed:** the headline two-till regression test
   (`TestTwoTills_SameLastUnit_BothSalesSucceed_StockNegative_OneProblem`)
   didn't actually pin the replica bypass — its chosen numbers
   (replica cache seeded to 1, selling 1) happen to pass the *old* gate
   too (`1 + (-1) = 0`, not negative), so reverting the fix wouldn't fail
   this test. Reseeded the replica's local cache to 0 instead (still
   representing real staleness — the replica's own copy lagging behind
   what actually happened on the other till), which the old gate
   *would* reject (`0 + (-1) = -1`) and the fix correctly lets through.
   Mutation-verified: reverting the bypass now fails this test with the
   field-report's own symptom.
3. **Should-fix, fixed:** negative stock wasn't surfaced on the primary's
   own direct sale when the gate was *on* but stock still went negative —
   two basket lines for the same item with different modifier
   signatures don't merge (ADR-0020) and are each checked against the
   *same* pre-sale figure, so a combination can individually pass the
   per-line gate and still land the item negative with nothing logged.
   `warnIfStockNegative` on the primary branch is now called
   unconditionally (previously gated behind `saleInput.AllowNegativeInventory`),
   matching ADR-0036's own unconditional wording ("whichever till's
   application of a movement takes shop-wide stock negative").
4. **Should-fix, fixed:** `warnIfStockNegative` warned on *every* sale of
   a chronically-negative item, not just the first — within ~50 sales of
   one stuck-negative item it would flood and evict the 50-entry Problems
   ring (`internal/logging.recentCap`), pushing out unrelated problems
   (plugin crashes, sync failures). Now warns only on the transition into
   negative: the pre-sale level is reconstructed as `post + l.Qty`
   (exact for a `"sale"`, no second DB read to race against), and an
   already-negative pre-sale level skips the warn.
5. **Should-fix, fixed:** the "exactly one Problem" assertions counted
   matches against the *whole* `logging.Recent()` buffer — a
   process-global ring with no reset, shared by the entire test binary.
   Mutation-verified failing under `-count=2`. My first fix attempt
   (before/after length diff) turned out **not** to be sufficient either:
   this package's test suite has other tests whose background loops keep
   logging ("database is closed") well after they return, churning —
   and, once the ring overflows its own 50-entry cap — silently breaking
   a length-based diff regardless of the actual scenario. Landed on the
   more direct fix from the review's own alternative suggestion: a
   test-only `logging.ResetRecent()`, called right before each scenario.
   Re-verified: the three affected tests pass individually, in the full
   package suite, and under `go test -count=5` on the targeted set — 15/15
   green, including under the exact interleaving that broke both the
   original and my first-attempt fix.
6. **Should-fix, addressed (documented, not restructured):** ADR-0036's
   own consequences note says the replica-side gate "should be removed
   [on that path], not just skipped." The implementation force-sets the
   flag `true` instead of removing the gate call structurally, because
   `AllowNegativeInventory` is *also* the primary's own policy switch —
   splitting "disabled by shop policy" from "bypassed because this till
   doesn't own the number" into two fields is a real refactor across
   every `CompleteSale` caller, out of proportion to this fix. Recorded
   that reasoning directly in the field's doc comment (not just here),
   per the reviewer's own suggested resolution.
7. **Should-fix, fixed:** the manual (`multitill.md`, all locales) and
   `ut-docs/architecture/lan-sync.md` hadn't been updated for a real
   change in what a shop owner experiences (a joined till no longer ever
   refuses a sale for insufficient stock) — the standing ut-docs#324 rule
   requires this in the same branch, and `guard-help-topics.sh` doesn't
   catch prose staleness (only structure/route coverage). Both updated;
   `make docs-shots` re-run in full (see "What shipped" above for why a
   prose-only, no-visual-change edit still needed it).
8. **Nitpick, fixed:** a comment claimed "the primary re-checks on
   journal replay" — it doesn't re-check, it force-applies and then
   warns. Reworded to "the primary re-derives the shop-wide level from
   the arriving journal and surfaces any negative."
9. **Nitpick, fixed:** an unused `primaryURL` binding in `pos_api.go`;
   simplified to a direct comparison.
10. **Nitpick, fixed:** replica refunds/returns didn't nudge the push
    loop (only tenders did) — an inconsistency against ADR-0011's "sale
    journal" scope, which includes returns. Added the same nudge to
    `refund_page.go` and `inventory_api.go`.

## Independently re-verified TDD claims

- Personally reverted the replica bypass in `pos_api.go` and
  re-ran `TestTenderHandler_ReplicaDirectSaleNeverBlocksOnLocalStock` —
  failed with the "insufficient stock" toast, as expected; restored,
  green again.
- Personally reverted `warnIfStockNegative`'s two call sites — all three
  Problem-surfacing tests failed with "got none"/"got 0"; restored,
  green.
- Ran the full targeted set at `-count=5` (15 runs across the three
  Problem-surfacing tests) after the `ResetRecent()` fix — 15/15 green,
  confirming the fix actually holds under repetition, not just once.
- The reviewer's own mutation tests (bypass removal, warn-site removal,
  push-nudge removal) are recorded in their findings above and were not
  re-run a second time here — re-deriving an already-independently-run
  mutation test doesn't add signal; the fixes for what they surfaced are
  what's re-verified.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` on every file this diff touches — clean (post-fix; see
  Finding 1).
- `go test ./...` — clean except
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, the
  known pre-existing sandbox-only failure (root-sandboxed test runner
  ignores the read-only directory permission the test relies on) —
  confirmed the *only* failure, package untouched by this diff.
- `go test -race -count=1` on every touched package
  (`internal/pages`, `internal/pages/common`, `internal/pos`,
  `internal/logging`) — clean. Specifically checked: `Deps.SyncPushNow`
  is written once during boot (`StartSyncPush`, called from
  `pages.Init`) strictly before the server starts accepting requests, so
  there is no window for `RequestSyncPush` to race the channel's own
  initialization; a nil channel is guarded explicitly (early return, not
  a permanent-block send).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` — all pass.
- Money/tax: confirmed no `money.Money` arithmetic touched anywhere in
  the diff (this is a stock/sync-layer change only).
- Scope: `git diff --stat` shows exactly the Go/test files, the
  `multitill.md` manual topic in all four locales, `lan-sync.md`, and the
  docs-shots regen artifacts (manifest + the three known-incidental
  screenshot diffs) — nothing else.

## Verdict

**Safe to merge.** One blocking (mechanical, gofmt) and six should-fix
findings from an independent Opus review, all resolved and
re-verified — including a real second-pass bug in my own first fix
attempt for Finding 5, caught by re-running under the same interleaving
conditions that broke the original. Closes universaltill/ut-docs#404.
