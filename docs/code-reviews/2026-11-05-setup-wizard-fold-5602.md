# Review: setup wizard Back/Next above the fold at 1024×600 on a PIN mismatch (ut-docs#5602)

Date: 2026-11-05 · Branch: `fix/5602-setup-wizard-fold` · Lane: cloud-24
Built by a Sonnet subagent. Reviewed independently by an Opus subagent. Tester was a separate agent.

## What shipped

- `web/public/app.css`: `@media (max-height: 700px) { .setup-card { margin-block-start: 1rem; } }`
  right after the `.setup-card` rule. The card's `8dvh` top margin was 48px of dead space at
  600px tall. On a PIN mismatch the server re-renders step 6 (admin PIN, not the country step as
  the card said), and Back/Next ended at y≈609, 9px below the fold. With the fix they end at
  y≈578 (en) / 563 (tr, fa, ar). Screens taller than 700px (360×740 phones, 1024×768,
  1280×800) are unchanged. It is a logical property, so it is RTL-safe.
- `e2e/tests/login.spec.ts`: a new test in the first-boot serial block, before the wizard is
  completed. For 1024×600, 360×740 and 1280×800 it opens its own context and goes to `/setup`
  (asserting that it doesn't redirect). It jumps the Alpine step to 6, submits mismatched PINs
  through the real `POST /api/setup`, and waits for the server's "PINs do not match" alert. It
  then asserts that both step-6 buttons have top ≥ 0, bottom ≤ innerHeight and height ≥ 44, and
  that nothing overflows horizontally.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | Blocker | The first version was a separate spec in `AUTH_ONLY_SPECS`. The auth project runs its files alphabetically on one till, and `login.spec.ts` completes the wizard first, so `/setup` redirected and the spec crashed in a full run (reproduced: 20 passed, 3 failed). | Fixed: the test moved into `login.spec.ts`'s serial block, before the wizard completes. The separate spec and the config entry are removed. The full `login.spec.ts` passes 21/21. |
| R2 | Low | The fix leaves 22px of headroom at 1024×600. One more wrapped line would use most of it. | Accepted. `gap: .6rem` is the next step if a locale needs it. The Tester measured tr, fa and ar: all fit. |
| R3 | Info | At 800×480 (Pi 7" panel) and 740×360 (landscape phone), Back/Next are still far below the fold. This predates the change, which made it 32px better. | Deferred → ut-docs#5604. |
| R4 | Nit | Deprecated `waitForNavigation`; no wait for fonts before measuring. | Fixed: `waitForResponse` on the POST plus `document.fonts.ready`. |

## Verification beyond unit tests

- **Red/green:**
  - Dev, Tester and Reviewer each reverted only the app.css hunk. The 1024×600 case failed with
    `Expected <= 600, Received 609.47`, and the phone and desktop cases passed.
  - With the fix restored it passes. After R1 the red check was repeated on the moved test.
- **e2e:** `npx playwright test --project=auth login.spec.ts` passes 21/21 after the fix, so a
  failed mismatched-PIN POST leaves the till in first-boot state for the later serial tests.
- **Driven run (Tester):** a fresh till with the PIN-mismatch state.
  - Screenshots at 1024×600 in en, tr, fa and ar, and at 360×740 in en, fa and tr.
  - Buttons are fully visible and 51px tall. RTL mirrors correctly, with no horizontal scroll.
  - Steps 1, 4 and 5 at 1024×600 fit. Steps 2, 3 and 7 scroll for their own long content, as
    before (→ #5604).
  - No German pack is bundled, so tr stood in as the long locale. Dark theme was not checked
    (spacing only).
- **Reviewer checks:**
  - Cascade: the media rule has the same specificity and comes later, and no later margin rule
    overrides it. `.setup-card` is used only in `setup.html`.
  - Computed margin is 17px at ≤700px tall and 8dvh at 1024×768.
- **Guards:** `guard-rtl-logical-css.sh`, `guard-fixed-dates.sh` and `guard-i18n.sh` pass.

Pre-flight: web/help n/a (spacing only, no copy or flow change); docs-shots n/a (they never capture /setup); reference/ n/a; README n/a; en.json/meta n/a (no keys); ADR n/a; review record updated; follow-up deferred → ut-docs#5604.

## Verdict

Approved after R1 and R4.
