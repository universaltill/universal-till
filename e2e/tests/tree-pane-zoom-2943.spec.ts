import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';
import { WORKER_TILL_EFFECTS_LEVEL } from './worker-till';

// ut-docs#2943 / ADR-0122 §4: a tree tap (the /items rail) zooms the
// swapped #items-panel out of the tapped rail item -- a TRANSIENT Web
// Animations transform (fill 'none'), finished on any tap and before any
// popup opens, so a position:fixed dialog in the pane is never shown inside
// a transformed ancestor (the #2338 hazard). Light (ADR-0119) = no
// animation at all. The static half is internal/pages/transitions_test.go.
//
// The e2e suite runs as a reduced-motion user (fixtures.ts), under which
// motion is off at every level: every test that expects motion emulates
// 'no-preference' first, and the Light test does too, so it cannot pass
// vacuously.

async function setLevel(page: Page, level: string) {
  const res = await page.request.post('/api/settings/effects-level', { form: { level } });
  expect(res.status(), `POST effects-level=${level}`).toBe(204);
}

type Rec = { first: string; opacity: number; fill: string; duration: number; pane: { l: number; t: number; w: number; h: number } };

// Record every Element.animate() call on the pane: its first keyframe and
// timing, plus the pane's own (untransformed) box when the call was made.
async function instrument(page: Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __zooms: Rec[] };
    w.__zooms = [];
    const orig = Element.prototype.animate;
    Element.prototype.animate = function (this: Element, kf: Keyframe[] | PropertyIndexedKeyframes | null, opts?: number | KeyframeAnimationOptions) {
      if (['items-panel', 'admin-panel', 'manual-panel'].includes(this.id) && Array.isArray(kf)) {
        const r = this.getBoundingClientRect();
        const o = (typeof opts === 'object' ? opts : {}) as KeyframeAnimationOptions;
        w.__zooms.push({
          first: String(kf[0].transform), opacity: Number(kf[0].opacity), fill: String(o.fill), duration: Number(o.duration),
          pane: { l: r.left, t: r.top, w: r.width, h: r.height },
        });
      }
      return orig.call(this, kf, opts as KeyframeAnimationOptions);
    };
  });
}

async function openItems(page: Page, level: string) {
  await setLevel(page, level);
  await page.goto('/items');
  await expect(page.locator('#items-panel')).toBeVisible();
  await expect(page.locator('html')).toHaveClass(new RegExp(`(^|\\s)fx-${level}(\\s|$)`));
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await instrument(page);
}

function parse(first: string) {
  const m = /translate\((-?[\d.e-]+)px, (-?[\d.e-]+)px\) scale\(([\d.e-]+)\)/.exec(first);
  expect(m, `first keyframe is a uniform translate+scale, got ${first}`).not.toBeNull();
  return { dx: Number(m![1]), dy: Number(m![2]), s: Number(m![3]) };
}

