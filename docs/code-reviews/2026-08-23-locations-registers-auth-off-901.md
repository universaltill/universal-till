# Code review — ut-docs#901: `/locations`/`/registers` permanently 403 under `UT_AUTH=off`

**Date:** 2026-08-23
**Card:** [ut-docs#901](https://github.com/universaltill/ut-docs/issues/901)
**Complexity:** medium
**Build model:** Sonnet (inline) — **Review model:** Opus, independent worktree-isolated subagent

## What shipped

`internal/pages/locations_page.go` and `internal/pages/registers_page.go`
both gated their routes with a page-local `requireManager` closure built
directly on `auth.FromContext(r.Context()).IsManager()`. `internal/pages/
init.go` skips `auth.Middleware` entirely when `UT_AUTH=off` (the dev/CI
escape hatch), so `auth.FromContext` never has a value set in that mode —
these two pages' `requireManager` failed closed **permanently** under
`UT_AUTH=off`, unlike every other admin page, which migrated onto
`canPerform(d, r, "<action>")` (`internal/pages/authz.go`) across #555's
five successor cards (#713 the last, #721 removed the old gate).
`canPerform` has an explicit `auth.Disabled(...)` bypass for exactly this
case. This was also why neither page had any e2e coverage: the e2e
suite's `default` Playwright project boots with `UT_AUTH=off` specifically
so specs can drive the UI without a login flow, and both pages 403'd
there unconditionally. Found and filed during #898's independent review
(`docs/code-reviews/2026-08-23-users-inline-rename-clip-898.md`).

Fix: migrate both pages' `requireManager` onto `canPerform(d, r,
"settings")` — reusing the existing `"settings"` action from the
`internal/db/migrations/039_role_permissions.sql` fixed catalog (granted
to exactly manager/admin/super_admin — the same set `IsManager()`
recognized), matching several other manager-only admin pages
(`settings_page.go`, `receipt_designer.go`, `menu_page.go`,
`print_api.go`). No new migration, no new permission action. The
decisive precedent: `menu_page.go`'s nav-tile gate already used
`canPerform(d, r, "settings")` for the `/locations`/`/registers` tiles
specifically — before this fix, the tile and the page it linked to
disagreed (tile visible under `UT_AUTH=off`, page 403). The fix makes
tile and page agree.

Added regression coverage: `TestLocationsPage_ReachableUnderAuthOff` /
`TestRegistersPage_ReachableUnderAuthOff` (GET, no session, expect 200)
and `TestLocationsPageCreate_ReachableUnderAuthOff` /
`TestRegistersPageCreate_ReachableUnderAuthOff` (POST, no session, expect
303) in each page's `_test.go`. Both test-mux constructors
(`newLocationsTestMux`/`newRegistersTestMux`) now wire a real
`AuthSvc: auth.NewService(db)` into the test `common.Deps` — required,
not incidental: `canPerform` calls `d.AuthSvc.Can(...)` on any
non-`UT_AUTH=off` request, and the existing session-based
`TestLocationsPagePermissions`/`TestRegistersPagePermissions` tests would
nil-pointer-panic without it (`AuthSvc` was nil in both fixtures before
this change, since the old raw `IsManager()` gate never touched it).

Added `e2e/tests/locations-registers-auth-901.spec.ts`: a real-browser
Playwright smoke spec (runs against the `default`/`UT_AUTH=off` project)
that loads each page, creates a fresh entity, renames it, deactivates it,
and reactivates it, asserting on the toggle form's own hidden `active`
input value (not `.muted` text — see review finding below) at each step.

## What the independent review found

Spawned an Opus subagent (`general-purpose`), isolated in its own git
worktree, given the diff and told to run things itself and try to break
it — not confirm it's fine. Verdict: **safe to merge, no blockers.**

Independently re-verified the TDD claim at both layers, by reverting only
the two production files (test files left in place):

- **Go, pre-fix:** `TestLocationsPage_ReachableUnderAuthOff` /
  `TestRegistersPage_ReachableUnderAuthOff` both fail — `403, want 200:
  Manager or admin required`.
- **Go, post-restore:** both pass.
- **e2e, pre-fix:** both specs fail on `expect(page.locator('h1'))
  .toBeVisible()` — `element(s) not found` (a `text/plain` 403 body has
  no `<h1>`).
- **e2e, post-restore:** `2 passed (9.4s)`, against a real `run-till.sh`
  till.

Confirmed the `"settings"` action choice is genuinely equivalent to the
old `IsManager()` gate (identical manager/admin/super_admin grant set,
no later migration touches `'settings'`), and drove the real binary by
hand on both an auth-off till (GET both pages → 200; full
create/rename/deactivate/reactivate cycle → 303s; `/audit` shows the
resulting entries with a `NULL` actor rendered as "System" — `actor.ID`
being `""` under no-session auth-off doesn't violate the audit table's
nullable actor FK, no new defect) and a genuinely auth-on till with no
session (GET both pages → 303 to `/login`; POST → 401) — production
gating is unchanged; the auth middleware bounces the request before
`canPerform` is ever reached.

Also ran the full non-`-race` and `-race` suites (`-race` needs
`-timeout 30m`; the default 10-minute timeout was hit on the first
attempt with **zero** actual data races reported — an artifact of
package size, not this change, and CI never runs `-race` at all), plus
the `de_DE`/`en_GB` locale variants CI also exercises, and the
`i18n`/`help-topics`/`data-access`/`kiosk-engine` guards. All green.

Non-blocking findings, four fixed here:

1. **Fixed.** The registers spec's original deactivate assertion
   (`renamedRowA.locator('.muted').first()`) was vacuous: a register
   with no assigned location renders a `.muted` "None" span in the
   *location* cell regardless of active/inactive state, so the assertion
   passed even if deactivation silently failed. Replaced with a direct
   read of the toggle form's own hidden `active` input value (`'1'`
   after deactivate, `'0'` after reactivate) on both pages' specs — one
   true, locale-independent assertion, and it also gives the flow a real
   sync point (see #4 below).
2. **Fixed.** The spec's top-of-file comment claimed a failed GET
   "renders the login error page" — it's actually a plain-text 403 body
   (`common.LocalizedError` → `http.Error`), which is *why* the `<h1>`
   assertion catches the regression. Comment corrected.
3. **Fixed.** The "leaving the shared e2e server's location count/state
   as this test found it" comment overstated: the spec leaves its own
   new/renamed rows behind (active). Reworded to say so plainly, and
   confirmed (per the reviewer's own check) that no other e2e spec
   asserts a row/option count on either page, so this is harmless.
4. **Fixed as a side effect of #1.** The `page.waitForURL(...)` calls
   after each submit were no-ops (the URL already matched before the
   click), so the deactivate→reactivate pair had no real synchronization
   point. The new toggle-value assertions (which `expect(...)` retries
   until the post-redirect page settles) are the real sync point now.
5. **Noted, not a code change.** AC#2 ("no change to production
   behavior") holds for the seeded default grants; `role_permissions` is
   runtime-editable by a super_admin
   (`internal/pages/permission_settings_page.go`), so these two pages
   now follow the `"settings"` grant in both directions the same way
   every other `settings`-gated page (and the nav tile) already does —
   consistent, not a regression, but worth recording so the AC isn't
   read as stronger than it is.
6. **Added.** Go-side coverage was GET-only; added
   `TestLocationsPageCreate_ReachableUnderAuthOff` /
   `TestRegistersPageCreate_ReachableUnderAuthOff` (a mutating POST
   under `UT_AUTH=off`, no session) as cheap insurance that the fix
   covers more than the read path.

Deferred (new Backlog cards, not this branch — see below):

- Reusing the generic `"settings"` action is semantically broad for
  stock-location/register administration specifically; a future
  dedicated action would need both a new migration and a matching
  `menu_page.go` tile-gate change, or tile and page desync again exactly
  as this card did.
- The **same** raw `auth.FromContext(...).IsManager()` pattern still
  exists, byte-identical, in five sibling pages:
  `country_settings_page.go`, `kitchen_stations_page.go`,
  `promotions_page.go`, `tables_page.go`, `translations_page.go` — all
  permanently 403 under `UT_AUTH=off` today. `tables_page.go` is the
  concrete cost already paid for this: `tables-keyboard-reposition-826
  .spec.ts` has to run on the `auth` Playwright project instead of
  `default` specifically because of this gap
  (`playwright.config.ts`'s `AUTH_ONLY_SPECS`).
- Possible stale entry (pre-existing, unverified): `AUTH_TILL_TOPICS` in
  `e2e/tests-docs/docs-shots.spec.ts` still lists `'users'`, but
  `users_page.go` already migrated to `canPerform(d, r,
  "user_management")`, which has the auth-off bypass — that topic may no
  longer need the auth till.

## What was verified beyond automated tests

- `gofmt -l .` — clean. `go build ./...` — clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite,
  zero failures.
- `go test -race ./internal/pages/...` (both this session's own run and
  the reviewer's independent worktree run, `-timeout 30m`) — clean, no
  data races.
- `go test ./internal/pages/...` under `LANG=de_DE.UTF-8` and
  `LANG=en_GB.UTF-8` (the reviewer's own extra check, mirroring what CI
  exercises) — pass.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-data-access.sh` — all
  pass; no new user-facing strings, no new routes, no SQL added outside
  `internal/data`/`internal/db`.
- Real Playwright run (pre-installed Chromium) of the new e2e spec
  against a real `run-till.sh` till, both before the fix (confirmed red:
  `2 failed`, `<h1>` not found) and after (confirmed green: `2 passed`).
- Manual live curl against both an auth-off and a genuinely auth-on
  fresh till, GET and POST, matching the acceptance criteria exactly.
- Manual (`web/help/`): `/locations` is claimed by `inventory.md`,
  `/registers` by `multitill.md`; neither describes dev/CI reachability
  and production behavior is unchanged, so no manual edit is owed —
  `guard-help-topics.sh` confirms no route-coverage regression.
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Verdict

**Safe to merge.** No blocking findings. Both acceptance criteria met:
`GET`/`POST` reachable under `UT_AUTH=off` with no session; production
(`UT_AUTH` on/unset) gating unchanged; a minimal e2e spec exists for
each page.

## Explicitly deferred (new Backlog cards filed)

- **ut-docs#902** — the same permanently-403-under-`UT_AUTH=off`
  pattern in `country_settings_page.go`, `kitchen_stations_page.go`,
  `promotions_page.go`, `tables_page.go`, `translations_page.go`.
- **ut-docs#903** — consider a dedicated `stock_location_management`/
  `register_management` permission action instead of reusing the
  generic `"settings"` action, if finer-grained control ever matters.
