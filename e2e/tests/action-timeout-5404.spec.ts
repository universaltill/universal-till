import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';
import { answerTillConfirm, noNativeDialogs } from './helpers';

// ut-docs#5404: ordinary user-initiated htmx actions (click/submit/change/...)
// get the same client timeout as #5335's prompts. Non-GET -> sticky outcome
// text (#5298), GET -> network text. Background refreshes (load/every/custom
// events), htmx.ajax, boosted navigation, multipart, listed paths and
// [data-ut-no-timeout] stay unbounded. 30 s is overridden to 1 s through
// window.__utPromptTimeoutMs.

const OUTCOME = 'The connection dropped before the till replied, so this may or may not have been done. Check before you try again.';
const TIMEOUT_TEXT = 'The till did not reply in time, so this may or may not have been done. Check before you try again.';
const OVERRIDE_MS = 1000;
// Note: htmx 1.9.12 keeps .htmx-request on an element that is both the
// indicator and its own hx-disabled-elt (shared requestCount), so probes
// disable a different element.

async function shortTimeout(page: Page) {
  await page.addInitScript((ms) => { (window as any).__utPromptTimeoutMs = ms; }, OVERRIDE_MS);
}

const pendingReleases: Array<() => void> = [];
test.afterEach(() => { for (const r of pendingReleases.splice(0)) r(); });

type Late = { status: number; contentType: string; body: string };

async function hold(page: Page, glob: string) {
  let n = 0;
  let release!: (l?: Late) => void;
  const gate = new Promise<Late | undefined>((r) => { release = r; });
  pendingReleases.push(() => release());
  let held!: () => void;
  const inFlight = new Promise<void>((r) => { held = r; });
  await page.route(glob, async (route: Route) => {
    n++;
    held();
    const late = await gate;
    try { if (late) await route.fulfill(late); else await route.abort(); } catch { /* already finished */ }
  });
  return { count: () => n, release, inFlight };
}

// Injects markup under #ut-page, lets htmx process it.
async function probe(page: Page, html: string) {
  await page.evaluate((h) => {
    const host = document.createElement('div');
    host.innerHTML = h;
    (document.querySelector('#ut-page') || document.body).appendChild(host);
    (window as any).htmx.process(host);
  }, html);
}

// Records xhr.timeout per path on beforeSend.
async function recordTimeouts(page: Page) {
  await page.evaluate(() => {
    const w = window as any;
    w.__xt = {};
    document.body.addEventListener('htmx:beforeSend', (ev: any) => {
      w.__xt[ev.detail.pathInfo.requestPath] = ev.detail.xhr.timeout;
    });
  });
}
const xt = (page: Page, path: string) => page.evaluate((p) => (window as any).__xt[p], path);

const alertText = (page: Page) => page.locator('#pos-alert .notice-text');

