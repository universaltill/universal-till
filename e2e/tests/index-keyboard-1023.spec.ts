import { test, expect } from '@playwright/test';
import { watchConsole, setOskMode } from './helpers';

// ut-docs#1023 ("this is the second time it has come back" — ut-docs#155
// was the first): the till main page (`/`) popped the native OS keyboard
// again. Two sites were implicated:
//   1. the tender scan-barcode input, whose `autofocus` is the page's
//      effective load-time focus target (index.html's own static
//      `{{ if ne (oskmode) "off" }}inputmode="none"{{ end }}` guard, from
//      ut-docs#155, already covers this — Go-level coverage already exists
//      in internal/pages/index_osk_test.go's TestIndexScanRowKeyboardIsOnDemand;
//      the test below adds the missing END-TO-END proof that the field
//      osk.js/the browser actually land focus on at real page load is the
//      one that carries the guard, not just that the attribute exists
//      somewhere in the markup).
//   2. the hold-modal's `#hold-label-input` (opened via the "Hold Sale"
//      button, focused both by its own `autofocus` and by a `.focus()`
//      call in the opener's onclick) — this field had NO guard at all
//      until ut-docs#1022's central `guardField`/`guardSweep` fix, which
//      sweeps every OSK-able field on the page up front, before any
//      interaction, regardless of which template it lives in. index.html
//      itself was never touched by #1022 (it only added new coverage for
//      /catalog) so this is the first regression test that actually
//      exercises this exact page + flow — see the #1022 code-review record
//      for why a page-specific test still earns its place even though the
//      underlying fix is centralized: this rule has regressed twice
//      already on coverage that looked adjacent but didn't actually touch
//      the page in question.
//
// Scope note (independent review, this card): #1023's device state
// (`display.osk = auto`, touch-capable) is reproduced here via
// `hasTouch: true` + osk mode 'auto' — NOT `setOskMode(page, 'on')`, which
// reaches `enabled=true` through a different branch of osk.js's own
// enable check (mode==='on' vs. the coarse-pointer/touchy detection 'auto'
// relies on) and would not actually prove this page behaves correctly for
// the config the product owner reported.
//
// Scope note 2: this only proves the fix for locales osk.js has a layout
// for (en/tr/fa/ar — see `localeSupported()` in osk.js). On de/es (no OSK
// layout yet), guardField() deliberately does NOT suppress the native
// keyboard on a non-numeric field like this one — per ut-docs#1022's own
// review, suppressing it there with no replacement would leave literally
// no way to type at all. That gap is real, known, and separately tracked
// as ut-docs#1047 (add de/es OSK layouts) — not re-verified here.
test.use({ hasTouch: true });

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('till main page load: the field that gets autofocus already carries inputmode="none"', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'auto');

  await page.goto('/');

  const scan = page.locator('input[name=code]');
  // This is the page's one effective autofocus target: the hold-modal's
  // own autofocus field lives inside a closed <dialog>, which HTML's
  // autofocus processing skips (not focusable while the dialog is closed),
  // so the browser lands on this field instead — asserting that here keeps
  // the test honest about which field is actually load-bearing.
  await expect(scan).toBeFocused();
  await expect(scan).toHaveAttribute('inputmode', 'none');

  assertClean();
});

test('opening Hold Sale focuses the label field without popping any keyboard, but the field stays usable (ut-docs#1022 guard reaching this page)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'auto');

  await page.goto('/');
  await page.locator('input[name=code]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();

  const label = page.locator('#hold-label-input');
  await expect(label).toBeFocused();
  // Read focus + inputmode together in one evaluate: the field must
  // ALREADY be "none" at the instant it's focused, not merely "none"
  // within toHaveAttribute's default 5s retry window (a hypothetical
  // future guard that fires reactively, a couple seconds after focus,
  // would still pass a plain toHaveAttribute call here).
  const guardedAtFocusTime = await page.evaluate(() => {
    const el = document.activeElement as HTMLInputElement | null;
    return el?.id === 'hold-label-input' && el.getAttribute('inputmode') === 'none';
  });
  expect(guardedAtFocusTime, 'hold-label-input must carry inputmode="none" at the moment it receives focus, not eventually').toBe(true);

  // The actual #155/#1023 rule: opening is programmatic here (a `.focus()`
  // call, not a tap), so the OS keyboard must be suppressed (inputmode
  // above) AND osk.js's own custom keyboard must NOT auto-open either —
  // per osk.js's own comment, opening the custom OSK is click-only, never
  // focusin/programmatic focus.
  await expect(page.locator('#osk')).toBeHidden();
  // Liveness proof — #osk is built lazily, so "hidden" alone would also
  // pass if osk.js never loaded (same pattern as settings-osk.spec.ts and
  // hold-named-tab.spec.ts's sibling checks): a real tap must still open
  // it, or the operator would have no way to type into this field at all.
  await label.click();
  await expect(page.locator('#osk')).toBeVisible();

  // Complete the hold so the spec leaves the basket clean, per this
  // project's own rule (e2e/README.md) for any spec that adds a line. A
  // label distinct from hold-named-tab.spec.ts's "Tab 1" (whose own
  // assertion is a substring match) so a run ordered differently can never
  // let one spec's leftover satisfy the other's check.
  await label.fill('OSK guard 1023');
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');

  assertClean();
});
