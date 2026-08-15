# Code review: super_admin permission-matrix UI (ut-docs#556)

**Date:** 2026-08-15
**Author (Dev/Tester):** Claude (Sonnet), autonomous SDLC pipeline
**Reviewer:** Claude (Opus), independent fresh-context subagent
**Branch:** `feat/permission-matrix-ui-556`
**Complexity:** medium

## What shipped

A settings page at `GET /users/permissions`, gated to the `super_admin`
role only (via a new `permission_management` action, migration 047, seeded
super_admin-ONLY — deliberately not the manager-inclusive pattern
039/042-045 use, nor 046's admin+super_admin pattern), that lists every
(role, action) pair in the permission catalog as a checkbox grid and lets
super_admin toggle grants via `POST /api/users/permissions`. Every change
is journaled through the existing `POSRepo.InsertAudit` pattern. A
self-lockout guard rejects revoking the one grant — (super_admin,
permission_management) — that gates the page itself, since that would
permanently lock every super_admin out of the only surface that can grant
it back.

New `AuthRepo` methods: `ListRolePermissionMatrix` (full role×action grid,
including never-explicitly-granted cells via `LEFT JOIN`), `SetRolePermission`
(upsert one cell), `RoleExists`/`ActionExists` (input validation).

**Known gap, not blocking:** nothing in the codebase creates a
super_admin-role user yet (pre-existing — already noted in
`internal/pages/audit_page_test.go` before this card). This editor is
unreachable by a real operator in production until a separate card adds a
promotion path. Filed as a follow-up rather than expanding this card's
scope.

## Independent review — findings and resolution

Reviewed at Opus (fresh context, this card's `complexity:medium` routing),
instructed to actually run the build/tests/guards rather than trust the
summary handed to it. Found 2 blockers, 4 should-fix issues, and 6 nits.
All blockers and should-fix items were fixed; nits were either fixed
opportunistically (cheap, same file) or accepted as pre-existing/out of
scope with a stated reason.

### Blockers (both fixed)

1. **Stale `hx-vals` — a second click on any checkbox sent the wrong
   value.** The original template precomputed `Toggled` (the opposite of
   the server-rendered `Granted`) into a *static* `hx-vals` attribute htmx
   never rewrites, and the response only swapped a status span, not the
   checkbox. So every click after the first re-sent the first click's
   value — e.g. grant → click to revoke actually re-granted, while the
   checkbox visually unchecked and the server replied "Saved". A privilege
   inversion presented as success, on the very first revoke attempt after
   any grant.
   **Fix:** dropped the precomputed `Toggled` field entirely; the checkbox
   now carries its own `name="granted" value="1"`, which htmx (following
   native HTML checkbox semantics) includes only when checked — `hx-vals`
   now carries only the static `role`/`action`. No JS, no server-side
   toggle-state tracking, no way for the DOM and the sent value to diverge.

2. **No reachable entry point — the feature's own doc pointed at a dead
   path.** `/users` (the page carrying the "Permissions" link) is gated by
   `requireManager`, which checked the old `auth.User.IsManager()`
   (manager/admin only) — a `super_admin` session got 403 on `/users`
   itself, before ever seeing the link. The `canEditPermissions` flag
   compounded it with a raw `actor.Role == "super_admin"` check instead of
   `canPerform`, so even reaching `/users` wouldn't show the link to a role
   that had been *granted* `permission_management` without holding the
   literal `super_admin` string. Meanwhile the help topic instructed
   shop owners to reach it "from Users → Permissions" — a path that did
   not exist in any configuration.
   **Fix:** `requireManager` now gates on `canPerform(d, r,
   "user_management")` (039's catalog action, already granted to
   manager/admin/super_admin — behavior-preserving for existing roles,
   additive for super_admin) instead of the old `IsManager()` bit;
   `canEditPermissions` now uses `canPerform(d, r, lockoutAction)`; and
   `canManage` (the per-target-user gate on the same page) now treats
   `super_admin` the same as `admin` rather than falling through to the
   manager-only cashier-only branch — a super_admin was otherwise more
   restricted than admin at managing other users, which made no sense for
   the top of the role hierarchy.

### Should-fix (all fixed)

3. **The lockout error was never actually displayed.** htmx doesn't swap
   non-2xx responses by default, and this codebase's only override
   (`app.js`'s `beforeSwap`) is scoped to 400s under `/api/pos/`. A 409
   from `/api/users/permissions` fell through to the generic
   `htmx:responseError` handler — a misleading "server error" banner
   instead of the real, carefully-translated reason.
   **Fix:** the guard now returns 200 with the error fragment (the same
   pattern the "Saved" success path already uses), using the established
   `.login-error` CSS class rather than an undefined `.error` class.

4. **Raw SQL errors leaked to the client; no input validation.** Any
   non-empty `role`/`action` went straight to the upsert; a garbage value
   would fail closed via the FK constraints on `role_permissions`, but
   surfaced as a raw SQLite error string via `http.Error(w, err.Error(),
   ...)` — an internal-detail leak on a privileged endpoint, and the wrong
   status code for bad input.
   **Fix:** added `AuthRepo.RoleExists`/`ActionExists`, called before any
   write; unknown values now return a clean 400. Every other DB error path
   now logs the real error server-side via `logging.L().Errorf` and
   returns a generic message to the client, matching the pattern already
   established elsewhere in this codebase (`eod_api.go`, `cloudsync_wire.go`).

5. **Migration 047's seed was untested against a real migrated DB.** The
   page's own test fixture (`internal/pages/ui_smoke_test.go`'s
   `seedForPages`) hand-seeds its own copy of the permission catalog rather
   than running migrations — a real, pre-existing pattern in this codebase,
   but it meant every test in the diff would still have passed even if 047
   had shipped the wrong seed pattern (manager-inclusive instead of
   super_admin-only).
   **Fix:** added
   `TestAuthRepo_Migration047SeedsPermissionManagementSuperAdminOnly` in
   `internal/data/auth_repo_test.go`, which runs against `openMigratedDB` —
   the real migration — and asserts cashier/manager/admin are NOT granted,
   super_admin IS.

6. **No test asserted the rendered grid's actual state.** The original GET
   test only checked that translated labels appeared in the body — a
   version of the page that dropped `Granted`/`Locked` handling entirely
   would still have passed, which is exactly how finding 1 survived to
   review.
   **Fix:** added
   `TestPermissionSettingsPage_GET_RendersCheckedLockedAndHxVals`, which
   locates specific `<input>` tags by their `hx-vals` payload (regexp,
   not a byte-exact string, so it survives incidental template reformats)
   and asserts: a granted cell renders `checked` + interactive; an
   ungranted cell doesn't; the locked cell renders `checked disabled` and
   carries no interactive `hx-vals` wiring at all.

### Nits

7. Locked cell's `checked` attribute is now conditional on `$cell.Granted`
   (was hardcoded `checked`) — fixed, cheap, same file as findings 1/2.
8. The lockout guard is deliberately broader than the literal invariant (it
   blocks revoking super_admin's grant even when another role also holds
   `permission_management`) — accepted as an intentional, simple, auditable
   rule rather than a derived one.
9. A redundant `r.ParseForm()` call (harmless; `FormValue` parses on
   demand) — removed while rewriting the handler for findings 3/4.
10. Folded into finding 2's fix (the `lockoutRole` constant reuse for an
    unrelated role check went away with the `canPerform`-based rewrite).
11. `permissions.intro`'s English copy said `super_admin` (the internal
    identifier) while `users.role.super_admin` renders "super admin" two
    keys away — reworded `permissions.intro` to say "super admin" for
    consistency (en locale; ar/fa/tr already used their own natural
    phrasing, not the raw identifier, so no change needed there).
