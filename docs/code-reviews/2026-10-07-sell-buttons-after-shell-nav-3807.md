# Review: sell-screen buttons survive shell navigation (ut-docs#3807)

**Date:** 2026-10-07 · **Built by:** Claude Opus 5.5 · **Reviewed by:** Claude Fable 5.1 (independent subagent)

## What shipped

Product owner's report: Sell → Menu → Sell left only the `+` button in the
scan row; the camera-scan, AI-identify and on-screen-keyboard buttons were
gone until a full reload.

Cause: a menu link is a boosted `#ut-page` swap (ADR-0098), and those three
buttons render `hidden` and were unhidden (and, for the cameras, wired) by
code that ran once per document load. It also meant a till that first
loaded on another page never got them on Sell.

- `web/public/app.js`: new `utSellCamera(bind)` helper. The AI-identify and
  barcode-scan blocks are bind functions run at load and on every
  `htmx:load`; a binding whose button left the document is closed first, so
  a camera open when the cashier navigates away is released. The barcode
  decoder cache stays per document.
- `web/public/osk.js`: `updateToggles` also runs on `htmx:load`.
- e2e: 6 new tests (barcode Sell→Menu→Sell twice with one camera per tap,
  cold start on `/menu`, release on leave; AI-identify after navigation and
  late-granted camera; OSK toggle after navigation).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | AI-identify's `getUserMedia().then` had no closed-overlay guard: a camera granted after the cashier left Sell (or pressed Close) stayed live in a detached closure. Reviewer reproduced it with a deferred-grant stub. | **Fixed** — same `overlay.hidden` guard as the barcode path; e2e "a camera granted after leaving Sell is released" was red before, green after. |
| 2 | Low | Comment said "see utSellCamera below"; it is above. | Fixed. |
| 3 | Low | `utSellCamera` is a new top-level global. | Accepted — same as `initOfflineOverride`; used only in this file. |

Checked and not a problem (reviewer): Back/popstate is a full reload
(`historyCacheSize:0`, `refreshOnHistoryMiss`), verified working; partial
swaps hit the `isConnected` early return; no double binding; no new
user-facing strings or inline handlers.

Accepted residual (pre-existing, both cameras): Close then reopen before the
first permission prompt resolves can let two streams arrive; very unlikely
on a till and unchanged by this diff.

## Verification

- TDD: each new navigation test failed on `main`'s JS with
  `Expected: visible / Received: hidden` (re-verified independently by the
  reviewer in its own worktree), then passed.
- e2e: `sale-screen-camera-barcode-scan-548`, `settings-osk`,
  `camera-error-branching-1292`, `nav-back-3352`, `phone-sell-3059` 57/57;
  `camera-error-branching-ai-identify-1559` (ai-identify project) 6/6; plus
  page-slide, phone-width, guided-tour, bugreport-panel, page-zoom 66/66.
- Real device: TECLAST P50T tablet, Chrome, this branch served via
  `adb reverse` (no LAN exposure): Sell → Menu → Sell shows keyboard and
  scan buttons; scan opens the camera overlay and permission prompt.
- Viewports: 1024×600 and 360×740 (sheet open) after Menu → Sell — both
  buttons shown, screenshots looked at. Not checked: the AI-identify button
  on the tablet (needs an AI endpoint configured); RTL (no layout change).
- `node --check`, `go build ./...`, `guard-i18n`, `guard-emoji-font`,
  `guard-no-inline-handlers`, `guard-no-showmodal`: clean.

Out of scope, filed: ut-docs#3812 (category tile names break mid-word at 360px).

**Verdict:** safe to merge.

## Addendum — docs-shots surface hash

CI's `guard-docs-shots` flagged the `web/public/**` change. Regenerating the
manual on this branch and on `origin/main` on the same machine gave
byte-identical PNGs except `en/inventory.png`, which differs only by the
LAN "Found a main till on this network" banner (mDNS discovery of a real
till during one run) — environment, not this diff. So the surface hash was
refreshed without new screenshots (`Docs-Shots-Unchanged: true`).
