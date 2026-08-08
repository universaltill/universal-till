import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#423: filed from independent review of ut-docs#418 (sale screen
// category tabs + search). #products-search (web/ui/partials/buttons.html)
// is a plain <input>, not in a form, with no Enter handler of its own — a
// cashier tapping it to look something up leaves focus there, and a wedge
// barcode scanner "types" its scan as fast keystrokes into whatever has
// focus. Reproduce with a real fast (scanner-speed) keystroke sequence via
// page.keyboard, not .fill() (which bypasses per-character keydown events
// entirely and would not exercise the app.js scan buffer either way).
// ut-docs#191: this shortcut_buttons barcode's check digit was corrected
// by migration 031 (was '2000010000017', a fabricated checksum) — kept in
// sync here since this test scans it by literal value, not by item name.
const BARCODE = '2000010000012'; // Coca-Cola 330ml (internal/db/migrations/001_init.sql)

test.describe('scan while focus is in another sale-screen field (ut-docs#423)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('a scan rings up the item, not filtered into the search box', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const search = page.locator('#products-search');
    await search.click();
    await expect(search).toBeFocused();

    // Scanner-speed keystrokes (well under app.js's 100ms fast-typing
    // window), landing in the focused search box exactly like a cashier's
    // tap-then-scan would.
    await page.keyboard.type(BARCODE, { delay: 5 });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.keyboard.press('Enter'),
    ]);

    await expect(page.locator('#basket')).toContainText('Coca-Cola 330ml');
    // The scan must not be left sitting in the search box as a stray filter
    // query — a barcode matches no product NAME, so every tile would stay
    // hidden and the panel would look empty from the next scan onwards.
    await expect(search).toHaveValue('');
    await expect(page.locator('.btn-tile', { hasText: 'Butter 250g' })).toBeVisible();

    assertClean();
  });

  // Review follow-up on the same handler: the search box isn't the only
  // field a cashier can leave focus in before scanning. The scan row's own
  // qty input is the dangerous one — pre-fix, "1" + a 13-digit barcode
  // submitted as a quantity of twelve trillion (basket total £14,400,012,
  // 000,020.40, observed live). The cleanup must strip exactly what the
  // scanner injected and leave the cashier's real quantity submitting.
  test('a scan with focus in the qty box rings up the cashier’s quantity, not the barcode', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    const qty = page.locator('form.scan-row input[name="qty"]');
    await qty.fill('3');
    await qty.click();
    await page.keyboard.press('End');
    await page.waitForTimeout(200); // let the scan buffer's fast-typing window lapse

    await page.keyboard.type(BARCODE, { delay: 5 });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.keyboard.press('Enter'),
    ]);

    const basket = page.locator('#basket');
    await expect(basket).toContainText('Coca-Cola 330ml');
    await expect(basket.getByTestId('basket-count')).toHaveText('3');
    await expect(qty).toHaveValue('3'); // barcode digits stripped, quantity intact

    assertClean();
  });

  test('typing a real search query into the search box still filters as normal', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');

    // Food is the default-active tab (sale-screen-category-tabs-search-418.spec.ts) —
    // search "Butter" (Food > Dairy) rather than a Drinks item, since search
    // filters WITHIN the active tab.
    const search = page.locator('#products-search');
    await search.click();
    await page.keyboard.type('Butter', { delay: 120 }); // human-speed, not scanner-speed
    await expect(page.locator('.btn-tile', { hasText: 'Butter 250g' })).toBeVisible();
    await expect(page.locator('.btn-tile', { hasText: 'Cheddar Cheese' })).toBeHidden();

    assertClean();
  });
});
