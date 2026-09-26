# Review: idle auto-lock vs background polls (ut-docs#2901)

- **Date:** 2026-09-26 · **Lane:** lane:cloud-54 · **Complexity:** medium
- **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, own worktree)
- **Branch:** `fix/2901-background-polls-no-touch`

## What shipped

The idle auto-lock (on by default, 10 min) never fired on any `base.html`
page. Timer-driven htmx polls go through `auth.Middleware` → `Service.Resolve`,
and `Resolve` refreshes `sessions.last_seen_at`. That covers the status chips
every 5–30 s and the sale-screen watchers every 3–5 s. The client idle timer's
jump to `/login` also touched the still-live session and 303'd back to `/`.
So the till reloaded every idle window instead of locking.

- `Service.ResolveNoTouch` (`internal/auth/service.go`): the same lookup,
  idle revoke and audit as `Resolve`, but it never writes `last_seen_at`.
- `backgroundPollPaths` (`internal/auth/middleware.go`): the single list of
  timer-driven GETs. The middleware resolves them with no touch. After
  revocation they still get 401 + `HX-Redirect`.
- `displayBoardPoll`: the kitchen display, `/ui/orders` and
  `/ui/kiosk-counter-orders` keep extending the session, as they always have.
  These screens are watched, not touched. Whether display devices should
  auto-lock is a follow-up product decision (ut-docs#2935).
- `/login`'s "already signed in" check uses `ResolveNoTouch`
  (`internal/pages/auth_page.go`).
- `input-heartbeat.js`: its `AbortSignal.timeout` call is now guarded, and
  its header records that it is the idle lock's activity signal for taps
  that never reach the server.
- Help: step 2 of `web/help/{en,de,fa,ar,tr}/users.md` "Idle auto-lock".
  Architecture doc: ut-docs `architecture/pos-auth.md`.

## Tests (TDD — each seen failing with the real error first)

- `TestBackgroundPollsNeverExtendTheSession`: every listed poll path, three
  rounds, inside the window → 200 and `last_seen_at` unchanged. Past the
  window → 401 + `HX-Redirect: /login`, and the session stays dead.
- `TestRealRequestStillExtendsTheSession`: `/`, `/ui/buttons`,
  `/api/window/input-heartbeat` and `/settings` still extend the session.
- `TestDisplayBoardPollsStillExtendTheSession`, including path-boundary
  negatives.
- `TestResolveNoTouch`: no write, still revokes, still audits.
- `TestLoginVisitDoesNotExtendAnIdleSession` (pages, real migrated DB).
- `TestEveryPollerIsClassified` (guard): scans `web/ui/**/*.html` and
  `internal/**/*.go` for timer `hx-trigger`s (both quote styles; Go comments
  skipped), plus `sell-screen-watch.js`'s `VERSION_URL`, plus any other
  `setInterval` in `web/public/*.js`. It fails on an unclassified poller or
  a stale list entry. Proven by temporarily adding a fake
  `hx-trigger='every 9s'` poller to `tables.html`: the test failed and named it.

## Driven run (real binary, headless Chromium, `auth.idle_lock_minutes=1`)

| build | sale screen left untouched 100 s | navigations |
|---|---|---|
| `origin/main` | still on `/`, no keypad | `/` → (`/login` 303) → `/` at 66 s: the "refresh" |
| this branch | `/login`, PIN keypad shown (screenshot read) | `/` → `/login` at 55 s, then stays |
| this branch, a tap every 20 s | still on `/` | none |

Polls seen during the run: `/ui/main-till-status` ×11,
`/ui/open-orders-badge/watch` ×18, `/ui/buttons/version` ×10, and the
30-second chips.

Surfaces looked at: the login keypad at 1280×800, light theme, English.
Not looked at: other themes, RTL and kiosk sizes. The markup is unchanged;
only help text changed.

## Findings (Fable)

| # | severity | finding | outcome |
|---|---|---|---|
| 1 | should-fix | `input-heartbeat.js` called `AbortSignal.timeout` unguarded. On older WebViews it throws on every tap, and the heartbeat is now load-bearing for the idle lock | **Fixed**: feature-guarded; header comment updated |
| 2 | nit | `displayBoardPoll` was test-only | **Fixed**: the middleware branches on it explicitly (this also cleared the deadcode-baseline guard) |
| 3 | nit | Guard blind spots: single quotes, JS timers, EventSource | **Fixed**: single quotes now matched; `setInterval` files flagged; limits written into the test doc |
| 4 | nit | The lock can now appear up to `min(60s, window/4)` before the client timer | **Documented** in `pos-auth.md` (expected: the server is authoritative) |
| 5 | nit | Product edges: a pairing wait of more than N minutes, a tables page used as a host-stand display, a customer taking more than N minutes on a card terminal | **Deferred** to ut-docs#2935 (display-board decision) |

The reviewer also verified the following. The TDD claims hold (behaviour
lines reverted → all three tests fail with the claimed errors). The poller
census is complete, and no classified path is click-triggered. There is no
payment-terminal polling in core. The change only ever shortens a session.
Path matching is exact, so `//` or trailing-slash variants fall to the old
branch. Server idle ≥ client idle, so there is no split between a keypad in
one tab and a session still live in another. The de/fa/ar/tr translations
are accurate.

## Gate

gofmt, build and vet are clean. Tests pass (`go test ./...`, the CI flags).
golangci-lint reports 0 issues. The CI build job's guards pass. Locally, the
deadcode guard reports only `logging.Stderr` / `timestampWriter.Write`, which
are used by `cmd/unitill-desktop`. The container has no GTK headers to build
that, and `origin/main` shows the same result.

**Verdict:** safe to merge.
