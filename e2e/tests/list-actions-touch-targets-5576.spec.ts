import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, clickThenReload } from './helpers';

// ut-docs#5576: (1) a list row's cells all end on the same line — .list-actions
// on a <td> used display:flex, so that cell stopped being a table cell and its
// border-bottom sat above the row's on a wrapped row; (2) the record dialog's
// exit-to-OS and lock buttons were 33x32, below the 44px touch floor.

const LONG = 'Weekend offer on every hot drink and pastry plus a very long extra sentence so this cell wraps onto several lines';

function isoDay(offsetDays: number): string {
  return new Date(Date.now() + offsetDays * 86_400_000).toISOString().slice(0, 10);
}

async function seedPromo(page: Page, code: string) {
  const r = await page.request.post('/api/promotions', {
    form: { code, type: 'amount', value_amount: '12.50', description: LONG,
            starts_at: isoDay(-1), ends_at: isoDay(200), customer_id: '' },
    maxRedirects: 0,
  });
  expect(r.status()).toBe(303);
}

async function createViaDialog(page: Page, newBtn: string, dialog: string, nameSel: string, name: string) {
  await page.locator(newBtn).click();
  await expect(page.locator(dialog)).toBeVisible();
  await page.locator(nameSel).fill(name);
  await clickThenReload(page, () => page.locator(`${dialog} .record-dialog-save`).click());
}

async function cellGeometry(page: Page, rowSel: string) {
  return page.locator(rowSel).first().evaluate((tr) => {
    const trB = tr.getBoundingClientRect().bottom;
    const tds = (Array.from(tr.querySelectorAll(':scope > td')) as HTMLElement[]).filter((td) => getComputedStyle(td).display !== 'none'); // hidden data cells render nothing
    const act = tr.querySelector(':scope > td.list-actions') as HTMLElement | null;
    const btn = tr.querySelector('.btn-icon') as HTMLElement | null;
    return {
      trBottom: trB,
      rowHeight: tr.getBoundingClientRect().height,
      btnHeight: btn ? btn.getBoundingClientRect().height : 0,
      bottoms: tds.map((td) => td.getBoundingClientRect().bottom),
      displays: tds.map((td) => getComputedStyle(td).display),
      actBottom: act ? act.getBoundingClientRect().bottom : null,
      hasActions: !!act,
      btnWrapped: btn ? btn.getBoundingClientRect().height > 60 : false,
    };
  });
}

function expectAligned(g: Awaited<ReturnType<typeof cellGeometry>>, needActions: boolean) {
  expect(g.rowHeight, 'precondition: row must be taller than its button so the wrap case is real').toBeGreaterThan(g.btnHeight + 12);
  expect(g.displays.every((d) => d === 'table-cell'), `displays ${g.displays}`).toBe(true);
  expect(Math.max(...g.bottoms) - Math.min(...g.bottoms), `bottoms ${g.bottoms}`).toBeLessThanOrEqual(1);
  if (needActions) {
    expect(g.hasActions).toBe(true);
    expect(Math.abs(g.actBottom! - g.trBottom)).toBeLessThanOrEqual(1);
  }
}

test.describe('list-actions borders + status-row touch targets (ut-docs#5576)', () => {
  test.use({ viewport: { width: 800, height: 600 } });

  test('/promotions: every cell, incl. td.list-actions, ends on the row line', async ({ page }) => {
    await seedPromo(page, 'B5576' + Date.now());
    await page.goto('/promotions');
    expectAligned(await cellGeometry(page, '#promotions-table .promotion-row'), true);
  });

  test('/locations: every cell, incl. td.list-actions, ends on the row line', async ({ page }) => {
    const name = 'E2E Very Long Location Name For Wrapping Check ' + Date.now() + ' Annex Building North Wing';
    await page.goto('/locations');
    await createViaDialog(page, '#locations-new', '#location-dialog', '#location-form input[name="name"]', name);
    await page.setViewportSize({ width: 600, height: 600 });
    const g = await cellGeometry(page, `#locations-table .location-row:has-text("${name.slice(0, 30)}")`);
    expectAligned(g, true);
  });

  test('/categories: cells share one bottom (regression guard)', async ({ page }) => {
    const name = 'E2E Very Long Category Name For Wrapping Check ' + Date.now() + ' Annex Building';
    await page.goto('/categories');
    await createViaDialog(page, '#categories-new', '#category-dialog', '#category-form input[name="name"]', name);
    await page.setViewportSize({ width: 500, height: 600 });
    const g = await cellGeometry(page, `#categories-table .category-row:has-text("${name.slice(0, 30)}")`);
    expect(g.displays.every((d) => d === 'table-cell')).toBe(true);
    expect(Math.max(...g.bottoms) - Math.min(...g.bottoms)).toBeLessThanOrEqual(1);
  });

  for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
    test(`record dialog exit + lock are >=44x44 at ${vp.width}x${vp.height}`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await seedPromo(page, `T5576${vp.width}`);
      await page.goto('/promotions');
      await page.locator('#promotions-table .promotion-row', { hasText: `T5576${vp.width}` }).first().locator('td').first().click();
      const DIALOG = '#promotion-dialog';
      await expect(page.locator(DIALOG)).toBeVisible();
      const exit = page.locator(`${DIALOG} [data-record-dialog-exit]`);
      const lock = page.locator(`${DIALOG} [data-record-dialog-lock] button`);
      for (const el of [exit, lock]) {
        await expect(el).toBeVisible();
        const b = (await el.boundingBox())!;
        expect(b.width, 'width').toBeGreaterThanOrEqual(43.5);
        expect(b.height, 'height').toBeGreaterThanOrEqual(43.5);
        expect(b.x).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width).toBeLessThanOrEqual(vp.width + 0.5);
        expect(b.y + b.height).toBeLessThanOrEqual(vp.height + 0.5);
      }
      expect(await exit.getAttribute('href')).toBe('/settings#settings-display');
      expect(await page.locator(`${DIALOG} [data-record-dialog-lock]`).getAttribute('action')).toBe('/api/auth/logout');
      const row = (await page.locator(`${DIALOG} [data-record-dialog-status-row]`).boundingBox())!;
      console.log(`MEASURE ${vp.width}x${vp.height} statusRow h=${row.height} exit=${JSON.stringify(await exit.boundingBox())}`);
      assertClean();
    });
  }
});
