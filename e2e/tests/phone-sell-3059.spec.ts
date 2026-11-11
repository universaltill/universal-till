// ut-docs#3059: the phone sale screen (<= 480px wide). The product owner's
// iPhone showed no product tile at all without scrolling: a wrapped
// three-row nav, the basket table and the tender block all came first.
// Owner-approved design (mock-up linked on the card): the nav is a 56px bar
// with a ☰ drawer, item tiles are app-icon squares (the item's image, else
// its colour with initials) filling the screen, and the basket collapses
// into a bottom bar (Dine in | Takeaway + "Pay £x · n items") that opens a
// sheet with the editable lines and the tender panel.
import { test, expect, type Page } from './fixtures';
import { setBrowsingMode } from './helpers';

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

  // Product owner, 2026-09-29, comparing the iPhone build with the mock-up:
  // search and edit in the top bar, the status row in the ☰ menu, Dine in |
  // Takeaway next to Pay, and the categories as a bar of pills.
  test('search and edit sit in the top bar; the search field opens under it', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await expect(page.locator('.btn-tile').first()).toBeVisible();
    const nav = (await page.locator('.nav').boundingBox())!;
    for (const sel of ['.products-strip-search', '.products-strip-edit']) {
      const b = (await page.locator(sel).boundingBox())!;
      expect(b.y, `${sel} is inside the top bar`).toBeGreaterThanOrEqual(nav.y);
      expect(b.y + b.height).toBeLessThanOrEqual(nav.y + nav.height + 1);
      expect(b.width).toBeGreaterThanOrEqual(44);
      expect(b.x + b.width, `${sel} is at the bar's end`).toBeGreaterThan(390 - 110);
    }
    // Nothing else in the bar sits under them.
    const logo = (await page.locator('.nav .logo').boundingBox())!;
    const search = (await page.locator('.products-strip-search').boundingBox())!;
    expect(logo.x + logo.width).toBeLessThanOrEqual(search.x);
    await page.locator('.products-strip-search').click();
    const input = page.locator('#products-search');
    await expect(input).toBeFocused();
    const ib = (await input.boundingBox())!;
    expect(ib.y, 'the field is under the bar, not over it').toBeGreaterThanOrEqual(nav.y + nav.height);
    await page.keyboard.press('Escape');
    await expect(page.locator('.products-strip-search')).toBeVisible();
  });

  test('the status row lives in the ☰ menu; a problem (offline) brings it back on the sale screen', async ({ page, context }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await expect(page.locator('.btn-tile').first()).toBeVisible();
    const status = page.locator('.statusbar');
    await expect(status, 'no status row over the tiles at rest').toBeHidden();
    await page.locator('.nav-drawer-toggle').click();
    await expect(status).toBeVisible();
    await expect(status.locator('.sb-ver')).toBeVisible();
    const drawer = (await page.locator('#nav-drawer').boundingBox())!;
    const sb = (await status.boundingBox())!;
    expect(Math.abs(sb.x - drawer.x), 'the row is the drawer\'s foot').toBeLessThanOrEqual(1);
    expect(Math.abs(sb.width - drawer.width)).toBeLessThanOrEqual(1);
    expect(Math.round(sb.y + sb.height)).toBeGreaterThanOrEqual(843);
    await page.keyboard.press('Escape');
    await expect(status).toBeHidden();
    await context.setOffline(true);
    try {
      await expect(page.locator('#sb-conn')).toHaveClass(/is-offline/);
      await expect(status, 'offline state is never hidden').toBeVisible();
      const bar = (await page.locator('#basket').boundingBox())!;
      const sb2 = (await status.boundingBox())!;
      expect(sb2.y + sb2.height).toBeLessThanOrEqual(bar.y + 1);
    } finally {
      await context.setOffline(false);
    }
  });

  test('after a boosted page change, going offline still re-measures the status row: Pay stays tappable', async ({ page, context }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await expect(page.locator('.btn-tile').first()).toBeVisible();
    // Sell -> Menu -> Sell through the drawer: hx-boost swaps #ut-page, status bar included.
    await page.locator('.nav-drawer-toggle').click();
    await page.locator('#nav-drawer .nav-primary a[href="/menu"]').click();
    await expect(page).toHaveURL(/\/menu/);
    await page.locator('.nav-drawer-toggle').click();
    await page.locator('#nav-drawer .nav-primary a[href="/"]').click();
    await expect(page.locator('.btn-tile').first()).toBeVisible();
    await addFirstTiles(page, 1);
    await page.locator('.basket-phonebar').click();
    await context.setOffline(true);
    try {
      const status = page.locator('.statusbar');
      await expect(status).toBeVisible();
      await expect.poll(async () => {
        const h = (await status.boundingBox())!.height;
        const v = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--phone-sb-h')) || 0);
        return Math.abs(v - h);
      }, { message: '--phone-sb-h follows the status row' }).toBeLessThanOrEqual(1);
      const pay = page.getByTestId('payment-open');
      await pay.scrollIntoViewIfNeeded();
      const hit = await pay.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!top && el.contains(top);
      });
      expect(hit, 'the status row never covers Pay').toBe(true);
    } finally {
      await context.setOffline(false);
    }
  });

  test('Dine in | Takeaway sits next to Pay, one row; categories are 40px pills', async ({ page }) => {
    await setBrowsingMode(page, 'all_filter_chips');
    try {
    for (const vp of [{ width: 390, height: 844 }, { width: 360, height: 800 }]) {
      await page.setViewportSize(vp);
      await page.goto('/');
      await expect(page.locator('.btn-tile').first()).toBeVisible();
      const seg = (await page.locator('#basket .order-type-toggle-group').boundingBox())!;
      const pay = (await page.locator('.basket-phonebar').boundingBox())!;
      expect(Math.abs(seg.y - pay.y), `${vp.width}: same row`).toBeLessThanOrEqual(2);
      expect(seg.x + seg.width).toBeLessThanOrEqual(pay.x);
      expect(pay.x + pay.width).toBeLessThanOrEqual(vp.width);
      expect(pay.height).toBeLessThanOrEqual(64);
      const chip = page.locator('.pos-container .products .filter-chips .chip').first();
      const c = (await chip.boundingBox())!;
      expect(Math.round(c.height)).toBe(40);
      expect(c.y, 'the pills sit right under the top bar').toBeLessThan(140);
    }
    } finally {
      await setBrowsingMode(page, 'strip_overflow');
    }
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

// ut-docs#3241: phone item tiles show how many of that item are in the sale.
// The number is server-rendered (#basket[data-item-counts]); app.js only copies it.
test.describe('in-basket tile badge (ut-docs#3241)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  const firstTile = (page: Page) => page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first();
  const tapFirst = (page: Page) =>
    Promise.all([page.waitForResponse((r) => r.url().includes('/api/pos/scan')), firstTile(page).click()]);

  test('fresh sale has no badge; two taps read 2 from the server count; + and - follow; remove clears', async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await expect(firstTile(page)).toBeVisible();
    await expect(page.locator('.tile-qty-badge')).toHaveCount(0);
    await tapFirst(page);
    await tapFirst(page);
    const badge = firstTile(page).locator('.tile-qty-badge');
    await expect(badge).toBeVisible();
    await expect(badge).toHaveText('2');
    await expect(badge).toHaveAttribute('aria-hidden', 'true');
    await expect(page.locator('.tile-qty-badge')).toHaveCount(1);
    const id = (await firstTile(page).getAttribute('data-item-id'))!;
    const counts = JSON.parse((await page.locator('#basket').getAttribute('data-item-counts'))!);
    expect(counts).toEqual({ [id]: 2 });
    await expect(firstTile(page).getByText('2 in basket')).toHaveCount(1);
    expect(await firstTile(page).evaluate((b) => b.lastElementChild!.className)).toContain('tile-qty-sr');
    // a tap on the badge's corner is still a tap on the tile
    expect(await badge.evaluate((el) => getComputedStyle(el).pointerEvents)).toBe('none');
    expect(await badge.evaluate((el) => getComputedStyle(el).transitionDuration)).toBe('0s');

    await page.locator('.basket-phonebar').click();
    const row = page.locator('#basket tbody tr').first();
    await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'POST'),
      row.locator('.qty-step-btn').last().click(),
    ]);
    await page.locator('.basket-sheet-close').click();
    await expect(badge).toHaveText('3');
    await page.locator('.basket-phonebar').click();
    await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'POST'),
      page.locator('#basket tbody tr').first().locator('.qty-step-btn').first().click(),
    ]);
    await page.locator('.basket-sheet-close').click();
    await expect(badge).toHaveText('2');

    await page.request.post('/api/pos/reset');
    await page.goto('/');
    await expect(page.locator('.tile-qty-badge')).toHaveCount(0);
  });

  test('qty 120 reads 99+ with the real number in the accessible name; survives a grid re-render', async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    const itemId = (await firstTile(page).getAttribute('data-item-id'))!;
    await tapFirst(page);
    await page.locator('.basket-phonebar').click();
    const qty = page.locator('#basket tbody tr').first().locator('input[name="qty"]');
    await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'POST'),
      qty.fill('120').then(() => qty.press('Enter')),
    ]);
    await page.locator('.basket-sheet-close').click();
    await expect(firstTile(page).locator('.tile-qty-badge')).toHaveText('99+');
    await expect(page.getByRole('button', { name: /120 in basket/ })).toHaveCount(1);
    // Re-render the grid without a basket action: badges come back at once.
    await page.evaluate(() => (window as any).htmx.trigger(document.body, 'buttons-changed'));
    await expect(firstTile(page).locator('.tile-qty-badge')).toHaveText('99+');
    await page.reload();
    await expect(firstTile(page).locator('.tile-qty-badge')).toHaveText('99+');
    await setBrowsingMode(page, 'all_filter_chips');
    try {
      await page.goto('/');
      // The All view orders tiles differently: find this item's tile by id.
      await expect(page.locator(`.pos-container .tile-cell > .btn-tile[data-item-id="${itemId}"] .tile-qty-badge`).first()).toHaveText('99+');
    } finally {
      await setBrowsingMode(page, 'strip_overflow');
    }
  });

  test('sits on the icon top inline-end corner: right in LTR, left in RTL (fa)', async ({ page }) => {
    for (const [lang, rtl] of [['en', false], ['fa', true]] as const) {
      await page.request.post('/api/pos/reset');
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(`/?lang=${lang}`);
      await tapFirst(page);
      const badge = (await firstTile(page).locator('.tile-qty-badge').boundingBox())!;
      const icon = (await firstTile(page).locator('.tile-initials, .thumb').first().boundingBox())!;
      expect(badge.width).toBeGreaterThanOrEqual(20);
      if (rtl) expect(badge.x, 'fa: top-left').toBeLessThanOrEqual(icon.x + icon.width * 0.3);
      else expect(badge.x + badge.width, 'en: top-right').toBeGreaterThanOrEqual(icon.x + icon.width * 0.7);
      expect(badge.y, 'top').toBeLessThanOrEqual(icon.y + icon.height * 0.3);
    }
  });

  test('hidden in jiggle mode; visible again after', async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await tapFirst(page);
    const badge = firstTile(page).locator('.tile-qty-badge');
    await expect(badge).toBeVisible();
    await page.evaluate(() => document.querySelector('#buttons-grid, #buttons-grid-all')!.classList.add('jiggle-mode'));
    await expect(badge).toBeHidden();
    await page.evaluate(() => document.querySelector('#buttons-grid, #buttons-grid-all')!.classList.remove('jiggle-mode'));
    await expect(badge).toBeVisible();
  });

  for (const vp of [{ width: 768, height: 1024 }, { width: 1024, height: 600 }, { width: 1280, height: 800 }]) {
    test(`${vp.width}x${vp.height}: no badge is visible`, async ({ page }) => {
      await page.request.post('/api/pos/reset');
      await page.setViewportSize(vp);
      await page.goto('/');
      await tapFirst(page);
      await expect(page.locator('#basket')).toHaveAttribute('data-item-counts', /\{"/);
      await expect(page.locator('.tile-qty-badge').first()).toBeHidden();
      await expect(page.locator('.tile-qty-sr').first()).toBeHidden();
    });
  }
});
