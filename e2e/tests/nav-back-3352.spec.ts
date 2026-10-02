// ut-docs#3352 / ADR-0137: the shell Back. Product owner, on the iPhone app
// (no browser Back, swipe-back off): "when I go to the menu and then items,
// it should go back to the menu, and from item I go to inventory then I
// cannot go back to item". Back returns to the page the operator came from
// (history), and to the page's declared parent when there is none (a cold
// start or deep link). The page map lives in ut-docs
// reference/page-navigation-map.md.
import { test, expect, type Page } from './fixtures';

const back = (page: Page) => page.getByTestId('nav-back');

async function tapBackTo(page: Page, path: string) {
  await back(page).click();
  await expect(page).toHaveURL(new RegExp(`${path.replace(/\//g, '\\/')}$`));
  // The new page's own nav is rendered for it (Back is server-rendered).
  await expect(page.locator('#ut-page')).toBeVisible();
}

test.describe('shell Back (ut-docs#3352)', () => {
  test('Sell is the root: no Back there', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.nav')).toBeVisible();
    await expect(back(page)).toHaveCount(0);
  });

  test('phone: Menu → Items → Inventory, then Back, Back, Back walks the same way home', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await page.locator('.nav-drawer-toggle').click();
    await page.getByTestId('nav-menu').click();
    await expect(page).toHaveURL(/\/menu$/);
    await page.locator('a.menu-tile[href="/items"]').click();
    await expect(page).toHaveURL(/\/items$/);
    await page.locator('#items-rail a.items-row[href="/inventory"]').click();
    await expect(page).toHaveURL(/\/inventory$/);

    // Back sits in the phone bar itself, before ☰, with a 48px target.
    const box = await back(page).boundingBox();
    const menuBtn = await page.locator('.nav-drawer-toggle').boundingBox();
    expect(box && menuBtn).toBeTruthy();
    expect(box!.x).toBeLessThan(menuBtn!.x);
    expect(box!.width).toBeGreaterThanOrEqual(44);
    expect(box!.height).toBeGreaterThanOrEqual(44);
    await expect(back(page)).toHaveAttribute('aria-label', 'Back');

    // Each Back is a step back through history, never a new entry (the
    // parent link would push one and lose the way forward).
    const len = await page.evaluate(() => history.length);
    await tapBackTo(page, '/items');
    await tapBackTo(page, '/menu');
    await tapBackTo(page, '/');
    await expect(back(page)).toHaveCount(0);
    expect(await page.evaluate(() => history.length)).toBe(len);
  });

  test('Back goes where the operator came from, not to the parent', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await page.getByTestId('nav-orders').click();
    await expect(page).toHaveURL(/\/orders$/);
    await page.locator('.nav').getByTestId('kiosk-inventory-link').click();
    await expect(page).toHaveURL(/\/inventory$/);
    // Inventory's parent is Items; the operator came from Orders.
    await expect(back(page)).toHaveAttribute('href', '/items');
    await tapBackTo(page, '/orders');
    await tapBackTo(page, '/');
  });

  test('wide: Inventory opened in the Items panel goes Back to Items', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/menu');
    await page.locator('a.menu-tile[href="/items"]').click();
    await expect(page).toHaveURL(/\/items$/);
    await Promise.all([
      page.waitForResponse((r) => new URL(r.url()).pathname === '/inventory'),
      page.locator('#items-rail a.items-row[href="/inventory"]').click(),
    ]);
    await expect(page).toHaveURL(/\/inventory$/);
    await tapBackTo(page, '/items');
    await tapBackTo(page, '/menu');
  });

  test('a cold start on a page goes Back to its declared parent', async ({ page }) => {
    await page.goto('/inventory');
    await expect(back(page)).toHaveAttribute('href', '/items');
    await tapBackTo(page, '/items');
    // Items was reached by Back's fallback link, a new page: its Back is
    // history again, to Inventory.
    await tapBackTo(page, '/inventory');
  });

  test('a cold start on the first page with nothing behind it follows the parent link', async ({ page }) => {
    await page.goto('/settings');
    await expect(back(page)).toHaveAttribute('href', '/menu');
    await tapBackTo(page, '/menu');
    await expect(back(page)).toHaveAttribute('href', '/');
  });

  test('returning to the first page keeps it first: its Back is the parent, never out of the app', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/orders');
    await page.locator('.nav').getByTestId('kiosk-inventory-link').click();
    await expect(page).toHaveURL(/\/inventory$/);
    await tapBackTo(page, '/orders');
    // /orders is this tab's first in-app page: nothing in-app behind it.
    await tapBackTo(page, '/menu');
  });

  test('a page arrived at from another site never goes Back out of the app', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/orders');
    await page.locator('.nav').getByTestId('kiosk-inventory-link').click();
    await expect(page).toHaveURL(/\/inventory$/);
    // Another site in the same tab, then the till again by its address.
    await page.goto('data:text/html,<p>another site</p>');
    await page.goto('/inventory');
    await tapBackTo(page, '/items');
  });

  test('Settings: Back from a category returns to the category grid', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/menu');
    await page.locator('a.menu-tile[href="/settings"]').click();
    await expect(page).toHaveURL(/\/settings$/);
    const len = await page.evaluate(() => history.length);
    await page.locator('a.settings-cat-tile').first().click();
    await expect(page).toHaveURL(/\/settings#cat-/);
    await back(page).click();
    await expect(page).toHaveURL(/\/settings$/);
    await expect(page.locator('a.settings-cat-tile').first()).toBeVisible();
    await tapBackTo(page, '/menu');
    expect(await page.evaluate(() => history.length)).toBe(len + 1);
  });

  test('Back survives a reload: the entry keeps its place', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/orders');
    await page.locator('.nav').getByTestId('kiosk-inventory-link').click();
    await expect(page).toHaveURL(/\/inventory$/);
    await page.reload();
    await tapBackTo(page, '/orders');
  });

  test('rail: Back sits under the logo, above Sell, at the kiosk floor', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/items');
    const b = await back(page).boundingBox();
    const logo = await page.locator('.nav .logo').boundingBox();
    const sell = await page.getByTestId('nav-till').boundingBox();
    expect(b && logo && sell).toBeTruthy();
    expect(b!.y).toBeGreaterThanOrEqual(logo!.y + logo!.height - 1);
    expect(b!.y + b!.height).toBeLessThanOrEqual(sell!.y + 1);
    expect(b!.height).toBeGreaterThanOrEqual(44);
  });

  test('RTL: the arrow points the way the page reads', async ({ page }) => {
    await page.goto('/items?lang=ar');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    const t = await back(page).locator('svg').evaluate((el) => getComputedStyle(el).transform);
    expect(t).not.toBe('none');
  });
});
