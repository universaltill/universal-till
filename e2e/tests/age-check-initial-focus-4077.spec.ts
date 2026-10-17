import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4077: when the tender gate opens an age-restricted line's ID-check
// sheet by itself, native .show() focused the sheet's first control -- the
// "?" help link beside the prompt -- and base.html's focus trap pulled
// stray focus back to that same link. A keyboard Enter then left the sale
// for /help mid-transaction. The sheet now starts on its (non-operable)
// prompt line, and the trap pulls focus back there too: an Enter does
// nothing, and in particular never accepts the ID check. Tab still reaches
// every control, so the keyboard path is unchanged.

const RUN = Date.now().toString(36);
// One item per test: a deactivated item keeps its barcode, so a second
// test re-creating the same one would be refused.
let NAME = '';
let BARCODE = '';
let seq = 0;

async function seedRestrictedItem(page: Page) {
  seq++;
  NAME = `Age4077 ${RUN} ${seq}`;
  BARCODE = `50099900407${seq}`;
  await page.goto('/catalog');
  await page.locator('#item-form-add-btn').click();
  await page.locator('#item-barcode').fill(BARCODE);
  await page.locator('#item-name').fill(NAME);
  await page.locator('#item-price').fill('3.00');
  await page.locator('#item-age-restricted').check();
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
}

async function scanIt(page: Page) {
  await page.goto('/');
  await page.waitForSelector('.pos-container');
  await page.locator('.scan-row input[name="code"]').fill(BARCODE);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  await expect(page.locator('#basket')).toContainText(NAME);
}

const focused = (page: Page) =>
  page.evaluate(() => {
    const a = document.activeElement as HTMLElement | null;
    if (!a) return 'none';
    if (a.classList.contains('age-check-sheet-prompt')) return 'prompt';
    return `${a.tagName.toLowerCase()}${a.getAttribute('href') ? `[href=${a.getAttribute('href')}]` : ''}:${(a.textContent || '').trim().slice(0, 30)}`;
  });

test.describe('ut-docs#4077 the ID-check sheet opens on a neutral focus', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.request.post('/api/pos/reset');
    await seedRestrictedItem(page);
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset').catch(() => {});
    await page.goto('/catalog');
    const id = await page.locator('.catalog-row', { hasText: NAME }).first().getAttribute('data-id');
    if (id) await page.request.post('/api/catalog/item/deactivate', { form: { id } });
  });

  test('tender gate: Enter on the auto-opened sheet neither leaves the sale nor records a check', async ({ page }) => {
    const assertClean = watchConsole(page);
    await scanIt(page);
    await page.getByTestId('payment-open').click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
      page.locator('#payment-overlay').getByTestId('pay-default').click(),
    ]);
    const sheet = page.locator('.age-check-sheet[open]');
    await expect(sheet).toBeVisible();
    await expect.poll(() => focused(page), 'the auto-opened sheet starts on its prompt').toBe('prompt');

    let checks = 0;
    page.on('request', (r) => { if (r.url().includes('/api/pos/age-check')) checks++; });
    await page.keyboard.press('Enter');
    await page.waitForTimeout(300);
    expect(new URL(page.url()).pathname, 'Enter does not navigate to /help').toBe('/');
    expect(checks, 'Enter does not accept (or refuse) the ID check').toBe(0);
    await expect(sheet).toBeVisible();

    // Focus that strays onto the page behind comes back to the prompt,
    // not to the help link (base.html's trap, data-ut-focus-home).
    await page.evaluate(() => (document.querySelector('.scan-row input[name="code"]') as HTMLElement).focus());
    await expect.poll(() => focused(page), 'stray focus is pulled back to the prompt').toBe('prompt');

    // The keyboard path still reaches Accept, and Enter there records it.
    const sheetSel = '.age-check-sheet[open]';
    for (let i = 0; i < 6 && !(await page.evaluate((s) => {
      const a = document.activeElement as HTMLElement | null;
      return !!a && !!a.closest(s) && a.matches('button.btn.primary');
    }, sheetSel)); i++) {
      await page.keyboard.press('Tab');
    }
    expect(await page.evaluate((s) => !!document.activeElement?.closest(s) && (document.activeElement as Element).matches('button.btn.primary'), sheetSel),
      'Tab reaches Accept').toBe(true);
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/age-check')),
      page.keyboard.press('Enter'),
    ]);
    await expect(page.locator('#basket-lines .age-check-toggle.is-accepted')).toHaveCount(1);
    assertClean();
  });

  test('badge tap: the sheet opens on the prompt, not on the help link', async ({ page }) => {
    const assertClean = watchConsole(page);
    await scanIt(page);
    await page.locator('#basket-lines .age-check-toggle').first().click();
    await expect(page.locator('.age-check-sheet[open]')).toBeVisible();
    await expect.poll(() => focused(page)).toBe('prompt');
    // The prompt is not in the Tab cycle: Tab from it lands on a control.
    await page.keyboard.press('Tab');
    expect(await focused(page)).not.toBe('prompt');
    assertClean();
  });
});
