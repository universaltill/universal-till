# Stop pruning roles/permission_actions/role_permissions on version skew (ut-docs#1589)

**Card:** universaltill/ut-docs#1589 — "role_permissions/roles/
permission_actions sync silently prunes a newer replica's rows during
mid-rollout version skew" (found during #1554's own review, filed
separately per that review's recommendation).
**Repo/branch:** universal-till, `fix/1589-role-permissions-skew-prune`
**Complexity:** medium (Dev at Sonnet, Review at Opus — one round, two
real findings, both fixed in-session; no blocker survived).

## The bug

`SyncAdminRepo.ApplyAdmin`'s phase-1 `deleteMissing` prune deletes any row
a table's PK set no longer includes in the primary's dump. `settings` is
explicitly exempted from this (`sync_admin_repo.go`, "version skew" — a
key a newer replica writes that the primary doesn't know must survive a
pull). #1554 added `roles`/`permission_actions`/`role_permissions` to the
synced set but did not give them the same exemption: a replica one
release ahead of its primary (extra migration-seeded role/action/grant
rows) got those rows silently deleted on the very next sync pull, no
error, no warning. Bounded severity — `AuthRepo.HasPermission` treats a
missing `role_permissions` row as denied (fail-closed), so the practical
effect is over-restrictive, not a privilege leak, and it self-corrects
once the primary upgrades — but real silent data loss in the meantime.

## What shipped

`internal/data/sync_admin_repo.go`:

- `roles`/`permission_actions` join the `settings`/`plugin_settings`
  unconditional prune exemption — verified by repo-wide grep that nothing
  ever `DELETE`s from either table (migration-seeded only, additive-only
  today).
- `role_permissions` gets a **narrower, conditional** exemption via a new
  `deleteMissing(..., skipPrune func(rec map[string]any) bool)` parameter,
  not a blanket skip — see "First-draft regression" below for why the
  blanket version was wrong. A `role_permissions` row is protected from
  pruning only when its `role` or `action` is absent from the *current*
  bundle's `roles`/`permission_actions` dump (real version skew); a row
  whose role and action the primary both already knows about is still
  pruned exactly as before (real same-version drift — primary wins).

`internal/data/sync_admin_repo_test.go`: two new tests plus a
Fatalf→Errorf nit fix (see below) —

- `TestAdminDumpApplyRoundTrip_RolePermissions_SurvivesReplicaAheadOfPrimarySkew`
  — the bug itself: replica has a role, an action, and two grants tying
  them together that the primary has never heard of; all four must
  survive an `ApplyAdmin` pull.
- `TestAdminDumpApplyRoundTrip_RolePermissions_PrunesSameVersionLocalDrift`
  — the regression guard for the finding below: both DBs on the identical
  migration, a satellite-local grant for a role/action the primary already
  knows about must still be pruned (primary wins).

## Independent review — one round, Opus, two real findings

Spawned as a worktree-isolated `general-purpose` subagent (`model: opus`)
against a WIP commit of the first draft (blanket exemption for all three
tables, mirroring `settings` exactly). Ran the full gate itself
(`go build`/`vet`/`gofmt`/`golangci-lint`/`go test ./internal/data/...`/
whole-repo `go test ./...`/`guard-data-access.sh`) and independently
re-verified the TDD claim on the skew test (reverted, confirmed the exact
failure, restored, confirmed green). Confirmed by reading the actual code
(not inference) that revocation-propagation still routes entirely through
the phase-2 upsert (`AuthRepo.SetRolePermission` always upserts —
`INSERT ... ON CONFLICT DO UPDATE SET granted = excluded.granted`, never a
row delete), and confirmed by repo-wide grep that no `DELETE FROM
roles|permission_actions|role_permissions` exists anywhere.

