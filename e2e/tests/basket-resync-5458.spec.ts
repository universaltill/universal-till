import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';

// ut-docs#5458 (from the #5404 UX gate): a sale-screen action that times out
// with the sticky "may or may not have been done" text leaves #basket on its
// pre-tap content while the server may well have applied it. After such a
// timeout (non-GET, /api/pos/*, not a prompt) the client re-fetches
// GET /ui/basket into #basket; a newer tap always wins over that re-fetch;
// its failure is silent. A tapped tile also shows a busy outline (aria-busy,
// after 300 ms) while its request is pending. 30 s is overridden to 1 s
// through window.__utPromptTimeoutMs, as in action-timeout-5404.spec.ts.

const OUTCOME = 'The connection dropped before the till replied, so this may or may not have been done. Check before you try again.';
const OVERRIDE_MS = 1000;

async function shortTimeout(page: Page) {
  await page.addInitScript((ms) => { (window as any).__utPromptTimeoutMs = ms; }, OVERRIDE_MS);
}

const pendingReleases: Array<() => void> = [];
test.afterEach(async ({ page }) => {
  for (const r of pendingReleases.splice(0)) r();
  await page.request.post('/api/pos/reset');
});

type Late = { status: number; contentType: string; body: string };

// Holds every request matching glob. forward=true performs the real request
// first (the server applies it) and holds only the DELIVERY of its response;
// release() delivers that real response, release(late) delivers `late`.
async function hold(page: Page, glob: string, forward = false) {
  let n = 0;
  let release!: (l?: Late) => void;
  const gate = new Promise<Late | undefined>((r) => { release = r; });
  pendingReleases.push(() => release());
  let held!: () => void;
  const inFlight = new Promise<void>((r) => { held = r; });
  await page.route(glob, async (route: Route) => {
    n++;
    const real = forward ? await route.fetch() : undefined;
    held();
    const late = await gate;
    try {
      if (late) await route.fulfill(late);
      else if (real) await route.fulfill({ response: real });
      else await route.abort();
    } catch { /* already finished */ }
  });
  return { count: () => n, release, inFlight };
}

function countResyncs(page: Page) {
  const urls: string[] = [];
  page.on('request', (r) => { if (new URL(r.url()).pathname === '/ui/basket') urls.push(r.url()); });
  return () => urls.length;
}

const alertText = (page: Page) => page.locator('#pos-alert .notice-text');
const scanTiles = (page: Page) => page.locator('.btn-tile[hx-post="/api/pos/scan"]');
const lines = (page: Page) => page.locator('#basket').getAttribute('data-lines-count');
const basketHtml = (page: Page) => page.locator('#basket').evaluate((e) => e.outerHTML);

