# Review: focused field stays fully above the on-screen keyboard (ut-docs#5775)

Date: 2026-11-07 · Branch: `fix/5775-osk-input-above-keyboard` · Complexity: easy
Author: Sonnet (Dev subagent) · Reviewer: Opus (fresh-context subagent) ·
Tester: Sonnet (separate subagent)

## What shipped

- `web/public/osk.js`: `show()`'s deferred `scrollIntoView({block:'center'})`
  centred the field in the full viewport. The keyboard covers the bottom
  ~288px, so at 1024×600 a field centred at y=300 ended ~11px under `#osk`
  (top ~312). A new `clearKeyboard(el)` runs right after it: it measures the
  overlap with `#osk`'s top (8px gap) and scrolls it away — scrollable
  ancestors first, then the window — never past the field's own top or the
  scrolling container's visible top. Instant, vertical only, generic.
- `e2e/tests/osk-input-above-keyboard-5775.spec.ts`: at 1024×600, an 80px
  field on `/settings` placed where centring puts its bottom ~28px under the
  keyboard must end with bottom ≤ `#osk` top and top ≥ 0.

## Findings (Opus review)

1. Major (test) — the first spec version passed ~40% of runs with the fix
   reverted: content loading in after injection shifted the field ±20px
   against a 12px overlap. **Fixed**: wait for `networkidle`, prepend the
   block (later content can't shift it), 80px field. Re-verified by the
   orchestrator: reverted 5/5 fail (`Received: 340`), fixed 5/5 pass.
2. Minor — `hide()` (Enter / focus moving away) inside the 60ms window left a
   hidden `#osk` reading top=0, which would scroll the field to the top.
   **Fixed**: return unless `current === el` and `#osk` is open.
3. Minor — the cap used the viewport top, not the scrolling container's
   visible top: a tall textarea in a mid-screen panel could be scrolled
   under the panel edge. **Fixed**: per-ancestor cap against
   `max(0, container top)`.
4. Nit — inside a `position: fixed` overlay (item-form-modal,
   category-items-modal, record-dialog) the window fallback scrolled the
   page behind it. **Fixed**: stop at a fixed ancestor.

Checked, no problem: overflow-hidden ancestors skipped; `osk-padded` padding
applies in the same tick before the timer; `show()` early return; htmx
removing the field (`isConnected`); no user-facing strings; vertical only, so
RTL unaffected.

## Extra verification (Tester)

- Field in a fixed `overflow:auto` container using `--osk-reserved-height`,
  at 1024×600 and 800×480: ends 8px above the keyboard, container scrolls,
  page `scrollY` stays 0, no movement over the next 600ms.
- Field already above the keyboard: no scroll at all.
- `dir=rtl lang=fa` page: passes. Screenshot at 1024×600 looked at: field
  fully above the keyboard, not clipped.
- Not looked at: a real item-form / record modal (synthetic containers
  used), dark theme, real fa locale, phone viewports.
- All OSK specs: pass (three timing-sensitive specs flaked once each under
  a shared-port load in one full run and passed 30/30 re-run alone).
- `guard-i18n.sh`, `guard-rtl-logical-css.sh`, `guard-fixed-dates.sh`: pass.

Pre-flight: help topic n/a (no visible behaviour beyond the field no longer
hidden; no text or screens change); `en.json`/meta n/a (no keys); reference
page n/a; README n/a; review record updated (this file).

## Verdict

Approve. Deferred: none.

## CI follow-up

First CI run: `build` failed `guard-docs-shots_test.sh` (stale surface hash — `web/public/osk.js` is screenshot surface). No docs-shot opens the OSK and `clearKeyboard` runs only when it is open, so the hash was refreshed with `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`) instead of regenerating; guard + its test pass locally.
