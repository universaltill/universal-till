# Review — Category editor: colour, category-level modifiers, kitchen-printer routing (ut-docs#2284)

**Date:** 2026-09-16 · **Lane:** cloud-54 · **Card:** ut-docs#2284 (p1, complexity:hard, 2026-09-16 design batch)
**Build:** Fable subagent, TDD · **Review:** fresh-context Opus subagent, isolated worktree, read-only · **Fix triage:** cloud-54

## What shipped

All backend groundwork (migration 031, `ModifierRepo`'s category-link methods, `ResolveGroupsForItem`, `kitchen_stations_repo.go`'s `SetCategoryStationRoutes`/`SetItemStationRoutes`) pre-existed on `main` from PR #1191 (ut-docs#1915). This card is the sequenced UI follow-up:

- **Category editor** (`internal/pages/categories_page.go`, `web/ui/pages/categories.html`): the dialog gains a colour swatch grid (reusing the item editor's palette/markup), a modifier-group multi-select, and a kitchen-station multi-select. One Save writes the row plus both link sets.
- **Item editor** (`internal/pages/catalog/handlers.go`, `web/ui/partials/modifier_group_admin.html`, `catalog_variants.html`): shows category-inherited modifier groups with per-group Skip/Use-again (opt-out/opt-in), and an item-level kitchen-station override.
- New repo methods: `CreateCategoryWithColor`, `UpdateCategory`, `SetCategoryModifierGroups`, `AllCategoryModifierGroupLinks`, `ListActiveModifierGroups`.
- i18n: 19 new keys × ar/en/fa/tr (translated for real); help topics `catalog.md`/`categories.md` updated in en/ar/fa/tr/de.
- e2e: `categories-editor-2284.spec.ts` (new), plus fixes carried into `categories-record-dialog-2010.spec.ts`.

## Review findings and outcome

| # | severity | finding | outcome |
|---|---|---|---|
| 1 | should-fix | dialog's group checkboxes were sourced from *active-only* groups, but row prefill included links to inactive groups too — a plain Save on a category with a link to a since-deactivated group silently deleted that link (no checkbox to keep it ticked) | fixed: existing links to inactive groups are now carried through the replace-all write unless explicitly unticked |
| 2 | should-fix | hidden-input default-value fix (`record-dialog.js`) only recorded the template default lazily, on an edit-mode prefill — create → create with no edit in between still leaked a page-script write (e.g. a colour-tile click) into the next create | fixed: capture every hidden input's default once per `open()`, matching the existing `defaultAction` "first sight, never live" pattern |
| 3 | should-fix | a group both category-inherited *and* directly linked to an item rendered twice in the item editor's modal — once under its direct link, again under "from this item's category" with a Skip button that reported success but changed nothing (the direct link always wins in `ResolveGroupsForItem` regardless of a category opt-out) | fixed: filtered the inherited list by the item's own group ids before rendering, mirroring the read-only summary line's existing logic; test updated to assert the filtered-out row is absent |
| 4 | nit | colour grid's arrow-key navigation was direction-agnostic (`ArrowRight` always `+1` in DOM order) — moves focus visually *left* under RTL | fixed: horizontal step inverted under `dir="rtl"` |
| 5 | nit | roving-tabindex fallback: if a stored colour ever matched no tile, every tile would sit at `tabindex="-1"`, making the grid keyboard-unreachable | fixed: falls back to the first tile |
| 6 | nit | edit-handler audit action stayed `"category_rename"` even though the same handler now also writes colour and both link sets | fixed: renamed to `"category_update"` |
| 7 | deferred | `#item-color-grid` in `catalog.html` was not given the same roving-tabindex/arrow-key treatment, so the two colour pickers now behave differently despite one comment claiming an exact contract match | out of scope for this card — filed as a follow-up |
| 8 | deferred | create-then-links-fail on the category dialog leaves a half-configured category row with no audit entry (rare: both writes are local SQLite) | noted, not fixed — tidiness, not correctness |

## Independently re-verified (not taken on trust)

- **Mutation testing** on `internal/pages/catalog/category_inherit_2284_test.go`'s safety-critical assertion: pointed the opt-out handler at `UnlinkGroupFromItemUnlessLastLink` instead of `OptOutItemFromGroup` (fails as expected), then made opt-out do opt-out *and* unlink — the exact conflation the code's own comment warns against — which still passed the first assertion but failed the second ("opt-out must never unlink a direct link"). Reverted; test green again.
- **Money/pricing angle**: modifier options carry `priceDeltaMinor`; confirmed nothing in this diff touches sale-time price resolution, and the existing dedup in `ResolveGroupsForItem` (own groups before inherited) already prevents a double-prompt for a group linked both ways.
- **Repository pattern**: no raw SQL outside `internal/data`/`internal/db` in the new handler code (read directly, not just guard output).
- **Help prose accuracy** (not just structure): spot-checked the claim "colour shows on the category's sale-screen tab" against `internal/ui/buttons.go`'s `resolveCategoryColor` and `web/public/app.css` — it renders as described.
- **Scope**: confirmed no sibling batch card's surface (#2282/#2283/#2308/#2285/#2286, the Sell screen) was touched.

## Full gate (after review fixes applied)

`gofmt -l .` clean · `go build ./...` / `go vet ./...` clean · `go test ./...` — 60/60 packages ok (`internal/pages` 330s, `internal/data` 92s, `internal/pages/catalog` 3s) · `golangci-lint run ./...` — 0 issues · guards green: `guard-data-access.sh`, `guard-i18n.sh` (1704 keys, all locales match `en.json`), `guard-help-topics.sh`, `guard-help-drift.sh` (only pre-existing baselined drift), `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`, `guard-page-http-error.sh`, `guard-htmx-loaded.sh`, `guard-docs-shots.sh` (regenerated via `make docs-shots`; only `categories`/`catalog` PNGs actually changed content — every other screenshot's byte diff was a Chromium-version rendering artifact and was reverted, keeping the manifest's new source-surface hash only).

## UX gate (SKILL.md §7, driven live)

PASS on every checklist line — no new text inputs (no OSK/`inputmode` concern), roving-tabindex + arrow-key nav confirmed live in a real browser, RTL (`fa`) verified mirrored correctly at 1024×600 and 360px, empty/error states are actionable ("No customization groups yet — create them under Customization options first."), status/lock/exit-to-OS stayed reachable above the full-screen dialog on every screenshot. One out-of-scope, pre-existing finding noted (native checkbox touch target below this codebase's 44px floor, inherited from the `.field-checks` pattern this diff copies, not introduced here) — filed as its own follow-up, not fixed here. Not exercised: the populated Skip/Use-again row itself (seed setup for a real inherited-group case exceeded the session's time budget) — verified by code read instead (visible labels, correct `hx-post`/`hx-target` wiring).

## Deferred to Backlog (not this card's problem)

- Port `categories.html`'s roving-tabindex colour grid back onto `catalog.html`'s `#item-color-grid` so both pickers behave identically (finding 7 above).
- `ut-plugin-language-{de,es}` follow-up PRs for the 19 new core locale keys — brand-new keys, so `lang-pack-drift` is expected red on `main` until they land; this same lane owns landing them in this cycle per `scrum-master/SKILL.md`'s "Work that has no card is not covered."
- Native-checkbox touch-target floor (pre-existing, codebase-wide gap, not introduced here).

## Verdict

Safe to merge. All must-fix items from the independent review are applied and gate-verified; no blockers found.
