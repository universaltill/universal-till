# Code review: item-editor Manage Modifiers — go-to-/modifiers link when a shop has zero groups anywhere

- **Card:** universaltill/ut-docs#2379 — a shop with zero modifier groups
  anywhere in the catalog (nothing directly linked, nothing attachable,
  nothing inherited from a category) had no discoverable way out of the
  item-editor's nested "Manage Modifiers" dialog (attach-only since
  ADR-0101/ut-docs#2330) to reach `/modifiers`, the only screen a group
  can be created on.
- **Repo:** `universal-till`
- **Reviewer:** independent Opus review, isolated worktree (`Agent`,
  `isolation: "worktree"`, `model: opus` — card is `complexity:medium`,
  built inline at Sonnet).

## What shipped

- `web/ui/partials/modifier_group_admin.html`: a "Go to Modifiers to
  create a group" button, scoped to the item-scoped nested-dialog wrapper
  template (`modifier_groups_item_panel`) only — never the shared
  `modifier_group_admin` define the shop-wide `/modifiers` page also
  renders per item. Shown only when `.ModifierGroups`, `.AttachableGroups`
  AND `.InheritedGroups` are all empty (the genuinely-stuck case; a shop
  with something to attach/inherit already has a real path forward).
  Placed after the inherited section, not right under "No Modifiers
  yet.", so the operator has already seen there is nothing here before
  being offered the way out.
- `web/ui/pages/catalog.html`: `window.utCatalogGoToModifiers()` —
  same-tab navigation only (never `target="_blank"`/`window.open`, the
  kiosk build is fully chromeless), guarded by a `window.confirm()` using
  the existing `common.discard_confirm` string, only when the item form
  actually has unsaved edits since it was opened.
- `web/public/app.css`: `1rem` top margin on the new button's wrapper,
  matching its siblings' existing spacing convention.
- `web/locales/{en,ar,fa,tr}.json`: one new key,
  `catalog.modifiers.go_to_modifiers`, real translations in all four,
  correctly sorted alphabetically.
