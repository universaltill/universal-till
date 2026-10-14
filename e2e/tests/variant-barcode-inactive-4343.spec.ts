import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm } from './helpers';

// ut-docs#4343: a switched-off variant used to keep its barcode ＋ form, and
// attaching failed with a bare "barcode attach failed" (the repo refuses
// barcodes on inactive variants). The row now says why in place of the
// form. This drives the real Variants tab: switch one variant off, then
// check its row shows the hint (fully on screen, not clipped) and offers no
// attach form, while the active variant keeps its form.

async function rowFor(page: import('@playwright/test').Page, name: string) {
  return page.locator('#catalog-variants .vg-row').filter({
    has: page.locator(`input[name="name"][value="${name}"]`),
  });
}

async function addVariant(page: import('@playwright/test').Page, name: string) {
  await page.locator('input[form="vf-new"][name="name"]').fill(name);
  await page.locator('input[form="vf-new"].variant-price-major').fill('1.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
    page.locator('button[form="vf-new"][type=submit]').click(),
  ]);
  await expect(await rowFor(page, name)).toHaveCount(1);
}

for (const vp of [
  { width: 1024, height: 600, lang: 'en' },
  { width: 360, height: 800, lang: 'en' },
  { width: 1024, height: 600, lang: 'fa' },
]) {
  test(`inactive variant row explains why it takes no barcode at ${vp.width}x${vp.height} ${vp.lang} (ut-docs#4343)`, async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await page.goto(`/catalog?lang=${vp.lang}`);
    const item = `Probe 4343 ${vp.width} ${vp.lang}`;
    await openNewItemForm(page);
    await page.locator('#item-name').fill(item);
    await page.locator('#item-price').fill('1.00');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
      page.locator('#item-form-submit').click(),
    ]);
    await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
    await page.locator('.catalog-row', { hasText: item }).click();
    await expect(page.locator('#item-form-modal')).toBeVisible();
    await page.locator('#item-form-tab-variants').click();
    await expect(page.locator('#catalog-variants')).toBeVisible();

    await addVariant(page, 'Small');
    await addVariant(page, 'Large');

    const large = await rowFor(page, 'Large');
    await large.locator('input[type=checkbox][name="isActive"]').uncheck();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/variant') && r.request().method() === 'POST'),
      large.locator('button[type=submit].primary').click(),
    ]);

    const off = await rowFor(page, 'Large');
    const hint = off.locator('[data-testid="variant-barcode-inactive"]');
    await expect(hint).toBeVisible();
    if (vp.lang === 'en') {
      await expect(hint).toContainText('Switched off');
      await expect(hint).toHaveAttribute('title', /switched off/);
    }
    await expect(off.locator('form.chip-add')).toHaveCount(0);
    const on = await rowFor(page, 'Small');
    await expect(on.locator('form.chip-add')).toHaveCount(1);
    await expect(on.locator('[data-testid="variant-barcode-inactive"]')).toHaveCount(0);

    // The hint must be readable where it sits: not under the row's sticky
    // Save button (#1967 pins that column over the grid's trailing edge)
    // and not over the Active checkbox.
    // Scrolled the way an operator reaches the Barcodes/Active columns: the
    // grid's own horizontal scroller all the way to its trailing end (a
    // negative scrollLeft in RTL).
    await hint.scrollIntoViewIfNeeded();
    const overlaps = await off.evaluate((row) => {
      const grid = row.closest('.variant-grid') as HTMLElement | null;
      if (grid) grid.scrollLeft = getComputedStyle(grid).direction === 'rtl' ? -grid.scrollWidth : grid.scrollWidth;
      const h = row.querySelector('[data-testid="variant-barcode-inactive"]')!.getBoundingClientRect();
      const hit = (sel: string) => {
        const o = row.querySelector(sel)!.getBoundingClientRect();
        return !(h.right <= o.left || o.right <= h.left || h.bottom <= o.top || o.bottom <= h.top);
      };
      return { save: hit('button[type=submit].primary'), active: hit('input[name="isActive"]') };
    });
    expect(overlaps).toEqual({ save: false, active: false });

    if (process.env.UT_SHOT_DIR) {
      await page.screenshot({ path: `${process.env.UT_SHOT_DIR}/variant-inactive-${vp.width}-${vp.lang}.png` });
    }
    assertClean();
  });
}