test.describe('ut-docs#5458 basket re-sync after an outcome-unknown timeout', () => {
  test('AC1 server applied, reply held past the timeout: alert, exactly one /ui/basket, basket shows the line', async ({ page }) => {
    await shortTimeout(page);
    const resyncs = countResyncs(page);
    const h = await hold(page, '**/api/pos/scan', true);
    await page.goto('/');
    const tile = scanTiles(page).first();
    const name = await tile.getAttribute('data-name');
    expect(await lines(page)).toBe('0');
    await tile.click();
    await h.inFlight;
    await expect(alertText(page)).toHaveText(OUTCOME, { timeout: 5000 });
    await expect.poll(resyncs, { timeout: 5000 }).toBe(1);
    await expect(page.locator('#basket')).toContainText(name!);
    expect(await lines(page)).toBe('1');
    await expect(alertText(page)).toHaveText(OUTCOME); // sticky, not cleared by the refresh
    expect(resyncs()).toBe(1);
  });

  test('AC2 server never got it: re-fetch shows the pre-tap basket, alert stays', async ({ page }) => {
    await shortTimeout(page);
    const resyncs = countResyncs(page);
    const h = await hold(page, '**/api/pos/scan', false);
    await page.goto('/');
    await scanTiles(page).first().click();
    await h.inFlight;
    await expect(alertText(page)).toHaveText(OUTCOME, { timeout: 5000 });
    await expect.poll(resyncs, { timeout: 5000 }).toBe(1);
    await page.waitForTimeout(400);
    expect(await lines(page)).toBe('0');
    await expect(alertText(page)).toHaveText(OUTCOME);
  });

  test('AC3 the late original reply, released after the re-fetch, does not change #basket', async ({ page }) => {
    await shortTimeout(page);
    const resyncs = countResyncs(page);
    const h = await hold(page, '**/api/pos/scan', true);
    await page.goto('/');
    await scanTiles(page).first().click();
    await h.inFlight;
    await expect.poll(resyncs, { timeout: 5000 }).toBe(1);
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '1');
    const before = await basketHtml(page);
    h.release({ status: 200, contentType: 'text/html', body: '<div id="basket" data-lines-count="9">late</div>' });
    await page.waitForTimeout(500);
    expect(await basketHtml(page)).toBe(before);
  });

  test('AC4 no re-fetch for a timed-out GET, a non-/api/pos POST, or a prompt form', async ({ page }) => {
    await shortTimeout(page);
    const resyncs = countResyncs(page);
    await page.addInitScript(() => {
      (window as any).__t5458 = 0;
      document.addEventListener('htmx:timeout', () => { (window as any).__t5458++; });
    });
    const hg = await hold(page, '**/api/pos/__ut5458/g');
    const hp = await hold(page, '**/__ut5458/admin');
    const hf = await hold(page, '**/api/pos/__ut5458/prompt');
    await page.goto('/');
    await page.evaluate(() => {
      const host = document.createElement('div');
      host.innerHTML = '<button id="g" hx-get="/api/pos/__ut5458/g" hx-target="#t5458">g</button>'
        + '<button id="p" hx-post="/__ut5458/admin" hx-target="#t5458">p</button>'
        + '<form data-after-request="elevation-done" hx-post="/api/pos/__ut5458/prompt" hx-target="#t5458"><button id="f" type="submit">f</button></form>'
        + '<div id="t5458"></div>';
      document.body.appendChild(host);
      (window as any).htmx.process(host);
    });
    for (const [id, h] of [['g', hg], ['p', hp], ['f', hf]] as const) {
      await page.locator('#' + id).click();
      await h.inFlight;
    }
    // all three really timed out (so the absence of a re-fetch is not vacuous)
    await expect.poll(() => page.evaluate(() => (window as any).__t5458), { timeout: 5000 }).toBe(3);
    await page.waitForTimeout(500);
    expect(resyncs()).toBe(0);
  });

  test('AC5 a re-fetch held in flight loses to a second tap that completes', async ({ page }) => {
    await shortTimeout(page);
    const resyncs = countResyncs(page);
    const hs = await hold(page, '**/api/pos/scan', false);
    await page.goto('/');
    const stale = await basketHtml(page); // empty basket
    const hb = await hold(page, '**/ui/basket', false);
    const failed: string[] = [];
    page.on('requestfailed', (r) => { if (new URL(r.url()).pathname === '/ui/basket') failed.push(r.url()); });
    await scanTiles(page).nth(0).click();
    await hs.inFlight;
    await expect.poll(resyncs, { timeout: 5000 }).toBe(1);
    await hb.inFlight;
    await page.unroute('**/api/pos/scan');
    const second = scanTiles(page).nth(1);
    const name = await second.getAttribute('data-name');
    await second.click();
    await expect(page.locator('#basket')).toContainText(name!);
    expect(await lines(page)).toBe('1');
    hb.release({ status: 200, contentType: 'text/html', body: stale });
    await page.waitForTimeout(500);
    expect(await lines(page)).toBe('1');
    await expect(page.locator('#basket')).toContainText(name!);
    expect(failed.length).toBe(1); // aborted by hx-sync="#basket:replace"
  });

  test('AC5b a re-fetch held in flight loses to a NON-synced #basket request (generation check)', async ({ page }) => {
    await shortTimeout(page);
    const resyncs = countResyncs(page);
    const hs = await hold(page, '**/api/pos/scan', false);
    await page.goto('/');
    const hb = await hold(page, '**/ui/basket', false);
    await page.route('**/api/pos/__ut5458/newer', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<i></i>' }));
    await scanTiles(page).nth(0).click();
    await hs.inFlight;
    await expect.poll(resyncs, { timeout: 5000 }).toBe(1);
    await hb.inFlight;
    // A newer #basket-targeted request WITHOUT hx-sync: nothing aborts the held
    // re-fetch, and (swap none) #basket is not replaced, so only the generation
    // check can stop the late re-fetch from swapping.
    await page.evaluate(() => (window as any).htmx.ajax('post', '/api/pos/__ut5458/newer', { target: '#basket', swap: 'none' }));
    hb.release({ status: 200, contentType: 'text/html', body: '<div id="basket" data-lines-count="9">stale5458</div>' });
    await page.waitForTimeout(600);
    expect(await lines(page)).toBe('0');
    await expect(page.locator('#basket')).not.toContainText('stale5458');
  });

  test('AC6 a failing re-fetch (500 and aborted) adds no alert and keeps the sticky text', async ({ page }) => {
    for (const mode of ['500', 'abort']) {
      await shortTimeout(page);
      const resyncs = countResyncs(page);
      await page.route('**/ui/basket', (r) => (mode === '500'
        ? r.fulfill({ status: 500, contentType: 'text/html', body: '<div id="bad5458">boom</div>' })
        : r.abort()));
      const h = await hold(page, '**/api/pos/scan', false);
      await page.goto('/');
      await scanTiles(page).first().click();
      await h.inFlight;
      await expect(alertText(page)).toHaveText(OUTCOME, { timeout: 5000 });
      await expect.poll(resyncs, { timeout: 5000 }).toBe(1);
      await page.waitForTimeout(500);
      await expect(alertText(page)).toHaveText(OUTCOME);
      await expect(page.locator('#bad5458')).toHaveCount(0); // #916 force-swap must skip the re-fetch
      await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '0');
      await expect(page.locator('#pos-alert')).toHaveAttribute('data-sticky', '1');
      await expect(page.locator('#pos-alert:not([hidden])')).toHaveCount(1);
      await page.unroute('**/ui/basket');
      await page.unroute('**/api/pos/scan');
    }
  });
});

