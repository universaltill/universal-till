# Code review: catalog admin mutations → row-scoped HTMX OOB swap (ut-docs#1363)

**Date:** 2026-08-31
**Card:** universaltill/ut-docs#1363 (`complexity:hard`, split off #1321's performance audit finding 17)
**Dev:** Fable subagent (per model routing for `complexity:hard`)
**Review:** Opus subagent, isolated worktree, independent of the Dev pass

## What shipped

Catalog admin mutations (create/edit/deactivate item, attach/detach
barcode, variant/modifier edits) now answer with just the affected item's
`<tr>` as HTMX out-of-band fragments, instead of re-running 4 unbounded
whole-catalog queries (`ListItems`/`ItemBarcodes`/`ItemVariants`/
`ListAllTaxCodes`) and re-rendering the entire table on every single-row
change.

- `internal/data/catalog_repo.go`: 5 new single-item repository methods
  (`GetItem`, `ItemVariantViews`, `CountActiveItems`, `ItemIDForVariant`,
  `ItemIDForBarcode`) — all SQL stays here, per the repository pattern.
- `internal/pages/catalog/handlers.go`: `renderCatalogTable` (the old
  whole-table closure, called from all 8 mutation handlers) replaced by
  `writeCatalogRowOOB`/`respondItemRowOOB`, handling insert / update /
  delete, plus empty-state placeholder bookkeeping for the first-item and
  last-item edge cases.
- `web/ui/partials/catalog_row.html` (new): row + empty-row partials, and
  the OOB carrier fragments (all wrapped in `<tbody>` — required by htmx
  1.9.12's fragment parser for a `<tr>`-shaped OOB payload; see
  verification below).
- `catalog_table.html` / `catalog_variants.html` / `catalog.html`:
  addressable `#catalog-rows` tbody, a nested OOB carrier for the variants
  panel's own ride-along row update, and the two JS call sites
  (`htmx.ajax` item form, raw `fetch()` image upload) converted to the new
  swap contract.
- `web/help/img/{en,ar,fa}/sell.png` + `manifest.json`: regenerated via
  `make docs-shots` (twice — once by Dev, once more by the orchestrator
  after review fixes touched the app surface again).

## Independent review

Spawned as an `Agent` at `model: opus` (Dev ran at `model: fable`, per
"Model routing by complexity" — hard card, review deliberately NOT
Fable), in an isolated `git worktree` off a `WIP: pre-review snapshot`
commit on the feature branch (ut-docs#386's mitigation — never let a
review's revert/restore share the orchestrator's live checkout).

**Verdict: safe to merge**, no blocking issues. The review independently
re-ran the full gate (gofmt/build/vet/targeted+full `go test`/all CI
guards/full e2e suite — 255 passed) and did 3 separate TDD
revert-then-restore checks against the new tests, not just read the diff.
It also read the actual vendored `htmx.min.js` source to verify the
`<tbody>`-wrapping claim in the code comments, rather than taking it on
faith — confirmed correct: htmx's `makeFragment` keeps only the first
`<tbody>` sibling for a bare `<tr>`-shaped response, and `oobSwap`'s
selector-style `beforeend` inserts the OOB element's *children*, so the
carrier must be a `<tbody>` and the row itself must carry no OOB
attribute in the insert case.

### Findings, triaged

All 7 findings were non-blocking. Fixed in this branch:

- **N1 (test-quality)** — `oob_row_swap_test.go`'s variant-summary
  assertion (`strings.Contains(body, ">Large<") || strings.Contains(body,
  "Large,")`) could never match the actual rendered markup
  (`Variants: Large</div>`), so it was a vacuous negative check. Proven
  by the reviewer: deleting `ItemVariantViews`'s `is_active` filter made
  the `internal/data` unit test fail correctly while this handler test
  kept passing. **Fixed** to `strings.Contains(body, "Large")` and
  independently re-verified by the same revert/restore method (now fails
  correctly when the filter is removed, passes when restored).
