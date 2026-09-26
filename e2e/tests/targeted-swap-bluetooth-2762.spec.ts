import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2762 slice 2: Pair and Forget on /bluetooth-devices used to call
// window.location.reload(); they now re-render only the paired-devices card
// (#bt-paired, UT.refreshRegion). The e2e runner has no bluetoothd, so the
// JSON endpoints are stubbed with page.route — what is under test is the
// page script's handling of a success, not BlueZ. The refresh itself is a
// real GET of the page. A marker on `window` proves no reload happened.
test('bluetooth: pair and forget refresh the paired card in place, no page reload', async ({ page }) => {
  const assertClean = watchConsole(page);
  const ok = { status: 200, contentType: 'application/json', body: JSON.stringify({ data: { ok: true }, error: null }) };
  await page.route('**/api/bluetooth-devices/scan', (r) => r.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ data: { devices: [{ address: 'AA:BB:CC:DD:EE:01', name: 'Probe Scanner 2762', icon: 'input-keyboard' }] }, error: null }),
  }));
  await page.route('**/api/bluetooth-devices/pair', (r) => r.fulfill(ok));
  await page.route('**/api/bluetooth-devices/forget', (r) => r.fulfill(ok));

  await page.goto('/bluetooth-devices');
  // Without Bluetooth on the host the scan button renders disabled; the
  // click path is what is tested here, so enable it.
  await page.locator('#bt-scan-btn').evaluate((b: HTMLButtonElement) => { b.disabled = false; });
  await page.evaluate(() => { (window as any).__ut2762 = 'still-here'; });

  // Pair: the candidate leaves the scan list, the confirmation stays, and
  // the paired card is re-rendered from the server.
  await page.locator('#bt-scan-btn').click();
  const candidate = page.locator('#bt-scan-results li', { hasText: 'Probe Scanner 2762' });
  await expect(candidate).toHaveCount(1);
  await page.locator('#bt-paired h3').evaluate((h) => { h.setAttribute('data-stale', '1'); });
  await candidate.getByRole('button').click();
  await expect(candidate).toHaveCount(0);
  await expect(page.locator('#bt-scan-msg')).not.toBeEmpty();
  await expect(page.locator('#bt-paired h3[data-stale]')).toHaveCount(0); // card was replaced
  expect(await page.evaluate(() => (window as any).__ut2762), 'page was reloaded').toBe('still-here');

  // Forget: a paired row (planted, since the host has none) — the delegated
  // listener must handle a button that arrived after page load, and the
  // confirmation must survive the card's refresh.
  await page.locator('#bt-paired').evaluate((card) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'btn secondary bt-forget-btn';
    b.setAttribute('data-address', 'AA:BB:CC:DD:EE:01');
    b.textContent = 'Forget probe';
    card.insertBefore(b, card.querySelector('#bt-paired-msg'));
  });
  await page.getByRole('button', { name: 'Forget probe' }).click();
  await expect(page.getByRole('button', { name: 'Forget probe' })).toHaveCount(0);
  await expect(page.locator('#bt-paired-msg')).not.toBeEmpty();
  expect(await page.evaluate(() => (window as any).__ut2762), 'page was reloaded').toBe('still-here');
  assertClean();
});
