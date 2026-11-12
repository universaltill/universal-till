# Review: phone width, Lock reachable over a scrim popup (ut-docs#6118)

Date: 2026-11-12 · Lane: lane:cloud-54 · Complexity: medium · Built by Sonnet, Architect/UX/Tester/review by Opus 5.5 (fresh contexts)

## What shipped
- At <=480px, Lock lives in the off-canvas drawer and the menu button (☰) sat under `#ut-scrim`
  (`html.ut-scrim-on .nav { pointer-events:none }`), so with any scrim popup open Lock was
  unreachable. Now, inside the existing <=480px block of `web/public/app.css`:
  - ☰ is lifted above the scrim (`pointer-events:auto`).
  - While the scrim is on and the drawer is open, `.nav` goes to z 595 (above every popup,
    including #ut-confirm 590 from universal-till#2511, and below `#osk` 1000) and the status
    bar goes to 596.
  - Only `.session-lock` is live in the drawer. The other rows are dimmed (opacity .45) and dead.
  - A1 (UX): the `body.osk-padded` hold/pfand/elevation/table-add popups start below the phone
    top bar, so ☰ stays tappable while the on-screen keyboard is up.
  - A2: Lock in the drawer gets the same white focus ring as ☰.
- `web/public/app.js` `setDrawer`:
  - When the scrim is on, it focuses Lock (which also drops the OSK) and scrolls it clear of the
    status foot.
  - A3: the dimmed rows get `aria-disabled="true"` while the scrim is on. A MutationObserver on
    `<html>` class keeps that in sync.
  - A capture-phase click guard makes a dimmed row dead for AT/synthetic activation too.
- `web/ui/layouts/base.html` `escAllowed` exempts `.nav-drawer-toggle`, so a focus-trapped popup
  does not pull focus back off ☰. Escape and a backdrop tap close only the drawer; the popup stays.
- 1024x600 is unchanged: pixel diff 0 on the popup case, and the rail is still z 460.
- Self-order kiosk pages render no nav, so there is still no ☰ and no Lock there (#4324).
- Exit-to-OS: at phone width it was never in the drawer. It is reached through Lock → `/login`
  (`.login-exit-os`), the same as at 1024.
- Help: `web/help/en/sell.md` gets one sentence. The ut-docs `reference/list-and-dialog-pattern.md`
  phone tier is in a separate ut-docs PR.
- New e2e `e2e/tests/phone-lock-over-scrim-6118.spec.ts` (7 tests, auth project).

## Findings (Opus review, round 1: REQUEST CHANGES, all fixed)
1. F1 (must, fixed): the focus-ring assertion passed on the UA default outline width. It now opens
   ☰ by keyboard and asserts `:focus-visible` with a non-`none` outline. Shown red with the A2 rule
   deleted.
2. F2 (must, fixed): the dead-row tap locator matched 3 elements and `.catch` swallowed it, so the
   tap never ran. It is now scoped to `#nav-drawer`, uses a real mouse click and no catch.
3. F3 (should, fixed): `dispatchEvent('click')` on a dimmed row navigated, because htmx's boosted
   link handler runs on the anchor. A capture-phase guard was added, and the spec asserts 0 htmx
   requests (shown red without the guard).
4. Doc nit (fixed): "click-through" → "dead". `#ut-confirm` 590 is attributed to
   universal-till#2511. #2511 merged first; this branch was rebased onto it, and its wrong CSS
   comment ("the rail is a top bar here and holds Lock") is corrected here. After the rebase: the new
   spec passes 7/7 and the #5524 confirm specs pass 20/20.
5. N4 (minor, accepted): a session-chip re-swap while the drawer is open is not re-synced for
   aria-disabled. The visual and pointer state is CSS and stays correct.
6. N7 (pre-existing, deferred): `#stock-dialog` on /inventory covers ☰ and has no Lock. Added to
   ut-docs#6125.
7. UX A4 (pre-existing, deferred → ut-docs#6125): the full-bleed `#item-form-modal`,
   `#import-modal` and `#tax-codes-modal` have no compact Lock affordance at any width.

## Verification
- TDD re-verified by the reviewer in an isolated worktree: with `web/` reverted, 5 of 7 fail
  (`<div id="ut-scrim"> intercepts pointer events`, A1 `top>=56`), and the 2 kiosk/1024 cases pass
  as expected. Restored, 7 of 7 pass.
- Tester (real PIN session, emulated touch, LTR + `?lang=ar`):
  - Covered the Open orders popup and the hold dialog with the real OSK.
  - Lock logs out to /login with exit-to-OS visible.
  - Dead rows do nothing.
  - Every target is >=48px.
  - The Lock label fits in en/tr/fa/ar and an injected German label.
  - Screenshots looked at.
- Not real hardware: emulated touch only, with no phone or Pi device run.
- After fixes: the new spec passes 7/7. Regression specs 1346/3297/3358/3407 pass (17/17) and
  2097 passes (17/17). Guards pass: rtl-logical-css, i18n, i18n-meta, no-showmodal, help-topics,
  help-breadcrumbs, help-terms, fixed-dates, compliance-claims, competitor-naming. gofmt is clean
  and `go build ./...` passes.
- `go test ./...`: all packages ok. `internal/plugins` needs more than the default 10m locally
  (907s alone); no Go changed.
- WebKit phone lane: n/a (auth-only specs are excluded there).

Pre-flight: help topic updated (sell.md); docs-shots n/a (no shot shows a popup with the phone
drawer); reference page updated (ut-docs list-and-dialog-pattern.md, separate PR); README n/a;
en.json/meta n/a (no new key); ADR n/a (applies the #1999/#2873 rule to the phone tier);
follow-ups deferred → ut-docs#6125.
