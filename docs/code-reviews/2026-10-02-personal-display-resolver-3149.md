# Code review: personal display part 1/3 — per-user storage + session-aware render resolver (ut-docs#3149)

- **Date:** 2026-10-02
- **Card:** universaltill/ut-docs#3149 (Part 1 of 3, rescoped at the BA +
  Architect pass). Follow-ups: #3438 (call-site migration), #3440 ("My
  display" page, grants, menu entry).
- **Branch:** `feat/3149-personal-display-part1-resolver`
- **Author:** autonomous pipeline, Opus 5.5 dev subagent; Tester PASS before
  this review.
- **Reviewer:** independent Fable 5.1 subagent (different model from the
  author), reading the diff cold; every TDD claim re-run by mutation, see
  below.
- **Verdict: SAFE TO MERGE**, with one scope deviation (finding 1) that the
  orchestrator must accept explicitly *before* merging — migration files are
  frozen once on `main` (ADR-0100), so this is the last cheap moment to
  change 061 if the Architect wants the deviation undone.

## What shipped

- `internal/db/migrations/061_user_display_settings.sql`: table
  `user_display_settings (user_id, key, value)`, PK `(user_id, key)`, FK to
  `users(id)` without cascade (operators are deactivated, never deleted —
  confirmed: no `DELETE FROM users` exists outside tests). Three
  `sync_admin_version` triggers ship with it (same as 025/031). Also
  `INSERT OR IGNORE INTO permission_actions ('personal_display')` with **no**
  `role_permissions` row — see finding 1. Checksum pinned in
  `shipped_migrations_test.go`.
- `internal/data/user_display_repo.go`: `UserDisplayRepo`
  `Get/GetAll/Set/Delete`, every statement a `?`-parameterised literal, all
  scoped by `user_id`. Six tests over a real migrated DB, including an
  admin-bundle dump/apply round trip and the FK refusal.
- `internal/pages/common/personal_display.go`: `Deps.ResolvedTheme(r)`,
  `Deps.ResolvedBrowsingMode(r)` and the private `personalDisplayValue`,
  which returns the operator's row only when: `Db` non-nil → operator in the
  request context with a non-empty ID → (`UT_AUTH=off`, or `AuthSvc` non-nil
  and `Can(personal_display)` true with no error) → row found. Anything else
  falls through to `CurrentState()`. The browsing mode is clamped to the
  closed enum; a blank theme is treated as absent. Thirteen tests after this
  review (twelve from Dev plus one added here).
- `sync_admin_repo.go`: `user_display_settings` added to `adminTables` after
  `users` (upserts forward, prunes in reverse). `permission_groups.go`: the
  action placed in the `settings` group. Two keys each in en/ar/fa/tr.
  `deadcode-baseline.txt`: +8 entries (the repo's 5 methods and the 3
  resolver functions — inert by design until #3438/#3440). `manifest.json`:
  surface hash only; all 120 screenshots byte-identical (Tester).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium (scope) | **The permission action ships in Part 1 against the Architect's design.** The card body says "do **not** add the `personal_display` permission action ... yet (that's Part 3)" and "`personal_display` as a permission *action* is Part 3's migration"; #3440 §1 plans its own migration registering the action, the `settings`-group placement and the `en.json` keys. 061 does all three now. Consequences: (a) `/users/permissions` gains a "Personal display" row an owner can tick that does nothing until #3438 + #3440 land — the "box the till never checks" shape #3134 hid other actions for; (b) #3440's Dev must *not* re-add the action/keys/placement (a duplicate locale key fails `guard-i18n_dupkey`, a duplicate placement fails `TestPermissionGroups_EveryActionInExactlyOneGroup`) — its migration should only seed the grants; (c) the de/es/pt language-pack catch-up PRs for the two new keys are owed this cycle, not with Part 3. Risk assessed as inert: nothing calls the resolver, no save path exists, `HasPermission` has no role bypass so every built-in role (super_admin included) falls through, and a grant ticked in Part 1 survives Part 3's `INSERT OR IGNORE` unchanged. The opposite reading — that 061 pre-registering the action is the pragmatic choice because 061 has to touch `permission_actions`-adjacent guards anyway — is defensible, which is why this is **flagged, not reverted**: it is an Architect/orchestrator call, and reverting means editing a migration whose checksum, group table, locale files and docs-shots manifest were all built on this state. | **Flagged.** Orchestrator: accept explicitly (and annotate #3440 with (b) and the owner-visible row), or bounce to Dev to strip lines 61–62 of 061 plus the group entry and keys before merge. Either way merge-safe. |
