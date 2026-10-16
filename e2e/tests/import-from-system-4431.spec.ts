import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole } from './helpers';

// ut-docs#4431: the import page's "From another till system" — a till-only
// shop pulls its catalogue straight from its old system through an
// installed API-source import plugin (ADR-0161). Driven against a real
// compiled WASM importer (internal/plugins/testdata/importrows_guest,
// installed by e2e/seed_api_importer) on this file's own till: the shared
// worker till must not carry a test plugin. Covers the welcome's fourth
// option, the importer listed by name with its keys' set / not-set state,
// Check & preview (counts + sample rows, nothing saved) and Import, at the
// kiosk's 1024×600 and a 360px phone, plus RTL (fa).
test.describe.configure({ mode: 'serial' });

const PLUGIN_NAME = 'Sample Till Connector';
const SHOTS = process.env.UT_E2E_SHOTS_DIR; // optional: where to drop screenshots for a human look

let till: WorkerTill;
let context: BrowserContext;
let page: Page;

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000); // the extra seed compiles the WASM guest
  till = await startWorkerTill(workerInfo.parallelIndex, { extraSeeds: ['./e2e/seed_api_importer'] });
  context = await browser.newContext({ baseURL: till.url });
  page = await context.newPage();
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' });
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

async function noHorizontalScroll(p: Page): Promise<void> {
  const overflow = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow, 'page scrolls sideways').toBeLessThanOrEqual(0);
}

async function touchTargets(p: Page): Promise<void> {
  const buttons = p.locator('#import-system-card .import-system-actions .btn');
  const n = await buttons.count();
  expect(n).toBeGreaterThan(0);
  for (let i = 0; i < n; i++) {
    const box = await buttons.nth(i).boundingBox();
    expect(box, `button ${i} rendered`).not.toBeNull();
    expect(box!.height, `button ${i} touch height`).toBeGreaterThanOrEqual(44);
  }
}

async function shot(p: Page, name: string): Promise<void> {
  if (SHOTS) await p.locator('#import-system-card').screenshot({ path: `${SHOTS}/${name}.png` });
}

for (const vp of [
  { name: 'kiosk', width: 1024, height: 600 },
  { name: 'phone', width: 360, height: 740 },
]) {
  test(`welcome option and importer card fit at ${vp.width}px (ut-docs#4431)`, async () => {
    const done = watchConsole(page);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await page.goto('/import?welcome=1&lang=en');

    const option = page.locator('[data-welcome-option="system"]');
    await expect(option).toBeVisible();
    await option.getByRole('link').click();
    await expect(page).toHaveURL(/#import-system-card$/);

    const card = page.locator('#import-system-card');
    const entry = card.locator('[data-import-system-entry]');
    await expect(entry).toHaveCount(1);
    await expect(entry.locator('h4')).toHaveText(PLUGIN_NAME);
    await expect(entry.locator('[data-secret-key="api_key"]')).toHaveAttribute('data-secret-state', 'set');
    await expect(entry.locator('[data-secret-key="api_secret"]')).toHaveAttribute('data-secret-state', 'not-set');
    await expect(card).not.toContainText('e2e-not-a-real-key');
    await expect(entry.getByRole('link', { name: 'Connection settings' })).toHaveAttribute('href', /\/plugins\/.+\/settings$/);

    await noHorizontalScroll(page);
    await touchTargets(page);
    await shot(page, `import-system-${vp.name}-before`);
    done();
  });
}

test('Check & preview shows counts and samples and saves nothing; Import brings the products in (ut-docs#4431)', async () => {
  const done = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/import?lang=en');
  const entry = page.locator('[data-import-system-entry]');

  await entry.getByRole('button', { name: 'Check & preview' }).click();
  const result = entry.locator('.import-system-result');
  await expect(result.locator('[data-import-system-result="preview"]')).toBeVisible();
  await expect(result.locator('[data-count="new"]')).toHaveText('2');
  await expect(result.locator('[data-count="issues"]')).toHaveText('1');
  await expect(result.locator('tr[data-sample-action="issue"]')).toHaveCount(1);
  await expect(result).toContainText('E2E Flat white');
  await noHorizontalScroll(page);
  await shot(page, 'import-system-kiosk-preview');

  await result.locator('[data-import-system-commit="1"]').click();
  await expect(result.locator('[data-import-system-result="commit"]')).toBeVisible();
  await expect(result.locator('[data-count="new"]')).toHaveText('2');
  await shot(page, 'import-system-kiosk-commit');

  // Running it again is safe: nothing new, nothing duplicated.
  await entry.getByRole('button', { name: 'Check & preview' }).click();
  await expect(result.locator('[data-import-system-result="preview"]')).toBeVisible();
  await expect(result.locator('[data-count="new"]')).toHaveText('0');
  await expect(result.locator('[data-count="unchanged"]')).toHaveText('2');
  done();
});

test('phone and RTL: the preview result fits at 360px in Persian (ut-docs#4431)', async () => {
  const done = watchConsole(page);
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto('/import?lang=fa');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  const entry = page.locator('[data-import-system-entry]');
  await entry.locator('.import-system-actions button').first().click();
  await expect(entry.locator('[data-import-system-result="preview"]')).toBeVisible();
  await noHorizontalScroll(page);
  await touchTargets(page);
  await shot(page, 'import-system-phone-fa-preview');
  await page.goto('/import?lang=en');
  done();
});
