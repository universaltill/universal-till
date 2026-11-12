# Review: landing the sales-copy stack on main (ut-docs#5074)

**Date:** 2026-11-03 · **Lane:** `lane:cloud-41` · **Branch:** `feat/5074-land-sales-mirror`

## What shipped

The seven stacked PRs (#2169 M1 → #2171 M2 → #2172 M3 → #2174 M7 → #2175 M4 →
#2176 M5 → #2179 M6) land in one integration branch, cut from the M6 tip.
Each layer keeps its own review record (`2026-10-26-sales-*-5074.md`). The
feature still ships **dark** (`sales_mirror.enabled` off by default). This
record covers only the landing delta:

- **Merge `adae6945e`** brings `origin/main` (206 commits) into the stack as a
  merge commit, so the original commits keep their history and authorship.
  The conflicted tests (join snapshot, shipped migrations and others) keep
  both sides' tests, once each.
- **Migrations renumbered** (`ca2a6777c`): main shipped
  `093_tills_bearer_rotation`, so the never-shipped `093_sales_mirror` →
  `094`, and `094_peer_closes` → `095`. The pins, tests, card-data allowlist,
  comments and the earlier review records all follow.
- **Promote test fix** (`ca2a6777c`, test only): M6's CI failed with
  `B's next Z gross = 900, want 0`. The test stamped the main till's sales
  "now", after a close it backdates by a minute, so B's Z correctly counted
  them. The sales now sit before the close, and the Z-from floor check
  compares instants instead of a UTC string against a local RFC3339 string.
- **Help-drift baseline**: the `multitill` entries are updated for the merged
  topic (main's restored-device section plus the branch's sales-copy
  section, added to English and every locale together).
- **Manual screenshots** (`22edebbc7`): `make docs-shots` was run on the
  merged tree. Every PNG is byte-identical; only `manifest.json` hashes
  moved.

## Review

Independent reviewer: a different model from the Dev author. It looked at the
merge's conflict resolutions (`git show --remerge-diff`) and at the fix
commit.

| Severity | Finding | Outcome |
|---|---|---|
| Major | `web/help/img/manifest.json` was stale after the merge: surface and `multitill` topic hashes from one side only. Main's push run would have gone red. | Fixed in `22edebbc7` (full `make docs-shots`). |
| Minor | The renamed migration tests still started from 93 and 94 (`openAtPreMigrationSchema` migrates to `v-1`), so each test ran two migrations, and their messages said "093". | Fixed: they now start from 94 and 95, and the wording is updated. |

Money check (Z window), done strictly:
- The window is `[max(own last close, floor), now)`, compared in UTC via
  `datetime()`. It is half-open and matches the archived close's
  `created_at`, so a sale is never lost or counted twice.
- Production code has no timezone string compare, so this is no product
  bug.
- **TDD re-check:** the old test only passed when it finished inside one
  wall-clock second. With a 1.1 s sleep before `generateEOD`, the reverted
  test fails with exactly the CI error, and the fixed test passes.

Clashes with main, all checked with nothing found:
- The join snapshot nulls `bearer_prev_hash` and resets the sales-copy
  bookkeeping.
- The sales-copy handlers use main's `syncTill`, so bearer rotation and the
  previous-bearer retry cover them.
- Main added no new table and no new write path to `sales` or
  `report_archive`.
- The admin bundle already redacts the new `tills` columns.

## Verified

- `gofmt -l .`: clean.
- `go build ./...`: ok.
- `golangci-lint run ./...`: 0 issues.
- Full `go test ./...`: ok, including `internal/pages` in 349 s. The
  reviewer re-ran db, pos, data and pages separately: ok.
- Guards: sales-mirror-isolation, data-access, card-data-schema,
  help-drift, i18n, i18n-meta, help-topics, kiosk-engine, core-neutral and
  docs-shots all pass.
- **Not runnable locally (CI judges these):** `guard-deadcode-baseline`
  (its pinned `deadcode` built with go1.26 here) and
  `guard-shellcheck-version` (no shellcheck in this container).

## Verdict

Safe to merge dark. Deferred:
- Language packs: the M5 keys go to the de/es/pt packs right after merge.
  Pack `main`'s `key-drift` is already red on #4128's keys, so those PRs
  join that queue.
- The owner's multi-till test-shop check is still needed before the setting
  is turned on anywhere.
