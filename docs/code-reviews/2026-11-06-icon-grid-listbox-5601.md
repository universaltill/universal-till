# Review: built-in icon grids become a focus-only listbox (ut-docs#5601)

Date: 2026-11-06 · Author model: Sonnet (Dev) · Reviewer: Opus 5.5 (independent, fresh context; second reviewer after the first was lost to a container restart, its uncommitted finding re-verified below)
Parent: ut-docs#5553 (`2026-11-05-picker-radiogroups-5553.md`, Deferred section).

## What shipped
- Both built-in icon grids (catalog item editor's **Item image** tab, category dialog) are an APG single-select listbox with grouped options: `role="listbox" data-roving-listbox` > `role="group"` (one per section, `aria-labelledby` a per-page unique heading id `builtin-icon-grid-gN` / `category-icon-grid-gN`) > `<button role="option" aria-selected>`. "No image" stays the first option, outside any group. No `aria-pressed` left on these tiles.
- `web/public/radiogroup.js`: a second, focus-only branch. Arrows (wrap; Left/Right mirrored in RTL), Home/End move focus over visible options only (attribute-based `[hidden]` walk, so it also works inside an x-show-hidden tab panel) and never `.click()` — arrowing the catalog grid no longer POSTs `/api/catalog/item/icon` and so cannot replace an uploaded photo. Enter/Space/tap choose through the existing click handlers (native `<button>` click). One `tabindex=0`: `focusin` moves it to the focused option, `focusout` (leaving the listbox) and `window.utRovingListbox.sync()` re-derive it (selected visible option, else first visible). The #5553 radiogroup branch now shares the `pick()` helper.
- `icon-picker-filter.js`: hides whole `.builtin-icon-group`s (heading + tiles) when none match and calls `sync()` so a filtered-away stop moves to a visible option. `categories.html` drops its page-local keydown handler; `paintIcons`/`setBuiltinIconTiles` set `aria-selected` and call `sync()`.
- CSS: `[aria-selected="true"]` added to the selected-tile rules (normal, `fx-light`, forced-colors); `.builtin-icon-group` repeats the grid's column template (no subgrid/`display:contents`); `scroll-padding: 5px` so an arrowed-to edge tile's focus ring is not clipped (Tester O1). Logical properties only.
- Help: keyboard sentence in `web/help/en/catalog.md` and `categories.md`; `make docs-shots` regenerated `catalog.png`/`categories.png` + `manifest.json`.
- e2e: new `icon-grid-listbox-5601.spec.ts` (11 cases); `category-image-2500`, `icon-library-2506` moved to `aria-selected`; forced-colors case for the selected icon tile; `picker-radiogroup-5553` header updated.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | Medium | Catalog repaints selection asynchronously (POST, then GET `icon-state` → `setBuiltinIconTiles` → `sync()`). If the operator kept arrowing before that GET landed, `sync()` moved the only `tabindex=0` back to the selected tile while focus sat on another one at `tabindex=-1`: Shift+Tab from the focused tile landed on the selected tile (a second stop inside the same listbox) instead of leaving it. | Fixed (found by the first reviewer, verified here): `sync(listbox, leaving)` keeps the stop on a focused visible option; `focusout` passes `leaving=true` so the stop returns to the selection once focus really leaves. New e2e "catalog: a late selection repaint keeps the stop on the focused tile" (holds the `icon-state` GET with `page.route`) **fails without the fix** (`Expected "0", Received "-1"` on the focused tile's tabindex) and passes with it. |
| R2 | Nit | The R1 comments (radiogroup.js, spec) said *Tab* reaches the second stop; for a selected tile before the focused one it is Shift+Tab. | Fixed: wording corrected. |
| R3 | Nit | `categories.html` comment still described the icon grid as "pressed" tiles with "roving tabindex like the colour grid". | Fixed: now "selected", focus-only listbox branch, unlike the colour grid's radiogroup. |
| R4 | Low | R1–R3 touch `web/public/radiogroup.js` / `web/ui/**`, so the docs-shots surface hash went stale (guard red). | Fixed with `scripts/ci/update-docs-shots-surface-hash.sh` (only `surface_sha256` changes; no rendered pixel changes: tabindex during a held repaint and comments). The commit carrying it should have the `Docs-Shots-Unchanged: true` trailer if it lands separately from the PNG regeneration. |
| Tester D1 | High (CI) | docs-shots stale after the help edits | Fixed by Dev before review (`make docs-shots`). |
| Tester O1 | Low | Focus ring clipped at the scroll box edge after arrowing | Fixed (`scroll-padding: 5px`). |
| UX A1 | Advisory | Focused vs selected tile differ by ring shape only (pre-existing since #1103) | Accepted; out of scope. |
| UX A2 | Advisory | categories.md keyboard sentence buried in a parenthetical | Fixed by Dev (separate sentence). |

Checked and clean (no finding):
- Filter hiding everything: impossible — "No image" is never hidden, so `sync()` always has a stop; the no-match message still shows. Filter hiding the selected tile: stop falls to "No image"; clearing the filter or leaving puts it back on the selection (Tester driven run + "a search filter limits…" spec).
- `setBuiltinIconTiles` / `paintIcons` re-render: every path ends in `sync()`, which writes `tabindex` on every option, so exactly one `0` — asserted in "exactly one Tab stop in every state" (both grids) and the R1 test.
- #5553 radiogroup branch: behaviour unchanged apart from not calling `preventDefault` when a group has no visible radio (harmless); `picker-radiogroup-5553` 6/6 and `picker-forced-colors-5553` 3/3 pass.
- ARIA: listbox > (option | group > option); `aria-selected` rendered on every option and set on every repaint; group heading ids unique per page and not focusable; accessible name = visible label (spec asserts it).
- No `aria-pressed` reference to these tiles left in `web/` or `e2e/` (grep; remaining `[aria-pressed]` hits in the 5601 spec assert its absence).
- Removed `categories.html` keydown handler only did arrow focus moves over `:not([hidden])` tiles — fully covered by the listbox branch (plus Home/End, plus no-op when unfocused).
- No user-facing strings added (guard-i18n green; listbox name reuses `catalog.builtin_icon.hint`). New CSS is logical-only (guard green). No `showModal`.

## Verification
- **R1 fix verified personally:** with `web/public/radiogroup.js` stashed, the new test fails (`tiles.nth(4)` tabindex `-1`); restored (sha256 identical), it passes.
- **Original TDD claim re-verified personally:** listbox keydown branch replaced by an early `return` → `icon-grid-listbox-5601` 6 failed / 5 passed (arrows/wrap/Home/End, filter, RTL, catalog no-write+Enter, late repaint, category Cancel+Space; the 5 passing are the template/markup, Tab-stop and category-photo cases, as the spec header states). Restored (sha256 identical).
- Gate after the last edit: `run-docs-shots-guard.sh` (33 topics fresh), `guard-i18n.sh`, `guard-rtl-logical-css.sh`, `guard-no-showmodal.sh`, `guard-help-topics.sh` all exit 0; `node --check radiogroup.js`. e2e (`--trace off`, default project): icon-grid-listbox-5601, category-image-2500, icon-library-2506, categories-editor-2284, picker-radiogroup-5553, picker-forced-colors-5553 — **35 passed, 0 failed**.
- Tester (not the Dev agent, notes `5601/tester.md`): false-pass hunt — catalog "writes nothing" test was vacuous without the keyboard branch, fixed to assert focus really moved; mutant "arrows click" → 3 failed; mutant "filter skips sync" → 2 failed. Gate: gofmt, golangci-lint 0, shellcheck 0.11.0 0, 137/141 ci.yml build-job guards (the 4 were docs-shots = D1, since fixed, and the sandbox shellcheck path). 56 touched-surface e2e, 14 photo-cutout, 113 Chromium phone specs passed. Driven run (1024×600, en + ar): one Tab stop, 0 POSTs while arrowing, Enter → 1 POST, RTL wrap correct, no overflow, columns unchanged, no console errors.
- UX §7 gate (notes `5601/ux.md`): PASS. Touch tap unchanged (emulated), one Tab stop in every state, nothing steals focus, RTL mirrored, forced colours distinct, 1024×600 and 360 px layouts unchanged, accessibility tree listbox > option / group > option.
- Not verified: WebKit (browser download blocked locally; CI `webkit-phone` runs it as the PR touches `web/**`), real touch hardware / kiosk Pi, real screen readers (inferred from the Chromium accessibility tree).

Pre-flight: Till user-visible behaviour: `web/help/` topics updated (catalog, categories) + `make docs-shots` (catalog.png, categories.png, manifest; surface hash refreshed after review R1–R3); `reference/` n/a (no reference page describes picker keyboard behaviour); README n/a (not stale). en.json keys: n/a (none added). ADR: n/a (APG pattern within ADR-0008, no new decision). Plugin type/host fn/manifest: n/a. Review record: this file. Follow-ups: deferred → ut-docs#5663.

## Verdict
**Approve.** R1 fixed and proven by a failing-then-passing test; remaining items were nits/comments, fixed. Merge once CI (incl. `webkit-phone`) is green.

## Deferred
- ut-docs#5663 — the out-of-scope follow-up from this card (Architect/UX A3: 2-D Up/Down by visual row, type-ahead, PageUp/PageDown). UX A1 (stronger selected cue) is not part of this card.
