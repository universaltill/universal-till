import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#2818: on a German till the item editor's price field and the
// variant grid were prefilled by utCurrency.toMajor with a dot ("4.00")
// while the till's own keyboard types a comma. Prefills now follow the
// shop language's decimal separator; the fields' readers accept either,
// so an untouched prefilled value must still save to the same amount.
// Stored values are read back from the server, never from the input.

async function createItem(page, name: string, lang: string): Promise<string> {
  await page.goto(`/catalog?lang=${lang}`);
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

async function storedVariantPrice(page, itemId: string): Promise<string | null> {
  const body = await (await page.request.get(`/api/catalog/item-variants?item_id=${itemId}`)).text();
  const m = body.match(/class="variant-price-major" data-minor="(\d+)"/);
  return m && m[1];
}

test.describe('Money prefills use the locale decimal separator (ut-docs#2818)', () => {
  for (const [lang, price, variant] of [['de', '4,00', '2,50'], ['en', '4.00', '2.50']]) {
    test(`${lang}: item and variant prices prefill as ${price} / ${variant} and save unchanged`, async ({ page }) => {
      const assertClean = watchConsole(page);
      const name = `Flat White 2818 ${lang} ${Date.now()}`;
      const itemId = await createItem(page, name, lang);

      await page.goto(`/catalog?item=${itemId}&return=/catalog&lang=${lang}`);
      await expect(page.locator('#item-form-modal')).toBeVisible();
      await expect(page.locator('#item-price')).toHaveValue(price);

      // Save the prefilled value untouched: it must pass the field's
      // pattern and store the same 400 minor units.
      const [resp] = await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/catalog/item/update')),
        page.locator('#item-form-submit').click(),
      ]);
      expect(resp.status()).toBe(200);
      expect(resp.request().postData()).toContain('price=400');
      expect(await storedItemPrice(page, itemId)).toBe('400');

      // Variant grid: add one, then its re-rendered row is prefilled.
      await page.goto(`/catalog?item=${itemId}&return=/catalog&lang=${lang}`);
      await page.locator('#item-form-tab-variants').click();
      await page.locator('input[form="vf-new"][name="name"]').fill('Large');
      await page.locator('input[form="vf-new"].variant-price-major').fill(variant);
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
        page.locator('button[form="vf-new"][type=submit]').click(),
      ]);
      const row = page.locator('.vg-row:not(.vg-new)');
      await expect(row.locator('.variant-price-major')).toHaveValue(variant);
      const [vresp] = await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
        row.locator('button[form^="vf-"][type=submit]').click(),
      ]);
      expect(vresp.status()).toBe(200);
      expect(vresp.request().postData()).toContain('price=250');
      expect(await storedVariantPrice(page, itemId)).toBe('250');
      assertClean();
    });
  }
});
