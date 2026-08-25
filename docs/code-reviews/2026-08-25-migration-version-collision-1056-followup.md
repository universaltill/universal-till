# Code review: ut-docs#1056 migration-collision guard — root-cause correction

- **Card:** universaltill/ut-docs#1056 (already closed/done via
  universal-till#537 — this is a follow-up correction to that merged fix,
  not a re-opening)
- **Repo:** `universal-till` (+ a matching correction in `ut-docs/reference/coding-standards.md`)
- **Reviewer:** independent Opus subagent, fresh context, no shared context
  with whoever authored the original #537 fix

## What this fixes

A separate independent review (run concurrently with, and unaware of,
PR#537's own review) found that #537's shipped fix — while functionally
correct (the zero-padding normalization, scratch-dir fixture testing, and
vacuous-pass guards are all sound and unchanged here) — carries a **wrong
root-cause explanation** in four places: `internal/db/migration.go`'s
`checkNoDuplicateVersions` doc comment, `scripts/ci/guard-migration-version-collision.sh`'s
header comment, `internal/db/migration_test.go`'s
`TestCheckNoDuplicateVersions_DetectsCollision` comment, and
`ut-docs/reference/coding-standards.md`'s migration-uniqueness bullet.

**The claimed mechanism** (still live on `main` before this fix): "`sort.Slice`
has no defined tie-break for equal keys, so a collision could silently let
one file win the sort and the other lose."

**The actual mechanism**, verified by reading `internal/db/db.go`'s
`migrate()`: it reads the highest applied version ONCE via
`SELECT MAX(version)` before its loop, then skips any migration whose
`Version <= current` — never re-checking per file, and not touching sort
order at all. Two concrete failure modes:

1. **Fresh install** (`current=0`): both colliding files pass the
   "newer than current" check and try to apply. The first succeeds and
   `INSERT`s its version into `schema_migrations` (version is its PRIMARY
   KEY); the second's own `INSERT` then hits that constraint and fails —
   a loud boot error, not silent, but still the wrong outcome (one
   migration's DDL ran, the other's didn't, on the same version number).
2. **Upgrade from an install that already has ONE of the two files**
   (`current` already `== 67`, say): a later release adds the SECOND
   067-numbered file. Its `Version <= current` is true too, so it is
   skipped ENTIRELY, forever — the watermark already passed 67 and never
   looks backward. **This is the real silent-data-loss case**: a
   table/column simply never gets created on upgrade, no error at all.

Sort order is irrelevant to both. A binding standards doc (`coding-standards.md`)
and the code's own comments sending the next reader to chase `sort.Slice`
would waste their time and could lead to the wrong fix if this guard ever
needs revisiting.

## What did NOT need fixing (verified correct, unchanged)

- The zero-padding normalization fix (`10#` base-10 cast) in the guard
  script — confirmed still correctly catches `067_x.sql` vs `67_y.sql`.
- The scratch-directory fixture testing (`MIGRATIONS_DIR` override) —
  confirmed the regression test no longer touches the real, `go:embed`'d
  `internal/db/migrations/`.
- The vacuous-pass guards (empty dir, missing dir, unparseable filename)
  — all still fail loudly as intended.
- `checkNoDuplicateVersions`'s placement before the sort in
  `loadMigrationsFromFS` — map-based and order-independent, so placement
  relative to the sort makes no difference either way.

## Change in this commit

- `internal/db/migration.go`: rewrote `checkNoDuplicateVersions`'s doc
  comment with the correct watermark-skip mechanism and both failure
  modes; strengthened `TestCheckNoDuplicateVersions_DetectsCollision` to
  assert the error message names both colliding filenames and the version
  (previously only checked `err != nil`).
- `scripts/ci/guard-migration-version-collision.sh`: rewrote the header
  comment to match.
- `ut-docs/reference/coding-standards.md`: rewrote the migration-uniqueness
  bullet to match (separate PR in that repo, same session).

No behavior change — every guard/test case that passed before this commit
still passes identically; verified with a full `go test ./...` and both
`guard-migration-version-collision.sh` / `_test.sh` runs.
