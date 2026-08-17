# Review: real DB-level uniqueness for plugin_settings global rows (ut-docs#787)

**Card:** universaltill/ut-docs#787 — "plugin_settings has no DB-level
unique constraint on global rows (scope_id NULL gap) — sync/future
writers still unguarded"
**Complexity:** medium. **Build:** Sonnet (inline). **Review:** Opus
(fresh-context subagent, independent of the build, isolated worktree).

## What was asked

`plugin_settings`' table-level `UNIQUE(plugin_id, key, scope, scope_id)`
(`internal/db/migrations/001_init.sql`) is a no-op for `scope='global'`
rows: `scope_id` is `NULL` for them, and SQLite treats `NULL`s as distinct
in a unique index, so it never actually fires. ut-docs#785 closed the one
known race that exploited this (in `PluginRepo.UpsertPluginSettingScoped`);
ut-docs#788 (migration 052, merged) repaired any pre-existing duplicate
global rows on upgrade. #787 asked for the schema-level backstop itself,
so no writer — present or future, notably the LAN-sync apply path
(`sync_admin_repo.go`'s `applyPluginSettings`, which has no upsert-in-place
guard of its own) — can ever create a new global duplicate.

## Decision

`internal/db/migrations/053_plugin_settings_global_unique_index.sql` adds
`ux_plugin_settings_global`, a **partial** unique index:
`(plugin_id, key) WHERE scope = 'global'`. No dedupe step in this
migration — 052 always runs first (ascending migration order), so the
table is already clean by the time 053 applies.

This deliberately deviates from the card's own literal suggestion (a
general index over all scopes via `COALESCE(scope_id, '')`). The Tester
step caught why during verification: register/user-scoped rows also store
`scope_id` as NULL today, but no migration has ever deduped *those*
scopes (052 is `scope='global'`-only, by its own design). A general index
would make migration 053 itself **fail to apply** — and therefore the app
fail to start — on any till that happened to already carry a pre-existing
register/user duplicate. Scoping the index to `scope='global'` only,
matching exactly what 052 repaired, avoids that risk entirely. Widening
to register/user scope is left as an explicit future follow-up, not
silently done here.

## TDD

Regression test written first
(`TestAdminSyncSharedPluginSettingsRejectsDuplicateGlobalRowsInBundle`,
`internal/data/sync_admin_repo_test.go`): builds an `AdminBundle` with two
`plugin_settings` records sharing `(plugin_id, key, scope='global')` but
different `id`s, applies it via `ApplyAdmin` to a freshly-migrated
replica, asserts the apply fails with the expected UNIQUE-constraint
error and that zero rows land afterward (the whole-bundle transaction
rolls back cleanly, not a partial write). Confirmed failing without the
migration (real assertion failure, not a build error) at three separate
points as the design evolved (initial COALESCE-over-all-scopes version,
the corrected partial-index version, and again after the reviewer's fixes
below), then passing with the migration in place each time.

Two existing tests needed adaptation because the fix correctly makes their
old setup impossible, not because they were wrong:
- `TestReconcilePluginSettingsUpgrade` (`internal/data/plugin_repo_test.go`)
  used to simulate a duplicate by raw-inserting a second row in the *same*
  scope as an existing one — that insert now correctly fails against the
  index. Changed to inject a cross-scope duplicate (register vs. global
  for the same key) instead, which the partial index legitimately still
  allows, keeping `ReconcilePluginSettings`' collapse-to-best-candidate
  logic under test.
- `TestMigration052DedupesGlobalPluginSettings`
  (`internal/db/plugin_settings_dedupe_migration_test.go`) opens a fresh
  DB (which already applies 053) before seeding its simulated pre-#785
  duplicate rows — added `DROP INDEX IF EXISTS ux_plugin_settings_global`
  immediately before that seed step; the test's existing rewind-and-reopen
  step then replays 052 (dedupe) then 053 (recreate the index) in order
  against the seeded bad state, unchanged.

## Independent review (Opus, fresh context, isolated worktree)

Verified independently, not taken on trust: re-ran the TDD red/green cycle
personally (moved 053 out, confirmed the new test fails with a genuine
assertion error, restored it, confirmed it passes); wrote and ran a
throwaway SQLite probe against the actual driver
(`modernc.org/sqlite` v1.29.10) to confirm partial-index semantics
directly rather than trusting the migration's own comment, including
confirming a general (non-partial) index really does fail `CREATE UNIQUE
INDEX` against a table already holding a register-scope duplicate while
the partial version succeeds; grepped every `plugin_settings` reader/
writer in the repo for anything that could rely on a duplicate the new
index would now (correctly) reject.

Findings, triaged:

1. **Wrong index name in two comments** (`plugin_repo_test.go`,
   `sync_admin_repo_test.go` both said `ux_plugin_settings_scoped`, a
   name left over from an earlier, wider version of the design that was
   never shipped) → **fixed**: corrected to `ux_plugin_settings_global`.
2. **Factually wrong comment** — `plugin_repo_test.go`'s test-level
   comment claimed same-scope duplicates "can no longer be created at
   all, same-scope or not," which is false (register/user same-scope
   duplicates are still possible; only global same-scope duplicates are
   blocked, which is the index's entire point) → **fixed**: corrected to
   say "GLOBAL duplicates," matching what the rest of the same comment
   already correctly implied.