test.describe('ordinary action timeout (ut-docs#5404)', () => {
  test('A1 held POST click: outcome text, control usable, one request, late reply ignored', async ({ page }) => {
    await shortTimeout(page);
    const h = await hold(page, '**/__ut5404/x');
    await page.goto('/');
    await probe(page, '<button id="p" hx-post="/__ut5404/x" hx-target="#t5404" hx-disabled-elt="#dis1">go</button><button id="dis1">other</button><div id="t5404"></div>');
    await page.locator('#p').click();
    await h.inFlight;
    await expect(alertText(page)).toHaveText(TIMEOUT_TEXT, { timeout: 5000 });
    await expect(page.locator('#p')).not.toHaveClass(/htmx-request/);
    await expect(page.locator('#dis1')).toBeEnabled();
    expect(h.count()).toBe(1);
    h.release({ status: 200, contentType: 'text/html', body: '<span id="late">late</span>' });
    await page.waitForTimeout(500);
    await expect(page.locator('#late')).toHaveCount(0);
    expect(h.count()).toBe(1);
  });

  test('A2 held GET click: network text, not outcome', async ({ page }) => {
    await shortTimeout(page);
    const h = await hold(page, '**/__ut5404/g');
    await page.goto('/');
    await probe(page, '<button id="g" hx-get="/__ut5404/g" hx-target="#t5404" hx-disabled-elt="#dis2">go</button><button id="dis2">other</button><div id="t5404"></div>');
    const net = await page.locator('#pos-alert').getAttribute('data-msg-network');
    expect(net).toBeTruthy();
    await page.locator('#g').click();
    await h.inFlight;
    await expect(alertText(page)).toHaveText(net!, { timeout: 5000 });
    await expect(alertText(page)).not.toHaveText(TIMEOUT_TEXT);
    await expect(page.locator('#g')).not.toHaveClass(/htmx-request/);
    await expect(page.locator('#dis2')).toBeEnabled();
  });

  test('A3 DELETE with hx-confirm: outcome, and it is sticky against a later network alert', async ({ page }) => {
    await shortTimeout(page);
    const noNative = noNativeDialogs(page);
    const hd = await hold(page, '**/__ut5404/d');
    const hg = await hold(page, '**/__ut5404/g2');
    await page.goto('/');
    await probe(page, '<button id="d" hx-delete="/__ut5404/d" hx-confirm="sure?" hx-target="#t5404">del</button>'
      + '<button id="g2" hx-get="/__ut5404/g2" hx-target="#t5404">get</button><div id="t5404"></div>');
    await page.locator('#d').click();
    await answerTillConfirm(page, 'confirm', 'sure?');
    await hd.inFlight;
    await expect(alertText(page)).toHaveText(TIMEOUT_TEXT, { timeout: 5000 });
    await page.locator('#g2').click();
    await hg.inFlight;
    await page.waitForTimeout(OVERRIDE_MS * 1.8);
    await expect(alertText(page)).toHaveText(TIMEOUT_TEXT);
    noNative();
  });

  test('A4 sale-screen tile tap: outcome, nothing stuck busy, a second tap sends a request', async ({ page }) => {
    await shortTimeout(page);
    const h = await hold(page, '**/api/pos/scan');
    await page.goto('/');
    const tile = page.locator('.btn-tile[data-code]').first();
    await expect(tile).toBeVisible();
    await tile.click();
    await h.inFlight;
    await expect(alertText(page)).toHaveText(TIMEOUT_TEXT, { timeout: 5000 });
    await expect(tile).not.toHaveClass(/htmx-request/);
    await expect(page.locator('#basket')).not.toHaveClass(/htmx-request/);
    expect(h.count()).toBe(1);
    await tile.click();
    await expect.poll(() => h.count(), { timeout: 3000 }).toBe(2);
    h.release();
  });

  test('A5 prompt-returning first POST that is held: outcome, no dialog, submit enabled', async ({ page }) => {
    await shortTimeout(page);
    const h = await hold(page, '**/__ut5404/prompt');
    await page.goto('/');
    await probe(page, '<form id="pf" hx-post="/__ut5404/prompt" hx-target="#t5404"><button id="ps" type="submit">ok</button></form><div id="t5404"></div>');
    await page.locator('#ps').click();
    await h.inFlight;
    await expect(alertText(page)).toHaveText(TIMEOUT_TEXT, { timeout: 5000 });
    await expect(page.locator('#ps')).toBeEnabled();
    await expect(page.locator('dialog[open]')).toHaveCount(0);
  });

  test('A10 prompt POST dropped (sendError) keeps the connection-dropped text, not the timeout text', async ({ page }) => {
    await page.route('**/__ut5463/drop', (r) => r.abort('failed'));
    await page.goto('/');
    await probe(page, '<form id="pd" hx-post="/__ut5463/drop" hx-target="#t5404" data-after-request="elevation-done"><button id="pds" type="submit">ok</button></form><div id="t5404"></div>');
    await page.locator('#pds').click();
    await expect(alertText(page)).toHaveText(OUTCOME, { timeout: 5000 });
    await expect(alertText(page)).not.toHaveText(TIMEOUT_TEXT);
  });

  test('A11 #pos-alert carries the timeout text; a later server alert replaces it', async ({ page }) => {
    await shortTimeout(page);
    await page.goto('/');
    expect(await page.locator('#pos-alert').getAttribute('data-msg-outcome-timeout')).toBe(TIMEOUT_TEXT);
    const h = await hold(page, '**/__ut5463/x');
    await page.route('**/__ut5463/err', (r) => r.fulfill({ status: 500, contentType: 'text/plain', body: 'boom' }));
    await probe(page, '<button id="p" hx-post="/__ut5463/x" hx-target="#t5404">go</button><button id="e" hx-post="/__ut5463/err" hx-target="#t5404">err</button><div id="t5404"></div>');
    await page.locator('#p').click();
    await h.inFlight;
    await expect(alertText(page)).toHaveText(TIMEOUT_TEXT, { timeout: 5000 });
    const srv = await page.locator('#pos-alert').getAttribute('data-msg-server');
    await page.locator('#e').click();
    await expect(alertText(page)).toHaveText(srv!, { timeout: 5000 });
  });

  test('A6 background, boosted, multipart, listed-path and opted-out requests are not timed', async ({ page }) => {
    await shortTimeout(page);
    for (const g of ['**/__ut5404/load', '**/__ut5404/every', '**/__ut5404/custom', '**/api/backup/now',
      '**/__ut5404/m', '**/__ut5404/nt', '**/__ut5404/boost']) {
      await page.route(g, (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<span>ok</span>' }));
    }
    await page.goto('/');
    await recordTimeouts(page);
    await probe(page,
      '<div id="t5404"></div>'
      + '<div hx-get="/__ut5404/load" hx-trigger="load" hx-target="#t5404"></div>'
      + '<div hx-get="/__ut5404/every" hx-trigger="every 1s" hx-target="#t5404"></div>'
      + '<div hx-get="/__ut5404/custom" hx-trigger="ut5404 from:body" hx-target="#t5404"></div>'
      + '<button id="bk" hx-post="/api/backup/now" hx-target="#t5404">b</button>'
      + '<button id="mp" hx-post="/__ut5404/m" hx-encoding="multipart/form-data" hx-target="#t5404">m</button>'
      + '<div data-ut-no-timeout><button id="nt" hx-post="/__ut5404/nt" hx-target="#t5404">n</button></div>'
      + '<div hx-boost="true"><a id="bo" href="/__ut5404/boost" hx-target="#t5404" hx-push-url="false">x</a></div>');
    await page.evaluate(() => document.body.dispatchEvent(new CustomEvent('ut5404')));
    for (const id of ['bk', 'mp', 'nt', 'bo']) await page.locator('#' + id).click();
    for (const p of ['/__ut5404/load', '/__ut5404/every', '/__ut5404/custom', '/api/backup/now',
      '/__ut5404/m', '/__ut5404/nt', '/__ut5404/boost']) {
      await expect.poll(() => xt(page, p), { message: p }).toBe(0);
    }
    expect(await page.evaluate(() => (window as any).htmx.config.timeout)).toBe(0);
  });

  test('A6b a held multipart request outlasting the override raises no timeout', async ({ page }) => {
    await shortTimeout(page);
    const h = await hold(page, '**/__ut5404/mh');
    await page.goto('/');
    await probe(page, '<button id="mp" hx-post="/__ut5404/mh" hx-encoding="multipart/form-data" hx-target="#t5404">m</button><div id="t5404"></div>');
    await page.evaluate(() => {
      const w = window as any; w.__to = 0;
      document.body.addEventListener('htmx:timeout', () => { w.__to++; });
    });
    await page.locator('#mp').click();
    await h.inFlight;
    await page.waitForTimeout(OVERRIDE_MS * 2.5);
    expect(await page.evaluate(() => (window as any).__to)).toBe(0);
    await expect(page.locator('#pos-alert')).toBeHidden();
  });

  // Review: tender/refund/kiosk checkout wait on a card terminal (server
  // payment gate alone is 30 s) and a join replaces local data -- a client
  // abort would be a false "may not have been done". Query string and a
  // relative URL must still match; a sibling path must still be timed.
  test('A8 payment and join paths stay unbounded (query/relative URL too); a sibling is timed', async ({ page }) => {
    await shortTimeout(page);
    await page.route('**/api/**', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<span>ok</span>' }));
    await page.goto('/');
    await recordTimeouts(page);
    const cases: Array<[string, string]> = [
      ['/api/pos/tender?x=1', '/api/pos/tender?x=1'],
      ['/api/refund', '/api/refund'],
      ['/api/self-order/checkout', '/api/self-order/checkout'],
      ['/api/sync/join', '/api/sync/join'],
      ['api/setup/join', 'api/setup/join'],
    ];
    let html = '<div id="t5404"></div><button id="sib" hx-post="/api/pos/scan" hx-target="#t5404">s</button>';
    cases.forEach(([url], i) => { html += `<button id="u${i}" hx-post="${url}" hx-target="#t5404">u</button>`; });
    await probe(page, html);
    for (let i = 0; i < cases.length; i++) await page.locator('#u' + i).click();
    await page.locator('#sib').click();
    for (const [, reqPath] of cases) {
      await expect.poll(() => xt(page, reqPath), { message: reqPath }).toBe(0);
    }
    await expect.poll(() => xt(page, '/api/pos/scan')).toBe(OVERRIDE_MS);
  });

  // ut-docs#5454: a prompt form (data-after-request="elevation-done") that
  // posts to a listed path is exempt too -- one predicate for every leg. A
  // prompt form to a sibling path is still timed (#5335).
  test('A9 prompt forms to payment and join paths stay unbounded; a sibling prompt is timed', async ({ page }) => {
    await shortTimeout(page);
    await page.route('**/api/**', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<span>ok</span>' }));
    await page.goto('/');
    await recordTimeouts(page);
    const cases = ['/api/pos/tender?x=1', '/api/refund', '/api/self-order/checkout', '/api/sync/join', 'api/setup/join'];
    let html = '<div id="t5454"></div>'
      + '<form id="fsib" hx-post="/api/pos/remove" hx-target="#t5454" data-after-request="elevation-done"><button id="bsib">s</button></form>';
    cases.forEach((url, i) => {
      html += `<form id="f${i}" hx-post="${url}" hx-target="#t5454" data-after-request="elevation-done"><button id="b${i}">s</button></form>`;
    });
    await probe(page, html);
    for (let i = 0; i < cases.length; i++) await page.locator('#b' + i).click();
    await page.locator('#bsib').click();
    for (const reqPath of cases) {
      await expect.poll(() => xt(page, reqPath), { message: reqPath }).toBe(0);
    }
    await expect.poll(() => xt(page, '/api/pos/remove')).toBe(OVERRIDE_MS);
  });

  test('A7 default timeout is 30000 and the getters agree', async ({ page }) => {
    await page.route('**/__ut5404/def', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: 'ok' }));
    await page.goto('/');
    await recordTimeouts(page);
    await probe(page, '<button id="df" hx-post="/__ut5404/def" hx-target="#t5404">d</button><div id="t5404"></div>');
    await page.locator('#df').click();
    await expect.poll(() => xt(page, '/__ut5404/def')).toBe(30000);
    const same = await page.evaluate(() => (window as any).utRequestTimeoutMs() === (window as any).utPromptTimeoutMs()
      && (window as any).utRequestTimeoutMs() === 30000);
    expect(same).toBe(true);
  });
});
