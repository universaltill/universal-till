import { test, expect } from './fixtures';
import { ensureOperator, openPhoneDrawer } from './helpers';

// ut-docs#3358: on the sale screen and the Menu screen the phone ☰ drawer
// hid the manager links (Users / Promotions / Translations) — the rules that
// hide them there only exist to keep the tablet RAIL short — so an
// administrator saw nothing but their name (Change my PIN). The drawer is a
// scrollable column: it always lists them. The tablet rail is unchanged.
test.describe('phone drawer', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  for (const path of ['/', '/menu']) {
    test(`${path}: drawer lists Users, Promotions and Translations`, async ({ page }) => {
      await ensureOperator(page);
      await page.goto(path);
      await openPhoneDrawer(page);
      const links = page.locator('#nav-drawer .session-admin-link');
      await expect(links).toHaveCount(3);
      for (let i = 0; i < 3; i++) await expect(links.nth(i)).toBeVisible();
      for (const name of ['Users', 'Promotions', 'Translations']) {
        await expect(page.locator('#nav-drawer .session-admin-link', { hasText: name })).toBeVisible();
      }
    });
  }
});

// Short phone viewports (Safari's visible area on an iPhone SE/mini and an
// iPhone 13), where the longer drawer reaches the status row that paints
// over its foot on every screen. Lock must stay reachable (CLAUDE.md:
// status, lock and exit stay reachable on every surface): scrolled to the
// drawer's end, Lock ends above the status row.
async function lockClearOfStatusRow(page: import('@playwright/test').Page) {
  // As far as the drawer scrolls — what an operator's thumb does.
  await page.locator('#nav-drawer').evaluate((d) => { d.scrollTop = d.scrollHeight; });
  return page.locator('#nav-drawer .btn-lock').evaluate((el) => {
    const lock = el.getBoundingClientRect();
    const sb = document.querySelector('.statusbar')!.getBoundingClientRect();
    return { lockBottom: lock.bottom, sbTop: sb.top, sbHeight: sb.height };
  });
}

for (const vp of [{ width: 375, height: 553 }, { width: 390, height: 664 }]) {
  test.describe(`phone drawer, ${vp.width}x${vp.height}`, () => {
    test.use({ viewport: vp, hasTouch: true, isMobile: true });

    for (const path of ['/', '/menu', '/users']) {
      test(`${path}: Lock scrolls clear of the status row`, async ({ page }) => {
        await ensureOperator(page);
        await page.goto(path);
        await openPhoneDrawer(page);
        const g = await lockClearOfStatusRow(page);
        expect(g.sbHeight, 'the status row is showing').toBeGreaterThan(0);
        expect(g.lockBottom, 'Lock ends above the status row').toBeLessThanOrEqual(g.sbTop + 0.5);
      });
    }
  });
}

// The status row can grow while the drawer is open (the main-till chip
// polls, update chips load late): the drawer's reserved space follows it.
test.describe('phone drawer, status row grows while open', () => {
  test.use({ viewport: { width: 390, height: 664 }, hasTouch: true, isMobile: true });

  test('/: Lock stays clear after the status row grows', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    await openPhoneDrawer(page);
    await page.locator('.statusbar').evaluate((sb) => {
      const extra = document.createElement('span');
      extra.className = 'sb-item';
      extra.style.display = 'block';
      extra.style.blockSize = '60px';
      sb.appendChild(extra);
    });
    await page.waitForTimeout(200); // ResizeObserver → next frame
    const g = await lockClearOfStatusRow(page);
    expect(g.lockBottom, 'Lock ends above the grown status row').toBeLessThanOrEqual(g.sbTop + 0.5);
  });
});

test.describe('tablet rail', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('sale screen: the rail still hides the manager links', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    await expect(page.locator('.session-admin-link').first()).toBeHidden();
  });
});
