import { test, expect } from './fixtures';

// ut-docs#1349 (product owner, reported twice live, "order button is not on
// the left menu"): Orders was reachable only via ☰ Menu → Orders — no rail
// shortcut on the sale screen itself. This adds a one-tap rail button
// (web/ui/partials/nav.html, .nav-primary), same .nav-toggle pattern as the
// existing Till/Menu/Inventory items, linking straight to the already-live
// /orders page (internal/pages/order_status.go).
test.describe('sale-screen "Orders" rail shortcut (ut-docs#1349)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('a rail button for Orders is visible on the sale screen, same treatment as its siblings', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const orders = page.locator('[data-testid="nav-orders"]');
    const menu = page.locator('[data-testid="nav-menu"]'); // ordinary .nav-toggle sibling, as reference
    await expect(orders).toBeVisible();
    await expect(orders).toHaveClass(/\bnav-toggle\b/);
    await expect(orders.locator('.nav-toggle-ico')).toBeVisible();
    // Real (if visually-hidden) accessible label, not a tooltip-only
    // affordance — same requirement nav.html's own top comment states for
    // every rail item (no hover on a touchscreen till).
    await expect(orders).toHaveAccessibleName(/.+/);
    await expect(orders.locator('.nav-toggle-ico')).toHaveText('🛎️');

    // Same visual box treatment as an existing sibling: centered, same class.
    const ordersBox = (await orders.boundingBox())!;
    const menuBox = (await menu.boundingBox())!;
    expect(ordersBox.width, 'same fixed rail-button width as an existing sibling').toBeCloseTo(menuBox.width, 0);
  });

  test('tapping the Orders rail button navigates to the orders page', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await page.locator('[data-testid="nav-orders"]').click();
    await expect(page).toHaveURL(/\/orders$/);
    await expect(page.locator('#orders')).toBeAttached();
  });

  test('the Orders rail button is hidden at phone width, same as Inventory (ut-docs#413 budget)', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 640 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    await expect(page.locator('[data-testid="nav-orders"]')).toBeHidden();
    // Still reachable via the Menu launcher at this width (unchanged by this
    // card — ut-docs#1349's own scoping: promote the shortcut, don't touch
    // the tile it complements). Scoped to .menu-tile: the rail's own
    // visually-hidden label text for the same word is still in the DOM
    // (nav-rail-only hides via CSS, not removal), so a bare text locator
    // matches both and fails strict mode.
    await page.goto('/menu');
    await page.waitForSelector('.menu-grid');
    await expect(page.locator('.menu-tile', { hasText: 'Orders' })).toBeVisible();
  });
});
