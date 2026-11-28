import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#6332 / ADR-0190 s: an additional ("satellite") till follows the main
// till, so the shop type is set THERE, never here. Project
// `templates-satellite` (run-till-templates.sh seeds sync.primary_url at a
// closed local port, so the main till is never contacted). EMULATED: a real
// paired satellite; the lock keys off sync.primary_url, the same test the
// product's other main-till-only controls use.

const KIOSK = { width: 1024, height: 600 };

test.beforeEach(async ({ page }) => {
  await page.setViewportSize(KIOSK);
});

test('Settings shows the lock text naming the main till, and no Change control or offer card', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.goto('/settings');
  const lock = page.locator('.shop-type-lock').first();
  await expect(lock).toContainText('Set on the main till (127.0.0.1:1). To change it, go there.');
  await expect(page.locator('#shop-type-change')).toHaveCount(0);
  await expect(page.locator('#shop-type-tidy')).toHaveCount(0);
  await expect(page.locator('[data-testid="template-offer"]')).toHaveCount(0);
  await expect(page.locator('.settings-footer-change')).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath('satellite-settings-1024x600.png'), fullPage: true });
  assertClean();
});

test('every shop-type endpoint refuses with 409 on a satellite, and the type is untouched', async ({ request }) => {
  const calls: Array<[string, string]> = [
    ['GET', '/api/settings/shop-type/pick'],
    ['GET', '/api/settings/shop-type/preview?to=retail'],
    ['POST', '/api/settings/shop-type'],
    ['POST', '/api/settings/shop-type/undo'],
    ['POST', '/api/settings/shop-type/dismiss-banner'],
    ['POST', '/api/settings/shop-type/offer/not-now'],
    ['POST', '/api/settings/shop-type/offer/never'],
  ];
  for (const [method, url] of calls) {
    const res = method === 'GET' ? await request.get(url) : await request.post(url, { form: { to: 'retail' } });
    expect(res.status(), `${method} ${url} on a satellite`).toBe(409);
  }
});

test('the Menu is untouched: no tidy-up note, no undo strip', async ({ page }) => {
  await page.goto('/menu');
  const hrefs = await page.locator('a.menu-tile').evaluateAll((els) => els.map((e) => e.getAttribute('href')));
  expect(hrefs, 'a satellite that was never tidied shows every tile').toContain('/tables');
  await expect(page.locator('[data-testid="shop-type-undo"]')).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Show all tiles' })).toHaveCount(0);
});
