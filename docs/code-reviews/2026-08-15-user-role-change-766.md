# Auth: general user role-change endpoint (ut-docs#766)

**Date:** 2026-08-15
**Card:** universaltill/ut-docs#766
**Complexity:** medium
**Repo/area:** `universal-till` — `internal/pages/users_page.go`, `web/ui/pages/users.html`, `web/locales/*.json`, `web/help/{en,ar,fa,tr}/users.md`

## What shipped

`POST /api/users/{id}/promote-super-admin` (ut-docs#761) could only ever
move a user *to* `super_admin` — there was no in-app way back down, or to
change any other user's role at all. The only lever was deactivation, a
coarser action that also drops the user's PIN and sign-in history.

Added a general `POST /api/users/{id}/role` endpoint (any role → any
role) rather than a second single-purpose "demote" mirror of
promote-super-admin, because `POST /api/users` already has to
validate+gate every role a user can be *created* with — reusing that same
shape for role *changes* covers promotion, demotion and lateral moves
with one gate.

Gating mirrors `POST /api/users`'s create-time rule exactly, applied
symmetrically to both directions of a change:
- `canManage(actor, target)` (existing helper) still gates who an actor
  can touch at all — managers: only cashiers; admins/super_admins:
  everyone but `system`.
- `kiosk` (migration 018's PIN-less service identity, reachable via the
  auth-exempt `/self-order` surface) is explicitly forbidden as a target,
  same as `system` and same as promote-super-admin already does.
- Granting or removing `super_admin` requires `permission_management`
  (same as creating one).
- Granting or removing `manager`/`admin` requires the actor to already be
  `admin` or `super_admin` (same as creating one).
- Changing the last active `admin` or last active `super_admin` (with a
  PIN) away from that role is blocked, reusing the existing
  `CountOtherActiveAdminsWithPIN`/`CountOtherActiveSuperAdminsWithPIN`
  repo methods the `/active` deactivate handler already uses for the same
  lockout class.
- Same role → no-op, no audit entry (mirrors promote-super-admin).
- A real change journals `user_role_changed`
  (`{"from":…, "to":…, "via":"in-app"}`, same convention as the existing
  promote path) inside the same tx as `SetUserRole`, and revokes the
  target's sessions.

UI: a "Change role" dropdown+button added per user row in
`web/ui/pages/users.html`, additive to (not replacing) the existing
"Promote to super admin" button — the card's own non-goal was not
reopening #761's scope. New locale keys `users.change_role` /
`users.error.role_change` added to all 4 `web/locales/*.json`. New
"Changing or stepping back a role" section added to
`web/help/{en,ar,fa,tr}/users.md`.

## Independent review

Opus subagent, isolated worktree, read-only pass plus actually running
build/vet/tests/guards, plus deliberate TDD re-verification of the two
lockout-guard tests.

**Verdict: safe to merge on correctness/security grounds** — authorization
model verified sound and fail-closed (all 16 target-role × new-role
transitions driven for a `manager` actor: every one 403s except the
cashier→cashier no-op; an `admin` actor cannot grant/remove
`super_admin`), the `kiosk` exclusion isn't bypassable (target identity
comes from the DB row, not the URL), `SetUserRole`+`InsertAudit` share
one tx with `defer tx.Rollback()`, and the last-super_admin guard covers
an actor demoting *themselves* specifically (not just some other
super_admin).

**Independent TDD re-verification:** the reviewer commented out both
lockout guards in the `/role` handler and reran
`TestUsersPage_ChangeRole_LastSuperAdminGuard` and
`TestUsersPage_ChangeRole_LastAdminGuard` — both failed with exactly the
claimed symptom (demotion succeeded instead of being blocked), while the
sibling `second_admin_allows_change` subtest still passed, confirming the
tests fail for the right reason. Restored; full suite green again;
working tree clean.

**Two findings, both fixed same-session:**

