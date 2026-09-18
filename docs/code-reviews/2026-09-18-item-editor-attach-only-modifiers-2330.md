# Code review — item-editor Manage Modifiers dialog goes attach/detach-only (ut-docs#2330)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2330 (`complexity:medium`, `ux`) — split out of ut-docs#2211
  per the product owner's 2026-09-16 request.
- **Branch:** `feat/2330-item-editor-attach-only-modifiers`
- **Reviewer:** independent pass, Opus subagent working from a scratchpad copy
  of the diff (never saw the implementation reasoning), per this card's
  `complexity:medium` routing (Dev at Sonnet, Review at Opus).
- **Verdict: SAFE TO MERGE** after fixing 6 of the review's 8 findings
  (2 nits accepted as-is, reasoned below) — **and after fixing a severe
  regression CI itself caught** (see "What CI caught" below): removing
  the item-editor's create-group form with no replacement anywhere broke
  creating a shop's very first modifier group entirely, for any item.

## What CI caught (post-review, pre-merge)

The PR's own `playwright` check failed on push: two pre-existing tests in
`osk-decimal-sale-catalog-fields-1284.spec.ts` timed out waiting for
`.modifier-admin-group-new input[name="name"]` inside the item-editor's
now-attach-only dialog. Investigating showed this was not a test-only
problem — it was real product breakage this card introduced and neither
the independent review nor this session's own earlier verification
caught, because both exercised the item-scoped dialog and `/modifiers`
in isolation, never the specific case of an item with **zero** modifier
groups anywhere in the shop:

- The item-editor's nested dialog was, before this card, the **only**
  place a completely new modifier group ever got created — its "add new
  group" form rendered unconditionally (`ModifierGroups` could be empty),
  for any item, groups-elsewhere-in-the-shop or not.
- `/modifiers` (`renderModifiersList`/`modifiers_list`) only ever lists
  items that **already** appear in `ListAllShopModifierGroups`' result —
  confirmed by reading `modifiers.html`'s own template and its prior
  empty-state copy, `"No Modifiers yet. Create one from an item's Catalog
  entry."`, which was explicitly pointing at the very form this card
  removed.
- So after `AttachOnly`, a shop's first-ever group — or any additional
  item's first group, if that exact group didn't already exist somewhere
  to attach — had **no creation path anywhere in the product**. This
  fails the card's own acceptance criterion ("`/modifiers` remains the
  only place to create... groups") at the literal level: `/modifiers`
  could create an *additional* group for an item that already had one,
  but never a first one for anything else.

