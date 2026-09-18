# Code review — v0.19.x boot failure on every existing till: restore `001_init.sql`, append 033, accept + re-stamp the shipped variant, pin every migration in CI (ut-docs#2395)

- **Date**: 2026-09-18
- **Card**: ut-docs#2395 (p1, `complexity:hard`, `lane:local`)
- **Branch / PR**: `fix/2395-baseline-checksum-hotfix`
- **Author model**: Fable (Dev subagent); **Reviewer model**: Opus, fresh context, isolated worktree
- **ADR**: ADR-0100 (ut-docs PR #2396) — supersedes ADR-0074 Decision 1

## What was wrong

`e148a7db` (ut-docs#2312, in v0.19.0/v0.19.1/v0.19.2) edited the already-applied baseline `internal/db/migrations/001_init.sql` — added the `catalog_management` permission action and three grants to its seed INSERTs. `universal-till/CLAUDE.md` and ADR-0074 Decision 1 said it could ("may still be edited freely before the first paying shop"). ADR-0074 Decision 3's ledger guard (`verifyAppliedMigrations`, ut-docs#1425) then refused boot on every till whose ledger recorded the pre-edit checksum — i.e. every install that existed before 2026-09-17. First seen on the product owner's TECLAST tablet upgrading v0.16.0 → v0.19.2: white page, `boot failed (migration) … migration 1: recorded as "001_init.sql" (checksum ee6f0a91…) but on-disk file is "001_init.sql" (checksum 13898ca6…)`, ref `6896-6D9D`. Reproduced locally by booting the v0.19.2 binary over a data dir created by the v0.18.0 binary (`/healthz` 503, recovery page).

## What shipped

1. `001_init.sql` restored to v0.18.0's content, statement-for-statement (`migrationChecksum` = `ee6f0a91…`, verified against `git show v0.18.0:…` by Dev, Tester and Reviewer independently). Its header comment now states the file is frozen (checksum-neutral, proven).
2. `033_catalog_management_permission.sql` — `INSERT OR IGNORE` for the action + admin/manager/super_admin grants. Both tables have PRIMARY KEYs on exactly those columns, so it backfills ≤v0.18.0 tills and is a no-op on v0.19.x fresh installs. A manual revoke (`granted=0`) on a v0.19.x till survives — the row exists, so nothing is re-inserted.
3. `db.go`: `acceptedPriorChecksums{1: {13898ca6…: reason}}`. When a ledger row carries that checksum under the same filename, boot warns once, re-stamps the row to the current checksum (single autocommit `UPDATE`, no SQL re-run) and continues. Any other foreign checksum, or a renamed file, fails exactly as before. Crash between re-stamp and 033: 033 (version 33 > watermark 31/32) still applies on the next boot; crash before re-stamp: re-stamps again. Both converge.
4. `shipped_migrations_test.go`: 33 literal `version → migrationChecksum` pins; `TestShippedMigrationsUnchanged` fails on a statement edit, a deleted/renumbered file, or an unpinned new file, each with an actionable message; comment-only edits pass. `TestShippedMigrationsGuard_Branches` proves every branch on synthetic input.
5. Docs: `CLAUDE.md` migration rule, `docs/data-model.md`, stale comments in `010_fiscal_device_receipts.sql` and `demo_seed_test.go`.

## Independent review — findings

No blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `001_init.sql` header still said "may be edited freely until the first paying shop" — the sentence that caused the incident, byte-restored from v0.18.0 | **Fixed** (comment-only; `TestShippedMigrationsUnchanged` + `TestOpenUpgradesV018TillViaMigration033` re-run green) |
| 2 | nit | `catalogManagementMigrationVersion` comment said "if the file is ever renumbered" — files are never renumbered under ADR-0100 | **Fixed** |
| 3 | nit | `010_fiscal_device_receipts.sql` comment cited the withdrawn allowance as current | **Fixed** (past tense, points at ADR-0100) |
| 4 | nit | population-B test comment said "v0.19.2 shape"; real v0.19.0 installs have watermark 31 not 32 | **Fixed** (comment) |

Reviewer also checked and cleared: branch ordering in `verifyAppliedMigrations` (accept path gated on `name == m.Name`, before the empty `idempotentRerunVersions`, no effect on the orphan-row second loop); no tautology in the pin test (all 33 literals recomputed and matched); no map-iteration-order dependence; `logging.Recent()` ring safe (no `t.Parallel()` in the package, every test resets it); 002–031 unchanged since their tags (`git diff v0.18.0 HEAD -- migrations/` shows only 032/033); CI runs `internal/db` (`ci.yml`); no file writes / cwd paths / client names / secret-shaped literals.

Dev's own flag, accepted as-is: the one-time re-stamp warning goes through `logging.Recent()` and so appears in the back-office "recent problems" panel on that single boot. For a hotfix of this class, visible is better than silent.

## Verified beyond the unit tests (Tester, this session)

- Built `v0.18.0` and `v0.19.2` from their tags; booted each to create a **real** data dir (ledger `ee6f0a91`/31 rows and `13898ca6`/32 rows with catalog rows present).
- Booted the fixed build over a copy of each: `/healthz` **200** both; ledger version 1 = `ee6f0a91` both; 33 rows; `catalog_management` action ×1, grants admin/manager/super_admin ×1 each; the v0.19.2 copy logged exactly one re-stamp WARN; second boot silent.
- Fresh install on the fixed build vs fresh v0.19.2 vs both upgraded dirs: `sqlite_master` schema **identical** (0 diff lines) and the full `roles`/`permission_actions`/`role_permissions` seed **identical** (81 rows).
- TDD by revert: with `db.go`'s accept path removed, `TestOpenAcceptsV019BaselineChecksumAndRestamps` fails with the device's exact error; a statement appended to 001 fails `TestShippedMigrationsUnchanged` with the ADR-0100 message; an unpinned `034_probe.sql` fails "not pinned"; a comment-only edit passes. (Reviewer repeated all of these independently in its worktree.)
- Full gate: `go build`, `go vet`, `go test ./... -count=1` (60 packages ok, ports 8091–8093 free beforehand), `golangci-lint` 0 issues, `guard-data-access.sh`, `guard-migration-version-collision.sh`.
- Not run: Playwright e2e — no UI surface is touched; migrations/runner only.
- Device verification on the released build (tablet 192.168.1.136) happens in DevOps after v0.19.3 is cut and is recorded on the card.

## Verdict

**Safe to merge.** Patch release required (v0.19.3): v0.19.0–v0.19.2 must never be installed over an existing data directory.

## Deferred / related

- ut-docs#1437 — the Android shell shows a white page instead of the server's recovery page (`waitUntilReady` wants 200; recovery `/healthz` answers 503 by design). Evidence posted there; next local card.
- `acceptedPriorChecksums[1]` is permanent (no way to prove no v0.19.0–v0.19.2 fresh install still exists).