| 2 | low | Stale doc comment on `PersonalDisplayAction` said the action "is not registered in permission_actions until part 3" — the Architect's plan, not what 061 does. | **Fixed** (comment now describes the shipped state: registered, ungranted, older schema resolves the same). |
| 3 | low (test gap) | No test exercised the "DB errors" fallback the resolver's own comment promises ("must not be able to break a page render"). | **Fixed.** Added `TestResolved_DbErrorFallsThrough`: renames `user_display_settings` away (row read errors while `Can` still grants) → till values; restores it → personal values again; renames `role_permissions` away (permission read errors) → till values. Passes; the resolver needed no change. |
| 4 | info | `TestResolved_NoOperatorInContextFallsThrough` is not load-bearing for the `!ok \|\| u.ID == ""` guard: with the guard removed it still passes (mutation M6), because an empty `user_id` can never match a row and the FK to `users` stops a test seeding one. | Accepted. The guard is correct defensive code; the test still pins the behaviour. |
| 5 | info (design, out of Part 1 scope) | **Admin-sync prune.** `DumpAdmin` emits a `user_display_settings` key for every bundle (even empty, `bundle.Tables[t.name] = recs` unconditionally) and `ApplyAdmin`'s generic `deleteMissing` prunes every satellite-local row the main till's bundle lacks. So a preference saved on a till that follows a main till would vanish on its next pull. **Not a Part 1 bug**: `Set` is baseline-dead and no endpoint exists, so no row can be written on any till yet. It *is* a hard constraint on #3440's `POST /api/my-display/*`, which that card does not mention: either refuse on a follower till (the `/api/users/permissions` 409 precedent — poor UX for a personal setting) or write through to the main till (`tables_sync_proxy.go` precedent). The classification itself is right for the feature ("follows the person to every till") and the migration header and `adminTables` comment both record the constraint. | Accepted for Part 1. **Orchestrator: add this constraint to #3440.** |
| 6 | info | `ResolvedTheme` does not validate the personal value against the installed themes — same as `LoadState` for the till theme; templates HTML-escape it, so no injection, only a possibly unknown key. #3440's "uninstalled theme plugin falls back" criterion therefore belongs to its own write-side validation and the CSS-serve layer, not to this resolver. | Accepted; noted for #3440. |
| 7 | info | Deadcode baseline grew by 8 (the guard says it should shrink). Unavoidable for inert infrastructure shipped on purpose; the guard does not force removal when they become reachable. | Accepted. **#3438 must drop the 3 resolver entries and #3440 the 5 repo entries** from `deadcode-baseline.txt`, or they linger stale. |

Security review (permission/authz-adjacent code):

- **Wrong-user / leak:** the only user id the resolver ever uses is
  `auth.FromContext(r.Context()).ID` — never a request parameter — and every
  repo statement binds `user_id`. Mutation M3 (below) shows the suite catches
  a cross-user leak at both layers.
- **SQL injection:** none. All four repo statements are literal SQL with `?`
  placeholders; no identifier or value is concatenated.
- **Fail closed, no panic:** nil `Db`, no operator, empty ID, nil `AuthSvc`
  (M2 shows the guard is what prevents a nil-pointer panic), `Can` error,
  `Can` false (M1), row-read error (finding 3), no row — all return the till
  value. `UT_AUTH=off` skips only the permission check (same as
  `pages.canPerform`) and still needs an operator in context; in that mode
  the real middleware puts no operator in context, so production under
  `UT_AUTH=off` always falls through.
- **Role bypass:** `AuthRepo.HasPermission` is a plain `role_permissions`
  lookup with no super-admin shortcut, so "registered, granted to no role"
  really is inert for every role (pinned by
  `TestResolved_AsShippedNoRoleGrantedFallsThrough`).
- **Version skew:** a follower on 061 pulling from a main still on 060 sees
  no `user_display_settings` key and skips the table; `permission_actions` is
  exempt from pruning, so the extra action row survives. A follower on 060
  pulling from a main on 061 ignores the unknown table. Covered by
  `TestResolved_ActionNotRegisteredFallsThrough` for the resolver side.

## Verified beyond the automated tests

TDD claims re-run by the reviewer, each as revert → run → restore inside one
command, on this checkout (no commit landed in between; every file was
compared byte-for-byte against its pre-mutation copy afterwards):

