# Code review: shortcut_buttons seed barcodes carried invalid EAN-13 checksums

**Ticket:** universaltill/ut-docs#191
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/191-shortcut-button-barcode-checksums`
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy tier), isolated worktree

## What shipped

`internal/db/migrations/001_init.sql`'s `shortcut_buttons` seed rows (10
rows, `barcode` column, prefix `2000010000NNN`) carried the same class of
fabricated 13th digit (a naive incrementing counter) that migration
`023_fix_demo_barcode_checksums.sql` already corrected for
`item_barcodes`/`variant_barcodes` (ut-docs#17) — a real EAN-13 check
digit is a mod-10 weighted checksum over the first 12 digits, not an
increment of the previous row's last digit.

`001_init.sql` is released and append-only (same reasoning `023` already
established), so the fix is a new migration rather than an edit to the
seed:

- `internal/db/migrations/031_fix_shortcut_button_checksums.sql` — ten
  `UPDATE OR IGNORE shortcut_buttons SET barcode = '<corrected>' WHERE
  barcode = '<original>'` statements, one per row, following `023`'s
  exact idiom. Pure DML, no DDL. Only the 13th digit changes; the
  first-12-digit prefix, `item_id`, `label`, and `image_path` are
  untouched.

### Tests (written test-first, TDD)

- `TestSeedShortcutButtonsValidEAN13` (mirrors
  `TestSeedItemBarcodesValidEAN13`) — opens a fresh DB, reads all 10
  seeded `shortcut_buttons.barcode` values, asserts every one passes the
  standard EAN-13 mod-10 weighted checksum.
- `TestSeedShortcutButtonChecksumFixedOnUpgrade` (mirrors
  `TestSeedBarcodeChecksumsFixedOnUpgrade`) — the upgrade path: a till
  that installed before migration 031 existed already has the broken
  checksum from 001's seed. Simulated by restoring one row to its
  original broken barcode, rewinding `schema_migrations` past version 31,
  and reopening — 031 alone must correct it. Unlike 023's followers, 031
  is pure `UPDATE` with no DDL, so (unlike the existing upgrade test) no
  column/table rewind was needed for migrations in between.

Both confirmed failing against the pre-fix state (file temporarily moved
out of the migrations directory) with the real, on-topic errors — quoted
below — then passing again after restoring the fix.

## Independent review (round 1)

An independent, fresh-context Sonnet subagent, isolated in its own git
worktree, reviewed the diff without having seen any prior reasoning about
it:

- **Independently recomputed the EAN-13 check digit for all 10 original
  barcodes itself** (not trusting the diff) and confirmed every corrected
  value in the migration matches, with every 12-digit prefix unchanged:

  | prefix | old (broken) check | correct check |
  |---|---|---|
  | 200001000001 | 7 | 2 |
  | 200001000002 | 4 | 9 |
  | 200001000003 | 1 | 6 |
  | 200001000004 | 8 | 3 |
  | 200001000005 | 5 | 0 |
  | 200001000006 | 2 | 7 |
  | 200001000007 | 9 | 4 |
  | 200001000008 | 6 | 1 |
  | 200001000009 | 3 | 8 |
  | 200001000010 | 9 | 4 |

- Confirmed migration numbering (031) is the next unused version, no
  collision, and that it's pure DML consistent with 023's own precedent.
- Ran `go test ./internal/db/... -run 'Barcode|ShortcutButton' -v` — all
  5 tests (2 new, 3 pre-existing) pass.
- **Independently re-verified the TDD claim**: moved
  `031_fix_shortcut_button_checksums.sql` out of the migrations
  directory, reran the two new tests, got:
  ```
  barcode_seed_test.go:132: seeded shortcut_buttons with invalid EAN-13 check digit: [2000010000017 2000010000024 2000010000031 2000010000048 2000010000055 2000010000062 2000010000079 2000010000086 2000010000093 2000010000109]
  --- FAIL: TestSeedShortcutButtonsValidEAN13 (0.09s)
  barcode_seed_test.go:236: itm001 shortcut barcode = "2000010000017" after 031 upgrade, want "2000010000012"
  --- FAIL: TestSeedShortcutButtonChecksumFixedOnUpgrade (0.10s)
  ```
  then restored the file and confirmed both pass again.
- Confirmed diff scope exactly two files
  (`internal/db/barcode_seed_test.go` modified,
  `internal/db/migrations/031_fix_shortcut_button_checksums.sql` added) —
  nothing else touched.
- Confirmed the 10 rows correspond exactly to the existing generic demo
  items (`itm001`–`itm010`: Coca-Cola, Pepsi, waters, juices, milk,
  butter, cheese) — no real client/shop name, no secrets, no new
  fabricated data.
- Confirmed N/A for the two recurring bug classes (missing
  `os.MkdirAll`, cwd-relative path instead of `paths.Data`) by grepping
  the diff for file-writing code — none exists; this is a SQL migration
  and a Go test file only.
- Confirmed no `web/help/` topic references shortcut-button barcodes or
  the literal demo digits — pure data-correctness fix, no user-visible
  behavior change (scan/tap flow identical), so no manual update needed.
- Confirmed money/i18n/offline-first/plugin-signing sections of
  `universal-till/CLAUDE.md` genuinely don't apply — no money types, no
  user-facing template strings, no runtime checkout path, no plugin code
  touched.

### Findings

None. No blocker-class issue, no non-blocking findings either — this is
the rare clean pass.

## Verification performed (this session, after the fix)

- `go build ./...` — clean.
- `go test ./internal/db/... -run 'Barcode|ShortcutButton' -v` — all 5
  pass.
- Personal TDD revert-restore (before delegating to the independent
  reviewer, who repeated it independently and got the same result): moved
  the migration file aside, confirmed both new tests fail with the
  expected messages, restored, confirmed green again.
- `go test ./...` (full repo) — every package passes except the
  pre-existing, unrelated `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`), which fails under this sandbox's root-run
  environment (`chmod 0500` doesn't block root writes) — different
  package, no shared code with `internal/db`, confirmed not a regression
  introduced by this diff.
- `bash scripts/ci/guard-data-access.sh` — `✓ data-access guard: no
  inline SQL outside internal/data / internal/db`.
- `gofmt -l internal/db/` — clean. `go vet ./internal/db/...` — clean.

## Scope

`internal/db` only — a demo-seed data-correctness fix plus its
regression tests. No SQL outside the data/migrations layer, no money
math, no user-facing template string, no HTTP handler, no UI screen, no
plugin-loading path touched. No manual-doc update needed.

## Outcome

Independent review found no blocking issues and no findings at all.

Safe to merge.