test.describe('tree-pane zoom (ut-docs#2943, ADR-0122 §4)', () => {
  test.afterEach(async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await setLevel(page, WORKER_TILL_EFFECTS_LEVEL);
  });

  test('Full: the pane grows from the tapped rail item and leaves no transform behind', async ({ page }) => {
    const assertClean = watchConsole(page);
    await openItems(page, 'full');
    const link = page.locator('#items-rail a[href="/categories"]');
    const src = await link.boundingBox();
    expect(src).not.toBeNull();
    await link.click();
    await expect(page).toHaveURL(/\/categories$/);
    await expect.poll(() => page.evaluate(() => (window as unknown as { __zooms: Rec[] }).__zooms.length)).toBeGreaterThan(0);
    const rec = await page.evaluate(() => (window as unknown as { __zooms: Rec[] }).__zooms[0]);

    const { dx, dy, s } = parse(rec.first);
    // Centred on the tapped item's centre (ADR-0122 §1), within a pixel.
    expect(Math.abs(rec.pane.l + rec.pane.w / 2 + dx - (src!.x + src!.width / 2))).toBeLessThan(1.5);
    expect(Math.abs(rec.pane.t + rec.pane.h / 2 + dy - (src!.y + src!.height / 2))).toBeLessThan(1.5);
    // Uniform start scale = source width / pane width, clamped 0.1..1.
    expect(s).toBeCloseTo(Math.min(1, Math.max(0.1, src!.width / rec.pane.w)), 3);
    expect(rec.opacity).toBeGreaterThanOrEqual(0.4);
    expect(rec.fill).toBe('none');
    expect(rec.duration).toBe(300);

    // Transient: once it ends, nothing is left on the pane.
    await page.waitForTimeout(450);
    const after = await page.locator('#items-panel').evaluate((el) => ({
      running: el.getAnimations().length, transform: getComputedStyle(el).transform,
    }));
    expect(after.running).toBe(0);
    expect(after.transform).toBe('none');
    assertClean();
  });

  test('Balanced: the same zoom at 200 ms', async ({ page }) => {
    await openItems(page, 'balanced');
    await page.locator('#items-rail a[href="/categories"]').click();
    await expect.poll(() => page.evaluate(() => (window as unknown as { __zooms: Rec[] }).__zooms.length)).toBeGreaterThan(0);
    expect(await page.evaluate(() => (window as unknown as { __zooms: Rec[] }).__zooms[0].duration)).toBe(200);
  });

  test('Light: no animation is created at all', async ({ page }) => {
    await openItems(page, 'light');
    await page.locator('#items-rail a[href="/categories"]').click();
    await expect(page).toHaveURL(/\/categories$/);
    await expect(page.locator('#items-panel .list-header')).toBeVisible();
    await page.waitForTimeout(400);
    expect(await page.evaluate(() => (window as unknown as { __zooms: Rec[] }).__zooms.length)).toBe(0);
    const transforms = await page.evaluate(() => document.getAnimations()
      .filter((a) => ((a.effect as KeyframeEffect | null)?.getKeyframes() || []).some((k) => 'transform' in k)).length);
    expect(transforms).toBe(0);
  });

  test('typing in the help search swaps #manual-panel without a zoom per keystroke', async ({ page }) => {
    await setLevel(page, 'full');
    await page.goto('/help');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await instrument(page);
    const q = page.locator('#manual-q');
    await q.click();
    await page.evaluate(() => { (window as unknown as { __zooms: Rec[] }).__zooms = []; });
    const swapped = page.waitForResponse((r) => r.url().includes('/help/search'));
    await q.pressSequentially('pay', { delay: 30 });
    await swapped;
    await page.waitForTimeout(500);
    expect(await page.evaluate(() => (window as unknown as { __zooms: Rec[] }).__zooms.length)).toBe(0);
  });

  test('a mouse tap during the zoom finishes it and lands on the control at its final position', async ({ page }) => {
    await tapDuringZoom(page, 'mouse');
  });

  test('a touch tap during the zoom finishes it and lands on the control at its final position', async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, hasTouch: true });
    const page = await ctx.newPage();
    try {
      await tapDuringZoom(page, 'touch');
    } finally {
      await setLevel(page, WORKER_TILL_EFFECTS_LEVEL);
      await ctx.close();
    }
  });

  async function tapDuringZoom(page: Page, how: 'mouse' | 'touch') {
    await openItems(page, 'full');
    // Learn where the Categories "New" button sits once the pane is still.
    await page.locator('#items-rail a[href="/categories"]').click();
    const newBtn = page.locator('#items-panel .list-header [data-record-dialog-open]').first();
    await expect(newBtn).toBeVisible();
    await page.waitForTimeout(450);
    const box = await newBtn.boundingBox();
    expect(box).not.toBeNull();

    // Go elsewhere, come back, and tap the button's final position while
    // the pane is still zooming.
    await page.locator('#items-rail a[href="/catalog"]').click();
    await expect(page).toHaveURL(/\/catalog$/);
    await page.waitForTimeout(450);
    // A slow zoom, so the press provably lands while the pane is still
    // displaced (zoomPane reads the duration per call).
    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '3000ms'));
    await page.locator('#items-rail a[href="/categories"]').click();
    await page.waitForFunction(() => {
      const el = document.getElementById('items-panel');
      return !!el && el.getAnimations().some((a) => a.playState === 'running');
    }, undefined, { polling: 'raf', timeout: 5_000 });
    const x = box!.x + box!.width / 2, y = box!.y + box!.height / 2;
    const under = await page.evaluate(([px, py]) => {
      const el = document.elementFromPoint(px, py);
      return !!el && !!el.closest('[data-record-dialog-open]');
    }, [x, y]);
    expect(under, 'mid-zoom, the New button is not yet under its final position').toBe(false);
    if (how === 'mouse') await page.mouse.click(x, y);
    else await page.touchscreen.tap(x, y);

    await expect(page.locator('#category-dialog')).toBeVisible();
    // The tap finished the zoom: the dialog is not shown inside a
    // transformed pane (the #2338 hazard).
    const pane = await page.locator('#items-panel').evaluate((el) => ({
      running: el.getAnimations().filter((a) => a.playState === 'running').length, transform: getComputedStyle(el).transform,
    }));
    expect(pane.running).toBe(0);
    expect(pane.transform).toBe('none');
    await page.keyboard.press('Escape');
    await page.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
  }

  test('opening a dialog programmatically finishes a running pane zoom first', async ({ page }) => {
    await openItems(page, 'full');
    await page.locator('#items-rail a[href="/categories"]').click();
    await page.waitForFunction(() => {
      const el = document.getElementById('items-panel');
      return !!el && el.getAnimations().some((a) => a.playState === 'running');
    }, undefined, { polling: 'raf', timeout: 5_000 });
    const state = await page.evaluate(() => {
      const d = document.getElementById('category-dialog') as HTMLDialogElement;
      d.show();
      const el = document.getElementById('items-panel')!;
      const r = { open: d.open, running: el.getAnimations().filter((a) => a.playState === 'running').length };
      d.close();
      return r;
    });
    expect(state.open).toBe(true);
    expect(state.running).toBe(0);
  });
});
