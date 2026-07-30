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

// Regression: the sale screen's scan input has `autofocus`, and on slow
// devices (the real Pi, busy CI runners) it gains focus BEFORE the
// deferred osk.js attaches its focusin listener — so no focusin ever
// fires for it and the forced-on keyboard silently never appeared until
// focus left and came back. Reproduced deterministically here by delaying
// osk.js so autofocus always wins the race; osk.js must catch up with the
// already-focused element at init.
test('the OSK still appears when the autofocused field wins the load race', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');

  await page.route('**/osk.js*', async (route) => {
    await new Promise((r) => setTimeout(r, 700)); // autofocus fires well before this
    await route.continue();
  });
  await page.goto('/');
  await expect(page.locator('body')).toHaveAttribute('data-osk', 'on');
  // No click, no refocus — the autofocused scan field alone must be enough.
  await expect(page.locator('#osk')).toBeVisible();
  await expect(page.locator('#osk .osk-key').first()).toBeVisible();
  await page.unroute('**/osk.js*');
  assertClean();
});
