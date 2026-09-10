# Code review: modifier-group CRUD relocated out of the item panel (ut-docs#1957)

**Date:** 2026-09-10
**Card:** ut-docs#1957 — modifier-group/option CRUD sat undifferentiated
under the Variants grid in the catalog item's edit panel; a product-owner
review found the placement confusing ("I saw the customization groups under
the variants").
**Author:** Dev + Tester phase, this pipeline (Sonnet, `complexity:medium`)
**Reviewer:** independent Opus review, isolated worktree
(`review/1957-verify`) — per `scrum-master`'s "Model routing by complexity"

## What shipped

Modifier-group/option CRUD moved out of the catalog item's edit panel
(`web/ui/partials/catalog_variants.html`) into two surfaces that share one
extracted partial (`web/ui/partials/modifier_group_admin.html`):

- **`/modifiers`**, upgraded from the read-only browse screen ut-docs#1899
  built into the shop-wide **CRUD home**, one instance of the partial per
  item, grouped by item. Backed by a new
  `ModifierRepo.ListAllShopModifierGroups` — the admin counterpart of
  `ListShopModifierGroups`, including deactivated groups/options so a
  manager can reactivate one, exactly the reason `ListAllGroupsForItem`
  already exists beside `ListGroupsForItem`.
- **A nested `<dialog id="modifier-groups-modal">`**, opened from a "Manage
  customization groups" button in the item form, lazy-loaded by
  `GET /api/catalog/modifier-groups-panel`. The item panel itself now keeps
  only a compact, read-only, active-only summary of group names.

The item panel's own dialog is never navigated away from. This was an
explicit UX-mandated correction to the original design: the kiosk build is
fully chromeless, so a `target="_blank"` / `window.open` would either
silently no-op or navigate in place and lose unsaved item-form edits.

`/api/catalog/modifier-group` and `/api/catalog/modifier-option` keep their
create/update logic **unchanged and shared**; only the terminal render call
became context-aware (`renderModifierMutationResult`), reading the request's
own `Hx-Target` header to pick between `#modifiers-list`,
`#modifier-groups-modal-list`, and the original `#catalog-variants`
fallback.

Tester additionally found and fixed a real regression Dev's own verification
missed: two Playwright tests in
`e2e/tests/osk-decimal-sale-catalog-fields-1284.spec.ts` drove the old
inline `.modifier-admin-group-new` markup directly and time out against the
new location.

## Findings

### 1. Nested dialog opens on a *failed* fetch, showing another item's groups — Medium, FIXED

`web/ui/partials/catalog_variants.html` opened the dialog from
`hx-on::after-request="document.getElementById('modifier-groups-modal').show()"`.
`htmx:afterRequest` fires on **any** completed request, success or not, and
htmx does not swap on a non-2xx. So when the lazy-load GET fails — the
handler's own `LogAndLocalizedError` 500 path, or a dropped LAN connection,
which is a live concern in an offline-first product — the dialog still
opens, displaying whatever `#modifier-groups-modal-list` held from the last
time it was opened: **a different item's groups, with that item's `itemId`
in every form's hidden field.** Editing there silently mutates the wrong
item.

This is the same item-scoping risk the new Go test guards on the server
side, reached through a client-side path that test cannot see. It is also
the only `hx-on::after-request` in the repo that opens or closes a dialog
*without* gating on success — `self_order_cart.html:50`,
`modifier_picker.html:4`, `self_order_modifier_picker.html:4` and
`table_picker.html:53` all already use `if (event.detail.successful)`.

Fixed by applying that same established gate. Re-verified by re-running the
two Playwright tests that click this exact button against a freshly
restarted server (both pass).

### 2. Item-panel modifiers summary lost its top spacing — Low, FIXED

The summary block was given brand-new classes
(`catalog-detail-modifiers-summary`, `catalog-modifiers-summary-line`) with
no CSS rule anywhere in the repo. The block it replaced carried
`.catalog-detail-modifiers`, which supplies `margin-block-start: 1rem`
(`web/public/app.css:573`), so the summary now butted straight against the
variants grid above it.

Fixed with a rule for the summary's own class rather than by widening the
existing selector — that one still legitimately styles the real CRUD block,
which now renders from `modifier_group_admin.html`.

### 3. Spurious binary churn in the manual's screenshots — Low, FIXED incidentally

The WIP commit carried 1–2 byte diffs to `web/help/img/{ar,en}/sell.png`,
unrelated to this change (PNG encoder nondeterminism). Re-running
`make docs-shots` after fixes 1 and 2 produced files byte-identical to
`main`, so both binaries dropped out of the diff entirely (21 files → 19).
Only `manifest.json`'s `surface_sha256` and the `catalog.en` topic hash
remain. `catalog.png` legitimately needed no regeneration — that screenshot
captures the list view, not an open item-detail panel.

### 4. Two locale keys orphaned by this change — Low, NOT fixed (Backlog card)

`modifiers.optional` and `modifiers.no_options` were used only by the old
read-only browse markup in `web/ui/pages/modifiers.html`. After the rewrite
they are referenced nowhere in the repo, but remain in all four locale
files. `guard-i18n.sh` only fails on missing/drifting keys, so CI is clean.

Deliberately **not** deleted here: `lang-pack-drift` is *blocking on push to
`main`* and compares core's `en.json` against the external
`ut-plugin-language-{de,es}` packs' own `check-key-drift.sh`, which is not
readable from this sandbox. Removing keys from `en.json` could red-X `main`
in a way I cannot verify beforehand. Worth a Backlog card to remove them
alongside a coordinated pack PR.

### 5. The manual's translated `catalog` topics now describe the old flow — Medium, NOT fixed (Backlog card)

`web/help/en/catalog.md`'s "Customization options" bullet was correctly
rewritten and is accurate to the new behaviour. But
`web/help/{ar,fa,tr,de}/catalog.md` — **four** locales; `de` exists too —
all still state that customization options are created and edited from each
item's own detail panel, which this change makes false. These are genuine
translations, not English-source-of-truth stubs, so this is a real gap
against this repo's "the user manual ships with the feature, not after it"
rule.

`guard-help-drift.sh` passes because it compares *structure* (heading, step
and bullet counts), which is unchanged — this is content drift, which that
guard explicitly cannot see, and it has no slot in
`help-drift-baseline.json` either (that file records structural drift and
self-fails once the recorded counts stop matching).

Not fixed here because the self-hosted NAS translation model is confirmed
unreachable from this cloud sandbox (hit twice today on sibling PRs #982 and
#1012), and hand-inventing product prose in four languages is exactly what
this pipeline's standing policy forbids — the same constraint that made the
two new UI keys English-only. Needs a Backlog card.

## Checked and clean

- **Money.** No `money.Money` handling changed. The only monetary path
  (`priceDeltaMajor` → `priceDeltaMinor`) is pre-existing and untouched,
  still decimal-aware via `httpx.CurrencyByCode(...).Decimals` rather than a
  hardcoded `*100`. The template carries the raw minor integer only as a
  `data-minor` attribute — a DTO boundary. No new float money handling.
- **Repository pattern.** Both new queries live in
  `internal/data/modifier_repo.go`; `guard-data-access.sh` passes. The
  `includeInactive` refactor genuinely shares one implementation, and keeps
  `i.is_active = 1` mandatory in *both* variants, so a deactivated item's
  groups cannot linger on a shop-wide screen labelled with a name the
  merchant can no longer find.
- **One shared create/update path** (the Architect's explicit constraint).
  Confirmed: both POST handlers keep a single validation + repo-call body,
  and only the final render call changed. No per-branch duplication.
- **`Hx-Target` dispatch robustness.** A request with *no* `Hx-Target` at
  all — a non-htmx POST, curl, any caller predating this card — hits
  `default:` and gets the original `#catalog-variants` re-render, i.e.
  exactly the pre-change behaviour. `strings.TrimSpace` covers whitespace.
  A *spoofed* header is not a privilege escalation: both alternative
  fragments render from the same route, behind the same `requirePrimary`
  gate and the same auth middleware as before, and `modifiers-list` exposes
  only data the same user can already `GET /modifiers`. The header selects
  a fragment and nothing else.
- **Item scoping, server side.** `renderItemModifierGroupsPanel` scopes in
  SQL via `ListAllGroupsForItem(ctx, itemID)`. Proven to genuinely detect a
  cross-item leak — see the mutation evidence below. A missing `item_id`
  renders an empty panel whose add-group form then 400s; harmless,
  accepted.
- **Kiosk no-navigation.** Grepped the whole diff: no `target="_blank"`,
  `window.open`, `location.href` or `location.assign` anywhere near the new
  control. The dialog uses `.show()`, not `showModal()`, matching the
  established `#osk`-reachability fix (ut-docs#1385).
- **Z-index, read from the actual CSS** rather than the PR description:
  `.item-form-modal` = 500 (`app.css:709`), `.modifier-groups-modal` = 520
  (`app.css:742`), `#osk` = 1000 (`app.css:513`). Correctly layered above
  the item form and below the on-screen keyboard. The dialog carries both
  classes; the `.modifier-groups-modal` rules come later at equal
  specificity, so its `z-index` / `inset-block-start` / `max-width`
  overrides win, and `position: fixed` comes from `.item-form-modal`, which
  is what makes `z-index` apply at all.
- **RTL and UI conventions.** New CSS uses `inset-block-start` /
  `margin-block`; templates use `max-inline-size`. No `left`/`right`, no new
  hardcoded colors. `.btn` / `.btn secondary` reused; the dialog markup
  follows `#barcode-backfill-modal`'s existing pattern.
- **i18n.** Both new keys are present in all four locale files with
  identical English text, added in the same sorted position. No existing
  (previously translated) key's value was touched — each locale diff is
  exactly two added lines. The new inline `<script>` blocks introduce no
  user-facing strings.
- **The two recurring bug classes.** The diff writes no files at all, so a
  missing `os.MkdirAll` and a cwd-relative path where `paths.Data(...)` /
  `paths.Plugins(...)` belongs cannot apply. Confirmed by reading, not
  assumed.
- **No real client/shop names** in seed or test data ("Flat White",
  "Latte", "Extras", "Milk"), and **no secret-shaped literals**.
- **Help prose accuracy.** `web/help/en/catalog.md`'s new bullet matches the
  implementation precisely: shop-wide CRUD on `/modifiers` grouped by item,
  the item panel showing a short group-name summary plus a Manage button
  opening the same tools in a small window without losing item-form edits.

## Verified beyond the automated tests

Every claim below was produced by actually reverting production code and
re-running, in this isolated worktree — not reasoned about.

**A. Item-scoping dispatch** —
`TestModifierGroupHandler_HxTargetModifierGroupsModalList_ScopedToOneItem`.
Removed the `case "modifier-groups-modal-list":` branch from
`renderModifierMutationResult` so the modal path falls through to the old
`#catalog-variants` render:

```
modifiers_relocate_test.go:86: expected the item-scoped modal fragment
--- FAIL: TestModifierGroupHandler_HxTargetModifierGroupsModalList_ScopedToOneItem (0.01s)
```

**B. The same test genuinely detects a real leak, not just a container id.**
Restored A, then made `renderItemModifierGroupsPanel` fetch
`ListAllShopModifierGroups` (shop-wide) instead of
`ListAllGroupsForItem(ctx, itemID)` — i.e. injected the exact cross-item
leak the test claims to prevent:

```
modifiers_relocate_test.go:92: must not include itm2's group — this fragment is scoped to one item
--- FAIL: TestModifierGroupHandler_HxTargetModifierGroupsModalList_ScopedToOneItem (0.01s)
```

Restored; passes again.

**C. Item-panel summary** —
`TestCatalogVariantsPanel_ModifierSummaryShowsActiveGroupNamesCommaSeparated`.
Dropped the `"ModifierGroupNames"` key from the panel's template data:

```
modifiers_relocate_test.go:157: expected a comma-separated active group name summary, got:
    <div class="card catalog-detail" id="catalog-variants"> ... (falls back to "No customization groups yet")
```

Restored; passes again.

**D. The no-navigation assertion is a forward guard, not a TDD-verified
one** — worth stating plainly. It passes both before *and* after this diff,
because the old markup had no `target="_blank"` either; reverting production
code cannot make it fail. Validated by mutation instead: injected
`target="_blank"` into the Manage button and confirmed it is caught:

```
modifiers_relocate_test.go:160: the Manage customization groups control must never use page navigation (kiosk is chromeless)
```

Restored; passes again. The assertion is real and does its job — it just
guards a future regression rather than proving this change.

**E. Playwright e2e, run for real in this worktree** (browsers at
`/opt/pw-browsers`, `node_modules` linked from the sibling checkout, all
four till servers booted fresh on ports 8091–8094; no pre-existing server
was reused). Reverted Tester's spec fix back to the `main` version:

```
Error: locator.fill: Test timeout of 30000ms exceeded.
Call log:
  - waiting for locator('.modifier-admin-group-new input[name="name"]')
    at osk-decimal-sale-catalog-fields-1284.spec.ts:284:72
1 failed
  › catalog_variants.html EXISTING modifier-option price-delta survives OSK typing
```

This confirms the regression Tester caught was genuine and that the fix is
what resolves it. Restored: all 8 tests in that spec pass (37.3s), including
after my own fix 1 changed the very button they click.

## Gate

Run in full by the reviewer, not taken from the PR description. All clean
after the fixes:

| Check | Result |
| --- | --- |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `gofmt -l .` | pass (no output) |
| `go test ./...` (full suite) | pass |
| `guard-i18n.sh` | pass — 1596 template keys resolve, all locales match `en.json` |
| `guard-data-access.sh` | pass |
| `guard-docs-shots.sh` | pass (after regenerating) |
| `guard-help-topics.sh` | pass |
| `guard-help-drift.sh` | pass (pre-existing baselined drift only) |
| `guard-compliance-claims.sh` | pass |
| `guard-htmx-loaded.sh` | pass |
| `guard-page-http-error.sh` | pass |
| `guard-kiosk-engine.sh` | pass |
| `guard-autofill-suppression.sh` | pass |
| `guard-e2e-fixtures-import.sh` | pass |
| Playwright `osk-decimal-sale-catalog-fields-1284.spec.ts` | 8/8 pass |

## Verdict

**Safe to merge**, subject to CI. The design is sound: one genuinely shared
create/update path with only the render target made context-aware, an
unchanged fallback for every pre-existing caller, correct SQL-level item
scoping, and a kiosk-safe nested dialog that never navigates away from the
item form.

Two findings are fixed here (the failed-fetch dialog leak, the lost summary
spacing) plus incidental removal of unrelated binary churn. Two are real but
deliberately deferred with reasons — the orphaned locale keys and, more
importantly, the four translated `catalog` help topics that now describe the
superseded flow. Neither blocks this merge; both deserve Backlog cards, the
help-topic one the more pressing of the two, since the manual is part of the
feature.
