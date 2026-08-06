import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#303: catalog import used to show the operator raw Go errors with
// internal UUIDs ("barcode already assigned to item e5454794-…") and the
// whole import status vocabulary was untranslated English literals. This
// drives a REAL browser through a commit that hits both a barcode conflict
// and the (unrelated) unsupported-barcode-shape warning, in a non-English
// locale (Turkish — ?lang=tr), and asserts on the rendered page rather than
// the JSON: the conflicting item is named, no raw UUID ever reaches the
// screen, the status text is genuinely translated (not English), and the
// warned row carries a real, geometrically-distinct visual treatment (not
// just a class name — same standard as sale-screen-213.spec.ts) rather than
// blending in with the clean rows around it.
test.describe('catalog import: friendly errors + translated statuses (ut-docs#303)', () => {
  test('barcode-conflict row names the item, stays UUID-free, and is visually + textually distinct', async ({ page }) => {
    const assertClean = watchConsole(page);

    await page.goto('/import?lang=tr');
    await expect(page.locator('input[type=file]')).toBeVisible();

    // Two rows sharing a barcode: the second one's attach fails against
    // the first, which actually holds it. A third row's barcode is an
    // unsupported shape (never blocks the import, just warns — also
    // exercises the row-warn treatment). A fourth, clean row is the
    // baseline the geometric assertion below compares against — NOT
    // "Widget Three", which is itself warned.
    const csv =
      'Name,SKU,Barcode,Price,Category,In stock\n' +
      'Import Widget One,IW303A,5019283746102,1.50,Snacks,7\n' +
      'Import Widget Two,IW303B,5019283746102,2.00,Snacks,4\n' +
      'Import Widget Three,IW303C,4011,0.59,Produce,0\n' +
      'Import Widget Four,IW303D,5019283746199,3.00,Snacks,0\n';

    await page.setInputFiles('input[type=file]', {
      name: 'import-303.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    });
    // The Import button sets the hidden commit=1 field before submit.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/import')),
      page.getByRole('button', { name: /İçe Aktar|Import/i }).last().click(),
    ]);

    const result = page.locator('#import-result');
    await expect(result).toContainText('Import Widget Two');

    // Names the conflicting item ("Import Widget One"), never its raw ID.
    await expect(result).toContainText('Import Widget One');
    const html = await result.innerHTML();
    expect(html).not.toMatch(/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/);

    // Genuinely translated, not the English literal — proves this isn't
    // just reachable in en, the locale most manual testing defaults to.
    expect(html).toContain('tarafından kullanılıyor'); // "already in use by"
    expect(html).not.toContain('already in use by');
    expect(html).not.toContain('barcode attach failed');

    // The warned row (Import Widget Two) is a real, geometrically distinct
    // treatment, not merely a class name string in the markup.
    const warnedRow = page.locator('tr.row-warn', { hasText: 'Import Widget Two' });
    await expect(warnedRow).toBeVisible();
    await expect(warnedRow.locator('.row-warn-icon')).toBeVisible();

    const cleanRow = page.locator('tr', { hasText: 'Import Widget Four' });
    const [warnedBg, cleanBg] = await Promise.all([
      warnedRow.locator('td').first().evaluate((el) => getComputedStyle(el).backgroundColor),
      cleanRow.locator('td').first().evaluate((el) => getComputedStyle(el).backgroundColor),
    ]);
    expect(warnedBg).not.toBe(cleanBg);

    // The unsupported-shape warning (unrelated to the conflict) is ALSO
    // translated, proving the whole vocabulary moved, not just the one
    // barcode-conflict message.
    await expect(result).toContainText('4011');
    expect(html).toContain('içe aktarılmadı'); // "not imported"

    assertClean();
  });
});
