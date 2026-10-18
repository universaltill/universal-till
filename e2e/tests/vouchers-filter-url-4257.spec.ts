import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4257: the Gift vouchers search ignores case in any language
// (MÜLLER finds "Müller", which SQLite's ASCII-only LIKE missed), and the
// filter stays in the address bar, so Back from a voucher's detail page and
// a reload keep it. The Go tests prove the HX-Replace-Url header; only a
// real browser proves htmx applies it and that Back re-renders the filter.

const RUN = Date.now().toString(36).toUpperCase();

test('search is Unicode case-insensitive and survives Back and reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  const code = `T4257-${RUN}`;
  await page.goto('/vouchers?lang=en');
  await page.locator('#vouchers-import-form input[type=file]').setInputFiles({
    name: 'v.csv', mimeType: 'text/csv', buffer: Buffer.from(`code,balance,label\n${code},9.00,Müller ${RUN}\n`),
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
    page.locator('#vouchers-import-form button[type=submit]').click(),
  ]);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/vouchers/import')),
    page.locator('#vouchers-import-result button[type=submit]').click(),
  ]);
  await expect(page.locator('#vouchers-list tbody tr', { hasText: code })).toHaveCount(1);

  const historyBefore = await page.evaluate(() => history.length);
  const term = `MÜLLER ${RUN}`;
  await page.locator('#vouchers-filter input[name=q]').pressSequentially(term);
  await expect(page).toHaveURL(/\/vouchers\?q=/);
  expect(decodeURIComponent(new URL(page.url()).searchParams.get('q') ?? '')).toBe(term);
  await expect(page.locator('#vouchers-list tbody tr')).toHaveCount(1);
  await expect(page.locator('#vouchers-list tbody tr', { hasText: code })).toHaveCount(1);
  // Replaced, not pushed: typing added no history entries.
  expect(await page.evaluate(() => history.length)).toBe(historyBefore);

  await page.locator('#vouchers-list tbody tr a', { hasText: code }).click();
  await expect(page).toHaveURL(new RegExp(`/vouchers/${code}`));
  await page.goBack();
  await expect(page.locator('#vouchers-filter input[name=q]')).toHaveValue(term);
  await expect(page.locator('#vouchers-list tbody tr')).toHaveCount(1);

  await page.reload();
  await expect(page.locator('#vouchers-filter input[name=q]')).toHaveValue(term);
  await expect(page.locator('#vouchers-list tbody tr', { hasText: code })).toHaveCount(1);

  // The status select still filters without a "change" trigger (it fires
  // "input"), and lands in the address too.
  await page.locator('#vouchers-filter select[name=status]').selectOption('void');
  await expect(page).toHaveURL(/status=void/);
  await expect(page.locator('#vouchers-list tbody tr')).toHaveCount(0);
  await page.locator('#vouchers-filter select[name=status]').selectOption('active');
  await expect(page).toHaveURL(/status=active/);
  await expect(page.locator('#vouchers-list tbody tr', { hasText: code })).toHaveCount(1);
  assertClean();
});
