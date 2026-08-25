# 2026-08-25 — Independent review: `.bkp` import fixes for real speedy kasse backups (ut-docs#968)

**Branch:** `fix/bkp-meta-crc32-checksum` · **Card:** [universaltill/ut-docs#968](https://github.com/universaltill/ut-docs/issues/968)
(complexity:medium) · **Base:** `c6a2f2b`

This is the **owed independent review** the PR body explicitly flagged as
missing ("Subagent spawning was disabled in the session that produced
this... the pipeline's independent different-model review did NOT run. It
is still owed on this branch."). The original `2026-08-25-bkp-real-file-import-968.md`
is a self-review by the implementing session and is left as-is; this is an
addendum, not a replacement.

**Verdict: safe to merge**, with one real finding fixed below.

## What was independently re-verified from the self-review's claims

1. **The CRC32 vs SHA-256 checksum fix.** Read `validateBkpMeta` and the new
   width-dispatch logic end to end. `TestParseBkp_ChecksumMatchPasses` and
   `TestParseBkp_ChecksumMismatchFailsLoudly` both pass
   (`go test ./internal/catimport/... -count=1`, clean). The self-review's
   own revert/restore evidence for this fix (documented with the real
   `"backup.db checksum recorded in meta.inf does not match the archive
   contents"` error text) is consistent with reading the code — a width
   that isn't 8 or 64 hex chars is skipped rather than failed, which is the
   correct conservative choice and matches the PR's own description.
2. **The `ProductGroups` join fix — found incomplete, fixed here.** The
   original fix introspects `pragma_table_info` for the **display** column
   (`ProductGroupText`/`ProductGroupName`) on both tables before deciding
   whether to join, but the join clause it then emits is unconditionally
   `ON g.ProductGroupID = p.ProductGroupID` — the **join key** was never
   introspected, only assumed present on `ProductGroups`. A real backup
   whose `ProductGroups` table carries the category text under a
   differently-named key column (plausible — the pilot's own schema was
   "guessed from a ticket's prose... never verified against a real file",
   per this PR's own stated root cause for defect 2) hits `no such column:
   g.ProductGroupID` and the entire import dies. That is precisely the
   failure mode this card exists to eliminate — the file's own contract is
   "every column is optional; a backup missing one reads narrower, never
   fails outright" — and the original fix didn't quite reach that contract
   for the join key itself.

   **Fixed**, same worktree, commit `34894e1`: `buildBkpProductsQuery` now
   requires `groupCols["productgroupid"]` before emitting the join, exactly
   the same pattern already used for the display column. New test
   `TestReadBkpProducts_ProductGroupsWithoutJoinKeyStillImports` written
   first, confirmed failing pre-fix with the real error
   (`no such column: g.ProductGroupID`), passing after.

## TDD revert/restore, independently performed

| Reverted | Test | Failure observed |
|---|---|---|
| The `groupCols["productgroupid"]` guard in `buildBkpProductsQuery` (this review's own fix) | `TestReadBkpProducts_ProductGroupsWithoutJoinKeyStillImports` | `an unjoinable ProductGroups must degrade to no category, not fail the import: query Products: SQL logic error: no such column: g.ProductGroupID (1)` |

Restored: test passes again, `go test ./internal/data/... -run
TestReadBkpProducts -v -count=1` all green.

## Recurring bug classes checked

- No file-write handler in this diff — nothing to check for a missing
  `os.MkdirAll`.
- No cwd-relative path introduced; nothing that should be `paths.Data(...)`.

## Cross-cutting checks

- **Repository pattern** — all touched SQL is in `internal/data`; re-ran
  `bash scripts/ci/guard-data-access.sh` clean.
- **Money** — no monetary amount touched by this diff (prices pass through
  as raw decimal strings from the source `.bkp`, parsed downstream by the
  existing importer pipeline, unchanged here).
- **i18n** — no new user-facing string.
- **Test data** — the new test's fixture (`Kaffee`, `Latte Macchiato`,
  `30033`) is synthetic, no real business/device/licence data, consistent
  with the original self-review's own fixture-scrubbing claim.

## Gate

`gofmt -l .` clean (this review's own changed files) ·
`go vet ./internal/catimport/... ./internal/data/...` clean ·
`go test ./internal/catimport/... ./internal/data/... -count=1` all
packages ok · `guard-data-access.sh` clean.

## Deferred / not this review's scope

- `main`'s own `TestUnusualSales` (`internal/alerts`) failure — pre-existing
  on `main`, unrelated to this diff, already carded as ut-docs#969 and
  picked up separately this same cycle.
- `DepositPrice` (Pfand) import — the PR's own body already notes this as
  out of scope, belongs with #47/#249.
