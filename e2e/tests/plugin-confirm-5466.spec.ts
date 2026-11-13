import { test, expect } from './fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { startWorkerTill, type WorkerTill } from './worker-till';
import { watchConsole, markTourDone } from './helpers';
import en from '../../web/locales/en.json';
import fa from '../../web/locales/fa.json';

// ut-docs#5466: the plugin-view `confirm` component (ADR-0153 Decision 3)
// driven end to end against a REAL AI-agent plugin on an AUTH-ON worker
// till. Core stamps an agent operator only from an auth session, so with
// UT_AUTH=off every action_propose is refused (ut-docs#4168) -- which is why
// plugin-views-4378.spec.ts could not cover it. startWorkerTill's `auth`
// option boots the till with auth on and completes first boot through the
// real POST /api/auth/setup; this spec creates a manager and signs in as
// that manager through the real /login PIN pad. The plugin is
// internal/plugins/testdata/agent_guest, installed by e2e/seed_agent_plugin.
// Every view open creates a proposal and a session may make
// MaxPerSessionPerHr (20), so `open` counts them.
test.describe.configure({ mode: 'serial' });

const CORE: Record<string, Record<string, string>> = { en, fa };
const RUN = Date.now().toString(36).toUpperCase();
const MANAGER_PIN = '735186'; // differs from the admin PIN: the pad identifies the user by PIN
const MANAGER_USER = 'manager5466' + RUN.toLowerCase();
const VIEWPORTS = [
  { w: 1024, h: 600 },
  { w: 360, h: 800 },
];
const MAX_OPENS = 15;

let till: WorkerTill;
let context: BrowserContext;
let page: Page;
let opens = 0;

