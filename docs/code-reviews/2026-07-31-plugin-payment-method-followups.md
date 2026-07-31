# Code review — plugin payment-method follow-ups (orphans, catalog ordering, name collisions)

- **Date:** 2026-07-31
- **Task:** ut-docs#16 (Plugin payment-method follow-ups, from PR #102's review)
- **Branch:** `fix/plugin-payment-method-followups`
- **Author:** pipeline Dev step
- **Independent reviewer:** general-purpose subagent on **Opus** (different model, per standing practice)

## What shipped

Three residuals ADR-0031 (plugin payment-method identity) explicitly deferred:

1. **Startup warning for orphan-owned tenders** — `PluginRepo.FindOrphanedPaymentMethods`
   (`internal/data/plugin_repo.go`) finds `payment_methods` rows whose
   `plugin_id` has no matching `plugins` row; `plugins.Init`/`Reload` log a
   warning per row via the new `warnPaymentMethodAnomalies`. Log-only — a row
   alone can't distinguish a stale capture from a legitimately-uninstalled
   plugin's retained tender, so this surfaces rather than auto-repairs.
2. **Orphan `plugin_catalog` row on a rejected marketplace install** —
   `internal/plugins/installer_marketplace.go`'s `Install` now runs
   `PersistManifest` (which validates and rolls back on rejection) **before**
   `upsertCatalogEntry`, instead of after. `PersistManifest`'s own
   `EnsureCatalogEntry` step already covers the `plugins(id,version)` FK, so
   the happy path is unchanged.
3. **`payment_methods.name` UNIQUE collisions** — extends ADR-0031's existing
   "loud at install time, harmless at sync time" pattern (already used for id
   collisions) to names: `PluginRepo.FindPaymentNameConflicts` (mirrors
   `FindPaymentKeyConflicts`'s two-step check — `payment_methods.name` AND
   unsynced `plugin_entries.label`) feeds `validatePaymentEntryKeys`, which
   now rejects an install/rollback whose payment label collides with another
   plugin's or a built-in tender's, with a clear error. At sync time,
   `SyncPluginPaymentMethods`'s upsert gets a second `ON CONFLICT(name) DO
   NOTHING` for the INSERT path, and its `DO UPDATE`'s `name` write is
   guarded by a `CASE` so a rename never itself attempts a colliding write.

Also fixed: `internal/pages/ui_smoke_test.go`'s hand-rolled `seedForPages`
fixture was missing the `NOT NULL UNIQUE` on `payment_methods.name` that
`001_init.sql` has had since day one — silent fixture drift that would have
hidden any real name-collision behavior from that whole test suite forever.

## TDD evidence (independently re-verified, not just claimed)

Every fix was written test-first and confirmed failing against the pre-fix
code with the real error message (undefined method, raw DB error, or
`nil` where an error was expected), then confirmed passing after the fix.
The reviewer independently mutation-tested three of them: reverted the
`FindPaymentNameConflicts` call → both name-conflict tests failed; reverted
the installer reorder → the catalog-orphan test failed with the count=1
message; deleted the `ON CONFLICT(name) DO NOTHING` clause → the legacy
name-collision test failed with the real `UNIQUE constraint failed:
payment_methods.name (2067)`.

## Verified beyond automated tests

- Full `go build ./...`, `go vet ./...`, `go test ./...` (every package, not
  just the touched ones) — clean except one pre-existing, unrelated failure
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, confirmed
  failing identically on unmodified `main` — a read-only-directory
  permission quirk of this sandbox running as root, not a regression here).
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`
  — both green; no raw SQL leaked outside `internal/data`, no new
  user-facing strings (the install-rejection errors and the startup warning
  are raw Go errors / server logs, consistent with the pre-existing
  key-conflict precedent — confirmed the install API surfaces a message
  *key*, not raw error text, via `writeInstallResponse`).
- No UI/template surface touched — no browser-driven check needed for this
  batch (backend-only: startup logging, install-time validation, an upsert's
  conflict handling).

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | **blocking** | The first pass's `ON CONFLICT(name) DO NOTHING` only guarded the INSERT path — the `DO UPDATE SET name = excluded.name` branch could still raise `UNIQUE constraint failed: payment_methods.name` (e.g. a plugin swapping labels between its own two entries, accepted at install time since neither collides with another plugin, then hard-aborting every sync/boot from then on) — the exact bug class ut-docs#16 exists to close | **Fixed** — the `DO UPDATE`'s `name` write is now guarded by a `CASE` that keeps the current name if another row already holds the target name; added `TestSyncPluginPaymentMethods_OwnLabelSwapDoesNotAbortSync` (fails pre-fix, passes post-fix, re-run twice to prove it doesn't just defer the crash one cycle) |
| 2 | should-fix | The name-collision guards (both the INSERT `DO NOTHING` and the new `DO UPDATE` `CASE`) are silent — an entry that can't materialize or rename vanishes with no error and no log line, a regression from the pre-fix hard-error (at least loud) behavior | **Fixed** — `PluginRepo.FindSuppressedPaymentNameEntries` + `warnPaymentMethodAnomalies` (also wired into `Reload`, not just `Init`, since lifecycle changes go through `Reload`) surface exactly these as startup/reload warnings |
| 3 | should-fix | Install-time validation doesn't catch duplicate labels **within one manifest**, or an empty `Label` (unlike `Key`, which gets empty/whitespace/`:` checks) | **Deferred → new Backlog card**: out of this card's scope (a manifest-shape validation gap, not one of the three original sub-items); needs its own small fix + tests |
| 4 | should-fix | Neither the pre-existing key-conflict error nor the new name-conflict error reaches `ClassifyInstallError` with a specific case — both fall to the generic `plugins.install.error.retryable` ("you can retry"), which is actively wrong for a permanent collision | **Deferred → new Backlog card**: pre-existing gap for keys, inherited (not introduced) for names; real UX issue worth its own pass across both |
| 5 | nit | `FindPaymentNameConflicts`'s error has only 2 branches vs `FindPaymentKeyConflicts`'s 3 — drops the "owner no longer installed" case, so a label held by an uninstalled plugin's retained tender misreports as owned by a plugin the operator can't see/remove | **Deferred → same Backlog card as #4**: small, but bundling with the error-classification pass makes more sense than three small PRs |
| 6 | nit | The orphan warning's remediation text ("reassign/rename the tender") points at a UI surface that doesn't exist (no tender-rename affordance found anywhere in `web/ui/`); also fires every boot forever for a legitimately-uninstalled plugin with no acknowledgement mechanism | **Deferred → same Backlog card**: a UX/wording follow-up, not a correctness bug |

Also checked and confirmed clean: repository pattern (all new SQL only in
`internal/data`, guard passes mechanically); the installer reorder is safe
(nothing reads the catalog row between the old and new call sites;
`manifest_verifier.go` verifies against the on-disk manifest signature, not
the catalog column, so no security-verification impact); everyday re-sync
(plugin re-enable/disable) unaffected — SQLite routes a same id+name
conflict to the `ON CONFLICT(id)` clause, ownership guard intact under
combined-constraint conflicts; no money-type surface (no monetary fields
touched); no file I/O at all in this diff (both recurring bug classes —
missing `MkdirAll`, cwd-relative paths — non-applicable); no real
client/shop names or secret-shaped literals in any test data.

## Verdict

**Safe to merge.** Both blocking/should-fix findings from the independent
review (#1, #2) are fixed and re-verified in-branch; #3–#6 are carded as
follow-ups rather than scope-creeping this commit further.
