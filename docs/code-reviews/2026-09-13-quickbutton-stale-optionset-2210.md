# Code review — quick button never reflects a modifier-group change (ut-docs#2210)

- **Date:** 2026-09-13
- **Ticket:** ut-docs#2210 (complexity:medium)
- **Branch:** `fix/2210-quickbutton-stale-optionset`
- **Reviewer:** independent pass, different model (Opus) from the Sonnet
  implementation, per this card's `complexity:medium` routing. One review
  round — the first pass found a real, non-blocker-class defect (a
  correctness bug, not money/tax/data-loss/security), which this pipeline's
  process rules treat as "fixed and re-verified in the same round," not as
  automatic grounds for a second full round.
- **Verdict: SAFE TO MERGE**, after the fix pass below. The orchestrator
  (this session) also personally re-verified the TDD claim by mutation
  (see "Independent TDD re-verification").

## What the product owner reported

Attached an option set ("Toppings") to an item ("American Coffee") that
already had a sale-screen quick button. The button did not offer the
customization picker until the quick-list entry was deleted and re-added.
(Variants never appearing at all is a separate, explicitly out-of-scope
issue, ut-docs#2209.)

## First pass: investigated the wrong layer, concluded "not reproducible"

The Dev subagent's first pass checked the backend thoroughly and correctly:
`shortcut_buttons` (`internal/data/shortcuts_repo.go`) stores only a live
`item_id` FK, never a config snapshot; `ButtonStore.Load()`
(`internal/ui/buttons.go:306-336`) computes `HasModifiers` fresh on every
call via a live DB join; `GET /ui/pos/modifiers`
(`internal/pages/pos_modifiers_api.go`) queries fresh per request too. All
true, and it shipped 3 (later a 4th) regression tests proving it, driving
real HTTP handlers. It concluded the bug was not reproducible and proposed
closing the ticket with tests only, citing an alleged precedent
(`docs/code-reviews/2026-09-02-category-switch-stale-tile-add-1433.md`).

**That conclusion was wrong**, and the independent review caught exactly why:

## What the review found

1. **The real bug is client-side, not server-side.** The sale screen
   fetches the tile grid exactly once, ever:
   `web/ui/pages/index.html`'s placeholder `<div class="products"
   hx-get="/ui/buttons" hx-trigger="load" hx-swap="outerHTML">` fetches
   once on load, and the `outerHTML` swap destroys that element. The
   fragment that replaces it, `web/ui/partials/buttons.html`'s `<div
   class="products">` root, carried **no** `hx-get`/`hx-trigger` of its
   own — so nothing on the page could ever re-fetch `/ui/buttons` again.
   A till left open on the sale screen (which it always is) would never
   see a newly-attached option set regardless of how correct the backend
   read is. The reported "fix" (delete + re-add the button) worked purely
   because navigating the Designer and back forced a fresh page load — not
   because the button itself held stale data.
2. **The cited precedent doesn't exist** in this repo — an unverifiable,
   fabricated-looking citation, now dropped from the record.
3. **The "real handler" regression test drove the wrong endpoint** —
   `POST /api/catalog/modifier-group` (creates a new group) instead of
   `POST /api/catalog/modifier-group/attach` (attaches an existing one,
   the PO's actual described action, ut-docs#2046).
4. **The detach-side test asserted a UI-unreachable state** — a raw
   `DELETE FROM item_modifier_group_links` removing a group's *only* link,
   which `UnlinkGroupFromItemUnlessLastLink`
   (`internal/data/modifier_repo.go:441`) refuses via the real handler. The
   real way a merchant neutralizes a group is the **Active checkbox**
   (`ItemIDsWithModifiers` gates on `is_active = 1`,
   `internal/data/modifier_repo.go:307-317`), which was untested.
5. **AC's "edit" case (toggling a group inactive/active again) was
   uncovered.**

## The real fix

Same self-refresh shape this codebase already uses for the held-sales
strip (`hold_api.go`'s `#held-sales`, `hx-trigger="held-changed
from:body"`, re-emitted via an `HX-Trigger` response header):

- `web/ui/partials/buttons.html`: the swapped-in root now carries
  `hx-get="/ui/buttons" hx-trigger="modifiers-changed from:body"
  hx-swap="outerHTML"`.
- `internal/pages/catalog/handlers.go`'s `renderModifierMutationResult` —
  the single shared dispatch for every modifier-group/option mutation
  (create/update a group including the Active toggle, attach, detach,
  create/update an option; verified all 6 call sites route through it) —
  now sets `HX-Trigger: modifiers-changed` on every `status == 200`
  success, and nothing on a refusal (e.g. the detach-last-link 409), so a
  rejected mutation cannot fire a false "something changed" signal.

`"load"` is deliberately not repeated on the swapped-in root: htmx already
fires `"load"` again the instant the element is inserted by the very
`outerHTML` swap `"modifiers-changed"` triggers, so adding it back would
only cost one harmless extra immediate refetch — omitted to match the
held-sales precedent exactly.

## Tests (all driving real HTTP handlers against a real test DB, no mocks)

- `TestButtonsUIFragment_ReflectsModifierGroupAttachedAfterButtonExisted` /
  `...Detached...` — raw-DB-seeded attach/detach reflected in `/ui/buttons`
  without the button being rebuilt (kept from the first pass; still
  correct as *server*-liveness proof, just not sufficient alone).
- `TestButtonsUIFragment_ReflectsModifierGroupAttachedViaRealAttachHandler`
  — drives `POST /api/catalog/modifier-group/attach` on an existing,
  unlinked group (replaces the pass-1 test that drove group *creation*
  instead).
- `TestButtonsUIFragment_ReflectsModifierGroupActiveToggleViaRealHandler` —
  replaces the UI-unreachable raw-delete detach test; toggles `isActive`
  via the real update handler both ways, covering AC's "edit" case.
- `TestGetModifiers_ReflectsGroupAddedAfterFirstFetch` — the picker's own
  content reflects a group added after the first fetch.
- `TestButtonsPartial_RootCarriesRefreshTrigger` — the swapped-in root
  carries the `hx-get`/`hx-trigger` pair.
- `TestModifierGroupAttach_FiresModifiersChangedTrigger` — a successful
  attach sets `HX-Trigger: modifiers-changed`.
- `TestModifierGroupDetach_LastLinkRefusalDoesNotFireTrigger` — a refused
  (409) detach must NOT fire the header.
- Brittle exact-string `hx-get="...&code=BTN1"` matching replaced with
  separate path/param substring assertions; the modifiers-picker test's
  DB seed now sets `is_active` explicitly instead of relying on a column
  default.

TDD-verified by the Dev subagent (stashed the two production-file changes,
confirmed `TestButtonsPartial_RootCarriesRefreshTrigger` and
`TestModifierGroupAttach_FiresModifiersChangedTrigger` fail red, popped the
stash, confirmed green).

## Independent TDD re-verification (this session, not the Dev subagent)

Per this pipeline's standing rule that the Reviewer step re-verifies any
TDD claim personally rather than trusting the Dev's own report: temporarily
replaced `w.Header().Set("HX-Trigger", "modifiers-changed")` with a no-op
comment in `handlers.go`, confirmed
`TestModifierGroupAttach_FiresModifiersChangedTrigger` fails
(`attach response HX-Trigger = "", want "modifiers-changed"`) while
`TestButtonsPartial_RootCarriesRefreshTrigger` and the refusal-guard test
are unaffected (as expected — they test the template and the refusal path,
not this line), then reverted and confirmed `git diff --stat` on the
production file shows no residual change and all tests pass again.

## Full verification (this session)

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l .` — clean.
- `go test ./internal/pages/... ./internal/ui/... ./internal/data/...` —
  all green.
- `guard-i18n.sh` — green (no new user-facing strings; `hx-*` attributes
  and the `HX-Trigger` header value are not display text).
- `guard-docs-shots.sh` — the diff touches `web/ui/partials/buttons.html`
  (in the guarded surface fileset), so it initially failed freshness.
  `hx-get`/`hx-trigger` are non-visual DOM attributes with no effect on
  any rendered pixel captured by the docs-shots harness (which never
  triggers this event during capture) — used the documented escape hatch
  (`scripts/ci/update-docs-shots-surface-hash.sh`) rather than a full
  104-screenshot regeneration, with the `Docs-Shots-Unchanged: true`
  commit trailer.
- E2e Playwright suite not run in this sandbox (npm deps not installed);
  the change is behavioral (an added refresh trigger + response header) on
  a page htmx already drives, with no markup/class-list changes visible to
  existing specs, so no functional spec touching `.pos-notice`/quick
  buttons has a code path this diff could break. Noting explicitly rather
  than silently skipping.

## Residual, explicitly out of scope for this card

The Opus reviewer noted, and this session concurs, that multi-till
satellite-sync staleness (a satellite till reading a stale replicated copy
of `shortcut_buttons`/`item_modifier_group_links` rather than the primary)
was not fully ruled out — the existing sync triggers (migration 025) were
checked and show no obvious gap, and the original report does not describe
a multi-till setup. Filed as a narrow follow-up rather than assumed away
(ut-docs#2236 — "not yet checked" residual from #2210, only worth
following up if a multi-till report of the same symptom surfaces).
