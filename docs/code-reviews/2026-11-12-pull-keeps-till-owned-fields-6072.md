# Code review — the cloud pull keeps till-owned item and category fields (ut-docs#6072)

- Card: universaltill/ut-docs#6072.
- Branch: `fix/6072-pull-keeps-till-owned-fields` (reviewed as WIP snapshot `ccf19a1b4` on `origin/main` `34e9cc9`).
- Process: BA, Architect and Dev (Sonnet), then Tester. The independent review was run by Opus 5.5, a different model from the author, in a detached worktree.
- Pre-flight: till user-visible behaviour — `web/help/` n/a (no topic describes the pull resetting these fields; the only visible effect is that till edits stop reverting), `docs/data-model.md` updated, `README.md` n/a; ut-docs `reference/contracts/cloud-catalog-feed.md` note + ADR-0162 §2 amendment deferred → follow-up card (ut-docs runners down, #6033); no `en.json` key, no manifest/host-function/plan change; review record: this file.

## What shipped

On a cloud-master store, the catalogue pull used to reset fields that the cloud never writes to the feed's schema defaults. This happened on every applied row: feed pulls, the till's own upload answers (accepted and conflict), and full re-pulls (applier bump, 410 resync).

The affected item fields were unit, colour, item_type, stock_untracked, sell_screen_hidden, sell_screen_removed and the three link lists. The affected category fields were colour, sell_screen_hidden and modifier groups.

The worst case was a till gift voucher reverting to `standard`.

- `internal/catalogpull/base.go`: adds `tillOwnedFields` (item and category). It is a deny-list next to `uploadFields`. It documents that a field leaves the set, with an `ApplierVersion` bump, in the release that gives the cloud a write path for it.
- `internal/catalogpull/apply.go` `(*runner).apply`: on a linked row (a `cloud_links` entry exists), those keys are dropped from the decoded row before the per-kind applier runs.
  - The drop sits outside the `!r.forced` guard, so a conflict answer keeps them too.
  - Creates (unlinked rows) still apply as served.
  - `icon` is deliberately still applied (#6071 / in-flight PR #2501).
- Tests: `till_owned_6072_test.go` (T1–T5, T7, T8 and the Tester's AC6 clear-case test).
  - Four existing link-list update tests now call the `allFieldsPulled(t)` helper. This keeps the future "cloud masters links" code path covered rather than deleting it.
- `docs/data-model.md`: a "Till-owned fields" paragraph and a link-list note.

## Review checks

### (a) Every kept field is never written by ut-cloud

Checked against ut-cloud `main` `feb28e5`.

- The only `CatalogItem`/`CatalogCategory` create/update calls outside `ent/` are in `internal/catalogwrite`. `TestMasteredWritesGoThroughCatalogwrite` enforces that in CI.
- `itemValues.applyCreate/applyUpdate` sets only name, description, sku, price, category, tax code, active, is_weighed, age_restricted and net quantity. Categories set only name, parent, sort_order and active.
- None of these write any kept field:
  - catalogimport, aitools proposals and storemerge all go through catalogwrite.
  - claims ingest (`catalog_ingest.go`) and `catalog_writes.go` write `StoreCatalogItem`, the till snapshot, not the mastered rows.
- No raw SQL updates `catalog_items`/`catalog_categories`. No `Catalog*Mutation` is used outside the scope hook.
- The only `SetOptionSetCloudIds`/`SetStockUntracked` on `CatalogItem` are in tests.
- `catalogfeed/kinds.go` serves these columns straight from the ent rows, so the feed always carries the schema defaults (`unit` "each", `item_type` "standard", "" / false / []).
- Confirmed.

### (b) No cloud-written field is dropped

- T7 partitions the fixture row: the cloud-written set matches the catalogwrite list above, and the till-owned set is disjoint from `uploadFields`.
- The Tester's `TestPullStillClearsMasteredFieldsOnALinkedRow_6072` shows that description, category, tax and net-quantity clears still apply on a linked row.

### (c) "Already linked" is correct on every path

`apply()` is the single entry point for:
- the feed pull
- accepted and conflict answers (`push.go` → `applyRow`)
- the applier-version re-pull and the 410 resync (links survive both)

A till-created row's first answer is linked, because `push.go` upserts the link at seal time. The till-create subtest covers this. `adoptForLocalDelete` does not apply the row at all.

A refused or deferred create rolls back its savepoint and so stays unlinked. Its retry is still a create.

### (d) Gift voucher / money

- `item_type` is kept, so a till gift voucher is no longer silently turned into a standard item by a cloud price edit. The price itself is still mastered.
- No money arithmetic is touched.

### (e) Concurrency and transactions

- The drop happens inside the caller's tx/savepoint, before any write.
- No new queries are added.
- `tillOwnedFields` is a package var that tests swap with `t.Cleanup`. This is the existing `commitChunk`/`maxPagesPerRun` pattern, and the package's tests do not run `t.Parallel`.

### (f) Tests are not false-pass

I re-verified this myself, as follows:
1. Reverted only the `apply.go` hunk and ran `-run '_6072|Until6071'`.
2. T1, T2, T3, T4 (accepted / conflict / till create), T5 and the AC6 test all fail with the claimed reset. For example, T1 got `{Color: Unit:each ItemType:standard …false}` where it wanted the till's `{#0f172a can gift_voucher true true true}`, and T2 got links `"||"`.
3. T7 and T8 stay green, as expected: they pin the partition and the icon interim.
4. Restored with `git checkout -- internal/catalogpull/apply.go`. `cmp` against a byte copy is identical and `git status` is clean.

The Tester's mutations also pin both the `linked` predicate and the placement outside `!r.forced`. The T5 410 leg was proven separately by the Tester.

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | Low | A `null` link list on a linked item/category is no longer refused, because the key is dropped before `refList` sees it. The contract's lists are never null, and the refusal is still tested under `allFieldsPulled`, so it comes back when the cloud masters the lists. | Accepted |
| 2 | Info | T7 partitions the test fixture (`itemRowData`/`catRow`), not ut-cloud's wire struct. A field the cloud adds is only forced into a set once the fixture gains it. The real guard belongs with N1 (a cross-repo contract test). | Accepted → N1 |
| 3 | Info | A linked row whose local row is gone is re-created with local defaults for the kept fields instead of the feed's values. These are the same values, because the feed only ever carries defaults. | Accepted |
| 4 | Info (pre-existing, out of scope) | ut-cloud consumers read `CatalogItem.item_type`/`stock_untracked`, which are always defaults on a cloud-master store: channelsync/channelalloc and catalogread gift-voucher lookups. A till gift voucher is therefore "standard" in the cloud. | → N1 |
| 5 | Info | The item and category `icon` are still reset by a pull. This is #6071's bug, and it is pinned by T8 so that #6071 flips a visible assertion. | → #6071 |

The reviewer changed no code.

## Extra verification (reviewer)

- `gofmt -l internal/catalogpull`: empty.
- `go build ./...`: ok.
- `go vet ./internal/catalogpull/...`: ok.
- `go test -count=1 -timeout 30m ./internal/catalogpull/...`: ok (64s).
- The Tester's full gate: golangci-lint 0 issues, shellcheck 0, all `ci.yml` build-job guards rc=0, and `go test ./...` green except load-only timeouts, which were rerun green.
- Recurring bugs: n/a. There is no file write (`os.MkdirAll`) and no data path (`paths.Data`) in the diff.
- No real shop names or secrets appear in the test data.

## Verdict

**Approve.** The fix is minimal and sits at the single apply chokepoint. The keep set was verified against every ut-cloud write path, and the tests fail for the right reason without the fix.

## Deferred / follow-up cards

- **N1**: master the till-owned fields in the cloud.
  - Needs catalogwrite plus a changes-contract minor, on both sides.
  - Then remove each field from `tillOwnedFields` and bump `ApplierVersion`.
  - Include a cross-repo contract test for the feed's item and category keys (finding 2).
  - Include the cloud-side consumers of item_type/stock_untracked (finding 4).
- **N2**: is_weighed, age_restricted and net quantity are cloud-written but never uploaded, so a till edit is reverted by the next cloud write. The upload contract needs to carry them.
- **ut-docs docs card**:
  - Add a `reference/contracts/cloud-catalog-feed.md` "Till-owned fields" note (no version bump).
  - Add an ADR-0162 §2 dated amendment: a field is mastered once the cloud has a write path for it, and the display fields are not in the §2 list.
- **#6071**: widen to category icons. The fix there is to move `icon` into `tillOwnedFields` after #2501 merges, adjusting `item_icon_6028_test.go`, unless the cloud gets a real icon write path.
- Release note: colours, units, item types, sell-screen and stock-tracking settings and modifier links set on the till are no longer reset by my. edits. Values already reset cannot be restored automatically.
