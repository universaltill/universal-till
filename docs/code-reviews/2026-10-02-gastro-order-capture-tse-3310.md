# Gastro order capture gets its own fiscal.order.start (ADR-0138, ut-docs#3310)

## What shipped

A new, additive plugin extension point, `fiscal.order.start`
(`internal/plugins/manifest.go`), dispatched once per **genuine new order
capture** so a German signer can open a `Bestellung-V1`-shaped TSE
transaction at the point AEAO zu §146a says the process begins — distinct
from the existing `fiscal.sign.start`/`fiscal.sign.ask` ("Kassenbeleg",
ADR-0077), whose trigger points are entirely unchanged by this card.

- Two call sites: `internal/pages/hold_api.go`'s `parkCurrentBasket`
  (fires only on a genuine first park — `origin.IsZero()` — never on a
  re-park of an already-held order; this one hook covers both held and
  table orders, since a table order is a held sale with `TableID` set),
  and `internal/pages/self_order_shop.go`'s `completeCounterOrderCheckout`
  (every pay-at-counter checkout).
- Dispatch mechanics mirror `fiscal.sign.start` exactly: background
  goroutine via `d.AsyncWork`, `EventBus.Ask` (not `Publish` — the event
  name has no `.ask` suffix), `HasSubscribers` zero-cost fast path,
  known-offline short-circuit, 15s safety ceiling
  (`fiscalOrderStartAsyncTimeout`), only a validated
  `{"status":"acknowledged","tx_id":"…","tx_revision":N}` answer persisted.
- New table `fiscal_order_starts` (migration `059_fiscal_order_starts.sql`,
  `CREATE TABLE IF NOT EXISTS` per this repo's convention since migration
  021; checksum pinned in `shipped_migrations_test.go`), repo methods
  `RecordFiscalOrderStart`/`GetFiscalOrderStart` in `internal/data/fiscal_repo.go`,
  classified per-till (not admin-synced) in `sync_admin_repo.go`.
