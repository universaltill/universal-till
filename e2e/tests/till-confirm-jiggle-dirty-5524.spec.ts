import { test, expect } from './fixtures';
import type { Page, Locator, Request } from '@playwright/test';
import { watchConsole, setBrowsingMode, answerTillConfirm, noNativeDialogs } from './helpers';

// ut-docs#5524 AC14: the remove badge's hx-confirm while a jiggle reorder is
// still UNSAVED goes through app.js's own htmx:confirm handler (document.body,
// before till-confirm.js's document listener): it asks in the till's
// #ut-confirm (not window.confirm), and
//   - Cancel: no reorder POST, no remove POST, still in edit mode, the
//     unsaved order is still there (and is saved later by Done);
//   - Confirm: the reorder is persisted FIRST, then the remove is issued
//     exactly once -- so the remove's grid refresh can't drop the reorder.
// Gestures are Playwright's synthetic mouse/keyboard in Chromium (keyboard
// ArrowLeft is the reorder path, as in sell-tile-jiggle-mode-2339), not
// real touch on the pilot till.

const RUN = Date.now().toString(36).toUpperCase();
const CAT = `Dirty5524 Cat ${RUN}`;
const mk = (k: string) => ({
  name: `Dirty5524 Item ${k} ${RUN}`,
  sku: `DRT5524${k}${RUN}`,
  barcode: `DRT5524BC-${k}-${RUN}`,
});
const A = mk('A');
const B = mk('B');
const C = mk('C');
const ALL = [A, B, C];

async function seed(page: Page) {
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', {
    name: `import-5524-${RUN}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(
      'Name,SKU,Barcode,Price,Category,In stock\n' +
        ALL.map((it) => `${it.name},${it.sku},${it.barcode},1.00,${CAT},1`).join('\n'),
    ),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
}

const tile = (page: Page, it: { barcode: string }): Locator =>
  page.locator(`.products-tab-panel .btn-tile[data-code="${it.barcode}"]`);
const codes = (page: Page) =>
  page
    .locator(`.products-tab-panel .btn-tile[data-code^="DRT5524BC-"][data-code$="-${RUN}"]`)
    .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.code));

test.describe('jiggle remove with an unsaved reorder asks in #ut-confirm (ut-docs#5524 AC14)', () => {
  test.beforeEach(async ({ page }) => {
    await setBrowsingMode(page, 'strip_overflow');
  });

  test('Cancel keeps the reorder unsaved and the tile; Confirm saves the reorder, then removes once', async ({ page }) => {
    const assertClean = watchConsole(page);
    await seed(page);
    await page.goto('/');
    await page.getByRole('tab', { name: CAT }).click();
    await expect.poll(() => codes(page)).toEqual([A.barcode, B.barcode, C.barcode]);

    const calls: Request[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/buttons/')) calls.push(r);
    });
    const noNative = noNativeDialogs(page);

    // Enter edit mode and make the order dirty: C moves to the front.
    await tile(page, C).click({ button: 'right' });
    await expect(page.locator('#buttons-grid.jiggle-mode')).toHaveCount(1);
    await tile(page, C).focus();
    await page.keyboard.press('ArrowLeft');
    await page.keyboard.press('ArrowLeft');
    await expect.poll(() => codes(page)).toEqual([C.barcode, A.barcode, B.barcode]);
    expect(calls.filter((r) => r.url().includes('/reorder')), 'arrow moves alone do not save').toHaveLength(0);

    const removeB = page
      .locator('.products-tab-panel .tile-cell', { has: page.locator(`.btn-tile[data-code="${B.barcode}"]`) })
      .getByTestId('tile-badge-remove');

    // (1) Cancel: nothing sent, still editing, the dirty order still on screen.
    await removeB.click();
    await expect(page.locator('#ut-confirm')).toHaveJSProperty('open', true);
    await page.screenshot({ path: test.info().outputPath('ac14-dirty-confirm-open.png') });
    await answerTillConfirm(page, 'cancel', B.name);
    await page.waitForTimeout(400);
    expect(calls.map((r) => r.url()), 'Cancel must send nothing').toEqual([]);
    await expect(page.locator('#buttons-grid.jiggle-mode')).toHaveCount(1);
    await expect.poll(() => codes(page)).toEqual([C.barcode, A.barcode, B.barcode]);

    // (2) Escape is Cancel on this path too (and does not end edit mode).
    await removeB.click();
    await answerTillConfirm(page, 'escape');
    await page.waitForTimeout(400);
    expect(calls.map((r) => r.url()), 'Escape must send nothing').toEqual([]);
    await expect(page.locator('#buttons-grid.jiggle-mode')).toHaveCount(1);

    // (3) Confirm: reorder POST first, then exactly one remove POST.
    const removed = page.waitForResponse((r) => r.url().includes('/api/buttons/remove') && r.ok());
    await removeB.click();
    await answerTillConfirm(page, 'confirm', B.name);
    await removed;
    await page.waitForTimeout(500);
    const urls = calls.map((r) => new URL(r.url()).pathname);
    const iReorder = urls.indexOf('/api/buttons/reorder');
    const removes = urls.filter((u) => u.startsWith('/api/buttons/remove'));
    expect(iReorder, `reorder must be posted (${urls.join(', ')})`).toBeGreaterThanOrEqual(0);
    expect(removes, `exactly one remove (${urls.join(', ')})`).toHaveLength(1);
    expect(iReorder, 'reorder before remove').toBeLessThan(urls.findIndex((u) => u.startsWith('/api/buttons/remove')));
    const posted = new URLSearchParams(calls[iReorder].postData() || '').getAll('codes');
    expect(posted.filter((c) => c.endsWith(`-${RUN}`))).toEqual([C.barcode, A.barcode, B.barcode]);

    // Server state after a reload: B's tile gone, C-before-A persisted.
    await page.keyboard.press('Escape');
    await page.reload();
    await page.getByRole('tab', { name: CAT }).click();
    await expect.poll(() => codes(page)).toEqual([C.barcode, A.barcode]);

    noNative();
    assertClean();
  });
});
