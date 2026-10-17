import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole } from './helpers';

// ut-docs#4526 (ADR-0164 §2): a file-rows import plugin reads its system's
// export file. On the import page, a file the plugin claims (its extension
// and header row) gets the plugin's preview instead of the standard one —
// nothing saved — with a line naming the plugin and a way back to the
// standard importer; Import commits the copy staged at preview. Driven
// against a real compiled WASM importer
// (internal/plugins/testdata/filerows_guest, installed by
// e2e/seed_file_importer) on this file's own till, at the kiosk's 1024×600
// and a 360px phone in Persian (RTL).
test.describe.configure({ mode: 'serial' });

const PLUGIN_NAME = 'Sample Export Reader';
const SHOTS = process.env.UT_E2E_SHOTS_DIR; // optional: screenshots for a human look
const EXPORT = 'fixture_ref,fixture_title,fixture_price_minor\ne2e-r1,E2E Oat latte,340\ne2e-r2,E2E Flapjack,220\n';

let till: WorkerTill;
let context: BrowserContext;
let page: Page;

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000); // the extra seed compiles the WASM guest
  till = await startWorkerTill(workerInfo.parallelIndex, { extraSeeds: ['./e2e/seed_file_importer'] });
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
  const buttons = p.locator('#import-result .import-system-actions .btn');
  const n = await buttons.count();
  expect(n).toBeGreaterThan(0);
  for (let i = 0; i < n; i++) {
    const box = await buttons.nth(i).boundingBox();
    expect(box, `button ${i} rendered`).not.toBeNull();
    expect(box!.height, `button ${i} touch height`).toBeGreaterThanOrEqual(44);
  }
}

async function chooseAndPreview(p: Page, name: string, content: string): Promise<void> {
  await p.locator('#import-form input[type=file]').setInputFiles({ name, mimeType: 'text/csv', buffer: Buffer.from(content) });
  await p.locator('#import-form button[data-action="set-value:import-commit,0"]').click();
}

async function shot(p: Page, name: string): Promise<void> {
  if (SHOTS) await p.locator('#import-result').screenshot({ path: `${SHOTS}/${name}.png` });
}

test('a claimed export previews through the plugin, saves nothing, and Import brings the products in (ut-docs#4526)', async () => {
  const done = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/import?lang=en');
  await chooseAndPreview(page, 'export.csv', EXPORT);

  const result = page.locator(`#import-result [data-import-file-plugin]`);
  await expect(result.locator('[data-import-system-result="preview"]')).toBeVisible();
  await expect(result).toContainText(PLUGIN_NAME);
  await expect(result.locator('[data-count="new"]')).toHaveText('2');
  await expect(result).toContainText('E2E Oat latte');
  await expect(result.locator('[data-import-use-standard="1"]')).toBeVisible();
  await noHorizontalScroll(page);
  await touchTargets(page);
  await shot(page, 'import-file-plugin-kiosk-preview');

  await result.locator('[data-import-system-commit="1"]').click();
  await expect(result.locator('[data-import-system-result="commit"]')).toBeVisible();
  await expect(result.locator('[data-count="new"]')).toHaveText('2');
  await shot(page, 'import-file-plugin-kiosk-commit');

  // The same file again: nothing new, nothing duplicated.
  await chooseAndPreview(page, 'export.csv', EXPORT);
  await expect(page.locator('#import-result [data-import-system-result="preview"]')).toBeVisible();
  await expect(page.locator('#import-result [data-count="new"]')).toHaveText('0');
  await expect(page.locator('#import-result [data-count="unchanged"]')).toHaveText('2');
  done();
});

test('"Use the standard importer instead" reads the same file with the till\'s own importer (ut-docs#4526)', async () => {
  const done = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/import?lang=en');
  // Both importers can read this one: the plugin's columns plus name/price.
  await chooseAndPreview(page, 'export.csv', 'fixture_ref,fixture_title,fixture_price_minor,name,price\ne2e-r9,E2E Tea,200,E2E Tea,2.00\n');
  await expect(page.locator('#import-result [data-import-file-plugin]')).toBeVisible();
  await page.locator('#import-result [data-import-use-standard="1"]').click();
  await expect(page.locator('#import-result [data-import-file-plugin]')).toHaveCount(0);
  await expect(page.locator('#import-result')).toContainText('E2E Tea');
  done();
});

test('phone and RTL: the plugin preview fits at 360px in Persian (ut-docs#4526)', async () => {
  const done = watchConsole(page);
  await page.setViewportSize({ width: 360, height: 740 });
  await page.goto('/import?lang=fa');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await chooseAndPreview(page, 'export.csv', EXPORT);
  await expect(page.locator('#import-result [data-import-system-result="preview"]')).toBeVisible();
  await noHorizontalScroll(page);
  await touchTargets(page);
  await shot(page, 'import-file-plugin-phone-fa-preview');
  await page.goto('/import?lang=en');
  done();
});
