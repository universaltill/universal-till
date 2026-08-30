# Code review — revoke acting session when a till enters self-order mode (ut-docs#1259)

- **Date:** 2026-08-30
- **Branch:** `fix/1259-self-order-mode-revoke-session`
- **Reviewer:** independent reviewer (Opus, different model from the Sonnet
  implementer — `complexity:medium` per the model-routing table)
- **Verdict: SAFE TO MERGE.** Two blocking findings from the independent
  review were fixed in this branch before merge; everything else was either
  confirmed clean or filed as a follow-up.

## What shipped

Follow-up from the `ut-docs#1253` review
(`docs/code-reviews/2026-08-28-self-order-exit-pin-bypass.md` finding 1).
`POST /api/settings/display-mode` wrote `display.mode` and nothing else — no
session revoke, no cookie clear. So after a till switched into
`display.mode=self_order` (customer-facing, auth-exempt: `/self-order`,
`/api/self-order/*`), the browser that made the switch kept a fully valid
session. On any till where the OS chrome/URL bar is reachable (not a
locked-down kiosk appliance), a customer could type `/settings` directly and
reach it with the stale session — for a stale manager/admin session,
`checkOrElevate`/`canPerform` returns `allowed` outright, so they could
mutate settings with zero PIN.

**Fix, first pass:** `POST /api/settings/display-mode` now revokes the
acting session server-side (`auth.Service.Logout`) and clears the response
cookie when the new mode is `self_order`, mirroring `POST /api/auth/logout`.
`register`/`backoffice` are untouched — that operator needs to stay signed
in, and someone must be able to log back in normally afterward.

