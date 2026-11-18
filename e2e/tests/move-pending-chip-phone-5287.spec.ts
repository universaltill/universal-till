import { test, expect, type Page } from './fixtures';

// ut-docs#5287: the read-only "Till move waiting for a manager" chip in the
// status bar. On the phone sale screen the status row is normally hidden
// (ut-docs#3358); the chip must bring it back above the pay bar without
// covering the basket or Pay, and an empty body must leave it hidden. Named
// "phone" so the webkit-phone job runs it too.

const LABEL = 'Till move waiting for a manager';
const LONG_LABEL = 'Verschiebung der Kasse wartet auf eine Führungsperson zur Freigabe';

function chipHTML(label: string): string {
  // Same markup as web/ui/partials/move_pending_chip.html, cashier form (no href).
  return (
    `<a class="sb-item sb-move-pending" data-testid="sb-move-pending" title="${label}">` +
    `<span class="btn-ico" aria-hidden="true"></span>` +
    `<span class="sb-move-pending-text">${label}</span></a>`
  );
}

async function routeChip(page: Page, label: string | null) {
  await page.route('**/ui/move-pending-chip', (route) =>
    route.fulfill({ status: 200, contentType: 'text/html', body: label === null ? '' : chipHTML(label) }),
  );
}

async function boxes(page: Page) {
  return page.evaluate(() => {
    const r = (sel: string) => {
      const el = document.querySelector(sel);
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { top: b.top, bottom: b.bottom, left: b.left, right: b.right, width: b.width, height: b.height };
    };
    return {
      chip: r('[data-testid=sb-move-pending]'),
      bar: r('.statusbar'),
      pay: r("#basket"),
      basket: r('.pos-container > .basket'),
      grid: r('.pos-container'),
      vw: document.documentElement.clientWidth,
      vh: window.innerHeight,
      sideways: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    };
  });
}

const intersects = (a: { top: number; bottom: number; left: number; right: number }, b: typeof a) =>
  a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;

test.describe('pending-move chip on the phone sale screen (ut-docs#5287)', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  for (const label of [LABEL, LONG_LABEL]) {
    test(`the chip brings the status row up without covering Pay or the basket ("${label.slice(0, 12)}...")`, async ({ page }) => {
      await routeChip(page, label);
      await page.goto('/');
      await expect(page.locator('.statusbar')).toBeVisible();
      await expect(page.locator('[data-testid=sb-move-pending]')).toBeVisible();
      const g = await boxes(page);
      expect(g.chip!.left).toBeGreaterThanOrEqual(-0.5);
      expect(g.chip!.right).toBeLessThanOrEqual(g.vw + 0.5);
      expect(g.chip!.bottom).toBeLessThanOrEqual(g.vh + 0.5);
      expect(g.sideways).toBe(false);
      if (g.pay) expect(intersects(g.chip!, g.pay)).toBe(false);
      if (g.basket) expect(intersects(g.chip!, g.basket)).toBe(false);
    });
  }

  test('an empty body leaves the phone status row hidden (the :has() guard)', async ({ page }) => {
    await routeChip(page, null);
    await page.goto('/');
    await expect(page.locator('#sb-move-pending-mount')).toBeAttached();
    await expect(page.locator('.statusbar')).toBeHidden();
  });
});

test.describe('pending-move chip at the 1024x600 kiosk size (ut-docs#5287)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('the chip shows and the bar grows by at most one wrapped row', async ({ page }) => {
    await routeChip(page, null);
    await page.goto('/');
    await expect(page.locator('#sb-move-pending-mount')).toBeAttached();
    const without = await boxes(page);

    await page.unroute('**/ui/move-pending-chip');
    await routeChip(page, LONG_LABEL);
    await page.goto('/');
    await expect(page.locator('[data-testid=sb-move-pending]')).toBeVisible();
    const withChip = await boxes(page);
    expect(withChip.sideways).toBe(false);
    expect(withChip.bar!.height).toBeLessThanOrEqual(without.bar!.height + withChip.chip!.height * 2);
    if (without.grid && withChip.grid) {
      expect(Math.abs(withChip.grid.top - without.grid.top)).toBeLessThanOrEqual(withChip.chip!.height * 2);
    }
  });
});
