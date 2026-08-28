import { test, expect } from '@playwright/test';
import { watchConsole, setOskMode } from './helpers';

// A failed run used to leak osk=on into later specs (the open keyboard
// then covered the scan submit button in ui-scale-basket.spec.ts and its
// click hung) — restore 'auto' even when a test body fails.
test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

// Farshid's field report: "still don't see the on-screen keyboard". Auto
// hides it on non-touch (this browser) BY DESIGN; forcing On in settings
// must make the real keyboard appear when an input focuses.
test('forcing the OSK on shows a real keyboard', async ({ page }) => {
  const assertClean = watchConsole(page);
  // setOskMode waits for the POST *and then* the reload's load event, or
  // the next goto races the reload and aborts (flaked on CI).
  await setOskMode(page, 'on');

  await page.goto('/');
  await expect(page.locator('body')).toHaveAttribute('data-osk', 'on');
  await page.getByRole('textbox').first().click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(page.locator('#osk .osk-key').first()).toBeVisible();

  // And keys actually type into the focused field.
  await page.locator('#osk button[data-k="1"]').click();
  await expect(page.getByRole('textbox').first()).toHaveValue('1');

  assertClean();
});

// ut-docs#155 REVERSED an earlier decision: the OSK used to catch up with
// the autofocused scan field at init (so it auto-opened on every sale-screen
// load on touch tills — the exact behavior the field report rejects). Now
// programmatic focus (autofocus, .focus() calls) must NEVER open the OSK;
// only a deliberate tap on a field, or the data-osk-toggle button, does.
// The delayed-osk.js trick stays: even when autofocus wins the load race,
// the keyboard must stay closed — while the field itself stays focused so
// keyboard-wedge scanners keep working.
test('the OSK does NOT auto-open at load, even when the autofocused field wins the load race', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.route('**/osk.js*', async (route) => {
    await new Promise((r) => setTimeout(r, 700)); // autofocus fires well before this
    await route.continue();
  });
  const oskLoaded = page.waitForResponse((r) => r.url().includes('osk.js'));
  await page.goto('/');
  await expect(page.locator('body')).toHaveAttribute('data-osk', 'on');
  await oskLoaded; // osk.js has evaluated by now — its init must NOT show
  const scan = page.locator('input[name=code]');
  await expect(scan).toBeFocused();
  await expect(page.locator('#osk')).toBeHidden();
  // Prove osk.js is actually live (not just missing): a real click opens it.
  await scan.click();
  await expect(page.locator('#osk')).toBeVisible();
  await page.unroute('**/osk.js*');
  assertClean();
});

// The checkout-start button refocuses the scan input (for the scanner) via
// a delayed .focus() — programmatic, so it must not pop the keyboard.
test('checkout-start refocuses the scan input without opening the OSK', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/');
  await page.getByTestId('kiosk-checkout-start').click();
  const scan = page.locator('input[name=code]');
  await expect(scan).toBeFocused(); // the 150ms-delayed refocus ran
  await expect(page.locator('#osk')).toBeHidden();
  // Liveness proof — #osk is built lazily, so "hidden" alone would also
  // pass if osk.js never loaded; a real click must still open it.
  await scan.click();
  await expect(page.locator('#osk')).toBeVisible();
  assertClean();
});

// The on-demand path the field report asked for: a visible keyboard button
// on the scan row opens the OSK for the scan input, and closes it again.
test('the scan-row toggle opens and closes the OSK', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/');
  // Scoped to .scan-row: ut-docs#1048 added a second data-osk-toggle button
  // (the hold-sale dialog's, always in the DOM whether or not it's open) —
  // a bare [data-osk-toggle] now matches both.
  const toggle = page.locator('.scan-row [data-osk-toggle]');
  await expect(toggle).toBeVisible(); // osk.js reveals it when enabled
  await toggle.click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(page.locator('input[name=code]')).toBeFocused();
  // Keys type into the scan field (the toggle targeted it).
  await page.locator('#osk button[data-k="1"]').click();
  await expect(page.locator('input[name=code]')).toHaveValue('1');
  await toggle.click();
  await expect(page.locator('#osk')).toBeHidden();
  assertClean();
});

// ut-docs#1048: ut-docs#1022 suppressed the native keyboard everywhere the
// OSK is active, which left the hold-sale naming dialog's autofocused
// #hold-label-input with no visible way to open a keyboard (per ut-docs#155,
// the OSK never auto-opens on programmatic focus). Product owner's answer:
// add a data-osk-toggle button matching the scan-row pattern above.
test('the hold-sale dialog has its own OSK toggle for the label field', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.goto('/');
  await page.getByRole('textbox').first().fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();

  const toggle = page.locator('#hold-modal [data-osk-toggle]');
  await expect(toggle).toBeVisible();
  await toggle.click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(page.locator('#hold-label-input')).toBeFocused();
  await page.locator('#osk button[data-k="1"]').click();
  await expect(page.locator('#hold-label-input')).toHaveValue('1');

  // Cancel rather than hold — same non-destructive close hold-named-tab.
  // spec.ts's own cancel test uses; it leaves the basket item in place,
  // which is fine, the next test scans its own item and only checks for
  // its presence, not an exact basket count.
  await page.locator('#hold-modal button', { hasText: 'Cancel' }).click();
  assertClean();
});

// With the OSK disabled in settings, the toggle must not appear at all.
test('OSK mode off hides the scan-row toggle', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'off');

  await page.goto('/');
  await expect(page.locator('input[name=code]')).toBeFocused();
  // Both toggles on the page (scan-row's, and the hold-sale dialog's added
  // by ut-docs#1048) must stay hidden with the OSK off — a bare
  // [data-osk-toggle] now matches both, so assert each individually.
  await expect(page.locator('.scan-row [data-osk-toggle]')).toBeHidden();
  await expect(page.locator('#hold-modal [data-osk-toggle]')).toBeHidden();
  assertClean();
});
