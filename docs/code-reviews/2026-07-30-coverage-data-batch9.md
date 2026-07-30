# Code review — coverage batch 9: `internal/data/plugin_repo.go` (2026-07-30)

**Branch:** `test/coverage-plugin-repo-batch9` · **PR:** #99 · **Scope:**
test-coverage push batch 9 (`ut-docs/QUEUE.md`, "Test-coverage push,
remainder"). **Coverage:** package `internal/data` 76.2% → **82.1%**;
`plugin_repo.go` 25 zero-coverage functions → 0 remaining at 0% (48/48 ≥60%).
**Reviewer:** independent different-model review (opus subagent), author =
pipeline dev (sonnet). **Verdict: SAFE TO MERGE — test-only diff, no
blocking findings.**

## What changed

Three new test files, no production code touched (this batch found no bug
that needed a production fix in the functions it targeted):

- `plugin_repo_manifest_hooks_test.go` — manifest lifecycle
  (`UpsertPluginManifest`/`DeletePlugin`/`UpdatePluginTrust`), audit logging
  (`InsertAudit`/`InsertAuditRaw`), `ReplacePluginEntries`,
  `InsertPluginHooks`, `InsertPluginPermissions`, `ListAutoStartPlugins`,
  `ListManagedPlugins`.
- `plugin_repo_permissions_storage_test.go` — the permission-check + KV
  storage security surface: `HasActiveHook` (via the hooks test above),
  `CheckPermission`, `SetPermission`, `ListPermissions`, `PluginActive`,
  `StorageGet`/`StorageSet`/`DeleteStorage`.
- `plugin_repo_catalog_test.go` — `EnsureCatalogEntry`, `UpsertCatalogEntry`,
  `ListCatalog`, `ListReceiptTemplates`, `ListPaymentEntries`,
  `SyncPluginPaymentMethods`, `ListPageEntries`.

All use the real migrated schema (`openMigratedDB`, already established by
batch 7) rather than a hand-rolled one — `plugins(id,version)` has a
composite FK onto `plugin_catalog`, so a minimal schema would need to
reproduce that anyway. Every one of the 25 functions was checked for a live
production caller before writing its test (grepped by both author and
reviewer) — none are dead code.

## Why no production bug this time

Unlike batches 5–8, this file's logic held up under test as written for the
25 functions actually in scope. That is a real finding, not a shortcut —
confirmed by the independent review's own mutation probes (below) rather
than taken on the author's word.

## Mutation testing — author + independently re-run by reviewer

Author, 5 probes, all caught:
1. `InsertPluginPermissions`'s `ON CONFLICT DO NOTHING` → `DO UPDATE SET
   granted = 0`: caught (re-declaring an already-granted permission on a
   manifest re-apply must not reset the operator's grant).
2. `StorageSet`'s `n >= StorageMaxKeys` → `n > StorageMaxKeys` (off-by-one
   at the 1024-key cap): caught.
3. `DeletePlugin` turned into a no-op: caught (cascade-delete assertion).
4. `SyncPluginPaymentMethods`'s `plugin_id IS NOT NULL` guard dropped from
   the deactivate `UPDATE`: caught (built-in `cash` method got deactivated).
5. `EnsureCatalogEntry`'s `ON CONFLICT DO NOTHING` → `DO UPDATE`: caught
   (existing catalog row got overwritten).

Reviewer independently re-ran probes 1 and 2 itself (confirmed the same
failing output) plus 12 of its own across the rest of the batch — all
caught — before looking for a genuine miss. It found four:

- `InsertPluginHooks`'s hardcoded `active := 1` is never exercised at
  `IsActive=false` — currently benign (the only production caller,
  `internal/plugins/manifest.go`, always passes `true`), logged as a test
  gap, not a bug.
- `ListPageEntries`/`ListReceiptTemplates`'s `pe.is_active = 1` filter is
  currently dormant — nothing in the codebase writes `is_active=0` to
  `plugin_entries` today (`ReplacePluginEntries` always inserts `1`).
  Correct defensive filter, just not yet reachable by any write path.
- `UpsertPluginManifest`'s forced `is_active = 1` / `trust_level =
  excluded.trust_level` on every `ON CONFLICT` re-apply isn't pinned by a
  test. Reviewer verified this is not currently exploitable in the way it
  first looks — `TrustLevel` in `ManifestRow` comes from the caller's
  `InstallOptions.TrustLevel` (defaults `"untrusted"`), never from the
  plugin's own manifest content, so a malicious plugin can't elevate its
  own trust via a crafted re-apply. Whether re-install should force
  `is_active=1` even over an operator's explicit disable is a caller-layer
  product decision (`internal/plugins/manifest.go`/`install.go`), not a
  `internal/data` repo-layer bug — out of scope here.
- `ListPermissions`'s `ORDER BY permission` mutation survived — the test's
  ordering assertion happened to pass off SQLite's incidental scan order,
  not a real check of the `ORDER BY` clause. Genuine test gap, logged below
  as a small follow-up (not a production bug, not blocking).

## Real bug found, pre-existing, correctly scoped OUT of this batch

Reviewer traced a genuine bug while probing `SyncPluginPaymentMethods`:
`payment_methods.id` is taken verbatim from `plugin_entries.key`
(`plugin_repo.go:1063`, `SELECT pe.key, ... FROM plugin_entries pe ...`),
and nothing validates or namespaces entry keys against the built-in
`cash`/`card`/`gift` ids seeded by `001_init.sql`. A plugin declaring
`{type:"payment", key:"cash"}` silently takes over the built-in cash
tender's row (name/type overwritten), and once that plugin is later
disabled, the same function's deactivate step turns the built-in `cash`
row `is_active=0` — `ListTenders` filters on `is_active=1`, so cash
disappears from checkout. That brushes against the offline-first
non-negotiable (a live tender silently vanishing). Reviewer proved it with
a throwaway probe (deleted after), pasted in its report.

This is real and worth fixing, but the fix belongs at the manifest
validation layer (`internal/plugins/manifest.go`, which currently checks
entry `type` but not `key` collisions) or a namespacing scheme
(`<plugin_id>:<key>` for plugin-backed payment method ids) — not a
one-line change inside `plugin_repo.go`, and touches the install/upgrade
path this batch didn't otherwise touch. Logged as its own `ut-docs/QUEUE.md`
item rather than folded into a coverage-batch diff.

## Hermeticity — proven, not claimed

Both author and reviewer ran the suite with `HTTP_PROXY`/`HTTPS_PROXY`
poisoned to a dead port (clean) and under `-race` (clean, ~16-17s).
`openMigratedDB` uses `t.TempDir()` — no cwd-relative paths, no missing
`MkdirAll` gap (matches the two recurring bug classes this pipeline
watches for; neither applies here since this is pure DB code with no
file I/O of its own). No `t.Parallel()`, no shared global state between
tests, no wall-clock-dependent assertions.

## Test data

All generic (`com.t.*` plugin ids, "Test Plugin", "Card Reader", "Loyalty")
— no real client or shop name, confirmed by the reviewer as a fresh set of
eyes.

## Gate

`go build ./...` ✓ · `go vet ./internal/data/...` ✓ ·
`go test ./internal/data/... -run TestPluginRepo_ -v` 28/28 ✓ · `-race` ✓ ·
full `go test ./internal/data/...` ✓ · `scripts/ci/guard-data-access.sh` ✓ ·
`scripts/ci/guard-i18n.sh` ✓ (untouched, no new user-facing strings — this
is repository-layer Go, no templates). PR CI (build/contract/e2e/playwright)
all green.

One pre-existing, unrelated failure noted and confirmed NOT caused by this
diff: `internal/issuereport`'s `TestSaveCleansUpDirectoryOnWriteFailure`
relies on a read-only-directory permission check that this sandbox's root
user bypasses (chmod 0o500 is a no-op for root); reproduces identically on
`main` with none of this PR's files present (verified via `git stash`
showing no local changes to stash — the new files are untracked and
irrelevant to that package).

## Follow-ups logged to `ut-docs/QUEUE.md`

1. Plugin payment-method entry keys can collide with/hijack built-in
   tender ids (`cash`/`card`/`gift`), and disabling the colliding plugin
   then deactivates the built-in tender — needs a BA/Architect pass at the
   manifest-validation or namespacing layer (`internal/plugins/manifest.go`).
2. `ListPermissions`'s `ORDER BY permission` isn't actually pinned by its
   test (passes off incidental scan order) — small test-only fix for a
   future micro-batch.
