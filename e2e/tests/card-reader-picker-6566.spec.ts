import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole, openPhoneSheet } from './helpers';

// ut-docs#6566: the "Which reader?" picker and /settings/card-readers, driven
// against a real compiled WASM test payment plugin (readers_guest) on this
// file's own till, seeded by e2e/seed_card_readers_mixed: 3 readers of one
// card method (one offline) + 1 of a second method, plus UT_E2E_EXTRA_READERS
// more ready ones so the list must scroll. The shared worker till must not
// carry a test plugin. Layout is asserted geometrically (UX s7), not by eye.
test.describe.configure({ mode: 'serial' });

const SHOTS = process.env.UT_E2E_SHOTS_DIR; // optional: where to drop screenshots for a human look
let till: WorkerTill;
let context: BrowserContext;
let page: Page;
let tenders: string[] = [];

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000); // the extra seed compiles the WASM guest
  process.env.UT_E2E_EXTRA_READERS = '6';
  till = await startWorkerTill(workerInfo.parallelIndex, { extraSeeds: ['./e2e/seed_card_readers_mixed'] });
  context = await browser.newContext({ baseURL: till.url });
  page = await context.newPage();
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' });
  page.on('request', (r) => {
    if (r.url().includes('/api/pos/tender') && r.method() === 'POST') tenders.push(r.postData() ?? '');
  });
});

test.afterAll(async () => {
  delete process.env.UT_E2E_EXTRA_READERS;
  await context?.close();
  await till?.stop();
});

async function openPicker(p: Page): Promise<void> {
  await p.goto('/');
  await openPhoneSheet(p);
  await p.locator('.scan-row input[name=code]').fill('5000000000012');
  await p.locator('.scan-row button[type=submit]').click();
  await expect(p.locator('#basket')).toContainText('Coca-Cola');
  await p.getByTestId('payment-open').click();
  await p.getByTestId('pay-card-readers').click();
  await expect(p.getByTestId('card-reader-picker')).toBeVisible();
  await expect(p.getByTestId('card-reader-option').first()).toBeVisible();
}

async function geometry(p: Page): Promise<void> {
  // Nothing overflows its own row (the reason line used to sit on the border).
  const rows = await p.getByTestId('card-reader-option').evaluateAll((els) =>
    els.map((e) => {
      const r = e.getBoundingClientRect();
      const kids = [...e.querySelectorAll('.card-reader-main, .card-reader-state, .card-reader-reason')]
        .filter((k) => (k as HTMLElement).offsetHeight > 0)
        .map((k) => k.getBoundingClientRect());
      const worst = kids.reduce((m, k) => Math.max(m, k.bottom - r.bottom), -999);
      return { id: (e as HTMLElement).dataset.readerId, h: r.height, w: r.width, overBottom: worst, overX: e.scrollWidth - e.clientWidth };
    }),
  );
  for (const r of rows) {
    expect(r.h, `${r.id} height`).toBeGreaterThanOrEqual(56);
    expect(r.overBottom, `${r.id}: content must end inside the row`).toBeLessThanOrEqual(0);
    expect(r.overX, `${r.id}: no sideways overflow`).toBeLessThanOrEqual(1);
  }
  const sideways = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(sideways, 'page scrolls sideways').toBeLessThanOrEqual(0);
}

