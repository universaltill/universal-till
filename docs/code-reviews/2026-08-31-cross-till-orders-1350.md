# Code review — cross-till order visibility & status writes (ut-docs#1350)

- **Date:** 2026-08-31
- **Branch:** `feat/1350-cross-till-orders`
- **Reviewer:** independent Opus review, two rounds (fresh subagent context
  each round, no shared history with the Sonnet/Fable dev pass) — hard-tier
  routing per `scrum-master`'s model-routing table.
- **Verdict:** Round 1 found 1 blocker + 4 should-fix findings, all fixed.
  Round 2 (scoped to the fixes) confirmed the blocker fix correct in both
  directions, found finding 2's fix narrower than intended (real code gap,
  not just docs), two comment-wording nits, and one CI-blocking gap
  (`guard-docs-shots.sh` stale after the round-2 doc edits) — all fixed
  below. **Safe to merge.**

## Context

ut-docs#1350 originally read "orders should be manageable (collect/cancel)
from other tills, not just the self-order kiosk." Live-device testing by the
reporter (see the issue's own comments) found the collect/cancel UI already
exists (`internal/pages/order_status.go`, ut-docs#526) — the real gap,
confirmed by reading the actual sync code, is that the Orders board is
**till-local**: `sales`/`order_status` are deliberately excluded from the
primary↔replica admin-bundle sync (`internal/data/sync_admin_repo.go`), and
the only sales-sync path (`internal/pages/sync_sales.go`) is a one-way
replica→primary journal push, once, at sale creation — a later
`order_status` UPDATE never re-syncs. An order was visible/actionable only
on the till that created it and (once journaled) the primary.

## Design

Route reads/writes through the **primary as single authoritative writer**,
synchronously, with silent local fallback — not a new bidirectional sync
channel:

- New primary-side, bearer-authed (`syncTill`) endpoints in
  `internal/pages/sync_orders.go`: `GET /api/sync/orders` (mirrors
  `ListRecentOrders`) and `POST /api/sync/orders/{receipt_no}/status`
  (mirrors the guarded write, factored into a shared `applyOrderStatusCore`
  so the human-facing and sync-facing handlers can never drift).
- Replica-side proxy in `internal/pages/order_status.go`:
  `GET /ui/orders` and `POST /api/orders/{receipt_no}/status` try the
  primary first (short 3s-timeout client — this is a foreground page
  load/tap, not a background retry loop); ANY failure falls straight
  through to today's local-DB path, unchanged (offline-first, ADR-0003).
- Concurrency needs no new resolution mechanism: once writes route to the
  one primary DB, the pre-existing `pos.OrderStatusAllowed` forward-only
  ladder + `ApplyOrderStatus`'s `_txlock=immediate` transaction already
  serializes/resolves races.
- No new ADR: this extends ADR-0011's existing shape (primary-authoritative
  writes, replica proxies/falls back), doesn't contradict it. Reasoning
  posted on the issue before Dev started.

## Round 1 findings and fixes

1. **BLOCKER — both new `/api/sync/orders*` routes were missing from
   `internal/auth.exempt()`.** `auth.Middleware` runs before the mux, so it
   401'd the replica's proxy calls before `syncTill` (the endpoints' own
   bearer check) ever ran — verbatim the failure class the middleware's own
   comment already documents (`/api/sync/stock` was missing once, silently
   breaking inventory sync shop-wide until found in the field). The whole
   feature was a silent no-op in any deployment with login enabled — i.e.
   every real deployment; my own first live two-process verification ran
   with `UT_AUTH=off` and could not have caught this.
   **Fix:** added `/api/sync/orders` to the exact-match switch and a
   segment-bounded rule for `/{receipt_no}/status` (mirroring the existing
   `/api/sync/pair-requests/{id}` pattern — bounded to exactly one segment
   so it can never widen to swallow a future `/api/sync/orders/{id}/<other
   action>` route). Pinned in `TestSyncPullPathsAreExempt`
   (`internal/auth/middleware_test.go`), positive and negative cases.

2. **SHOULD-FIX — a primary-sourced row's `/journal/{receipt_no}` link
   404s on a replica** (that till never held the sale locally; sales only
   ever journal forward from wherever they were rung up, never back down).
   **Fix:** `orderRow` gained `JournalLinkable` (false for the whole batch
   when it came from the primary); `orders_list.html` renders plain text
   instead of a link for those rows.

3. **SHOULD-FIX — a proxied write attributed only to the calling till,
   never the real operator** — an accountability loss on every cross-till
   status change (this repo's audit posture, German pilot, cares who did
   what). **Fix:** the replica now sends its own session `actor_id`
   alongside `status`; the primary validates it against **its own** users
   table (`data.AuthRepo.GetUser`) before trusting it — resolved → journal
   + audit actor become the real operator; unresolved (older replica, no
   session, or an id this till's users table doesn't have) → falls back to
   the till, exactly as before. `source_till` now always rides the audit
   payload on a sync-applied write, independent of whether the operator
   resolved.

4. **SHOULD-FIX — the "no regression" framing for the local-fallback path
   was inaccurate.** A status change applied while a replica is
   disconnected doesn't just "fail to propagate" — once the primary is
   reachable again, the very next 15s poll replaces the board with the
   primary's rows (which never saw the offline write), so the tap can
   **visibly revert** on that till's own screen. That's a real,
   till-visible behavior this feature introduces, not a carry-over of a
   pre-existing gap. **Fix:** rewrote the code comment and the help docs
   (en, plus model-translated ar/fa/tr) to state this plainly, and to note
   a till's own brand-new order can be briefly invisible on its own board
   until it journals to the primary.

5. **Test gaps** — added exemption-pinning tests, non-200/malformed-JSON
   primary-response fallback tests (the actual production failure shape —
   connection-refused alone wasn't the whole "ANY failure" contract), and
   the two actor-attribution tests (resolved + unresolved).

## Verification

- `gofmt -l .` clean, `go build ./...` / `go vet ./...` clean.
- `go test ./internal/pages/... ./internal/auth/... ./internal/data/...
  ./internal/pos/...` — all green (cache cleared, re-run after every fix
  round).
- `-race` on all 26 new/changed order-status, sync-orders and
  auth-middleware tests — all pass.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-help-topics.sh` — all clean.
- **Two real, live two-OS-process runs** (built the actual binary, ran a
  genuine primary + replica as separate processes, killed/restarted to
  test the offline path):
  - First run, `UT_AUTH=off`: proved the proxy/fallback mechanism itself —
    a sale that exists only on the primary is visible and actionable from
    the replica over real HTTP with real bearer auth; the write lands on
    the primary (0 local rows/events on the replica); killing the primary
    makes the replica fall back cleanly (200 local view / 404 for an
    unknown local receipt, no hangs or errors).
  - **Second run, auth genuinely ON** (no `UT_AUTH=off`): this is the run
    that actually exercises the real deployment shape and is what
    surfaces finding 1's class of bug. Logged in on the replica via
    `POST /api/auth/login` for a real session cookie, then confirmed
    `GET /ui/orders` and `POST /api/orders/{r}/status` correctly proxy to
    the primary using only `sync.bearer` (no session cookie crosses to the
    primary) — pre-fix this 401'd at the replica's own middleware before
    the proxy code ever ran. Also confirmed live: the dead-link fix (no
    `<a href>` for the primary-sourced row) and both operator-attribution
    paths (unresolved → till name; resolved, with the user seeded on both
    DBs simulating normal admin-bundle sync → the real operator's display
    name).
- TDD claim re-verified personally (not just trusted): re-ran the full
  suite from a clean test cache after each fix, not only once at the end.

## Known, accepted limitation (stated precisely, not glossed over)

A status change applied via the replica's local fallback (primary
unreachable) is not queued or replayed — it only reaches the primary if
the same till later proxies a further change for the same receipt. See
finding 4 above and the code comment in `order_status.go` for the exact
mechanics and the revert behavior this can produce. Queuing/replaying the
offline write would need the general bidirectional sales-sync mechanism
this design was deliberately scoped to avoid; worth a follow-up card if it
matters in practice, not blocking here.

## Round 2 (scoped to the fixes above)

A fresh Opus pass, scoped to re-verifying rounds 1's 5 findings rather than
re-reviewing the whole diff. It read the exemption logic adversarially
(walked every boundary case by hand: `.../a/b/status`, `.../status/extra`,
`.../status` with an empty id, a bare `.../orders/status`) and confirmed
both directions by temporarily reverting each fix and watching the
matching test fail, then restored the tree and verified it matched
(`git status`, `gofmt`, build, tests) before reporting.

**1 (auth exemption): confirmed FIXED, both directions pinned.** One
residual, accepted note: a receipt number containing an encoded `/`
wouldn't match the segment-bounded rule and would fail closed to the local
fallback — not a hole (receipt numbers are `R-nnnn`), just recorded.

**2 (dead journal link): PARTIALLY fixed — real gap, not just docs.** The
original whole-batch `JournalLinkable = !fromPrimary` was broader than
intended: on a *connected* replica, the till's own sales are ALSO
primary-sourced (they journal to the primary same as any other), so the
blanket rule was killing a link that would have resolved locally, on every
row, whenever the primary is reachable — not just for genuinely-foreign
receipts. **Fixed for real** (not just re-worded) in
`internal/pages/order_status.go`: `JournalLinkable` is now checked **per
row** via `POSRepo.ReceiptExists` against this till's own local DB,
bounded to the existing 50-row page cap (cheap embedded-SQLite lookups on
a 15s poll, not a hot path); a lookup error fails closed to "no link"
rather than risking a 404. The help docs' existing wording ("except an
order shown here because it was taken on a *different* till") turns out to
describe exactly this corrected behavior accurately — a receipt this till
never rang up is the only receipt genuinely absent locally — so no further
doc changes were needed. New tests:
`TestUIOrders_ReplicaOwnJournaledSaleKeepsWorkingLink` (a replica's own
already-journaled sale keeps a working link even though the row is
primary-sourced) and an added negative assertion in
`TestUIOrders_ReplicaRendersPrimaryData` (a genuinely foreign row stays
link-free).

**3 (operator attribution): confirmed FIXED**, with two comment
corrections (no code change needed):
- The trust-boundary comment in `sync_orders.go` overstated what the
  existence check does — reworded to say plainly that a bearer-authed
  peer's claimed `actor_id` is validated for *existence* in the primary's
  users table, not further authenticated; a stronger check would need the
  endpoint to carry more than a till's own bearer, which it deliberately
  doesn't. Judged acceptable: the forgeable actions are low-stakes status
  taps (never money/fiscal), and `source_till` always rides the audit
  payload regardless, so an investigator can always see which till relayed
  a write.
- A stale comment in `order_status.go` claimed `actorID` "may be \"\"" —
  `getSessionUserID`'s `auth.UserID` fallback is `"system"`, never empty,
  so that branch was dead prose. Corrected.
- Round 2 also upheld the round-1 call not to namespace the till-fallback
  actor as `"till:"+till.ID` — the reachable collision is the two seeded
  literals `system`/`kiosk` (migrations 003/018), not an arbitrary UUID
  clash, but it only affects the *unresolved* fallback path and is
  cosmetically harmless (a mis-rendered display name, nothing else). Not
  worth the UX cost of showing a raw till id instead of its name.
- Noted, not fixed (pre-existing, out of this card's scope):
  `AuthRepo.GetUser` doesn't filter `is_active`, so a deactivated
  operator's id still resolves for attribution purposes.

**4 (revert-behavior prose): confirmed FIXED, accurate** — round 2 walked
the actual sequence (offline tap → primary reconnects → next 15s poll →
`fetchOrdersFromPrimary` replaces the whole list) against the real code
rather than reading for plausibility, and confirmed the described revert
is exactly as strong as documented, not milder or more severe.

**5 (test gaps): confirmed FIXED** — spot-verified by reverting the
underlying code and watching the specific new tests fail (the non-200
fallback guard, the `actor_id` field being sent, the unresolved-actor
fallback).

**New, CI-blocking finding: `guard-docs-shots.sh` was red.** The round-2
doc/code edits (help text, `order_status.go`, `orders_list.html`) landed
after the dev pass's `make docs-shots` run, leaving
`web/help/img/manifest.json` stale on the surface hash and all four
`order-status` topic hashes. **Fixed:** re-ran `make docs-shots` (92/92
screenshots regenerated); only `manifest.json` and the two
already-known-nondeterministic `sell.png` byte-diffs changed — no
`order-status` screenshot changed pixel content (the docs fixture's
context doesn't exercise the replica-proxy path), consistent with this
being a real UI-neutral change.

## Final verification (after round 2's fixes)

- `gofmt -l .` clean, `go build ./...` / `go vet ./...` clean.
- `go test ./internal/pages/... ./internal/auth/... ./internal/data/...
  ./internal/pos/...` — all green, cache cleared.
- `-race` on all 27 order-status/sync-orders/auth-middleware tests (added
  2 for the per-row link fix) — all pass.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  individually — all pass, `guard-docs-shots.sh` included this time.
