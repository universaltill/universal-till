import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole } from './helpers';
import en from '../../web/locales/en.json';
import fa from '../../web/locales/fa.json';

// ut-docs#4160 (ADR-0121 S5): a plugin settings dropdown whose list the
// plugin supplies (`options_ask`), driven end to end against a REAL plugin.
// Its own till with a real compiled WASM guest
// (internal/plugins/testdata/settings_guest, installed by
// e2e/seed_settings_plugin) that answers ui.settings.options.ask from the
// unsaved "provider": a -> two options (one labelled with markup), b -> one,
// c -> 33 (over the cap, so the whole answer is refused and the operator sees
// the failure note). The stored model, "retired-model", is in no answer.
// Core strings come from web/locales/*.json; the option labels are the
// plugin's own data. Serial: the tests share one till and one stored state.
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

const model = (p: Page) => p.locator('#ps-f-model');
const msg = (p: Page) => p.locator('#ps-opts-msg-model');
async function noHScroll(p: Page) {
  expect(await p.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
}
async function open(p: Page, lang = 'en') {
  await p.goto(`${URL}?lang=${lang}`);
  await expect(p.locator('#ps-form')).toBeVisible();
  // The message line exists before any text does (a live region).
  await expect(msg(p)).toHaveAttribute('role', 'status');
}

test('the list fills in after the page, keeps the stored choice and escapes labels', async () => {
  const done = watchConsole(page);
  await open(page);
  await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(1);
  // The stored value no answer lists stays first and selected, with the hint.
  await expect(model(page)).toHaveValue('retired-model');
  await expect(msg(page)).toContainText(CORE.en['plugins.settings.options_saved_missing']);
  // Markup in a label is text, never an element.
  await expect(model(page).locator('option[value="alpha-big"]')).toHaveText('<b>Big</b> alpha');
  await expect(page.locator('#ps-form b')).toHaveCount(0);
  await expect(model(page)).not.toHaveAttribute('aria-busy', 'true');
  done();
});

test('changing the provider refreshes the list without saving', async () => {
  const done = watchConsole(page);
  await open(page);
  await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(1);
  await page.locator('select[name="setting_provider"]').selectOption('b');
  await expect(model(page).locator('option[value="beta-only"]')).toHaveCount(1);
  await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(0);
  // Nothing was saved: a reload shows the stored provider and its list again.
  await open(page);
  await expect(page.locator('select[name="setting_provider"]')).toHaveValue('a');
  await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(1);
  done();
});

test('a refused answer shows the note and Try again, keeps the stored choice, and Save still works', async () => {
  const done = watchConsole(page);
  await open(page);
  await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(1);
  await page.locator('select[name="setting_provider"]').selectOption('c');
  await expect(msg(page)).toContainText(CORE.en['plugins.settings.options_failed']);
  const retry = msg(page).locator('[data-ps-ask-retry="model"]');
  await expect(retry).toBeVisible();
  expect((await retry.boundingBox())!.height).toBeGreaterThanOrEqual(45);
  await expect(model(page)).toHaveValue('retired-model');
  await expect(model(page).locator('option[value^="gamma-"]')).toHaveCount(0);
  // Try again asks again (a failure is never cached): the note comes back.
  const again = page.waitForResponse((r) => r.url().endsWith('/settings/model/options'));
  await retry.click();
  expect((await again).status()).toBe(200);
  await expect(msg(page).locator('[data-ps-ask-retry="model"]')).toBeVisible();
  await page.getByRole('button', { name: CORE.en['plugins.settings.save'] }).click();
  await expect(page.locator('#ps-msg')).toContainText(CORE.en['plugins.settings.saved']);
  // Put the provider back for the next test. The saved form asks again (c fails
  // again); let that finish, since a new ask aborts one still in flight and
  // htmx logs the abort as a console error.
  await expect(msg(page).locator('[data-ps-ask-retry="model"]')).toBeVisible();
  // (let the ask for the new provider finish first: a Save swaps the form out
  // from under a request still in flight, and htmx logs the abort)
  const back = page.waitForResponse((r) => r.url().endsWith('/settings/model/options'));
  await page.locator('select[name="setting_provider"]').selectOption('a');
  await back;
  await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(1);
  await page.getByRole('button', { name: CORE.en['plugins.settings.save'] }).click();
  await expect(page.locator('#ps-msg')).toContainText(CORE.en['plugins.settings.saved']);
  done();
});

test('a plugin-supplied choice saves and is selected after a reload', async () => {
  const done = watchConsole(page);
  await open(page);
  await expect(model(page).locator('option[value="alpha-big"]')).toHaveCount(1);
  await model(page).selectOption('alpha-big');
  await page.getByRole('button', { name: CORE.en['plugins.settings.save'] }).click();
  await expect(page.locator('#ps-msg')).toContainText(CORE.en['plugins.settings.saved']);
  await open(page);
  await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(1);
  await expect(model(page)).toHaveValue('alpha-big');
  await expect(msg(page)).toHaveText('');
  done();
});

for (const vp of [
  { w: 1024, h: 600, lang: 'en' },
  { w: 360, h: 800, lang: 'fa' },
]) {
  test(`no sideways scroll, readable note and Try again at ${vp.w}px (${vp.lang})`, async () => {
    const done = watchConsole(page);
    await page.setViewportSize({ width: vp.w, height: vp.h });
    await open(page, vp.lang);
    await expect(model(page).locator('option[value="alpha-small"]')).toHaveCount(1);
    await noHScroll(page);
    await page.locator('select[name="setting_provider"]').selectOption('c');
    await expect(msg(page)).toContainText(txt(vp.lang, 'plugins.settings.options_failed'));
    const retry = msg(page).locator('[data-ps-ask-retry="model"]');
    await expect(retry).toBeVisible();
    await expect(retry).toHaveText(txt(vp.lang, 'plugins.settings.options_retry'));
    expect((await retry.boundingBox())!.height).toBeGreaterThanOrEqual(45);
    const card = await page.locator('#ps-form').evaluate((f) => f.closest('.card')!.getBoundingClientRect());
    for (const b of [await retry.boundingBox(), await model(page).boundingBox()]) {
      expect(b!.x).toBeGreaterThanOrEqual(card.left - 1);
      expect(b!.x + b!.width).toBeLessThanOrEqual(card.right + 1);
    }
    await noHScroll(page);
    done();
  });
}
