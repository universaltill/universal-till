import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3090: Settings for an admin who "doesn't understand computers at
// all" — /settings opens on a grid of large category tiles (My shop,
// Selling, Payments, …, Advanced), each with an icon and a one-line
// explanation. A tile opens the two-pane view with only that category's
// sections; Advanced keeps every section. Still one server render,
// show/hide only (ADR-0008). settings-two-pane-1960.spec.ts covers the
// two-pane mechanics inside a view; this covers moving between views.

test.describe('settings categories (ut-docs#3090)', () => {
  test('no hash: the category grid, Advanced last, the two-pane shell hidden', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings');

    await expect(page.locator('#settings-home')).toBeVisible();
    await expect(page.locator('#settings-shell')).toBeHidden();
    const tiles = page.locator('#settings-home .settings-cat-tile');
    expect(await tiles.count()).toBeGreaterThanOrEqual(8);
    await expect(tiles.first()).toContainText('My shop');
    await expect(tiles.first().locator('.settings-cat-desc')).toContainText('Currency, language');
    await expect(tiles.first().locator('svg[data-icon="store"]')).toBeVisible();
    await expect(tiles.last()).toHaveAttribute('data-cat', 'advanced');

    // Touch-sized: every tile at least 44px tall, and wide enough for a
    // thumb (the 1024x600 kiosk floor).
    for (const box of await tiles.evaluateAll((els) => els.map((e) => e.getBoundingClientRect().toJSON()))) {
      expect(box.height).toBeGreaterThanOrEqual(44);
      expect(box.width).toBeGreaterThanOrEqual(120);
    }
    await page.screenshot({ path: test.info().outputPath('settings-home-1024x600.png'), fullPage: true });
    assertClean();
  });

  test('a tile opens only its category; All settings returns; Back works', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');

    await page.locator('.settings-cat-tile[data-cat="look"]').click();
    await expect(page).toHaveURL(/#cat-look$/);
    await expect(page.locator('#settings-home')).toBeHidden();
    await expect(page.locator('#settings-shell')).toBeVisible();
    await expect(page.locator('#settings-cat-title')).toHaveText('Look & feel');

    const listed = await page.locator('#settings-tree a[data-section]:visible').evaluateAll((els) => els.map((e) => e.getAttribute('data-section')));
    expect(listed).toEqual(['settings-menulayout', 'settings-theme', 'settings-display']);
    // No redundant group heading inside a single category.
    await expect(page.locator('#settings-tree .settings-tree-group:visible')).toHaveCount(0);
    // The first section of the category is open at tablet+ width.
    await expect(page.locator('#settings-menulayout')).toBeVisible();
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.screenshot({ path: test.info().outputPath('settings-cat-look-1024x600.png') });
    await page.setViewportSize({ width: 1280, height: 800 });

    await page.locator('#settings-tree a[data-section="settings-theme"]').click();
    await expect(page.locator('#settings-theme')).toBeVisible();

    await expect(page.locator('#settings-home-link')).toHaveText('Back to categories');
    await page.locator('#settings-home-link').click();
    await expect(page.locator('#settings-home')).toBeVisible();
    await expect(page.locator('#settings-shell')).toBeHidden();
    await expect(page).toHaveURL(/\/settings$/);

    // Browser Back returns to the category left.
    await page.goBack();
    await expect(page.locator('#settings-shell')).toBeVisible();
    await expect(page.locator('#settings-home')).toBeHidden();
    assertClean();
  });

  test('Advanced lists every section under category headings', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');
    await page.locator('.settings-cat-tile[data-cat="advanced"]').click();
    await expect(page.locator('#settings-cat-title')).toHaveText('Advanced');
    const cards = await page.locator('#settings-grid > .card').count();
    await expect(page.locator('#settings-tree a[data-section]:visible')).toHaveCount(cards);
    const headings = await page.locator('#settings-tree .settings-tree-group:visible').allTextContents();
    expect(headings[0]).toBe('My shop');
    expect(headings[headings.length - 1]).toBe('Advanced');
    // The protected sections keep their labels and stay reachable (AC 4).
    await expect(page.locator('#settings-tree a[data-section="settings-all"]')).toBeVisible();
    await expect(page.locator('#settings-tree a[data-section="settings-retention"]')).toContainText('Report retention');
    assertClean();
  });

  test('a deep link to a section opens it inside its own category', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings#settings-printer');
    await expect(page.locator('#settings-printer')).toBeVisible();
    await expect(page.locator('#settings-cat-title')).toHaveText('Receipts & printers');
    await expect(page.locator('#settings-tree a[data-section="settings-printer"]')).toHaveAttribute('aria-current', 'page');
    // A technical section with no simple category opens in Advanced.
    await page.goto('/settings#settings-telemetry');
    await expect(page.locator('#settings-telemetry')).toBeVisible();
    await expect(page.locator('#settings-cat-title')).toHaveText('Advanced');
    assertClean();
  });

  test('searching from the grid searches every setting', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');
    await page.locator('#settings-home-q').fill('find printers');
    await expect(page.locator('#settings-shell')).toBeVisible();
    await expect(page.locator('#settings-q')).toHaveValue('find printers');
    await expect(page.locator('#settings-q')).toBeFocused();
    const hit = page.locator('#settings-tree a[data-hit]').first();
    await expect(hit).toContainText('Find printers on this network');
    await hit.click();
    await expect(page.locator('#settings-printer')).toBeVisible();
    // Back returns to the grid, not off /settings (review of #3090).
    await page.goBack();
    await expect(page.locator('#settings-home')).toBeVisible();
    await expect(page).toHaveURL(/\/settings$/);
    assertClean();
  });

  test('phone width: the grid fits, a tile opens its list first', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/settings');
    await expect(page.locator('#settings-home')).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, 'no horizontal scroll at 360px').toBeLessThanOrEqual(0);
    await page.screenshot({ path: test.info().outputPath('settings-home-360.png'), fullPage: true });

    await page.locator('.settings-cat-tile[data-cat="selling"]').click();
    await expect(page.locator('#settings-nav')).toBeVisible();
    await expect(page.locator('#settings-panel')).toBeHidden();
    await expect(page.locator('#settings-tree a[data-section="settings-order-no"]')).toBeVisible();
    await page.locator('#settings-tree a[data-section="settings-order-no"]').click();
    await expect(page.locator('#settings-order-no')).toBeVisible();

    // Review of #3090: Back to the grid, then another tile, must open that
    // category's LIST — the section left open before must not carry over.
    await page.goBack();
    await expect(page.locator('#settings-home')).toBeVisible();
    await page.locator('.settings-cat-tile[data-cat="shop"]').click();
    await expect(page.locator('#settings-nav')).toBeVisible();
    await expect(page.locator('#settings-panel')).toBeHidden();
    await expect(page.locator('#settings-tree a[data-section="settings-currency"]')).toBeVisible();
    assertClean();
  });

  test('keyboard: Enter on a tile moves focus to the category title', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings');
    await page.locator('.settings-cat-tile[data-cat="receipts"]').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('#settings-cat-title')).toBeFocused();
    await expect(page.locator('#settings-cat-title')).toHaveText('Receipts & printers');
    assertClean();
  });

  test('RTL (fa): the grid and the category view mirror', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await expect(page.locator('.settings-cat-tile').first()).toContainText('فروشگاه من');
    await page.screenshot({ path: test.info().outputPath('settings-home-fa.png'), fullPage: true });
    await page.locator('.settings-cat-tile[data-cat="shop"]').click();
    const nav = (await page.locator('#settings-nav').boundingBox())!;
    const panel = (await page.locator('#settings-panel').boundingBox())!;
    expect(nav.x, 'RTL: the section list sits on the right').toBeGreaterThan(panel.x);
    assertClean();
  });
});
