# Sync role_permissions/roles/permission_actions to satellites (ut-docs#1554)

**Card:** universaltill/ut-docs#1554 — "Shop-wide sync gaps:
roles/permissions/registers/stock_locations/item_images undecided, no
schema-drift guard, tables/kitchen_stations admin pages not
replica-gated." Follow-up from ut-docs#1546 (universal-till#769, merged).
**Repo/branch:** universal-till, `pipeline/1554-role-permission-sync`
**Complexity:** medium (Dev at Sonnet, Review at Opus — one round, two
non-blocking findings, no blocker; one finding fixed in-session, one
filed as a new card).

## Scope decision — this PR ships only the security-relevant slice

#1554 bundled five distinct gaps into one card. Re-scoping during
BA/Architect found:

- `stock_locations`/`item_images` were already effectively decided in
  `sync_admin_repo.go`'s own comments, just not clearly recorded as such
  (`item_images`: files don't travel over the D2 bundle — genuinely
  settled). `stock_locations`/`registers` need an actual usage trace
  (multi-location semantics, `stock_locations.name` UNIQUE collision risk)
  that deserves its own focused pass, not a guess folded into this PR.
- The schema-drift CI guard requires classifying **~75 tables** (only
  ~23 currently in `adminTables`) — a rushed classification here would be
  worse than none (a wrong "safe to exclude" verdict reproduces the exact
  bug class the guard exists to catch, just behind a green check).
- No admin page in this codebase (checked `catalog_page.go`,
  `users_page.go`, `registers_page.go`, `locations_page.go`,
  `permission_settings_page.go`) currently blocks or warns on a satellite
  write to any shop-wide table — #1554's premise that "other shop-wide
  admin pages presumably already gate on primary-only" is false. The
  `tables`/`kitchen_stations` replica-write protection it asks for would
  be a **new cross-cutting mechanism**, not a two-file patch reusing an
  existing pattern, and plausibly generalizes to every shop-wide admin
  page — worth its own Architect/UX pass.

So this PR ships the one part that is both well-understood and genuinely
security-relevant — the rest is split into three follow-up Backlog cards
(all filed against `universaltill/ut-docs`, all referencing this PR):
**#1584** (registers/stock_locations decision), **#1585** (replica-write
protection UX), **#1586** (schema-drift guard). #1554 itself is closed by
this PR's merge with a narrative explaining the split, matching the
precedent already set by #1546→#1554 itself.

## What shipped

`roles`, `permission_actions`, `role_permissions` added to `adminTables`
in `internal/data/sync_admin_repo.go`, right after `users`:

```go
{name: "roles", pk: []string{"role"}},
{name: "permission_actions", pk: []string{"action"}},
{name: "role_permissions", pk: []string{"role", "action"}},
```

**The actual bug fixed:** `role_permissions` is runtime-mutable
(`AuthRepo.SetRolePermission`, wired from the permission-matrix editor)
but was entirely absent from `adminTables`. A manager revoking, say,
`refund` from cashiers on the primary left every satellite still granting
it — a real security gap, not a convenience one. `roles`/
`permission_actions` are included too even though they're migration-seeded
only (no runtime `INSERT` anywhere) and so already match across tills
today — but only "because every till seeds them identically," the exact
fragility #1554 itself calls out (a future custom-roles feature, or two
tills mid-rollout on different migration versions, would silently break
that assumption).

Insert order (`roles`, `permission_actions` before `role_permissions`)
matches the schema's own FK references (`role_permissions` REFERENCES
both; the other two have no FK dependencies) — verified against
`internal/db/migrations/001_init.sql:729-742`. Delete order (reverse of
insert) is therefore also correct: children (`role_permissions`) before
parents.

**Test:** `TestAdminDumpApplyRoundTrip_RolePermissions` — proves the two
properties seeding alone can never cover: a **new** grant made on the
primary reaches the satellite, and a **revoked** grant does too (the
actual bug). Uses `wireTrip` like every other `ApplyAdmin` call in this
file, so the JSON-hop float64 conversion on the one INTEGER column
(`granted`) the test asserts on is genuinely exercised.

Also strengthened the top-of-file comment: it used to assert
`stock_locations (per-till)` as a settled fact with no rationale; now it
states plainly that this is an open decision tracked by #1584.

