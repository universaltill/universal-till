import { test, expect } from '@playwright/test';
import { watchConsole, setOskMode } from './helpers';

// ut-docs#1177: the product owner typed SKU/barcode '30005' into the sale
// screen's scan field on the real Pi 5 till via the on-screen keyboard and it
// "didn't find it to add it to the basket" — injecting the exact same code
// directly over SSH (bypassing the touchscreen/OSK entirely) worked
// immediately, which rules out the resolution chain (PriceResolverAdapter,
// the /api/pos/scan endpoint, htmx wiring) and the item's own data. The bug
// is in how the operator's on-screen input reaches the form. Two candidate
// paths, per the issue's own theories:
//   1. the OSK's own '↵' key (osk.js press('↵')) fails to submit the form.
//   2. the visible "Add" submit button's tap is swallowed (ut-docs#1170's
//      touch-drag-selects-text bug, same root cause as that card).
// This drives both paths with real click events (not .fill()/keyboard
// synthesis, which wouldn't exercise osk.js's own click handler at all) to
// tell them apart.
const BARCODE = '2000010000012'; // Coca-Cola 330ml (internal/db/migrations/001_init.sql)

async function typeViaOsk(page: import('@playwright/test').Page, digits: string) {
  for (const d of digits) {
    await page.locator(`#osk button[data-k="${d}"]`).click();
  }
}

test.describe('sale screen scan field via the on-screen keyboard (ut-docs#1177)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await setOskMode(page, 'auto');
  });

  test('typing a code via the OSK and pressing its own ↵ key adds the item (theory 1)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');

    const code = page.locator('form.scan-row input[name="code"]');
    await code.click();
    await expect(page.locator('#osk')).toBeVisible();

    await typeViaOsk(page, BARCODE);
    await expect(code).toHaveValue(BARCODE);

    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('#osk button[data-k="↵"]').click(),
    ]);

    // form.scan-row's own hx-target is #basket, not itself — the response
    // never re-renders the scan-row form, so the code field is left holding
    // whatever was typed. Assert what the handler actually does (rings up
    // the item), not a field-clear behavior this form doesn't implement.
    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');

    assertClean();
  });

  test('typing a code via the OSK and tapping the on-screen Add button adds the item (theory 2 / ut-docs#1170)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    await page.goto('/');

    const code = page.locator('form.scan-row input[name="code"]');
    await code.click();
    await expect(page.locator('#osk')).toBeVisible();

    await typeViaOsk(page, BARCODE);
    await expect(code).toHaveValue(BARCODE);

    // The reported flow: type via the OSK, then tap the visible "Add"
    // button rather than the OSK's own ↵ key.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('form.scan-row button[type="submit"]').click(),
    ]);

    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');

    assertClean();
  });
});
