import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole } from './helpers';
import en from '../../web/locales/en.json';
import fa from '../../web/locales/fa.json';

// ut-docs#5961 (ADR-0121 S5): the Test connection button on a plugin's
// settings page, driven against the REAL compiled WASM guest
// (internal/plugins/testdata/settings_guest via e2e/seed_settings_plugin),
// which answers ui.settings.test.ask from the SAVED "mode" setting:
// ok / okmsg / bad (own message + field "provider") / slow (11 s, past the
// till's 10 s bound) / none (no answer). The ask carries no value, so the
// verdict can only come from what the save stored. Serial: one till, one
// stored state. Not run in the authoring sandbox (no Playwright install):
// run it with the rest of e2e in CI.
test.describe.configure({ mode: 'serial' });

const CORE: Record<string, Record<string, string>> = { en, fa };
// A new key reaches other languages through Weblate (ADR-0160): until then the
// page falls back to English.
const txt = (lang: string, key: string) => CORE[lang][key] ?? en[key as keyof typeof en];
const URL = '/plugins/com.example.e2e-settings/settings';

let till: WorkerTill;
let context: BrowserContext;
let page: Page;

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000); // the extra seed compiles the WASM guest
  till = await startWorkerTill(workerInfo.parallelIndex, { extraSeeds: ['./e2e/seed_settings_plugin'] });
  context = await browser.newContext({ baseURL: till.url, viewport: { width: 1024, height: 600 } });
  page = await context.newPage();
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' });
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

