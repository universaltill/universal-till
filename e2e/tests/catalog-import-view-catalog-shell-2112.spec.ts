import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2112 (found during independent review of ut-docs#2095): the
// commit success summary's "View catalog" button/link is emitted from Go
// (import_page.go), not the template -- so unlike the dialog's own
// back-link (fixed by #2095, itself an .InItemsShell template check), it
// still plain-navigated the WHOLE PAGE to bare /catalog even when the
// import ran inside the /items shell's dialog overlay (#import-modal,
// ut-docs#2095) -- exactly the railless standalone destination ut-docs#2090
// deliberately moved every other in-shell exit away from. Fixed by
// threading the dialog signal through as a hidden form field
// (in_items_shell) into the POST /api/import response, since an hx-post
// carries HX-Request:true whether the page around it loaded standalone or
// inside the dialog -- httpx.IsFragmentSwap alone can't tell them apart on
// the POST the way it can on the GET.
//
// Independent review finding F1: closing the dialog alone isn't enough --
// the /catalog panel behind it was rendered BEFORE the import and never
// refreshed, so the operator would tap "View catalog" and see a stale list
// missing the very rows they just imported (the exact doubt the button
// exists to remove, ut-docs#1171). The button's own click now refetches
// #items-panel before closing (scoped to this one control, not a generic
// dialog 'close' listener -- a prior version of this fix used one and it
// also fired on a plain cancel, wiping the operator's live search/filter
// state on the panel behind it for nothing); the assertion below pins that
// the imported row is actually visible after the click, not just that the
// dialog went away.
const ITEM_NAME = 'Import Shell Widget 2112';

test.describe('/items shell: Import dialog\'s "View catalog" stays in the shell (ut-docs#2112)', () => {
  test.afterEach(async ({ page }) => {
    // Same shared-till cleanup convention as catalog-import-friendly-
    // errors.spec.ts -- this test commits a REAL catalog row.
    await page.goto('/catalog');
    const row = page.locator(`.catalog-row[data-name="${ITEM_NAME}"]`);
    if ((await row.count()) > 0) {
      const id = await row.first().getAttribute('data-id');
      if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
    }
  });

  test('completing an import from inside the dialog and tapping View catalog closes the dialog, not a bare /catalog navigation', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');
    await expect(page.locator('#items-rail')).toBeVisible();

    await page.locator('#catalog-import-btn').click();
    const dialog = page.locator('#import-modal');
    await expect(dialog).toBeVisible();

    const csv = `Name,SKU,Barcode,Price,Category,In stock\n${ITEM_NAME},SKU2112,,1.00,,1\n`;
    await dialog.locator('input[type=file]').setInputFiles({
      name: 'import-2112.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/import')),
      dialog.getByRole('button', { name: 'Import', exact: true }).last().click(),
    ]);

    const result = dialog.locator('#import-result');
    await expect(result).toContainText('Imported 1 item(s)');
    const viewCatalog = result.getByRole('button', { name: 'View catalog' });
    await expect(viewCatalog).toBeVisible();
    // The bug: this used to be an <a href="/catalog"> that plain-navigated
    // the whole page. It must now be a button, never a link out.
    await expect(result.locator('a', { hasText: 'View catalog' })).toHaveCount(0);

    // Deliberately NOT raced against a page.waitForResponse() here (review
    // finding G3): a loose `endsWith('/catalog')` predicate can also match
    // this SAME file's own afterEach cleanup navigation on a genuine
    // regression, stalling the test for the full 30s timeout and then
    // failing on an unrelated later assertion instead of the real one.
    // Every assertion below already auto-retries against the live DOM, so
    // there's nothing a manual wait adds -- and dropping it means a real F1
    // regression (the row never appears) fails fast, at the right line.
    await viewCatalog.click();

    // Stayed in the shell: dialog closed, no navigation away from /items,
    // rail still present and still on Catalog -- never swapped, never
    // reloaded.
    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(/\/items$/);
    await expect(page.locator('#items-rail')).toBeVisible();
    await expect(page.locator('.items-row.is-current')).toHaveAttribute('href', '/catalog');
    // F1: the panel behind the dialog was refreshed, not left stale -- the
    // row this test just imported is actually visible now, not just a
    // dialog that quietly closed onto the pre-import list.
    await expect(page.locator(`.catalog-row[data-name="${ITEM_NAME}"]`)).toBeVisible();
    assertClean();
  });

  test('the standalone /import page keeps a real /catalog link on View catalog, unchanged', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/import');
    await expect(page.locator('#items-rail')).toHaveCount(0);

    const csv = `Name,SKU,Barcode,Price,Category,In stock\n${ITEM_NAME},SKU2112B,,1.00,,1\n`;
    await page.setInputFiles('input[type=file]', {
      name: 'import-2112b.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/import')),
      page.getByRole('button', { name: 'Import', exact: true }).last().click(),
    ]);

    const result = page.locator('#import-result');
    await expect(result).toContainText('Imported 1 item(s)');
    const viewCatalog = result.locator('a', { hasText: 'View catalog' });
    await expect(viewCatalog).toHaveAttribute('href', '/catalog');
    assertClean();
  });
});
