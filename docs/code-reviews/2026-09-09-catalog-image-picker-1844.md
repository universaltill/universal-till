# Code review: catalog built-in image picker

**Date:** 2026-09-09
**Card:** ut-docs#1844
**Author:** scrum-master pipeline (cloud cycle, lane:cloud-54), on behalf of Farshid Mirza

## What shipped

The catalog add/edit item flow can now assign an item's thumbnail from the
5 existing bundled category icons (`coffee`, `drink`, `sandwich`, `pastry`,
`generic` — the same set `catimport.PlaceholderIcon` already picks from for
imageless imports, ut-docs#1189), not only via an uploaded photo. Both
paths write the same `item_images`/`role=thumbnail` row, so every reader
that already resolves through `item_images` (POS grid, basket, search,
suggestions) needs no change to pick either up.

- `internal/catimport/placeholder.go`: exported `IconPath(icon) (string,
  bool)` (was unexported `iconPath`), added `BuiltinIcon` + `BuiltinIcons()`.
- `internal/data/catalog_repo.go`: added `ItemThumbnailPath` (current path +
  whether one exists) and `ClearItemThumbnail`.
- `internal/pages/catalog/handlers.go`: `GET /api/catalog/item/icon-state`
  (selected/custom/suggested three-way read) and `POST
  /api/catalog/item/icon` (choose or clear).
- `web/ui/pages/catalog.html` / `web/public/app.css`: a `.picker-grid` of
  icon tiles inside the existing "Item image" panel, reusing the same
  touch-first tile pattern `setup.html`'s country/shop-type choosers use.
- `web/locales/{en,ar,fa,tr}.json` (11 keys) and the manual
  (`web/help/*/catalog.md`), screenshots regenerated.
- Split out of scope, filed separately: expanding the icon set beyond 5
  (ut-docs#1862, a licensing/curation task the card's own body called out
  as too large for this pass).

## Independent review

Fresh-context **Opus** subagent (this card is `complexity:medium` — Dev at
Sonnet, review at Opus, per `scrum-master`'s model-routing table), isolated
in its own worktree. It built and tested the diff for real, ran every
CI-blocking guard, independently re-verified the TDD claims by reverting
two separate production changes and confirming the corresponding tests
failed with real assertion errors (not compile errors) before restoring,
and confirmed the two believed-unrelated screenshot diffs
(`web/help/img/ar/{sell,till-designer}.png`) are pre-existing PNG-encoder
nondeterminism by checking `git log --follow` shows them churning on every
prior regen, unrelated to this diff.

**Verdict: FAIL on the first pass** — 1 blocker, 3 should-fix, 5 nits.

### Blocker — fixed

**F1 — choosing/clearing a built-in icon didn't remove a superseded
uploaded photo FILE, only the DB row.** Three surfaces resolve an item's
photo by the `items/<id>/thumb.png` path *convention* rather than through
`item_images`: `catalog_row.html`/`catalog_variants.html`'s `imgExists`
check, and `self_order_shop.go`'s hardcoded `ImageURL` (that file's own
comment already documents this same split for ut-docs#1189's placeholder
icons). Without removing the file, an item with an uploaded photo that
then got a built-in icon would show the OLD photo on those three surfaces
and the NEW icon everywhere else — visibly inconsistent, and directly
contradicting this same PR's own manual claim ("an item only ever shows
one or the other").

Fixed: `removeUploadedThumbnail` best-effort-deletes the conventional
upload path after every choose/clear. This makes `item_id` reach a
filesystem path on this route for the first time, so the reviewer's own
follow-up note applied too: added the same path-traversal guard
(`strings.ContainsAny(itemID, "/\\.")`) the sibling upload handler already
has. Four new tests: removes-on-choose, removes-on-clear, no-file-is-not-
an-error, rejects-path-traversal-item-id. TDD-verified myself (reverted the
fix, confirmed `TestChooseIcon_RemovesUploadedThumbnailFile` fails with a
real "file still exists" assertion, restored, confirmed it passes).

### Should-fix — fixed

**F2 — the `#1842` visibility caveat was English-only.** The `ar`/`fa`/`tr`
manual paragraphs omitted the final sentence disclosing that the Catalog
list may still show a plain tile. Translated into all three.

**F3 — manual overclaimed self-order support.** Self-order resolves a
photo by the same hardcoded convention F1 describes, never through
`item_images` — so it does NOT show a built-in icon at all, and (after
F1's fix) shows a blank tile rather than a stale photo once one is chosen.
Corrected the claim in all four locales rather than widening this PR to
fix `self_order_shop.go` itself; filed as its own card,
**ut-docs#1870**.

**F4 — `is_custom` was computed, tested, and never read; "none" could
never show pressed; the suggestion outline reappeared right after an
explicit clear with no other feedback.** Reworked
`setBuiltinIconTiles`/`refreshBuiltinIconState` (`catalog.html`) into three
mutually-exclusive states: a real uploaded photo (no built-in tile
pressed, an explicit "current image is an uploaded photo" notice — new key
`catalog.builtin_icon.custom_active`, all four core locales + the `de`/`es`
packs), a specific built-in key (that tile pressed), or truly no image at
all (the **"No image" tile itself now shows pressed**, which resolves the
"reappears with no feedback" complaint: the operator sees a definite
"empty, selected" state, and the suggestion dashed marker is an
independent, coexisting hint on a *different* tile, not a contradiction of
it). Also wired `refreshBuiltinIconState` into the existing photo-upload
success path, which previously left the icon grid showing stale state
until the next row click.

### Nits — fixed

**F5 — the suggestion's `outline` and `.picker-tile:focus-visible`'s own
`outline` had identical specificity, so a suggested tile's dashed ring won
regardless of focus, hiding the focus indicator.** Moved the suggestion
marker to `box-shadow` (a different property, can't collide) with
`border-style: dashed` for the visual cue.

**F6 — the only tile-label text was a hover-only `title`,** which
`reference/ux-guidelines.md` explicitly rules out for a touchscreen till.
Added a visible `<span>` label under each icon; `alt` emptied to avoid a
duplicate screen-reader announcement now that the visible text is the
button's accessible name.

**F7 — tile `min-block-size` (2.6rem) undershot the ~44px touch-target
convention.** Resolved as a side effect of F6's visible label needing more
vertical room (bumped to a flex column layout, `3.6rem` minimum).

### Nits — deferred, filed as new cards

**F8** (`item_images` has no `UNIQUE(item_id, role)`, so
`SetItemThumbnail`'s UPDATE-then-INSERT can race into duplicate rows under
concurrent writes) — pre-existing, not introduced by this diff; narrow
(SQLite serializes writers) but cheap to close. Filed as **ut-docs#1871**.

**F9** (lang-pack-drift: 11 new `en.json` keys need `ut-plugin-language-
{de,es}` translations before `main` can stay green) — not deferred, just
sequenced: the two pack PRs (universaltill/ut-plugin-language-de#205,
universaltill/ut-plugin-language-es#206) were opened the same cycle,
before this PR, per the ecosystem's "the lane that merges the core change
owns the implied follow-up" rule. Their `key-drift` check is red right now
because it compares against `main`, which doesn't have these keys yet —
resolves once this PR merges; see those PRs' own comments.

## Verification performed (beyond automated tests)

- Full gate on the final diff: `gofmt -l .` clean, `go build ./...`,
  `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test ./...`
  (all green — no flake this run), every guard listed in this repo's
  `CLAUDE.md` (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-page-http-error`, `guard-i18n`,
  `guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`,
  `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`,
  `guard-e2e-fixtures-import`, `check-brand-assets`,
  `guard-makefile-version`).
- **Real driven run, twice** (before and after the review's fixes), a
  built binary against a throwaway SQLite data dir, driven with Playwright
  against the pre-installed Chromium — not just rendered-HTML assertions:
  - Confirmed the suggestion (dashed) vs. selection (solid) visual
    distinction for a genuinely imageless item (name "Fresh Croissant" →
    suggests `pastry`, nothing else pressed pre-fix / "No image" pressed
    post-fix).
  - Confirmed persistence survives a full page reload (not just in-memory
    JS state) — chose `coffee`, reloaded, still showed pressed.
  - **Post-fix**, with a real uploaded photo (an actual multipart upload,
    a real file on disk): confirmed the "current image is an uploaded
    photo" notice, confirmed choosing `coffee` afterward both updates
    `item_images` AND deletes the uploaded file from disk (`ls` on the
    item's asset directory came back empty), confirmed clearing shows
    "No image" pressed with a suggestion simultaneously visible on a
    different tile, confirmed the visible text labels render
    ("Coffee"/"Drink"/"Sandwich"/"Pastry"/"Generic").
  - RTL (`fa`): confirmed the whole page mirrors correctly; the picker's
    own CSS additions contain no literal `left`/`right`/`margin-left`/
    `margin-right`, confirmed by grepping the diff, so RTL correctness
    holds by construction, not just by this one screenshot.
  - **Not checked**: an installed dark-theme plugin specifically (the
    `?theme=dark` query param used in one drive attempt didn't actually
    switch anything, and chasing a real theme-plugin install was
    disproportionate to this card) — accepted, real gap: the new CSS reuses
    the same `var(--accent)`/`var(--surface)`/`var(--border)` tokens
    `.picker-tile` already uses elsewhere across themes, so risk is low,
    but this is explicitly unverified, not silently assumed fine.
- Manual topic (`web/help/{en,ar,fa,tr}/catalog.md`) updated in the same
  branch per the standing product-owner instruction; screenshots
  regenerated (`make docs-shots`) — the picker itself lives inside a
  collapsed `<details>` panel, so the default catalog screenshot is
  byte-identical (nothing visible changed in the collapsed state); this
  was verified to be the reason, not an oversight.
- No real client/shop name used anywhere (seed data throughout this
  review used "Cappuccino"/"Fresh Croissant"/"Task Runner"-style generic
  names); no secret-shaped value anywhere in the diff.

## Safe-to-merge verdict

**Yes**, after the fixes above. All of F1-F7 addressed and re-verified
(TDD re-verification on F1, a fresh driven run on F1/F4/F5/F6/F7
together); F8/F9 are legitimate, tracked, non-blocking follow-ups per the
reasoning above.

## Explicitly deferred / out of scope

- Expanding the built-in icon set beyond 5 — ut-docs#1862.
- Self-order kiosk resolving photos through `item_images` instead of a
  hardcoded path convention — ut-docs#1870.
- `item_images` unique-constraint hardening — ut-docs#1871.
- Dark-theme-plugin-specific visual verification — noted above as a real,
  accepted gap, not silently skipped.
