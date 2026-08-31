import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setOskMode } from './helpers';

// ut-docs#1177: the product owner typed SKU/barcode '30005' into the sale
// screen's scan field on the real Pi 5 till via the on-screen keyboard and it
// "didn't find it to add it to the basket." Injecting the exact same code
// directly over SSH (bypassing the touchscreen/OSK entirely) worked
// immediately, which ruled out the resolution chain (PriceResolverAdapter,
// the /api/pos/scan endpoint, htmx wiring) and the item's own data.
//
// Root cause: web/public/app.js's hardware/wedge-scanner path (the
// window.addEventListener('keydown', ...) Enter handler) clears the code
// field after every submit via its own submit() helper. osk.js's '↵' key
// (press('↵') -> form.requestSubmit()) and a direct tap on the visible "Add"
// submit button both bypass submit() entirely and left the field holding
// its stale value — invisible on a FIRST scan of a fresh page (which is all
// an SSH-injected keystroke, or a naive single-scan test, ever exercises),
// but on every scan after the first the new code just concatenates onto the
// old one, producing a garbled code that resolves to nothing and surfaces
// exactly the reported "item not found" toast. This is why the SSH
// reproduction attempt (a real Enter keydown, which always takes the
// hardware path) could not reproduce it, while the operator's real,
// multi-scan touchscreen session did. Fixed by clearing the field on ANY
// submission of this form (app.js, delegated 'submit' listener), not only
// the hardware path.
//
// These barcodes are seeded shortcut_buttons rows (internal/data/seeddata/
// demo_catalogue.sql:366,374), corrected to a real EAN-13 check digit by
// migration 031 (internal/db/migrations/031_fix_shortcut_button_checksums.sql)
// — 001_init.sql's own seed still carries the pre-migration fabricated
// digit, so these values must stay in sync with 031, not with 001_init.sql.
const BARCODE_1 = '2000010000012'; // Coca-Cola 330ml
const BARCODE_2 = '2000010000098'; // Butter 250g — a second, distinct item

async function typeViaOsk(page: Page, digits: string) {
  for (const d of digits) {
    await page.locator(`#osk button[data-k="${d}"]`).click();
  }
}

async function scanViaOskEnter(page: Page, code: string) {
  const input = page.locator('form.scan-row input[name="code"]');
  await input.click();
  await expect(page.locator('#osk')).toBeVisible();
  await typeViaOsk(page, code);
  await expect(input).toHaveValue(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('#osk button[data-k="↵"]').click(),
  ]);
}

async function scanViaAddButton(page: Page, code: string) {
  const input = page.locator('form.scan-row input[name="code"]');
  await input.click();
  await expect(page.locator('#osk')).toBeVisible();
  await typeViaOsk(page, code);
  await expect(input).toHaveValue(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('form.scan-row button[type="submit"]').click(),
  ]);
}

test.describe('sale screen scan field via the on-screen keyboard (ut-docs#1177)', () => {
  test.beforeEach(async ({ page }) => {
    // The basket lives in the server-global engine, shared by every spec on
    // this till — without this, a line left behind by an earlier spec would
    // satisfy the basket assertions below before the scan under test ever
    // runs (ut-docs#1177 review, F2).
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await setOskMode(page, 'auto');
  });

  test('the OSK\'s own ↵ key submits, rings up the item, clears the field, and closes the keyboard', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');

    await scanViaOskEnter(page, BARCODE_1);

    const basket = page.locator('#basket');
    await expect(basket).toContainText('Coca-Cola 330ml');
    await expect(basket.locator('.pos-notice.error')).toHaveCount(0);
    await expect(page.locator('form.scan-row input[name="code"]')).toHaveValue('');
    await expect(page.locator('#osk')).toBeHidden();

    assertClean();
  });

  test('a second scan via the OSK ↵ key does not concatenate onto the stale field value (ut-docs#1177)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');

    await scanViaOskEnter(page, BARCODE_1);
    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');

    // Pre-fix, the field still held BARCODE_1 here — typing BARCODE_2 would
    // have produced their concatenation, matching neither item.
    await scanViaOskEnter(page, BARCODE_2);

    const basket = page.locator('#basket');
    await expect(basket).toContainText('Coca-Cola 330ml');
    await expect(basket).toContainText('Butter 250g');
    await expect(basket.locator('.pos-notice.error')).toHaveCount(0);

    assertClean();
  });

  test('typing via the OSK and tapping the visible Add button submits, rings up the item, and clears the field', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');

    await scanViaAddButton(page, BARCODE_1);

    const basket = page.locator('#basket');
    await expect(basket).toContainText('Coca-Cola 330ml');
    await expect(page.locator('form.scan-row input[name="code"]')).toHaveValue('');

    assertClean();
  });

  test('a second scan via the Add button does not concatenate onto the stale field value (ut-docs#1177)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');

    await scanViaAddButton(page, BARCODE_1);
    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');

    await scanViaAddButton(page, BARCODE_2);

    const basket = page.locator('#basket');
    await expect(basket).toContainText('Coca-Cola 330ml');
    await expect(basket).toContainText('Butter 250g');
    await expect(basket.locator('.pos-notice.error')).toHaveCount(0);

    assertClean();
  });
});
