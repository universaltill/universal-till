import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2902: Settings saves that only change their own card used to
// reload the whole page; they now re-render only a region around the form
// (UT.refreshRegion). A marker on `window` proves no reload happened, a
// stale attribute on the old form proves the region really was replaced
// from the server, and the section switcher must still drive the card
// afterwards (regions sit inside the .card, which the switcher holds).
test('settings: invoice, telemetry and till-name saves refresh their region, no page reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings#settings-invoice');
  const card = page.locator('#settings-invoice');
  await expect(card).toBeVisible();
  await page.evaluate(() => { (window as any).__ut2902 = 'still-here'; });

  const region = page.locator('#settings-invoice-region');
  await region.locator('form').evaluate((f) => f.setAttribute('data-stale', '1'));
  await region.locator('input[name="seller_name"]').fill('Probe Seller 2902');
  await region.getByRole('button').click();
  await expect(region.locator('form[data-stale]')).toHaveCount(0); // region was replaced
  await expect(page.locator('#settings-invoice-region input[name="seller_name"]')).toHaveValue('Probe Seller 2902');
  // Identical content comes back, so the region says it saved.
  await expect(page.locator('#invoice-settings-msg .pos-notice.success')).toBeVisible();
  await expect(card).toBeVisible();
  expect(await page.evaluate(() => (window as any).__ut2902), 'page was reloaded').toBe('still-here');

  // Telemetry: a checkbox round-trips through the server render.
  await page.goto('/settings#settings-telemetry');
  await page.evaluate(() => { (window as any).__ut2902 = 'still-here'; });
  const tel = page.locator('#settings-telemetry-region');
  const box = tel.locator('input[name="optIn"]');
  const was = await box.isChecked();
  await tel.locator('form').evaluate((f) => f.setAttribute('data-stale', '1'));
  await box.setChecked(!was);
  await tel.getByRole('button').click();
  await expect(tel.locator('form[data-stale]')).toHaveCount(0);
  await expect(page.locator('#settings-telemetry-region input[name="optIn"]')).toBeChecked({ checked: !was });
  expect(await page.evaluate(() => (window as any).__ut2902), 'page was reloaded').toBe('still-here');

  // The section switcher still shows the card after its region swapped:
  // go to another section and back.
  await page.evaluate(() => { location.hash = '#settings-invoice'; });
  await expect(page.locator('#settings-invoice')).toBeVisible();
  await page.evaluate(() => { location.hash = '#settings-telemetry'; });
  await expect(page.locator('#settings-telemetry')).toBeVisible();
  await expect(page.locator('#settings-telemetry-region form')).toBeVisible();

  // Till name (Tills card, two forms with a region each).
  await page.goto('/settings#settings-tills');
  await page.evaluate(() => { (window as any).__ut2902 = 'still-here'; });
  const tn = page.locator('#settings-till-name-region');
  await tn.locator('form').evaluate((f) => f.setAttribute('data-stale', '1'));
  await tn.locator('input[name="name"]').fill('Probe Till 2902');
  await tn.getByRole('button').click();
  await expect(tn.locator('form[data-stale]')).toHaveCount(0);
  await expect(page.locator('#settings-till-name-region input[name="name"]')).toHaveValue('Probe Till 2902');
  await expect(page.locator('#till-name-msg .pos-notice.success')).toBeVisible();
  expect(await page.evaluate(() => (window as any).__ut2902), 'page was reloaded').toBe('still-here');
  assertClean();
});

// The printer region holds the LAN-discovery button; its listener is
// delegated from the card, so it must still work after a save swapped the
// region. The scan endpoint is stubbed (no printers on the CI network).
test('settings: printer discovery still works after the printer region swapped', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.route('**/api/kitchen-stations/discover-printers', (r) => r.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ data: { printers: [{ name: 'Probe Printer 2902', address: '192.0.2.29:9100' }] }, error: null }),
  }));
  await page.goto('/settings#settings-printer');
  await page.evaluate(() => { (window as any).__ut2902 = 'still-here'; });
  const region = page.locator('#settings-printer-region');
  await region.locator('form').evaluate((f) => f.setAttribute('data-stale', '1'));
  await region.locator('form button[type="submit"]').first().click();
  await expect(region.locator('form[data-stale]')).toHaveCount(0);
  expect(await page.evaluate(() => (window as any).__ut2902), 'page was reloaded').toBe('still-here');

  await page.locator('#printer-discover-btn').click();
  const hit = page.locator('#printer-discover-results li', { hasText: 'Probe Printer 2902' });
  await expect(hit).toHaveCount(1);
  await hit.getByRole('button').first().click();
  await expect(page.locator('#printer-address-input')).toHaveValue('192.0.2.29:9100');
  assertClean();
});
