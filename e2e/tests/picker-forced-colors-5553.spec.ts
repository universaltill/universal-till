import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm, setOrderTypePromptMode } from './helpers';

// ut-docs#5553: under Windows High Contrast / forced colors the normal
// selected look (accent border + inset box-shadow + tint) is erased, so a
// selected picker tile / order-type option must be redrawn with system
// colours. Chromium emulates the mode via page.emulateMedia({ forcedColors: 'active' }). Screenshots
// are attached (no baselines) for the review record.

// `use: { forcedColors }` is silently dropped here (same as reducedMotion, see
// fixtures.ts), so the mode is set per page with emulateMedia.
test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ forcedColors: 'active' });
});

type Look = { bw: string; bg: string; bs: string };
const look = (el: Element): Look => {
  const s = getComputedStyle(el);
  return { bw: s.borderTopWidth, bg: s.backgroundColor, bs: s.boxShadow };
};

test('forced colors: selected colour tile is distinct from unselected', async ({ page }, testInfo) => {
  const assertClean = watchConsole(page);
  await page.goto('/catalog');
  expect(await page.evaluate(() => matchMedia('(forced-colors: active)').matches)).toBe(true);
  await openNewItemForm(page);
  const tiles = page.locator('#item-color-grid .item-color-tile');
  await tiles.nth(2).click();
  await expect(tiles.nth(2)).toHaveAttribute('aria-checked', 'true');
  const on = await tiles.nth(2).evaluate(look);
  const off = await tiles.nth(3).evaluate(look);
  // Thick edge is an inset ring, so the box (border + padding) does not grow.
  expect(on.bw).toBe(off.bw);
  expect(on.bs).not.toBe('none');
  expect(off.bs).toBe('none');
  expect(on.bg).not.toBe(off.bg);
  await testInfo.attach('catalog-colour-forced-colors.png', {
    body: await page.locator('#item-color-grid').screenshot(),
    contentType: 'image/png',
  });
  await closeItemForm(page);
  assertClean();
});

test('forced colors: checked order-type option is distinct from the other', async ({ page }, testInfo) => {
  const assertClean = watchConsole(page);
  await setOrderTypePromptMode(page, 'top');
  await page.request.post('/api/pos/reset');
  await page.goto('/');
  const on = await page.getByTestId('order-type-dine-in').evaluate(look);
  const off = await page.getByTestId('order-type-takeaway').evaluate(look);
  expect(on.bw).toBe('3px');
  expect(off.bw).toBe('1px');
  expect(on.bg).not.toBe(off.bg);
  await testInfo.attach('basket-order-type-forced-colors.png', {
    body: await page.locator('.order-type-toggle-group').screenshot(),
    contentType: 'image/png',
  });
  assertClean();
});
