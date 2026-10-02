# Code review: custom roles on the Permissions and Users pages (ut-docs#3324, ut-docs#3190)

- **Date:** 2026-10-02
- **Card:** universaltill/ut-docs#3324 (ADR-0128 §3 "Assigning a custom role", §6). Also closes ut-docs#3190 (Users page showed the raw role id).
- **Author:** autonomous pipeline, `lane:cloud-54` (Opus 5.5)
- **Reviewer:** independent Fable subagent, separate worktree

## What shipped

- **Permissions page:** custom roles (`roles.origin='cloud'`) are extra columns after the built-in ones, ordered by label. The header is the shop's label (escaped) with a "Managed in my.universaltill.com" note. The cells are disabled and never post. The POST already refused cloud roles (#3165). On a phone, `data-card-label` gives the card line the label alone (`table-cards.js` honours it; other tables don't set it, so they're unchanged).
- **Users page:** the role column shows a label for every role: the translation for built-in roles (#3190) and the label for custom roles. Admins and super admins get custom roles in the row picker and in the new-user picker. Previously a custom-role holder's picker preselected "cashier", which was wrong. Pickers are capped at 9rem (`select.users-role-pick`) so a 40-character label can't push "Change role" out of the card.
- **Assignable roles widened** (`user_rules.go`): `isAssignableUserRole(ctx, repo, role)` = a built-in name, or an existing `origin='cloud'` row. `cloudAssignableUserRole` is the same minus `super_admin`. All four callers moved over: users page create and role change, main-till write-through create and set_role, and the cloud `save_user` directive. The directive re-checks the role under its write transaction, so a racing `delete_role` can't leave a user holding a vanished role.
- `AuthRepo.ListCustomRoles` (ordered by label, NOCASE).
- Elevation summaries name a custom role by its label.
- Help: `web/help/{en,de,fa,tr,ar}/users.md` gains a paragraph under Permissions matrix and one under Changing a role. Screenshots regenerated (`make docs-shots`).
- i18n: new key `permissions.cloud_role_managed` in en/ar/fa/tr. Pack follow-ups go to `ut-plugin-language-{de,es,pt}`.

## Name-based check audit (ADR-0128 §6, #2841 AC 5): every check keeps built-in names only

| check | decision |
|---|---|
| `auth.User.IsManager()` | unchanged: custom role → false (session chip only) |
| `auth.Service.AuthorizeManager` (elevation approvals, shift adjustments, pairing) | unchanged: a custom-role PIN is never accepted as manager approval |
| fiscal owner-PIN check (`fiscal_api.go`, admin/super_admin) | unchanged |
| `canManageUser` | unchanged: a custom-role actor holding `user_management` manages cashiers only; a custom-role holder is managed by admin/super_admin only (tested) |
| `authorizeUserChange` (main-till write-through) | unchanged: creating or moving to/from a custom role needs admin, like manager/admin (tested) |
| users page create / role change admin gate | unchanged in code; the comment now says custom roles fall under it (tested: manager gets 403) |
| `lastPrivilegedRoleLeft` / `lastPrivilegedUserGuard` | unchanged: a custom role never counts as the last admin |
| `ensureFirstBootAdmin` | unchanged (first boot has no custom roles) |
| `isAssignableUserRole` / `cloudAssignableUserRole` | **widened** as above |

## Findings

1. **major, fixed:** the docs-shots surface hash was stale after a late template edit. Regenerated.
2. **minor, fixed:** `.users-inline select` cap leaked to the kitchen stations, promotions and tables pickers. Scoped to `select.users-role-pick`.
3. **nit, comment added:** a custom role with zero matrix rows gets no column. Unreachable: the column set is roles × actions.
4. **nit, accepted:** `customRolesForLabels` lists every custom role to label one, on elevation prompts only. A trivial cost.

The reviewer ran build, vet, the pages/data/auth tests, golangci-lint and the i18n/data-access/help-drift guards, and checked authorization, escaping, satellite write-through and help accuracy. None were blocking.

## Verified beyond automated tests

- TDD: the new handler tests fail against the old templates and handler (checked by reverting `users.html` / `permission_settings_page.go`), and pass with the fix. The #3190 test fails with `{{ .Role }}` restored.
- **Driven run:** a real binary with a seeded DB (super admin plus two custom roles), signed in through the PIN pad in Chromium. A custom role was assigned through the real Users page ("Cara" → "Bar"). Permissions showed 42 disabled custom cells. No horizontal page scroll anywhere.
- **Looked at:** Permissions and Users in en at 1024×600 and 360×800, fa (RTL) and de at 1024×600, light theme. **Not looked at:** dark theme, kiosk sizes other than 1024.
- **Pre-existing, not this change:** for a super-admin viewer at 1024px the Users row actions overflow the card (also on `main`, compared side by side). Filed as a Backlog card.
- Gate: `go test ./...`, golangci-lint 0, every ci.yml guard green except `guard-deadcode-baseline` (fails identically on clean `main`: `internal/logging` Stderr/timestampWriter) and `guard-shellcheck-version` (no shellcheck in this container).

## Verdict

Safe to merge.