async function pinLogin(p: Page, pin: string) {
  await p.goto('/login');
  for (const d of pin.split('')) {
    await p.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await p.locator('button[type=submit].pin-key').click();
  await p.waitForURL((u) => !u.pathname.includes('/login'));
  await markTourDone(p);
}

async function createManager(p: Page) {
  const created = await p.request.post('/api/users', {
    form: { username: MANAGER_USER, display_name: 'Manager 5466', role: 'manager' },
  });
  expect(created.ok(), 'create manager user').toBe(true);
  await p.goto('/users');
  // The handlers answer 200 even when they show an error, so the row must exist.
  const row = p.locator('tr', { hasText: MANAGER_USER });
  await expect(row, 'manager row on /users').toHaveCount(1);
  const pinAction = await row.locator('form[hx-post$="/pin"]').getAttribute('hx-post');
  const id = pinAction!.match(/\/api\/users\/([^/]+)\/pin/)![1];
  const pinResp = await p.request.post(`/api/users/${id}/pin`, { form: { pin: MANAGER_PIN } });
  expect(pinResp.ok(), 'set manager PIN').toBe(true);
}

test.beforeAll(async ({ browser }, workerInfo) => {
  test.setTimeout(240_000); // the extra seed compiles the WASM guest
  till = await startWorkerTill(workerInfo.parallelIndex, { auth: true, extraSeeds: ['./e2e/seed_agent_plugin'] });
  context = await browser.newContext({ baseURL: till.url, viewport: { width: 1024, height: 600 } });
  page = await context.newPage();
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' });
  await pinLogin(page, till.adminPin!);
  await createManager(page);
  await page.request.post('/api/auth/logout');
  await pinLogin(page, MANAGER_PIN);
});

test.afterAll(async () => {
  await context?.close();
  await till?.stop();
});

async function noHScroll(p: Page) {
  expect(await p.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
}

// Opens the view (which makes the plugin propose a new item) and returns the item name.
async function open(p: Page, lang: string, vp: { w: number; h: number }, tag: string): Promise<string> {
  opens++;
  expect(opens, 'view opens per session (cap is 20)').toBeLessThanOrEqual(MAX_OPENS);
  const name = `E2E 5466 ${RUN} ${tag}`;
  await p.setViewportSize({ width: vp.w, height: vp.h });
  await p.goto(`/plugin/e2e-agent/confirm?lang=${lang}&name=${encodeURIComponent(name)}`);
  await expect(p.locator('.agent-confirm')).toBeVisible();
  await expect(p.locator('.plugin-view-notice')).toHaveCount(0);
  return name;
}

// A core string with a %s placeholder as an anchored regex.
function fmtRe(s: string): RegExp {
  return new RegExp('^' + s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace('%s', '.+') + '$');
}

test('auth is on (ut-docs#5466)', async ({ browser }) => {
  const ctx2 = await browser.newContext({ baseURL: till.url });
  try {
    const p = await ctx2.newPage();
    const done = watchConsole(p);
    await p.goto('/');
    await expect(p).toHaveURL(/\/login/);
    done();
  } finally {
    await ctx2.close();
  }
});

test('confirm card content and geometry (ut-docs#5466)', async () => {
  for (const lang of ['en', 'fa']) {
    for (const vp of VIEWPORTS) {
      const done = watchConsole(page);
      const name = await open(page, lang, vp, `geo-${lang}-${vp.w}`);
      const C = CORE[lang];
      await expect(page.locator('html')).toHaveAttribute('dir', lang === 'fa' ? 'rtl' : 'ltr');
      const card = page.locator('.agent-confirm');
      await expect(card.locator('p.muted')).toHaveText(C['agent.confirm.intro']);
      await expect(card.locator('thead th')).toHaveText([
        C['agent.confirm.col_item'],
        C['agent.confirm.col_field'],
        C['agent.confirm.col_before'],
        C['agent.confirm.col_after'],
      ]);
      const row = card.locator('tbody tr').filter({ has: page.locator('td:nth-child(1)', { hasText: name }) });
      await expect(row).toHaveCount(1);
      await expect(row.locator('td:nth-child(4)')).not.toBeEmpty();
      const confirmBtn = card.locator('.btn.btn-touch.primary');
      const cancelBtn = card.locator('.btn.btn-touch.secondary');
      await expect(confirmBtn).toHaveText(fmtRe(C['agent.confirm.button']));
      await expect(cancelBtn).toHaveText(C['agent.confirm.cancel']);
      for (const b of [confirmBtn, cancelBtn]) {
        const box = await b.boundingBox();
        expect(box!.width).toBeGreaterThanOrEqual(44);
        expect(box!.height).toBeGreaterThanOrEqual(44);
      }
      if (vp.w === 360) await noHScroll(page);
      done();
    }
  }
});

let confirmedName = '';

test('confirm applies the change (ut-docs#5466)', async () => {
  const done = watchConsole(page);
  confirmedName = await open(page, 'en', { w: 360, h: 800 }, 'confirm');
  const id = (await page.locator('.agent-confirm').getAttribute('id'))!.replace('agent-confirm-', '');
  await page.locator('.agent-confirm .btn.btn-touch.primary').click();
  await expect(page.locator('.plugin-view-notice.info')).toHaveText(fmtRe(en['agent.notice.applied']));
  await expect(page.locator(`#agent-confirm-${id}`)).toHaveCount(0);
  await expect(page.locator('.agent-confirm')).toHaveCount(0);
  await noHScroll(page);
  await page.goto('/catalog?lang=en');
  expect(await page.content()).toContain(confirmedName);
  done();
});

test('cancel applies nothing (ut-docs#5466)', async () => {
  const done = watchConsole(page);
  const name = await open(page, 'en', { w: 1024, h: 600 }, 'cancel-en');
  await page.locator('.agent-confirm .btn.btn-touch.secondary').click();
  await expect(page.locator('.plugin-view-notice.info')).toHaveText(en['agent.notice.cancelled']);
  await expect(page.locator('.agent-confirm')).toHaveCount(0);
  await page.goto('/catalog?lang=en');
  const html = await page.content();
  expect(html).toContain(confirmedName); // the catalogue did render the applied item
  expect(html).not.toContain(name);
  done();
});

test('cancel in fa at 360px stays inside the viewport (ut-docs#5466)', async () => {
  const done = watchConsole(page);
  await open(page, 'fa', { w: 360, h: 800 }, 'cancel-fa');
  await page.locator('.agent-confirm .btn.btn-touch.secondary').click();
  await expect(page.locator('.plugin-view-notice.info')).toHaveText(fa['agent.notice.cancelled']);
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await noHScroll(page);
  done();
});
