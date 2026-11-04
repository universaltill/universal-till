import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';
import { execFileSync } from 'child_process';
import { watchConsole } from './helpers';
import { REPO_ROOT } from './worker-till';

// ut-docs#5335: a PIN/confirm prompt's own request that gets no answer within
// a bounded time is abandoned client-side and treated EXACTLY like #5298's
// network failure (dialog closed, "may or may not have been done" outcome
// text, no replay). The 30 s production value is overridden to 1 s through
// window.__utPromptTimeoutMs (read per request). The PIN-dialog cases are in
// prompt-timeout-pin-5335.spec.ts (auth project).

const OUTCOME = 'The connection dropped before the till replied, so this may or may not have been done. Check before you try again.';
const OVERRIDE_MS = 1000;

function setting(op: 'set' | 'delete', key: string, value?: string) {
  const dataDir = process.env.UT_E2E_WORKER_DATA_DIR;
  if (!dataDir) throw new Error('UT_E2E_WORKER_DATA_DIR unset');
  execFileSync('go', ['run', './e2e/till_setting', op, key, ...(value === undefined ? [] : [value])], {
    cwd: REPO_ROOT,
    env: { ...process.env, UT_DATA_DIR: dataDir },
    stdio: ['ignore', 'ignore', 'inherit'],
  });
}

async function shortTimeout(page: Page) {
  await page.addInitScript((ms) => { (window as any).__utPromptTimeoutMs = ms; }, OVERRIDE_MS);
}

type Late = { status: number; contentType: string; body: string; headers?: Record<string, string> };

// Every held route is released when its test ends, pass or fail, so a failed
// assertion never leaves a route handler parked on its gate (review).
const pendingReleases: Array<() => void> = [];
test.afterEach(() => { for (const r of pendingReleases.splice(0)) r(); });

// Lets POST #1 through, holds every later POST until release(late).
async function holdSecondPost(page: Page, glob: string, passFirst: boolean) {
  let posts = 0;
  let release!: (l?: Late) => void;
  const gate = new Promise<Late | undefined>((r) => { release = r; });
  pendingReleases.push(() => release());
  let held!: () => void;
  const inFlight = new Promise<void>((r) => { held = r; });
  await page.route(glob, async (route: Route) => {
    if (route.request().method() !== 'POST') return route.continue();
    posts++;
    if (passFirst && posts === 1) return route.continue();
    held();
    const late = await gate;
    try {
      if (late) await route.fulfill(late); else await route.abort();
    } catch { /* an aborted request cannot be fulfilled */ }
  });
  return { posts: () => posts, release, inFlight };
}

async function openConfirm(page: Page) {
  await page.goto('/settings#settings-display');
  const form = page.locator('form.set-gap[hx-post="/api/settings/display-mode"]');
  await form.locator('select[name=mode]').selectOption('self_order');
  await form.locator('button[type=submit]').click();
  const dlg = page.locator('#confirm-modal');
  await expect(dlg).toHaveJSProperty('open', true);
  return dlg;
}

test.describe('confirm prompt timeout (ut-docs#5335)', () => {
  test.beforeEach(() => {
    test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir)');
    setting('set', 'sync.primary_url', 'http://127.0.0.1:9');
  });
  const restore = () => {
    if (!process.env.UT_E2E_WORKER_DATA_DIR) return;
    try { setting('delete', 'sync.primary_url'); } catch { setting('delete', 'sync.primary_url'); }
  };
  test.afterEach(restore);
  test.afterAll(restore);

  test('T1 held confirm request: dialog closes, outcome text shown, one POST', async ({ page }) => {
    await shortTimeout(page);
    const h = await holdSecondPost(page, '**/api/settings/display-mode', true);
    const dlg = await openConfirm(page);
    await dlg.locator('button[type=submit]').click();
    await h.inFlight;
    await expect(dlg).toHaveJSProperty('open', false, { timeout: 5000 });
    const text = page.locator('#pos-alert .notice-text');
    await expect(text).toHaveText(OUTCOME);
    await expect(page.locator('#pos-alert')).toBeInViewport();
    expect(h.posts()).toBe(2);
    h.release();
  });

  for (const kind of ['success', 'prompt'] as const) {
    test(`T2 late ${kind} response after the timeout changes nothing`, async ({ page }) => {
      await shortTimeout(page);
      const h = await holdSecondPost(page, '**/api/settings/display-mode', true);
      const dlg = await openConfirm(page);
      await dlg.locator('button[type=submit]').click();
      await h.inFlight;
      await expect(page.locator('#pos-alert .notice-text')).toHaveText(OUTCOME);
      await expect(dlg).toHaveJSProperty('open', false);
      h.release(kind === 'success'
        ? { status: 200, contentType: 'text/html', body: '<span id="late-5335">late</span>' }
        : { status: 200, contentType: 'text/html', headers: { 'X-UT-Response': 'elevation-prompt' },
            body: '<dialog id="confirm-modal" hx-swap-oob="true" open><form><button type="submit">late</button></form></dialog>' });
      await page.waitForTimeout(500);
      await expect(dlg).toHaveJSProperty('open', false);
      await expect(page.locator('#late-5335')).toHaveCount(0);
      await expect(page.locator('#pos-alert .notice-text')).toHaveText(OUTCOME);
      expect(h.posts()).toBe(2);
    });
  }

  test('T8 default timeout is 30000 for prompt requests, none for htmx.ajax', async ({ page }) => {
    const seen: Record<string, number> = {};
    await page.route('**/api/settings/display-mode', async (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      if (!seen.n) { seen.n = 1; return route.continue(); }
      return route.abort();
    });
    await page.route('**/__ut5335/plain', (route) => route.abort());
    const dlg = await openConfirm(page);
    await page.evaluate(() => {
      const w = window as any;
      w.__to = {};
      document.body.addEventListener('htmx:beforeSend', (ev: any) => {
        const inPrompt = !!ev.detail.elt.closest('[data-after-request~="elevation-done"]');
        w.__to[inPrompt ? 'prompt' : 'plain'] = ev.detail.xhr.timeout;
      });
    });
    await dlg.locator('button[type=submit]').click();
    await page.evaluate(() => {
      const t = document.createElement('div'); t.id = 't5335'; document.body.appendChild(t);
      (window as any).htmx.ajax('GET', '/__ut5335/plain', { target: '#t5335' });
    });
    await expect.poll(() => page.evaluate(() => Object.keys((window as any).__to).length)).toBe(2);
    const to = await page.evaluate(() => (window as any).__to);
    expect(to.prompt).toBe(30000);
    expect(to.plain).toBe(0);
    expect(await page.evaluate(() => (window as any).utPromptTimeoutMs())).toBe(30000);
  });
});