- `web/help/en/catalog.md`: documents the new button and when it shows.
- `web/help/img/manifest.json`: regenerated via `make docs-shots` (no
  screenshot pixels actually changed — the automated shots don't drill
  into this dialog's empty state — only the topic/surface hashes moved).
- Tests: two new Go handler tests plus two added during review
  (`internal/pages/catalog/modifiers_zero_groups_2379_test.go`), four
  Playwright e2e tests (`e2e/tests/catalog-modifiers-goto-guard-2379.spec.ts`).

## Review findings

**1 must-fix, 2 should-fix, 3 nits — all fixed before merge.**

- **Must-fix (real bug, confirmed by direct repro before accepting):**
  the first implementation snapshotted the item-form via
  `new URLSearchParams(new FormData(form))` (record-dialog.js's own
  `confirmDiscard` technique). `#item-price` — the field the operator
  actually types into — carries **no `name` attribute**; only the hidden
  `#item-price-minor` (`name="price"`) does, and that's written exactly
  once, by the submit handler (`catalog.html`, `document.getElementById
  ('item-price-minor').value = …` right before the save POST). A
  FormData snapshot is therefore byte-identical before and after editing
  ONLY the price, so `utCatalogGoToModifiers` never prompted and silently
  discarded the edit — exactly the silent loss the card's acceptance
  criteria forbids. Verified the repro by reading the template (`#item-
  price` has `id` but no `name`) and confirming `#item-price-minor` is
  written nowhere else.
  **Fix:** switched the guard to snapshot `formSession` (already bumped
  by a form-level `input` listener, which fires on a native `input` event
  regardless of whether the target field has a `name` attribute — so it
  sees the price field too) instead of a FormData serialization. Added a
  regression e2e test (`editing ONLY the price still counts as dirty`)
  that fills `#item-price` alone and asserts the confirm still fires.
- **Should-fix:** the two original Go tests didn't discriminate the
  three-way `and` condition — reviewer mutated it to `{{ if not
  .AttachableGroups }}` and both still passed. Added two tests: one
  seeding a group directly linked to the item only (`ModifierGroups`
  non-empty, the other two empty for unrelated reasons — no other item
  exists), one seeding a category-inherited-only group via the real
  migrated-DB `setupInheritDeps` helper. Re-ran the reviewer's exact
  mutation after adding them: the new direct-link test now fails as
  expected, confirming the discrimination gap is closed. (Noted in the
  inherited-only test's own comment: under this repo's current
  `ListAttachableModifierGroups` semantics — excludes only *directly*-
  linked groups — any group visible in `InheritedGroups` is necessarily
  also present in `AttachableGroups`, so that test doesn't add further
  discriminating power over the direct-link test; kept anyway as a real,
  user-reachable regression lock.)
- **Should-fix:** the e2e "dirty form + Cancel" test used
  `page.waitForTimeout(200)` before asserting no navigation happened — a
  poll that passes instantly regardless of whether the guard actually
  worked. Removed the timeout: `window.confirm()` is a blocking native
  call, so by the time the `page.evaluate()` promise that invoked
  `utCatalogGoToModifiers` resolves, Cancel/OK has already been answered
  and the function has already returned (calling `location.assign` only
  on the OK path, before returning) — asserting the URL immediately after
  the `await` is deterministic, not a race.
- **Nits, all fixed:** the help-doc sentence said the button shows
  "instead of an empty list" (it renders *in addition to* the existing
  empty messages) and "no modifier groups at all yet" (imprecise —
  `AttachableGroups` is active-only, so a shop with only inactive groups
  also gets the button) — reworded. The new button's wrapper div had no
  CSS margin rule at all, sitting flush against the section above it —
  added the same `1rem margin-block-start` its siblings already use. The
  new locale key was inserted in the wrong alphabetical position in all
  four files (`go_` sorts before `gr_`, not after `group_name`) —
  reordered.

## Verified, live, not just read

- `gofmt -l .` — silent. `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/...` — all green, including the 4
  zero-groups-panel tests (both new discriminating cases pass, and the
  reviewer's own mutation now correctly fails the direct-link test).
- `golangci-lint run ./internal/pages/catalog/...` — 0 issues.
- `bash scripts/ci/guard-i18n.sh`, `guard-data-access.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-docs-shots.sh` — all ✓ (docs-shots
  regenerated twice, once per round of fixes; no screenshot pixels
  actually differ, only hashes).
- `e2e/tests/catalog-modifiers-goto-guard-2379.spec.ts` run for real
  against the live app (Playwright, Chromium, `--project=default`): all
  4 tests pass — clean-form no-prompt, dirty+Cancel stays put, price-only
  edit still prompts (the must-fix's regression lock), dirty+OK
  navigates.
- **Visual check** (tester skill's "look at it" rule): rendered the exact
  server output of the zero-groups fragment against the real `app.css`
  in a headless browser at 480×500, in en, ar (RTL), fa (RTL) and tr (the
  longest translation, which wraps to two lines inside the button with no
  clipping/overflow). Button reads clearly, right-aligned correctly in
  RTL, comfortably spaced from the sections above it after the CSS nit
  fix. Not verified: the real running app's own auth/setup flow end to
  end (used a static-fragment render + the shared e2e harness instead,
  given the shared e2e DB can't reliably reach a genuine "zero groups
  shop-wide" state once other specs have seeded groups).
- No `target="_blank"`/`window.open` introduced anywhere in the diff
  (grepped). No real client/shop name used as test data. No secret-shaped
  literal anywhere in the diff.
- Button correctly confined to the item-scoped nested-dialog wrapper —
  confirmed `web/ui/pages/modifiers.html` only calls the shared
  `modifier_group_admin` define directly, never the wrapper, so the new
  button cannot leak onto `/modifiers` itself (where the JS it calls
  doesn't even exist).

## Verdict

**Safe to merge** — one real, confirmed bug found and fixed (with a
regression test proving it), test discrimination strengthened and
re-verified via the reviewer's own mutation, all nits closed, full gate
green. `merge_method: "merge"`.
