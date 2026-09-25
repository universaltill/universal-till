import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm, setOskMode } from './helpers';

// ut-docs#2815 (p1, product owner on the tablet = main till, German
// locale): "I changed a price, pressed Save, and it went back to the
// original price." Two separate root causes, both client-side, both driven
// here through the real item editor dialog:
//
// 1. Variant row (Variants tab): catalog.html's htmx:configRequest listener
//    found the visible price input via `f.id`, but an EXISTING variant's
//    hidden form has its own <input name="id">, which shadows the form's
//    `id` property — so the lookup matched nothing and the stale hidden
//    `price` (the original) was posted. Language-independent: "3.50" in en
//    reverted too. (The add-variant form has no id input, so create worked.)
// 2. Item price (the dialog the sell-screen tile sheet deep-links to, and
//    the variant grid too): the money `pattern` accepted only a dot, so a
//    German keyboard's "5,50" failed native validation, the browser blocked
//    the submit with no request at all, and Android WebView shows no
//    validation bubble — Save looked like a no-op and the old price came
//    back on close. utCurrency.toMinor also read "5,50" as 0.
//
// The stored value is read back from the server, never from the input the
// test just typed into (a populated field is not evidence of a save).

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

async function openVariantsWithOneVariant(page, name: string, lang: string): Promise<string> {
  const itemId = await createItem(page, name, lang);
  await page.locator('.catalog-row', { hasText: name }).click();
  await page.locator('#item-form-tab-variants').click();
  await page.locator('input[form="vf-new"][name="name"]').fill('Large');
  await page.locator('input[form="vf-new"].variant-price-major').fill('2.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
    page.locator('button[form="vf-new"][type=submit]').click(),
  ]);
  await expect(page.locator('.vg-row:not(.vg-new)')).toBeVisible();
  return itemId;
}

async function storedVariantPrice(page, itemId: string): Promise<string | null> {
  const body = await (await page.request.get(`/api/catalog/item-variants?item_id=${itemId}`)).text();
  const m = body.match(/class="variant-price-major" data-minor="(\d+)"/);
  return m && m[1];
}

async function storedItemPrice(page, itemId: string): Promise<string | null> {
  const html = await (await page.request.get('/catalog')).text();
  const m = html.match(new RegExp(`data-id="${itemId}"[\\s\\S]*?data-price="(\\d+)"`));
  return m && m[1];
}

test.describe('Price edits save (ut-docs#2815)', () => {
  for (const [lang, typed] of [['en', '3.50'], ['de', '3,50']]) {
    test(`${lang}: an existing variant's price edited to ${typed} is stored and confirmed`, async ({ page }) => {
      const assertClean = watchConsole(page);
      const itemId = await openVariantsWithOneVariant(page, `Americano 2815 ${lang} ${Date.now()}`, lang);
      const row = page.locator('.vg-row:not(.vg-new)');
      await row.locator('.variant-price-major').fill(typed);
      const [resp] = await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
        row.locator('button[form^="vf-"][type=submit]').click(),
      ]);
      expect(resp.status()).toBe(200);
      expect(resp.request().postData()).toContain('price=350');
      await expect(page.getByTestId('variant-saved')).toBeVisible();
      // Still inside the open dialog: a targeted panel swap, not a reload.
      await expect(page.locator('#item-form-modal')).toBeVisible();
      expect(await storedVariantPrice(page, itemId)).toBe('350');
      assertClean();
    });
  }

  for (const [lang, typed] of [['en', '5.50'], ['de', '5,50']]) {
    test(`${lang}: the item's own price edited to ${typed} from the tile deep link is stored`, async ({ page }) => {
      const assertClean = watchConsole(page);
      const itemId = await createItem(page, `Cold Brew 2815 ${lang} ${Date.now()}`, lang);
      await page.goto(`/catalog?item=${itemId}&return=/&lang=${lang}`);
      await expect(page.locator('#item-form-modal')).toBeVisible();
      await page.locator('#item-price').fill(typed);
      const [resp] = await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/catalog/item/update')),
        page.locator('#item-form-submit').click(),
      ]);
      expect(resp.status()).toBe(200);
      expect(resp.request().postData()).toContain('price=550');
      expect(await storedItemPrice(page, itemId)).toBe('550');
      assertClean();
    });
  }

  // The owner's own path on the tablet: tap the filled price, press 6 on the
  // till's on-screen keyboard, Save. The keyboard inserted at the tap's caret
  // ("5.006"), the pattern refused it and nothing was posted. Real OSK keys,
  // never fill(), which bypasses osk.js.
  test('tapping a filled price and typing on the on-screen keyboard replaces it', async ({ page }) => {
    const assertClean = watchConsole(page);
    const itemId = await createItem(page, `OSK 2815 ${Date.now()}`, 'de');
    await setOskMode(page, 'on');
    try {
      await page.goto(`/catalog?item=${itemId}&return=/catalog&lang=de`);
      await expect(page.locator('#item-form-modal')).toBeVisible();
      await page.locator('#item-price').click();
      await expect(page.locator('#osk')).toBeVisible();
      await page.locator('#osk button[data-k="6"]').click();
      await expect(page.locator('#item-price')).toHaveValue('6');
      const [resp] = await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/catalog/item/update')),
        page.locator('#item-form-submit').click(),
      ]);
      expect(resp.status()).toBe(200);
      expect(await storedItemPrice(page, itemId)).toBe('600');
      // A second tap on the same open field doesn't re-select (show()'s
      // early return): with the caret put at the end, a key inserts there.
      // (Real caret placement by a tap is the browser's, not tested here.)
      await page.goto(`/catalog?item=${itemId}&lang=de`);
      await page.locator('#item-price').click();
      await page.locator('#item-price').click();
      await page.locator('#item-price').evaluate((el: HTMLInputElement) => el.setSelectionRange(el.value.length, el.value.length));
      await page.locator('#osk button[data-k="5"]').click();
      await expect(page.locator('#item-price')).toHaveValue('6.005');
    } finally {
      await setOskMode(page, 'auto');
    }
    assertClean();
  });

  test('a price the browser refuses shows a visible message instead of doing nothing', async ({ page }) => {
    const assertClean = watchConsole(page);
    const itemId = await createItem(page, `Refused 2815 ${Date.now()}`, 'en');
    await page.goto(`/catalog?item=${itemId}`);
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await page.locator('#item-price').fill('5,5,0');
    await page.locator('#item-form-submit').click();
    await expect(page.locator('#item-form-msg .pos-notice.error')).toBeVisible();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    expect(await storedItemPrice(page, itemId)).toBe('400');
    assertClean();
  });
});
