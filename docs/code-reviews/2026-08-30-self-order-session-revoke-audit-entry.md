# Code review — dedicated audit entry for the self-order session revoke (ut-docs#1303)

- **Date:** 2026-08-30
- **Branch:** `fix/1303-self-order-revoke-audit-entry`
- **Reviewer:** independent reviewer (fresh read, no prior involvement in this
  branch)
- **Verdict: SAFE TO MERGE.** No blocking findings.

## What shipped

Follow-up from `ut-docs#1259` (`docs/code-reviews/2026-08-30-self-order-mode-revoke-session.md`).
`POST /api/settings/display-mode` (`internal/pages/settings_page.go`) already
revoked the acting browser's session when switching `display.mode` to
`self_order`, but that revoke was only implicitly covered by the existing
`display_mode_changed` audit entry — unlike `POST /api/auth/logout`, which
writes its own dedicated `"logout"` action.

This change adds one call:

```go
if c, err := r.Cookie(auth.CookieName); err == nil && d.AuthSvc != nil {
    d.AuthSvc.Logout(r.Context(), c.Value)
    settingsAudit(r, posRepo, elev, "user", elev.ActorID, "self_order_session_revoked", nil)
}
```

placed inside the same `cookie-present && d.AuthSvc != nil` block that already
gated `Logout()`, so it fires only on the `self_order` branch and only when a
session actually existed to revoke; `register`/`backoffice` never call it.

Two new tests in `internal/pages/self_order_mode_test.go`:
- `TestSelfOrderMode_RevokedSessionWritesDedicatedAuditEntry` — real
  login → switch to `self_order` → asserts both `self_order_session_revoked`
  and `display_mode_changed` audit rows == 1.
- `TestSetDisplayMode_BackofficeWritesNoRevokeAudit` — switch to
  `backoffice` → asserts `self_order_session_revoked` == 0,
  `display_mode_changed` == 1 (regression guard for the non-revoking modes).

## What I independently verified

### Attribution correctness (`elev.ActorID`)

Read `settingsAudit` (`internal/pages/settings_page.go:132`) and
`checkOrElevate`/`elevationCheck` (`internal/pages/elevation.go:58-136`)
directly rather than trusting the diff's comment:

- `elev.ActorID` is set once, from `getSessionUserID(r)` at the top of
  `checkOrElevate` (elevation.go:99), **before** any elevation branching —
  it is the current session's own user id on the `allowed` path, and stays
  the *originally-blocked* session user (not the approver) on the `elevated`
  path (elevation.go:95-97, 135).
- `settingsAudit` itself picks the right insert: `InsertAudit(actorID=elev.ActorID, ...)`
  when not elevated, `InsertAuditElevated(actorID=elev.ApproverID,
  blockedActorID=elev.ActorID, ...)` when elevated (settings_page.go:132-143).
  So the new call's dual attribution automatically matches the existing
  `display_mode_changed` entry written two lines later from the same `elev` —
  same primary actor, same blocked-actor id, in both outcomes.
- The `entityID` argument passed to `settingsAudit` for the new entry is also
  `elev.ActorID` — i.e. `entity_type="user"`, `entity_id=<the session user
  whose cookie is actually being revoked>`. That's correct in the elevated
  case too: `r.Cookie(auth.CookieName)` reads the *requesting* browser's
  cookie regardless of elevation outcome (an approver's PIN never mints a
  second session), so the cookie handed to `Logout()` always belongs to
  `elev.ActorID`, never the approver.
- **No staleness risk:** `elev` is computed once at settings_page.go:1121,
  well before both the `Logout()` call and the new audit call (1146-1157).
  It's a plain value-type struct (a string + two more strings), not a live
  session lookup, so revoking the session afterward cannot retroactively
  change what it holds.

### Gating vs. `POST /api/auth/logout`

Traced the difference between this handler's gating (cookie presence) and
`/api/auth/logout`'s (`svc.Resolve` succeeding) and confirmed it's not a bug:

