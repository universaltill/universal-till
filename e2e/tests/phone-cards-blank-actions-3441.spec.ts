import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3441: at the phone tier table-cards.js gives a cell under an EMPTY
// header `.t-cards-actions` (display:flex). app.css hid such a cell with
// `:empty`, but templates leave whitespace text nodes inside, so `:empty`
// never matched and every card showed a blank ~9px strip. table-cards.js now
// marks a cell with no element children and whitespace-only text
// `.t-cards-blank`, which app.css hides too.
test.describe('phone 360px: blank header-less cells are hidden on cards (ut-docs#3441)', () => {
  test.use({ hasTouch: true, isMobile: true });

  test('whitespace-only header-less cell is hidden; cells with content stay; re-annotation is idempotent', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/orders');
    await page.waitForSelector('main');

    await page.evaluate(() => {
      const main = document.querySelector('main')!;
      const wrap = document.createElement('div');
      wrap.id = 'blank-3441';
      wrap.innerHTML =
        '<table id="t3441"><thead><tr><th>Name</th><th></th></tr></thead><tbody>' +
        '<tr id="r1"><td>one</td><td>\n   \n</td></tr>' +
        '<tr id="r2"><td>two</td><td><button type="button">Go</button></td></tr>' +
        '<tr id="r3"><td>three</td><td>note</td></tr>' +
        '</tbody></table>';
      main.appendChild(wrap);
      (window as any).utTableCards(wrap);
    });

    const cell = (id: string) => page.locator(`#t3441 tr#${id} > td.t-cards-actions`);
    const display = (id: string) => cell(id).evaluate((el) => getComputedStyle(el).display);

    expect(await display('r1')).toBe('none');
    await expect(cell('r2')).toBeVisible();
    await expect(cell('r3')).toBeVisible();

    // The cell gains content, the table is re-annotated: it must show again.
    await page.evaluate(() => {
      const td = document.querySelector('#t3441 tr#r1 > td.t-cards-actions')!;
      td.innerHTML = '<button type="button">Now</button>';
      (window as any).utTableCards(document.getElementById('blank-3441'));
    });
    await expect(cell('r1')).toBeVisible();
    expect(await display('r1')).not.toBe('none');

    assertClean();
  });

  test('a real order card has no blank print-warning strip', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await page.getByTestId('payment-open').click();
    await expect(page.locator('#payment-overlay')).toBeVisible();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('.tab-panel .btn', { hasText: 'Cash' }).first().click(),
    ]);
    const receiptHeading = await page.locator('#basket.receipt-view h2', { hasText: 'Receipt' }).textContent();
    const receiptNo = receiptHeading?.match(/#(\S+)/)?.[1];
    expect(receiptNo, `receipt number must be readable from ${receiptHeading}`).toBeTruthy();

    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/orders');
    const row = page.locator(`tr#order-row-${receiptNo}`);
    await expect(row).toHaveClass(/\bt-cards-row\b/);

    const cell = row.locator('> td.t-cards-actions');
    await expect(cell).toHaveCount(1);
    expect(await cell.evaluate((el) => getComputedStyle(el).display)).toBe('none');
    await expect(cell).toBeHidden();
    expect(await cell.boundingBox()).toBeNull();

    assertClean();
  });
});