- New optional `order_id` field, `omitempty`, on **both**
  `fiscal.sign.start` and `fiscal.sign.ask`'s request payloads — echoes the
  captured order's own id when the sale being tendered was resumed from a
  held/table/counter order with a matching `fiscal_order_starts` row, so a
  signer or auditor can join the order's `Bestellung-V1` transaction to its
  eventual `Kassenbeleg`. New `pos.SaleInput.HeldOriginID` (in-memory only)
  carries the correlating id from the engine actually being tendered
  (`completeTender` reads `engine.HeldOrigin().ID` off its own parameter,
  never `d.Engine`, so a kiosk sale or a refund/return never borrows the
  cashier basket's order id).
- `fiscal.order.start` joins the existing 3-member fiscal-signing
  exclusivity group (`FiscalSignExclusiveEvents`, now 4 members) at both
  install/update and enable time.
- One UI touch, non-visual: the hold form (`web/ui/pages/index.html`)
  gains `hx-include="#offline-flag"` so a known-offline hold skips the new
  dispatch, mirroring the existing tender/kiosk-checkout convention. No new
  string, no new control, no visible change.
- Docs: `ut-docs/reference/contracts/fiscal-sign-ask.md` bumped to 1.12.0
  (new `fiscal.order.start` sibling section, the `order_id` field
  documented on both existing payloads, the reserved-but-undispatched
  `fiscal.order.cancel` named explicitly as not yet built).

**Deliberately not built this cycle** (ADR-0138 D2/D4/D5, each a named
follow-up):
- `fiscal.order.cancel` — this cycle's own research found no
  cashier-initiated cancel/void action for a held, table or counter order
  anywhere in the codebase to hook it to (every existing delete path is
  cross-till sync cleanup, the resume-to-tender path, or failed-park
  rollback). Named/reserved in comments and the contract doc only; not
  dispatched, not in the exclusivity group. Building a real cancel action
  is filed as its own product/UX follow-up.
- `fiscal.order.update`, satellite order capture (blocked on ut-docs#1154,
  unbuilt), the `ut-plugin-tax-de` handler (blocked:env, mirrors
  ut-docs#1521's precedent), and the DSFinV-K export (blocked on
  ut-docs#39/#38, roadmap).

## Independent review

Fable subagent (card labelled `complexity:hard`, built at Opus — review at
a different model per `MODEL-ROUTING.md`), isolated worktree branched from
a `WIP: pre-review snapshot` commit on this branch. Ran independently, in
its own detached worktrees (orchestrator's checkout never touched):
`gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test -count=1 ./internal/plugins/... ./internal/pages/... ./internal/data/... ./internal/pos/... ./internal/db/...`
(all green — plugins ~273s, pages ~181s, data ~41s, pos ~20s, db ~41s),
`golangci-lint run ./internal/...` (0 issues), and
`guard-data-access.sh`/`guard-i18n.sh`/`guard-kiosk-engine.sh`/`guard-core-neutral.sh`
(all pass).

Independently re-verified the load-bearing TDD claim, not just trusted:
moved the `dispatchFiscalOrderStart` call in `parkCurrentBasket` out of the
first-park-only branch so it fired on every park; confirmed
`TestFiscalOrderStart_FirstParkDispatchesReParkDoesNot` fails with
`a re-park of an already-held order must NOT re-dispatch fiscal.order.start,
got 2 dispatches`; restored the code (`git status --porcelain` clean
against HEAD) and confirmed the test passes again. The orchestrating
session (this review's author) separately ran the same mutation once more
before handing off to review, and re-ran the full gate fresh (`-count=1`,
no cache) after recovering from an unrelated in-session mishap (a
`git checkout --` accidentally reverted `hold_api.go`'s legitimate Dev
changes while cleaning up that same mutation test; reconstructed by hand
from the diff captured earlier in-session and re-verified identical
before re-running the gate) — both runs green.

### Findings — triaged

**Left as-is, documented here rather than fixed (all judged genuinely
safe, not deferred-because-ignored):**

1. **nit** (`internal/pages/fiscal_sign_hook.go`, inside
   `dispatchFiscalSignAsk`) — `fiscalOrderIDFor`'s `data.NewPOSRepo(d.Db)`
   argument is built before that function's own
   `heldOriginID == "" || !HasSubscribers(...)` guard runs, so a till with
   a `fiscal.sign.ask` signer but no `fiscal.order.start` subscriber (every
   real signer until `ut-plugin-tax-de` catches up) pays two small
   allocations it never uses. Nanoseconds against a payload build that
   already allocates far more; the zero-plugin-cost guarantee itself (a
   till with NO signer at all) is unaffected and asserted by
   `TestFiscalSignAsk_ZeroPluginTillAllocatesNothing`. Not fixed — the
   existing repo variable at the call site one line above could be reused
   in a future pass if this ever matters.
2. **minor, design observation** (`internal/pages/self_order_shop.go`,
   `completeCounterOrderCheckout`) — `fiscal.order.start` fires right after
   the `kiosk_counter_orders` row exists, before the park write. If the
   park then fails (ut-docs#2714's own cleanup path), a Bestellung-V1
   transaction may already have been started for an order that no longer
   exists. This is exactly the same "start fired, process then abandoned"
   class ADR-0077 D1 and this card's own contract section already carry as
   an accepted open vendor question (whether SIGN DE needs/supports a
   cancel for an abandoned transaction) — not a new gap, and ADR-0138 D2
   explicitly scopes this call site as "every pay-at-counter order
   creation." No code change; worth keeping in mind if the vendor answer
   ever lands.
3. **nit, test coverage** — no test drives `completeTender` with
   `d.KioskEngine` while `d.Engine` simultaneously holds a resumed order
   (the literal cross-engine-bleed scenario). Verified by direct code
   reading instead: `completeTender` reads `engine.HeldOrigin().ID` off its
   own parameter (never `d.Engine`), and `RestoreHeld` — the only setter of
   a held origin — has exactly one caller, on `d.Engine`, so the kiosk
   engine can never carry an origin to borrow from. Not fixed; the existing
   `TestFiscalOrderStart_OrderIDNotBorrowedFromCashierBasket` already
   proves the dispatch-level half of this at the `SaleInput` boundary.

**Verified sound, not findings:**

- Exclusivity group has exactly 4 members, both install-time (16
  directional pairs) and enable-time (4×4) tests extended correctly;
  `fiscal.order.cancel` confirmed absent from the group (comments only).
- Migration 059 correctly additive, checksum correctly pinned (the
  comment-stripped checksum the test actually computes, not raw file
  sha256 — checked, not assumed); `fiscal_order_starts` correctly
  classified per-till in `sync_admin_repo.go`.
- `hx-include="#offline-flag"` references a real, always-rendered element;
  `app.js` already folds the manual override and `navigator.onLine` into
  that same input, so including it is sufficient — no further wiring
  needed.
- No file writes anywhere in the diff (grepped, not assumed) — only DB
  writes through repository methods; no cwd-relative path, no missing
  `MkdirAll`.
- Zero `web/locales`/`web/help` changes; the only template change is a
  non-visual attribute — no manual update owed.
- No real client/shop name, no literal secret.
- All SQL confined to `internal/data`/`internal/db` (guard-data-access
  confirms).

## Beyond automated tests

- Read the full diff against ADR-0138 end to end (not just the parts the
  Dev subagent's own report called out) to confirm no drift and no quiet
  scope creep into the explicitly-deferred items (cancel, update,
  satellite, tax-de, DSFinV-K) — none found.
- Ran the real browser-driven Playwright specs for the touched flows
  (`kiosk-counter-order-held-2703.spec.ts`,
  `open-orders-counter-tabs-2703.spec.ts`, `hold-named-tab.spec.ts`) — all
  6 tests pass, confirming the `hx-include` change didn't break the hold
  form or the counter-order flow in a real browser, not just in Go's
  httptest harness.
- Personally traced `fiscalOrderIDFor`'s guard ordering and the
  `SaleInput.HeldOriginID` plumbing by hand, independent of the subagent's
  own account, before accepting the "never borrowed from the cashier
  basket" claim.

## Verdict

**Safe to merge.** Build/vet/gofmt/lint clean; full test suite green
across every touched package (fresh, uncached, confirmed twice
independently — once by this session, once by the Fable reviewer in an
isolated worktree); every relevant CI guard passes; the real e2e suite for
the touched flows passes in a real browser; the load-bearing TDD claim
(re-park never re-dispatches) was independently falsified and restored by
two separate parties. Zero blockers, zero majors; three minor/nit
observations, all judged genuinely safe to ship as-is and documented above
rather than silently dropped. Tax-advisor sign-off (ADR-0044 D5) remains
required before any of this reaches a live German till — unaffected by
this card, unchanged from ADR-0077's own standing gate.
