# Review — additional tills write user/PIN edits through to the main till (ut-docs#2755)

Date: 2026-09-25 · Branch: `feat/2755-user-write-through` ·
Author: Opus 5.5 (pipeline, `lane:cloud-24`) · Reviewer: Fable (independent subagent, two rounds)
Design: ut-docs ADR-0115 §1 (epic ut-docs#2731).

## What shipped

Before this change, an admin editing users on an additional till wrote only
to that till's own database. The next admin-bundle pull (primary-wins,
prune-then-upsert) then silently reverted the edit: a new user was
deleted, and a changed PIN, role or active flag was overwritten.

- **Main till:** new `POST /api/sync/users/apply` (`internal/pages/sync_users.go`).
  - Authenticated with `syncTill`'s bearer and exempted from session auth,
    pinned in `TestSyncPullPathsAreExempt`. It refuses with 409 when the
    receiving till is itself an additional till.
  - Ops: `create`, `set_pin`, `set_active`, `set_role`.
  - Re-checks the role allow-list, username uniqueness and the `pin_hash`
    format (`auth.ValidatePINHash`: parse only, iteration cap, 16 KiB
    body cap).
  - Re-checks the actor's authority from its own `users` row
    (`authorizeUserChange`, `user_rules.go`). The actor must be active, and
    setting one's own PIN is allowed for any role. Everything else needs
    `user_management`; anything touching super_admin needs
    `permission_management`; and the target must pass
    `canManageUser`-style hierarchy checks.
  - The last-admin and last-super-admin guards count and write in one
    transaction (`SetUserActiveGuarded` / `SetUserRoleGuarded`, SQLite
    `_txlock=immediate`), with the audit row written inside that
    transaction.
  - Revokes the target's sessions and never logs or returns the hash.
- **Additional till:** `internal/pages/user_sync_proxy.go` (5 s budget: this
  is an admin screen, not checkout).
  - Covers create, set PIN, active, role and promote-super-admin
    (`users_page.go`) and the staffer's own PIN change (`auth_page.go`,
    `ChangeOwnPINVia`).
  - Local checks run unchanged. The PIN is then hashed locally, so only the
    hash goes on the wire.
  - On a 200 from the main till, the row is mirrored locally
    (`AuthRepo.MirrorUser`).
  - Any other outcome writes nothing locally: forbidden → 403, an invariant
    refusal → the existing message, anything else → the new
    `users.error.main_till_unreachable`.
- **Permission matrix** is refused on an additional till (409,
  `users.error.replica_use_primary`).
- Shared rules moved to `user_rules.go`, so the local and main-till paths
  use one copy.
- **i18n:** 2 keys in en/ar/fa/tr; de/es packs follow in
  `ut-plugin-language-{de,es}`.
- **Help:** a "Users on a second till" section in `web/help/*/users.md`.
- `internal/logging/capture.go`: a test-only tee, used to assert that the
  PIN and hash never reach any log level. It is baselined in
  `scripts/ci/deadcode-baseline.txt` as a test-only-reachable helper, the
  guard's documented `ResetCacheForTests` shape.

## Findings

Round 1 (Fable):

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **Blocker** | Main till trusted `actor_id` blindly. The reviewer proved that a paired-till bearer naming a *cashier* could create a super_admin, set an admin's PIN, or promote a user. | **Fixed**: `authorizeUserChange` + `TestSyncUsersApply_ActorAuthorization` (14 refusal / 8 allowed cases). ADR-0115 amended to require it. |
| 2 | Should-fix | Last-admin guards counted outside a transaction, so two tills could together remove the last admin. | **Fixed**: guarded tx repo methods + `TestAuthRepo_GuardedUserUpdatesAreAtomic` (20 concurrent rounds). |
| 3 | Should-fix | Sessions on a *third* till are not revoked (sessions don't travel in the bundle). | **Accepted residual**, stated in ADR-0115; carried by ut-docs#2758 (link nudge revokes). |
| 4 | Should-fix | 800 ms budget: a reply lost after the main till committed a create shows "unreachable", and a retry then says "username taken". | Budget raised to 5 s. The ambiguous-timeout residual is accepted: the next pull repairs the mirror. |
| 5 | Nit | Language packs lack the keys. | PRs in `ut-plugin-language-{de,es}` this cycle. |
| 6 | Nit | `UT_AUTH` off → every replica user edit refused (`unknown_actor`). | Accepted: dev-only mode. |

Round 2 (Fable, scoped to the fix): **no blockers or should-fixes**.

- Main-till authorization equals or is stricter than every local path.
  `set_role` to super_admin follows the local `/promote-super-admin` rule.
- The tx guard is atomic, and every error path rolls back.
- A forbidden reply reaches the browser as a 403 with no local write.
- Nits only (cosmetic pre-tx role in the audit's `from`, a redundant
  `errors.As`).

Accepted residual (ADR-0115): the till bearer names the actor, so a stolen
LAN bearer can name an admin. This is the same trust boundary that already
exposes every `pin_hash` in the bundle. It will be closed by an operator
proof over ADR-0114's TLS link (ut-docs#2758).

## Verified

- **TDD re-verified by the reviewer:**
  - Reverting the replica set-PIN write-through fails
    `TestUserWriteThrough_SetPINSendsHashAndMirrorsIt` and
    `…MainUnreachableRefusesWithoutLocalWrite`.
  - Removing the main-till last-admin guard fails
    `TestSyncUsersApply_LastAdminGuards`.
  - Removing `authorizeUserChange` fails all 14 forbidden subtests.
  - Each passes again once restored.
- **Replica-side tests** run against a real main-till handler with its own
  SQLite database: each op lands on both tills with the same id and hash,
  and an unreachable main till leaves the local database unchanged.
- **Gate:**
  - `gofmt`, `go build`, `go vet` and `golangci-lint` (0 issues) all pass.
  - `go test ./...` passes all packages; `-race` passes on
    auth/data/logging/pages.
  - Every `ci.yml` build guard passes locally, except
    `guard-shellcheck-version.sh`: shellcheck isn't installed in this
    sandbox, and CI runs it.
- **Not driven in a browser:** the refusal uses the existing
  `usersRespondError` / 409 HTML swap paths, unchanged markup.

**Verdict: safe to merge.**
