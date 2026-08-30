# Code review: import placeholder thumbnails (ut-docs#1189, Phase 1)

**Date:** 2026-08-30
**Author:** Farshid Mirza (autonomous pipeline, `lane:cloud-54`)
**Reviewer:** independent Opus subagent, isolated worktree
**PR:** universaltill/universal-till (this branch: `feat/1189-import-placeholder-thumbnails`)

## What shipped

Phase 1 of ut-docs#1189: an item imported via CSV or `.bkp` with no source
image gets a bundled generic category icon (coffee / pastry / sandwich /
drink / generic) instead of a permanently blank tile, chosen by whole-word
keyword match against the item's name (checked first) then its category.
Offline, zero AI, zero network — matches how SumUp/Square/Loyverse actually
handle this (per the research-first check, ut-docs#1054).

- `internal/catimport/placeholder.go` — pure keyword→icon matcher (no DB,
  no SQL, matches this package's existing "pure parser" contract).
- `internal/data/catalog_repo.go` — `EnsureDefaultThumbnail` (insert only
  if the item has no thumbnail row yet; used by import) and
  `SetItemThumbnail` (unconditional upsert; used by real photo uploads and
  barcode-lookup auto-fill, added in review response — see F2 below).
- `internal/pages/import_page.go` — one wiring call in the commit loop,
  right after `tx.Commit()` succeeds, same after-commit/best-effort
  placement as the existing barcode-attach step immediately below it.
- `web/public/assets/category-icons/*.svg` — 5 small bundled icons,
  embedded via the existing `//go:embed ui public` directive, no new
  runtime file writes.
- `web/help/{en,fa,ar,tr}/catalog.md` — manual updated in the same branch
  per the standing "manual ships with the feature" rule, plus
  `make docs-shots` regenerated.

## Independent review — findings, severity, resolution

Reviewed by a fresh-context Opus subagent in an isolated git worktree
(complexity:medium → Opus review, per the `scrum-master` skill's model
routing). The subagent built, vetted, ran the full affected test surface,
independently re-verified the TDD claim via a real revert→fail→restore→pass
cycle, ran the CI-blocking guards, read every changed/added file, and
probed the keyword matcher and bundled SVGs directly rather than trusting
the diff's own claims.

| # | Severity | Finding | Resolution |
|---|---|---|---|
| F1 | **Blocker** | Help text said "never a blank tile" and pointed at the Catalog grid as a universal fix, but the admin Catalog table itself (a separate, disk-based thumbnail-resolution path — see F2/F3) still shows a blank tile for a placeholder-only item. Shipped false in 4 locales. | Reworded in all 4 locales to name exactly which screens get the automatic icon (sale screen, basket, search) and which don't yet (the Catalog list itself), and that adding a real photo now replaces the icon everywhere at once (see F2). |
| F2 | Should-fix | The manual item-photo upload handler (`POST /api/catalog/item/image`) and the barcode-lookup auto-fill path (`saveLookupImage`) wrote only the disk `thumb.png` file, never an `item_images` row — so a real photo uploaded over a placeholder icon showed correctly in the admin Catalog table (disk-based) but the sale-screen grid/basket/self-order/suggestions (`item_images`-based) kept showing the placeholder icon forever, with no in-app way to clear it. This is a pre-existing gap the reviewer traced independently (also documented from the shortcuts-button angle in `internal/data/shortcuts_repo.go`'s own comment), made materially worse by this diff (a stale placeholder is confidently wrong, where before it was an honest blank). | Added `CatalogRepo.SetItemThumbnail` (update-if-exists, else insert) and wired it into both call sites. Regression tests added for both (`TestItemImageUpload_RecordsItemImagesRow`, extended `TestCatalogCreate_SavesLookupImage`), both TDD-verified red→green. |
| F3 | Should-fix | The scoping analysis this PR's description was drafted against (and the follow-up card ut-docs#1324 as first filed) claimed the self-order kiosk was already covered by this fix's `item_images`-backed mechanism. The reviewer traced `internal/pages/self_order_shop.go`'s `loadShopItems` and found it hardcodes the same disk-convention path as the admin table (`"/public/assets/items/" + it.ID + "/thumb.png"`) — the kiosk is NOT covered. | Corrected on ut-docs#1324 (edited before this PR opened) — the kiosk is now listed among the surfaces still needing the follow-up, flagged as high-priority given customer visibility. |
| F4 | Should-fix | `TestPlaceholderIcon_AllIconsKnown` only checked that `iconPath` returned a non-empty string for every known icon key — a typo'd filename (`cofee.svg`) would still pass the test and 404 at runtime. | Test now resolves the real on-disk path (mirroring `internal/httpx`'s own built-in-asset resolution convention) and `os.Stat`s it for every icon key. |
| F5 | Nit (fixed) | First-draft keyword matching used plain `strings.Contains`, which matched "tea" inside "s-**tea**-k" (→ "Steak Sandwich" tagged coffee) and "cola" inside "cho-**cola**-te" (→ "Chocolate Bar" tagged drink) — an item sharing no real word with any keyword still got mis-tagged. | Switched to whole-word tokenized matching (`tokenize`, splits on non-letter/digit runs, membership-checks the resulting word set). Confirmed the false positives are gone; existing keyword-match test cases all still pass unchanged. |
| F6 | Nit (documented) | The keyword table is English-only — a shop cataloguing in Persian/Arabic/Turkish gets "generic" for every item, since none of those scripts match the English keyword list. | Documented as a known Phase 1 limitation in `placeholder.go`'s doc comment. Not fixed here — real scope, would need a translated keyword table per supported catalog language, better as its own follow-up if it turns out to matter in practice. |
| F7 | Nit (fixed alongside F2) | `EnsureDefaultThumbnail`'s doc comment claimed it protects "an operator's own upload" — false at the time, since uploads didn't write `item_images` at all (the actual F2 bug). | Comment corrected once F2's fix made the claim true. |
| F8 | Nit (accepted, deferred) | `EnsureDefaultThumbnail`/`SetItemThumbnail`'s check-then-write isn't atomic — `item_images` has no `UNIQUE(item_id, role)` constraint, so two concurrent calls for the same item could both insert. Not reachable from the import loop (sequential, fresh UUID per row) or the upload handlers (one request at a time per item in practice), but the methods are exported. | Accepted as-is — fixing needs a schema migration (a new `UNIQUE` index; `item_images`' existing rows across the whole table have never had this constraint, not something introduced by this diff) plus an `INSERT ... WHERE NOT EXISTS`/upsert rewrite, out of proportion to this card's Phase 1 scope. Worth a note on ut-docs#1324 if that follow-up touches this table's schema anyway. |
| F9 | Nit (accepted) | One extra `SELECT` + autocommit `INSERT` per imported row, outside the row's own transaction. Immaterial on a 217-item `.bkp` (this card's own reference scale); a measurable add on a several-thousand-row import. WAL is already on. | Accepted, low priority. |
| F10 | Nit (accepted) | Both new `internal/pages` regression tests use CSV fixtures only; `.bkp` isn't separately exercised for this specific behavior. | Accepted — both parsers converge on the same commit loop this fix hooks into, so `.bkp` is covered by the mechanism even though not by a dedicated test; not worth a near-duplicate test for Phase 1. |

## What was verified beyond automated tests

- **Live, driven verification against the real running app** (not just
  `httptest`): started the actual server (`UT_STORE=sqlite`,
  `UT_AUTH=off`), imported a real CSV via `curl` through the currency-
  confirmation gate, confirmed the `item_images` rows landed with the
  correct icon paths via a direct SQLite query, and confirmed both icon
  SVGs served a real `200` over HTTP through the app's own static handler
  — not just that the Go code compiled.
- **TDD claims independently re-verified twice**, both by the reviewer
  subagent (in its own isolated worktree, per the `reviewer` skill's
  worktree-isolation rule) and again by this session for every fix made
  in response to review findings (F2's upload-handler fix, F5's word-
  boundary fix): revert the fix → confirm the specific test **fails with
  a real assertion**, not a build error → restore → confirm it **passes**
  again. Full transcripts for both rounds exist in this session's own
  record (the reviewer's own transcript is in its final report, quoted
  into the PR).
- **All 18 CI-blocking guards** run locally and green, including
  `guard-docs-shots.sh` (screenshots genuinely regenerated via
  `make docs-shots`, not hand-edited) and `guard-help-topics.sh`
  (all 4 locales' manual content stays structurally in sync).
- **Whole-tree `go test ./...`** green, not just the touched packages.

## Explicitly deferred

- Full thumbnail-resolution coverage for the admin Catalog table, the
  variants panel, the self-order kiosk, and the AI thumbnail API — all of
  which resolve a thumbnail from a disk-file convention rather than
  `item_images`, independently of this diff. Filed as ut-docs#1324, scope
  corrected per F3 above (the self-order kiosk is NOT covered by this fix,
  contrary to the card's own first draft).
- F6 (non-English keyword matching) and F8 (non-atomic upsert) — see table
  above for why each is being left for a follow-up rather than folded in
  here.

## Verdict

**Safe to merge.** The one blocker (F1) and all should-fix findings (F2–F4)
are resolved in this same commit and independently TDD-verified; the
remaining nits are either fixed opportunistically (F5, F7) or explicitly,
reasonably deferred (F6, F8–F10) with the reasoning recorded above rather
than silently dropped.