- **N2 (real, currently-unreachable bug)** — the empty-state placeholder
  bookkeeping appended a fresh `#catalog-empty-row` on every response
  where `CountActiveItems() == 0`, regardless of whether that specific
  response actually caused the transition — so two deactivation calls in
  a row (e.g. a genuinely no-op second POST) would leave two
  `#catalog-empty-row` elements in a real browser's DOM. Not reachable
  from the UI today only because of N3 below (unchecking Active in the
  edit form doesn't actually submit `isActive=0`), but it becomes
  reachable the moment N3 is fixed, and it's a latent correctness bug
  regardless of what currently triggers it. **Fixed**: the delete branch
  now always OOB-deletes any existing placeholder immediately before
  appending, making the append idempotent (an OOB delete against a
  missing id is a documented no-op in htmx 1.9.12).
- **N5/N6 (comment/dead-code accuracy)** — the code comments and one
  e2e-spec header incorrectly claimed htmx 1.9.12 logs a console error
  (`htmx:oobErrorNoTarget`) for an OOB swap against a missing target id.
  Read the actual vendored source: `querySelectorAll` is always truthy,
  so that error path is unreachable in this version — the real reason
  the code tracks active-item count explicitly is to keep the DOM
  correct (no stray/missing placeholder), not to avoid a console error.
  **Fixed** the three comments to state the real reason. Also removed a
  now-dead `htmx:afterSwap` listener in `catalog.html` that checked for
  `ev.detail.target.id === 'catalog-table'` — nothing targets that id
  for a swap anymore (only `htmx:oobAfterSwap` fires for row updates
  now), and updated two stale `renderCatalogTable`/`catalog_table.html`
  references in comments (`internal/httpx/httpx.go`,
  `tax_code_display_test.go`) that named the function this diff deleted.

Not fixed in this diff, filed to the board instead:

- **N3** — pre-existing bug (not introduced here): the item-edit form's
  `isActive` checkbox has no paired `<input type="hidden" name="isActive"
  value="0">` (unlike the variant/modifier forms, which already have
  this), so unchecking Active and saving silently does nothing —
  `parseItemInput` reads the field's absence as "still active." Surfaced
  during this review because the diff's own new test
  (`TestItemUpdate_SetInactiveRemovesRow`) correctly covers the
  server-side handling of an `isActive=0` payload that the real form
  never actually sends. Filed as **ut-docs#1367**
  (`complexity:easy`, `status:ready`).
- **N4** — a genuine, AC-silent behaviour change: a newly created item
  now appends at the bottom of the visible list (`beforeend`) rather than
  its alphabetically-sorted position, and self-heals on the next page
  load. Inherent to row-scoped OOB (the issue's own scoping notes
  prescribed `beforeend`); noted here for visibility rather than as a
  card — arguably an improvement (you see what you just created), but
  worth a product-owner nod if it surprises anyone.
- **N7** — coverage gaps, none blocking: the empty-state placeholder
  lifecycle, an active search filter interacting with an OOB insert, an
  image-upload failure path (verified by reading htmx's error-handling
  code that it correctly falls into the existing error-notice branch,
  but not exercised end-to-end), the deactivate button on a row that
  arrived via insert specifically, and `ItemIDForBarcode`'s canonical-
  barcode fallback. None reflect known bugs — flagged as coverage debt,
  not required for this card's AC.

## Verification (beyond automated tests)

- Full `go test ./...` — all 42 packages pass (run twice: once by
  the review subagent in its worktree, once more by the orchestrator on
  the final, post-fix diff).
- All CI-blocking guards from `.github/workflows/ci.yml`'s `build` job —
  clean, including `guard-data-access.sh` (all new SQL confined to
  `internal/data`), `guard-i18n.sh` (zero new locale keys — this is a
  rendering-protocol change, no new copy), and `guard-docs-shots.sh`
  (screenshots regenerated to match the final diff).
- Full default-project e2e suite — 240 passed (orchestrator run) / 255
  passed (review subagent's own separate run, larger project selection).
  The 5 catalog-specific specs (including the new
  `catalog-row-oob-1363.spec.ts`, which drives create → edit →
  deactivate in real Chromium with a clean-console assertion) all green,
  re-run a third time after the review fixes landed.
- TDD re-verification, done independently by both Dev and Reviewer:
  reverting the empty-state branches, the barcode-owning-item resolution
  order, and the variant `is_active` filter each produced the expected
  real assertion failure (not a panic/compile error); restoring returned
  all to green.
- Manual (`web/help/en/catalog.md`): confirmed unaffected — this is an
  internal rendering-protocol change with zero visible/operator-facing
  behaviour difference (aside from N4's insert-position nuance, which
  self-heals). No prose has gone stale; screenshots regenerated anyway
  since the app-surface hash changed.
- No real client/shop name used as demo data; no literal secrets
  introduced.

## Safe-to-merge verdict

**Yes.** No blocking findings from an independent, adversarial review
that actually ran the code (not just read it) and re-derived the htmx
mechanics from source. All non-blocking findings are either fixed in
this branch (N1, N2, N5, N6) or explicitly deferred with a reason (N3 →
ut-docs#1367, N4/N7 → noted here, no card needed).