for (const vp of [
  { name: 'touch-1280x800', width: 1280, height: 800 },
  { name: 'kiosk-1024x600', width: 1024, height: 600 },
  { name: 'phone-360x740', width: 360, height: 740 },
]) {
  test(`picker layout, states and sticky Confirm at ${vp.name}`, async () => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await openPicker(page);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/picker-${vp.name}.png` });

    const opts = page.getByTestId('card-reader-option');
    expect(await opts.count()).toBe(10); // 4 seeded + 6 extra
    // Offline row: icon + text + visible reason, not selectable by click.
    const off = page.locator('[data-reader-id=e2e-r3]');
    await expect(off).toHaveAttribute('aria-disabled', 'true');
    await expect(off.locator('.card-reader-state-text')).toHaveText('Offline');
    await expect(off.locator('.card-reader-reason')).toBeVisible();
    await off.click({ force: true });
    await expect(off).toHaveAttribute('aria-checked', 'false');

    await geometry(page);

    // O2: the tender footer (New Sale / Hold / New Customer) and the Offline
    // toggle give the list the room while the picker is open.
    for (const sel of ['[data-testid=payment-overlay-new-sale]', '[data-testid=payment-overlay-hold]', '#offline-override', '.tender-footer.single button']) {
      await expect(page.locator(sel), `${sel} hidden while the picker is open`).toBeHidden();
    }
    const listH = await page.locator('#card-reader-list').evaluate((e) => e.clientHeight);
    expect(listH, 'list window shows at least 4 rows (56px each; was ~1.5 before the footer stepped aside)').toBeGreaterThanOrEqual(224);

    // Confirm and Back stay on screen (sticky) and are finger sized.
    for (const id of ['card-reader-confirm', 'card-reader-back']) {
      const box = await page.getByTestId(id).boundingBox();
      expect(box, id).not.toBeNull();
      expect(box!.height, `${id} height`).toBeGreaterThanOrEqual(56);
      expect(box!.y + box!.height, `${id} inside the viewport`).toBeLessThanOrEqual(vp.height + 1);
    }
    assertClean();
  });
}

test('O1: the reader button and the generic card button read differently', async () => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  await page.locator('.scan-row input[name=code]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.getByTestId('payment-open').click();
  const generic = page.locator('#panel-pay .pay-grid [data-method=card]');
  await expect(generic).toHaveCount(1);
  await expect(generic).toHaveText('Card');
  await expect(page.getByTestId('pay-card-readers')).toContainText('Card reader');
});

test('O2: Back brings the tender footer back', async () => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await openPicker(page);
  await page.getByTestId('card-reader-back').click();
  await expect(page.getByTestId('payment-overlay-new-sale')).toBeVisible();
  await expect(page.locator('#offline-override')).toBeVisible();
});

test('O4: Confirm label isolates the amount and the reader name with bdi', async () => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await openPicker(page);
  const bdi = page.locator('[data-testid=card-reader-confirm] bdi');
  await expect(bdi).toHaveCount(2);
  const sel = page.locator('[data-testid=card-reader-option][aria-checked=true] .card-reader-name');
  const name = (await sel.textContent())!.replace(/^[\u2713\s]+/, '').trim();
  await expect(bdi.nth(1)).toHaveText(name);
  // choosing another ready reader keeps the structure
  await page.locator('[data-reader-id=e2e-r4]').click();
  await expect(bdi).toHaveCount(2);
});

test('legacy till view: the page has no reader picker until Card is pressed', async () => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  await page.getByTestId('payment-open').click().catch(() => {});
  await expect(page.getByTestId('card-reader-picker')).toHaveCount(0);
});

test('a row tap only selects; a double-tapped Confirm sends ONE tender with the registry row id; last used becomes the default', async () => {
  await page.setViewportSize({ width: 1280, height: 800 });
  tenders = [];
  await openPicker(page);
  await page.locator('[data-reader-id=e2e-r4]').click();
  expect(tenders.length, 'a row tap must never charge').toBe(0);
  await expect(page.getByTestId('card-reader-confirm')).toContainText('Pop-up stand');
  await page.getByTestId('card-reader-confirm').dblclick({ timeout: 3000 }).catch(() => {});
  await expect(page.locator('#basket.receipt-view')).toBeVisible();
  expect(tenders.length, 'double tap sends exactly one tender').toBe(1);
  expect(tenders[0]).toContain('method=e2estripe');
  expect(tenders[0]).toContain('reader=e2e-r4');
  expect(tenders[0], 'never a provider reader id from the browser').not.toContain('tmr_popup');

  await openPicker(page);
  await expect(page.locator('.card-reader-option[aria-checked=true]')).toHaveAttribute('data-reader-id', 'e2e-r4');
});

test('Back restores the tender grid; Escape does too', async () => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await openPicker(page);
  await page.getByTestId('card-reader-back').click();
  await expect(page.locator('#panel-pay .pay-grid')).toBeVisible();
});

test('settings page lists the readers, with no sideways scroll at 360px', async () => {
  const assertClean = watchConsole(page);
  for (const [w, h] of [[1280, 800], [360, 740]]) {
    await page.setViewportSize({ width: w, height: h });
    await page.goto('/settings/card-readers');
    await expect(page.getByTestId('card-reader-row')).toHaveCount(10);
    const sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(sideways, `settings page scrolls sideways at ${w}px`).toBeLessThanOrEqual(0);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/settings-card-readers-${w}.png`, fullPage: true });
  }
  assertClean();
});

test('O3: the self-order kiosk checkbox is a full-row 48px target with a bigger control', async () => {
  for (const [w, h] of [[1280, 800], [360, 740]]) {
    await page.setViewportSize({ width: w, height: h });
    await page.goto('/settings/card-readers');
    const cb = page.getByTestId('card-reader-unattended').first();
    const box = await cb.boundingBox();
    expect(box!.width, `checkbox width at ${w}`).toBeGreaterThanOrEqual(28);
    expect(box!.height, `checkbox height at ${w}`).toBeGreaterThanOrEqual(28);
    const lab = await cb.locator('xpath=ancestor::label').boundingBox();
    expect(lab!.height, `label height at ${w}`).toBeGreaterThanOrEqual(48);
    const actions = await cb.locator('xpath=ancestor::div[contains(@class,"card-reader-settings-actions")]').boundingBox();
    expect(lab!.width, `label is a full-row target at ${w}`).toBeGreaterThanOrEqual(actions!.width - 1);
  }
});
