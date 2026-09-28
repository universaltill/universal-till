import { test, expect } from './fixtures';
import { watchConsole, setBrowsingMode } from './helpers';

// ut-docs#3072: an item imported with no SKU and no barcode is stored with
// sku = NULL. Its sell tile carries the synthesized "item:<id>" code, which
// POSRepo.resolveItemByID resolves — but that query scanned the NULL sku into
// a Go string, the Scan failed, and the scan handler took the #2525
// stale-tile branch: no basket line, the "quick button was out of date" toast,
// and a grid refresh that reset the category chip to All. The pilot café hit
// it on 125 of 231 items. #1459's own spec never reached this tier: it taps a
// Designer quick button, which resolves through its shortcut_buttons row.
test.describe('all_filter_chips: a SKU-less item sells from its tile', () => {
  let itemId: string | null = null;

  test.afterEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
    await page.request.post('/api/pos/reset');
    if (itemId) await page.request.post('/api/catalog/item/deactivate', { form: { id: itemId } });
    itemId = null;
  });

  test('tapping it under a category chip adds one line, no stale toast, chip stays', async ({ page }) => {
    const assertClean = watchConsole(page);
    const stamp = Date.now();
    const name = `NullSku3072 Item ${stamp}`;
    const category = `NullSku3072 Cat ${stamp}`;

    await page.goto('/import');
    await page.setInputFiles('input[type=file]', {
      name: `import-3072-${stamp}.csv`,
      mimeType: 'text/csv',
      buffer: Buffer.from(`Name,SKU,Barcode,Price,Category,In stock\n${name},,,4.90,${category},1`),
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/import')),
      page.getByRole('button', { name: /Import/i }).last().click(),
    ]);
    await page.goto('/catalog');
    const row = page.locator('.catalog-row', { hasText: name });
    await expect(row).toBeVisible();
    itemId = await row.first().getAttribute('data-id');

    await setBrowsingMode(page, 'all_filter_chips');
    await page.goto('/');
    await page.request.post('/api/pos/reset');
    await page.reload();

    const chip = page.locator('#browsing-category-chips .chip', { hasText: category });
    await chip.click();
    await expect(chip).toHaveAttribute('aria-pressed', 'true');

    const tile = page.locator(`#buttons-grid-all .btn-tile[data-name="${name}"]`);
    await expect(tile).toBeVisible();
    await expect(tile).toHaveAttribute('data-code', /^item:/);
    await tile.click();

    await expect(page.locator('#basket-lines tr', { hasText: name })).toHaveCount(1);
    await expect(page.locator('.basket .total')).toContainText('4.90');
    await expect(page.locator('#toast-message')).toHaveCount(0);
    await expect(chip).toHaveAttribute('aria-pressed', 'true');

    assertClean();
  });
});
