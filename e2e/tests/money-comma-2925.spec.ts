import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2925: the money fields #2819 didn't reach kept the dot-only
// pattern, so a German/Turkish keyboard's "3,50" was blocked by native
// validation (message in the device OS language). They now carry the
// comma-tolerant pattern + data-money-local; server-parsed ones read the
// value with httpx.ParseMoneyMajor, client-parsed ones with
// utCurrency.toMinor. Stored values are read back from the server.

test.describe('Money inputs: decimal comma on the remaining fields (ut-docs#2925)', () => {
  test('a promotion value of "3,50" saves as 350 minor units', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = `K2925${Date.now() % 100000}`;
    await page.goto('/promotions?lang=de');
    const form = page.locator('form[action="/api/promotions"]');
    await form.locator('input[name="code"]').fill(code);
    const value = form.locator('input[name="value_amount"]');
    await value.fill('3,50');
    expect(await value.evaluate((el: HTMLInputElement) => el.checkValidity())).toBe(true);
    await Promise.all([
      page.waitForURL(/\/promotions(\?lang=de)?$|\/promotions$/),
      form.locator('button[type="submit"]').click(),
    ]);
    // The inline edit row prefills FormatMajorPlain(stored minor) in the
    // session's German decimal comma (ut-docs#2818): "3,50" only if the
    // server stored 350.
    const html = await (await page.request.get('/promotions')).text();
    const row = html.slice(html.indexOf(`action="/api/promotions/${code}/edit"`));
    expect(row).toMatch(/name="value_amount"[^>]*value="3,50"/);
    assertClean();
  });

  test('"3,50" is valid and converts to 350 on the client-parsed fields', async ({ page }) => {
    const assertClean = watchConsole(page);
    const cases: Array<{ url: string; field: string; minor: string; value: string; want: string }> = [
      { url: '/shifts', field: '#opening-cash', minor: '#opening-cash-minor', value: '3,50', want: '350' },
      { url: '/menu', field: '#pfand-amount', minor: '#pfand-amount-minor', value: '3,50', want: '350' },
    ];
    for (const c of cases) {
      await page.goto(`${c.url}?lang=de`);
      const got = await page.locator(c.field).evaluate((el: HTMLInputElement, v: string) => {
        el.value = v;
        el.dispatchEvent(new Event('input', { bubbles: true }));
        return el.checkValidity();
      }, c.value);
      expect(got, `${c.url} ${c.field} validity`).toBe(true);
      await expect(page.locator(c.minor), `${c.url} ${c.minor}`).toHaveValue(c.want);
    }

    // The tender / voucher amounts are read by app.js via toMinor on
    // submit; the pattern is what used to refuse the comma.
    await page.goto('/?lang=de');
    const tender = await page.evaluate(() =>
      Array.from(document.querySelectorAll<HTMLInputElement>(
        '#split-tender-form input[name="amount"], #split-tender-form input[name="change"], #split-tender-issue-form input[name="amount"]',
      )).map((el) => { el.value = '3,50'; return el.checkValidity() && el.hasAttribute('data-money-local'); }),
    );
    expect(tender.length).toBeGreaterThanOrEqual(3);
    expect(tender.every(Boolean)).toBe(true);
    assertClean();
  });

  test('the stock cost accepts "3,50" and a malformed cost stays blank', async ({ page }) => {
    await page.goto('/inventory?lang=de');
    await page.locator('#stock-dialog-open').click();
    await expect(page.locator('#stock-form')).toBeVisible();
    const submit = () => page.locator('#stock-form').evaluate((form: HTMLFormElement) =>
      form.dispatchEvent(new Event('submit', { cancelable: true })));

    await page.locator('#stock-cost').fill('3,50');
    expect(await page.locator('#stock-cost').evaluate((el: HTMLInputElement) => el.checkValidity())).toBe(true);
    await submit();
    await expect(page.locator('#stock-cost-minor')).toHaveValue('350');

    // Number("1e3") is 1000; the old gate let it through as 100000.
    await page.locator('#stock-cost').fill('1e3');
    await submit();
    await expect(page.locator('#stock-cost-minor')).toHaveValue('');
  });

  test('a shift opens, adjusts (signed) and closes with comma amounts', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/shifts?lang=de');
    if (await page.locator('#close-shift-form').count()) {
      await page.evaluate(async () => {
        const id = (document.querySelector('#close-shift-form input[name="shift_id"]') as HTMLInputElement).value;
        await fetch('/api/shifts/close', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: new URLSearchParams({ shift_id: id, closing_cash: '0' }),
        });
      });
      await page.goto('/shifts?lang=de');
    }
    await page.locator('#opening-cash').fill('3,50');
    await expect(page.locator('#opening-cash-minor')).toHaveValue('350');
    const [open] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/open')),
      page.locator('#open-shift-form button[type=submit]').click(),
    ]);
    expect(open.status()).toBe(200);
    await expect(page.locator('#close-shift-form')).toBeVisible();

    await page.locator('#adjustment-form').evaluate((f: HTMLElement) => {
      const d = f.closest('details'); if (d) (d as HTMLDetailsElement).open = true;
    });
    await page.locator('#adjustment-form select[name="type"]').selectOption('adjustment');
    await page.locator('#adjust-pounds').fill('-3,50');
    await expect(page.locator('#adjust-minor')).toHaveValue('-350');
    await page.locator('#adjustment-form input[name="reason"]').fill('till count correction');
    const [adj] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/adjustment')),
      page.locator('#adjustment-form button[type=submit]').click(),
    ]);
    expect(adj.status(), 'a "-3,50" adjustment must pass native validation and the server').toBe(200);

    await page.locator('#closing-cash').fill('3,50');
    await expect(page.locator('#closing-cash-minor')).toHaveValue('350');
    await page.locator('#skim-pounds').fill('1,00');
    await expect(page.locator('#skim-minor')).toHaveValue('100');
    const [close] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/shifts/close')),
      page.locator('#close-shift-form button[type=submit]').click(),
    ]);
    expect(close.status()).toBe(200);
    assertClean();
  });

  test('a tip of "7,50" is submitted as 750', async ({ page }) => {
    await page.goto('/reports?lang=de');
    await page.locator('#report-tab-tips').click();
    await expect(page.locator('#tips-amount')).toBeVisible();
    await page.locator('select[name="cashier_id"]').selectOption({ index: 1 });
    await page.locator('#tips-amount').fill('7,50');
    await expect(page.locator('#tips-amount-minor')).toHaveValue('750');
    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/reports/worker-allocations')),
      page.locator('form[hx-post="/api/reports/worker-allocations"] button[type=submit]').click(),
    ]);
    expect(resp.status()).toBe(200);
  });
});
