# Code review: super_admin creation/promotion path (ut-docs#761)

**Date:** 2026-08-15
**Card:** universaltill/ut-docs#761 — "Auth: no way to create or promote a
user to super_admin"
**Author (build):** Sonnet, inline (complexity: medium)
**Reviewer (independent):** Opus subagent, isolated git worktree

## What shipped

The `super_admin` role has existed in the schema since migration 039 and
gates real surfaces (`audit_page.go`, `backoffice_page.go`, and
`permission_settings_page.go`'s role→action grant-matrix editor, migration
047) — but nothing could ever create a user with that role. Migration 047's
own comment flagged this exact gap as a known follow-up.

This change closes it with two paths:

1. **In-app**, gated on the `permission_management` action (i.e. requires
   an existing `super_admin`, mirroring `permission_settings_page.go`'s own
   gate, since minting a `super_admin` is at least as sensitive as anything
   that action already gates):
   - `POST /api/users` now accepts `role=super_admin`.
   - New `POST /api/users/{id}/promote-super-admin` promotes an existing
     user, journals a `user_role_changed` audit entry, and revokes the
     target's sessions so the new role takes effect on next login.
   - `system` and `kiosk` (the two migration-seeded service identities —
     `003_system_user.sql`, `018_kiosk_user.sql`) are explicitly excluded;
     `kiosk` in particular is reachable by any anonymous LAN client via the
     auth-exempt `/self-order` surface, so promoting it would be a real
     privilege-escalation path.
   - `web/ui/pages/users.html`: a "Promote to super admin" button per
     eligible row, and a `super_admin` option in the create-user dropdown,
     both gated on the existing `.canEditPermissions` flag.
2. **Bootstrap CLI** (`scripts/promote-super-admin`), for the very first
   `super_admin` — the in-app path is circular for that case, since it
   needs an existing `super_admin` to grant one. Refuses to run if a
   `super_admin` already exists unless `--force` is passed (disaster
   recovery only). Goes through `AuthRepo`/`POSRepo` like any other write —
   deliberately not raw SQL, unlike `scripts/e2e_seed`, since this is real
   production tooling, not test/seed support.

New `AuthRepo` methods: `SetUserRole`, `CountUsersByRole`,
`CountOtherActiveSuperAdminsWithPIN`. New locale keys (`users.promote_super_admin`,
`users.error.promote`, `users.error.last_super_admin`) in all four shipped
locales, and a new "Becoming a super admin" section in all four `web/help/*/users.md`.

Also fixed, as part of this same change (`ui_smoke_test.go`): a pre-existing
test-fixture drift in `internal/pages`' shared `seedForPages` helper — its
hand-built `users` table had `pin_hash TEXT NOT NULL` (production's
`001_init.sql` has it nullable) and a `created_at` column with no default
that doesn't even exist in production — both made `AuthRepo.CreateUser`'s
real INSERT fail against the fixture. Two sibling fixtures in the same
package (`auth_page_test.go`, `setup_page_test.go`) already had `pin_hash`
nullable, so this brings the third into line.

## Independent review — findings

The review (Opus, isolated worktree, full `go test ./...` + all guards run,
two TDD revert-verifications performed) found **zero blocking security
issues** — it deliberately tried to find a bypass of the
`permission_management` gate on both the create and promote paths and could
not. One **CI-blocking** finding and four **should-fix** findings, all
fixed in this same round (no second review round — none were blocker-class
per the pipeline's "earn a second round" rule, so this pipeline instance
triaged and fixed them directly rather than re-spawning the reviewer):

1. **Blocking — `guard-docs-shots.sh` failed.** `web/ui/pages/users.html`
   changed with no regenerated screenshot. Fixed: ran the docs-shots
   Playwright harness scoped to the `users` topic (`-g "screenshot: users"`)
   across all four locales and committed the result. Confirmed the
   captured pixels are unchanged for en/tr/ar (byte-identical — the
   harness always logs in as a plain `admin`, and the new button/dropdown
   only render for a `super_admin` session, so there's genuinely nothing
   new to see in that capture) and negligibly re-encoded for fa (9-byte
   diff, visually identical side-by-side).
2. **`IsManager()` never learned about `super_admin`.** Before this fix,
   promoting an operator to the *highest* role would have silently locked
   them out of five pages (`promotions_page.go`, `country_settings_page.go`,
   `translations_page.go`, `kitchen_stations_page.go`, `locations_page.go`)
   and rejected their PIN as a manager override at checkout
   (`AuthorizeManager`). `authz.go`'s own comment used to say this gap was
   inert ("no code path creates a super_admin-role user yet") — this diff
   is exactly what makes that false, so it had to be fixed in the same
   change, not left as a follow-up. Fixed in `internal/auth/service.go`
   (`IsManager()` and `AuthorizeManager`); comment in `authz.go` updated to
   stop asserting the now-false claim. Regression test:
   `TestUser_IsManager_IncludesSuperAdmin`, `TestAuthorizeManager`.
3. **A `super_admin` actor couldn't create a `manager` or `admin`.** The
   pre-existing `POST /api/users` gate (`actor.Role != "admin"`) predates
   this card, but this diff edits the exact line next to it, and leaving
   `super_admin` — the top of the role hierarchy — less capable than a
   plain `admin` on the same handler this diff modifies would have been a
   real, visible inconsistency (`canManage` in the same file already treats
   `super_admin` as at least as capable as `admin`). Fixed by widening the
   condition and the `isAdmin` template flag. Regression test:
   `TestUsersPage_CreateUser_SuperAdminRole_RequiresPermissionManagement/super_admin_actor_can_create_manager_and_admin`.
