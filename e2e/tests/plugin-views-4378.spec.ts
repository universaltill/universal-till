import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole, expectGermanIfPackLoaded, waitForStableLayout } from './helpers';
import en from '../../web/locales/en.json';
import fa from '../../web/locales/fa.json';
import pvEn from '../seed_plugin_views/locales/en.json';
import pvDe from '../seed_plugin_views/locales/de.json';
import pvFa from '../seed_plugin_views/locales/fa.json';

// ut-docs#4378: the plugin-view components (ADR-0121 plugin views) driven
// end to end against a REAL plugin. Until now they were covered only by Go
// tests on a fake bus (internal/pages/plugin_view_test.go) and by
// plugin-chat-focus-4379.spec.ts, which hand-writes the chat markup. This
// file runs on its OWN till with a real compiled WASM guest
// (internal/plugins/testdata/views_guest, installed by
// e2e/seed_plugin_views) that answers ui.view.ask / ui.action.ask with a
// chat, a wizard (+ qr) and a long-job page, so the real servePluginView,
// plugin_job.go poll, templates and app.css are what is measured.
// Core strings come from web/locales/*.json; the plugin's own strings from
// e2e/seed_plugin_views/locales/*.json (its bundle, a fixture, not core
// i18n). The `confirm` component needs an auth-on till and a signed-in
// manager (ut-docs#5466) and is not covered here.
test.describe.configure({ mode: 'serial' });

const CORE: Record<string, Record<string, string>> = { en, fa };
const PV: Record<string, Record<string, string>> = { en: pvEn, fa: pvFa, de: pvDe };
const VIEWPORTS = [
  { w: 1024, h: 600 },
  { w: 360, h: 800 },
];
const LANGS = ['en', 'fa'];
const QUESTION = 'What is 4378?';

let till: WorkerTill;
let context: BrowserContext;
let page: Page;

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000); // the extra seed compiles the WASM guest
  till = await startWorkerTill(workerInfo.parallelIndex, { extraSeeds: ['./e2e/seed_plugin_views'] });
  context = await browser.newContext({ baseURL: till.url, viewport: { width: 1024, height: 600 } });
  page = await context.newPage();
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' });
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

async function noHScroll(p: Page) {
  expect(await p.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
}

async function open(p: Page, route: string, lang: string, vp: { w: number; h: number }) {
  await p.setViewportSize({ width: vp.w, height: vp.h });
  await p.goto(`${route}?lang=${lang}`);
  await expect(p.locator('#plugin-view')).toBeVisible();
  await expect(p.locator('.plugin-view-notice')).toHaveCount(0); // not the "unavailable" notice
}

const rect = (p: Page, sel: string) =>
  p.locator(sel).evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect();
      return { left: r.left, right: r.right, width: r.width, height: r.height, top: r.top, bottom: r.bottom };
    }),
  );

// ---- chat (#4132) ----