3. **Migration header slightly overstated** — claimed the index matches
   "exactly what 052 repaired," but 052's own predicate additionally
   requires `scope_id IS NULL`, which 053's index doesn't check → **fixed**:
   corrected the header to note the (currently unreachable, since nothing
   writes a non-NULL `scope_id` on a global row) gap explicitly rather
   than overclaiming equivalence.
4. **`applyPluginSettings` left unchanged, and that has a real
   consequence worth a deliberate decision** — a bundle from a stale
   primary that still carries a pre-#785 duplicate global row now fails
   the *entire* admin-bundle apply (catalog, users, tax codes, payment
   methods, till roster — not just that plugin's settings), and
   `internal/pages/sync_admin.go`'s pull handler only logs and returns on
   failure, so the replica gets permanently stuck re-failing the same
   apply, while `sync.last_contact_at` (set *before* the apply attempt)
   makes the freshness chip report healthy contact throughout. Reachable
   during a staged multi-till rollout, not hypothetical. **Deferred, not
   fixed here** — this is a genuine fork in behavior (self-healing dedupe
   vs. fail-loud) that deserves its own deliberate decision rather than a
   silent default bundled into this card's schema fix. Filed as
   ut-docs#807 (p2). Doc consequence (below) fixed in this session per
   this repo's standing "update the affected reference/guide in the same
   session" rule.
5. **No manifest-side duplicate-setting-key validation** — a plugin
   manifest declaring the same setting key twice now fails install with a
   raw SQLite error instead of a clean one. Consistent with existing
   precedent for a structurally identical gap on entry keys
   (`manifest.go`), so not a new class of problem — filed as ut-docs#808
   (p3) rather than blocking this card.
6. **Stale comments in `sync_admin_repo.go` and
   `architecture/lan-sync.md`** claiming no upsert could ever target the
   old constraint (true before this card, no longer true — a real
   partial-index constraint now exists, `applyPluginSettings` just
   doesn't use it as an upsert target) → **fixed** in this session, both
   in code and in `ut-docs/architecture/lan-sync.md` (pushed directly to
   `ut-docs` main as a standalone doc-only commit, `04bc00d`), per this
   repo's standing rule that a behavior-adjacent doc going stale is a
   same-session fix, not a follow-up.
7. **Minor: new regression test only asserted an error occurred, not
   which one** → **fixed**: now asserts the specific
   `UNIQUE constraint failed: plugin_settings.plugin_id,
   plugin_settings.key` error text, so a future unrelated `ApplyAdmin`
   failure can't accidentally make this test look like it's still
   covering the right thing.
8. Hand-rolled `plugin_settings` schemas in `manifest_test.go`/
   `ui_smoke_test.go` (built without the new index) now diverge slightly
   from the real schema — noted, folded into ut-docs#808, not blocking.

No correctness defect found in the shipped SQL or the partial-index
design itself — the reviewer's independent judgment agreed with it after
verifying the reasoning from first principles rather than trusting the
comment.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean (repo-wide, both before and
  after the reviewer's fixes above).
- `go test ./internal/data/... -race -timeout=20m` — clean, 716.8s (large,
  slow package under `-race`; default 10-minute `go test` timeout is not
  enough for this package, confirmed the hard way once during Dev).
- `go test ./internal/db/... -race -timeout=10m` — clean, 119.6s.
- `go test ./internal/pages/... -timeout=15m` (full, non-race) plus a
  targeted `-race` pass (`-run 'TestPluginSettings|TestImport_'`) — clean,
  the three packages that read/write `plugin_settings` directly.
- `go test ./internal/plugins/...` (owns `PersistManifest`) — clean.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh` — all clean.
- TDD claim independently re-verified by the reviewer subagent in an
  isolated worktree (not just re-trusted from Dev/Tester's report):
  genuine failing assertion without the migration, passing with it.
- No real client/shop name, no literal credentials — test plugin IDs
  (`com.ut.dup`, `com.t.dupe`, `com.t.p`) and fixture values (`deadbeef`
  sha256, `https://mp/x`) all read as clearly synthetic.
- Backend-only, no UI surface touched: no `web/`, no i18n keys, no manual
  topic affected in `universal-till`. The one doc consequence
  (`architecture/lan-sync.md` in `ut-docs`) was addressed directly.

## Deferred / explicitly out of scope

- ut-docs#807 (p2): `applyPluginSettings` has no defensive dedupe against
  a stale pre-#785 primary poisoning the whole sync-apply, and the
  freshness chip can report healthy contact through a stuck failure.
  Needs its own deliberate decision, not folded into this schema fix.
- ut-docs#808 (p3): manifest duplicate-setting-key validation, a
  `scope` CHECK constraint, and hand-rolled test-schema drift — low
  severity, bundled into one follow-up card.
- Widening `ux_plugin_settings_global` to cover register/user scope is
  explicitly not done — no migration has ever deduped those scopes, and
  neither has a realistic concurrent writer today.

## Safe-to-merge verdict

**Safe to merge.** The partial-index design is correct and was
independently re-derived and verified (not just re-trusted) by a
different model against a finished diff, including a real SQLite probe
of the driver's actual behavior. All reviewer findings were either fixed
in this session (comments, a stale doc, test assertion precision) or
consciously deferred to new, clearly-scoped follow-up cards rather than
silently dropped or scope-crept into this change.
