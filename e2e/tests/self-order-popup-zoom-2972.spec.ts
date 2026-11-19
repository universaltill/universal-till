import { test, expect, type Page, type BrowserContext } from './fixtures';
import { watchConsole } from './helpers';
import { WORKER_TILL_EFFECTS_LEVEL } from './worker-till';

// ut-docs#2972 / ADR-0122 §5: the self-order kiosk's picker and payment
// dialog (#selforder-modal, a modal <dialog> opened by inline-actions.js's
// show-modal) get the SAME shared popup zoom as the till: web/public/
// ut-motion.js, loaded first and synchronously by the kiosk page. Open =
// grow from the tapped tile / Checkout button; close = the same element
// shrinks back (data-ut-closing), and only AFTER the shrink does the dialog's
// data-ut-clear-on-close empty its markup (close-clear no longer empties it
// at the close site). Light (ADR-0119) and reduced motion: no animation, no
// closing re-show, still cleared. Motion is never allowed to cost a tap
// (ADR-0122 §7). The static half is internal/pages/popup_zoom_guard_test.go;
// the till half is popup-zoom-2944.spec.ts.
//
// The e2e suite is a reduced-motion user (fixtures.ts), so each test
// emulates 'no-preference' first -- the Light test too, so it cannot pass
// vacuously.

async function setLevel(page: Page, level: string) {
  const res = await page.request.post('/api/settings/effects-level', { form: { level } });
  expect(res.status(), `POST effects-level=${level}`).toBe(204);
}

type Rec = { id: string; cls: string; first: string; opacity: number; fill: string; duration: number; box: { l: number; t: number; w: number; h: number } };
type W = { __pz: Rec[]; __closingSeen: string[]; __closingKids: number[] };

// Record every transform Element.animate() call on a popup (a <dialog> or
// the bug-report panel): its first keyframe, timing and the element's own
// (untransformed) box when the call was made. Also log every element that
// ever gets data-ut-closing.
async function instrument(page: Page) {
  await page.evaluate(() => {
    const w = window as unknown as W;
    w.__pz = [];
    w.__closingSeen = [];
    w.__closingKids = [];
    const orig = Element.prototype.animate;
    Element.prototype.animate = function (this: Element, kf: Keyframe[] | PropertyIndexedKeyframes | null, opts?: number | KeyframeAnimationOptions) {
      if ((this.tagName === 'DIALOG' || this.classList.contains('bugreport-panel')) && Array.isArray(kf) && 'transform' in kf[0]) {
        const r = this.getBoundingClientRect();
        const o = (typeof opts === 'object' ? opts : {}) as KeyframeAnimationOptions;
        w.__pz.push({
          id: this.id, cls: this.className, first: String(kf[0].transform), opacity: Number(kf[0].opacity),
          fill: String(o.fill), duration: Number(o.duration), box: { l: r.left, t: r.top, w: r.width, h: r.height },
        });
      }
      return orig.call(this, kf, opts as KeyframeAnimationOptions);
    };
    new MutationObserver((ms) => {
      for (const m of ms) {
        const el = m.target as Element;
        if (el.hasAttribute('data-ut-closing')) {
          w.__closingSeen.push(el.id || el.className);
          // What the shrink shows: an emptied dialog would be a blank card.
          w.__closingKids.push(el.childElementCount);
        }
      }
    }).observe(document.body, { attributes: true, attributeFilter: ['data-ut-closing'], subtree: true });
  });
}

const recs = (page: Page) => page.evaluate(() => (window as unknown as W).__pz);

function parse(first: string) {
  const m = /^translate\((-?[\d.e-]+)px, (-?[\d.e-]+)px\) scale\(([\d.e-]+)\)/.exec(first);
  expect(m, `first keyframe is a uniform translate+scale, got ${first}`).not.toBeNull();
  return { dx: Number(m![1]), dy: Number(m![2]), s: Number(m![3]) };
}

// One item with one real modifier group/option as a sell-screen shortcut,
// so its tile hx-gets /ui/pos/modifiers and the response showModal()s
// #modifier-modal -- the card's own example (sale screen -> tap an item ->
// variants/modifiers popup). Trimmed from order-type-prompt-placement-2282.
const RUN = Date.now().toString(36).toUpperCase();
type Seeded = { itemId: string; name: string; barcode: string; optionName: string };
let seeded: Seeded | null = null;