for (const vp of VIEWPORTS) {
  for (const lang of LANGS) {
    test(`chat geometry ${lang} ${vp.w}px (ut-docs#4378)`, async () => {
      const done = watchConsole(page);
      await open(page, '/plugin/e2e-views/chat', lang, vp);
      await waitForStableLayout(page, '.plugin-chat-log, .plugin-chat-turn');
      const [log] = await rect(page, '#plugin-view .plugin-chat-log');
      const users = await rect(page, '.plugin-chat-turn.user');
      const assistants = await rect(page, '.plugin-chat-turn.assistant');
      expect(users).toHaveLength(2);
      expect(assistants).toHaveLength(2);
      const rtl = lang === 'fa';
      await expect(page.locator('html')).toHaveAttribute('dir', rtl ? 'rtl' : 'ltr');
      for (const u of users) {
        if (rtl) {
          expect(Math.abs(u.left - log.left)).toBeLessThanOrEqual(2);
          expect(u.right).toBeLessThan(log.right - 8);
        } else {
          expect(Math.abs(u.right - log.right)).toBeLessThanOrEqual(2);
          expect(u.left).toBeGreaterThan(log.left + 8);
        }
      }
      for (const a of assistants) {
        if (rtl) expect(Math.abs(a.right - log.right)).toBeLessThanOrEqual(2);
        else expect(Math.abs(a.left - log.left)).toBeLessThanOrEqual(2);
      }
      await expect(page.locator('.plugin-chat-tools li')).toHaveText(
        CORE[lang]['plugin.chat.used_tool'].replace('%s', PV[lang]['e2eviews.chat.tool']),
      );
      for (const sel of ['#plugin-view-chat-q', 'form.plugin-chat-ask button[type=submit]']) {
        const [r] = await rect(page, sel);
        expect(r.height, sel).toBeGreaterThanOrEqual(44);
      }
      await noHScroll(page);
      done();
    });

    test(`chat send, thinking, answer ${lang} ${vp.w}px (ut-docs#4378)`, async () => {
      const done = watchConsole(page);
      await open(page, '/plugin/e2e-views/chat', lang, vp);
      const polls: string[] = [];
      page.on('request', (r) => {
        if (r.url().includes('_job=')) polls.push(r.url());
      });
      await page.locator('#plugin-view-chat-q').fill(QUESTION);
      await page.locator('form.plugin-chat-ask button[type=submit]').click();
      const thinking = page.locator('.plugin-chat-thinking');
      await expect(thinking).toBeVisible();
      await expect(thinking).toContainText(CORE[lang]['plugin.chat.thinking']);
      await expect(page.locator('.plugin-chat-turn.user').last()).toContainText(QUESTION); // a plain substring: QUESTION's "?" is not a regex quantifier
      await expect(page.locator('form.plugin-chat-ask')).toHaveCount(0);
      await noHScroll(page);
      await expect(thinking).toHaveCount(0, { timeout: 10_000 });
      expect(polls.length).toBeGreaterThanOrEqual(1);
      const answers = page.locator('.plugin-chat-turn.assistant:not(.plugin-chat-thinking)');
      await expect(answers.last()).toContainText(`Echo: ${QUESTION}`);
      // The history survived the round trip through _turns.
      await expect(page.locator('.plugin-chat-turn')).toHaveCount(6);
      await expect(page.locator('.plugin-chat-turn').first()).toContainText('Hello');
      await expect(page.locator('#plugin-view-chat-q')).toBeFocused(); // ut-docs#4379
      await noHScroll(page);
      page.removeAllListeners('request');
      done();
    });
  }
}

// ---- job poll (ADR-0121 §8, #3908) ----

for (const vp of VIEWPORTS) {
  for (const lang of LANGS) {
    test(`job poll ${lang} ${vp.w}px (ut-docs#4378)`, async () => {
      const done = watchConsole(page);
      await open(page, '/plugin/e2e-views/job', lang, vp);
      const url = page.url();
      let navs = 0;
      page.on('framenavigated', (f) => {
        if (f === page.mainFrame()) navs++;
      });
      await page.locator('form.plugin-view-action button', { hasText: PV[lang]['e2eviews.job.run'] }).click();
      const poll = page.locator('#plugin-view .plugin-view-poll[role=status]');
      await expect(poll).toBeVisible();
      await expect(poll.locator('progress.plugin-view-poll-bar')).toHaveCount(1);
      // The plugin's own JobProgress text, not core's "Working on it…".
      await expect(poll.locator('.plugin-view-poll-msg')).toHaveText(PV[lang]['e2eviews.job.progress']);
      await noHScroll(page);
      await expect(poll).toHaveCount(0, { timeout: 10_000 });
      await expect(page.locator('#plugin-view')).toContainText(PV[lang]['e2eviews.job.done']);
      await expect(page.locator('#plugin-view')).toContainText('E2E-4378-RESULT');
      expect(page.url()).toBe(url);
      expect(navs).toBe(0);
      await noHScroll(page);
      page.removeAllListeners('framenavigated');
      done();
    });
  }
}

// ---- wizard + qr (#3874) ----

