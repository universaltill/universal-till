import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#303: catalog import used to show the operator raw Go errors with
// internal UUIDs ("barcode already assigned to item e5454794-…") and the
// whole import status vocabulary was untranslated English literals. This
// drives a REAL browser through a commit that hits a barcode conflict, in a
// non-English locale (Turkish — ?lang=tr), and asserts on the rendered page
// rather than the JSON: the conflicting item is named, no raw UUID ever
// reaches the screen, the status text is genuinely translated (not
// English), and the warned row carries a real, geometrically-distinct
// visual treatment (not just a class name — same standard as
// sale-screen-213.spec.ts) rather than blending in with the clean rows
// around it.
//
// A short PLU-style barcode (4011) is included as a clean row: under the
// default enabled symbology set it now imports with its barcode attached
// (ADR-0059 / ut-docs#936) — it used to be rejected by catimport's old
// ad hoc digit-length rule with an "unsupported shape" warning, which this
// test previously asserted. The "no enabled symbology matches" warning is
// now only reachable once a shop narrows its enabled set away from the
// default catch-alls, which this default-config e2e run can't set; that
// translated-reason path is covered at the handler level by
// TestImport_NoSymbologyMatchWarnsButStillImports.
const ITEM_NAMES = ['Import Widget One', 'Import Widget Two', 'Import Widget Three', 'Import Widget Four'];

test.describe('catalog import: friendly errors + translated statuses (ut-docs#303)', () => {
  // The commit below creates REAL catalog rows (not a preview) with no
  // thumbnail image. Left active, they pollute the shared demo till for
  // every other spec on this server (e2e/README.md's rule) — specifically
  // pages.spec.ts's /catalog console-error check, which then fails on the
  // 404s a missing web/public/assets/items/<id>/thumb.png triggers for
  // each one. Deactivate them here regardless of whether the test passed.
  test.afterEach(async ({ page }) => {
    await page.goto('/catalog');
    for (const name of ITEM_NAMES) {
      const row = page.locator(`.catalog-row[data-name="${name}"]`);
      if ((await row.count()) === 0) continue;
      const id = await row.first().getAttribute('data-id');
      if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
    }
  });

  test('barcode-conflict row names the item, stays UUID-free, and is visually + textually distinct', async ({ page }) => {
    const assertClean = watchConsole(page);

    await page.goto('/import?lang=tr');
    await expect(page.locator('input[type=file]')).toBeVisible();

    // Two rows sharing a barcode: the second one's attach fails against
    // the first, which actually holds it. A third row carries a short PLU
    // barcode (4011) that now imports cleanly under the default enabled
    // set (ut-docs#936). A fourth, clean row is the baseline the geometric
    // assertion below compares against.
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

    // The short PLU barcode (4011) now imports cleanly under the default
    // enabled set (ut-docs#936) — its row is present and is NOT a warned
    // row (no "not imported"/attach warning against it).
    await expect(result).toContainText('4011');
    const pluRow = page.locator('tr', { hasText: 'Import Widget Three' });
    await expect(pluRow).toBeVisible();
    await expect(pluRow).not.toHaveClass(/row-warn/);
    expect(html).not.toContain('içe aktarılmadı'); // never "not imported"

    assertClean();
  });
});
