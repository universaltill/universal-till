import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4251: the Gift vouchers page (/vouchers). Two things the Tester's
// driven run found that the Go handler tests cannot see:
//  1. at phone width the search/status filter row must wrap, not push the
//     page sideways (en, de-length and fa/RTL);
//  2. confirming an import in the page's own import section must refresh the
//     list above it — the operator should not have to reload to see the
//     vouchers they just imported.

const RUN = Date.now().toString(36).toUpperCase();

for (const lang of ['en', 'fa']) {
  test(`/vouchers at 360px does not scroll sideways (${lang})`, async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(`/vouchers?lang=${lang}`);
    await expect(page.locator('#vouchers-filter')).toBeVisible();
    const { scrollW, vw } = await page.evaluate(() => ({
      scrollW: document.documentElement.scrollWidth,
      vw: document.documentElement.clientWidth,
    }));
    expect(scrollW, 'page wider than the viewport').toBeLessThanOrEqual(vw + 1);
    for (const sel of ['#vouchers-filter input[name=q]', '#vouchers-filter select[name=status]']) {
      const b = (await page.locator(sel).boundingBox())!;
      expect(b.x, `${sel} starts on screen`).toBeGreaterThanOrEqual(-1);
      expect(b.x + b.width, `${sel} ends on screen`).toBeLessThanOrEqual(vw + 1);
    }
    assertClean();
  });
}

test('confirming an import refreshes the list above it', async ({ page }) => {
  const assertClean = watchConsole(page);
  const code = `T4251-${RUN}`;
  await page.goto('/vouchers?lang=en');
  await page.locator('#vouchers-import-form input[type=file]').setInputFiles({
    name: 'v.csv', mimeType: 'text/csv', buffer: Buffer.from(`code,balance,label\n${code},12.50,Spec holder\n`),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
    page.locator('#vouchers-import-form button[type=submit]').click(),
  ]);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
    page.locator('#vouchers-import-result button[type=submit]').click(),
  ]);
  await expect(page.locator('#vouchers-import-result')).toContainText('1 voucher(s) imported');
  // No reload: the list must already show it.
  await expect(page.locator('#vouchers-list tbody tr', { hasText: code })).toHaveCount(1);
  assertClean();
});