**Fix:** `/modifiers` gained a real, always-visible "create a new group"
form with an item picker (any active catalog item, not just ones already
in `.Groups`) — same id/name/sku `<datalist>` + hidden-field-resolve
mechanics as `inventory.html`'s own stock-item picker (ut-docs#2011),
reusing the *existing* `/api/catalog/modifier-group` endpoint unchanged.
`handlers.go` gained `modifierItemPickerJSON` (mirrors
`inventory_page.go`'s `pickerItem`/`ItemsJSON`), wired into both the
page's own GET and the `#modifiers-list` mutation re-render. The
misleading empty-state copy was corrected in all four core locales
(no new keys — same `inventory.item_placeholder`/`catalog.modifiers.*`
strings already in use elsewhere on this page). The two e2e tests that
caught this were updated to create their probe group via `/modifiers`'
new picker instead of the item-editor dialog, matching the actual new
workflow, and re-verified passing along with the full `osk-decimal-*`
suite (8/8) and every other e2e file touching this area (26/26 across 5
files). Full gate (build/vet/test/lint/all guards) re-run clean after
this fix; docs-shots regenerated again (`/modifiers`'s own screenshot
came out byte-identical — it isn't one of the 31 routed manual topics,
only the surface hash needed refreshing).

This is exactly the kind of gap a review of a diff in isolation can miss
and a real end-to-end test catches — worth recording plainly rather than
folding quietly into the fixes list above.

## The change

The item-editor's nested "Manage Modifiers" dialog reused the exact same
full-CRUD partial (`modifier_group_admin.html`) as the shop-wide
`/modifiers` page — an operator could rename groups, edit options, and
create brand-new groups from inside an item's own edit screen. Per the
product owner's request, that surface should only let an operator pick
which *existing* groups apply to this item; all group/option authoring
stays exclusively on `/modifiers`.

- `internal/pages/catalog/handlers.go`: new `AttachOnly bool` field on
  `modifierAdminItem`, set `true` only in `renderItemModifierGroupsPanel`
  (the item-scoped dialog); `/modifiers`' own `groupModifierAdminByItem`
  path leaves it at its `false` zero value, so that page is untouched.
  `/api/catalog/modifier-group/attach` now reads `r.Form["groupId"]`
  (every value, not just the first) so one submission can attach several
  groups at once, still validated against `ListAttachableModifierGroups`
  and still using `NextGroupSortOrderForItem`'s own re-read-per-insert
  pattern so each attached group gets its own increasing `sort_order`.
- `web/ui/partials/modifier_group_admin.html`: branches on `.AttachOnly`.
  True → each linked group is a read-only row (name + active-option names
  + a Detach button), the "add new group" form is gone entirely, and the
  attach control is a checkbox list (not a native `<select multiple>` —
  those are poorly supported on touch) inside a `<fieldset>`/`<legend>`,
  one submit attaching everything checked. False → byte-identical to
  before.
- `web/public/app.css`: touch-target-sized checkbox rows, a
  `.modifier-admin-group-name` flex rule, a fieldset/legend reset, and a
  scroll container for `#modifier-groups-modal-list` (this dialog had
  **no** scroll container at all before this card — harmless while the
  attach control was a collapsed `<select>`, a real gap now that the
  checkbox list is always expanded and can make the dialog taller than
  its own 86vh cap).
- Zero new i18n keys — reuses `catalog.modifiers.attach`/
  `attach_placeholder`/`detach`/`summary_none` verbatim, so no
  `ut-plugin-language-{de,es}` follow-up is owed this cycle.
- `data-group-id="{{ .ID }}"` added to the common `.modifier-admin-group`
  wrapper (both render modes) so tests read a group's id off a stable
  attribute instead of depending on which mode is active — two existing
  e2e helpers (`sell-screen-categories-tab-2283.spec.ts`,
  `order-type-prompt-placement-2282.spec.ts`) that scraped a
  `name="id"` hidden input preceding the name text were updated to use it,
  since that input no longer exists in attach-only mode.
- New e2e spec `catalog-item-editor-attach-only-2330.spec.ts`: drives the
  real dialog — asserts no CRUD form/input is reachable, checks a real
  checkbox, submits, confirms the group is now a read-only row, detaches
  it (accepting the native `confirm()`), confirms it's offered as a
  checkbox again.
- `web/help/en/catalog.md` rewritten to describe the new flow;
  `de`/`ar`/`fa`/`tr` translated to match (see "What the review found →
  fixed" below).

## What the review found — fixed

1. **Blocker-class UX bug: submitting the attach form with nothing
   checked failed completely silently.** The checkbox list has no
   client-side `required` (there's no single-attribute HTML equivalent of
   "at least one of these boxes"), so an empty submit hit
   `http.Error(...400)` — `text/plain`, which `app.js`'s
   `htmx:beforeSwap` never force-swaps in. Fixed: that path now goes
   through `renderModifierMutationResult` with a `Notice`
   (`catalog.error.invalid_request`, an existing key), the same pattern
   every other refusal in this handler already uses.
2. **The empty-state copy actively lied in attach-only mode.**
   `catalog.modifiers.none` ("No Modifiers yet — add a group below…")
   still told the operator to do something the dialog no longer offers.
   Fixed: attach-only mode now shows `catalog.modifiers.summary_none`
   ("No Modifiers yet.") instead — an existing key, no new string needed.
3. **Deactivated options were shown as if sellable, indistinguishably
   from active ones.** The read-only options list rendered
   `ListAllGroupsForItem`'s full set (inactive included, by design — the
   full-CRUD branch needs that to offer reactivation), but the read-only
   branch has no Active checkbox to signal that. Fixed: filtered to
   active options only in the read-only listing, matching
   `InheritedGroups`' own options summary a few lines down the same file.
4. **Four translated manuals now described removed functionality.**
   Only `en/catalog.md` was updated; `de`/`ar`/`fa`/`tr` still said the
   item-editor's window "opens the same create/edit tools" and offered a
   single-group **Attach existing group** *list* rather than a
   multi-select. Fixed: translated the same two bullets into all four,
   preserving heading/bullet/bold-leadin counts (`guard-help-drift.sh`
   still reports the identical, already-tracked ut-docs#329/#2119/#2284
   baseline gap for this topic — confirmed structurally unchanged, not
   newly introduced).
5. **`.modifier-admin-group-name` had no CSS rule at all**, despite a
   comment claiming parity with `.modifier-inherited-name` — it fell back
   to flex's `0 1 auto` default next to a `2 1 12rem` sibling, so the name
   shrank to content, lost its bold weight, and didn't align with the
   inherited-groups rows directly below it. Fixed: added the same
   `flex: 1 1 10rem; min-inline-size: 8rem; font-weight: 600` as
   `.modifier-inherited-name`.
6. **A partly-stale multi-select attached what it could with no
   explanation**, and `category_inherit_2284_test.go`'s rewritten
   assertion (`>Extras<`) was looser than its two siblings' class-qualified
   form (also matches an unrelated `.modifier-inherited-name` span).
   Fixed both: a `Notice` now surfaces when some but not all submitted
   ids were valid (reusing `catalog.error.invalid_request` again — no new
   key), and the test assertion was tightened to match its siblings.
7. **The checkbox list had no shared context for assistive tech** — a
   bare `<div>` label above a run of checkboxes, no `fieldset`/`legend`
   or ARIA grouping. Fixed with plain `<fieldset>`/`<legend>` (CSS reset
   to remove the browser's default border/padding) — no new locale string
   needed, reuses the same label text.

## What the review found — accepted as-is (not changed)

- **Multi-attach's per-group `LinkGroupToItem` calls aren't wrapped in one
  `*sql.Tx`**, so a DB error partway through a multi-select submission
  could leave some groups attached and the request still answering 400.
  Accepted: `LinkGroupToItem` realistically only fails on a genuine DB
  fault (the id-level validation that actually varies request-to-request
  already runs up front, against `ListAttachableModifierGroups`), the
  re-rendered panel always reflects the true end state on the (overwhelmingly
  common) success path, and wrapping a handler already built around
  `*sql.DB` in a hand-rolled transaction for a not-yet-observed failure
  mode is more machinery than this card's scope calls for. Left as a
  known, accepted edge case rather than fixed speculatively.
- **First-run shops with zero modifier groups in the whole shop have no
  in-dialog path to `/modifiers`** (the dialog can't navigate away without
  either losing the item form's own unsaved edits underneath it or
  regaining the `target=_blank`/`location` navigation this exact dialog's
  own header comment forbids on the chromeless kiosk build). The
  empty-state copy fix (finding 2) makes the state honest rather than
  misleading, but doesn't add a way out of it. Filed as a follow-up
  Backlog card (ut-docs#2379) rather than widening this card's scope with
  a kiosk-navigation redesign. **Narrowed by the "What CI caught" fix
  below**, filed after this list was first written: `/modifiers` itself
  can now create a group for ANY item regardless of whether it already
  has one, so #2379 is down to a pure discoverability gap (the operator
  has to already know to go to `/modifiers`), not the "nowhere in the
  product can do this at all" severity it was when filed.

## Verification run (this session, not just the subagent's)

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./internal/pages/catalog/...` — clean.
- `go test ./internal/pages/catalog/...` — all pass, including the three
  rewritten assertions and the new multi-attach coverage.
- `go test ./...` (full repo) — green, no collateral breakage.
- `golangci-lint run ./internal/pages/catalog/...` — 0 issues.
- `scripts/ci/guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`
  — all green (docs-shots regenerated via `make docs-shots`, real
  Playwright run against the actual harness, not the surface-hash
  shortcut, since this card genuinely changes rendered pixels).
- New e2e spec + the two edited ones (`sell-screen-categories-tab-2283`,
  `order-type-prompt-placement-2282`) + the pre-existing kiosk-reachability
  spec (`catalog-modifier-summary-reachable-1989`) — all pass driven
  against a real Chromium instance, not just asserted against rendered
  HTML.

## Explicitly deferred (not this card)

- ut-docs#2379 (filed this cycle): a way to reach `/modifiers` from the
  item-editor's Manage Modifiers dialog when the shop has zero modifier
  groups at all — see "accepted as-is" above.
- The shop-wide `/modifiers` page's own single-`<select>` attach picker
  is untouched by this card; converting it to the same multi-select
  checkbox list (for consistency, not because anything is broken there)
  would be a separate, optional follow-up.
