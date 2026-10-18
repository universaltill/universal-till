import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4627: a line discount above the line's own total used to be
// accepted by the basket and only refused at tender, as the generic "sale
// could not be completed" banner. The basket now refuses it on entry: the
// line keeps its old discount and a translated note under the item name
// says the most the discount can be. At the 1024x600 kiosk floor the note
// must be on screen next to the line it is about.
test.use({ viewport: { width: 1024, height: 600 } });

const CODE = '5000000000012';

async function scan(page, code: string) {
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

async function setDiscount(page, value: string) {
  const disc = page.locator('#basket .disc-input').first();
  await disc.fill(value);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/line')),
    disc.press('Tab'),
  ]);
}

test.describe('line discount above the line total (ut-docs#4627)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('is refused on entry with a note on that line', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.request.post('/api/pos/reset');
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scan(page, CODE);
    const row = page.locator('#basket tbody tr').first();
    const totalBefore = (await row.locator('td').nth(3).innerText()).trim();

    // Far above any demo item's price.
    await setDiscount(page, '99999');
    const note = row.locator('.line-discount-error');
    await expect(note).toBeVisible();
    await expect(note).toHaveAttribute('role', 'alert');
    // The note names the line total, the most the discount can be.
    await expect(note).toContainText(totalBefore);
    await expect(row.locator('.disc-input')).toHaveValue('');
    await expect(row.locator('td').nth(3)).toHaveText(totalBefore);
    const box = await note.boundingBox();
    expect(box, 'note rendered').not.toBeNull();
    expect(box!.x + box!.width, 'note inside the viewport').toBeLessThanOrEqual(1024);
    await page.screenshot({ path: test.info().outputPath('line-discount-refused.png') });

    // A discount that fits is applied and the note goes away.
    await setDiscount(page, '10');
    await expect(row.locator('.line-discount-error')).toHaveCount(0);
    await expect(row.locator('.disc-input')).toHaveValue('10');
    await expect(row.locator('td').nth(3)).not.toHaveText(totalBefore);
    assertClean();
  });
});