- `/api/auth/logout` is **exempt** from `auth.Middleware`
  (`internal/auth/middleware.go`'s `exempt()`, the `/api/auth/` prefix case)
  — it can be reached with a missing, stale, or already-expired cookie, so
  its handler needs `svc.Resolve(...)` as its own validity gate before
  writing a `"logout"` entry for a revoke that may not have actually
  revoked anything.
- `POST /api/settings/display-mode` is **not** exempt. `auth.Middleware`
  itself calls `svc.Resolve(r.Context(), c.Value)` on every request to this
  route and 401s (JSON, since it's under `/api/`) before the handler ever
  runs (middleware.go:133-145) if that fails. So by the time
  `registerSettings`'s closure executes, the cookie already resolved to a
  live session moments earlier in the same request. Re-reading it with
  `r.Cookie(...)` and gating on presence is not a weaker check here — it's
  effectively equivalent to "a session was actually revoked," because the
  precondition was already enforced one layer up. Matching `auth_page.go`'s
  `svc.Resolve` pattern here would be redundant, not more correct.
- `d.AuthSvc == nil`: gated identically to the existing `Logout()` call
  (same `if` condition), so the new audit line skips exactly when the revoke
  itself would have skipped — symmetric, no drift possible.
- `Logout` itself (`internal/auth/service.go:264`) is a no-op on an empty
  token and swallows `RevokeSession`'s error — no panic risk from calling it
  on an edge-case token.

### Build / vet / tests (isolated worktree)

| Check | Result |
|---|---|
| `go build ./...` | pass |
| `go vet ./internal/pages/...` | pass |
| `gofmt -l .` | empty |
| `go test ./internal/pages/... -run 'TestSelfOrderMode_RevokedSessionWritesDedicatedAuditEntry\|TestSetDisplayMode_BackofficeWritesNoRevokeAudit\|TestSelfOrderMode_RevokesActingSessionOnEntry\|TestLogoutRevokesSessionAndClearsCookie' -v` | all 4 pass |
| same 4 tests with `-race` | all 4 pass |
| `go test ./...` (whole repo) | pass |

### TDD re-verification (independent, in an isolated worktree)

Reverted only `internal/pages/settings_page.go` to its pre-#1303 content,
leaving the new test file untouched, and re-ran the two new tests:

```
--- FAIL: TestSelfOrderMode_RevokedSessionWritesDedicatedAuditEntry (0.28s)
    self_order_mode_test.go:693: self_order_session_revoked audit rows = 0, want 1 (actor 994cb5ba-a797-4115-89b6-b70d1a83add2)
--- PASS: TestSetDisplayMode_BackofficeWritesNoRevokeAudit (0.29s)
```

Genuinely red for the reported reason (`want 1, got 0`) — the backoffice
regression guard correctly stays green since it never expected the new
entry. Restored and re-ran: both tests pass again.

### CI-blocking guards (`.github/workflows/ci.yml`'s `build` job, current list)

All 16 run and pass: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
`guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-webkit-version.sh`,
`guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
`guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh`, `check-brand-assets.sh`,
`guard-makefile-version.sh`.

`guard-docs-shots.sh` in particular reported the surface hash
(`3be0ecd9a590…`) matching the manifest's stored `surface_sha256` — the
regeneration the diff claims to have run is real, not just asserted.

### `web/help/img/manifest.json` / `invoices.png` diff sanity-check

Confirmed this is unrelated noise, not a masked problem:

- The manifest diff touches exactly one line, `surface_sha256`
  (`99a11cd8…` → `3be0ecd9a590…`). No per-topic markdown hash changed
  (`invoices`'s four locale hashes are unchanged) — the manifest doesn't
  store per-image byte hashes at all, only the whole-surface source hash and
  per-topic *markdown* hashes. The surface hash moving is expected:
  `settings_page.go` registers the screenshotted `/settings` route, so
  editing it anywhere in the file changes the tracked surface, forcing a
  `make docs-shots` regen even though this specific handler isn't itself
  rendered.
- Exactly one image touched (`web/help/img/en/invoices.png`, a few bytes
  different, no dimension change) — no other topic's screenshot changed, so
  nothing suggests a different page's rendering broke. Render
  non-determinism noise, the same kind already seen on `main` across other
  PRs' screenshot diffs.

## Checked and found clean / not applicable

- **File-write handler missing `os.MkdirAll`**, **cwd-relative path where
  `paths.Data(...)` belongs**: not applicable — this diff adds no file I/O,
  only an audit-table insert through the existing repository method.
- **UX / user-manual-ships-with-the-feature**: not applicable — no new
  template, HTML, or JS; the audit log is an internal/operational record.
  `web/help/en/display.md` and `elevation.md` describe the audit trail only
  in general terms, never naming specific action strings, so nothing there
  goes stale. No locale key was added or needed (`guard-i18n.sh` passes with
  the same key count as `main`).
- **Real client/shop names in test data**: none — `mgr3`/`"Manager Three"`/
  `9998` and `mgr4`/`"Manager Four"`/`9997` are generic, following the same
  convention as the pre-existing `mgr1`/`mgr2` fixtures in this test file.
  No secret-shaped literals.
- **Double-audit-write / wrong ordering**: only one new call was added (no
  duplication); order is `Logout()` → new `self_order_session_revoked` audit
  → cookie clear → existing `display_mode_changed` audit — both audit calls
  read from the same already-captured `elev`, so ordering relative to the
  revoke doesn't affect correctness.

## Merge

`merge_method: "merge"` (never squash/rebase — ut-docs#250), after CI is
green on the PR.