test.describe('ut-docs#5458 tapped tile busy state', () => {
  test('AC7 held tap: the tile (only) is aria-busy with a visible outline; cleared on timeout and on a second tap', async ({ page }) => {
    await shortTimeout(page);
    const h = await hold(page, '**/api/pos/scan', false);
    await page.goto('/');
    const a = scanTiles(page).nth(0);
    const b = scanTiles(page).nth(1);
    const idle = await a.evaluate((e) => getComputedStyle(e).outlineStyle + '|' + getComputedStyle(e).outlineWidth);
    await a.click();
    await h.inFlight;
    await expect(a).toHaveAttribute('aria-busy', 'true', { timeout: 3000 });
    await expect(b).not.toHaveAttribute('aria-busy', 'true');
    const busy = await a.evaluate((e) => getComputedStyle(e).outlineStyle + '|' + getComputedStyle(e).outlineWidth);
    expect(busy).not.toBe(idle);
    // second tap aborts the first through hx-sync: A loses busy
    await b.click();
    await expect(a).not.toHaveAttribute('aria-busy', 'true');
    // and timeout clears B
    await expect(alertText(page)).toHaveText(OUTCOME, { timeout: 5000 });
    await expect(b).not.toHaveAttribute('aria-busy', 'true');
    await expect(a).not.toHaveAttribute('aria-busy', 'true');
  });

  test('AC7b release clears the busy state', async ({ page }) => {
    await shortTimeout(page);
    await page.addInitScript(() => { (window as any).__utPromptTimeoutMs = 20000; });
    const h = await hold(page, '**/api/pos/scan', true);
    await page.goto('/');
    const a = scanTiles(page).first();
    await a.click();
    await h.inFlight;
    await expect(a).toHaveAttribute('aria-busy', 'true', { timeout: 1500 });
    h.release();
    await expect(a).not.toHaveAttribute('aria-busy', 'true');
  });

  test('AC8 a fast tap never shows the busy state', async ({ page }) => {
    await page.goto('/');
    await page.evaluate(() => {
      const w = window as any; w.__busySeen = 0;
      new MutationObserver((ms) => { for (const m of ms) if ((m.target as Element).getAttribute('aria-busy') === 'true') w.__busySeen++; })
        .observe(document.body, { attributes: true, subtree: true, attributeFilter: ['aria-busy'] });
    });
    await scanTiles(page).first().click();
    await expect(page.locator('#basket')).toHaveAttribute('data-lines-count', '1');
    await page.waitForTimeout(600);
    expect(await page.evaluate(() => (window as any).__busySeen)).toBe(0);
    await expect(page.locator('.btn-tile[aria-busy]')).toHaveCount(0);
  });

  // The demo catalogue has no coloured scan tile, so one is made here.
  async function colourOne(page: Page) {
    await page.evaluate(() => {
      const t = document.querySelectorAll('.btn-tile[hx-post="/api/pos/scan"]')[1] as HTMLElement;
      t.classList.add('tile-colored'); t.style.setProperty('--tile-color', '#b45309'); t.id = 'colored5458';
    });
    return page.locator('#colored5458');
  }

  for (const mode of ['reduced-motion', 'fx-light']) {
    test(`AC9 ${mode}: busy outline still solid, no animation; coloured tile outline is white`, async ({ page }) => {
      await shortTimeout(page);
      await page.addInitScript(() => { (window as any).__utPromptTimeoutMs = 20000; });
      if (mode === 'reduced-motion') await page.emulateMedia({ reducedMotion: 'reduce' });
      const h = await hold(page, '**/api/pos/scan', false);
      await page.goto('/');
      if (mode === 'fx-light') await page.evaluate(() => document.documentElement.classList.add('fx-light'));
      const colored = await colourOne(page);
      const plain = scanTiles(page).first();
      for (const t of [plain, colored]) {
        await t.click();
        await expect(t).toHaveAttribute('aria-busy', 'true', { timeout: 1500 });
        const cs = await t.evaluate((e) => {
          const s = getComputedStyle(e);
          return { style: s.outlineStyle, anim: s.animationName, color: s.outlineColor };
        });
        expect(cs.style).toBe('solid');
        expect(cs.anim).toBe('none');
        if (t === colored) expect(cs.color).toBe('rgb(255, 255, 255)');
        await page.keyboard.press('Escape');
      }
      expect(h.count()).toBeGreaterThan(0);
    });
  }

  // M1 regression: motion allowed, phone width. The coloured tile has no
  // swatch there, so the pulse must use the focus token, never white.
  for (const [w, label] of [[360, 'compact'], [1280, 'wide']] as const) {
    test(`AC9m motion allowed, ${label} (${w}px): pulse colour and never fully transparent`, async ({ page }) => {
      await shortTimeout(page);
      await page.addInitScript(() => { (window as any).__utPromptTimeoutMs = 20000; });
      await page.setViewportSize({ width: w, height: 800 });
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      await hold(page, '**/api/pos/scan', false);
      await page.goto('/');
      const colored = await colourOne(page);
      await colored.click();
      await expect(colored).toHaveAttribute('aria-busy', 'true', { timeout: 1500 });
      // outlineColor may serialise as rgb() or color(srgb ...): normalise via a canvas.
      const samples: Array<{ anim: string; rgb: number[]; a: number }> = [];
      for (let i = 0; i < 12; i++) {
        samples.push(await colored.evaluate((e) => {
          const s = getComputedStyle(e);
          const c = document.createElement('canvas'); c.width = c.height = 1;
          const g = c.getContext('2d')!; g.clearRect(0, 0, 1, 1); g.fillStyle = s.outlineColor; g.fillRect(0, 0, 1, 1);
          const d = g.getImageData(0, 0, 1, 1).data; // premultiplied loss is fine for the white test
          return { anim: s.animationName, rgb: [d[0], d[1], d[2]], a: d[3] / 255 };
        }));
        await page.waitForTimeout(90);
      }
      for (const x of samples) {
        expect(x.anim).toBe(w === 360 ? 'ut-tile-busy' : 'ut-tile-busy-c');
        expect(x.a).toBeGreaterThanOrEqual(0.4);
      }
      const isWhite = (x: { rgb: number[] }) => x.rgb.every((v) => v > 250);
      expect(samples.some(isWhite)).toBe(w !== 360); // compact: focus token, never the white of a swatch tile
    });
  }
});
