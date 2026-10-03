# Code review — every catalogue dialog carries its loaded stamp (ut-docs#3606)

Date: 2026-10-03 · Branch: `fix/3606-catalog-dialog-base-stamp` · Lane: cloud-54
Author: Opus 5.5 (Dev subagent) · Reviewer: Fable (independent, worktree) ·
Fix round: orchestrator (Opus 5.5)

## What shipped

Follow-up to ut-docs#2817 (catalogue write-through to the main till),
findings 3+4 of `2026-10-03-catalog-write-through-replica-2817.md`.

- Every form that edits an existing record on a conflict-checked
  write-through route now sends the record's `updated_at` **as rendered**
  in `base_updated_at`: variant edit rows and the item cost / lead time /
  reorder level forms (`catalog_variants.html`), modifier-group edit rows
  (`modifiers.html`), the `/categories` record dialog (`categories.html`,
  filled by `record-dialog.js` from the row), and the Designer's category
  forms (`buttons.html`). Create forms send none. The stamps come from the
  existing list queries (`VariantsForItem`, `ListCategoriesForAdmin`,
  `ListAllModifierGroupsWithAssignments`) — no per-row lookups.
- Before this, these dialogs fell back to the additional till's own copy
  of the row, which the pull refreshes ~1s after a main-till change, so a
  main-till change landing >1s before the save was silently overwritten.
- `/api/buttons/add` is now conflict-checked on the row `AddButton`
  actually writes: new allow-listed kind `item_button` (the item's first
  button by `sort_order, barcode`, the repo's own query), keyed on
  `itemId`/`item_id`. It used `form:code`, which pointed at the wrong row
  (or none) when the item already had a button under another barcode.
- `recentSaves` (`catalogsync/forward.go`): surfaces that re-render from
  this till's own copy right after a save (Designer `buttons-changed`,
  `/categories` redirect) show a stamp the pull hasn't caught up from yet.
  Forward remembers, per main till + record, the unbroken chain of stamps
  this till's own accepted saves moved past, and sends the latest for a
  form showing any of them. The chain restarts whenever a save's base is
  not the remembered latest, so it never spans someone else's change.
- `followItemStamp` (`catalog.html`): the item editor, its card and the
  Variants-tab item forms all edit the item row; after a successful save
  from one, the others follow only if they showed the exact stamp that
  save sent. This also fixes an existing self-conflict (cost save, then
  details save, in one open dialog).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | `recentSaves` kept one (shown→saved) pair per record: the 3rd consecutive save from a re-rendered Designer/categories form showed a mid-chain stamp and was refused as a conflict with the till's own save (reviewer reproduced; regression vs main). | **Fixed**: chain of superseded stamps (≤16), restarted when a save's base isn't the latest. `TestCatalogWriteThrough_DesignerThirdSaveIsNotASelfConflict` (seen failing on the old code with 409, passing now), `TestRecentSaves_ChainCovers…`, `TestRecentSaves_ChainRestartsAfterSomeoneElsesChange`. |
| 2 | Should-fix | Audit rows / answer for a till-synced `/api/buttons/add` now name `item_button` + the item id instead of `button` + the code. | **Accepted, recorded here**: it names the row actually written; only the audit page prints it (`audit_page.go`), no code consumes `Entity` for buttons. |
| 3 | Should-fix | Eviction cleared the whole map at 512. | **Fixed**: 10-min TTL, then drop-oldest. `TestRecentSaves_ExpireAndEvictOldest`. |
| 4 | Nit | `SyncPrimaryURL` called 3× per forward. | Fixed (hoisted). |
| 5 | Nit | Category stale-save test's status clause was inert. | Fixed: asserts a ≥400 refusal carrying the conflict message (the dialog answers its own 400 fragment). |
| 6 | Nit | Variants panel swallowed the `CatalogUpdatedAt` error. | Fixed: logged. |
| 7 | Nit | One-second `datetime('now')` resolution blinds both check and substitution within a second. | Pre-existing design (#2817), not changed. |
| 8 | Nit | Odd rune-built keys in the bound test. | Fixed (`strconv.Itoa`). |

Reviewer found sound: substitution can't mask a main-till or other-till
change; keyed per main URL so a re-pair is safe; lock use, `-race` clean;
every conflict route has a loaded stamp except buttons/add (justified: an
add-from-search shows no button record, so the local fallback — now on the
right row — applies); create forms send none; `item_button` query mirrors
`AddButton`; old↔new version skew only ever skips a check, never a false
409; `followItemStamp` only rewrites values equal to an accepted sent
stamp, never on a GET; templates escaped by html/template.

## Verification beyond unit tests

- Reviewer re-verified TDD claims by reverting the fix:
  `TestCatalogWriteThrough_VariantStaleLoadedStampRefusedAfterPull` (200
  without the hidden input) and
  `TestSyncCatalogApply_ButtonAddConflictCheckedOnTheRelabelledRow` (200,
  entity `button`/`NEWCODE` on the old route).
- New Playwright spec `e2e/tests/catalog-item-stamp-follow-3606.spec.ts`
  drives the real item dialog: cost save advances the editor base and the
  card; details save advances the panel forms. Fails with the pre-change
  `catalog.html` (editor base stuck one second behind), passes with it.
- Playwright on the touched surfaces (66/66): categories record dialog and
  editor, category popup, Designer drop, modifiers shop-wide / bulk delete,
  item modifiers tab, variant grid reachability (1024×600, 1280×800, 360px,
  RTL), variant price save (en/de), price history, item form, row OOB.
- Visual: the change adds hidden inputs and data attributes only — no
  pixels change (docs-shots surface hash updated, `Docs-Shots-Unchanged`).
  Layout specs above cover 360px, kiosk 1024×600, tablet 1280×800 and RTL;
  no new screenshots were inspected.
- Not driven: a real two-till pair in a browser (the pair is exercised by
  the Go handler tests with two real migrated DBs).

## Verdict

Safe to merge after the fix round; full gate green (see PR).

## Deferred

None.
