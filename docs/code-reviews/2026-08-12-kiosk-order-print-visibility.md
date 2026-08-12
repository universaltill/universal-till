# Code review — kiosk order print-failure visibility (ut-docs#517 part a)

- **Date:** 2026-08-12
- **Branch:** `feat/kiosk-order-print-visibility-517`
- **Card:** ut-docs#517 **part (a) only**. Part (b) (cross-till order view) is
  tracked separately as ut-docs#550 and is *not* in this change.
- **Reviewer:** independent review pass (Opus) on a diff written by a different
  model (Fable), in an isolated worktree — the author's own reasoning was not
  reused.

## What shipped

The problem: a kiosk order whose kitchen ticket never printed (paper out,
printer unplugged) was invisible on the till. A paid order could be silently
lost, because print failures only ever produced an `audit_log` row that nobody
reads.

1. **`/orders` polls.** The live-order fragment re-arms a 15s poll on itself
   (`web/ui/partials/orders_list.html`), so new orders — including self-order
   kiosk ones — appear without a page reload.
2. **Print failures are persisted per sale.** Migration `034` adds two nullable
   `sales` columns, `kitchen_print_failed_at` / `receipt_print_failed_at`:
   NULL = "no attempt has failed", RFC3339 timestamp = "the most recent attempt
   failed". Written through new `POSRepo.SetKitchenPrintFailed` /
   `SetReceiptPrintFailed`, cleared on the next successful attempt.
3. **The failure is surfaced.** A ⚠ warning cell on the `/orders` row, in its
   own column so the one-tap status buttons' `innerHTML` swap cannot wipe it.
4. Manual topic `order-status` updated in all four shipped locales.

## Findings

### 1. Print failures were dropped in exactly the reported scenario — HIGH, fixed

`printKitchenAsync` / `printReceiptAsync` recorded the failure using the **same
context the print attempt had just exhausted**.

An out-of-paper / unplugged / hung printer does not fail fast. Both transports
block until the print context's deadline — `deviceTransport.Print` selects on
`ctx.Done()`, `networkTransport.Print` hands the deadline straight to
`SetWriteDeadline` — so when `printKitchen`/`printReceipt` return that error,
their context is **already expired**, and `database/sql` drops any write made
with it before it reaches the DB. Result: no audit row, no ⚠ on `/orders`, the
paid order silently lost — the precise bug this card exists to fix, in the
precise case it was reported for. (It also silently lost the *pre-existing*
`kitchen_print_failed` / `print_failed` audit rows, so the fallback trail was
gone too.)

Fixed by writing the failure on a fresh short-lived context
(`recordPrintFailureCtx`, `internal/pages/print_api.go`), used for both the
audit row and the flag at both call sites. The success path keeps the print
context (a successful print proves it had not expired).

Regression test: `TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired` — a
substituted print that blocks until the (shortened) budget runs out, exactly
like the real transports. Two small test-only seams (`printAsyncTimeout`,
`printReceiptFn`/`printKitchenFn`) exist only for it.

### 2. The receipt warning could never be cleared — MEDIUM, fixed

