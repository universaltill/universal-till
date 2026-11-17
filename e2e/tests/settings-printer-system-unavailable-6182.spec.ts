import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#6182: the "no system printer" warning under Settings -> Receipt
// printer -> Connection follows the select live. The e2e till is not a Pi, so
// its probe is nil and the server renders nothing; like #6154's spec, this
// injects the server's exact markup by hand and drives the page's delegated
// script. Server-side rendering is covered by Go tests.
const MARKUP =
  '<p class="notice-block-warn" id="printer-system-unavailable" role="status" ' +
  'data-testid="printer-system-unavailable" hidden>' +
  '<span class="row-warn-icon" aria-hidden="true">⚠</span>Regular printer can\'t print on this till.</p>';

async function inject(page: import('@playwright/test').Page) {
  await page.evaluate((html) => {
    document.getElementById('printer-system-unavailable')?.remove();
    const pair = document.querySelector('#settings-printer-region .field-pair');
    pair!.insertAdjacentHTML('afterend', html);
  }, MARKUP);
}

test.describe('printer system-unavailable warning (ut-docs#6182)', () => {
  test('toggles with the Connection select, survives a Save swap', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/settings#settings-printer');
    const select = page.locator('#settings-printer select[name="mode"]');
    await expect(select).toBeVisible();

    // Nil probe: nothing rendered at all.
    await expect(page.getByTestId('printer-system-unavailable')).toHaveCount(0);

    await inject(page);
    const warn = page.getByTestId('printer-system-unavailable');
    await expect(warn).toBeHidden();

    await select.selectOption('system');
    await expect(warn).toBeVisible();
    await expect(select).toHaveAttribute('aria-describedby', 'printer-system-unavailable');

    await select.selectOption('network');
    await expect(warn).toBeHidden();
    await expect(select).not.toHaveAttribute('aria-describedby', /.*/);

    // Save with network: the region is re-rendered (UT.refreshRegion); the
    // card-level listener must still work on the new select.
    await page.locator('#settings-printer input[name="address"]').first().fill('192.168.1.50:9100');
    const saved = page.waitForResponse((r) => r.url().includes('/api/settings/printer') && r.request().method() === 'POST');
    await page.locator('#settings-printer').getByRole('button', { name: 'Save printer' }).click();
    await saved;
    await expect(page.getByTestId('printer-system-unavailable')).toHaveCount(0);

    await inject(page);
    const sel2 = page.locator('#settings-printer select[name="mode"]');
    await sel2.selectOption('system');
    await expect(page.getByTestId('printer-system-unavailable')).toBeVisible();

    // Restore the default so other specs see an untouched till.
    await sel2.selectOption('off');
    const restored = page.waitForResponse((r) => r.url().includes('/api/settings/printer') && r.request().method() === 'POST');
    await page.locator('#settings-printer').getByRole('button', { name: 'Save printer' }).click();
    await restored;
    assertClean();
  });
});
