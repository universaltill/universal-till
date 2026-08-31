# Catalog admin: row-level HTMX OOB swaps instead of whole-table refetch (ut-docs#1363)

**Date:** 2026-08-31
**Card:** [ut-docs#1363](https://github.com/universaltill/ut-docs/issues/1363)
(p2, escalated `complexity:medium` → `complexity:hard` at pick-up — see the
issue comment; scoping showed it touched all 8 mutation handlers plus the
variants-panel OOB path, not "one file + one template").
**Dev:** Fable subagent. **Review:** independent Opus subagent, isolated
worktree, did not write the code under review.

## What shipped

Catalog admin (`/catalog`) mutations used to re-render and OOB-swap the
**entire** unbounded item/barcode/variant/tax-code table after every single
mutation — attaching one barcode triggered 4 full-table queries
(`ListItems`/`ItemBarcodes`/`ItemVariants`/`ListAllTaxCodes`) plus a
full-page re-render. Every one of the 8 mutation handlers (`renderCatalogTable`)
and the variants panel's `withTable` OOB path now answer with **row-level**
HTMX out-of-band fragments for only the one item affected:

- an in-place row replacement (`hx-swap-oob="true"` on `#catalog-row-<id>`),
- a row insert (`hx-swap-oob="beforeend:#catalog-tbody"`),
- a row removal (`hx-swap-oob="delete"` on `#catalog-row-<id>`),
- plus the `#catalog-empty-row` placeholder appearing/disappearing as the
  catalog goes from/to empty.

New single-item repo methods (`internal/data/catalog_repo.go`): `GetItem`,
`ItemBarcodesFor`, `ItemVariantsFor`, `ItemIDForVariant`, `ItemIDForBarcode`
(exact-then-canonical, mirroring `DeleteBarcode`'s resolution),
`HasActiveItems`, `HasOtherActiveItems` — all single-row/indexed-lookup
queries, no new whole-table scans. The fragment protocol itself lives in new
`internal/pages/catalog/row_oob.go` + `web/ui/partials/catalog_row.html`
(with `catalog_table.html` reduced to the initial-page-load skeleton).
Client-side (`web/ui/pages/catalog.html`): the item form and image upload
now request `swap:'none'` (image upload converted from a raw `fetch()` to
`htmx.ajax()` so the response's OOB fragments are actually processed), and
an `htmx:oobAfterSwap` listener re-triggers the search filter.

Reactivation edge case: an item edited from inactive back to active has no
row in the DOM (inactive items are never rendered), so the update handler
reads the item's *previous* `is_active` before writing and answers with an
insert fragment, not an in-place update, when it was previously inactive.

## Independent review — findings, fixed in this pass

The first review round (Opus, isolated worktree, ran the full gate + e2e +
TDD-reverted 3 of the new protocol tests) returned **NO — not safe to merge**
with 2 blockers and 4 should-fix findings. All fixed in this same pass
(one review round; no blocker survived, so no second round was needed):

1. **BLOCKER, fixed** — `internal/httpx/sync_banner_test.go` built the
   `/catalog` page's `NewRenderer` from its own independent partial-file
   list (separate from the production call site in `handlers.go`) and was
   never updated when `catalog_row.html` was split out, so
   `go test ./internal/httpx/...` failed with `no such template
   "catalog_row"` — the production render path (`handlers.go:264-265`) was
   already correct; this was test-only breakage. Fixed: added
   `catalog_row.html` to both `NewRenderer(...)` calls.
2. **BLOCKER, fixed** — `guard-docs-shots.sh` failed: the diff touches
   `web/ui/**`/`internal/pages/**.go`, inside the guard's hashed surface.
   Fixed: `make docs-shots`, re-ran the guard clean. (No shop-owner-visible
   screen changed — the row markup is cell-for-cell identical to the old
   inline table row, confirmed by diffing `catalog_row.html` against the
   pre-refactor `catalog_table.html` — so this is a hash-surface
   regeneration, not a documented behaviour change; **no `web/help/**`
   topic update was needed**, confirmed rather than assumed.)
3. **SHOULD-FIX, fixed** — real UX regression: a `beforeend` insert always
   lands the new row at the bottom of the tbody, and an edited row stays
   wherever it already was — `ListItems`' `ORDER BY name` means the old
   full-table re-render always restored name-sorted order; the row protocol
   never did. Reviewer measured it live against the 50-item demo catalog: a
   created item landed at index 50/51, an edited one stayed put regardless
   of its new name. Fixed client-side: a `sortRows()` call in the same
   `htmx:oobAfterSwap` listener re-sorts `#catalog-tbody`'s `.catalog-row`
   children by `data-name` after every insert/update.  `appendChild()` on
   an already-attached node *moves* it rather than cloning/detaching, so
   this preserves row DOM identity — verified by a new e2e test asserting
   both the sorted position and `isConnected`.
4. **SHOULD-FIX, fixed** — four comments (`row_oob.go`, `catalog_row.html`,
   `row_oob_test.go`, the e2e spec) asserted an OOB delete/swap whose target
   is missing "console.errors" in the vendored htmx 1.9.12, used as the
   stated reason for guarding fragment emission. Independently re-verified
   against `web/public/vendor/htmx.min.js`: `oobSwap`'s "no target" branch
   keys off `document.querySelectorAll(...)` being falsy, which it never
   is (an empty `NodeList` is truthy; only an invalid *selector* throws) —
   the branch is dead code, and an over-emitted delete is a silent no-op
   (reviewer confirmed live: double-deactivating an item left the console
   empty). Comments corrected to state the real reason the guards exist —
   matching each fragment to actual DOM state is simply the correct
   behaviour, not error-avoidance — same runtime behaviour, no logic
   changed.
5. **Recommended, fixed** — `writeCatalogRowOOB` still called
   `repo.ListAllTaxCodes(ctx)` (a whole-table read) on every row write, the
   last of the original finding's 4 whole-catalog queries left unaddressed.
   Replaced with the existing single-row `GetTaxCode(ctx, id)` lookup: zero
   queries for the common no-tax-code case (`Item.TaxCodeID == nil`), one PK
   lookup otherwise — strictly cheaper, not just smaller. Verified semantic
   equivalence with the replaced `taxCodeNameFunc`: both resolve active
   *and* inactive tax codes (ut-docs#1178 finding F1) and both render `""`
   on a nil/unresolvable id; existing coverage
   (`TestCatalogTablePartial_TaxCodeShowsNameNotID`,
   `TestCatalogPage_InactiveTaxCodeSurvivesUnrelatedSave`) already pins
   this.
6. **Nit, documented not fixed** — multi-client DOM drift on the
   empty-state placeholder (two tills viewing `/catalog` concurrently can
   see the placeholder appear/disappear based on stale local state).
   Cosmetic, self-heals on reload; a one-paragraph "known accepted
   limitation" note added to `row_oob.go`'s header.

**Deferred to Backlog, not fixed here** (both genuinely separate from this
card's scope):
- Nit: a rapid double-submit of a *reactivating* save (inactive → active)
  can duplicate a row in the DOM until reload — `#item-form-submit` isn't
  disabled during any save request, a pre-existing gap across the whole
  form that this refactor made slightly more visible (previously
  idempotent by the full-table swap). New Backlog card.
- Informational: `go test -race ./internal/data/...` exceeds the default
  600s per-package timeout in this environment — confirmed by both the
  Dev and Reviewer passes to be a pre-existing scale characteristic
  (`ok … 1290s`/`1306s`, no race, no failure), not caused by this diff, and
  irrelevant to CI (plain `go test`, no `-race`). New Backlog card so the
  next person hitting it doesn't re-investigate from scratch.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...` clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full
  CI-equivalent suite, all packages green.
- `go test -race ./internal/pages/catalog/... ./internal/data/...` — green
  (`internal/data` needs `-timeout=25m`+ under `-race`, see above; not a
  CI concern).
- All 19 CI-blocking guards from `ci.yml`'s `build` job pass, including
  `guard-data-access` (all new SQL confined to `internal/data`),
  `guard-i18n` (no new user-facing strings), `guard-htmx-loaded`,
  `guard-help-topics`, `guard-docs-shots` (regenerated).
- e2e: the new `catalog-row-oob-1363.spec.ts` (7 tests: insert/edit/
  deactivate DOM-identity + sort-order, filter-reapplication including a
  panel-driven case) plus the 4 pre-existing catalog specs most likely to
  regress (`catalog-save-notice-917`, `catalog-thumbnail-no-request`,
  `catalog-import-friendly-errors`, `catalog-image-to-till`) — 12/12 green,
  re-run after every fix in this pass.
- Independent TDD re-verification (by the reviewer, in its isolated
  worktree): reverted 3 of the new protocol behaviors one at a time
  (reactivation → insert-not-update, empty-state placeholder append on last
  deactivate, empty-state delete skipped for a create into a non-empty
  catalog) — each reproduced the exact defect the corresponding test names,
  then was restored to green.
- No real client/shop name or secret-shaped literal introduced.

## Safe-to-merge verdict

**Yes.** Both blockers fixed and re-verified; all findings the reviewer
recommended fixing in-pass are fixed; the two genuinely-deferred items are
tracked as new Backlog cards, not silently dropped.
