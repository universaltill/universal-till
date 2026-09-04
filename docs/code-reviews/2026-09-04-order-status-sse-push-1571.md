# Code review: order-status push via SSE (ut-docs#1571 / ADR-0079)

**Date:** 2026-09-04
**Card:** ut-docs#1571 — "Order status does not propagate between tills
immediately — the board is a 15s poll and the `OrderStatusBroadcaster` has
no subscribers." Reported directly by the pilot shop owner.
**Design:** [ADR-0079](https://github.com/universaltill/ut-docs/blob/main/adr/0079-order-status-push-via-sse.md)
(ut-docs, merged).
**Complexity:** `complexity:hard` — Dev at Fable, review at Opus (fresh
context, deliberately not Fable).

## What shipped

Server-Sent Events on top of the existing primary/replica HTTP trust
boundary (ADR-0011) — no new dependency either side:

- `internal/pages/order_status.go`: `streamOrderStatus`, the one shared SSE
  writer loop (subscribe-before-headers, `event: order-status` /
  `data: <json>` per `pos.OrderStatusChanged`, `: ping` heartbeat every 20s,
  unsubscribes on client disconnect / broadcaster close / write error).
  Registered as `GET /api/orders/stream` (session-authed).
- `internal/pages/sync_orders.go`: `GET /api/sync/orders/stream`, the
  bearer-authed sibling of the existing `/api/sync/orders`, calling the same
  shared writer.
- `internal/pages/order_status_stream_bridge.go` (new):
  `StartOrderStatusStreamBridge` — a replica holds
  `GET {primary}/api/sync/orders/stream` open (client `Timeout: 0`, a 60s
  idle cutoff reset on every byte read) and republishes every decoded event
  onto its own local broadcaster, so a page never needs to know whether
  it's on the primary or a replica. Bounded exponential backoff (2s→30s) on
  any failure, 30s re-check while not (yet) a replica, wired in `init.go`
  alongside `StartTSEProvisionRetry`.
- `internal/pos/order_status.go`: `OrderStatusChanged` gets snake_case JSON
  tags (now a wire type, not just in-process); `OrderStatusBroadcaster`
  gains `SubscriberCount()` (test-only) and `Close()` (wired to
  `app.Run`'s shutdown context — without it, every open SSE stream would
  hold `http.Server.Shutdown` to its full timeout).
- `internal/auth/middleware.go`: exact-path exemption for
  `/api/sync/orders/stream` (bearer-auth happens inside the handler, same
  as its `/api/sync/orders` sibling).
- `web/ui/pages/orders.html` / `web/ui/partials/orders_list.html`: a native
  `EventSource` triggers an immediate htmx re-fetch of the existing
  `/ui/orders` fragment on every event; the 15s poll is untouched and stays
  the offline-first fallback. No new user-facing strings.
- `e2e/tests-docs/docs-shots.spec.ts`: stubs the new stream endpoint
  (`fulfill({status: 204})`) so Playwright's `networkidle` heuristic can
  settle against a page that now opens a deliberately-never-ending
  connection.

## Independent review

Fresh-context **Opus** subagent (Dev was Fable, per this card's
`complexity:hard` routing — reviewing Fable with Fable would share the
author's blind spots). Ran, not just read: `gofmt`, `go build`, the full
`go test ./...`, all 35 CI-blocking guards from `ci.yml`'s `build` job,
and `go test -race` on the touched packages (green — the one `-race` run
that initially reported `FAIL` was the default 600s per-package timeout on
`internal/pages`, not a hang; a `-timeout 45m` re-run passed clean in
~789s, and CI itself doesn't run `-race` at all).

**TDD claims re-verified personally, for real** (revert → confirm the
specific failure → restore → confirm green), four times:
- `defer unsubscribe()` removed → `TestOrderStatusStream_UnsubscribesOnClientDisconnect`
  failed with a leaked-subscriber count.
- The bridge's `Publish` call removed → both
  `..._RepublishesPrimaryEvents` and `..._ReconnectsAfterPrimaryCloses`
  failed with "event never reached the replica's own broadcaster".
- `syncTill` gate removed from the sync stream handler →
  `TestSyncOrdersStream_RequiresBearer` hung to timeout (an unauthenticated
  caller got a live stream instead of a 401).
- The exempt-list entry removed → `TestSyncPullPathsAreExempt` failed with
  "is not exempt — this middleware will 401 it before the handler's bearer
  check runs".

**Verdict: safe to merge.** No blocking findings. Specifically checked and
found clean: `Subscribe`/unsubscribe on every exit path; no
send-on-closed-channel panic in the broadcaster (checked by inspection and
under `-race`); the auth exemption is an exact path match, not a widened
prefix, and `syncTill` still gates the handler; offline-first is untouched
(no `WriteTimeout` on the server, so SSE isn't periodically severed; the
15s poll and the primary-proxy path are unchanged); the htmx
`orders-push`/`every 15s` trigger doesn't accumulate listeners across the
fragment's repeated `outerHTML` swaps (verified against the vendored htmx
1.9.12's `deInitNode` cleanup); no real client/shop name or secret-shaped
literal anywhere in the diff; i18n clean (the script renders nothing,
`guard-i18n.sh` confirms).

**One gap found and fixed by the reviewer, in the same round** (no second
round needed — this was found in the first pass, not after a fix, so the
one-round default per `scrum-master`'s "Model routing by complexity" still
applies): the new idle-cutoff path (`orderStreamBridgeIdleCutoff`) had zero
test coverage — a primary that keeps the TCP connection open but goes
completely silent (a half-dead process, a NAT black-holing the return
path) is exactly the failure mode this card exists to fix, reintroduced
through a different door: the replica would sit on a live-looking socket
indefinitely with no push at all. Added
`TestOrderStatusStreamBridge_ReDialsASilentStream` (pins the cutoff at
100ms, a primary that answers 200 then says nothing at all, asserts a
re-dial); TDD-verified (fails with the cutoff/idle-reader code removed,
passes restored). Folded into this same diff.

**One hygiene fix, mine, in the same pass**: the three pre-existing bridge
tests that hold a connection open create their `context.CancelFunc` without
`defer`ing it, relying on reaching `stopBridge(...)` at the end of the
test. A `t.Fatalf` before that point skips it, so the test's own
`defer primary.Close()` then blocks for the full 60s idle cutoff waiting on
a connection nothing ever cancelled — cosmetic (only bites a failing run),
but a `defer cancel()` right after each `context.WithCancel` costs nothing
and removes the risk. Applied to
`..._RepublishesPrimaryEvents`/`..._ReconnectsAfterPrimaryCloses`/`..._PicksUpLateEnrolment`
(the fourth, `..._BacksOffOnRefusal`, never holds a connection open, so it
wasn't at risk).

## Explicitly not fixed here (judgment calls, not defects)

- **Help doc wording** (`web/help/en/order-status.md:27`, "refreshes
  itself every few seconds") is still factually true — the poll remains —
  but doesn't yet say cross-till changes now arrive in ~1s, which is
  exactly what the pilot's complaint was about. Any edit to that file trips
  `guard-docs-shots.sh` (screenshot-freshness), so improving the wording
  costs a full `make docs-shots` regen for a sentence. Judged a
  nice-to-have, not a blocker, since nothing currently reads as false;
  left as a candidate follow-up rather than expanding this diff.
- **No browser-level (two-tab) e2e** exercising the actual
  EventSource→`orders-push`→fragment-reload path — the existing Go test
  only asserts the script is present in the rendered page. The mechanism
  is proven end-to-end at the HTTP layer (browser-facing stream, sync
  stream, and the bridge all have real integration tests against a real
  `httptest.Server`), and separately live-verified by hand against a real
  running till (see below) — a real two-browser e2e would be a reasonable
  follow-up, not required to close this card.
- **Instant-close reconnect has no backoff** — a primary that accepts a
  connection and closes it immediately resets the backoff to minimum
  (`connected=true`), so that specific failure mode retries every 2s
  indefinitely rather than backing off. Bounded, LAN-local, `Debugf`-only;
  accepted as-is rather than adding complexity to distinguish "connected
  and useful for a while" from "connected and immediately useless".
- **No event replay** (`Last-Event-ID`) for a gap while a replica was
  disconnected — correct by design: the 15s poll plus the existing
  primary-proxied board already close that gap; SSE is additive, not a
  durability mechanism.

## Verified beyond automated tests

Live-driven by hand against a real running till (not just `httptest`):
started the server for real, seeded a real `sales` row, opened
`GET /api/orders/stream` with `curl -N`, POSTed a real status change via
`POST /api/orders/{receipt_no}/status` from a second terminal, and watched
the correctly-framed `event: order-status` / `data: {...}` SSE frame
arrive on the open connection in real time. Separately confirmed the `: ping`
heartbeat arrives for real over the wire on an idle connection (waited out
the full 20s interval). The primary↔replica bridge itself is exercised
against a real HTTP server in the automated tests (`httptest.Server`, real
sockets, real SSE framing) rather than mocked function calls; a full
second-till pairing flow (real enrolment, two live server processes) was
judged unnecessary given the bridge's actual networking code is already
under real-HTTP test, not simulated.

## Files changed

`internal/pos/order_status.go` (+tags, `SubscriberCount`, `Close`),
`internal/pos/order_status_test.go`, `internal/pages/order_status.go`
(+`streamOrderStatus`, `/api/orders/stream`),
`internal/pages/order_status_test.go`, `internal/pages/sync_orders.go`
(+`/api/sync/orders/stream`), `internal/pages/order_status_stream_bridge.go`
(new), `internal/pages/order_status_stream_bridge_test.go` (new),
`internal/pages/order_status_stream_test.go` (new), `internal/pages/init.go`
(wiring + shutdown), `internal/auth/middleware.go` (+exemption),
`internal/auth/middleware_test.go`, `web/ui/pages/orders.html`,
`web/ui/partials/orders_list.html`, `web/help/img/manifest.json`
(regenerated, screen is pixel-identical), `e2e/tests-docs/docs-shots.spec.ts`
(harness stub for the new endpoint).

## Safe-to-merge verdict

**Yes.** Full suite, all 35 CI guards, `-race` on every touched package,
and four independently-reproduced TDD reverts all green on the final tree.