12. `UT_AUTH=off` bypasses `canPerform` entirely (any LAN client can
    rewrite the whole permission matrix) — a pre-existing, repo-wide dev/CI
    escape hatch (`authz.go`), not introduced by this card. Noted here as a
    conscious acknowledgment per the reviewer's flag, not an action item:
    it's the same escape hatch every other `canPerform`-gated endpoint
    already accepts, including money/tax-adjacent ones (`fiscal_api.go`).

## What the reviewer checked and found correct (not just claimed)

- **Transaction atomicity**: `BeginTx` + `defer tx.Rollback()`, both the
  permission write and the audit journal execute on the same `tx`, every
  error path returns before `Commit` — no partial-write window. Confirmed
  the prod DSN's `_txlock=immediate` + `busy_timeout=5000` serializes
  concurrent toggles rather than deadlocking.
- **Lockout guard cannot be bypassed** by case, whitespace, or duplicated
  form params — `role_permissions`' FK constraints + `foreign_keys=ON`
  reject anything but the exact seeded strings; no TOCTOU (the guard and
  the write consume the same two request-scoped variables).
- **`ListRolePermissionMatrix`'s SQL** (`roles CROSS JOIN
  permission_actions LEFT JOIN role_permissions` + `COALESCE(granted,0)`)
  is exactly right for a full grid including never-seeded cells.
- **Repository pattern compliance** — no SQL literal leaked into
  `internal/pages` (guard-confirmed).
- **Template variable scoping** — `$roles`/`$action`/`$cells` captured
  before the inner `range`, no `$` vs `.` confusion; `index $cells $role`
  on a missing key yields the zero-value struct, not a nil deref.
- **i18n coverage** — all 16 catalog actions (migrations 039-047) have
  `permissions.action.*` keys; all four `users.role.*` keys exist; all four
  locale files match `en.json`.

## Verification (re-run after every fix)

```
go build ./...                                  clean
go vet ./...                                     clean
gofmt -l <every touched file>                    clean
go test ./...                                    ok, all packages
scripts/ci/guard-data-access.sh                  ✓
scripts/ci/guard-kiosk-engine.sh                 ✓
scripts/ci/guard-plugin-menu-read.sh             ✓
scripts/ci/guard-i18n.sh                         ✓ (1055 template keys, all locales match)
scripts/ci/guard-help-topics.sh                  ✓
scripts/ci/guard-compliance-claims.sh            ✓
```

Fixing finding 2 (requireManager's gate) surfaced a real regression the
first pass of `go test ./...` caught before this record was written: a
pre-existing `internal/pages/auth_page_test.go` fixture (`newAuthTestMux`,
predates #554) constructed `common.Deps` without `AuthSvc` and without the
`role_permissions`/`roles`/`permission_actions` tables — `canPerform` nil-
panicked on `d.AuthSvc.Can(...)`. Fixed by extending that fixture with the
same minimal permission-schema seed pattern already established in
`ui_smoke_test.go`'s `seedForPages`, rather than reverting the gate change.

## Verified beyond automated tests

- Traced the full self-lockout scenario by hand against the seeded catalog:
  a solo super_admin cannot lock themselves out via any request shape,
  including a crafted request bypassing the disabled checkbox.
- Traced `canManage`'s new super_admin branch against every existing
  `/api/users/*` write path — behavior for `manager`/`admin`/`cashier`
  actors is byte-for-byte unchanged; only `super_admin` gains capability
  (previously 403'd entirely at `requireManager`).
- Confirmed `permission_management`'s migration 047 seed against
  `openMigratedDB` (not just the hand-written test fixtures) via finding
  5's new test.
