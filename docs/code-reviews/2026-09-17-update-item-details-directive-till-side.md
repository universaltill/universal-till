# Code review: `update_item_details` directive — till-side (ut-docs#2324)

**Date:** 2026-09-17
**Card:** ut-docs#2324 — "ut-cloud shop panel: catalog items editor (Phase A slice of #2289)"
**Complexity:** medium (Sonnet build, Opus review, per model-routing rules)
**Repo/side:** `universal-till` — the till-side directive hook and dispatch

## What shipped

A new directive type `update_item_details` (ADR-0095 Decision 1 pattern,
mirroring the already-shipped `upsert_category`): a **partial update** of a
catalog item's `sku`/`description`/`unit`/`color`/`is_weighed`/
`stock_untracked` fields, queued from the cloud merchant portal and applied
on the till through the same repository layer the local admin item editor
uses.

- `internal/cloudsync/cloudsync.go`: `Hooks.UpdateItemDetails` field, and
  `apply()`'s new `case "update_item_details":` with presence-aware
  `strp`/`boolp` helpers — a `nil` pointer means "absent from the payload,
  leave untouched," distinct from "present but empty/false."
- `internal/pages/cloudsync_wire.go`: `cloudUpdateItemDetails` — the hook
  itself.
- `internal/data/catalog_repo.go`: new `CatalogRepo.UpdateItemPartial`
  method — a single `BEGIN IMMEDIATE` transaction wrapping the
  read-modify-write, mirroring `UpdateItemReturningWasActive`'s existing
  ut-docs#1399 pattern.
- `CategoryID`/`BrandID`/`TaxCodeID` are deliberately never touched — the
  portal has no synced view of those lookup tables yet (ADR-0095 Decision 2,
  tracked as ut-docs#2354).

## Independent review

Opus, isolated git worktree (detached HEAD on the pre-review WIP commit),
never saw the implementation reasoning. Full report in the PR body /
pipeline record; summarized here.

**Commands actually run and independently re-verified (not taken on the
implementer's word):** `gofmt -l .`, `go build ./...`, `go vet ./...`,
`golangci-lint run ./...` (0 issues), `go test ./internal/cloudsync/...
./internal/pages/...` (full suite, ~180s), plus the CI-blocking guards
(`guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
`guard-compliance-claims.sh`, `guard-help-topics.sh`) — all green.

**TDD claims independently re-verified**, not trusted from the commit
message: removed the `case "update_item_details":` block →
`TestApplyUpdateItemDetails` failed with "unknown directive type"; restored
→ green. Injected a full-overwrite bug (`cur.CategoryID = nil`) into the
hook → the crux test (`TestCloudUpdateItemDetails_SkuOnlyLeavesEverythingElseUnchanged`)
caught it immediately.

### Finding S1 (SHOULD-FIX, fixed) — non-transactional read-modify-write

The original implementation used two separate calls
(`CatalogRepo.GetItem` then `CatalogRepo.UpdateItem`) rather than a single
transaction. A genuinely concurrent local edit to price, name, or
active-state landing between the two calls would be silently reverted by
this directive's own stale read — the exact race
`UpdateItemReturningWasActive` (ut-docs#1399) already exists to prevent for
the *other* item-update path, which this new one didn't reuse.

**Fix:** added `CatalogRepo.UpdateItemPartial`, a single `BEGIN IMMEDIATE`
transaction doing the read, the six-field partial merge, and the write —
mirroring `UpdateItemReturningWasActive`'s own locking reasoning.
`cloudUpdateItemDetails` now calls this instead of `GetItem`+`UpdateItem`.

**Regression test:** `internal/data/catalog_repo_update_item_partial_race_test.go`
— `TestUpdateItemPartialConcurrentRace` — real goroutines against a
file-backed (not in-memory) database, 15 rounds, one goroutine calling
`SetItemPrice` and one calling `UpdateItemPartial` concurrently, asserting
both writes survive every round. **Independently confirmed this test
actually catches the bug**: reverted `UpdateItemPartial` to the
non-transactional shape, the test failed consistently across 3 runs
("price reverted by an unrelated sku update"); restored, green across 3
runs.

(A sequential single-goroutine test cannot exercise a true interleaving —
this was tried first, found to pass identically whether or not the fix was
present, and removed in favour of the real concurrency test above. Noted
in-code at `cloudsync_wire_test.go` so a future reader doesn't reintroduce
a misleadingly-named sequential test believing it proves the same thing.)

### Finding S3 (SHOULD-FIX, fixed) — false attribution for blank sku/unit

`updateItemExec`'s own SQL makes a blank `sku` a true no-op
(`COALESCE(NULLIF(?,''), sku)`) but silently **defaults** a blank `unit` to
`"each"`. The original code reported and audited both as real changes
regardless, which is false for `sku` (nothing actually changed) and
potentially surprising for `unit` (a silent reset to "each" reported as
"you asked for this").

**Fix:** `cloudUpdateItemDetails` now normalizes a blank `sku`/`unit`
pointer to `nil` (treated as "not actually provided") before building the
`changed`/audit list — this runs before the "any fields left?" check, so a
request carrying only blank sku/unit correctly reports `"no changes"`.
Colour is treated differently: `""` is a real, valid "clear the colour"
state (matching `upsert_category`'s own convention), so it is NOT
normalized away.

**Regression test:** `TestCloudUpdateItemDetails_BlankSkuAndUnitAreNoOps`
— asserts `"no changes"`, `assertItemUnchangedExcept` with nothing changed,
and zero audit rows written.

### Checks that came back clean (re-verified independently, not just read)

- `CategoryID`/`BrandID`/`TaxCodeID` genuinely never touched — confirmed by
  reading the code and by breaking it (see above).
- Colour defence-in-depth on both layers (till + cloud), byte-identical
  8-swatch palettes, off-palette values cleanly rejected before any write.
- Audit payload contains only genuinely-changed fields.
- No file writes in the diff (no missing `os.MkdirAll` class of bug), no
  hardcoded paths.
- No real client/shop names, no secret-shaped literals.
- No `universal-till/web/help/` update owed — this diff adds no new
  operator-facing UI on the till itself (the till's own catalog editor
  already has these fields; only the cloud portal gained a form).
  `guard-help-topics.sh` passes.

## Verdict

Safe to merge. Both SHOULD-FIX findings (S1, S3) fixed and independently
re-verified via genuine regression tests (one of which required a real
concurrency harness, not a sequential simulation). No BLOCKER findings on
this side of the diff — see the paired `ut-cloud` review record for the
cloud-side BLOCKER (B1) and its fix.

## Explicitly deferred

- Category/brand/tax-code assignment from the cloud panel — needs
  ADR-0095 Decision 2's read-side lookup sync (tracked as ut-docs#2354,
  the same blocker already tracked for the categories editor).
- A cloud-side template render test asserting the new form's default
  radio/select state (`ut-cloud` review's finding S4) — deferred; the
  actual bug (B1) is fixed and covered at the payload-construction layer
  (the real contract boundary), and a render test is a redundant
  belt-and-suspenders check on top of that, not the only line of defense.
  Filed as a follow-up rather than built into this already-large diff.
