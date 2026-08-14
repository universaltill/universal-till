# Review: Auth Can() sweep — Data / sync / device-pairing pages (ut-docs#707)

**Date**: 2026-08-14
**Card**: universaltill/ut-docs#707 — successor of #555 (splitting the
blanket `isManagerOrAuthOff` gate into per-subsystem `Auth.Can()` checks,
one subsystem at a time — same pattern as #554/#706/#709)
**Complexity**: medium
**Reviewer model**: fresh-context Opus subagent, worktree-isolated (per
this card's `complexity:medium` tier — see `scrum-master` skill's model
routing)

## What shipped

Replaced every `isManagerOrAuthOff(r)` call site in
`internal/pages/data_api.go`, `backup_api.go`, `sync_api.go`,
`discovery_api.go`, `pairing_api.go`, `pending_pairings.go` (18 sites) with
`canPerform(d, r, action)` checks against two new `role_permissions`
catalog actions.

- `internal/db/migrations/044_data_sync_management_permissions.sql` (new,
  append-only): adds `data_management` and `sync_management` to the
  catalog, granted to manager/admin/super_admin — same seed shape as
  039/042/043.
- `data_management`: `data_api.go`'s reset-transactions, reset-archives
  list/restore/purge, GDPR customer search/erase, catalog cleanup
  preview/apply, data/export (9 sites) + `backup_api.go`'s shared `deny`
  closure covering backup now/download/save-copy/restore (4 routes).
  Grouped as one action because every one of these is the same shape of
  operation — destroy/restore/export the till's own stored data — same
  precedent as #706's `plugin_management` grouping a whole subsystem.
- `sync_management`: `sync_api.go`'s Tills page/enroll-token/revoke/
  promote/join (5 sites), `pairing_api.go`'s pending pair-request list,
  `pending_pairings.go`'s pending-pairings UI.
- `internal/pages/api_gates.go`'s `managerGate` — the shared `apiGate`
  behind `discovery_api.go`'s `/api/sync/discover-primaries` — changed
  from a bare `func(w, r) bool` to a `func(d *common.Deps) apiGate`
  constructor so it could call `canPerform(d, r, "sync_management")`.
  That same function also gates `pairing_join.go`'s `/api/sync/pair-start`
  and `/api/sync/pair-status` — **not in #707's named file list**, but
  converting the shared gate necessarily converts them too (documented in
  the migration's own comment). The first-boot-gated flavours
  (`/api/setup/*`, using `firstBootGate` instead) were correctly left
  untouched.
- Test updates: fixed a latent nil-`AuthSvc` deref in
  `pairing_api_test.go`'s `newPairingAPITestDeps` (an existing test,
  `TestListPairRequests_RequiresManager`, already exercised a real cashier
  session — harmless under the old gate, which never touches `AuthSvc`,
  but would have panicked under the new one); extended
  `ui_smoke_test.go`'s `seedForPages` catalog list; added two new
  cashier/manager/admin/super_admin role-matrix test files mirroring
  #706's `plugin_api_manager_gate_test.go` precedent:
  `data_backup_manager_gate_test.go`, `sync_pairing_manager_gate_test.go`
  (the latter also covers the two out-of-file-list `pairing_join.go`
  routes).

## Independent review (fresh-context Opus, worktree-isolated)

**Verdict: safe to merge**, with two should-fix items applied before
commit and two nits taken. No security defect, missed route, or bypass —
the reviewer independently re-counted `isManagerOrAuthOff` call sites on
`main` per file (matched the diff exactly, 18/18 converted, 0 left), diffed
every "simple" file to confirm no logic drifted in alongside the gate
swap, `git grep`'d every `managerGate` reference repo-wide (all 3 updated,
Go's type system backstops a leftover anyway), and confirmed the
first-boot flavours and every other non-`isManagerOrAuthOff` route in the
touched files were correctly left alone.

Ran (and re-ran after fixes): `go build ./...`, `go vet ./internal/pages/...`,
`go test ./internal/pages/... -race` (green, 593s), `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-compliance-claims.sh` — all green.

**Findings (should-fix, both applied):**

1. **Migration 044's grants were unpinned.** `internal/data/auth_repo_test.go`
   is the *only* place in the codebase that exercises `HasPermission`
   against a really-migrated DB rather than `internal/pages`' hand-rolled
   `seedForPages` fixture (which seeds every role for its own catalog list
   regardless of what a migration actually grants). The reviewer proved
   this by mutating migration 044 (dropping `sync_management`, then
   dropping `admin`/`super_admin` from the granted-roles list) and
   confirming `internal/pages`, `internal/data`, and `internal/db` all
   stayed green either way — a typo locking every admin out of Data/Backup/
   Tills/pairing would have shipped undetected. Fixed: added
   `"data_management", "sync_management"` to `TestAuthRepo_HasPermission`'s
   two action lists (established by #709's review as the load-bearing
   pin for every migration since).
2. **New gate tests covered only cashier/manager.** The reviewer reverted
   `pending_pairings.go`'s call site to the old `isManagerOrAuthOff` and
   confirmed the new test still passed — cashier/manager is exactly where
   the old and new gates agree, so that matrix couldn't have caught an
   unconverted call site. Fixed: both new test files now check
   `manager`/`admin`/`super_admin` (matching #706/#709's full-role-matrix
   precedent, e.g. `update_api_test.go`'s `TestPostSettingsUpdateSchedule_
   RealSessionGatesByRole`).

**Nits (taken):**

3. `discovery_api_test.go`'s `newDiscoveryAPITestDeps` had the same latent
   missing-`AuthSvc` gap as `pairing_api_test.go` did before this diff's
   fix — harmless today (every test in that file sends no session), but a
   future session-bearing test added there would panic. Set `AuthSvc` on
   its `Deps` construction.
4. A repurposed doc comment in `sync_api_test.go` sat directly above
   `TestSyncEnrollTokenAndEnroll_FullPairingFlow`, so godoc would attribute
   a package-scoped note to one specific test. Reworded and given its own
   paragraph break.

## Verified beyond automated tests

- Full `go test ./...` (whole repo, not just `internal/pages`) green.
- `go test ./internal/pages/... -race` green (run twice: once by this
  session, once independently by the reviewer subagent in its own
  worktree — 593–595s each, no data races).
- Manual accounting: `git grep -c isManagerOrAuthOff` on `main` vs. this
  branch for all 7 touched files (6 named + `api_gates.go`) — 18 real call
  sites on `main`, 0 remaining post-change, matching the diff exactly.
- Reviewer independently mutated the `sync_management` action string in
  `api_gates.go` to `"sync_managementX"` and confirmed exactly the 3
  `managerGate`-gated routes (discover-primaries, pair-start, pair-status)
  failed — proof the new tests genuinely resolve through the real
  permission machinery, not a role-name shortcut, and that the two
  out-of-file-list `pairing_join.go` routes are really covered.

## Explicitly checked, no action needed

- No new user-facing string (this is a pure backend gating-mechanism swap)
  — `guard-i18n.sh` green, no locale touched.
- `web/help/` — no update needed. No route added or removed, no visible
  behavior changed for any role that exists in production (manager/admin
  still in, cashier still out); `guard-help-topics.sh` green.
- README — its "Employee permissions" / "Role-based access control"
  claims remain accurate; no edit needed.
- No real client/shop name, no secret-shaped literal, in any new test
  fixture.

## Safe-to-merge verdict

Yes. Merged via `merge_method: "merge"` (never squash/rebase — ut-docs#250)
once CI is green on the PR.