let seq = 0;
async function seedModifierItem(page: Page): Promise<Seeded> {
  // A fresh item per test: afterEach deactivates the previous one, and a
  // re-import of the same SKU would land on that inactive row.
  const tag = `${RUN}${++seq}`;
  const name = `Pz2972 Mod ${tag}`;
  const barcode = `PZ2972BC-${tag}`;
  const csv = `Name,SKU,Barcode,Price,Category,In stock\n${name},PZ2972${tag},${barcode},1.00,Pz2972 Cat,1\n`;
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: `import-pz2972-${RUN}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/catalog');
  const itemId = (await page.locator(`.catalog-row[data-name="${name}"]`).first().getAttribute('data-id'))!;
  const groupName = `Size ${tag}`;
  expect((await page.request.post('/api/catalog/modifier-group', { form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' } })).ok()).toBe(true);
  const html = await (await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`)).text();
  const at = html.indexOf(`>${groupName}<`);
  expect(at).toBeGreaterThan(-1);
  const ids = [...html.slice(0, at).matchAll(/data-group-id="([^"]*)"/g)];
  const groupId = ids[ids.length - 1][1];
  const optionName = `Large ${tag}`;
  expect((await page.request.post('/api/catalog/modifier-option', { form: { groupId, itemId, name: optionName } })).ok()).toBe(true);
  return { itemId, name, barcode, optionName };
}


type Kiosk = { ctx: BrowserContext; k: Page };
let kiosk: Kiosk | null = null;

// A fresh kiosk device (touch, 1024x600) on /self-order/shop at `level`.
async function openKiosk(page: Page, browser: import('@playwright/test').Browser, baseURL: string | undefined, level: string, motion: 'no-preference' | 'reduce' = 'no-preference') {
  if (!seeded) seeded = await seedModifierItem(page);
  await setLevel(page, level);
  const ctx = await browser.newContext({ baseURL, hasTouch: true, viewport: { width: 1024, height: 600 } });
  const k = await ctx.newPage();
  kiosk = { ctx, k };
  await k.emulateMedia({ reducedMotion: motion });
  // A walk-up basket may hold leftovers from another spec.
  await k.goto('/self-order/shop');
  await expect(k.locator('html')).toHaveClass(new RegExp(`(^|\\s)fx-${level}(\\s|$)`));
  await instrument(k);
  const chip = k.locator('.cat-chip', { hasText: 'Pz2972 Cat' });
  await chip.click();
  const tile = k.locator('.selforder-tile:visible', { hasText: seeded.name });
  await expect(tile).toBeVisible();
  return { k, tile };
}

const modal = (k: Page) => k.locator('#selforder-modal');
async function tapTile(k: Page, tile: ReturnType<Page['locator']>) {
  await Promise.all([
    k.waitForResponse((r) => r.url().includes('/api/self-order/modifiers')),
    tile.tap(),
  ]);
  await expect(modal(k)).toHaveJSProperty('open', true);
}
const kState = (k: Page) => k.evaluate(() => {
  const d = document.getElementById('selforder-modal') as HTMLDialogElement;
  return {
    open: d.open,
    closing: d.hasAttribute('data-ut-closing'),
    inert: d.hasAttribute('inert'),
    ariaHidden: d.getAttribute('aria-hidden'),
    kids: d.childElementCount,
    modal: d.matches(':modal'),
    running: d.getAnimations().filter((a) => a.playState === 'running'
      && ((a.effect as KeyframeEffect | null)?.getKeyframes() || []).some((x) => 'transform' in x)).length,
  };
});
const cssMs = (k: Page, prop: string) => k.evaluate((p) => parseFloat(getComputedStyle(document.documentElement).getPropertyValue(p)), prop);
const closingLog = (k: Page) => k.evaluate(() => {
  const w = window as unknown as W;
  return { seen: w.__closingSeen.filter((x) => x === 'selforder-modal').length, kids: w.__closingKids.slice() };
});

async function emptyBasket(k: Page) {
  const remove = k.locator('#selforder-cart button[hx-post="/api/self-order/remove"]');
  while (await remove.count()) {
    await Promise.all([
      k.waitForResponse((r) => r.url().includes('/api/self-order/remove')),
      remove.first().click(),
    ]);
  }
}

