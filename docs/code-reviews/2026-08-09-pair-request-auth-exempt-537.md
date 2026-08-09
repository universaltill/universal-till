# 2026-08-09 — Multi-till pairing 401 on auth-enabled tills (ut-docs#537)

## What shipped

`internal/auth/middleware.go`'s session-auth allowlist (`exempt()`) was
missing the inbound ADR-0033 §8 pairing surface, so a joining replica —
which has no session on the primary at all, by design — was rejected 401
by the middleware before either handler ever ran:

- `POST /api/sync/pair-request` (the inbound pair request itself,
  unauthenticated by design: rate-limited + sha256-commitment-gated in
  the handler).
- `GET /api/sync/pair-requests/{id}` (the replica's possession-gated
  status poll while waiting for approval — found during this
  investigation, not in the original ticket text, by reading
  `pairStatusHandler`'s outbound call in `pairing_join.go`).

Fix: both added to `exempt()`. The first is an exact-match addition to
the existing switch. The second needed care — see below.

## Independent review (Opus, isolated worktree) — one blocker found and fixed

First pass used `strings.HasPrefix(path, "/api/sync/pair-requests/")` to
exempt the `{id}` status-poll route. Independent review (Opus, spawned
with `isolation: "worktree"`, briefed with the diff + both handler files)
found this **over-broad**: `pairing_api.go` registers two more routes
under that same prefix, one segment deeper —
`POST /api/sync/pair-requests/{id}/approve` and `.../deny` — and those
are **manager-PIN-gated**, meant to stay behind a required session
(`authorizePairingManager`). The plain prefix match exempted them too,
turning manager approval — ADR-0033 §8's own stated trust boundary for
inbound pairing — into an anonymous, un-rate-limited LAN PIN-guessing
oracle that also trips the device-wide login lockout shared with the
keypad (`internal/auth/service.go`'s `maxFailedAttempts`/
`lockoutDuration`), i.e. an anonymous LAN caller could lock every real
operator out of `/login` too.

The reviewer verified this live in their isolated worktree (throwaway
probe, not committed): anonymous approve with a wrong PIN returned 403
(handler ran) instead of 401 (middleware blocked); five wrong PINs
tripped the till-wide lockout; a correct-PIN anonymous approve then
succeeded with **zero session**. Confirmed absent on `origin/main` (same
probe returns 401 there — this diff introduced it, not a pre-existing gap).

**Fix, verified by both the reviewer and independently re-verified here**:
bound the prefix match to exactly one path segment —

```go
if rest, ok := strings.CutPrefix(path, "/api/sync/pair-requests/"); ok &&
    rest != "" && !strings.Contains(rest, "/") {
    return true
}
```

## Tests

- `internal/auth/middleware_test.go` — `TestSyncPullPathsAreExempt`
  extended: both new exempt paths pinned must-be-exempt; the bare
  `/api/sync/pair-requests` list AND `/api/sync/pair-requests/{id}/approve`
  + `/deny` pinned must-NOT-be-exempt (the last two pin the
  segment-boundary fix specifically, not just the bare-list boundary).
- `internal/pages/pairing_join_test.go` —
  - `TestPairingSurface_ReachablePastAuthMiddleware`: wraps a **real**
    primary mux in the **real** `auth.Middleware` (`UT_AUTH=on`) and
    drives a session-less `http.Client` through pair-request (200),
    the status poll with a wrong secret (404 from the handler, not 401
    from the middleware), the bare list (401 — still gated), and
    approve/deny with no session (401 — still gated, the exact case the
    review's blocker broke).
  - `TestPairingSurface_FullAuthenticatedRoundTrip`: the AC4 "end-to-end
    test that pairs against an auth-enabled primary" completed rather
    than reachability-only — a session-less replica sends the pair
    request, a **real** logged-in manager (real `/api/auth/login`, real
    session cookie via `httptest.Server` + `cookiejar`, real PIN check)
    approves it through the real middleware, and the replica retrieves a
    genuine token. Uses a fully-migrated schema (`appdb.Open`), not the
    simplified `seedForPages` fixture, since this test creates a real
    operator with a real PIN — `seedForPages`' users table has a `NOT
    NULL pin_hash` that doesn't match production's actual first-boot
    shape (same reasoning `TestPairingFlow_AgainstRealMigratedSchema`
    already established for this file).

This matters because **every existing "real primary" pairing test in this
codebase constructs the primary as a bare `*http.ServeMux`, never wrapped
in `auth.Middleware`** — verified by both the author and, independently,
the reviewer, by reading `pairing_join_test.go` and `setup_pairing_test.go`
line by line. Those tests would keep passing even if the exempt list
regressed again, which is exactly how this shipped broken in the first
place (every pairing test before this change ran with `UT_AUTH=off`, or
against an unwrapped mux, or both).

## Verified beyond automated tests

- Real revert-then-restore, twice: once for the base fix (both new tests
  fail with the exact claimed 401s pre-fix, pass post-fix), once for the
  segment-boundary fix specifically (temporarily widened back to the
  bare-prefix version — both `TestSyncPullPathsAreExempt` and
  `TestPairingSurface_ReachablePastAuthMiddleware` catch the regression
  with the exact approve/deny 403-not-401 symptom the review found;
  restored and re-confirmed green).
- Full `go build ./...` / `go test ./...` green.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-help-topics.sh`, `guard-i18n.sh` —
  all green.
- `gofmt -l` clean; `go vet ./internal/auth/... ./internal/pages/...`
  clean; `-race` clean on both touched packages.
- Full ADR-0033 pairing surface re-derived from the handler code (not
  just the ticket's own audit checklist): `pairStartHandler` →
  `pair-request`; `pairStatusHandler` → `pair-requests/{id}`;
  `completeJoin` → `/api/sync/enroll` + `/api/sync/snapshot`, both
  already exempt/bearer-gated. Nothing else is called before the replica
  holds a bearer token.
- No real client/shop name in test data (`"Till 2"`, generic).
- Routing/middleware-only change, no new file I/O, no page/template/
  locale-visible behaviour beyond "a pairing operation that always
  silently 401'd now works" — no `web/help/` topic update needed
  (`guard-help-topics.sh` confirms no route coverage gap either way).

## Safe-to-merge verdict

Safe to merge. One blocker was found and fixed pre-merge by independent
review; the fix is verified (build/test/guards/race, plus a live
revert-then-restore of both the base fix and the blocker fix
specifically). No items deferred.