| Run | Mutation | Result |
|---|---|---|
| M1 | resolver ignores the granted flag (`err != nil \|\| !can` → `err != nil`) | `TestResolved_ActionNotRegisteredFallsThrough`, `_AsShippedNoRoleGrantedFallsThrough`, `_RegisteredButNotGrantedFallsThrough` all fail with the personal value leaking through |
| M2 | nil-`AuthSvc` guard removed | `TestResolved_NilAuthSvcFallsThrough` fails: nil pointer dereference panic |
| M3 | `Get` predicate neutralised (`user_id = ? OR 1=1`) | `TestUserDisplayRepo_SetGetRoundTripAndOverwrite` ("Get for bob = light"), `_DeleteThenGetMisses` and `TestResolved_GrantedWithRowReturnsPersonal` (cashier sees the manager's values) fail |
| M4 | 061 statement edited (`DEFAULT ''` on `value`) | `TestShippedMigrationsUnchanged` fails naming the pinned vs new checksum. (A plain `sha256sum` of the file does not equal the pin — `migrationChecksum` strips comments and blank lines first; noted so nobody reads that as a wrong pin.) |
| M5 | `adminTables` entry removed | `TestSchemaTablesAreClassified` ("classified in neither adminTables nor nonAdminTables") and `TestUserDisplaySettings_AdminDumpApplyRoundTrip` fail |
| M6 | no-operator guard removed | still passes — finding 4 |

CI gate, run locally from `.github/workflows/ci.yml` (not a blanket
`go test ./...`): `gofmt -l .` clean; `go build ./...` ok; `go vet` on the
touched packages ok; `golangci-lint run ./...` 0 issues;
`guard-data-access`, `guard-migration-version-collision`, `guard-i18n`,
`guard-demo-env`, `guard-page-http-error`, `guard-competitor-naming`,
`guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`,
`guard-help-drift` (only the pre-existing tracked drifts), `guard-plugin-menu-read`,
`guard-kiosk-engine`, `guard-price-history-sync`, `guard-no-showmodal`,
`guard-core-neutral` all exit 0; `go test -count=1` on `internal/data`,
`internal/pages/common`, `internal/db`, `internal/auth`, `internal/settings`
and `internal/pages` (169s, includes the permissions-page and
`TestPermissionGroups_*` guards) all ok, re-run after the review's own edits.
`guard-deadcode-baseline` fails locally on `internal/logging.Stderr` /
`timestampWriter.Write` only — both are called solely from
`cmd/unitill-desktop`, which the local run skips without GTK headers
(the script's own documented #2425 carve-out); real CI analyses all three
roots. The 8 new entries this diff adds were accepted by the guard, i.e.
they are exactly the new unreachable set.

Docs-shots after this review's own edits: the comment fix (finding 2) is in
a route-less `internal/pages/**.go` file, which the surface hash covers
whole, so `guard-docs-shots` went red for a zero-pixel change. Recomputed
only `surface_sha256` with `scripts/ci/update-docs-shots-surface-hash.sh`
(its documented #2102 case); the guard passes again, the manifest diff is
that one line, no PNG or topic hash moved. **The commit must carry the
`Docs-Shots-Unchanged: true` trailer.**

UI/manual: no new page. The one visible change (an unticked "Personal
display" row under Settings on `/users/permissions`) is on no screenshotted
route — `/users/permissions` is `routes[4]` of the `users` help topic and only
`routes[0]` (`/users`) is captured — which matches the byte-identical 120
screenshots. No help topic enumerates permission actions, so nothing to
update; #3440 owns the feature's help topic. Tester had already run the
Playwright `permissions-matrix-3132` spec (incl. RTL and phone layouts) and a
byte-for-byte HTML diff of every existing page against a pre-Dev binary;
not repeated here. Translations read correctly (ar/fa "personal display
settings", tr "personal appearance"); no real shop/client names; no secrets.

## Deferred

- **Orchestrator, before merge:** decide finding 1; annotate #3440 with
  findings 1(b), 5 and 6; language-pack PRs (de/es/pt) for
  `permissions.action.personal_display` and
  `permissions.action_desc.personal_display` this cycle (brand-new keys:
  core merges first, `lang-pack-drift` goes red as expected).
- #3438: swap call sites; drop the 3 resolver deadcode entries.
- #3440: grants-only migration (action already registered); write path must
  land on or through the main till; validate theme keys on write; drop the 5
  repo deadcode entries.
