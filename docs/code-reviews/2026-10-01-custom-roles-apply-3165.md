# Code review: custom roles, migration + save_role/delete_role apply + admin-bundle prune (ut-docs#3165)

- **Date:** 2026-10-01
- **Card:** universaltill/ut-docs#3165 (ADR-0128 §2, §3, §6). Split at BA: the
  check-in roles report is ut-docs#3323; the Permissions/Users UI, widened
  assignable roles and the name-based check audit are ut-docs#3324.
- **Author:** autonomous pipeline, `lane:cloud-54` (Opus 5.5 dev subagent)
- **Reviewer:** independent Fable subagent, separate worktree

## What shipped

- `054_roles_label_origin.sql`: `roles.label` (default `''`), `roles.origin`
  (default `'builtin'`), and a backfill of `origin='cloud'` for `c\_%` keys.
  The checksum is pinned in `shipped_migrations_test.go`.
- `internal/cloudsync/role_directives.go`: decodes `save_role` / `delete_role`
  (`grants` is a JSON array inside a string field). Both types are in
  `mainTillOnlyTypes`, with `Hooks.SaveRole` / `Hooks.DeleteRole` dispatch.
  A nil hook fails with "not supported on this till".
- `internal/data/auth_repo.go`: tx-scoped role methods (`GetRoleTx`,
  `ListRolesTx`, `ListActionsTx`, `RoleGrantsTx`, `UpsertCloudRoleTx`,
  `ReplaceRoleGrantsTx`, `CountUsersWithRoleTx`, `DeleteCloudRoleTx`, …) and
  `RoleOrigin`.
- `internal/pages/cloud_role_directives.go`: the main-till apply. It runs in
  one BEGIN IMMEDIATE transaction and enforces:
  - key regex and refusal of built-in keys;
  - label rules: 1–40 runes, no control or format characters, unique
    case-insensitively among cloud roles, and not equal to a built-in key or
    to any loaded locale's `users.role.*` label;
  - the complete grant set: an unknown action fails the whole directive, and
    `permission_management` is refused;
  - delete is refused while any user holds the role (count only, no names).

  Every apply that changes something writes one audit row. A true no-op
  re-apply writes nothing.
- `ApplyAdmin`: prunes `origin='cloud'` roles that are missing from a bundle
  that carries a non-empty `roles` table, grants first. `rolePermissionSkew`
  no longer spares the grant rows of a role being pruned.
- Permissions page POST: a cloud role gets 409 with
  `permissions.error.cloud_role`. The key is in en/ar/fa/tr; pack PRs for
  de/es/pt are in the same cycle.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `unicode.IsControl` misses Cf (U+200B, U+202E, U+FEFF), so a label like `"Admin​"` passed the built-in label check and rendered as "Admin". | **Fixed.** Cf is refused too. Test cases for zero-width space, RTL override and BOM failed before the fix and pass after. |
| 2 | nit | The narrowed skew is redundant with the prune's own grant DELETE, so the table test doesn't isolate it. | Accepted as belt-and-braces. The reviewer showed the FK abort is real when both are absent (A3 below). |
| 3 | nit | `checkCustomRoleKey` reads `RoleOrigin` outside the tx. | Accepted. The decision is re-made inside the tx; the pre-read only gives an early message. |
| 4 | nit | A replay after a new action was installed writes a `cloud_role_saved` audit row with before == after. | Accepted. The audit is accurate (rows were written for the new action). |
| 5 | nit | A bundle with `roles: []` would prune every cloud role. | **Fixed.** An empty roles table never prunes. `TestAdminSync_EmptyRolesTablePrunesNoRole` failed before the fix (pruned, then FK abort) and passes after. |
| 6 | nit | Until #3324 lands, cloud roles render as raw `c_…` columns whose toggles return 409. | Accepted. Tracked on ut-docs#3324, and ut-cloud (#3167) isn't sending role directives yet. |
| 7 | nit | SQLite `LIKE` is ASCII-case-insensitive, so a `C_…` key would also be backfilled. | Accepted. No such key can exist (the key regex is lowercase). |
| 8 | nit | The new key needs language-pack follow-ups. | Done in this cycle: ut-plugin-language-{de,es,pt}. |

## Verified beyond the unit tests

The reviewer re-ran each TDD claim by reverting the production code in a separate worktree:

| Run | What was reverted | Result |
|---|---|---|
| A1 | the skew narrowing only | the test still passes (see #2) |
| A2 | the roles prune | `TestAdminSync_CustomRoleCreateThenDeleteOnMain` fails: "deleted custom role still on the satellite" |
| A3 | the old skew, plus no grant DELETE in the prune | fails with `FOREIGN KEY constraint failed (787)`, the ADR §6 abort scenario |
| B | the `permission_management` check | `TestCloudRoleDirective_Refusals` fails |

Every file was restored afterwards, and the tests passed again.

Other checks:
- `-race` passes on `internal/cloudsync`, `internal/data` and `internal/db`; `internal/db` needs `-timeout 40m` under race, which is package slowness, not this diff.
- `users.role` has no FK onto `roles` (001_init.sql), so pruning a role that a satellite user still holds can't abort `ApplyAdmin`.
- An older primary's bundle without `label`/`origin` still applies and keeps the cloud role.

**UI:** no visual surface was checked. The only UI-facing change is a 409 fragment on an existing page, and the UI work belongs to #3324.

## Verdict

Safe to merge once the two fixes above pass the full gate.

## Deferred

- ut-docs#3323: check-in report.
- ut-docs#3324: Permissions/Users UI, widened assignable roles, audit of the name-based checks, help topics.
- ut-docs#3166: the in-memory bitmask.