test.describe('self-order kiosk popup zoom (ut-docs#2972, ADR-0122 §5)', () => {
  test.afterEach(async ({ page }) => {
    if (kiosk) {
      await emptyBasket(kiosk.k).catch(() => {});
      await kiosk.ctx.close();
      kiosk = null;
    }
    await setLevel(page, WORKER_TILL_EFFECTS_LEVEL);
    if (seeded) {
      await page.request.post('/api/catalog/item/deactivate', { form: { id: seeded.itemId } });
      seeded = null;
    }
  });

  test('Full: a tile tap grows the modal picker from the tile; the page stays modal and chrome-free', async ({ page, browser, baseURL }) => {
    const { k, tile } = await openKiosk(page, browser, baseURL, 'full');
    const assertClean = watchConsole(k);
    const src = (await tile.boundingBox())!;
    await tapTile(k, tile);

    const all = await recs(k);
    const mine = all.filter((r) => r.id === 'selforder-modal');
    expect(mine, `exactly one transform zoom on #selforder-modal, got ${JSON.stringify(all)}`).toHaveLength(1);
    const rec = mine[0];
    const { dx, dy, s } = parse(rec.first);
    expect(s).toBeLessThan(1);
    expect(Math.abs(rec.box.l + rec.box.w / 2 + dx - (src.x + src.width / 2))).toBeLessThan(1.5);
    expect(Math.abs(rec.box.t + rec.box.h / 2 + dy - (src.y + src.height / 2))).toBeLessThan(1.5);
    expect(rec.opacity).toBeCloseTo(0.4, 2);
    expect(rec.fill).toBe('none');
    expect(rec.duration).toBe(await cssMs(k, '--ut-zoom-small-ms'));
    // Interactive at once, and still a real modal (#4324): nothing of the
    // till's chrome exists on this page.
    expect((await kState(k)).modal).toBe(true);
    await expect(modal(k).locator('.modifier-option', { hasText: seeded!.optionName })).toBeVisible();
    await expect(k.locator('.statusbar, .session-lock')).toHaveCount(0);
    await k.waitForTimeout(450);
    expect(await modal(k).evaluate((el) => getComputedStyle(el).transform)).toBe('none');
    await k.keyboard.press('Escape');
    await expect(modal(k)).toBeHidden({ timeout: 5_000 });
    assertClean();
  });

  for (const how of ['Cancel', 'Add', 'Escape'] as const) {
    test(`Full: ${how} shrinks the FILLED picker back into the tile, then clears it; the next open is fresh`, async ({ page, browser, baseURL }) => {
      const { k, tile } = await openKiosk(page, browser, baseURL, 'full');
      const assertClean = watchConsole(k);
      await tapTile(k, tile);
      await k.waitForTimeout(450);
      const closeMs = 1500;
      await k.evaluate((ms) => document.documentElement.style.setProperty('--ut-zoom-close-ms', ms + 'ms'), closeMs);
      const m = modal(k);
      if (how === 'Cancel') {
        await m.locator('.modifier-actions .btn.secondary').click();
      } else if (how === 'Add') {
        await m.locator('.modifier-option', { hasText: seeded!.optionName }).click();
        await Promise.all([
          k.waitForResponse((r) => r.url().includes('/api/self-order/scan-with-modifiers') && r.ok()),
          m.locator('.modifier-actions button[type=submit]').click(),
        ]);
      } else {
        await k.keyboard.press('Escape');
      }
      await expect.poll(async () => (await closingLog(k)).seen).toBe(1);
      const c = await closingLog(k);
      expect(c.kids.every((n) => n > 0), `closing picker children (never a blank card): ${JSON.stringify(c.kids)}`).toBe(true);
      // While it shrinks: re-shown, inert, aria-hidden, one running zoom; the
      // page is already interactive (focus is back outside the dialog).
      const mid = await kState(k);
      expect(mid).toMatchObject({ open: false, closing: true, inert: true, ariaHidden: 'true' });
      expect(mid.kids).toBeGreaterThan(0);
      expect(mid.running).toBe(1);
      // Ends within closeMs + 250, then the markup is gone (AC5).
      await expect.poll(async () => (await kState(k)).closing, { timeout: closeMs + 2_000 }).toBe(false);
      await expect.poll(async () => (await kState(k)).kids, { timeout: 2_000 }).toBe(0);
      const end = await kState(k);
      expect(end).toMatchObject({ open: false, closing: false, inert: false, ariaHidden: null });

      // A second tap never shows the previous content: exactly one picker.
      await k.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-close-ms'));
      await tapTile(k, tile);
      await expect(m.locator('.modifier-picker')).toHaveCount(1);
      await expect(m.locator('h3')).toHaveText(seeded!.name);
      await k.keyboard.press('Escape');
      await expect(m).toBeHidden({ timeout: 5_000 });
      assertClean();
    });
  }

  test('Full: reopening the picker during the closing shrink shows the new content and the old shrink does not clear it', async ({ page, browser, baseURL }) => {
    const { k, tile } = await openKiosk(page, browser, baseURL, 'full');
    const assertClean = watchConsole(k);
    await tapTile(k, tile);
    await k.waitForTimeout(450);
    const closeMs = 1500;
    await k.evaluate((ms) => document.documentElement.style.setProperty('--ut-zoom-close-ms', ms + 'ms'), closeMs);
    await k.keyboard.press('Escape');
    await expect.poll(async () => (await kState(k)).closing).toBe(true);
    // Tap the tile again while the old picker still shrinks.
    await tapTile(k, tile);
    const m = modal(k);
    await m.locator('.modifier-picker').evaluate((el) => el.setAttribute('data-fresh', '1'));
    // Outlast the old shrink's finish and its backstop timer.
    await k.waitForTimeout(closeMs + 500);
    const st = await kState(k);
    expect(st).toMatchObject({ open: true, closing: false, inert: false, ariaHidden: null });
    expect(st.kids, 'the reopened content survives the old shrink').toBeGreaterThan(0);
    await expect(m.locator('.modifier-picker')).toHaveCount(1);
    await expect(m.locator('.modifier-picker[data-fresh]')).toHaveCount(1);
    await expect(m.locator('h3')).toHaveText(seeded!.name);
    await k.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-close-ms'));
    await k.keyboard.press('Escape');
    await expect(m).toBeHidden({ timeout: 5_000 });
    assertClean();
  });

  test('Full: content htmx-swapped into the dialog during the shrink is not removed when it ends', async ({ page, browser, baseURL }) => {
    const { k, tile } = await openKiosk(page, browser, baseURL, 'full');
    const assertClean = watchConsole(k);
    const url = (await tile.getAttribute('hx-get'))!;
    await tapTile(k, tile);
    await k.waitForTimeout(450);
    const closeMs = 1500;
    await k.evaluate((ms) => document.documentElement.style.setProperty('--ut-zoom-close-ms', ms + 'ms'), closeMs);
    await k.keyboard.press('Escape');
    await expect.poll(async () => (await kState(k)).closing).toBe(true);
    // A response lands in the still-painted, closed dialog.
    await Promise.all([
      k.waitForResponse((r) => r.url().includes('/api/self-order/modifiers')),
      k.evaluate((u) => (window as unknown as { htmx: { ajax: (m: string, u: string, o: object) => Promise<unknown> } })
        .htmx.ajax('GET', u, { target: '#selforder-modal', swap: 'innerHTML' }), url),
    ]);
    const m = modal(k);
    await expect(m.locator('.modifier-picker')).toHaveCount(1);
    await m.locator('.modifier-picker').evaluate((el) => el.setAttribute('data-fresh', '1'));
    await expect.poll(async () => (await kState(k)).closing, { timeout: closeMs + 2_000 }).toBe(false);
    await k.waitForTimeout(500);
    const end = await kState(k);
    expect(end.open).toBe(false);
    expect(end.kids, 'content swapped in during the shrink is kept').toBeGreaterThan(0);
    await expect(m.locator('.modifier-picker[data-fresh]')).toHaveCount(1);
    await k.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-close-ms'));
    assertClean();
  });

  test('Balanced: the same zoom at 200 ms, closing at 240 ms (ADR-0123)', async ({ page, browser, baseURL }) => {
    const { k, tile } = await openKiosk(page, browser, baseURL, 'balanced');
    await tapTile(k, tile);
    const rec = (await recs(k)).find((r) => r.id === 'selforder-modal');
    expect(rec?.duration).toBe(200);
    await k.waitForTimeout(300);
    const t = await k.evaluate(async () => {
      const d = document.getElementById('selforder-modal') as HTMLDialogElement;
      d.close();
      await new Promise((r) => setTimeout(r, 50)); // the close event is a queued task
      const a = d.getAnimations().find((x) => ((x.effect as KeyframeEffect).getKeyframes()).some((kf) => 'transform' in kf))!;
      return { duration: Number(a.effect!.getTiming().duration), kids: d.childElementCount };
    });
    expect(t.duration).toBe(240);
    expect(t.kids).toBeGreaterThan(0);
    await expect.poll(async () => (await kState(k)).kids, { timeout: 3_000 }).toBe(0);
  });

  for (const c of [
    { name: 'Light', level: 'light', motion: 'no-preference' as const },
    { name: 'Full under reduced motion', level: 'full', motion: 'reduce' as const },
  ]) {
    test(`${c.name}: no animation, no closing re-show; open and close are instant and the markup is still cleared`, async ({ page, browser, baseURL }) => {
      const { k, tile } = await openKiosk(page, browser, baseURL, c.level, c.motion);
      await tapTile(k, tile);
      await k.waitForTimeout(350);
      expect(await recs(k)).toHaveLength(0);
      await k.locator('#selforder-modal .modifier-actions .btn.secondary').click();
      await expect(modal(k)).toBeHidden();
      await expect.poll(async () => (await kState(k)).kids).toBe(0);
      await k.waitForTimeout(350);
      expect(await k.evaluate(() => (window as unknown as W).__closingSeen)).toHaveLength(0);
      expect(await recs(k)).toHaveLength(0);
      expect(await k.evaluate(() => document.getAnimations()
        .filter((a) => ((a.effect as KeyframeEffect | null)?.getKeyframes() || []).some((x) => 'transform' in x)).length)).toBe(0);
    });
  }

  test('Full: Checkout zooms the payment dialog from the button; the in-place swap adds no second animation', async ({ page, browser, baseURL }) => {
    const { k, tile } = await openKiosk(page, browser, baseURL, 'full');
    await tapTile(k, tile);
    await k.waitForTimeout(450);
    const m = modal(k);
    await m.locator('.modifier-option', { hasText: seeded!.optionName }).click();
    await Promise.all([
      k.waitForResponse((r) => r.url().includes('/api/self-order/scan-with-modifiers') && r.ok()),
      m.locator('.modifier-actions button[type=submit]').click(),
    ]);
    await expect(m).toBeHidden({ timeout: 5_000 });
    await k.waitForTimeout(500);
    await k.evaluate(() => { (window as unknown as W).__pz = []; });
    const btn = k.locator('#selforder-cart .selforder-checkout');
    await expect(btn).toBeVisible();
    const src = (await btn.boundingBox())!;
    await Promise.all([
      k.waitForResponse((r) => r.url().includes('/api/self-order/checkout')),
      btn.tap(),
    ]);
    await expect(m).toHaveJSProperty('open', true);
    const mine = (await recs(k)).filter((r) => r.id === 'selforder-modal');
    expect(mine).toHaveLength(1);
    const { dx, dy } = parse(mine[0].first);
    expect(Math.abs(mine[0].box.l + mine[0].box.w / 2 + dx - (src.x + src.width / 2))).toBeLessThan(1.5);
    expect(Math.abs(mine[0].box.t + mine[0].box.h / 2 + dy - (src.y + src.height / 2))).toBeLessThan(1.5);
    await k.waitForTimeout(450);
    expect((await recs(k)).filter((r) => r.id === 'selforder-modal')).toHaveLength(1);
    await k.keyboard.press('Escape');
    await expect(m).toBeHidden({ timeout: 5_000 });
  });

  test('a touch tap during the opening zoom lands on the picker control once', async ({ page, browser, baseURL }) => {
    const { k, tile } = await openKiosk(page, browser, baseURL, 'full');
    // Learn where the option sits once the popup is still.
    await tapTile(k, tile);
    await k.waitForTimeout(450);
    const option = modal(k).locator('.modifier-option', { hasText: seeded!.optionName });
    const box = (await option.boundingBox())!;
    await k.keyboard.press('Escape');
    await expect(modal(k)).toBeHidden({ timeout: 5_000 });
    await expect.poll(async () => (await kState(k)).kids).toBe(0);

    // A slow zoom, so the press provably lands while the popup moves.
    await k.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '3000ms'));
    await tapTile(k, tile);
    await k.waitForFunction(() => {
      const d = document.getElementById('selforder-modal');
      return !!d && d.getAnimations().some((a) => a.playState === 'running');
    }, undefined, { polling: 'raf', timeout: 5_000 });
    const x = box.x + box.width / 2, y = box.y + box.height / 2;
    const under = await k.evaluate(([px, py]) => {
      const el = document.elementFromPoint(px, py);
      return !!el && !!el.closest('.modifier-option');
    }, [x, y]);
    expect(under, 'mid-zoom, the option is not yet under its final position').toBe(false);
    await k.touchscreen.tap(x, y);
    await expect(option.locator('input')).toBeChecked();
    expect((await kState(k)).running).toBe(0);
    await k.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
    await k.keyboard.press('Escape');
    await expect(modal(k)).toBeHidden({ timeout: 5_000 });
  });
});
