import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, setOskMode, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#2971: the on-screen numeric pad only offered a '.' decimal key,
// while a comma-decimal till (de, tr, …) prefills money fields as "4,00" —
// keeping the prefill and tapping '.' gave "4,00.5". The pad's decimal key
// now types the shop language's separator into a field whose reader takes
// either (data-money-local); every other numeric field keeps '.'.
// Real OSK keys only, never fill(); stored values are read back from the
// server, never from the input.

const padKey = (page: Page, k: string) => page.locator(`#osk button[data-k="${k}"]`);

async function tapKeys(page: Page, keys: string[]) {
  for (const k of keys) await padKey(page, k).click();
}

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
});

for (const [lang, dec] of [['de', ','], ['en', '.']]) {
  test(`${lang}: the pad's decimal key types "${dec}" into #item-price and the price stores as 3.75`, async ({ page }) => {
    const assertClean = watchConsole(page);
    await setOskMode(page, 'on');
    const name = `Pad Price 2971 ${lang} ${Date.now()}`;

    await page.goto(`/catalog?lang=${lang}`);
    await openNewItemForm(page);
    await page.locator('#item-name').fill(name);
    const price = page.locator('#item-price');
    await price.click();
    await expect(page.locator('#osk')).toBeVisible();
    await expect(padKey(page, dec)).toBeVisible();
    // Only one decimal key: the other separator must not be on the pad.
    await expect(padKey(page, dec === ',' ? '.' : ',')).toHaveCount(0);

    await tapKeys(page, ['3', dec, '7', '5']);
    await expect(price).toHaveValue(`3${dec}75`);

    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
      page.locator('#item-form-submit').click(),
    ]);
    expect(resp.status()).toBe(200);
    await closeItemForm(page);
    const id = await page.locator('.catalog-row', { hasText: name }).getAttribute('data-id');
    expect(id).toBeTruthy();
    const html = await (await page.request.get('/catalog')).text();
    const m = html.match(new RegExp(`data-id="${id}"[\\s\\S]*?data-price="(\\d+)"`));
    expect(m && m[1]).toBe('375');
    assertClean();
  });
}

test('de: a numeric field without data-money-local keeps the "." decimal key', async ({ page }) => {
  const assertClean = watchConsole(page);
  await setOskMode(page, 'on');
  await page.goto('/catalog?lang=de');
  await openNewItemForm(page);
  // A dot-only numeric field: inject one into the open form so the test
  // doesn't depend on which real fields are still dot-only.
  await page.evaluate(() => {
    const f = document.createElement('input');
    f.type = 'text';
    f.inputMode = 'decimal';
    f.id = 'osk-2971-dot-only';
    document.querySelector('#item-form-modal form, #item-form-modal')!.appendChild(f);
  });
  const field = page.locator('#osk-2971-dot-only');
  await field.click();
  await expect(page.locator('#osk')).toBeVisible();
  await expect(padKey(page, '.')).toBeVisible();
  await expect(padKey(page, ',')).toHaveCount(0);
  await tapKeys(page, ['1', '.', '5']);
  await expect(field).toHaveValue('1.5');
  assertClean();
});
