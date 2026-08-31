# Code review — cross-till order visibility & status writes (ut-docs#1350)

- **Date:** 2026-08-31
- **Branch:** `feat/1350-cross-till-orders`
- **Reviewer:** independent Opus review, two rounds (fresh subagent context
  each round, no shared history with the Sonnet/Fable dev pass) — hard-tier
  routing per `scrum-master`'s model-routing table.
- **Verdict:** Round 1 found 1 blocker + 4 should-fix findings, all fixed.
  Round 2 (scoped to the fixes) was still in progress when this record was
  first written; its outcome is appended below before merge.

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

_Appended once complete._
