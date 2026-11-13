# Code review — tick badge on the chosen icon tile (ut-docs#5736)

**Date:** 2026-11-06 · **Branch:** feat/5736-icon-chosen-tick · **Reviewer:** independent subagent (Opus 5.5; code by Sonnet)
**Precedent:** 2026-11-06-icon-grid-keys-5663.md (its deferred UX advisory A1 is this card)

## What shipped
The chosen tile (`aria-selected="true"`) of the built-in icon listboxes now shows a small tick badge in its top
inline-end corner. This applies to the catalog item editor's Item image tab and the category dialog. Before, "chosen"
was shown only by the accent border and fill, and the focus outline uses the same hue. The badge adds a shape cue,
so chosen and focused can be told apart without colour (WCAG 1.4.1).
- CSS only (`web/public/app.css`). `::after` draws the filled circle and `::before` draws the check (a `clip-path`
  polygon). There is no image and no left/right: placement uses `inset-block-start` / `inset-inline-end`. The badge is
  decorative (`content: ""`), because `aria-selected` already carries the state.
- Light effects: the badge uses only `background` and `clip-path`, and the `html.fx-light` box-shadow/filter wipe does
  not remove either.
- Forced colours: the tile is `Highlight`, so the badge inverts to a `HighlightText` circle with a `Highlight` check.
- The rules are scoped to `.builtin-icon-tile`. The colour pickers, the setup picker tiles, the order-type tiles and
  the self-order kiosk (which has no `.builtin-icon-tile`) are unchanged.
- `web/help/img/manifest.json`: only the surface hash changed. No PNG changed, because no docs-shot shows a chosen icon
  tile (`Docs-Shots-Unchanged: true`).
- Tests: new `e2e/tests/icon-chosen-tick-5736.spec.ts` (6 tests) covering normal, "No image", Light effects, forced
  colours, RTL and a negative case.

## Findings
| # | Severity | Finding | Resolution |
|---|---|---|---|
| F1 | Medium | The badge circle used `--accent`. In amber, fresh and monarch the white check on `--accent` is only 2.80, 2.77 and 2.54:1, below the 3:1 a graphical cue needs (WCAG 1.4.11). Those are the same themes ut-docs#797 gave a darker `--focus-border` for this reason. | Fixed: the circle uses `var(--focus-border)` (the default is `var(--accent)`, so default, slate, mono and dark are unchanged). Check on circle is now 3.42, 3.40 and 3.42:1. A comment cites the reason. |
| F2 | Low | The negative test only checked a colour tile (`aria-checked`). It could not catch the rule being widened to `.picker-tile[aria-selected]` or `[aria-pressed]` (the tester flagged this). | Fixed: the test also renders plain `.picker-tile` buttons with `aria-selected` and with `aria-pressed` and asserts no `::before`/`::after` content. Mutation-checked: adding `.picker-tile[aria-pressed="true"]::after{content:""}` makes it fail, and reverting makes it pass. |
| I1 | Info | Dark theme (the tester did not check it): circle on the chosen tile (`--surface-2` #1e293b) is 9.89:1 and the check (`--accent-contrast` #04120f) on the circle is 12.92:1. | No change |
| I2 | Info | Amber: the circle against the chosen tile's `--surface-2` is 2.99:1. The cue is the check, which is 3.42:1 against its circle, and the tile keeps its 2px accent ring. | Accepted |
| I3 | Info | "No image" is a text-only tile. The badge sits in the corner padding and does not overlap the centred label; the spec covers it. | No change |

## Extra verification
- Contrast was computed from the theme files for all 7 themes (default, amber, dark, fresh, monarch, monochrome,
  slate), for both the circle on the tile and the check on the circle.
- The tester's red/green was confirmed: with the CSS stashed, the spec gives 5 failed and 1 passed. That 1 was the
  negative test, which now fails on a mutation (F2).
- After the fixes, the 5736, icon-grid-listbox-5601, picker-forced-colors-5553, icon-grid-2d-nav-5663 and
  picker-radiogroup-5553 specs give 45/45 (Chromium). A re-run of the 5736 spec after the mutation check gave 6/6.
- I looked at `tick-normal.png`. The tick reads clearly in the top inline-end corner, clear of the icon and the label.
- `gofmt -l .` is empty and `go build ./...` passes. No Go was touched, so `go vet` and `go test` are n/a.
- These guards pass: docs-shots (after `update-docs-shots-surface-hash.sh`), run-docs-shots-guard, rtl-logical-css,
  fixed-dates, i18n, no-showmodal, compliance-claims, competitor-naming, help-topics, mail-addresses,
  ci-tests-sandboxed, e2e-fixtures-import, e2e-no-browser, e2e-no-lan-discovery and e2e-no-update-check.
- Not verified: WebKit, a real Windows High Contrast session, real screen readers, the real de language pack.

Pre-flight: till user-visible behaviour → `web/help/` topic: n/a (catalog.md and categories.md do not describe how the
chosen tile looks, so no prose is stale); `make docs-shots`: run by dev, manifest surface hash refreshed, PNGs
unchanged (no shot shows a chosen icon tile; `Docs-Shots-Unchanged: true`); `reference/` page: n/a (no ut-docs
reference page documents the icon picker's selected style); `README.md`: n/a (does not describe it); `en.json` keys:
n/a (none added; the badge is decorative); review record: this file.

## Verdict
Approve. F1 and F2 are fixed in this one review round, and the fixes were re-verified: five icon/picker specs 45/45,
the strengthened negative test mutation-checked, and the docs-shots guard fresh.

## Deferred
- None.
