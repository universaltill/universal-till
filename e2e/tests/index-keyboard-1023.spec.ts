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
test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

test('till main page load: the field that gets autofocus already carries inputmode="none"', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

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

test('opening Hold Sale focuses the label field without popping the native keyboard (ut-docs#1022 guard reaching this page)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/');
  await page.locator('input[name=code]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();

  const label = page.locator('#hold-label-input');
  await expect(label).toBeFocused();
  // The actual regression: before ut-docs#1022, this field carried no
  // template guard and no central sweep existed, so the browser's own IME
  // opened here at focus time. It must already be "none" by the time focus
  // lands — proving the up-front sweep (not a reactive one keyed off a
  // click, which would be one interaction too late) is what's guarding it.
  await expect(label).toHaveAttribute('inputmode', 'none');

  // Complete the hold so the spec leaves the basket clean, per this
  // project's own rule (e2e/README.md) for any spec that adds a line.
  await label.fill('Tab 1023');
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');

  assertClean();
});
