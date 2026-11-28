import { test, expect, type Page, type APIRequestContext } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#6348 / ut-docs#6332 / ADR-0190: does the till still WORK in every
// shop type? The matrix spec proves what each template hides; this one walks
// the daily flows on a till set to each of the six types, because a template
// that re-lays-out the Menu must never break a sale, a refund, a shift, a
// backup, a print or a report (hiding removes a tile, never a route).
//
// Runs in the `templates` project (own till, UT_AUTH=off, so the operator is a
// manager; run-till-templates.sh). Named to sort AFTER template-matrix-6332
// because that spec starts from the till's legacy (no template) state.
//
// REAL: the server, its DB, the real cash/card tender, refund, shift, backup,
// print-test and report endpoints. EMULATED: there is no card terminal plugin
// on a throwaway till, and `payment.<method>.authorize` with no subscriber is
// no gate (pos_api.go), so the `card` leg records exactly as a test/demo
// provider leg would; the printer is off, so the print-test path is the real
// "printer is off" 502, not paper. Cashier-per-type lives in the `auth`
// project (template-cashier-6332.spec.ts): this till has no sessions.

const TYPES = ['cafe', 'hospitality', 'retail', 'service', 'market_stall', 'other'] as const;
const COLA = '5000000000012'; // demo catalogue itm001, Coca-Cola 330ml

async function setType(request: APIRequestContext, to: string) {
  const res = await request.post('/api/settings/shop-type', { form: { to } });
  expect(res.ok(), `set shop type ${to}: ${res.status()}`).toBe(true);
}

async function ringCola(page: Page) {
  await page.request.post('/api/pos/reset');
  await page.goto('/');
  await page.getByRole('textbox').first().fill(COLA);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
}

async function openShift(page: Page): Promise<string> {
  await page.goto('/shifts');
  if ((await page.locator('#close-shift-form').count()) === 0) {
    await page.locator('#opening-cash').fill('10.00');
    await page.locator('#open-shift-form button[type=submit]').click();
    await expect(page.locator('#close-shift-form')).toBeVisible();
  }
  return (await page.locator('#close-shift-form input[name="shift_id"]').getAttribute('value'))!;
}

for (const type of TYPES) {
  test(`${type}: cash sale, card sale, refund, shift open/close, backup, test print, reports`, async ({ page, request }) => {
    test.setTimeout(120_000);
    await page.setViewportSize({ width: 1024, height: 600 });
    // A hidden tile is still a reachable route; a failed XHR here is a real defect.
    const assertClean = watchConsole(page, /^Failed to load resource:.*(502|ERR_FAILED)/);
    await setType(request, type);

    // --- Shift open (so the sales below land in it)
    const shiftId = await openShift(page);
    expect(shiftId, `${type}: a shift is open`).toBeTruthy();

    // --- Cash sale through the real sale screen
    await ringCola(page);
    await page.getByTestId('payment-open').click();
    await page.locator('.pay-btn', { hasText: 'Cash' }).first().click();
    await expect(page.locator('#basket.receipt-view'), `${type}: cash sale completes`).toBeVisible();

    // --- Refund that sale through the real refund screen (default = everything, as cash)
    await page.locator('button[data-action*="go:/refund/"]').click();
    await expect(page.locator('#refund-form')).toBeVisible();
    await page.locator('#refund-form button[type=submit]').click();
    await page.waitForURL(/\/journal\//);
    await expect(page.locator('main')).toContainText(/refund/i);

    // --- Card sale: a 0.50 card leg (the test provider leg) + cash for the rest
    await page.request.post('/api/pos/reset');
    expect((await page.request.post('/api/pos/scan', { form: { code: COLA, qty: '1' } })).ok()).toBe(true);
    const tender = await page.request.post('/api/pos/tender', {
      headers: { 'Content-Type': 'application/json' },
      data: { payments: [{ method: 'card', amount: 50 }, { method: 'cash', amount: 100000 }] },
    });
    expect(tender.ok(), `${type}: card+cash tender: ${tender.status()} ${await tender.text()}`).toBe(true);
    await page.goto('/journal');
    const href = await page.locator('a[href^="/journal/"]').first().getAttribute('href');
    await page.goto(href!);
    await expect(page.locator('main'), `${type}: the journal shows the card leg`).toContainText(/card/i);

    // --- Shift close
    const close = await page.request.post('/api/shifts/close', { form: { shift_id: shiftId, closing_cash: '0' } });
    expect(close.ok(), `${type}: shift close: ${close.status()} ${await close.text()}`).toBe(true);
    await page.goto('/shifts');
    await expect(page.locator('#open-shift-form'), `${type}: shift is closed`).toBeVisible();

    // --- Backup now
    const backup = await request.post('/api/backup/now');
    expect(backup.ok(), `${type}: backup now: ${backup.status()}`).toBe(true);
    expect(await backup.text()).toContain('✓');

    // --- Test print: no printer on a throwaway till, so the real answer is the
    // inline "printer is off" 502 (reachable, not 403/404), never a crash.
    const print = await request.post('/api/print/test');
    expect(print.status(), `${type}: print test answers inline`).toBe(502);
    expect(await print.text()).toContain('✗');

    // --- Reports as the manager: the page and the day-so-far X report
    await page.goto('/reports');
    await page.locator('#report-tab-eod').click();
    await page.locator('#eod-day-x-show').click();
    await expect(page.locator('#eod-day-x-body pre.paper-preview')).toContainText('X REPORT - DAY SO FAR');

    // --- The type is still what we set (no flow reset it)
    await page.goto('/settings');
    await expect(page.locator('[data-testid="settings-footer"]')).toContainText('Set up for:');
    assertClean();
  });
}