The manual kitchen reprint set/cleared its flag; `POST /api/print/receipt/{receiptNo}`
(the Journal's reprint button) did not touch the receipt flag at all. A receipt
only auto-prints once, at tender, so the Journal reprint is the **only** later
attempt a shop can make — a receipt ⚠, once set, stayed on `/orders` forever.
The shipped manual text explicitly promised the opposite ("a successful print
clears the warning"). A permanent warning is a warning nobody reads.

Fixed by mirroring the kitchen handler in the reprint endpoint. Regression
test: `TestManualReceiptReprintSetsAndClearsPrintFailedFlag`.

### 3. `guard-docs-shots.sh` red — BLOCKING (CI), fixed

The report said "all 5 CI guards green", but `.github/workflows/ci.yml` runs
**20**. The manual's screenshot-freshness guard was **green on `main` and red on
this branch** (verified by recomputing the guard's hashes against `32ebb9cf`):
the `/orders` screen and the `order-status` topic prose both changed, and
neither the screenshots nor `web/help/img/manifest.json` were regenerated. CI
would have failed the PR.

Fixed by running the real harness (`playwright test --config=playwright.docs.config.ts`
+ `tests-docs/write-manifest.js`) — 64 screenshots, 16 topics × 4 locales, then
the manifest. All 20 CI guards are now green locally.

Note for whoever runs this next in a sandbox: `npx playwright install chromium`
is blocked by the egress policy (403 on `cdn.playwright.dev`). The identical
build is reachable from `storage.googleapis.com/chrome-for-testing-public/…`
and can be unpacked into `/opt/pw-browsers/chromium-<rev>/` by hand.

### 4. The manual promised an action that does not exist — MEDIUM, fixed (prose)

The new note told the shop owner to "print that order again — a successful print
clears the warning". True for receipts (Journal reprint button); **not** true for
kitchen tickets: `POST /api/print/kitchen` has no UI trigger anywhere in
`web/ui/**`, so there is no way for staff to retry a kitchen ticket from the
product. Prose corrected in en/fa/tr/ar to say what the operator can actually
do: a kitchen ⚠ means the kitchen never got the ticket, so pass the order on
directly; a receipt ⚠ clears on a successful reprint from the Journal.
Migration 034's comment (which named only the kitchen endpoint) updated too.

**Deferred, worth a card:** a "reprint kitchen ticket" control on the `/orders`
row would make the warning actionable instead of merely informative. Deliberately
not added here — that is new feature work, not a review fix.

### 5. Both warnings collided on one line — LOW, fixed

Adjacent inline `<span>`s wrapped into an unreadable
"⚠ Kitchen print failed ⚠ Receipt print failed" when a sale carried both (seen
in a real driven run, not in review of the markup). Each warning is now its own
block. Confirmed in en and fa/RTL.

### Checked, no finding

- **Money:** the diff touches no monetary value anywhere — this is print-status
  metadata only. `internal/money.Money` rule not engaged.
- **Offline-first:** both flag writes stay inside the existing fire-and-forget
  `d.AsyncWork` goroutines; nothing was hoisted onto the synchronous tender
  path. Checkout is not blocked by a printer or by these writes.
- **"No attempt ≠ failure":** verified by reading both call sites. The
  `!KitchenEnabled()` and `!cfg.Enabled() || !cfg.AutoPrint` early returns sit
  *before* any flag access, so a disabled printer neither sets a false failure
  nor clears a real one.
- **Attack surface:** no new routes at all. `/api/print/*`, `/orders` and
  `/ui/orders` are all session-gated — `internal/auth/middleware.go`'s exempt
  list covers `/self-order`, `/api/self-order/*`, sync and setup only. The flag
  is only ever written server-side from the print goroutines or the manual
  reprint handlers; no client input reaches it. `guard-kiosk-engine.sh` green.
- **Migration hygiene:** `034` is additive, append-only, and both new columns
  are nullable, so existing rows and shops that never hit a failure are
  unaffected. The three "upgrade path" tests in `internal/db` were extended in
  exactly the established per-migration idiom (compare 029/032/033 in the same
  functions).
- **Recurring pipeline bug classes:** N/A here and confirmed — nothing in this
  change writes a file, so there is no missing `os.MkdirAll` and no
  cwd-relative path where `paths.Data(...)` belongs.
- **No real client/shop name, no secret-shaped literal** anywhere in the diff.
- Pre-existing `gofmt` drift exists in 5 files, none of them touched here.

## Verified beyond the automated suite

- **TDD re-verification of the author's claim, performed personally.** Reverted
  the flag writes in `printKitchenAsync`/`printReceiptAsync` in an isolated
  worktree, re-ran `TestAsyncPrintFailureSetsFlagAndSuccessClears`: it failed
  with a real assertion (`failed kitchen print must set KitchenPrintFailedAt,
  got empty`), not a compile error or a skip. Restored, re-ran, green; working
  tree verified clean afterwards. The revert→run→restore ran as one atomic
  command so no turn boundary could commit the reverted state (ut-docs#386).
- **TDD verification of my own two fixes**, the same way: with them reverted,
  `TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired` fails on all three
  assertions (both flags *and* both audit rows lost — confirming finding 1 cost
  the audit trail as well) and
  `TestManualReceiptReprintSetsAndClearsPrintFailedFlag` fails on the set.
  Restored: both green.
- **A real driven run of the real app**, not a mock: seeded till, printer
  configured to unopenable device paths (enabled, so a genuine attempt is
  made), real scan + real `POST /api/pos/tender`. Both ⚠ warnings appeared on
  `/orders` through the full production path. Screenshotted en (light) and
  fa (RTL) — RTL mirrors correctly, no `left`/`right` leakage.
- `go build ./...`, `go vet ./...`, `go test ./...` — all green.
- `go test ./internal/db/... ./internal/data/... ./internal/pages/... -race` — green.
- **All 20 CI guards** from `.github/workflows/ci.yml` run locally — green.
- Manual prose re-read against the code in all four locales, not just checked
  for presence (that is how findings 2 and 4 surfaced).

## Verdict

**Safe to merge.** Three real defects were found and fixed (one of them
defeated the feature in its headline scenario, one made CI red), plus two
smaller correctness/legibility fixes. Nothing outstanding is blocking.

Merge with `merge_method: "merge"` — never squash or rebase (ut-docs#250).

## Explicitly deferred (out of scope for this card)

- **Cross-till order view** — ut-docs#550, part (b) of #517, still open.
- **SSE / push** instead of a 15s poll.
- **A kitchen display (KDS) screen** — ut-docs#516.
- **A nav-wide banner** for print failures, rather than a per-row chip.
- **A "reprint kitchen ticket" control** so a kitchen ⚠ is actionable from the
  UI (finding 4). Worth a backlog card.
- The `order-status` manual screenshot shows an empty till ("No orders yet"), so
  it does not illustrate the new warning. Pre-existing property of the docs
  harness for this topic, not a regression from this change.