test.describe('utPostWithElevation timeout (ut-docs#5335)', () => {
  test('T3 held first send: outcome text in the card, button enabled, no replay', async ({ page }) => {
    await shortTimeout(page);
    const h = await holdSecondPost(page, '**/api/data/reset-transactions', false);
    await page.goto('/settings#settings-data');
    await page.locator('[data-testid=data-reset]').click();
    await h.inFlight;
    const text = page.locator('#data-reset-msg .pos-notice.error .notice-text');
    await expect(text).toHaveText(OUTCOME, { timeout: 5000 });
    const body = await page.locator('body').innerText();
    for (const bad of ['TypeError', 'AbortError', 'Failed to fetch', 'signal is aborted']) expect(body).not.toContain(bad);
    await expect(page.locator('#data-reset-btn')).toBeEnabled();
    await expect(page.locator('#elevation-modal')).not.toHaveJSProperty('open', true);
    expect(h.posts()).toBe(1);
    h.release({ status: 200, contentType: 'text/plain', body: 'late-ok' });
    await page.waitForTimeout(500);
    await expect(text).toHaveText(OUTCOME);
    expect(h.posts()).toBe(1);
  });

  test('T4 held send rejects tagged; neither callback runs', async ({ page }) => {
    await shortTimeout(page);
    const h = await holdSecondPost(page, '**/__ut5335/x', false);
    await page.goto('/');
    await page.evaluate(() => {
      const w = window as any;
      w.__r = { done: 0, cancelled: 0, net: null, rejected: false };
      w.utPostWithElevation('/__ut5335/x', {}, () => { w.__r.done++; }, () => { w.__r.cancelled++; })
        .catch((e: any) => { w.__r.rejected = true; w.__r.net = !!e.utNetworkError; });
    });
    await h.inFlight;
    await expect.poll(() => page.evaluate(() => (window as any).__r.rejected), { timeout: 5000 }).toBe(true);
    const r = await page.evaluate(() => (window as any).__r);
    expect(r).toMatchObject({ done: 0, cancelled: 0, net: true });
    h.release({ status: 200, contentType: 'text/plain', body: 'late' });
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => (window as any).__r.done)).toBe(0);
  });

  // ut-docs#5454: a listed (server-bounded) path is never aborted on either leg.
  test('T9 held first send to a listed path is not aborted; late reply reaches onDone', async ({ page }) => {
    await shortTimeout(page);
    const h = await holdSecondPost(page, '**/api/refund', false);
    await page.goto('/');
    await page.evaluate(() => {
      const w = window as any;
      w.__r = { done: 0, cancelled: 0, rejected: false, resolved: false };
      w.utPostWithElevation('/api/refund', {}, () => { w.__r.done++; }, () => { w.__r.cancelled++; })
        .then(() => { w.__r.resolved = true; }, () => { w.__r.rejected = true; });
    });
    await h.inFlight;
    await page.waitForTimeout(OVERRIDE_MS * 2.5);
    expect(await page.evaluate(() => (window as any).__r)).toMatchObject({ done: 0, rejected: false, resolved: false });
    await expect(page.locator('#pos-alert .notice-text')).not.toHaveText(OUTCOME);
    h.release({ status: 200, contentType: 'text/plain', body: 'ok' });
    await expect.poll(() => page.evaluate(() => (window as any).__r.resolved), { timeout: 5000 }).toBe(true);
    expect(await page.evaluate(() => (window as any).__r)).toMatchObject({ done: 1, cancelled: 0, rejected: false });
    await expect(page.locator('#pos-alert .notice-text')).not.toHaveText(OUTCOME);
  });

  test('T10 held PIN retry to a listed path is not aborted; late reply reaches onDone', async ({ page }) => {
    await shortTimeout(page);
    let posts = 0;
    let fulfilled = 0;
    let release!: () => void;
    const gate = new Promise<void>((r) => { release = r; });
    pendingReleases.push(() => release());
    let held!: () => void;
    const retryInFlight = new Promise<void>((r) => { held = r; });
    await page.route('**/api/refund', async (route: Route) => {
      posts++;
      if (posts === 1) {
        await route.fulfill({
          status: 200, contentType: 'text/html',
          body: '<dialog id="elevation-modal"><form><input name="pin"><button type="submit">ok</button></form></dialog>',
        });
        return;
      }
      held();
      await gate;
      try { await route.fulfill({ status: 200, contentType: 'text/plain', body: 'ok' }); fulfilled++; } catch { /* aborted */ }
    });
    await page.goto('/');
    await page.evaluate(() => {
      const w = window as any;
      w.__r = { done: 0, cancelled: 0, rejected: false, resolved: false };
      w.utPostWithElevation('/api/refund', {}, () => { w.__r.done++; }, () => { w.__r.cancelled++; })
        .then(() => { w.__r.resolved = true; }, () => { w.__r.rejected = true; });
    });
    await page.locator('#elevation-modal input[name=pin]').fill('1234');
    await page.locator('#elevation-modal button[type=submit]').click();
    await retryInFlight;
    await page.waitForTimeout(OVERRIDE_MS * 2.5);
    expect(await page.evaluate(() => (window as any).__r)).toMatchObject({ done: 0, rejected: false, resolved: false });
    await expect(page.locator('#pos-alert .notice-text')).not.toHaveText(OUTCOME);
    release();
    await expect.poll(() => page.evaluate(() => (window as any).__r.resolved), { timeout: 5000 }).toBe(true);
    expect(fulfilled).toBe(1);
    expect(await page.evaluate(() => (window as any).__r)).toMatchObject({ done: 1, cancelled: 0, rejected: false });
    await expect(page.locator('#pos-alert .notice-text')).not.toHaveText(OUTCOME);
    await expect(page.locator('#elevation-modal')).not.toHaveJSProperty('open', true);
  });

  test('T5 a download whose body outlasts the timeout is not truncated', async ({ page }) => {
    await shortTimeout(page);
    await page.route('**/__ut5335/dl', (route) => route.fulfill({
      status: 200, contentType: 'text/csv', headers: { 'Content-Disposition': 'attachment; filename=x.csv' }, body: 'a,b\n1,2\n',
    }));
    await page.goto('/');
    const out = await page.evaluate((ms) => (window as any).utPostWithElevation('/__ut5335/dl', {},
      async (r: Response) => { await new Promise((res) => setTimeout(res, ms * 2)); return r.text(); }), OVERRIDE_MS);
    expect(out).toBe('a,b\n1,2\n');
  });

  test('T6 a non-prompt htmx.ajax request is never timed out (#5404 times user events only)', async ({ page }) => {
    await shortTimeout(page);
    const h = await holdSecondPost(page, '**/__ut5335/plain', false);
    await page.goto('/');
    await page.evaluate(() => {
      const w = window as any;
      w.__timeouts = 0;
      document.body.addEventListener('htmx:timeout', () => { w.__timeouts++; });
      const t = document.createElement('div'); t.id = 't5335'; document.body.appendChild(t);
      w.htmx.ajax('POST', '/__ut5335/plain', { target: '#t5335' });
    });
    await h.inFlight;
    await page.waitForTimeout(OVERRIDE_MS * 2.5);
    expect(await page.evaluate(() => (window as any).__timeouts)).toBe(0);
    await expect(page.locator('#pos-alert .notice-text')).not.toHaveText(OUTCOME);
    h.release({ status: 200, contentType: 'text/html', body: 'x' });
  });

  test('T7 a completed prompt leaves no stale timer', async ({ page }) => {
    const assertClean = watchConsole(page);
    await shortTimeout(page);
    let posts = 0;
    await page.route('**/__ut5335/ok', (route) => { posts++; return route.fulfill({ status: 200, contentType: 'text/plain', body: 'ok' }); });
    await page.goto('/');
    const first = await page.evaluate(() => (window as any).utPostWithElevation('/__ut5335/ok', {}, (r: Response) => r.text()));
    expect(first).toBe('ok');
    await page.waitForTimeout(OVERRIDE_MS * 2.5);
    await expect(page.locator('#pos-alert')).toBeHidden();
    const second = await page.evaluate(() => (window as any).utPostWithElevation('/__ut5335/ok', {}, (r: Response) => r.text()));
    expect(second).toBe('ok');
    expect(posts).toBe(2);
    assertClean();
  });
});
