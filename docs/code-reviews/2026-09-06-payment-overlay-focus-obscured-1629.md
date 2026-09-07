# Code review: covered payment-overlay originals drop out of tab order (ut-docs#1629)

**Branch:** `fix/1629-payment-overlay-focus-obscured`
**Card:** universaltill/ut-docs#1629 (found reviewing #1625, complexity:medium)
**Reviewer:** independent Opus subagent, isolated worktree (Sonnet wrote the fix)

## What shipped

`#payment-overlay` opens non-modally (`.show()`, ut-docs#1385 — the custom
on-screen keyboard must stay usable while it's open), so nothing outside it
becomes `inert`. #1625 gave the ORIGINAL Hold Sale / New Sale buttons in
`.tender-default-footer` their own unambiguous accessible name while the
overlay is open, but — correctly, per its own review — did not address
focus *visibility*: at desktop viewports where the overlay's fixed,
right-anchored 26rem panel geometrically covers that footer (measured live
up to ~1440px, `payment-overlay-footer-reachable-1542.spec.ts`), the
originals stayed in the keyboard tab order with no visible focus indicator
anywhere on screen — WCAG 2.2 SC 2.4.11 (Focus Not Obscured).

A blanket `inert` on `.tender-default-footer` was already considered and
rejected by #1625's own review: at WIDE viewports the originals are never
covered at all and are driven directly by an existing, already-passing spec
(`new-sale-closes-payment-overlay-1386.spec.ts`, 1920x1080) — `inert`-ing
the whole footer would silently break that already-legitimate path too.

Fix, narrower than that: `tabindex="-1"` on just the two originals
(`kiosk-checkout-start`, and a new `data-testid="tender-footer-hold"` added
to the original Hold Sale button), applied only while the overlay is open
AND only while they are actually covered. Rather than re-encode the
"~1440px" figure as a second, driftable magic breakpoint, `web/public/app.js`
reuses the exact same center-point `elementFromPoint` hit-test
`payment-overlay-footer-reachable-1542.spec.ts`'s own e2e spec already uses
to prove coverage — so it tracks the real, current geometry (this file's
responsive grid, the overlay's own CSS, RTL) instead of a number someone
has to remember to update. A `MutationObserver` on the overlay's `open`
attribute (covers every `.show()`/`.close()` call site uniformly) plus a
`resize` listener while open keep the tab-order state live; a small
sentinel attribute (`data-a11y-tabindex-saved`) restores whatever tabindex
each button had before (normally none) once it's no longer covered or the
overlay closes. Deliberately excludes the third footer button (Payment,
the overlay's own trigger) and the phone-width New Sale duplicate
(`kiosk-checkout-start-phone`) — neither was raised by #1625's review or is
in this card's scope.

New e2e spec: `payment-overlay-focus-obscured-1629.spec.ts` — asserts
`tabindex="-1"` on both originals at 1024x600 while the overlay is open
(cleared again on close), and asserts it's absent at 1920x1080 (backed by
the same hit-test technique, confirming they're genuinely not covered
there — a negative control against a future over-fix reintroducing
`inert`-like behavior at wide viewports).

## Independent review — findings

**Verdict: PASS, approved to merge.** No blocking findings. The reviewer
swept 15 viewport widths (901–1920px) measuring true area-coverage
fraction against the hit-test verdict for both buttons: a 100%-covered
button always produced a "covered" verdict at every width tested, and the
worst partial-coverage case that stayed focusable (Hold Sale, 37.9%
covered / 62% visible at 1600px) is WCAG-conformant (SC 2.4.11 only
requires the component not be *entirely* hidden) — the hit-test method
degrades conservatively, never under-protecting. RTL was verified live
(not just reasoned about): at `?lang=fa` the overlay correctly anchors
left, button order mirrors, and both originals still correctly receive
`tabindex="-1"` — the JS has no physical-direction assumption since it
only asks the DOM what's on top of each element's own rect. The on-screen
keyboard reflow case (`body.osk-padded` shrinking the overlay) was also
checked live and handled correctly — the coverage verdict stays correct
through the reflow.

**Two non-blocker findings, one fixed in this diff, one deferred:**

- **Fixed (F2):** the code comment above the `MutationObserver` incorrectly
  claimed Escape was one of the close paths it needed to cover. Verified
  live: Escape does **not** close this dialog (only `showModal()`'d/
  top-layer dialogs dismiss on Escape; this one is deliberately `.show()`n,
  ut-docs#1385). Corrected the comment; no behavior change — the observer
  logic was already correct regardless, since it only ever needed to cover
  paths that actually exist.
- **Deferred as a new Backlog card (F1):** the reviewer's same coverage
  sweep found the Payment trigger button (the overlay's own opener) is
  **100% covered at every width tested, 901–1920px** — unconditionally, not
  just at narrow viewports like the two buttons this card fixes — and still
  focusable. More significantly, `.tender-quickpay`'s one-tap charge button
  is also fully covered (901–1500px) and still focusable; unlike Payment,
  activating it isn't a no-op — it POSTs `/api/pos/tender` and charges the
  sale directly, so a keyboard operator could complete a charge on a
  control they cannot see. The phone-width New Sale duplicate
  (`kiosk-checkout-start-phone`) has the same latent issue at ≤480px.
  None of these were raised by #1625's review or are in this card's scope,
  so not fixed here — filed as ut-docs#1673-adjacent new card (see
  close-out comment on the issue for the exact number) rather than widening
  this diff. The `targets` array in `updateFocusability()` is written to
  make that follow-up a one-line extension.

Two non-blocking nits noted, not changed: the `resize` listener is never
removed (a page-lifetime IIFE on the sale screen, not a real leak) and is
unthrottled (two forced reflows per resize event while open — negligible
next to the grid relayout the resize itself already causes on this fixed
kiosk-class layout).

## Verified beyond automated tests

- **TDD, independently re-verified in an isolated worktree** (not just
  taken on the implementer's word): reverted `web/public/app.js` only,
  confirmed the 1024x600 case fails with exactly the claimed error
  (`tabindex` expected `"-1"`, received `""`/`null`) at the exact assertion
  line, confirmed the 1920x1080 negative control still passes (as it must
  — it asserts the *absence* of `tabindex="-1"`), then restored and
  confirmed both pass again.
- **Real keyboard Tab-key walk** (not just the attribute assertion): pressed
  Tab repeatedly from the overlay's close button at 1024x600 with the
  overlay open; `document.activeElement` sequence never included
  `kiosk-checkout-start`/`tender-footer-hold`, landing on the in-overlay
  duplicates (`payment-overlay-new-sale`/`payment-overlay-hold`) instead.
  Screenshot taken and read: layout renders correctly, nothing overlapping
  or misaligned, in-overlay duplicates render as expected.
- Full regression set green: `payment-overlay-footer-reachable-1542.spec.ts`,
  `new-sale-closes-payment-overlay-1386.spec.ts`,
  `payment-overlay-duplicate-labels-1625.spec.ts`,
  `payment-overlay-osk-1385.spec.ts`, plus the new spec — 13/13.
- `gofmt -l .`, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), full `go test ./...` — all green
  (no Go files touched by this change; run for completeness per the
  standing gate).
- Guards: `guard-i18n.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-docs-shots.sh` (regenerated via `make docs-shots`; PNG diff
  decoded byte-for-byte — 30 of 1,843,800 raw bytes differ, all ±1
  antialiasing jitter on ~10 pixels in one glyph region, no structural
  change), `guard-compliance-claims.sh`, `guard-help-topics.sh`, plus every
  other CI-blocking guard in the `build` job — all green.
- RTL (fa): live-verified as above, not just existing coverage re-run.
- No real client/shop name, no secret-shaped literal introduced.
- Manual (`web/help/`): correctly not updated — this changes tab order
  only, nothing a sighted shop owner sees or does; `guard-help-topics.sh`
  passes and screenshots are regenerated (fresh hash) but show no
  structural change.

## Safe to merge

Yes.

## Deferred (new Backlog card)

- Payment trigger / quick-pay one-tap-charge button / phone-width New Sale
  duplicate all measurably stay focusable while 100% visually covered by
  the open overlay (same WCAG 2.4.11 class as this card) — the quick-pay
  case is the more urgent of the three (an invisible control that charges
  the sale on Enter). Filed as a new Backlog card in the same cycle; not
  fixed here as out of scope for #1629.
