import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#2819 (follow-ups from the #2815 review):
// 1. A malformed price showed the browser's validationMessage, which is in
//    the device OS language ("Please match the requested format"), not the
//    shop's. Fields with the comma-tolerant pattern now carry the shop-
//    language message from <body data-money-invalid>.
// 2. The item cost field (server-parsed) refused a German "3,50".
// 3. utCurrency.parseMinor used Number(): "0x10" -> 1600, "1e3" -> 100000,
//    "19.999" -> 2000.
// Stored values are read back from the server, never from the input.

async function createItem(page, name: string): Promise<string> {
  await page.goto('/catalog');
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('4.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
    page.locator('#item-form-submit').click(),
  ]);
  await closeItemForm(page);
  return (await page.locator('.catalog-row', { hasText: name }).getAttribute('data-id'))!;
}

async function storedItemPrice(page, itemId: string): Promise<string | null> {
  const html = await (await page.request.get('/catalog')).text();
  const m = html.match(new RegExp(`data-id="${itemId}"[\\s\\S]*?data-price="(\\d+)"`));
  return m && m[1];
}

test.describe('Money inputs (ut-docs#2819)', () => {
  test('a malformed price shows the shop-language message, and a corrected one saves', async ({ page }) => {
    const assertClean = watchConsole(page);
    const itemId = await createItem(page, `Invalid 2819 ${Date.now()}`);
    await page.goto(`/catalog?item=${itemId}&lang=tr`);
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await page.locator('#item-price').fill('5,5,0');
    await page.locator('#item-form-submit').click();
    const notice = page.locator('#item-form-msg .pos-notice.error');
    await expect(notice).toBeVisible();
    await expect(notice).toContainText('Tutarı rakamla girin');
    expect(await storedItemPrice(page, itemId)).toBe('400');

    // A script refill (no input event) must not leave the stale custom
    // message blocking a now-valid Save.
    await page.locator('#item-price').evaluate((el: HTMLInputElement) => { el.value = '6,00'; });
    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item/update')),
      page.locator('#item-form-submit').click(),
    ]);
    expect(resp.status()).toBe(200);
    expect(await storedItemPrice(page, itemId)).toBe('600');
    assertClean();
  });

  test('the item cost accepts a decimal comma and stores minor units', async ({ page }) => {
    const assertClean = watchConsole(page);
    const itemId = await createItem(page, `Cost 2819 ${Date.now()}`);
    await page.goto(`/catalog?item=${itemId}&lang=de`);
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await page.locator('#item-form-tab-variants').click();
    const cost = page.locator('input[name="cost"]');
    await cost.fill('3,50');
    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item-cost')),
      cost.locator('xpath=ancestor::form').locator('button[type=submit]').click(),
    ]);
    expect(resp.status()).toBe(200);
    const panel = await (await page.request.get(`/api/catalog/item-variants?item_id=${itemId}`)).text();
    expect(panel).toMatch(/name="cost"[^>]*value="3\.50"/);
    assertClean();
  });

  test('parseMinor reads only plain amounts', async ({ page }) => {
    await page.goto('/catalog');
    const got = await page.evaluate(() => {
      const c = (window as any).utCurrency;
      const out: Record<string, number | null> = {};
      for (const v of ['3.50', '3,50', '3,5', '-2.00', '12', '0x10', '1e3', '19.999', '1,234.56', ' 7.10 ', '', 'abc']) {
        const n = c.parseMinor(v);
        out[v] = Number.isNaN(n) ? null : n;
      }
      out['num:12.5'] = c.parseMinor(12.5);
      out['toMinor:1e3'] = c.toMinor('1e3');
      return out;
    });
    expect(got).toEqual({
      '3.50': 350, '3,50': 350, '3,5': 350, '-2.00': -200, '12': 1200, ' 7.10 ': 710,
      '0x10': null, '1e3': null, '19.999': null, '1,234.56': null, '': null, 'abc': null,
      'num:12.5': 1250, 'toMinor:1e3': 0,
    });
  });
});
