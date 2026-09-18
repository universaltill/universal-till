# Code review — jiggle-mode edit/remove badges lock affordance (ut-docs#2361)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2361 (`complexity:easy`, `ux`, `p3`)
- **Branch:** `feat/2361-jiggle-lock-affordance`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing, `MODEL-ROUTING.md` — a clean-context
  instance of the same model that wrote the code, never seeing the dev
  reasoning).
- **Verdict: SAFE TO MERGE.** No blocking findings; two nits fixed in a
  follow-up commit; one should-fix checked with a real screenshot and
  found not to actually manifest.

## What shipped

ut-docs#2312 gated `/api/buttons/{add,remove,reorder}` and navigating
into `/catalog` on the `catalog_management` permission. The sell screen's
jiggle-mode edit grid (ut-docs#2339) still rendered its edit/remove
badges identically for every operator — a cashier only discovered they
needed a manager PIN after already dragging a tile or tapping remove,
losing the discoverability the retired ut-docs#2285 `tile_sheet.html`
sheet had (a lock icon shown *before* acting).

- `internal/ui/buttons.go`: `ButtonVM` gains a `Locked bool` field;
  `ButtonsHTTP` gains a `Granted bool` field; a new `stampLocked` helper
  walks the `BuildCategoryGroups` tree (categorized, nested, and the
  synthetic uncategorized bucket) and sets `Locked = !granted` on every
  button. `BuildCategoryGroups`'s own signature is untouched, so its
  existing ~8 test call sites keep compiling.
- `internal/pages/buttons_api.go`: the `/ui/buttons` GET handler computes
  `granted := canPerform(d, r, "catalog_management")` and passes
  `Granted: granted` into the `ui.ButtonsHTTP{}` it constructs. The other
  two `ButtonsHTTP{}` sites (Add/Remove, rendering the Designer's own
  `buttons_admin_grid`) are untouched — that template never reads
  `.Locked`.
- `web/ui/partials/buttons.html`: `product-tile`'s badges now mirror the
  already-reviewed `tile_sheet.html` `.Locked` convention exactly — a
  `locked` class, a lock-dot overlay, an aria-label/title suffix using
  the existing `tile_sheet.locked` key, and no client-side `hx-confirm`
  on the locked remove badge (the real elevation prompt is the
  confirmation).
- `web/public/app.css`: a small `.tile-badge-lock` overlay badge at the
  corner of the existing `.tile-badge-dot`.
- `web/help/en/sell.md`: documents the new affordance in the existing
  "Rearranging the quick buttons" section, linking `/help/elevation`.
- `web/help/img/manifest.json`: regenerated via `make docs-shots` —
  zero PNG pixel changes (the lock only renders for a non-manager
  session, a state the docs-shots harness doesn't capture).
- New test `TestButtonsPartial_JiggleModeLockAffordance`
  (`internal/pages/buttons_api_catalog_management_gate_test.go`), plus a
  `getWithUser` test helper.

## Independent review — what was run

Build, `go vet`, targeted `go test -v` (`Button|Jiggle|CatalogManagement`
across `internal/ui`/`internal/pages`), `gofmt -l`, `golangci-lint run`,
and `guard-i18n.sh`/`guard-compliance-claims.sh`/`guard-kiosk-engine.sh`/
`guard-data-access.sh`/`guard-help-topics.sh`/`guard-help-drift.sh` all
passed. The reviewer also proved the new test non-vacuous by reverting
just the template change in a scratch copy and confirming
`TestButtonsPartial_JiggleModeLockAffordance` fails as expected.

Confirmed by reading the code: `canPerform` short-circuits to `true`
under `UT_AUTH=off`, so every pre-existing `UT_AUTH=off`-based test
(including `TestButtonsPartial_JiggleModeMarkup`'s exact-string
assertions with no `locked` suffix) renders unchanged.

## Findings and resolution

1. **Should-fix — lock overlay might visually clip the pencil/trash icon**
   (CSS math: the 14px lock badge's coordinate overlap with the parent
   22px dot reaches ~6.5px into the icon's own bounding box). Checked
   directly rather than argued: rendered the real `/ui/buttons` output
   for both a cashier and a manager session through the real
   `web/public/app.css`, and screenshotted the jiggle-mode grid with
   Playwright/Chromium (a `chromium-1194` binary already on disk) at 4x
   device scale. **Result: no clipping.** The pencil and trash glyphs
   stay fully legible; the lock renders as a clearly separate corner
   accent, because the icons don't fill their square bounding boxes into
   the corners and the badges themselves are circular. No CSS change
   needed. (Ad-hoc screenshot script and captured images were scratch
   files under `/tmp`, deleted after inspection — not part of this
   diff.)
2. **Nit — misleading CSS comment**, fixed: the comment claimed
   `.tile-badge-remove .tile-badge-dot { color: var(--danger) }` "only
   targets the FIRST `.tile-badge-dot`" — false, it's a descendant
   selector and matches the lock span too (which also carries the
   `tile-badge-dot` class). The lock still renders in `var(--warning)`
   because `.tile-badge-lock svg` targets the `<svg>` element directly,
   and a direct match always beats an inherited value regardless of the
   inherited rule's own specificity. Comment corrected to state the real
   reason.
3. **Nit — no test for `stampLocked`'s recursion into nested categories**,
   fixed: added `TestStampLocked_RecursesIntoNestedCategoriesAndUncategorized`
   in `internal/ui/buttons_category_groups_test.go`, covering a nested
   category (mirroring `TestBuildCategoryGroups_NestsByParentID`'s own
   fixture) and the uncategorized bucket, for both `granted=true` and
   `granted=false`.

## Verified beyond automated tests

- Real Playwright/Chromium screenshot of the actual rendered template +
  real `app.css`, both roles (see finding 1 above) — not just reasoning
  about CSS specificity/geometry.
- `git diff -- web/help/img/` confirmed only `manifest.json` changed, no
  `.png` files, so `guard-docs-shots.sh`'s concern was a false alarm for
  this diff.
- Full `go build ./... && go test ./... -race` run once across the whole
  repo: green except a pre-existing, unrelated `internal/plugins`
  timeout/flake under `-race` (already the subject of several earlier
  review records: `2026-09-12-plugins-race-timeout-margin-2156.md`,
  `2026-09-12-race-hang-newrealdbdeps-testdb-template-2191.md`, and
  others) — confirmed unrelated by inspection (this diff touches
  `internal/ui`/`internal/pages`/`web/` only).

## Deferred / not this ticket's scope

None — both nits were cheap enough to fold in directly.