**Fix, second pass (from the independent review, see below):** revoking the
session alone regressed the kiosk. `/` is not auth-exempt — it only
redirects to `/self-order` for a request that resolves a *live* session
(`registerIndex`'s `mode == "self_order"` branch). Revoking the session
meant `/` now sent the acting browser to `/login` instead, and — worse —
every real kiosk launcher (`packaging/linux/unitill-kiosk-launch.sh`,
`cmd/unitill-desktop/desktop.go`, the Android app's `MainActivity.kt`) opens
`/`, not `/self-order` directly, so a self-order kiosk device would sit on
the staff PIN keypad indefinitely after any reboot, with no anonymous route
to the kiosk at all. Fixed with a new `auth.Service` seam,
`SetAnonymousRootRedirect` (same "callback installed by `pages.Init`, auth
stays SQL-free" pattern already used for `SetIdleLockAudit`) — a non-empty
answer sends an unauthenticated `GET /` there instead of `/login`.
`pages.Init` wires it to return `/self-order` when `display.mode ==
"self_order"`, so both the just-switched acting browser and a freshly
rebooted kiosk land on the (already auth-exempt) kiosk landing without ever
needing a session.

Also corrected a comment in `auth_page.go` that the fix made stale (it
claimed self-order mode "never logs the till out", which is no longer true
for the *acting* session — the exclusion it explains is still needed, but
now because *other* live sessions on the same till survive, not because none
do), and clarified `index_page.go`'s comment about which layer now owns
routing an anonymous `/`.

## Independent review findings (Opus, worktree-isolated subagent)

### Blocking 1 — `guard-docs-shots.sh` would have failed CI

`internal/pages/settings_page.go`'s change touches the app surface the
manual's screenshots are hashed against. Reviewer proved it with a
revert/restore pair (guard red with the fix, green without it). **Fixed:**
ran `make docs-shots` and committed the regenerated `web/help/img/**` +
`manifest.json`.

### Blocking 2 — after the switch, the till landed on the PIN keypad, not the kiosk

Detailed above. **Fixed:** the `SetAnonymousRootRedirect` seam. Reviewer's
own probe (`/` with the old cookie, and a genuinely anonymous `/`, both
303-ing to `/login` pre-fix) is now covered by two new assertions in
`TestSelfOrderMode_RevokesActingSessionOnEntry` plus a new regression test,
`TestAnonymousRoot_StillGoesToLoginWhenNotSelfOrderMode`, pinning the
ordinary (non-self-order) case stays unchanged.

### Non-blocking, addressed

- **NB-1 (stale comment)** — `auth_page.go`'s comment claiming self-order
  mode never logs anyone out. Rewritten to describe the actual post-fix
  invariant (the acting session is revoked; other sessions on the till are
  not).
- **NB-3 (no test for the landing behaviour)** — same work as blocking 2;
  covered by the new assertions.

### Non-blocking, filed as follow-ups (not this branch's scope)

- **NB-2** — only the *acting* browser's session is revoked; a second
  manager signed in on another LAN device keeps a valid session and can
  still reach `/settings` on a self-order till. Read as the ticket's
  intended scope ("revoke the acting session") and defensible (you
  shouldn't kick a back-office manager off their own laptop because a till
  flipped modes elsewhere), but undocumented and untested as a deliberate
  choice. Also folds in the reviewer's catch that `index_page.go`'s comment
  (pre-existing, now removed) claimed a kiosk browser opens `/self-order`
  directly — no launcher in this repo actually does that.
- **NB-4** — `web/help/en/display.md` never documented the Register / Back
  office / Self-order kiosk device-profile selector at all (pre-existing
  gap), and this change adds new user-visible behavior to it (switching to
  self-order now signs that screen out). Needs a manual update across all
  four locales (`en`/`fa`/`ar`/`tr`), not just `en`.

Both filed as `ut-docs` Backlog cards after this PR (see the issue's
close-out comment for links).

### Nit

- **N-1** — `d.AuthSvc != nil` is checked inside the same `if` as the cookie
  read in `settings_page.go`; reviewer confirmed no actual nil-panic risk
  (an earlier `canPerform` call would already have panicked first) and left
  it as a style note, not worth changing.

## Verification performed

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l .` | pass / pass / empty |
| `go test ./internal/pages/...` (full package) | pass |
| `go test ./internal/auth/...` | pass |
| `go test ./...` (whole repo) | pass |
| `-race` on the new/related tests | pass |
| `bash scripts/ci/guard-data-access.sh` | pass |
| `bash scripts/ci/guard-kiosk-engine.sh` | pass |
| `bash scripts/ci/guard-plugin-menu-read.sh` | pass |
| `bash scripts/ci/guard-i18n.sh` | pass |
| `bash scripts/ci/guard-compliance-claims.sh` | pass |
| `bash scripts/ci/guard-docs-shots.sh` | pass (after `make docs-shots`) |
| `bash scripts/ci/guard-help-topics.sh` | pass |
| `bash scripts/ci/guard-webkit-version.sh` | pass |
| `bash scripts/ci/guard-kiosk-launch-flags.sh` | pass |
| `bash scripts/ci/guard-android-status-address.sh` | pass |
| `bash scripts/ci/guard-android-i18n.sh` | pass |
| `bash scripts/ci/guard-emoji-font.sh` | pass |
| `bash scripts/ci/guard-htmx-loaded.sh` | pass |
| `bash scripts/ci/guard-autofill-suppression.sh` | pass |
| `bash scripts/ci/check-brand-assets.sh` | pass |
| `bash scripts/ci/guard-makefile-version.sh` | pass |

### TDD re-verification (done twice, independently)

**Revoke logic alone** — reverted only `internal/pages/settings_page.go`,
confirmed the package still compiled, re-ran
`TestSelfOrderMode_RevokesActingSessionOnEntry`:

```
--- FAIL: TestSelfOrderMode_RevokesActingSessionOnEntry (0.32s)
    self_order_mode_test.go:580: switching to self_order must clear the
    session cookie in the response, got
```

Genuinely red for the reported reason. Restored, green again.

**Landing-redirect seam alone** — reverted `internal/auth/middleware.go`,
`internal/auth/service.go`, `internal/pages/init.go` (keeping the revoke fix
and the new tests), re-ran the same tests:

```
internal/pages/self_order_mode_test.go:547:6: svc.SetAnonymousRootRedirect
undefined (type *"…/internal/auth".Service has no field or method
SetAnonymousRootRedirect)
FAIL	github.com/universaltill/universal-till/internal/pages [build failed]
```

Confirms the tests exercise the actual seam, not a vacuous pass. Restored,
full suite green again (`go test ./...`, whole repo).

## Checked and found clean

- No raw SQL outside `internal/data`/tests (`guard-data-access`).
- No new user-facing string — Go-only routing/session-handling change
  (`guard-i18n`).
- No new route, no template/locale file touched (`guard-help-topics`
  structural check passes; the content-level manual gap is NB-4, filed
  separately).
- `register`/`backoffice` modes genuinely unaffected — gated on
  `rawMode == "self_order"`, captured before the `register` → `""` collapse.
- Audit log attribution (`settingsAudit`) is unaffected — it uses
  `elev.ActorID`, captured by `checkOrElevate` *before* the revoke, off the
  request context the middleware already populated; it never re-resolves a
  session after the cookie is cleared.
- Elevated case (a cashier who needed a manager PIN override) revokes the
  *requesting* browser's own cookie, not the approver's — the right choice,
  since the requesting browser is the one that must go anonymous.
- No nil-`AuthSvc` panic risk in any existing `registerSettings(mux, dp)`
  test call site — every one either sets `AuthSvc` or never exercises
  `mode=self_order` with a real cookie.
- `setSessionCookie` is genuinely reused, not duplicated (defined once in
  `auth_page.go`).
- Session revocation is DB-backed (`RevokeSession`/`LookupSession`), so a
  *separate* `auth.Service` instance in the tests sees it immediately — no
  in-memory cache to go stale, no false-pass risk.

## Merge

`merge_method: "merge"` (never squash/rebase — ut-docs#250), after CI is
green on the PR.