## Independent review — one round, Opus, no blocker

Spawned as a worktree-isolated `general-purpose` subagent (`model: opus`)
against a WIP commit. Ran the full gate itself (`go build`/`vet`/
`gofmt`/`golangci-lint`/`go test ./internal/data/... -race`/
`go test ./...`/`guard-data-access.sh`) and independently re-verified the
TDD claim: reverted just the 3 new `adminTables` entries, confirmed
`TestAdminDumpApplyRoundTrip_RolePermissions` fails on both the new-grant
and the revoke assertions (`granted=1` on the satellite for a permission
the primary had revoked — the literal security gap this PR closes),
restored, confirmed green again. Also independently verified FK-safety in
both insert and delete directions by reading `001_init.sql` and
`ApplyAdmin` directly (not by inference), and confirmed neither of the two
recurring bug classes this pipeline watches for (missing `os.MkdirAll`, a
cwd-relative path instead of `paths.Data(...)`) applies — no file/path
I/O anywhere in this diff.

**Non-blocking, fixed in-session.** The new test was the only
`ApplyAdmin` call in the file skipping `wireTrip` (the helper that
simulates the JSON wire hop, numbers becoming `float64`) — the one
INTEGER column (`granted`) the test asserts on never actually crossed
that hop. Reviewer proved SQLite's INTEGER affinity happens to make this
harmless today (no latent bug), but fixing it costs nothing and matches
the file's own established convention (every other `ApplyAdmin` call
already does this) — done.

**Non-blocking, deferred as a new card.** Reviewer found this fix
introduces a **new** hazard, verified empirically (not by inference): a
replica running a newer migration than its primary (mid-rollout version
skew) has its extra `roles`/`permission_actions`/`role_permissions` rows
silently pruned on the very next sync pull — the exact same shape
`settings` is explicitly exempted from pruning for
(`sync_admin_repo.go:281-288`), which this PR's new tables were not given.
Bounded severity (`HasPermission` treats a missing row as denied —
fail-closed — and it self-corrects once the primary itself upgrades), so
not a blocker, but real silent data loss on the replica in the meantime.
Filed as **ut-docs#1589**, `complexity:medium`.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`: clean. `gofmt -l .`: clean (both
  changed files). `golangci-lint run ./internal/data/...`: 0 issues.
- `go test ./internal/data/... -run TestAdmin -race -v`: 17/17 pass
  (22.7s). `go test ./internal/pages/... -run "Permission|Sync"`: pass —
  covers `permission_settings_page.go`'s actual write path and
  `TestTablesPagePermissions`/`TestUsersPage_*RequiresPermissionManagement`.
- `go test ./internal/data/... -race` (the package's own **full** suite,
  not just the `TestAdmin*` subset) hit the environment's known
  pre-existing timeout (ut-docs#1366, "internal/data's full test suite
  exceeds the default go test timeout under -race" — not new, not caused
  by this diff); the targeted `-race` run above plus a full non-race
  `go test ./...` (also green, run by the independent reviewer) is the
  practical substitute this pipeline already accepts elsewhere for that
  same known gap.
- `bash scripts/ci/guard-data-access.sh`: passes — both changed files are
  in `internal/data`, no SQL added outside the repository layer.
- No migration file touched (`git diff --name-only main.. -- internal/db/`
  is empty) — the append-only-after-first-shop rule (ADR-0074) isn't
  engaged; this changes only which existing tables sync, not the schema.
- No money, i18n, UI, help-topic, or compliance-wording surface touched —
  those checklists correctly don't apply to this diff.
- No real client/shop name anywhere in the diff — new test data
  (`cashier`/`admin`/`refund`/`audit`) is literally the product's own
  migration-seeded role/action catalog.

## Safe to merge

Yes. No blocker. One trivial finding fixed in-session (wireTrip
convention); one real-but-bounded finding filed as ut-docs#1589 rather
than folded in here, since fixing it correctly means designing the same
kind of version-skew exemption `settings` already has — a deliberate,
separate piece of work, not a one-line addition to this diff. Three
further follow-up cards (#1584, #1585, #1586) carry the rest of #1554's
original scope, each independently actionable.
