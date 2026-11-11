import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole, waitForStableLayout } from './helpers';
import en from '../../web/locales/en.json';
import fa from '../../web/locales/fa.json';

// ut-docs#4114: table paging inside a content slot panel, driven against a
// REAL plugin (the views guest seeded by e2e/seed_plugin_views: Reports
// carries two reports.panels panels, paged_a over three pages and a static
// paged_b). Only a browser shows the swap: htmx's target, no boost, no
// history entry, focus after the tapped link is replaced, and the OOB alert
// under HX-Reswap: none. Own till (own worker), like plugin-views-4378.
test.describe.configure({ mode: 'serial' });

const CORE: Record<string, Record<string, string>> = { en, fa };
const VIEWPORTS = [
  { w: 1024, h: 600 },
  { w: 360, h: 740 },
];
const LANGS = ['en', 'fa'];
const A = '#plugin-slot-reports-panels-0';
const B = '#plugin-slot-reports-panels-1';
const SHOTS = process.env.UT_4114_SHOTS || ''; // a directory: keep screenshots for the review record

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

async function openReports(p: Page, lang: string, vp: { w: number; h: number }) {
  await p.setViewportSize({ width: vp.w, height: vp.h });
  await p.goto(`/reports?lang=${lang}`);
  await expect(p.locator(`${A} .plugin-view-table`)).toBeVisible();
  await expect(p.locator(`${B} .plugin-view-table`)).toBeVisible();
  await waitForStableLayout(p, `${A} .plugin-view-paging`);
}

const rowText = (p: Page, panel: string) => p.locator(`${panel} .plugin-view-table tbody td`).first();
const link = (p: Page, panel: string, rel: 'next' | 'prev') => p.locator(`${panel} .plugin-view-paging a[rel="${rel}"]`);
const activeIs = (p: Page, panel: string, rel: string) =>
  p.evaluate(([sel, r]) => document.activeElement === document.querySelector(`${sel} .plugin-view-paging a[rel="${r}"]`), [panel, rel]);

async function shot(p: Page, name: string) {
  if (SHOTS) await p.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: false });
}

for (const vp of VIEWPORTS) {
  for (const lang of LANGS) {
    test(`paging swaps only its panel ${lang} ${vp.w}px (ut-docs#4114)`, async () => {
      const done = watchConsole(page);
      await openReports(page, lang, vp);
      await expect(rowText(page, A)).toHaveText('A row p.1');
      const bBefore = await page.locator(B).innerHTML();
      const tabsBefore = await page.locator('main').locator('nav, [role=tablist]').first().innerHTML().catch(() => '');
      const urlBefore = page.url();
      await page.evaluate(() => { (window as any).__pagingMarker = 4114; });

      // First load took no focus.
      expect(await page.evaluate(() => !!document.activeElement?.closest('.plugin-view'))).toBe(false);

      // Strings and wiring.
      const t = CORE[lang];
      await expect(link(page, A, 'next')).toHaveText(t['plugin.view.page_next']);
      await expect(link(page, A, 'prev')).toHaveCount(0);
      const box = await link(page, A, 'next').boundingBox();
      expect(box!.height).toBeGreaterThanOrEqual(45.5);
      expect(box!.width).toBeGreaterThanOrEqual(45.5);
      await shot(page, `page1-${lang}-${vp.w}`);

      // Keyboard: Tab-reachable, Enter pages.
      await link(page, A, 'next').focus();
      await page.keyboard.press('Enter');
      await expect(rowText(page, A)).toHaveText('A row p.2');
      expect(await activeIs(page, A, 'next')).toBe(true);
      const described = await page.evaluate((sel) => {
        const a = document.querySelector(`${sel} .plugin-view-paging a[rel="next"]`)!;
        return document.getElementById(a.getAttribute('aria-describedby') || '')?.textContent || '';
      }, A);
      expect(described).toMatch(/[2\u06F2]/); // Latin or Persian digit
      await shot(page, `page2-${lang}-${vp.w}`);

      // Last page: no Next, focus lands on Previous (never <body>).
      await link(page, A, 'next').click();
      await expect(rowText(page, A)).toHaveText('A row p.3');
      await expect(link(page, A, 'next')).toHaveCount(0);
      expect(await activeIs(page, A, 'prev')).toBe(true);
      await shot(page, `page3-${lang}-${vp.w}`);

      await link(page, A, 'prev').click();
      await expect(rowText(page, A)).toHaveText('A row p.2');
      await link(page, A, 'prev').click();
      await expect(rowText(page, A)).toHaveText('A row p.1');

      // Nothing else moved: panel B, the page, the history, the shell.
      expect(await page.locator(B).innerHTML()).toBe(bBefore);
      expect(await page.locator('main').locator('nav, [role=tablist]').first().innerHTML().catch(() => '')).toBe(tabsBefore);
      expect(page.url()).toBe(urlBefore);
      expect(await page.evaluate(() => (window as any).__pagingMarker)).toBe(4114);
      await expect(page.locator(`${A}-alert .plugin-view-notice`)).toHaveCount(0);
      await expect(page.locator(A)).not.toHaveAttribute('aria-busy', 'true');
      await noHScroll(page);
      await expect(page.locator('.statusbar')).toBeVisible();
      done();
    });
  }
}

