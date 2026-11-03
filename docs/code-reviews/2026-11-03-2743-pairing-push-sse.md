# Code review — pairing push to the Tills page over SSE (ut-docs#2743, slice A)

Date: 2026-11-03. Lane `lane:cloud-41`. Pipeline: BA → Architect (Opus) →
UX (design-time) → Dev (Sonnet) → Tester (Opus) → Review (Opus, independent).
Slice B (the joining till's `request_secret` pending link) is split out to
ut-docs#5297.

Reviewer: Opus 5.5 (code written by Sonnet). Scope: branch `feat/2743-pairing-sse` against `main`. The fixes below are in this branch.

## What shipped
- `internal/pairingpush`: a payload-free broadcaster. It caps subscribers
  (16), gives each a one-slot buffer that drops on full, and has an
  idempotent cancel and Close.
- `GET /api/pairings/stream`: session-authed and gated by `sync_management`.
  A replica gets 204 and a client over the cap gets 503 with Retry-After.
  The handler subscribes before writing headers. It sends `event: pairing` /
  `data: {}` on each change and a `: ping` every 20s. It is excluded from the
  session touch, is on the demo-mode denied list, and is not auth-exempt.
- Publishes happen on create, approve and deny. Create also arms an
  `AfterFunc` that fires at TTL+1s, and boot rescans pending rows to re-arm
  those timers. The broadcaster is closed on `bgCtx.Done`.
- `web/public/pairing-stream.js` opens one EventSource per page, only when
  the notice partial renders `data-pairing-stream`. A nudge fires
  `tills-changed`. The `#pending-pairings` poll and the push are guarded by
  `utPairingBusy()`.
- The notice partial is now always rendered for an authorised primary
  manager, because it carries the marker. The notice itself still needs
  `.Count`.
- Help prose for multitill is updated in all 5 locales.

## Findings

| # | Sev | Where | Scenario | Status |
|---|-----|-------|----------|--------|
| R1 | **major** | `web/public/pairing-stream.js:36` (`!document.body.classList.contains('kiosk')`) | `body.kiosk` comes from `{{ if kiosk }}`, which is `httpx.InitKiosk(UT_KIOSK=1)`, the *dedicated till* flag. It is not the self-order kiosk. On a Pi or dedicated main till, the deployment UX names as where managers approve, the stream therefore never opened and the feature was silently off. Self-order customer pages (`self_order.html`, `self_order_shop.html`) do not load base.html or this script, so the check guarded nothing. A node harness confirmed that the original script creates 0 EventSources when `body.kiosk` is set. Nobody's driven run used UT_KIOSK=1. | **Fixed**: removed the check. The marker gating (session plus `sync_management` plus primary) is the only gate, as the arch AC11 intends. Test assertion added. |
| R2 (=F1) | **major** (raised from minor) | `pairing-stream.js:61-65` (`sync()` on every `htmx:afterSettle`) | After a CLOSED error (403/204/503/401), `es = null`, and the next swap (about 12 settles at page load, then every chip refresh) opens a new EventSource. With the stream over the cap, the harness measured 12 attempts in 18s, against at most one per 30s intended. Every tab beyond the cap hammers the 503. | **Fixed**: `RETRY_MS = 30000`. On CLOSED the script sets `retryAt = Date.now() + RETRY_MS`, and `sync()` opens only when `Date.now() >= retryAt`. Transient drops (CONNECTING) are still left to the browser's own reconnect. Harness: 1 attempt in 18s, 1 retry after 30s. A Go source assertion was added. |
| R3 | **major** | `internal/pages/pending_pairings.go` stream loop | AC7 says that when the session expires, locks or is revoked, "the stream ends". The middleware checks the session only at connect, so a stream on a signed-out or idle-locked session kept receiving nudges and held a cap slot until the tab closed. The order-status precedent has the same gap, but there it is bearer/board scoped. | **Fixed**: added `pairingStreamSessionCheck`, which uses `auth.Service.SessionCheck`, the same precedent as `agent_confirm.go`. On every heartbeat the stream returns when the session is no longer live or the user id has changed, so the browser's reconnect meets the middleware's 401 and goes CLOSED. Behind it, R2 caps retries. With UT_AUTH=off there is no cookie and the check is skipped. New test: `TestPairingStream_EndsWhenSessionSignedOut`. |
| R4 | minor | `pairing-stream.js` `!want && es` branch | When a tab is hidden the script closes the stream and resets `opened`. The re-open after the tab becomes visible did not nudge, so changes made while it was hidden showed only on the next 30s poll, which contradicts the arch "+catch-up trigger via onopen". | **Fixed**: a `catchUp` flag is set when an opened stream is closed for visibility, and onopen nudges once. Checked with the harness. |
| R5 | minor | `pairing_api.go:schedulePairingExpiryNudge` | Each create arms an untracked 10m+1s `AfterFunc`, and these timers are not stopped at shutdown. When one fires after Close, `Publish` on the empty map is a no-op, so this is safe. The timers are unbounded only by the per-source pair-request rate limit, the same bound as the DB rows they mirror. | Accepted. Optional follow-up: coalesce into a single earliest-expiry timer. |
| R6 | minor | `pending_pairings.html` busy guard | A push that arrives while the manager is mid-PIN is dropped, not deferred. The row catches up on the next 30s poll. The help text says "updates once you are done", which is accurate if it means "within 30s". | Accepted. |
| R7 | info | `pairing_stream_test.go:162,188` | The tests swap the package var `pairingExpiryAfter`. No pages test that hits pair-request uses `t.Parallel`, and `-race` is clean. | Accepted. |

## Verified (not just read)
- `go build ./...`, `go vet` on pairingpush, pages and auth: clean. `gofmt -l internal/`: clean.
- `go test -race ./internal/pairingpush/ ./internal/auth/`: ok.
  `go test -race ./internal/pages/ -run 'Pairing|PendingPairing|Stream|Notice'`:
  ok, before and after the fixes.
- TDD re-check: I put back the original `pending_pairings.go` and
  `pairing-stream.js`. `TestPairingStream_EndsWhenSessionSignedOut` then
  failed ("stream kept running after the session was signed out") and
  `TestPairingStreamJS_SingleSourceNoUserText` failed (the kiosk assertion).
  After restoring the fixes, both pass.
- JS behaviour was checked with a throwaway node harness: a fake
  EventSource and clock, run against both the original and the fixed
  script. The original fails 4 of 9 checks (kiosk, F1 retry storm, hidden
  catch-up, reconnect nudge in the kiosk-off run). The fixed script passes
  9 of 9.
- The cap slot is never leaked. Every refusal (403, 204, no flusher, nil
  broadcaster, over cap) returns before `Subscribe`, and `defer cancel()`
  covers every return after it. There is no write-after-close, because the
  handler is the only writer and Close only closes the channels.
- Every pairing state change publishes: CreatePendingRequestWithRole,
  ApproveWithRole and Deny are only called from `pairing_api.go`, and
  `sync_api.go` only reads (RoleForToken).
- Shutdown: `dp.PairingPush.Close()` runs on `bgCtx.Done` and ends open
  streams, the same as for OrderStatus.
- Auth: the middleware accepts the cookie only (no bearer), the route is
  not exempt, and the demo-mode denied list includes it. A display-board
  path is not a board request, so a narrowed board session is revoked
  rather than served (an existing test covers this).
- Guards: i18n, i18n-meta, data-access, kiosk-engine, no-showmodal,
  rtl-logical-css, no-inline-handlers, help-topics, help-drift,
  help-breadcrumbs, help-terms and compliance-claims all OK. The httpx
  shell/HeadAsset tests are OK.
- Recurring bugs: the diff adds no file writes and no cwd-relative data
  paths. The only file reads are in tests, which use `chdirRoot`.
- Offline-first: the stream is same-origin and local, and failures are
  silent. Polls stay in place, and no modal or banner was added.


## Tester (driven run, before review fixes)

- The binary was built from the branch and run with auth on and a fresh
  data dir, then driven with Playwright.
- Push timings:
  - A new request appeared on /tills after 77 ms and in the /settings
    notice after 93 ms, without waiting for the 30 s poll.
  - Approve in one tab cleared it in the other tabs in about 131 ms; deny
    cleared in 69 ms.
- PIN guard: with a PIN typed (and with a role changed), a push did not
  replace the table. It caught up on the next 30 s poll.
- Fallback: with the stream forced to fail, the row still arrived on the
  30 s poll. No banner or dialog appeared.
- EventSource: exactly one on /tills and on /settings, and none on the
  cashier sale page or on /self-order.
- Stream responses: 401 without a session, 403 for a cashier, 200
  `text/event-stream` for a manager.
- Not driven, covered only by unit tests: expiry, the replica 204, the
  session-touch exclusion and the 16-stream cap.
- Not checked: real touch and the on-screen keyboard, dark theme and RTL.
  Everything was emulated.

## Gate after the review fixes (orchestrator)

`gofmt -l .` and `go build ./...` were clean. `golangci-lint run ./...`
reported 0 issues. These guards passed: i18n, no-showmodal, rtl-logical-css,
data-access, kiosk-engine, help-drift, help-topics, page-http-error and
core-neutral. Full `go test ./...` result: see the PR.

## Verdict

Safe to merge once CI is green. R5 (coalescing expiry timers into one) is an
optional follow-up and is not filed.

## Found by CI after the review (fixed on this branch)

| # | Sev | Where | Scenario | Status |
|---|-----|-------|----------|--------|
| C1 | **major** | `web/public/pairing-stream.js` | Failing test: e2e `session-display-board-idle-2935`. An idle manager session on `/orders` is narrowed to board-only requests, and any other request revokes it. The pairing-notice poll is a shell request a board-only session may make, and it still rendered the stream marker. The stream request, and the heartbeat `SessionCheck`, then revoked the session, so the board dropped to /login. | **Fixed**: the script never opens on a display-board page (`/orders`, `/kitchen-display/<id>`). This mirrors `auth.displayBoardPage` and is pinned in `TestPairingStreamJS_SingleSourceNoUserText`, which was seen red first. |
| C2 | major (harness) | e2e harnesses | The open stream kept Playwright's `networkidle` from ever resolving, which timed out the locale audit. A first fix used `page.route` with a 204, but that turned off the HTTP cache: persistent-shell refetched assets and the zoom/snapshot timings broke. | **Fixed**: `stubPairingStream()` in `e2e/tests/fixtures.ts` replaces the pairing EventSource with an inert, CLOSED object through `addInitScript`. The page fixture applies it, and so do the two specs that build their own context. docs-shots and the locale audit keep their 204 route, as they already do for `/api/orders/stream`. Locally, the 5 failing specs now pass (31 tests). |
