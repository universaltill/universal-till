import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#4344: a scanned code that is more than one thing (here an item's
// SKU that is also a gift voucher number) adds nothing and asks which one
// is meant, in the basket, with one row per answer. The code is unique per
// run: a worker's till is shared by every spec file it runs, and a voucher
// on a seeded item code would make that code ambiguous for all of them.

const RUN = Date.now().toString(36).toUpperCase();
// Per test: a deactivated item still holds its SKU, and a voucher number
// can be issued once.
let n = 0;
let CODE = '';
let ITEM = '';
let itemId: string | undefined;

async function seedCollision(page: Page) {
  let res = await page.request.post('/api/catalog/item', { form: { name: ITEM, sku: CODE, price: '150' } });
  expect(res.ok(), `create item: ${res.status()}`).toBe(true);
  res = await page.request.post('/api/pos/tender', {
    headers: { 'Content-Type': 'application/json' },
    data: { payments: [{ method: 'cash', amount: 500 }], issue_vouchers: [{ amount: 500, code: CODE }] },
  });
  expect(res.ok(), `issue voucher: ${res.status()} ${await res.text()}`).toBe(true);
}

async function scan(page: Page) {
  const field = page.locator('.scan-row input[name="code"]');
  const done = page.waitForResponse((r) => r.url().includes('/api/pos/scan'));
  if (await field.isVisible()) {
    await field.fill(CODE);
    await field.press('Enter');
  } else {
    // A phone hides the scan field; a camera scan submits the same form.
    await page.evaluate((code) => {
      const input = document.querySelector<HTMLInputElement>('.scan-row input[name="code"]')!;
      input.value = code;
      (window as any).htmx.trigger(input.form!, 'submit');
    }, CODE);
  }
  await done;
}

test.describe('a code that is two things asks which one (ut-docs#4344)', () => {
  test.beforeEach(async ({ page }) => {
    n++;
    CODE = `E2E4344${RUN}N${n}`;
    ITEM = `Chooser item ${RUN} ${n}`;
    await seedCollision(page);
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
    if (!itemId) {
      await page.goto('/catalog');
      itemId = (await page.locator(`.catalog-row[data-name="${ITEM}"]`).first().getAttribute('data-id').catch(() => null)) || undefined;
    }
    if (itemId) await page.request.post('/api/catalog/item/deactivate', { form: { id: itemId } }).catch(() => {});
    itemId = undefined;
  });

  test('nothing is added until a row is picked; Item adds the line', async ({ page }) => {
    await page.goto('/');
    await scan(page);
    const chooser = page.locator('#basket .scan-choices');
    await expect(chooser).toBeVisible();
    await expect(chooser.locator('.scan-choice')).toHaveCount(2);
    await expect(chooser.locator('[data-scan-kind="item"]')).toContainText(ITEM);
    await expect(chooser.locator('[data-scan-kind="voucher"]')).toContainText(CODE);
    // Amounts read like the rest of the sale screen: the shop's currency.
    await expect(chooser.locator('[data-scan-kind="item"]')).toContainText('£1.50');
    await expect(chooser.locator('[data-scan-kind="voucher"]')).toContainText('£5.00');
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
    await expect(page.locator('#basket')).toHaveAttribute('data-voucher-id', '');
    // The scan field keeps focus: a scanner can keep going.
    await expect(page.locator('.scan-row input[name="code"]')).toBeFocused();

    await chooser.locator('[data-scan-kind="item"]').click();
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '1');
    await expect(page.locator('#basket .scan-choices')).toHaveCount(0);
    await expect(page.locator('#basket')).toHaveAttribute('data-voucher-id', '');
  });

  test('Gift voucher offers the voucher and adds nothing', async ({ page }) => {
    await page.goto('/');
    await scan(page);
    await page.locator('#basket .scan-choices [data-scan-kind="voucher"]').click();
    await expect(page.locator('#basket')).toHaveAttribute('data-voucher-id', CODE);
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
  });

  test('Cancel closes it and changes nothing; it does not fade on its own', async ({ page }) => {
    await page.goto('/');
    await scan(page);
    const chooser = page.locator('#basket .scan-choices');
    await page.waitForTimeout(6000); // longer than an info toast lives
    await expect(chooser).toBeVisible();
    await chooser.locator('.scan-choices-cancel').click();
    await expect(page.locator('#basket .scan-choices')).toHaveCount(0);
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
  });

  for (const vp of [
    { name: 'kiosk floor 1024x600', width: 1024, height: 600, lang: 'en' },
    { name: 'phone 360px, basket sheet closed', width: 360, height: 800, lang: 'en' },
    { name: 'phone 360px, Persian (RTL)', width: 360, height: 800, lang: 'fa' },
  ]) {
    test(`fits and is touch-sized: ${vp.name}`, async ({ page }) => {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto(`/?lang=${vp.lang}`);
      await scan(page);
      const chooser = page.locator('#basket .scan-choices');
      await expect(chooser).toBeVisible();
      for (const row of await chooser.locator('.btn').all()) {
        const box = (await row.boundingBox())!;
        expect(box.height, 'touch target').toBeGreaterThanOrEqual(48);
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width, 'inside the screen').toBeLessThanOrEqual(vp.width + 1);
      }
      const sw = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(sw, 'page must not scroll sideways').toBeLessThanOrEqual(vp.width + 1);
      if (vp.lang === 'fa') {
        expect(await chooser.evaluate((el) => getComputedStyle(el).direction)).toBe('rtl');
        // Persian digits, like the tiles' prices beside it.
        await expect(chooser.locator('[data-scan-kind="item"]')).toContainText('۱');
      }
      await page.screenshot({ path: test.info().outputPath(`chooser-${vp.width}-${vp.lang}.png`) });
    });
  }

  // The chooser's tint is translucent over --surface: it must stay readable
  // on the dark theme too (UX note 6 on the card).
  test('dark theme: the title and rows stay readable', async ({ page }) => {
    const setTheme = (theme: string) =>
      page.request.post('/api/settings/theme', { form: { theme } });
    const before = await page.request.get('/');
    const prev = /\/themes\/([a-z0-9-]+)\.css/.exec(await before.text())?.[1] ?? 'default';
    expect((await setTheme('dark')).status()).toBe(204);
    try {
      await page.setViewportSize({ width: 1024, height: 600 });
      await page.goto('/');
      await expect(page.locator('#theme-css')).toHaveAttribute('href', /\/themes\/dark\.css/);
      await scan(page);
      const chooser = page.locator('#basket .scan-choices');
      await expect(chooser).toBeVisible();
      // Text colour must differ clearly from the page background behind the
      // translucent tint (a light-theme-only colour would vanish here).
      const [fg, bg] = await chooser.locator('.scan-choices-title').evaluate((el) => {
        const lum = (c: string) => {
          const [r, g, b] = (c.match(/\d+(\.\d+)?/g) || ['0', '0', '0']).slice(0, 3).map(Number);
          return 0.2126 * r + 0.7152 * g + 0.0722 * b;
        };
        return [lum(getComputedStyle(el).color), lum(getComputedStyle(document.body).backgroundColor)];
      });
      expect(Math.abs(fg - bg), 'title contrast on dark').toBeGreaterThan(100);
      await page.screenshot({ path: test.info().outputPath('chooser-1024-dark.png') });
    } finally {
      await setTheme(prev);
    }
  });
});
