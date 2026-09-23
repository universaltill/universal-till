# 2026-09-23 — Designer live sale-screen replica (ut-docs#2174)

## What shipped

`/designer` changed from a flat admin list (`buttons_admin.html`'s
move-up/move-down grid) to an in-place **live replica** of the sale
screen's own product panel (`web/ui/partials/buttons.html` +
`internal/ui/buttons.go`'s `BuildCategoryGroups`), fetched via a new
`GET /ui/designer/buttons` and `ui.ButtonsHTTP`'s new `Designer` flag.
Category CRUD (create/rename/recolor/reorder/remove) is now edited
in-place through a popover-based edit mode that extends the existing
tile "jiggle mode" (`web/public/app.js`) to also cover the category tab
strip — pencil affordance per tab, a `+` add-category tab, keyboard
reorder (ArrowLeft/Right + Escape, mirroring jiggle-mode's existing tile
pattern per ut-docs#826), `hx-confirm` destructive removal matching
`/categories`'s own copy/shape, and the blocked-removal count surfaced
via `/categories`'s existing `ErrCategoryHasItems` error key. New routes
in `internal/pages/designer_categories_api.go` call existing
`internal/data/catalog_repo.go` methods — no new SQL — gated
`canPerform(d, r, "catalog_management")`, matching `/designer`'s existing
gate. 4 new i18n keys added to en/ar/fa/tr (de/es live in separate
`ut-plugin-language-*` repos — see "Language packs" below).
`web/help/{en,ar,de,fa,tr}/till-designer.md` updated; screenshots
regenerated via `make docs-shots`.

Follow-ups the Architect split out of this card up front (see the issue
body's 2026-09-23 scope note): cross-category button move (#2465),
in-place catalog-item creation (#2466, `blocked:dep` on this card),
real-hardware touch verification (#2467, `blocked:env`).

## Process

BA (lane:cloud-54) verified the card's premise was stale — it was written
against #2285 as a shipped foundation, which #2339 had already replaced
with jiggle-mode — and mapped exactly what already existed vs. what
still needed building. Architect then scoped the card to the buildable
remainder and split three pieces into their own follow-up cards. UX
produced the interaction spec (popover shape, keyboard path, destructive
copy, RTL/1024×600 constraints) as this card's §7 gate. Dev (Fable)
implemented TDD-first in an isolated worktree. Tester (Sonnet, fresh
context) independently re-verified the diff, ran the full gate, drove
the real app at 1024×600/360px/dark theme/`fa` RTL, and found and fixed
a real regression (below). Reviewer (Opus, fresh context, isolated
worktree, no visibility into Dev/Tester's own reasoning) found and fixed
one further blocker (below); all findings and verdicts are this
reviewer's own, independently reached.

## Independent review

Opus, isolated worktree, own gate run from scratch (not a re-read of
Dev's or Tester's transcripts): `gofmt -l .` clean, `go build ./...`,
`go vet ./...` clean, full `go test ./...` green, `golangci-lint run
./...` 0 issues, 18 guards from `ci.yml`'s `build` job (data-access,
i18n, kiosk-engine, page-http-error, plugin-menu-read, compliance-claims,
competitor-naming, docs-shots, help-topics, help-drift, htmx-loaded,
autofill-suppression, osk-loaded, e2e-fixtures-import, emoji-font,
migration-version-collision, price-history-sync, plugin-settings-bump)
all green, before and after its own fix. Playwright: 25 e2e specs run
for real (designer-live-replica-2174, designer-search, jiggle-mode ×3,
codeless-item-shortcut-1459, category-tabs-search-418,
categories-tab-2283, category-color-1325, stale-tile-add-1433), all
passed.

### Blocker found and fixed: a refused search-add vanished silently

`web/ui/partials/buttons_admin.html`'s search-result button posted
`/api/buttons/add` with `hx-swap="none"`. A refusal (400 text/html
fragment) is force-swapped by `app.js`'s global `htmx:beforeSwap`
handler and marked *not an error*, so `htmx:responseError` never fires
for it — with `hx-swap="none"` there was nowhere for the message to
land, and the operator saw nothing happen. On `main` before this card
the same refusal was at least swapped into the (now-retired) flat grid's
wrapper, so this was a genuine regression this branch introduced, not a
pre-existing gap.

Fixed: `hx-target="#buttons-add-error" hx-swap="innerHTML"`, and the
`hx-on` success check changed from `event.detail.successful` (htmx
reports the beforeSwap-forced 400 as `successful`) to
`event.detail.successful && event.detail.xhr.status < 400`, so a real
204 still clears/hides the dropdown and a 400 no longer wipes the
message it just swapped in. New regression test in
`e2e/tests/designer-search.spec.ts` ("a refused search-result add shows
its message in the add-error region"), confirmed red against the
pre-fix template, green after.

The template change altered the docs-shots surface hash with no pixel
change (the screenshot shows no dropdown either way); ran
`scripts/ci/update-docs-shots-surface-hash.sh` to update
`web/help/img/manifest.json` accordingly — `guard-docs-shots.sh` is
green against the fresh hash.

### TDD claims re-verified independently (revert → fail → restore → pass)

- Forced `BuildCategoryGroups` to always drop empty categories:
  `TestBuildCategoryGroupsKeepEmpty_…` and
  `TestDesignerReplica_RendersEmptyCategoryAndEditAffordances` both
  failed with the predicted errors; restored, both pass.
- Disabled the `canPerform` gate check on the new routes:
  `TestDesignerCategories_GatedOnCatalogManagement` failed (a cashier's
  reorder returned 204 instead of 403); restored, passes.
- Reverted Tester's status-bar occlusion fix: the height-cap half and the
  z-index half each reproduce a failure (the z-index-only revert
  reproduces Tester's exact claimed `FOOTER.statusbar`-intercepts error;
  the combined revert instead fails earlier, at `elementFromPoint`
  returning null, because the result is off-screen and the test doesn't
  scroll — the z-index half is directly proven, the height-cap half only
  indirectly). Restored, the full spec passes.
- The reviewer's own new regression test: fails on the pre-fix template
  (empty `#buttons-add-error`), passes after the fix.

### Confirmed clean

- **Money:** not touched by this change.
- **The two recurring bugs this codebase keeps making:** no new file
  write anywhere in the diff (so no missing `os.MkdirAll` risk); no new
  cwd-relative path (the new renderer uses the same `filepath.Join("web",
  …)` pattern the existing `/ui/buttons` route already uses, not a new
  pattern).
- **SQL:** none outside `internal/data`; every new route calls an
  existing `CatalogRepo` method (`guard-data-access.sh` green).
- **Gate coverage:** `canPerform(d, r, "catalog_management")` is on all
  four `/api/designer/categories*` routes and on `GET
  /ui/designer/buttons`; every write route also refuses on a satellite
  till with the translated message, same as `/categories`.
- **i18n:** all 4 new keys present in en/ar/fa/tr; every key the new
  templates reference exists in all 4; no hardcoded strings in the new
  `app.js` code (`guard-i18n.sh` green, 1845 keys checked).
- **RTL:** no physical `left`/`right` in new CSS; popover position uses
  `insetInlineStart`, verified it flips correctly for RTL.
- **Reuse claims verified, not just trusted:** the popover's colour
  swatches are `/categories`'s own markup verbatim
  (`catalogtypes.ItemColors`); Remove uses the identical `hx-confirm` +
  `categories.deactivate_confirm` copy `categories.html:377` uses; the
  jiggle-mode extension is additive only — every Designer-only code path
  is gated on conditions (`finder()`, `.cat-editable`/`.cat-edit-mode`)
  that never exist on the sale screen, and all three sale-screen jiggle
  specs (including the locked-cashier PIN flow) still pass unmodified.
- **UX guidelines:** existing `:root` tokens reused; the popover is a
  non-modal `<dialog>` on an admin page (no new checkout/kiosk blocker);
  a real empty state exists for categories
  (`designer.category.empty`); refusals go to an `aria-live` region;
  long-locale-safe (popover width capped, strip wraps in edit mode).
- **Manual (#324):** all 5 locale help topics updated with matching
  prose; regenerated screenshots opened and looked at (English PNG shows
  the live replica with tabs, tiles, pencil affordance and the search box
  below — plausible, matches the shipped UI); help-topics/help-drift
  guards green.
- **Test/seed data:** generic names only ("Replica2174 …", "Sparkling
  Water", "Zephyr Tile"); no real shop name, no literal secret.
- Two e2e specs deleted (`designer-reorder-1221`,
  `designer-reorder-buttons-overflow-1354`) covered only the now-retired
  flat-grid markup — correct to remove, not a coverage loss (superseded
  by `designer-live-replica-2174.spec.ts`'s broader coverage).

## Deferred — filed as their own Backlog cards, not fixed here

Real findings, each out of this card's scope (per `BUILD-CYCLE.md`'s
"bigger once scoped → split into sub-cards", applied here to fixes found
during review rather than widening this PR):

- **ut-docs#2482** — category reorder only persists the active
  categories shown in the edit-mode strip; an inactive category keeps
  its old `sort_order`, which can now tie with a renumbered active one.
- **ut-docs#2483** — `/designer`'s new category routes gate on
  `catalog_management`, `/categories` gates on `settings`; invisible
  under the default roles, a real gap for a custom role. Needs a
  product-owner call — filed `needs-info`/Admin Review.
- **ut-docs#2484** — a tile's "edit in catalog" badge always returns to
  `/` (correct on the sale screen), so editing from `/designer` now
  drops the operator on the sale screen instead of back on `/designer`.

## Nitpicks (no action)

- `designer.long_press_hint` (`en.json`) is now unused — harmless; the
  de/es packs will carry a stale copy of it and of the two other removed
  keys until their own follow-up PRs land.
- The replica's height cap (`min(44dvh, 22rem)`) leaves ~1.3 tile rows
  visible at 1024×600 by design; drag edge-scroll on the replica's
  scroll container already works (`app.js:2187`), so this is a documented
  trade-off, not a bug.
- `err == data.ErrX` direct comparison (not `errors.Is`) mirrors
  `categories_page.go`'s existing convention; fine today since the repo
  layer returns these errors unwrapped.

## Language packs

4 brand-new `en.json` keys (`designer.replica_hint`,
`designer.edit_mode.toggle`, `designer.edit_mode.title`,
`designer.category.empty`) — per `CLAUDE.md`, core merges first
(`main` goes red on `lang-pack-drift`, expected for a brand-new key),
then `ut-plugin-language-{de,es}` follow-up PRs land in this same cycle.

## Verdict

Safe to merge. PR references `Closes universaltill/ut-docs#2174`.
