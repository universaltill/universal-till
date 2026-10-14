# Code review — item off/on revives the variants switched off with it (ut-docs#4348)

**Date:** 2026-10-14 · **Lane:** lane:cloud-41 · **Author:** Opus 5.5 (dev subagent) · **Reviewer:** Fable (independent, own worktree)

## What shipped

- Migration `073_item_variants_retired_with_item.sql` adds `item_variants.retired_with_item`
  (INTEGER NOT NULL DEFAULT 0; append-only, checksum pinned — ADR-0100). No backfill.
- `cascadeItemActiveToVariants` (internal/data) runs in the item writer's transaction:
  on → off retires the live variants and flags them; off → on revives exactly the flagged
  (and still inactive) variants and clears the flag. A variant already off before its item
  was switched off keeps 0 and stays off.
- Callers: `DeactivateItem` (now one transaction), `SaveItem` (my./cloud `save_item`
  directive; replaces the off-only cascade and its "never revives" comment), and
  `UpdateItemReturningWasActive` (till item editor). The editor did not cascade at all before,
  so an item switched off there kept live variants under it; now all three behave the same.
- Explicit per-variant on/off clears the flag: `DeactivateVariant` always; `UpdateVariant`
  and `SaveVariant` only when the write actually changes `is_active`. The till's variant form
  always posts its Active box, and my. may echo `active` on every edit (ut-cloud
  `claims/item_variants.go`), so an unchanged echo keeps the flag.
- Main → joined till sync copies columns from the local schema, so the flag travels with no
  sync change; mixed versions degrade to the old behaviour (primary-wins).
- Import only creates items (existing ones are skipped), so it never reactivates; no change.
- Help: `web/help/{en,de,tr,ar,fa}/catalog.md` step 1 explains the revive, and that a
  variant switched off with its item before this version stays off.
- ut-docs `reference/manage-shop-catalog-api.md` (`save_item.active`, `save_item_variant.active`)
  updated in a separate ut-docs PR.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| D1 | major (orchestrator) | `SaveVariant` cleared the flag whenever the patch carried `active`; my. can echo an unchanged `active`, which would silently drop a variant from the revive | Fixed: same change-only CASE as `UpdateVariant`; test extended (echo keeps flag; a real switch-off clears it and survives an off/on cycle) |
| R1 | minor | Help stated the revive unconditionally, but legacy rows (no backfill) stay off | Fixed in en + 4 translations; help-drift/help-topics guards green |
| R2 | nit | Revive predicate lacked `AND is_active = 0` (state unreachable, defence in depth) | Fixed |
| R3 | nit | `UpdateItemReturningWasActive` skips the cascade when the pre-read failed (prevKnown=false) | Accepted: documented conservative choice; the read runs in the same immediate tx, so a failure there means the write fails too |

Reviewer's writer audit found no other writer of `items.is_active` / `item_variants.is_active`
(option-set generation inserts with the default; sync retire-in-place only sets 0; agentaction
and item delete don't touch it). DeactivateItem's new tx has no caller holding a tx.

## Verified beyond automated tests

- Reviewer confirmed SQLite evaluates `CASE WHEN is_active = ?` against the pre-update value
  (throwaway test on modernc.org/sqlite, both SET orders).
- TDD re-verified by the reviewer in its own worktree: with the off → on branch disabled all
  five `*_4348` tests fail on the "after switch-on" assertion; restored, all pass.
- No UI change; no screenshots regenerated. No surface needed a visual check.

## Gate

gofmt clean · go build ./... · go vet · go test ./... (full) · golangci-lint 0 issues ·
guards: data-access, i18n, help-topics, help-drift, card-data-schema,
migration-version-collision, core-neutral, compliance-claims, competitor-naming,
price-history-sync, kiosk-engine, no-showmodal.

## Verdict

Safe to merge. Deferred: none.