const posText = (lang: string, n: number) =>
  CORE[lang]['plugin.view.wizard_step_of'].replace('%s', n.toLocaleString(lang === 'fa' ? 'fa-IR' : 'en')).replace('%s', (3).toLocaleString(lang === 'fa' ? 'fa-IR' : 'en'));

for (const vp of VIEWPORTS) {
  for (const lang of LANGS) {
    test(`wizard step 1 and qr ${lang} ${vp.w}px (ut-docs#4378)`, async () => {
      const done = watchConsole(page);
      await open(page, '/plugin/e2e-views/wizard', lang, vp);
      await expect(page.locator('.plugin-view-wizard-pos')).toHaveText(posText(lang, 1));
      const steps = page.locator('ol.plugin-view-wizard-steps > li');
      await expect(steps).toHaveCount(3);
      for (const [i, k] of ['step1', 'step2', 'step3'].entries()) {
        await expect(steps.nth(i)).toContainText(PV[lang][`e2eviews.wizard.${k}`]);
      }
      await expect(page.locator('li[aria-current="step"]')).toHaveCount(1);
      await expect(steps.first()).toHaveAttribute('aria-current', 'step');
      const img = page.locator('figure.plugin-view-qr img');
      await img.scrollIntoViewIfNeeded();
      const [r] = await rect(page, 'figure.plugin-view-qr img');
      expect(r.width).toBeGreaterThan(0);
      expect(Math.abs(r.width - r.height)).toBeLessThanOrEqual(1);
      expect(r.left).toBeGreaterThanOrEqual(0);
      expect(r.right).toBeLessThanOrEqual(vp.w);
      expect(r.top).toBeGreaterThanOrEqual(0);
      expect(r.bottom).toBeLessThanOrEqual(vp.h);
      expect(await img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeGreaterThan(0);
      await noHScroll(page);
      done();
    });
  }
}

for (const lang of LANGS) {
  test(`wizard next moves to step 2 ${lang} (ut-docs#4378)`, async () => {
    const done = watchConsole(page);
    await open(page, '/plugin/e2e-views/wizard', lang, VIEWPORTS[0]);
    await page.locator('form.plugin-view-action button', { hasText: PV[lang]['e2eviews.wizard.next'] }).click();
    await expect(page.locator('.plugin-view-wizard-pos')).toHaveText(posText(lang, 2));
    const steps = page.locator('ol.plugin-view-wizard-steps > li');
    await expect(steps.nth(0)).toHaveClass(/done/);
    await expect(steps.nth(0)).toContainText(CORE[lang]['plugin.view.step_done']);
    await expect(steps.nth(1)).toHaveAttribute('aria-current', 'step');
    await expect(page.locator('li[aria-current="step"]')).toHaveCount(1);
    await expect(page.locator('figure.plugin-view-qr')).toHaveCount(0);
    done();
  });
}

// ---- German (needs the language pack for core strings) ----

test('German pass over chat, wizard and job (ut-docs#4378)', async () => {
  const done = watchConsole(page);
  const vp = VIEWPORTS[0];
  await open(page, '/plugin/e2e-views/chat', 'de', vp);
  await expectGermanIfPackLoaded(page);
  // The plugin's own de bundle is third-party layer: it renders with or
  // without the core language pack (verified), so these are unconditional.
  await expect(page.locator('.plugin-chat-tools li')).toContainText(pvDe['e2eviews.chat.tool']);
  await open(page, '/plugin/e2e-views/wizard', 'de', vp);
  const steps = page.locator('ol.plugin-view-wizard-steps > li');
  for (const [i, k] of ['step1', 'step2', 'step3'].entries()) {
    await expect(steps.nth(i)).toContainText(pvDe[`e2eviews.wizard.${k}`]);
  }
  await open(page, '/plugin/e2e-views/job', 'de', vp);
  await page.locator('form.plugin-view-action button', { hasText: pvDe['e2eviews.job.run'] }).click();
  await expect(page.locator('.plugin-view-poll-msg')).toHaveText(pvDe['e2eviews.job.progress']);
  await expect(page.locator('.plugin-view-poll')).toHaveCount(0, { timeout: 10_000 });
  done();
});
