import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#46: cashier can name a held sale (e.g. "Tab 1") instead of it
// always falling back to the CRM customer name or a bare timestamp.
//
// ut-docs#1284 review finding: this file has no reset between tests, and
// the "cancelling" test below deliberately leaves Coca-Cola in the basket
// -- so every `.scan-row input[name="code"]` fill() here is scoped
// directly to the scan field rather than `getByRole('textbox').first()`,
// which would resolve to a leftover basket qty-input instead once one
// exists (a basket qty-input is type="text" -> ARIA role "textbox" since
// ut-docs#1284's own decimal-corruption fix; it used to be type="number"
// -> role "spinbutton", which `.first()` safely skipped). Also reset the
// basket after each test so this file stops relying on later tests
// tolerating whatever an earlier one left behind.
test.afterEach(async ({ page }) => {
  await page.request.post('/api/pos/reset').catch(() => {});
});
test('holding a sale with a typed name shows that name in the held strip', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();

  // Regression guard for a real bug an independent review caught: dialog.
  // showModal() makes the rest of the document inert, which would silently
  // block the custom on-screen keyboard (appended to <body>, outside the
  // dialog) that kiosk Pis rely on to type into this very field. The dialog
  // must be opened non-modal (dialog.show()) so the rest of the page stays
  // interactive while it's up.
  expect(await page.evaluate(() => document.body.inert)).toBe(false);
  expect(await page.evaluate(() => document.querySelector('input[name=code]')?.disabled)).toBe(false);

  await page.locator('#hold-label-input').fill('Tab 1');
  await page.locator('#hold-modal button[type=submit]').click();

  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
  await expect(page.locator('#held-sales')).toContainText('Tab 1');
  assertClean();
});

test('cancelling the name prompt does not hold the sale', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();
  await page.locator('#hold-label-input').fill('should not be held');
  await page.locator('#hold-modal button', { hasText: 'Cancel' }).click();

  await expect(page.locator('#hold-modal')).toBeHidden();
  // The basket is untouched -- cancel never posted /api/pos/hold.
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  assertClean();
});

test('holding with a blank name still falls back to a timestamp label', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/');

  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');

  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();
  await page.locator('#hold-modal button[type=submit]').click();

  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#held-sales')).toContainText(/\d{2}:\d{2}/);
  assertClean();
});
