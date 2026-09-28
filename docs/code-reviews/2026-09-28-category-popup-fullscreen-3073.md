# Code review — category popup: full screen + closes after an add (ut-docs#3073)

- **Date:** 2026-09-28
- **Card:** universaltill/ut-docs#3073 (p1, complexity:medium, ux)
- **Branch:** `fix/3073-category-popup-fullscreen-autoclose`
- **Author:** pipeline lane `lane:cloud-24` (Opus 5.5). **Reviewer:** independent Fable subagent in a separate worktree.

## What shipped

In the `category_tabs` browsing mode (ut-docs#2499), tapping a category tile opens `#category-items-modal`. The product owner and the pilot café owner asked for two changes, tested on the tablet:

1. **Full screen.** `.modifier-modal.category-items-modal` (web/public/app.css) now fills the space from the nav rail's inline-end edge to the viewport's end, top to bottom. It used to be a centred 44rem box with a 60vh list. The heading stays at the top, the item list scrolls (`flex: 1`), and Close is pinned at the bottom. The selector is doubled with `.modifier-modal` so that rule's 28rem/90vw cap, which comes later in the file, can't win. Other cases:
   - The on-screen keyboard reservation follows `body.osk-padded .payment-overlay`.
   - At ≤480px the rail becomes a top bar, so the popup starts under it (`calc(var(--topbar-h) + .5rem)`, the same offset `.bugreport-panel` uses).
   - All insets are logical, so under RTL the popup sits beside the right-hand rail.
   - The rail is never covered. Its chips stay visible and Lock stays live through the scrim's existing `.nav` lift (ut-docs#1999/#2873).
2. **Closes by itself after an add** (web/public/app.js, `catPopupArmed`). A tap on a tile inside the popup arms it. The next successful `/api/pos/scan*` swap of `#basket` closes it with `dialog.close()`, so the shared ADR-0122/0123 shrink plays as it does for a Close tap. That single rule covers:
   - a plain item;
   - a modifier/variant item, which closes only after the picker's Add succeeds; if the picker is cancelled the popup stays open and armed;
   - the before_item order-type prompt, which replays the request through `htmx.ajax` with no issuing element.

   The popup does **not** close on a refusal:
   - a 4xx;
   - a 200 basket re-render carrying `.pos-notice.error`;
   - the #2525 stale-tile answer: 200, an info notice and `HX-Trigger: buttons-changed`, with nothing added.

   Any close disarms it.

Also in this change:
- web/ui/pages/index.html comment.
- User manual `web/help/{en,de,ar,fa,tr}/sell.md`: the "popup stays open so you can add several" sentence is replaced; all five languages were translated in this change.
- `web/help/img/manifest.json`: surface and topic hashes refreshed. No new locale keys, no ADR (this follows ADR-0122/0123 and the #1999 rail rule).

## Tests

- New `e2e/tests/category-popup-fullscreen-3073.spec.ts`:
  - Full-screen geometry and the rail beside it, lifted above the scrim, at 1024×600 and 1280×800.
  - Auto-close at both sizes: a plain add closes the popup and `data-ut-closing` is observed (motion enabled with `emulateMedia no-preference`). Cancelling the picker keeps the popup open. Adding from the picker closes both. After a reopen nothing is left armed: a scan-path add leaves the popup open.
  - A refused add (200 with an error notice) keeps the popup open.
  - A stale tile keeps the popup open.
  - At 360×800 the popup sits below the top bar and fills the rest of the screen.
- `sell-screen-browsing-mode-2499.spec.ts` is updated: it reopens the popup for the modifier item, and the picker's Add now closes the popup.
- TDD: the new spec failed 4/5 on the old code (geometry, and the popup still open after the add). The reviewer re-verified this independently by reverting app.css/app.js: the popup started at x=274 instead of the rail's edge (76.5) and `<dialog open>` persisted. The stale-tile test was checked the same way: with the `buttons-changed` check removed it fails on "the popup stays open on a stale tile".
- Gate: `gofmt`, `go build`, `go vet` and `go test ./...` are green, and every guard in ci.yml's `build` job passes. `shellcheck` isn't installed in this container, and no shell script changed. Adjacent e2e specs are green: popup-scrim-2873, popup-zoom-2944, category-image-2500, tap-feedback-3000, order-type-prompt-placement-2282, sale-screen-213 and the 2499 spec. The new spec passed 3/3 under `--repeat-each=3`.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The #2525 stale-tile answer (200 + info toast, nothing added) closed the popup, which contradicted the comment and the manual ("a refusal keeps it open"). | **Fixed**: skip the close when the response carries `HX-Trigger: buttons-changed`. Covered by a new e2e test, verified red without the fix. |
| 2 | nit | The full-height popup (z 500) covers the sticky `.statusbar` (connection chip) while it is open. The rail's own chips and Lock stay visible. | **Accepted.** Same precedent as `.payment-overlay` at ≤900px, and the card asks for full screen with the rail reachable. If the owner wants the bar kept visible, the change is an `inset-block-end` reservation for its height. Noted on the card. |
| 3 | nit | The "nothing left armed" assertion could not fail. | **Fixed**: it now adds an item through `/api/pos/scan` into `#basket` and asserts the popup stays open. |
| 4 | nit | After a cancelled picker the popup stays armed, so a barcode scan of an unrelated item closes it. | **Accepted.** An item did land; the popup's job is done. |
| 5 | nit | A 1px `border-inline-start` showed at ≤480px, where the popup runs edge to edge. | **Fixed.** |

The reviewer also checked and cleared:
- the CSS cascade and specificity;
- the `data-ut-closing` re-show (it records `display: flex`);
- the HX-Retarget of a variant parent scan to `#modifier-modal`, which does not close the popup;
- the 400 basket carve-out;
- the before_item replay;
- z-order against the scrim;
- RTL;
- the four translations, which carry the same seven facts as the English.

## Verified beyond automated tests

Screenshots were taken and looked at:
- 1024×600 (en): full-bleed beside the rail, seven columns of tiles, Close bottom-right.
- 360×800: below the top bar (now with the +.5rem offset).
- 1024×600 fa (RTL): mirrored, beside the right-hand rail. With the on-screen keyboard up, Close stays above the keyboard.
- 1280×800 de.

**Not checked:** the real pilot tablet, or touch on real hardware. Everything was driven with Playwright's synthetic pointer in Chromium, so this goes to the local lane.

## Docs screenshots

`make docs-shots` in this container re-rendered all 124 PNGs, and every one differed through font-rendering noise. No docs screenshot opens this popup, so the PNGs were restored and only `manifest.json` was refreshed: the sell topic hashes (markdown changed) and `surface_sha256`, via `update-docs-shots-surface-hash.sh`. Commit trailer: `Docs-Shots-Unchanged: true`. This follows the 981e2c8 precedent.

## Verdict

Safe to merge.