1. **UI dead-end (`web/ui/pages/users.html`).** The "Change role" control
   rendered whenever `.CanEdit` was true, but an `admin` actor viewing a
   `super_admin`'s row had every option in the dropdown rejected by the
   backend (only a `super_admin` can touch a `super_admin`'s role,
   either direction) — worse, since none of the `<option>`s matched the
   row's actual role, the browser displayed the *first* option
   ("cashier") as selected next to a Role column that said
   `super_admin`, and submitting dumped the operator onto a raw
   unlocalized `text/plain` 403 page with no way back but Back. Fixed by
   gating the whole control on whether the actor has at least one option
   they could actually submit successfully:
   `(or $.canEditPermissions (and $.isAdmin (ne .Role "super_admin")))`.
   This also fixed a second, related nit: a `manager` actor's only
   editable targets are cashiers, and a `manager` can never grant
   `manager`/`admin` — so their control was a guaranteed no-op (the sole
   offered option always equalled the current role). The same condition
   removes it for managers entirely rather than shipping a dead button.
2. **Help-doc inaccuracy (`web/help/{en,ar,fa,tr}/users.md`).** The
   original text claimed "a manager can only promote a cashier to
   manager" — false: a manager cannot change any role at all (`canManage`
   restricts them to cashier targets, and every non-cashier `newRole`
   then 403s on the actor-role check). The shipped test
   `TestUsersPage_ChangeRole_ManagerCannotPromoteCashier` asserts exactly
   the opposite of the old sentence. Corrected in all 4 locale files to
   state the control is admin-and-up only, matching the code (and the
   fix above, which now also hides the control from managers in the UI).

**Two nits deferred, not fixed (both pre-existing patterns, not new
risk):**

- `internal/pages/users_page.go` — `CountOtherActive*WithPIN` reads run
  outside the `BeginTx` that follows; two concurrent demotions of the
  last two super_admins could theoretically both observe `others == 1`
  and commit, leaving zero. Identical shape to the pre-existing `/active`
  deactivate handler, which has the same gap — deferred as a shared
  follow-up rather than fixed once here and left inconsistent.
- No test renders `users.html` directly in this package's test suite
  (`GET /users` 500s under `seedForPages`'s fixture — a pre-existing,
  fixture-only NULL-`display_name` issue unrelated to this change,
  production `CreateUser`/migration 018 always set it). This is why the
  two review findings above weren't caught by automated tests; visual
  verification (below) is what caught them instead.

A vacuous sub-condition the reviewer flagged in the gating logic
(`newRole != "cashier" || target.Role != "cashier"`, always true given
the preceding no-op check already guarantees `newRole != target.Role`)
was also simplified same-session — harmless, but read as encoding a rule
it didn't.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `go test ./internal/pages/... ./internal/data/...` (full packages)
  green, and the reviewer separately ran the full `go test ./...` (38
  packages) green in their isolated worktree.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh` all pass.
- **Driven in a real running instance** (`go run .` against a
  fully-migrated temp SQLite DB, real PIN login through the numeric
  keypad, Playwright/Chromium): screenshotted `/users` as a `super_admin`
  actor (light theme) and as a plain `admin` actor, before and after the
  fix — confirmed the dead-end control is gone from the `admin`-viewing-
  `super_admin` row post-fix, and the `kiosk` row correctly never shows
  either role control. Also screenshotted in `fa` (RTL): layout mirrors
  correctly, the "تغییر نقش" button and role select align with the row's
  other actions, nothing overlaps or truncates. Dark theme was requested
  via Playwright's `colorScheme` context option but the app's own theme
  is a server-side user setting, not OS-`prefers-color-scheme`-driven, so
  that particular screenshot didn't actually exercise a different theme —
  noted rather than claimed as coverage. Did not separately verify `ar`
  RTL or the 10-inch/kiosk viewport size for this change (not a
  kiosk/checkout-path surface).

## Deferred / out of scope

- The two nits above (transactional race on the lockout count-read;
  `users.html` test coverage gap) — filed as follow-up material, not
  blocking, and not created as new cards this cycle since neither is new
  risk introduced by this change.
- Whether a `manager` actor *should* be allowed to promote a cashier to
  manager (which would make the dead-control nit moot) is a product
  scope question, not decided here — the card's own non-goals said not
  to reopen #761's scope, and this mirrors the identical existing rule at
  user-creation time, so changing it here would be scope creep beyond
  what #766 asked for.
