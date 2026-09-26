import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2954: #2925 made every money input comma-tolerant, but the
// percent inputs (promotion percent, payment-fee percent, tax rates) kept a
// hand-typed dot-only pattern, so a German/Turkish keyboard's "1,5" was
// blocked by native validation in the device OS language. They now carry
// {{ percentpatternlocal }} (comma-tolerant + data-money-local) and the
// server reads them with httpx.ParsePercentBP. Stored values are read back
// from the server.

test.describe('Percent inputs: decimal comma (ut-docs#2954)', () => {
  test('a promotion percent of "1,5" saves as 150 bp', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = `P2954${Date.now() % 100000}`;
    await page.goto('/promotions?lang=de');
    const form = page.locator('form[action="/api/promotions"]');
    await form.locator('input[name="code"]').fill(code);
    await form.locator('select[name="type"]').selectOption('percent');
    const value = form.locator('input[name="value_percent"]');
    await value.fill('1,5');
    expect(await value.evaluate((el: HTMLInputElement) => el.checkValidity())).toBe(true);
    await Promise.all([
      page.waitForURL(/\/promotions(\?lang=de)?$/),
      form.locator('button[type="submit"]').click(),
    ]);
    // The inline edit row prefills "%.2f" of the stored bp/100: "1.50"
    // only if the server stored 150.
    const html = await (await page.request.get('/promotions')).text();
    const row = html.slice(html.indexOf(`action="/api/promotions/${code}/edit"`));
    expect(row).toMatch(/name="value_percent"[^>]*value="1\.50"/);
    assertClean();
  });

  test('"1,5" is valid and "1e3" gets the shop-language message on every percent field', async ({ page }) => {
    const cases: Array<{ url: string; sel: string }> = [
      { url: '/promotions', sel: 'input[name="value_percent"]' },
      { url: '/settings', sel: '.fee-row input[name="percent"]' },
      { url: '/catalog/tax-codes', sel: '#tax-code-rate' },
      { url: '/catalog/tax-codes', sel: '#tax-code-takeaway-rate' },
      { url: '/country-settings', sel: 'input[name="tax_rate_pct"]' },
    ];
    for (const c of cases) {
      await page.goto(`${c.url}?lang=de`);
      const expected = await page.evaluate(() => document.body.dataset.percentInvalid || '');
      expect(expected, 'body carries the shop-language message').not.toBe('');
      const got = await page.locator(c.sel).first().evaluate((el: HTMLInputElement) => {
        el.value = '1,5';
        el.dispatchEvent(new Event('input', { bubbles: true }));
        const comma = el.checkValidity();
        el.value = '1e3';
        el.dispatchEvent(new Event('input', { bubbles: true }));
        return { comma, bad: el.checkValidity(), msg: el.validationMessage, local: el.hasAttribute('data-money-local') };
      });
      expect(got.comma, `${c.url} ${c.sel}: "1,5" valid`).toBe(true);
      expect(got.bad, `${c.url} ${c.sel}: "1e3" refused`).toBe(false);
      expect(got.local, `${c.url} ${c.sel}: data-money-local`).toBe(true);
      expect(got.msg, `${c.url} ${c.sel}: shop-language message`).toBe(expected);
    }
  });

  test('a payment-fee percent of "1,5" saves as 150 bp', async ({ page }) => {
    await page.goto('/settings?lang=de#settings-payments');
    const row = page.locator('form.fee-row').filter({ has: page.locator('input[name="method"][value="card"]') });
    await row.locator('input[name="percent"]').fill('1,5');
    await row.locator('input[name="fixed"]').fill('0,10');
    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/payments-fee')),
      row.locator('button[type="submit"]').click(),
    ]);
    expect(resp.status()).toBe(200);
    await expect(row.locator('.fee-msg')).toContainText('✓');
    const html = await (await page.request.get('/settings')).text();
    const card = html.slice(html.indexOf('name="method" value="card"'));
    expect(card).toMatch(/name="percent"[^>]*value="1\.50"/);
  });

  test('a tax code rate of "7,5" / takeaway "5,5" saves as 750 / 550 bp', async ({ page }) => {
    const name = `Komma ${Date.now() % 100000}`;
    await page.goto('/catalog/tax-codes?lang=de');
    await page.locator('#tax-code-name').fill(name);
    await page.locator('#tax-code-rate').fill('7,5');
    await page.locator('#tax-code-takeaway-rate').fill('5,5');
    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/tax-codes')),
      page.locator('#tax-code-form button[type="submit"]').click(),
    ]);
    expect(resp.status()).toBe(200);
    const row = page.locator('.tax-code-row').filter({ hasText: name });
    await expect(row).toHaveAttribute('data-rate', '7.5');
    await expect(row).toHaveAttribute('data-takeaway', '5.5');
  });
});
