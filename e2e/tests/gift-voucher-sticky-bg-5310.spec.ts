import { test, expect } from './fixtures';

// ut-docs#5310 (fix: ut-docs#5304): the Gift voucher popup's sticky
// Add/Cancel row must paint the SAME background as the dialog that scrolls
// under it, in every theme. It used `var(--surface)`, which dark.css sets
// to a different colour from the dialog's own UA `Canvas`, leaving a
// visible band. Compares computed styles, so it needs no screenshots.

const NAME = `Gift voucher bg E2E ${Date.now()}`;
let itemID = '';

async function openPopup(page) {
  await page.locator('.products-strip-search').click();
  await page.locator('#products-search').fill(NAME);
  const tile = page.locator('#search-results .btn-tile', { hasText: NAME });
  await expect(tile).toBeVisible();
  await tile.click();
  const modal = page.locator('#modifier-modal');
  await expect(modal).toBeVisible();
  await expect(modal.locator('.gift-voucher-popup h3')).toHaveText('Gift voucher');
  return modal;
}

test.describe('gift voucher popup: sticky action row background (ut-docs#5310)', () => {
  test.beforeAll(async ({ request }) => {
    const res = await request.post('/api/catalog/item', {
      form: { name: NAME, price: '2500', sku: `GV-BG-${Date.now()}`, isActive: '1', itemType: 'gift_voucher' },
    });
    expect(res.ok(), `create gift voucher item: ${res.status()} ${await res.text()}`).toBeTruthy();
    const m = /data-id="([^"]+)"/.exec(await res.text());
    itemID = m ? m[1] : '';
  });

  test.afterAll(async ({ request }) => {
    await request.post('/api/pos/reset');
    if (itemID) await request.post('/api/catalog/item/deactivate', { form: { id: itemID } });
  });

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const theme of ['default', 'dark']) {
    test(`sticky row matches the dialog background (${theme} theme)`, async ({ page }) => {
      await page.goto('/');
      await page.waitForSelector('.pos-container');

      if (theme === 'dark') {
        await page.evaluate(async () => {
          const link = document.getElementById('theme-css') as HTMLLinkElement;
          await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
        });
      }

      const modal = await openPopup(page);
      const row = modal.locator('.gift-voucher-popup .modifier-actions');
      await expect(row).toBeVisible();

      const info = await page.evaluate(() => {
        const dlg = document.getElementById('modifier-modal')!;
        const r = dlg.querySelector('.gift-voucher-popup .modifier-actions')!;
        return {
          colorScheme: getComputedStyle(document.documentElement).colorScheme,
          surface: getComputedStyle(document.documentElement).getPropertyValue('--surface').trim().toLowerCase(),
          dialogBg: getComputedStyle(dlg).backgroundColor,
          rowBg: getComputedStyle(r).backgroundColor,
        };
      });

      // Not vacuous: the theme under test really applied.
      if (theme === 'dark') {
        expect(info.colorScheme, 'dark.css applied (color-scheme)').toContain('dark');
        expect(info.surface, 'dark.css applied (--surface)').toBe('#111827');
      } else {
        expect(info.surface, 'default theme, not dark.css').not.toBe('#111827');
      }
      // The row is painted (not transparent) and equals the dialog's own.
      expect(info.rowBg).not.toBe('rgba(0, 0, 0, 0)');
      expect(info.rowBg, 'sticky row background equals the dialog background').toBe(info.dialogBg);
    });
  }
});
