# Code review — phone: tapping a manual topic does nothing (ut-docs#3356)

- **Date:** 2026-10-02
- **Branch:** `fix/3356-phone-help-tree`
- **Lane:** cloud-54 · complexity:easy. Built by a Sonnet dev subagent. Reviewed independently by Opus 5.5 in a fresh context and its own worktree.

## What shipped

At ≤52rem the manual (`/help`) stacks the topic tree above the reading
panel. `.manual-nav` kept `position: sticky` and a viewport-tall
`max-block-size`, so on a phone the tree filled the screen and stayed
pinned. A tapped topic swapped into `#manual-panel` below it, out of sight.
`/items` had the same bug (#3297).

- `web/public/app.css`: at ≤52rem the nav is unpinned. While the panel
  shows a topic (`:has(#manual-topic[data-topic])`) the whole nav hides.
  While it shows search results only the tree hides, and the search box
  stays. `.manual` gets `overflow-anchor: none`, because scroll anchoring
  re-scrolled the page past the heading once the tree left the layout. A
  phone-only `.manual-back` link with a 44px touch target. Wide layout
  unchanged.
- `web/ui/partials/help_topic.html`: a "‹ Manual topics" back link before
  the topic. It reuses the existing key `help.nav.label`, so no new locale
  keys and no language-pack follow-ups. It does `hx-get="/help"`, which
  renders the landing plus the OOB tree. The iOS app's WebView has no
  browser Back button, so this link is the way back.
- `web/public/app.js`: after a click-triggered swap into `#manual-panel` at
  phone width, scroll to the top. If the engine has no `:has()`, scroll the
  panel into view instead. Typing in search is not a click, so it never
  jumps.
- Tests:
  - `TestHelpTopicHTMXHasBackLinkLandingDoesNot`.
  - `e2e/tests/phone-help-tree-3356.spec.ts`:
    - phone tap / back / search;
    - deep-tree tap;
    - RTL chevron;
    - wide layout unchanged.
- `web/help/img/manifest.json`: surface hash only. `make docs-shots`
  changed no PNG, because the manual screenshots are wide-layout.

## Findings (Opus 5.5 review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (cosmetic) | `[dir=rtl] .manual-back-chev { transform: scaleX(-1) }` double-flipped the chevron. U+2039 is bidi-mirrored, so RTL already draws it toward inline-start. | **Fixed.** Rule removed. The new RTL e2e test fails with the rule and passes without it. |
| 2 | minor | The spec tapped a topic near the top of the tree, so the scroll-to-top JS was untested. Reverting it still passed. | **Fixed.** Added a deep-tree tap test. It fails with app.js at `main` and passes with the fix. |
| 3 | minor | On an engine without `:has()` (Safari < 15.4, Chromium < 105, older WebKitGTK) the tree never hides, so `scrollTo(0,0)` would show the tree, not the topic. | **Fixed.** The JS checks `CSS.supports('selector(:has(*))')` and otherwise scrolls the panel into view. Such an engine still gets the unpinned stacked layout. |
| 4 | nit | After search → topic → back, the search box still holds the old query while the panel shows the landing. | Accepted. The wide layout behaves the same, so this is not a regression. |
| 5 | nit | `.manual-back:hover` sticks after a tap on iOS. | Accepted. The link is replaced on the next swap. |

The reviewer also checked these and found them fine:
- Browser Back/Forward at phone width (htmx re-requests full pages).
- Search → result → topic.
- The afterSettle listener: it filters on target, an OOB double-fire is idempotent, and history restore is ignored.
- i18n: the glyph is decorative and `aria-hidden`.
- Kiosk and status-bar reachability: only the manual's own `<aside>` hides.
- No file writes and no cwd paths.

## Verification

- **TDD, re-verified by the reviewer:**
  - With app.css reverted, the phone and wide specs fail; restored, they pass.
  - With help_topic.html reverted, the Go test fails; restored, it passes.
- **New tests, re-verified by the orchestrator:**
  - The deep-tap test fails without the app.js listener.
  - The RTL test fails with the scaleX rule.
  - Both pass with the fix.
- **Commands:**
  - gofmt clean, `go build ./...`, `go test ./...`.
  - Guards: i18n, no-showmodal, help-topics, help-drift, data-access, kiosk-engine, compliance, competitor, core-neutral, htmx/osk-loaded, docs-shots (after `make docs-shots`) and the others in the build job.
  - Playwright: the new spec (4/4), plus faq/manual/rtl/pages (dev run, 33 passed).
- **Screenshots looked at:**
  - 440×956: landing, topic, after back, search.
  - 360px: topic.
  - 1024×600: topic (tree beside it, no back link).
  - fa at 440: mirrored, back link at inline-start.
- **Not verified:** a real iPhone (WebKit). Every run was Chromium touch emulation.

## Verdict

Safe to merge.
