# Code review — cloud-master catalogue pull on the main till (ut-docs#4463)

- **Date:** 2026-10-16
- **Card:** universaltill/ut-docs#4463 (task 3 of epic #4453, ADR-0162 §4),
  rescoped to the foundation slice. The rest is split into #4491, #4492,
  #4493 and #4494.
- **Author:** Opus 5.5 (dev subagent). **Reviewer:** Fable, in two rounds.
  Round 1 found a data-integrity issue, so a second round reviewed the fix
  commit.

## What shipped

- `entitlement.Block` reads `catalog_master`. It fails safe: anything other
  than exactly `cloud` or `adopting` reads as `till`. The value is cached as
  `entitlement.catalog_master`.
- Migration `082_cloud_links.sql` adds two tables. Both are main-till-local
  and never LAN-synced.
  - `cloud_links(entity, cloud_id, local_id, version, gen, updated_at)`.
  - `cloud_apply_failures`: the latest refusal per row, so no refused row is
    dropped silently.
- `internal/catalogpull` runs the pull loop against contract
  `cloud-catalog-feed.md` v1.0.0:
  - It pages while `next < high_water`, and an empty page is not the end.
  - Each transaction holds at most 100 rows, with one savepoint per row.
  - The cursor is written inside the chunk's own transaction, and only at
    a version boundary.
  - A version group is applied in dependency order: tax_code, then category
    (parents first), then item.
  - 409, 401 and 403 stop the pull and leave the cursor unchanged.
  - On 410 the till resyncs. It prunes links the full pull did not return,
    after one confirming fetch.
  - On 503 or any other failure the cursor is kept and the next run retries.
  - Feed validation: UUIDs, a row-count bound, and a byte bound.
  - The applier version forces a single re-pull from 0 when a later card
    adds kinds.
- Apply rules for `tax_code`, `category` and `item`:
  - Field-wise, through `*Tx` variants of the existing repository methods,
    so the validation is the repository's own.
  - A row the cloud creates gets `local_id = cloud_id`, which makes a crash
    between row write and link write idempotent.
  - On a tombstone, an item that was sold, is in stock history or is in a
    live basket is deactivated; otherwise it is deleted. A category is
    soft-deleted. A tax code is deactivated.
  - Other kinds are skipped, and the cursor still advances.
- `cloudsync.tick` runs the pull only on the main till, only when the cached
  mode is `cloud`, and only after a check-in POST that reached the cloud. A
  pull error never fails the check-in. A changed catalogue re-pushes the
  snapshot in the same tick.
- `docs/data-model.md`: new section "Cloud catalogue pull — cloud_links".

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1-1 | major | Rows of one version were applied in feed order, but the server orders `item` before `tax_code` and sorts by `cloud_id`. Same-version cross-references were refused, which breaks the adoption bootstrap. | Fixed: `dependencyOrder` runs before chunking. Tests: item before tax code, child before parent, and a reversed 5-row chain split over chunks. The orchestrator confirmed by mutation that the test fails without the fix. |
| R1-2 | minor | Resync prune race: a row rewritten during the pull moves above `high_water` and was pruned. | Fixed: the till makes one confirming fetch at `since=high_water` and defers the prune otherwise (test). The server-side snapshot read is filed as #4499. |
| R1-3 | minor | Infrastructure errors (BUSY, FULL, a failed hook, a cancelled context) were recorded as refusals, and the cursor skipped the row. | Fixed: `data.IsStorageError` plus `isInfra` abort the chunk. Constraint errors stay refusals. |
| R1-4 | minor | There was no row-count bound per page. | Fixed: a page with more rows than `limit` is refused, unless all its rows share one version. |
| R1-5 | minor | A till with wizard-seeded tax codes or categories refuses cloud rows that have the same names. | Scope note posted on #4494 and #4465. Cloud mode must not reach such a till before they ship. |
| R1-6..8 | nit | `localID` adoption comment; extra `active` update per category; 401/403 retry noise. | Comment added; 401/403 now stop. The category nit is accepted. |
| R2-1 | minor | No pull-level test for the storage-error branch: removing it leaves the suite green. | Accepted gap. `TestIsStorageError` covers the classifier, and the hook and context branches are tested at pull level. Injecting a real driver failure mid-chunk needs a seam that costs more than it buys. |
| R2-2 | minor | `driver.ErrBadConn`, `sql.ErrTxDone` and `sql.ErrConnDone` were not classed as storage errors. | Fixed, with test rows. |
| R2-3 | minor | On a feed that is never quiet, the prune waits forever. That direction is safe. | Documented in the package and in `data-model.md`. |
| R2-n1 | nit | A 410 on the confirming fetch was logged as a warning. | Fixed: the next run handles it. |
| R2-n2 | nit | The auth-warning latch was never reset. | Fixed: it resets once the feed accepts the credential. |
| R2-n3 | nit | A doc line was over-long. | Re-wrapped. |

## Verified beyond the unit tests

- The orchestrator re-verified the dependency-order fix by mutation in a
  separate worktree (red without it, green with it).
- The reviewers re-verified several fixes by mutation and restored each one:
  - cursor stored mid-version;
  - savepoint removed;
  - confirming fetch removed;
  - row bound removed;
  - cycle fallback dropping rows.
- A real `Tick` test shows a cloud-master main till pulling, with the pulled
  item in the same tick's snapshot. Replicas, till/adopting modes and 304
  ticks never pull, and a feed 503 never fails the tick.
- In a real `pos.Service` test, an open basket line keeps its price and the
  next line rung up gets the new one.
- No UI surface was touched, so no screenshots were taken. The docs-shots
  surface hash was refreshed only because a `internal/pages` wire file
  changed.

## Deferred

#4491 (variants, barcodes, modifier groups, option sets), #4492 (layout,
staff, settings), #4493 (report `apply_failed` to the cloud; cloud-side
skip for an undecryptable staff row), #4494 (seed skip in the setup wizard,
plus e2e), #4499 (snapshot read in ut-cloud's feed), and #4464 (outbox
exemption from the field-wise apply).

## Verdict

Safe to merge (round 2: no blockers, no majors).
