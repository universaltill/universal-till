// ut-docs#3059: the phone sale screen (<= 480px wide). The product owner's
// iPhone showed no product tile at all without scrolling: a wrapped
// three-row nav, the basket table and the tender block all came first.
// Owner-approved design (mock-up linked on the card): the nav is a 56px bar
// with a ☰ drawer, item tiles are app-icon squares (the item's image, else
// its colour with initials) filling the screen, and the basket collapses
// into a bottom bar (Dine in | Takeaway + "Pay £x · n items") that opens a
// sheet with the editable lines and the tender panel.
import { test, expect, type Page } from './fixtures';

async function geometry(page: Page) {
  return page.evaluate(() => {
    const bar = document.querySelector('.basket-phonebar') as HTMLElement | null;
    const barTop = bar ? bar.getBoundingClientRect().top : innerHeight;
    const nav = document.querySelector('.nav')!.getBoundingClientRect();
    const tiles = [...document.querySelectorAll('.btn-tile')].filter((t) => {
      const r = t.getBoundingClientRect();
      return r.width > 0 && r.top >= nav.bottom && r.bottom <= barTop && r.left >= 0 && r.right <= innerWidth;
    });
    return {
      scrollH: document.documentElement.scrollHeight,
      clientH: document.documentElement.clientHeight,
      scrollW: document.documentElement.scrollWidth,
      clientW: document.documentElement.clientWidth,
      navH: nav.height,
      fullTiles: tiles.length,
    };
  });
}

async function addFirstTiles(page: Page, n: number) {
  for (let i = 0; i < n; i++) {
    const tile = page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').nth(i);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      tile.click(),
    ]);
  }
}

