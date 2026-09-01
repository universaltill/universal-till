# Code review: New Sale phone-width fallback (ut-docs#1345)

**Date:** 2026-09-01
**Branch:** `fix/1345-phone-new-sale-fallback`
**PR:** universaltill/universal-till#TBD
**Card:** ut-docs#1345 (`p3`, `complexity:easy`, `source:review`-adjacent —
independent-review finding from the ut-docs#1332 nav-rail change)

## What shipped

New Sale moved into `.tender-default-footer` (ut-docs#1332, next to Hold
Sale/Payment, per the product owner's own live request) and renders at
every width uniformly — but at 360x640 that footer sits below the fold,
reachable only by scrolling `.pos-container` (measured live pre-fix:
`kiosk-checkout-start` rect top:635/bottom:689 against a 640px viewport,
`document.elementFromPoint()` at its centre returned `null`).

Fix: duplicated New Sale into the existing `.kiosk-header.phone-fallback-only`
row (`web/ui/pages/index.html`) — the same shape of problem the row
already exists to solve for Inventory (`kiosk-inventory-link-phone`).
The new button (`data-testid="kiosk-checkout-start-phone"`) is
byte-identical in behavior to the `.tender-default-footer` copy
(`hx-post="/api/pos/reset"`, same `hx-target`/`hx-swap`/`hx-sync`, same
focus-the-scan-input `onclick`), reuses the existing `kiosk.checkout_start`
i18n key, and only differs in class (`compact`, matching its Inventory
sibling) and testid. The `.tender-default-footer` copy is untouched — this
is additive, not a move, so tablet/desktop placement doesn't change.

## TDD

Added `New Sale is reachable at 360px without scrolling .pos-container`
to `e2e/tests/phone-width-layout-413.spec.ts`, asserting: visible,
`.pos-container.scrollTop === 0` at the point of assertion (reachable at
the initial scroll position, not merely present further down), on-screen
bounding box, a genuine `elementFromPoint` hit-test, and that the
tender-footer's own copy is still attached (no regression/replacement).

Verified red→green myself, with a **freshly-built server each time** (not
a reused one — an earlier self-check with a stale reused Go binary gave a
false green against the reverted source, since the fix lives in a
server-rendered template, not client JS; caught this before trusting the
result): reverted `index.html`, killed any running till processes,
confirmed the test failed with a genuine "element(s) not found" against a
fresh server; restored the fix, confirmed all 12 tests in the file pass
again.

## Independent review

Spawned a fresh-context Sonnet subagent (this is `complexity:easy` — review
at Sonnet, different instance, per the `scrum-master`/`reviewer` skills'
model-routing table), read-only (asked not to modify anything or re-run
the e2e/docs-shots suites, since those were already run directly).

**Verdict: SAFE TO MERGE AS-IS.** No blocking findings. The subagent:
- Re-derived the diff itself (`git diff --cached`) rather than trusting
  the description.
- Checked `web/public/app.js` for anything keyed off `kiosk-checkout-start`/
  `kiosk-header`/`phone-fallback-only`/`kiosk-actions` — none found; the
  two buttons are toggled by plain CSS (`display:none`/`flex` on a
  `max-width:480px` media query), mutually exclusive, so exactly one is
  ever in the DOM's visible/hit-testable/a11y-tree state at once.
- Confirmed sharing `hx-sync="#basket:replace"` across multiple
  simultaneously-*mounted* (not simultaneously-visible) elements is
  already this file's own established pattern (scan-row form, hold-modal
  form, both quick-pay button variants) — not a new risk this change
  introduces.
- Confirmed `kiosk.checkout_start` already exists in all four locale files
  and none were touched — `guard-i18n.sh`'s checks pass.
- Confirmed the "single-line label" e2e assertion (`.kiosk-header .btn`,
  filtered to the currently-visible copy) is unaffected: the new button
  reuses the same `.btn.secondary.compact` class as the already-passing
  Inventory phone-fallback link, and `.kiosk-header`/`.kiosk-actions`
  already wrap at this breakpoint.
- Confirmed no Go/SQL touched (`guard-data-access.sh`/
  `guard-kiosk-engine.sh` structurally unaffected); `index.html` is a
  `{{ define "content" }}` partial, exempt from `guard-htmx-loaded.sh`.
- Pixel-diffed the regenerated `ar/tr` doc screenshots against the prior
  versions with PIL: 1×1 / 2×2 px bounding boxes — anti-aliasing noise,
  not a real layout change, consistent with the reference viewport
  (1024×600, well above the 480px breakpoint) never showing this row.
- Confirmed `manifest.json`'s only change is the aggregate surface hash
  (expected for any `web/ui/**` edit); no per-topic hash changed.
- No accessibility/ADR/self-order-isolation concerns.

## Beyond automated tests

Drove the real app directly (not just the subagent's static review):
- **360x640 (phone):** New Sale + Inventory both visible in one row above
  BASKET, no overlap, single-line labels; clicked New Sale, confirmed a
  real `POST /api/pos/reset` (200) fires and the basket resets.
- **1280x800 (desktop):** `.kiosk-header.phone-fallback-only` computed
  `display: none`; `.tender-default-footer`'s New Sale unchanged and
  visible — screenshot reviewed, no regression.
- **RTL (fa, ar) at 360px:** both buttons render single-line, fully
  on-screen (bounding-box right edge ≤ 360px in both locales), correct
  visual order flip (فروش جدید before انبار). Screenshot reviewed for fa.
- **tr at 360px:** same check, on-screen, single line.
- Not checked: dark-theme plugin variants (this row has no
  theme-conditional styling to begin with — same as its Inventory
  sibling) and tablet widths between 480–900px (out of scope: the fix is
  gated strictly to ≤480px, unchanged above that).

## Deferred / out of scope

Nothing deferred. Single-file markup fix + test, no follow-up card needed.

## Verdict

Safe to merge via `merge_method: "merge"` (never squash/rebase — see
`reviewer` skill's "Merge method" note, ut-docs#250).
