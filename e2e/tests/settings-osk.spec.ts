import { test, expect, Page } from '@playwright/test';
import { watchConsole } from './helpers';

// The OSK mode is a SERVER-side setting shared by every spec on this
// server. Restore 'auto' even when a test body fails — a failed run used
// to leak osk=on into later specs (the open keyboard then covered the
// scan submit button in ui-scale-basket.spec.ts and its click hung).
async function setOskMode(page: Page, mode: string) {
  await page.goto('/settings');
  const osk = page.locator('form[hx-post="/api/settings/osk"] select');
  await osk.selectOption(mode);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/osk')),
    osk.locator('..').locator('button[type=submit]').click(),
  ]);
  await page.waitForEvent('load');
}

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
  const toggle = page.locator('[data-osk-toggle]');
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

// With the OSK disabled in settings, the toggle must not appear at all.
test('OSK mode off hides the scan-row toggle', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'off');

  await page.goto('/');
  await expect(page.locator('input[name=code]')).toBeFocused();
  await expect(page.locator('[data-osk-toggle]')).toBeHidden();
  assertClean();
});