const btn = (p: Page) => p.locator('#ps-test-btn-conn');
const line = (p: Page) => p.locator('#ps-test-conn');
const mode = (p: Page) => p.locator('select[name="setting_mode"]');
async function noHScroll(p: Page) {
  expect(await p.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
}
async function open(p: Page, lang = 'en') {
  await p.goto(`${URL}?lang=${lang}`);
  await expect(p.locator('#ps-form')).toBeVisible();
}
// Set the plugin's mode, press Test and wait for the line to settle.
async function testWith(p: Page, m: string) {
  await mode(p).selectOption(m);
  await btn(p).click();
}

test('the button is drawn below Save, secondary, at least 48 px high, with an empty status line', async () => {
  const done = watchConsole(page);
  await open(page);
  await expect(btn(page)).toHaveText('Test connection'); // the plugin's own label
  await expect(btn(page)).toHaveClass(/secondary/);
  expect((await btn(page).boundingBox())!.height).toBeGreaterThanOrEqual(47);
  await expect(line(page)).toHaveAttribute('role', 'status');
  await expect(line(page)).toHaveText('');
  const save = await page.locator('#ps-form .btn.primary').boundingBox();
  expect((await btn(page).boundingBox())!.y).toBeGreaterThan(save!.y);
  await noHScroll(page);
  done();
});

// AC2, AC5: saves first, then asks; the verdict reflects the SAVED mode.
test('Connected after the save; an own message replaces it', async () => {
  const done = watchConsole(page);
  await open(page);
  await testWith(page, 'ok');
  await expect(page.locator('#ps-msg')).toContainText(txt('en', 'plugins.settings.saved'));
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.ok'));
  // Save really happened: a reload shows the new mode.
  await open(page);
  await expect(mode(page)).toHaveValue('ok');
  await testWith(page, 'okmsg');
  await expect(line(page)).toContainText('Sample service reachable');
  done();
});

// AC3 (UX amendment): a refused save sends no test and says so.
test('an invalid form is refused, nothing is tested, the line says so', async () => {
  const done = watchConsole(page, /Failed to load resource:.*422/); // the browser's own log of the 422
  await open(page);
  let runs = 0;
  page.on('request', (r) => { if (r.url().includes('/tests/conn/run')) runs++; });
  // The input's maxlength would stop the typing: lift it so the server's check refuses.
  await page.locator('input[name="setting_note"]').evaluate((el: HTMLInputElement) => el.removeAttribute('maxlength'));
  await page.locator('input[name="setting_note"]').fill('far too long for this field');
  await btn(page).click();
  await expect(page.locator('#ps-msg')).toContainText(txt('en', 'plugins.settings.error.summary'));
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.not_run'));
  await page.waitForTimeout(500);
  expect(runs).toBe(0);
  done();
});

// AC6: the plugin's reason, the field marked with a visible note, no focus move.
test('a failed test shows the plugin reason, marks its field and keeps focus on the button', async () => {
  const done = watchConsole(page);
  await open(page);
  await testWith(page, 'bad');
  await expect(line(page)).toContainText('The sample service rejected this value');
  const field = page.locator('select[name="setting_provider"]');
  await expect(field).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#ps-test-fe-conn')).toContainText('The sample service rejected this value');
  await expect(field).toHaveAttribute('aria-describedby', /ps-test-fe-conn/);
  expect(await page.evaluate(() => document.activeElement?.id)).toBe('ps-test-btn-conn');
  // The next test clears the mark.
  await testWith(page, 'ok');
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.ok'));
  await expect(field).not.toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#ps-test-fe-conn')).toHaveCount(0);
  done();
});

// AC9a: a plugin that gives no answer reads as "couldn't run", never as success.
test('no answer shows Couldn\'t run the test; Try again re-tests without saving again', async () => {
  const done = watchConsole(page);
  await open(page);
  await testWith(page, 'none');
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.unavailable'));
  const retry = page.locator('#ps-test-retry-conn');
  await expect(retry).toBeVisible();
  expect((await retry.boundingBox())!.height).toBeGreaterThanOrEqual(47);
  let saves = 0;
  page.on('request', (r) => { if (r.method() === 'POST' && r.url().endsWith('/api/plugins/com.example.e2e-settings/settings')) saves++; });
  await retry.click();
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.unavailable'));
  expect(saves).toBe(0);
  done();
});

// S10: a failing /run request must not leave the line on "Testing…".
test('a failed run request falls back to Couldn\'t run the test', async () => {
  const done = watchConsole(page, /Failed to load resource|^htmx:(afterRequest|sendError)/); // the aborted request, logged by the browser and by htmx
  await open(page);
  await page.route('**/tests/conn/run', (route) => route.abort());
  await testWith(page, 'ok');
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.unavailable'));
  await expect(page.locator('#ps-test-retry-conn')).toBeVisible();
  await page.unroute('**/tests/conn/run');
  done();
});

// AC7 + AC12: the till stops waiting at 10 s; what was typed meanwhile survives.
test('a slow plugin: Testing… shows, typing during the wait survives, then Couldn\'t run the test', async () => {
  test.slow();
  const done = watchConsole(page);
  await open(page);
  await testWith(page, 'slow');
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.running'));
  await page.locator('input[name="setting_note"]').fill('typed');
  await expect(line(page)).toContainText(txt('en', 'plugins.settings.test.unavailable'), { timeout: 20_000 });
  await expect(page.locator('input[name="setting_note"]')).toHaveValue('typed');
  // The slow mode itself was saved first and stays saved.
  await open(page);
  await expect(mode(page)).toHaveValue('slow');
  done();
});

// S11: RTL phone widths, no overflow, buttons wrap inside the card.
for (const vp of [{ w: 360, h: 740 }, { w: 390, h: 844 }]) {
  test(`fa at ${vp.w}x${vp.h}: mirrored, no horizontal overflow, result wraps`, async () => {
    const done = watchConsole(page);
    await page.setViewportSize({ width: vp.w, height: vp.h });
    await open(page, 'fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await testWith(page, 'bad');
    await expect(line(page)).toContainText('The sample service rejected this value');
    await noHScroll(page);
    expect((await btn(page).boundingBox())!.height).toBeGreaterThanOrEqual(47);
    await page.setViewportSize({ width: 1024, height: 600 });
    done();
  });
}