test('RTL order and no clipping in fa at 360px (ut-docs#4114)', async () => {
  await openReports(page, 'fa', VIEWPORTS[1]);
  await link(page, A, 'next').click();
  await expect(rowText(page, A)).toHaveText('A row p.2');
  const [prev, pos, next] = await Promise.all([
    link(page, A, 'prev').boundingBox(),
    page.locator(`${A} .plugin-view-paging-pos`).boundingBox(),
    link(page, A, 'next').boundingBox(),
  ]);
  // Right to left: Previous, indicator, Next (all on one row or wrapped, never reversed).
  if (Math.abs(prev!.y - next!.y) < 4) {
    expect(prev!.x).toBeGreaterThan(pos!.x);
    expect(pos!.x).toBeGreaterThan(next!.x);
  }
  for (const b of [prev!, pos!, next!]) {
    expect(b.x).toBeGreaterThanOrEqual(0);
    expect(b.x + b.width).toBeLessThanOrEqual(360 + 0.5);
  }
  await noHScroll(page);
  await shot(page, 'rtl-fa-360');
});

test('in flight: one ask, panel B stays pageable, busy state clears (ut-docs#4114)', async () => {
  await openReports(page, 'en', VIEWPORTS[0]);
  let asks = 0;
  await page.route('**/plugin/e2e-views/paged_a?*', async (route) => {
    asks++;
    await new Promise((r) => setTimeout(r, 1500));
    await route.continue();
  });
  const next = link(page, A, 'next');
  await next.click();
  await expect(page.locator(A)).toHaveAttribute('aria-busy', 'true');
  await expect(page.locator(A)).toHaveClass(/htmx-request/);
  await expect(next).toHaveAttribute('data-ut-busy', '1'); // the busy ring (htmx puts .htmx-request on the panel)
  await shot(page, 'in-flight');
  // Status bar still answers hit tests while the request runs.
  const hit = await page.evaluate(() => {
    const r = document.querySelector('.statusbar')!.getBoundingClientRect();
    return !!document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.closest('.statusbar');
  });
  expect(hit).toBe(true);
  // Taps on the busy panel's links are dropped (pointer-events + hx-sync).
  await next.click({ force: true }).catch(() => {});
  await next.click({ force: true }).catch(() => {});
  await expect(rowText(page, A)).toHaveText('A row p.2');
  expect(asks).toBe(1);
  await expect(page.locator(A)).not.toHaveAttribute('aria-busy', 'true');
  await page.unroute('**/plugin/e2e-views/paged_a?*');
});

test('a failed page keeps the page, focus and retry (ut-docs#4114)', async () => {
  await openReports(page, 'en', VIEWPORTS[0]);
  await page.route('**/plugin/e2e-views/paged_a?*', (route) => route.abort());
  await link(page, A, 'next').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator(A)).not.toHaveAttribute('aria-busy', 'true');
  await expect(rowText(page, A)).toHaveText('A row p.1');
  expect(await activeIs(page, A, 'next')).toBe(true);
  await page.unroute('**/plugin/e2e-views/paged_a?*');
  await page.keyboard.press('Enter'); // retry
  await expect(rowText(page, A)).toHaveText('A row p.2');
});

test('a server-side failure fills only the panel alert (ut-docs#4114)', async () => {
  await openReports(page, 'en', VIEWPORTS[0]);
  await page.route('**/plugin/e2e-views/paged_a?*', (route) =>
    route.fulfill({
      status: 502,
      contentType: 'text/html; charset=utf-8',
      headers: { 'HX-Reswap': 'none' },
      body: `<div id="plugin-slot-reports-panels-0-alert" class="plugin-view-alert" aria-live="polite" hx-swap-oob="innerHTML"><div class="plugin-view-notice warn">${en['plugin.view.page_failed']}</div></div>`,
    }),
  );
  await link(page, A, 'next').click();
  await expect(page.locator(`${A}-alert .plugin-view-notice`)).toHaveText(en['plugin.view.page_failed']);
  await expect(rowText(page, A)).toHaveText('A row p.1');
  expect(await activeIs(page, A, 'next')).toBe(true);
  await shot(page, 'failed');
  await page.unroute('**/plugin/e2e-views/paged_a?*');
  await link(page, A, 'next').click();
  await expect(rowText(page, A)).toHaveText('A row p.2');
  await expect(page.locator(`${A}-alert .plugin-view-notice`)).toHaveCount(0);
});
