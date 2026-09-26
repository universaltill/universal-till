import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';
import { WORKER_TILL_EFFECTS_LEVEL } from './worker-till';

// ut-docs#2944 / ADR-0122 §5-§8: every popup grows from the element that was
// tapped and, on close, shrinks back into it -- ONE shared mechanism in
// base.html (the HTMLDialogElement showModal/show wrapper + a capture-phase
// `close` listener; UT.popupOpened/UT.popupClosed for a popup that is not a
// <dialog>). The closing shrink re-shows the SAME element with
// data-ut-closing + inert until the motion ends. Light (ADR-0119) = no
// animation and no re-show. The static half is
// internal/pages/popup_zoom_guard_test.go.
//
// The e2e suite runs as a reduced-motion user (fixtures.ts), under which
// motion is off at every level: every test here emulates 'no-preference'
// first -- the Light test too, so it cannot pass vacuously.

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
  const name = `Pz2944 Mod ${tag}`;
  const barcode = `PZ2944BC-${tag}`;
  const csv = `Name,SKU,Barcode,Price,Category,In stock\n${name},PZ2944${tag},${barcode},1.00,Pz2944 Cat,1\n`;
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: `import-pz2944-${RUN}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/catalog');
  const itemId = (await page.locator(`.catalog-row[data-name="${name}"]`).first().getAttribute('data-id'))!;
  expect((await page.request.post('/api/buttons/add', { form: { itemId, label: name, code: barcode } })).ok()).toBe(true);
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

async function openSale(page: Page, level: string) {
  if (!seeded) seeded = await seedModifierItem(page);
  await setLevel(page, level);
  await page.goto('/');
  await expect(page.locator('html')).toHaveClass(new RegExp(`(^|\\s)fx-${level}(\\s|$)`));
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await instrument(page);
  const tile = page.locator('.btn-tile:visible', { hasText: seeded.name });
  await expect(tile).toBeVisible();
  return tile;
}

async function tapTileForModal(page: Page, tile: ReturnType<Page['locator']>) {
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
    tile.click(),
  ]);
  await expect(page.locator('#modifier-modal')).toHaveJSProperty('open', true);
}

// State of #modifier-modal right now: open, closing marker, inert, a running
// transform animation.
const modalState = (page: Page) => page.evaluate(() => {
  const d = document.getElementById('modifier-modal') as HTMLDialogElement;
  return {
    open: d.open,
    closing: d.hasAttribute('data-ut-closing'),
    inert: d.hasAttribute('inert'),
    ariaHidden: d.getAttribute('aria-hidden'),
    running: d.getAnimations().filter((a) => a.playState === 'running'
      && ((a.effect as KeyframeEffect | null)?.getKeyframes() || []).some((k) => 'transform' in k)).length,
    visible: d.getClientRects().length > 0,
  };
});

test.describe('popup zoom (ut-docs#2944, ADR-0122 §5)', () => {
  test.afterEach(async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await setLevel(page, WORKER_TILL_EFFECTS_LEVEL);
    await page.request.post('/api/pos/reset').catch(() => {});
    if (seeded) {
      await page.request.post('/api/buttons/remove', { form: { code: seeded.barcode } });
      await page.request.post('/api/catalog/item/deactivate', { form: { id: seeded.itemId } });
      seeded = null;
    }
  });

  test('Full: an item tap grows the modifiers popup from the tapped tile; close() and Escape shrink it back', async ({ page }) => {
    const assertClean = watchConsole(page);
    const tile = await openSale(page, 'full');
    const src = (await tile.boundingBox())!;
    await tapTileForModal(page, tile);

    const all = await recs(page);
    const rec = all.find((r) => r.id === 'modifier-modal');
    expect(rec, `a transform zoom on #modifier-modal, got ${JSON.stringify(all)}`).toBeTruthy();
    const { dx, dy, s } = parse(rec!.first);
    // Grows from the tapped tile's centre (ADR-0122 §1), within a pixel.
    expect(Math.abs(rec!.box.l + rec!.box.w / 2 + dx - (src.x + src.width / 2))).toBeLessThan(1.5);
    expect(Math.abs(rec!.box.t + rec!.box.h / 2 + dy - (src.y + src.height / 2))).toBeLessThan(1.5);
    expect(s).toBeCloseTo(Math.min(1, Math.max(0.1, src.width / rec!.box.w)), 3);
    expect(rec!.opacity).toBeGreaterThanOrEqual(0.4);
    expect(rec!.fill).toBe('none');
    expect(rec!.duration).toBe(300);

    // Transient: nothing left on the dialog after the last frame.
    await page.waitForTimeout(450);
    expect(await page.locator('#modifier-modal').evaluate((el) => getComputedStyle(el).transform)).toBe('none');

    // A slower shrink, so the assertions below provably land mid-motion.
    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '1500ms'));

    // close(): closed at once (native), then painted again, inert, shrinking.
    const now = await page.evaluate(() => {
      const d = document.getElementById('modifier-modal') as HTMLDialogElement;
      d.close();
      return { open: d.open };
    });
    expect(now.open).toBe(false);
    await expect.poll(async () => (await modalState(page)).closing).toBe(true);
    let st = await modalState(page);
    expect(st).toMatchObject({ open: false, closing: true, inert: true, ariaHidden: 'true', visible: true });
    expect(st.running).toBe(1);
    // It shrinks INTO the tile: the closing animation's last keyframe
    // centres on the tile.
    const lastCentre = await page.locator('#modifier-modal').evaluate((el) => {
      const a = el.getAnimations().find((x) => ((x.effect as KeyframeEffect).getKeyframes()).some((k) => 'transform' in k))!;
      a.pause();
      a.currentTime = Number((a.effect as KeyframeEffect).getComputedTiming().endTime) - 1;
      const r = el.getBoundingClientRect();
      a.play();
      return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    });
    expect(Math.abs(lastCentre.x - (src.x + src.width / 2))).toBeLessThan(3);
    expect(Math.abs(lastCentre.y - (src.y + src.height / 2))).toBeLessThan(3);
    // Focus came back and the page takes input while it shrinks.
    expect(await page.evaluate(() => !!document.activeElement && !document.activeElement.closest('#modifier-modal'))).toBe(true);
    await expect.poll(async () => (await modalState(page)).closing, { timeout: 5_000 }).toBe(false);
    st = await modalState(page);
    expect(st).toMatchObject({ open: false, closing: false, inert: false, ariaHidden: null, visible: false });

    // Escape: the same shrink (the close EVENT, not a .close() wrapper).
    await page.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
    await tapTileForModal(page, tile);
    await page.waitForTimeout(450);
    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '1500ms'));
    await page.keyboard.press('Escape');
    await expect.poll(async () => (await modalState(page)).closing).toBe(true);
    st = await modalState(page);
    expect(st).toMatchObject({ open: false, closing: true, inert: true, visible: true });
    await expect(page.locator('#modifier-modal')).toBeHidden({ timeout: 5_000 });
    expect((await modalState(page)).inert).toBe(false);

    // Reopening during a shrink ends it first: open, interactive, no marker.
    await page.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
    await tapTileForModal(page, tile);
    await page.waitForTimeout(450);
    const re = await page.evaluate(async () => {
      document.documentElement.style.setProperty('--ut-zoom-small-ms', '3000ms');
      const d = document.getElementById('modifier-modal') as HTMLDialogElement;
      d.close();
      await new Promise((r) => setTimeout(r, 50)); // the close event is delivered
      const mid = d.hasAttribute('data-ut-closing');
      d.showModal();
      const r = { mid, open: d.open, closing: d.hasAttribute('data-ut-closing'), inert: d.hasAttribute('inert') };
      d.close();
      document.documentElement.style.removeProperty('--ut-zoom-small-ms');
      return r;
    });
    expect(re).toEqual({ mid: true, open: true, closing: false, inert: false });
    assertClean();
  });

  test('Balanced: the same zoom at 200 ms', async ({ page }) => {
    const tile = await openSale(page, 'balanced');
    await tapTileForModal(page, tile);
    const rec = (await recs(page)).find((r) => r.id === 'modifier-modal');
    expect(rec?.duration).toBe(200);
    await page.keyboard.press('Escape');
  });

  test('Light: no animation and no closing re-show', async ({ page }) => {
    const tile = await openSale(page, 'light');
    await tapTileForModal(page, tile);
    await page.waitForTimeout(350);
    expect(await recs(page)).toHaveLength(0);
    const transforms = () => page.evaluate(() => document.getAnimations()
      .filter((a) => ((a.effect as KeyframeEffect | null)?.getKeyframes() || []).some((k) => 'transform' in k)).length);
    expect(await transforms()).toBe(0);
    const st = await page.evaluate(() => {
      const d = document.getElementById('modifier-modal') as HTMLDialogElement;
      d.close();
      return { closing: d.hasAttribute('data-ut-closing'), visible: d.getClientRects().length > 0 };
    });
    expect(st).toEqual({ closing: false, visible: false });
    await page.waitForTimeout(350);
    expect(await transforms()).toBe(0);
    expect(await page.evaluate(() => (window as unknown as W).__closingSeen)).toHaveLength(0);
  });

  for (const how of ['mouse', 'touch'] as const) {
    test(`a ${how} tap during the opening zoom lands on the control at its final position`, async ({ page, browser, baseURL }) => {
      let p = page;
      let ctx = null as null | Awaited<ReturnType<typeof browser.newContext>>;
      if (how === 'touch') {
        ctx = await browser.newContext({ baseURL, hasTouch: true });
        p = await ctx.newPage();
      }
      try {
        const tile = await openSale(p, 'full');
        // Learn where the option sits once the popup is still.
        await tapTileForModal(p, tile);
        await p.waitForTimeout(450);
        const option = p.locator('#modifier-modal .modifier-option', { hasText: seeded!.optionName });
        const box = (await option.boundingBox())!;
        await p.keyboard.press('Escape');
        await expect(p.locator('#modifier-modal')).toBeHidden();

        // A slow zoom, so the press provably lands while the popup moves.
        await p.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '3000ms'));
        await tapTileForModal(p, tile);
        await p.waitForFunction(() => {
          const d = document.getElementById('modifier-modal');
          return !!d && d.getAnimations().some((a) => a.playState === 'running');
        }, undefined, { polling: 'raf', timeout: 5_000 });
        const x = box.x + box.width / 2, y = box.y + box.height / 2;
        const under = await p.evaluate(([px, py]) => {
          const el = document.elementFromPoint(px, py);
          return !!el && !!el.closest('.modifier-option');
        }, [x, y]);
        expect(under, 'mid-zoom, the option is not yet under its final position').toBe(false);
        if (how === 'mouse') await p.mouse.click(x, y);
        else await p.touchscreen.tap(x, y);
        await expect(option.locator('input')).toBeChecked();
        expect((await modalState(p)).running).toBe(0);
        await p.evaluate(() => {
          document.documentElement.style.removeProperty('--ut-zoom-small-ms');
          (document.getElementById('modifier-modal') as HTMLDialogElement).close();
        });
      } finally {
        if (ctx) {
          await setLevel(p, WORKER_TILL_EFFECTS_LEVEL);
          await ctx.close();
        }
      }
    });
  }

  // Review (major): the dialog `close` event is a queued task, so a close
  // site that empties the dialog right after .close() hands the shrink an
  // already-empty element -- a blank white card shrinking into the tile.
  // Drive the picker's REAL buttons, not d.close().
  test("Full: the picker's real Cancel and Add buttons shrink the filled picker, not a blank card", async ({ page }) => {
    const assertClean = watchConsole(page);
    const tile = await openSale(page, 'full');
    const modal = page.locator('#modifier-modal');
    const closing = () => page.evaluate(() => {
      const w = window as unknown as W;
      return { seen: w.__closingSeen.filter((x) => x === 'modifier-modal').length, kids: w.__closingKids.slice() };
    });

    // Cancel.
    await tapTileForModal(page, tile);
    await page.waitForTimeout(450);
    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '1500ms'));
    await modal.locator('.modifier-actions .btn.secondary').click();
    await expect.poll(async () => (await closing()).seen).toBe(1);
    let c = await closing();
    expect(c.kids.every((k) => k > 0), `closing picker children: ${JSON.stringify(c.kids)}`).toBe(true);
    expect(await modal.evaluate((el) => el.hasAttribute('data-ut-closing') && el.childElementCount > 0)).toBe(true);
    await expect(modal).toBeHidden({ timeout: 5_000 });

    // Add: the item still reaches the basket, and the shrink is not blank.
    await page.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
    await tapTileForModal(page, tile);
    await page.waitForTimeout(450);
    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '1500ms'));
    await modal.locator('.modifier-option', { hasText: seeded!.optionName }).click();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan-with-modifiers') && r.ok()),
      modal.locator('.modifier-actions button[type=submit]').click(),
    ]);
    await expect.poll(async () => (await closing()).seen).toBe(2);
    c = await closing();
    expect(c.kids.every((k) => k > 0), `closing picker children: ${JSON.stringify(c.kids)}`).toBe(true);
    await expect(page.locator('#basket')).toContainText(seeded!.name);
    await expect(page.locator('#basket')).toContainText(seeded!.optionName);
    await expect(modal).toBeHidden({ timeout: 5_000 });
    await page.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
    assertClean();
  });

  // Review (minor): the closing shrink is inert and pointer-events:none, so
  // a tap during it was hit-tested at nothing moving -- it must not arm the
  // §7 click re-dispatch. UT.finishZooms() still finishes a passive zoom
  // but reports only non-passive ones.
  test('a tap during a closing shrink lands once on what is behind it (passive zoom)', async ({ page }) => {
    const tile = await openSale(page, 'full');
    const unit = await page.evaluate(() => {
      const UT = (window as unknown as { UT: { trackZoom: (a: Animation, o?: { passive: boolean }) => void; finishZooms: () => boolean } }).UT;
      const a = document.body.animate({ opacity: [1, 1] }, { duration: 10_000 });
      UT.trackZoom(a, { passive: true });
      const passiveOnly = UT.finishZooms();
      const b = document.body.animate({ opacity: [1, 1] }, { duration: 10_000 });
      UT.trackZoom(b);
      const nonPassive = UT.finishZooms();
      return { passiveOnly, passiveFinished: a.playState, nonPassive, nonPassiveFinished: b.playState };
    });
    expect(unit).toEqual({ passiveOnly: false, passiveFinished: 'finished', nonPassive: true, nonPassiveFinished: 'finished' });

    await tapTileForModal(page, tile);
    await page.waitForTimeout(450);
    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '3000ms'));
    await page.keyboard.press('Escape');
    await expect.poll(async () => (await modalState(page)).closing).toBe(true);
    let gets = 0;
    page.on('request', (r) => { if (r.url().includes('/ui/pos/modifiers')) gets++; });
    await page.evaluate(() => {
      const w = window as unknown as { __tileClicks: number };
      w.__tileClicks = 0;
      document.addEventListener('click', (e) => {
        if ((e.target as Element).closest && (e.target as Element).closest('.btn-tile')) w.__tileClicks++;
      }, true);
    });
    const box = (await tile.boundingBox())!;
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
      page.mouse.click(box.x + box.width / 2, box.y + box.height / 2),
    ]);
    await expect(page.locator('#modifier-modal')).toHaveJSProperty('open', true);
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => (window as unknown as { __tileClicks: number }).__tileClicks)).toBe(1);
    expect(gets).toBe(1);
    await page.evaluate(() => {
      document.documentElement.style.removeProperty('--ut-zoom-small-ms');
      (document.getElementById('modifier-modal') as HTMLDialogElement).close();
    });
  });

  test('a popup that is not a <dialog> (the bug-report panel) zooms from its toggle and shrinks back', async ({ page }) => {
    await setLevel(page, 'full');
    await page.goto('/');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await instrument(page);
    const toggle = page.getByTestId('bugreport-toggle');
    const src = (await toggle.boundingBox())!;
    await toggle.click();
    const panel = page.locator('.bugreport-panel');
    await expect(panel).toBeVisible();
    const rec = (await recs(page)).find((r) => r.cls.includes('bugreport-panel'));
    expect(rec, 'a transform zoom on the bug-report panel').toBeTruthy();
    const { dx, dy } = parse(rec!.first);
    expect(Math.abs(rec!.box.l + rec!.box.w / 2 + dx - (src.x + src.width / 2))).toBeLessThan(1.5);
    expect(Math.abs(rec!.box.t + rec!.box.h / 2 + dy - (src.y + src.height / 2))).toBeLessThan(1.5);
    await page.waitForTimeout(450);

    await page.evaluate(() => document.documentElement.style.setProperty('--ut-zoom-small-ms', '1500ms'));
    await page.locator('#bugreport-close').click();
    const st = await panel.evaluate((el) => ({ open: el.classList.contains('open'), closing: el.hasAttribute('data-ut-closing'), inert: el.hasAttribute('inert') }));
    expect(st).toEqual({ open: false, closing: true, inert: true });
    await expect(panel).toBeHidden({ timeout: 5_000 });
    expect(await panel.evaluate((el) => el.hasAttribute('inert') || el.hasAttribute('data-ut-closing'))).toBe(false);
    await page.evaluate(() => document.documentElement.style.removeProperty('--ut-zoom-small-ms'));
  });
});
