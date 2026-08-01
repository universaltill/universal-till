# Code review — demo barcode EAN-13 fix corrected to a migration (was a 001 edit)

- **Date:** 2026-08-01
- **Task:** ut-docs#17 follow-up — corrects universal-till#122
- **Branch:** `fix/demo-barcode-ean13-checksums` (force-pushed after #122
  merged as a squash commit; the branch's prior commits are fully
  superseded, this is a fresh start from `origin/main`)
- **Author:** pipeline Dev step (Sonnet 5)
- **Independent reviewer:** general-purpose subagent on **Opus** (different model, per standing practice)

## What was wrong with #122

#122 (see `docs/code-reviews/2026-07-31-demo-barcode-ean13-checksums.md`)
edited `internal/db/migrations/001_init.sql`'s seed rows directly to fix
50 `item_barcodes` + 12 `variant_barcodes` invalid EAN-13 check digits.
That review, and the BA/Dev steps before it, all checked `git tag` on a
**shallow clone that hadn't fetched tags** and got an empty result,
concluding the repo was pre-release — so `001_init.sql` looked editable
per this repo's own rule ("`001_init.sql` may still be edited
pre-release"). In fact the repo has **57 release tags** (latest
`v0.2.53`, tagged *before* #122 merged) — the rule's carve-out didn't
apply, and `001` should have stayed append-only.

**Real consequence:** editing `001` in place only helps a *brand-new*
install (which runs `001` for the first time, seeding the corrected
values directly). Any till that had already run `001` before this change
shipped never re-seeds — `001` doesn't re-run — so the "fix" silently did
nothing for every upgrading till. Ironically, migration `022`'s own
header (already in-tree) states "001 is released and append-only" — the
rule was documented right next to where it was violated.

## What this correction does

1. **Reverted `001_init.sql` to its exact released state** — confirmed by
   the reviewer via blob-hash comparison: identical to what shipped in
   `v0.2.6` *and* `v0.2.53`, not just "a small diff."
2. **New migration `023_fix_demo_barcode_checksums.sql`** — the same 62
   corrections (50 `item_barcodes` + 12 `variant_barcodes`), applied as
   `UPDATE OR IGNORE ... SET barcode = <corrected> WHERE barcode =
   <original broken value>`. `OR IGNORE` per the reviewer's finding below.
3. **New upgrade-path test** `TestSeedBarcodeChecksumsFixedOnUpgrade`
   (mirrors `dead_seed_test.go`'s established pattern) — rewinds
   `schema_migrations`, reopens, confirms migration 023 alone fixes an
   already-seeded barcode. Proven to fail without 023 (mutation-tested).
4. **Fixed a watermark side effect this exposed**: `db.go`'s `migrate()`
   gates re-application on `MAX(version)` in `schema_migrations`, not a
   per-version applied-set — correct for real installs (versions always
   apply in strict order with no gaps) but it made
   `TestDeadTaxInclusiveSeedRemovedOnUpgrade`'s old rewind
   (`DELETE ... WHERE version = 22`) fragile the moment any migration
   numbered above 22 exists: row 23 stayed present, so the watermark
   never dropped and 22 silently failed to reapply. Changed to
   `version >= 22`. Mutation-tested: reverting this change alone (keeping
   023) reproduces the failure, proving the coupling is real.
5. **Fixed `e2e/tests/tender-panel-reachable.spec.ts`** — added by a
   *different*, later PR (universal-till#129 / ut-docs#161) that landed
   on `main` after #122 merged, using the pre-fix barcode literal. Same
   miss class as the 5 specs #122 already fixed; swept up here.

## Independent review — two real findings, both fixed

Different-model (Opus) review, genuinely independent: recomputed all 62
checksums from scratch, byte-diffed `001_init.sql` against release tags,
compared the full post-migration table state row-for-row against #122's
(wrong but internally-consistent) final state to prove the two approaches
converge on identical data, and mutation-tested every claim (removed 023
entirely; reverted the `dead_seed_test.go` fix alone; dropped in a dummy
`024_probe.sql`).

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | should-fix | Plain `UPDATE` in migration 023 hard-fails the whole migration (and therefore `db.Open()`, and therefore **boot**) with a `UNIQUE constraint failed` if a till already owns one of the 62 target barcode values (e.g. a merchant added their own product at that exact code before upgrading) — collides with the offline-first non-negotiable that boot/checkout must never be blocked. Demonstrated, not theorized: reviewer reproduced the exact failure. | **Fixed** — `UPDATE OR IGNORE`; re-verified by reproducing the reviewer's exact collision scenario and confirming boot now succeeds (that one demo barcode stays unfixed on that till, which is an acceptable, non-blocking degradation) |
| 2 | should-fix | `TestSeedBarcodeChecksumsFixedOnUpgrade`'s own rewind (`DELETE ... WHERE version = 23`) reintroduces the *exact* trap item 4 above just fixed in the sibling test — it silently breaks the moment a migration 024 exists. Reviewer proved it by dropping in a dummy `024_probe.sql`: the new test failed, the fixed sibling test didn't. | **Fixed** — `version >= 23`; re-verified with the same dummy-migration mutation test, both tests now pass |

Also confirmed by the reviewer: no other stale barcode literal anywhere
in the repo (fresh grep across everything, including files added by
concurrent merges since #122); migration numbering has no collision (023
was genuinely free, nothing else claims it); full gate green
(`go build`, `go vet`, `go test ./...` — same pre-existing
`TestSaveCleansUpDirectoryOnWriteFailure` root-in-container artifact,
independently re-confirmed in a separate worktree — `guard-data-access.sh`,
`guard-i18n.sh`).

## Verdict

**Safe to merge.** Both should-fix findings were fixed and re-verified
with reproductions of the reviewer's exact scenarios, not just trusted.
The corrected approach is proven, at the data level, to converge on
exactly the same final barcode values #122 shipped — new installs are
unaffected, and upgrading tills now genuinely receive the fix that #122
claimed to give them but didn't.