test.describe('phone sale screen (ut-docs#3059)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('paying on a phone shows the receipt in full; New Customer goes back to the tiles', async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await addFirstTiles(page, 1);
    await page.locator('.basket-phonebar').click();
    await page.getByTestId('payment-open').click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#payment-overlay').getByTestId('pay-default').click(),
    ]);
    await expect(page.locator('#basket.receipt-view')).toBeVisible();
    await expect(page.locator('body'), 'the receipt stays in the open sheet').toHaveClass(/pos-sheet-open/);
    const next = page.getByTestId('receipt-new-customer');
    await expect(next).toBeInViewport();
    await next.click();
    await expect(page.locator('body')).not.toHaveClass(/pos-sheet-open/);
    await expect(page.locator('.basket-phonebar')).toBeVisible();
    await expect(page.locator('.btn-tile').first()).toBeInViewport();
  });

  test('removing the only line keeps the sheet open (only a finished sale returns to the tiles)', async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await addFirstTiles(page, 1);
    await page.locator('.basket-phonebar').click();
    const row = page.locator('#basket tbody tr').first();
    await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'POST'),
      row.locator('.qty-step-btn').first().click(),
    ]);
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
    await expect(page.locator('body')).toHaveClass(/pos-sheet-open/);
  });

  for (const vp of [{ width: 390, height: 844 }, { width: 360, height: 800 }]) {
    for (const lang of ['en', 'de', 'fa']) {
      test(`${vp.width}x${vp.height} ${lang}: one-row top bar, tiles fill the screen, no page scroll`, async ({ page }) => {
        await page.setViewportSize(vp);
        await page.goto(`/?lang=${lang}`);
        await expect(page.locator('.btn-tile').first()).toBeVisible();
        const g = await geometry(page);
        expect(g.scrollW, 'no horizontal scroll').toBeLessThanOrEqual(g.clientW);
        expect(g.scrollH, 'the page itself never scrolls; the grid does').toBeLessThanOrEqual(g.clientH);
        expect(g.navH, 'one-row top bar').toBeLessThanOrEqual(64);
        expect(g.fullTiles, 'at least three rows of tiles fully visible').toBeGreaterThanOrEqual(9);
        await expect(page.locator('.nav-drawer-toggle')).toBeVisible();
        await expect(page.locator('#nav-drawer .nav-primary')).toBeHidden();
        await expect(page.locator('.basket-phonebar')).toBeVisible();
        await expect(page.locator('#basket .order-type-row')).toBeVisible();
        await expect(page.locator('.pos-container > .tender')).toBeHidden();
      });
    }
  }

  test('☰ opens the drawer with the nav links and chips; Escape and the backdrop close it', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    const toggle = page.locator('.nav-drawer-toggle');
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    const drawer = page.locator('#nav-drawer');
    await expect(drawer.locator('.nav-primary a').first()).toBeVisible();
    await expect(drawer.locator('.nav-right')).toBeVisible();
    const box = (await drawer.boundingBox())!;
    expect(box.x, 'LTR: drawer slides in from the left').toBeLessThanOrEqual(1);
    await page.keyboard.press('Escape');
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await expect(drawer.locator('.nav-primary')).toBeHidden();
    await expect(toggle).toBeFocused();
    await toggle.click();
    await page.locator('.nav-drawer-backdrop').click({ position: { x: 370, y: 400 } });
    await expect(drawer.locator('.nav-primary')).toBeHidden();
  });

  test('fa (RTL): the drawer comes in from the inline-start (right) edge', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/?lang=fa');
    await page.locator('.nav-drawer-toggle').click();
    const box = (await page.locator('#nav-drawer').boundingBox())!;
    expect(Math.round(box.x + box.width)).toBeGreaterThanOrEqual(359);
  });

  test('tapping tiles updates the pay bar; the bar opens an editable basket sheet with Pay', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await addFirstTiles(page, 2);
    const total = await page.locator('#basket .total').getAttribute('data-label');
    await expect(page.locator('.basket-phonebar')).toContainText(total!);
    await expect(page.locator('.basket-phonebar')).toContainText('2');
    await page.locator('.basket-phonebar').click();
    await expect(page.locator('body')).toHaveClass(/pos-sheet-open/);
    await expect(page.locator('#basket tbody tr').first()).toBeVisible();
    await expect(page.locator('[data-testid="payment-open"]')).toBeVisible();
    const qty = page.locator('#basket tbody tr').first().locator('input[name="qty"]');
    const before = Number(await qty.inputValue());
    await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'POST'),
      page.locator('#basket tbody tr').first().locator('.qty-step-btn').last().click(),
    ]);
    await expect(page.locator('#basket tbody tr').first().locator('input[name="qty"]')).toHaveValue(String(before + 1));
    await expect(page.locator('body'), 'a basket swap keeps the sheet open').toHaveClass(/pos-sheet-open/);
    await page.locator('.basket-sheet-close').click();
    await expect(page.locator('body')).not.toHaveClass(/pos-sheet-open/);
    await expect(page.locator('.btn-tile').first()).toBeVisible();
  });

  test('tiles are app icons: square, an image fills it, else initials on the colour', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    const first = page.locator('.pos-container .btn-tile').first();
    await expect(first.locator('.tile-initials')).toBeVisible();
    const sq = await first.locator('.tile-initials').boundingBox();
    expect(Math.abs(sq!.width - sq!.height), 'the icon is square').toBeLessThanOrEqual(2);
    expect(sq!.width).toBeGreaterThanOrEqual(80);
    // An item with a picture: the same square, filled by the image.
    await first.evaluate((b) => {
      const img = document.createElement('img');
      img.className = 'thumb';
      img.src = '/public/assets/logo/unitill-logo-light.svg';
      b.prepend(img);
      b.querySelector('.tile-initials')?.remove();
    });
    const im = await first.locator('.thumb').boundingBox();
    expect(Math.abs(im!.width - im!.height)).toBeLessThanOrEqual(2);
    expect(Math.abs(im!.width - sq!.width)).toBeLessThanOrEqual(2);
  });

  test('with a dialog open the pay bar is under the scrim, the status row stays above it', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/');
    await page.locator('.btn-tile').first().waitFor();
    await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).show());
    await expect(page.locator('html')).toHaveClass(/ut-scrim-on/);
    const z = await page.evaluate(() => ({
      scrim: Number(getComputedStyle(document.getElementById('ut-scrim')!).zIndex),
      bar: Number(getComputedStyle(document.getElementById('basket')!).zIndex),
      status: Number(getComputedStyle(document.querySelector('.statusbar')!).zIndex),
    }));
    expect(z.bar, 'the pay bar must sit under the scrim, never tappable behind a dialog').toBeLessThan(z.scrim);
    expect(z.status, 'the status row stays reachable over the scrim').toBeGreaterThan(z.scrim);
  });

  for (const vp of [{ width: 1024, height: 600 }, { width: 800, height: 1280 }]) {
    test(`${vp.width}x${vp.height}: the tablet layout is unchanged (rail, no phone bar, no initials)`, async ({ page }) => {
      await page.setViewportSize(vp);
      await page.goto('/');
      await expect(page.locator('.btn-tile').first()).toBeVisible();
      await expect(page.locator('.nav-drawer-toggle')).toBeHidden();
      await expect(page.locator('.nav-primary')).toBeVisible();
      await expect(page.locator('.basket-phonebar')).toBeHidden();
      await expect(page.locator('.tile-initials').first()).toBeHidden();
      await expect(page.locator('.pos-container > .tender')).toBeVisible();
    });
  }
});
