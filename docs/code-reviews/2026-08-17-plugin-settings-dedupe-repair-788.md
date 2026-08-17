# Review: repair path for duplicate plugin_settings global rows (ut-docs#788)

**Card:** universaltill/ut-docs#788 — "no repair path for plugin_settings
duplicate global rows created before ut-docs#785's fix"
**Complexity:** easy. **Build:** Sonnet (inline). **Review:** Sonnet
(fresh-context subagent, independent of the build, isolated worktree).

## What was asked

`plugin_settings`' `UNIQUE(plugin_id, key, scope, scope_id)` doesn't catch
duplicate `scope='global'` rows because `scope_id` is `NULL` for them and
SQLite treats `NULL`s as distinct in a unique index. ut-docs#785 closed the
race in `UpsertPluginSettingScoped` that created them, but did nothing for
a till that already has some. #788 asked for either a repair mechanism or
a documented decision that the existing lazy self-heal (`PluginRepo.
ReconcilePluginSettings`, which runs on a plugin's next install/upgrade)
is an acceptable window.

## Decision

Ship a repair mechanism rather than just document the gap: a one-off SQL
migration, `internal/db/migrations/052_dedupe_plugin_settings_global.sql`.
Migrations apply automatically on every `db.Open()` (standard
versioned-migration pipeline in `internal/db/migration.go`/`db.go`), so
this repairs an affected till proactively on its next app start, rather
than waiting for that till's next plugin install/upgrade — the stronger
of #788's three suggested options, and no Go code changes needed.

The migration deletes all but the most-recently-updated `scope='global'`
row per `(plugin_id, key)`, deterministically tie-broken on `id` when
`updated_at` collides at second-resolution. Deliberately scoped to
`scope='global'` only, matching #785's own review scope — register/user
rows share the same `scope_id IS NULL` gap in principle, but #785 scoped
the finding to global as the only scope with realistic concurrent
writers, and this migration mirrors that rather than widening it
unilaterally.

## TDD

Regression tests written first (`internal/db/plugin_settings_dedupe_migration_test.go`),
confirmed failing against the code without migration 052 (both duplicate
rows survived, real assertion failure — not a compile error), then the
migration added and the tests confirmed passing.

Two tests: one seeds a genuine pre-#785 duplicate-row scenario (two
`scope='global'` rows for the same key, plus a non-duplicated global row,
plus a register-scoped duplicate pair sharing a key) and confirms only
the global duplicate collapses, to the newer row, with everything else
left alone; the other confirms 052 is idempotent by actually re-applying
it against already-clean data and checking the surviving row is
byte-identical.

## Independent review (Sonnet, fresh context, isolated worktree)

Findings, triaged:

1. **Misleading comment reference** — the migration's header comment
   justified the "keep latest write" tiebreak by pointing at "InstallPlugin
   ... via CASE scope ordering," but the only `CASE scope` ordering in the
   codebase is `GetPluginSetting` (scope-priority selection for reads, not
   a recency tiebreak) and isn't part of `InstallPlugin` at all →
   **fixed**: corrected the comment to accurately describe this as a fresh
   deterministic-fallback call, not reuse of an existing pattern.
2. **Idempotency test didn't actually test idempotency** — it asserted a
   seeded single row's count after one migration run, never re-applied
   052 → **fixed**: rewrote it to rewind `schema_migrations`, close/reopen
   (genuinely re-applying 052 a second time), and assert the surviving
   row is unchanged.
3. **SQL correctness** — hand-traced the `ROW_NUMBER()`/`PARTITION BY`
   logic independently: singleton rows always keep `rn=1` and survive;
   only true duplicates within the same `(plugin_id, key)` global
   partition collapse; every non-global row and every global row with a
   non-NULL `scope_id` is excluded by both the inner and outer `WHERE`.
   No edge case found where the wrong row could be deleted.
4. **Migration numbering / append-only** — confirmed 052 is a new file,
   051 untouched, numbering contiguous.
5. **Scope decision re-verified independently** — checked
   `ReconcilePluginSettings` directly: it already groups by key across
   *all* scopes and lazily heals register/user duplicates too on next
   install/upgrade, so leaving them out of this at-rest migration doesn't
   leave them permanently unrepaired, just on the pre-#788 lazy path
   #788 itself accepted as adequate for global before this migration
   shipped. Judged defensible, not a real gap.

No blockers found.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/db/...` (full package) — clean, nothing else broken.
- `gofmt -l internal/db/plugin_settings_dedupe_migration_test.go` — clean.
- `bash scripts/ci/guard-data-access.sh` — clean; correctly does not flag
  the `.sql` migration file (the guard targets Go source with inline SQL
  outside `internal/data`/`internal/db`, which a migration file is exempt
  from by design).
- `bash scripts/ci/guard-kiosk-engine.sh`, `bash scripts/ci/guard-plugin-menu-read.sh`,
  `bash scripts/ci/guard-i18n.sh` — all clean (no self-order routes,
  plugin-menu reads, or user-facing strings touched).
- TDD claim independently re-verified twice (once during Dev, once by the
  reviewer in an isolated worktree): moved the migration file out, reran
  the targeted tests, confirmed a genuine assertion failure (`global
  apiKey rows after 052 = 2, want exactly 1`), restored the file,
  confirmed both tests pass again.
- No real client/shop name, no literal credentials — test plugin IDs
  (`com.t.dupe`, `com.t.clean`) and fixture values (`deadbeef` sha256,
  `https://mp/x`, `1.2.3.4`/`5.6.7.8`) all read as clearly synthetic.
- Backend-only: diff touches only `internal/db/migrations/052_...sql` and
  `internal/db/plugin_settings_dedupe_migration_test.go` — no `web/`, no
  `internal/pages/*.html`, no i18n keys, no manual topic affected.

## Safe-to-merge verdict

**Safe to merge.** Fix is correct and independently re-verified; both
review findings (a misleading comment reference, an under-testing
idempotency-test name) were non-blocking documentation/test-quality
nitpicks, fixed before this record was written rather than deferred.