4. **No guard against deactivating the last `super_admin`.** The existing
   "cannot deactivate the last admin" guard (`CountOtherActiveAdminsWithPIN`)
   never had a `super_admin` equivalent, because nothing could reach
   `super_admin` before this card — deactivating the only one would strand
   the till with nobody able to reach the permission matrix, audit page or
   backoffice. Added `CountOtherActiveSuperAdminsWithPIN` (same shape as the
   admin one) and wired the same guard, with a new `users.error.last_super_admin`
   locale key. Regression test: `TestUsersPage_Deactivate_LastSuperAdminGuard`.
5. **CLI could silently create a new empty DB at a typo'd path.**
   `db.Open` `MkdirAll`s and migrates whatever it finds, so a wrong path
   produced a misleading "no user with username" error instead of "wrong
   path". Fixed with an `os.Stat` check before opening. Regression test:
   `TestRun_NonexistentDBPathErrors`.

Also fixed as a one-line cheap addition: the bootstrap CLI's audit payload
already carried `"via": "bootstrap-cli"`; the in-app handler's now carries
`"via": "in-app"` too, so an auditor reading `user_role_changed` entries can
tell the two provenances apart without cross-referencing actor ids.

## Explicitly accepted / deferred (not fixed here)

- **Promoting a deactivated (`is_active=0`) user is still possible.** Low
  real risk — a deactivated account can't sign in regardless of role, and
  reactivating it already goes through the same `canManage` gate. Noted by
  review as "worth closing" but not blocking; deferred rather than
  expanding this diff's scope further.
- **No demotion path.** `SetUserRole` is general-purpose, but every caller
  hardcodes `"super_admin"`. A wrongly-promoted user can only be
  deactivated, not demoted, in-app today. Real gap, but demotion is a
  materially bigger feature (what happens to a demoted super_admin's
  pending grants, whether it needs its own confirmation flow) than this
  card's stated scope ("create or promote"). Filed as a follow-up rather
  than folded in here.
- **No ADR for the bootstrap CLI.** Judged at the Architect step not to be
  architecturally novel — it's a scoped, single-purpose follow-up using
  entirely established patterns (repository methods, the `InsertAudit`
  journal shape, a `scripts/`-housed one-off tool following
  `scripts/run_migrations`' precedent), not a new cross-cutting mechanism a
  future Architect would need to know the *why* of beyond what's already
  in this record and the code's own comments.
- **`promote` requires only `permission_management`, not `user_management`.**
  A `super_admin` whose `user_management` grant was individually revoked
  via the matrix couldn't load `/users` but could still `POST` the promote
  endpoint directly. Theoretical (nothing in the seeded catalog allows
  revoking `user_management` from `super_admin` without also being able to
  re-grant it), noted for completeness.

## Verified beyond automated tests

- Full `go build ./...`, `go vet ./...`, `go test ./...` (38 packages, all
  green) and every `scripts/ci/guard-*.sh` (data-access, i18n, kiosk-engine,
  plugin-menu-read, help-topics, compliance-claims, docs-shots) — all pass.
- **Driven, real-app check** (not just `httptest`): built the binary,
  seeded a real migrated SQLite DB with a `super_admin` and a plain `admin`
  user, ran the server, logged in via real PIN auth over HTTP, and drove
  the actual promote flow end-to-end (`curl` + a Playwright screenshot pass)
  — confirmed the button/dropdown appear only for a `super_admin` session,
  disappear once a target is already `super_admin`, are absent for the
  `kiosk` service row, and that promoting a real `admin` via the live
  endpoint actually flips their role in the DB.
- **Visual check, both directions**: screenshotted `/users` as a
  `super_admin` at 1024×600 (the kiosk viewport) in English, and in `fa`
  and `ar` (RTL) — button placement, wrapping, and RTL mirroring (form
  panel correctly swaps side) all look correct; no overlap or truncation on
  the new button among the existing `Set PIN`/`Deactivate` actions. Did
  **not** independently verify a dark-theme render (this app's theme isn't
  a URL-toggleable query param the way the check script assumed) — noted
  as an accepted gap, not silently skipped.
- **TDD, independently re-verified by the reviewer**: two reverts
  performed live (removing the `kiosk` exclusion; collapsing the new
  `role == "super_admin"` branch back to the old single condition), each
  reproducing a real, concrete privilege-escalation-shaped failure (a
  service identity actually promotable; a plain `admin` actually able to
  mint a `super_admin`), then restored and re-confirmed green.
- **Translation provenance disclosed.** The NAS Ollama translation
  pipeline (`reference/translation.md`) is unreachable from this cloud
  session (confirmed by both the implementer and, independently, the
  reviewer — TCP connect to `192.168.1.231:11434` times out). The tr/fa/ar
  UI strings and help-doc prose were hand-authored directly instead — a
  real, disclosed deviation, not an oversight (precedent:
  `2026-08-13-promotions-management-ui-634.md`). The reviewer, reading each
  language, judged all three as genuine idiomatic translations reusing the
  pre-existing `users.role.super_admin` terminology per locale, not
  transliteration or English left in place.

## Safe to merge

Yes. No blocking issues remain; the one CI-blocking finding is fixed and
the guard passes; the security-shaped questions were interrogated
adversarially and no bypass was found.
