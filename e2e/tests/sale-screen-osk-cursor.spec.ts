import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#155: on a touch till, the sale screen's autofocused barcode
// input used to auto-pop the on-screen keyboard on load (osk.js's default
// AUTO mode shows on any touch-capable device) and no cursor-hiding
// existed for kiosk builds. Fixed via a field-level data-osk="off" (see
// settings-osk.spec.ts for the "admin forces OSK Always-on still wins"
// guarantee that opt-out must not break) plus a deliberate toggle button,
// and `cursor: none` scoped to body.kiosk.

test('touch device: scan input suppresses auto-OSK by default; toggle opens it deliberately', async ({ browser }) => {
  // The default project's page fixture is a non-touch context (see the
  // comment in settings-osk.spec.ts) — auto mode never shows OSK there
  // regardless of this fix, so this needs its own touch-emulated context
  // to actually exercise the suppression this card is about. (newContext
  // inherits the project's baseURL — verified: page.url() after goto('/')
  // resolves to the real server, not a bare '/'.)
  const touchContext = await browser.newContext({ hasTouch: true, viewport: { width: 1024, height: 600 } });
  const touchPage = await touchContext.newPage();
  const assertTouchClean = watchConsole(touchPage);

  await touchPage.goto('/');
  await expect(touchPage.locator('body')).toHaveAttribute('data-osk', 'auto');
  await expect(touchPage.locator('input[name=code]')).toBeFocused();
  // Would auto-show here pre-fix (autofocus + touch + auto mode) — must not.
  await expect(touchPage.locator('#osk')).toHaveCount(0);

  // Deliberate open still works.
  await touchPage.locator('#scan-keyboard-open').click();
  await expect(touchPage.locator('#osk')).toBeVisible();
  await expect(touchPage.locator('#osk .osk-key').first()).toBeVisible();
  await touchPage.locator('#osk button[data-k="1"]').click();
  await expect(touchPage.locator('input[name=code]')).toHaveValue('1');

  // The open is a one-shot, not a permanent latch: blurring the field
  // (e.g. tapping the Add button) must restore the suppression, so a
  // later refocus (the "New Sale" reset re-focuses this same field)
  // doesn't re-pop the keyboard on every subsequent sale.
  await touchPage.locator('.scan-row button[type=submit]').click();
  await expect(touchPage.locator('input[name=code]')).toHaveAttribute('data-osk', 'off');

  assertTouchClean();
  await touchContext.close();
});

test('kiosk mode hides the cursor everywhere, not just over bare background', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');
  // The server only adds body.kiosk under UT_KIOSK=1 (not this e2e
  // server) — the class itself and its wiring are pre-existing, untouched
  // by this card, so simulate it here to verify the new CSS rule in
  // isolation rather than standing up a second kiosk-mode server.
  await page.evaluate(() => document.body.classList.add('kiosk'));
  await expect(page.locator('body')).toHaveCSS('cursor', 'none');
  // cursor is inherited — the meaningful check is a descendant that
  // declares its OWN cursor (.btn sets cursor: pointer) still ending up
  // none, not just the body element itself.
  await expect(page.locator('.scan-row button[type=submit]')).toHaveCSS('cursor', 'none');
  assertClean();
});