**Finding 1 (MEDIUM, fixed) — the first draft's blanket exemption silently
reopened #1554 in the opposite direction.** Unconditionally exempting
`role_permissions` from pruning (the same shape as `settings`) does not
distinguish "replica ahead of primary" (this card's actual bug) from "a
satellite fabricated a local grant for a role/action the primary already
knows about" — the exact drift #1554's prune phase was healing. Reviewer
demonstrated this empirically with a throwaway probe: a super_admin at a
satellite (gated only on `permission_management`, no primary-only check
anywhere in `permission_settings_page.go`) grants `cashier`/`audit`
locally; before #1554's original fix this was pruned back to denied on
the next pull, after the first draft of *this* fix it survived forever —
more permissive than the primary, the opposite of #1554's fail-closed
intent. Fixed by replacing the blanket skip with the `skipPrune` predicate
described above (`knownRoles`/`knownActions` computed from the incoming
bundle itself), which preserves both properties: version-skew rows
survive, same-version local drift still heals. Locked in with
`TestAdminDumpApplyRoundTrip_RolePermissions_PrunesSameVersionLocalDrift`,
independently confirmed to fail against the first-draft blanket-skip
version and pass against the predicate fix.

**Finding 2 (MEDIUM, fixed) — the first draft's safety comment was
factually backwards.** It claimed "the row this exemption leaves unpruned
always still gets the primary's granted value written onto it in phase 2"
— self-contradictory, since the rows an exemption protects from pruning
are by definition the rows *absent* from the bundle, and phase 2 only
iterates rows *present* in the bundle. Rewritten to state the real
argument (a revoke is an upsert to `granted=0` on a row that stays in the
dump, never a delete — so phase 2 still overwrites it regardless of what
phase 1 does) and to name the Finding-1 tradeoff explicitly, since a wrong
safety comment is exactly what a future maintainer would lean on when
deciding whether to extend this exemption elsewhere.

**Finding 3 (LOW, accepted, noted in comment) —** the "additive-only"
premise (nothing ever deletes a `roles`/`permission_actions` row) is true
today but unenforced; the rewritten comment now says so explicitly and
points at the existing "future custom-roles feature" note on `adminTables`
as the trigger to re-check it.

**Finding 4 (NIT, fixed) —** the two grant-value assertions in the new
skew test used `t.Fatalf` on the `QueryRowContext` error, so a failure on
the `shift_lead`/`refund` axis would abort the test before the
`cashier`/`inventory_count` axis ran (the `permission_actions` count
assertion still covered that axis, so no coverage was actually lost, but
the axis wouldn't show its own failure). Changed both to
`t.Errorf`/`else` so every axis reports independently.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`: clean. `gofmt -l .`: clean (both
  changed files). `golangci-lint run ./...` (whole repo): 0 issues.
- `go test ./internal/data/... -run TestAdmin -race -v`: 19/19 pass
  (22.7s) — includes both new tests plus the pre-existing
  `TestAdminDumpApplyRoundTrip_RolePermissions` (#1554's own
  revocation-propagation guard, unaffected by this diff).
- `go test ./internal/data/...` (full package) and whole-repo
  `go test ./...`: all green.
- TDD re-verified twice, independently: reviewer reverted the skew fix
  and confirmed the skew test's real assertion failures, then restored
  and confirmed green; after applying the reviewer's own fixes, I
  separately reverted to the first-draft blanket-skip version and
  confirmed the new drift test fails against it (count=1, local grant
  survived) before restoring the reviewed predicate fix (count=0).
- `bash scripts/ci/guard-data-access.sh`: passes — both changed files are
  in `internal/data`.
- No migration file touched — the append-only-after-first-shop rule
  (ADR-0074) isn't engaged; this changes only prune eligibility for
  already-synced tables.
- No money, i18n, UI, help-topic, or compliance-wording surface touched —
  those checklists correctly don't apply to this diff.
- No real client/shop name anywhere in the diff — new test identifiers
  (`shift_lead`, `inventory_count`) are generic, invented for the test.

## Safe to merge

Yes. No blocker survived review. Both real findings (the over-permissive
regression risk, and the factually-backwards safety comment) were fixed
in-session and locked in with a dedicated regression test, independently
confirmed to catch the exact first-draft defect. The one accepted note
(Finding 3, additive-only premise) is documented in the code comment
itself rather than deferred to a card — it costs nothing to state and has
no actionable follow-up until a custom-roles feature actually exists.
