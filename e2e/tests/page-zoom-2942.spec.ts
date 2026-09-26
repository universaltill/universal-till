import { test, expect } from './fixtures';
import { watchConsole } from './helpers';
import { WORKER_TILL_EFFECTS_LEVEL } from './worker-till';

// ut-docs#2942 / ADR-0122 §2, §3: page motion is an iOS zoom from the tapped
// element. The shell's capture-phase recorder remembers the pressed
// element's box; htmx:beforeTransition turns it into three custom
// properties on <html> (--ut-zoom-x/--ut-zoom-y = the source centre in
// viewport px, --ut-zoom-s = source width / viewport width, clamped .1-1)
// which app.css's ut-page-zoom-in / ut-page-zoom-out keyframes read. Back
// shrinks into the box stored for the page being left, but only when it
// lands on the page that box came from; anything else (and no origin at
// all) uses the bottom centre of the viewport.
//
// Headless Chromium runs same-document View Transitions (the boosted
// shell navigation, ADR-0098), so every assertion here is on the real
// product path. The cross-document half (pageswap/pagereveal) cannot run
// in this automation environment (see page-transitions-2223.spec.ts); its
// test drives a real navigation and stands in only for the event delivery.
//
// The suite runs as a reduced-motion user (fixtures.ts) -- under which
// motion is off at every level -- so each test opts back in first.

type Frame = { newT: string; newO: string; oldT: string; oldO: string; w: number; h: number };
type Rec = {
  created: number;
  ready: string;
  finished: string;
  dir: string | null;
  vars: { x: string; y: string; s: string } | null;
  anims: { pseudo: string; name: string }[];
  frame: Frame | null;
};

// Wraps base.html's startViewTransition wrapper (the shipped watchdog and
// shell sync still run). With `freezeAt` (0..1) every ::view-transition
// animation is paused at that fraction the moment the motion starts, and
// the computed transform/opacity of the root pair is read there.
async function instrument(page: import('@playwright/test').Page, freezeAt: number | null = null) {
  await page.evaluate((freezeAt) => {
    const w = window as unknown as { __z: Rec; __zArm: () => void };
    w.__zArm = () => { w.__z = { created: 0, ready: 'pending', finished: 'pending', dir: null, vars: null, anims: [], frame: null }; };
    w.__zArm();
    const orig = document.startViewTransition.bind(document);
    (document as unknown as { startViewTransition: unknown }).startViewTransition = (...args: unknown[]) => {
      const rec = w.__z;
      rec.created++;
      const vt = (orig as (...a: unknown[]) => ViewTransition)(...args);
      vt.ready.then(() => {
        const cs = getComputedStyle(document.documentElement);
        rec.vars = {
          x: cs.getPropertyValue('--ut-zoom-x').trim(),
          y: cs.getPropertyValue('--ut-zoom-y').trim(),
          s: cs.getPropertyValue('--ut-zoom-s').trim(),
        };
        rec.dir = document.documentElement.getAttribute('data-nav-dir');
        const all = document.getAnimations().filter((a) => ((a.effect as KeyframeEffect | null)?.pseudoElement || '').startsWith('::view-transition'));
        rec.anims = all
          .filter((a) => /::view-transition-(old|new)\(root\)/.test((a.effect as KeyframeEffect).pseudoElement || ''))
          .map((a) => ({ pseudo: (a.effect as KeyframeEffect).pseudoElement || '', name: (a as CSSAnimation).animationName }));
        if (freezeAt !== null) {
          all.forEach((a) => {
            a.pause();
            const d = Number((a.effect as KeyframeEffect).getTiming().duration) || 0;
            a.currentTime = d * freezeAt;
          });
          const n = getComputedStyle(document.documentElement, '::view-transition-new(root)');
          const o = getComputedStyle(document.documentElement, '::view-transition-old(root)');
          rec.frame = { newT: n.transform, newO: n.opacity, oldT: o.transform, oldO: o.opacity, w: window.innerWidth, h: window.innerHeight };
        }
        rec.ready = 'resolved';
      }, () => { rec.ready = 'rejected'; });
      vt.finished.then(() => { rec.finished = 'resolved'; }, () => { rec.finished = 'rejected'; });
      return vt;
    };
  }, freezeAt);
}

const readRec = (page: import('@playwright/test').Page) => page.evaluate(() => (window as unknown as { __z: Rec }).__z);
const rearm = (page: import('@playwright/test').Page) => page.evaluate(() => (window as unknown as { __zArm: () => void }).__zArm());

async function open(page: import('@playwright/test').Page, url: string, size = { width: 1024, height: 600 }) {
  await page.setViewportSize(size);
  await page.goto(url);
  await page.waitForLoadState('networkidle');
  const supported = await page.evaluate(() => typeof document.startViewTransition === 'function');
  test.skip(!supported, 'engine without same-document View Transitions');
  await page.emulateMedia({ reducedMotion: 'no-preference' });
}

function box(b: { x: number; y: number; width: number; height: number } | null) {
  expect(b).not.toBeNull();
  return { cx: b!.x + b!.width / 2, cy: b!.y + b!.height / 2, w: b!.width };
}

function expectVarsAt(rec: Rec, cx: number, cy: number, s: number) {
  expect(rec.vars, 'the zoom origin was set before the motion started').not.toBeNull();
  expect(Math.abs(parseFloat(rec.vars!.x) - cx), `--ut-zoom-x ${rec.vars!.x} vs ${cx}`).toBeLessThanOrEqual(1);
  expect(Math.abs(parseFloat(rec.vars!.y) - cy), `--ut-zoom-y ${rec.vars!.y} vs ${cy}`).toBeLessThanOrEqual(1);
  expect(Math.abs(parseFloat(rec.vars!.s) - s), `--ut-zoom-s ${rec.vars!.s} vs ${s}`).toBeLessThanOrEqual(0.002);
}

function parseMatrix(t: string): number[] {
  const m = /^matrix\(([^)]+)\)$/.exec(t);
  expect(m, `a 2D matrix, got ${t}`).not.toBeNull();
  return m![1].split(',').map((v) => parseFloat(v));
}

async function finishMotion(page: import('@playwright/test').Page) {
  await expect.poll(async () => (await readRec(page)).finished).not.toBe('pending');
}

test.describe('page zoom from the tapped element (ut-docs#2942, ADR-0122)', () => {
  test.afterEach(async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.request.post('/api/settings/effects-level', { form: { level: WORKER_TILL_EFFECTS_LEVEL } });
    await page.request.post('/api/pos/reset');
  });

  test('Full: a menu tile tap grows the new page out of that tile', async ({ page }) => {
    const assertClean = watchConsole(page);
    await open(page, '/menu');
    await instrument(page, 0);
    const tile = page.locator('a.menu-tile[href="/reports"]');
    const b = box(await tile.boundingBox());
    const vw = await page.evaluate(() => window.innerWidth);
    await tile.click();
    await expect(page).toHaveURL(/\/reports$/);
    await expect.poll(async () => (await readRec(page)).ready).toBe('resolved');
    const rec = await readRec(page);
    expect(rec.dir).toBe('push');
    expectVarsAt(rec, b.cx, b.cy, Math.min(1, Math.max(0.1, b.w / vw)));
    const byPseudo = Object.fromEntries(rec.anims.map((a) => [a.pseudo, a.name]));
    expect(byPseudo['::view-transition-new(root)']).toBe('ut-page-zoom-in');
    expect(byPseudo['::view-transition-old(root)']).toBe('ut-page-recede');
    // The first frame, geometrically: the incoming page's visible box is
    // the tile's box (uniform scale, centred on the tile's centre), and it
    // is never blank.
    const f = rec.frame!;
    const [a, bb, c, d, tx, ty] = parseMatrix(f.newT);
    expect(bb).toBeCloseTo(0, 5);
    expect(c).toBeCloseTo(0, 5);
    expect(a, 'uniform scale').toBeCloseTo(d, 5);
    expect(Math.abs(a * f.w - b.w), `start width ${a * f.w} vs tile ${b.w}`).toBeLessThanOrEqual(2);
    expect(Math.abs(f.w / 2 + tx - b.cx), 'start centre x').toBeLessThanOrEqual(2);
    expect(Math.abs(f.h / 2 + ty - b.cy), 'start centre y').toBeLessThanOrEqual(2);
    expect(parseFloat(f.newO), 'the growing page starts at opacity >= .4').toBeGreaterThanOrEqual(0.4);
    expect(f.oldT === 'none' || /^matrix\(1, 0, 0, 1, 0, 0\)$/.test(f.oldT), `the old page starts in place, got ${f.oldT}`).toBe(true);
    // Control for the Light test below: the same keyframe probe it uses
    // does see the zoom's transform animation when motion is on.
    expect(await page.evaluate(() => document.getAnimations().some((x) =>
      ((x.effect as KeyframeEffect | null)?.getKeyframes?.() || []).some((k) => 'transform' in k)))).toBe(true);
    await page.evaluate(() => document.getAnimations().forEach((x) => x.finish()));
    await finishMotion(page);
    assertClean();
  });

  test('back shrinks into the stored box only when it lands where that box was; else bottom centre', async ({ page }) => {
    const assertClean = watchConsole(page);
    await open(page, '/menu');
    await instrument(page);
    const vw = await page.evaluate(() => window.innerWidth);
    const vh = await page.evaluate(() => window.innerHeight);
    const b = box(await page.locator('a.menu-tile[href="/reports"]').boundingBox());
    const tileS = Math.min(1, Math.max(0.1, b.w / vw));

    await page.locator('a.menu-tile[href="/reports"]').click();
    await expect(page).toHaveURL(/\/reports$/);
    await finishMotion(page);
    const stored = await page.evaluate(() => JSON.parse(sessionStorage.getItem('ut-zoom-back') || '[]'));
    expect(stored.find((e: unknown[]) => e[0] === '/reports'), 'the forward hop stored {from, rect} for its destination').toEqual(
      ['/reports', '/menu', Math.round(b.cx - b.w / 2), expect.any(Number), Math.round(b.w), expect.any(Number)]);

    // Reports -> Menu via the rail: a pop landing on the `from` of the
    // entry -> shrink into the tile the page was opened from.
    await rearm(page);
    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    await finishMotion(page);
    let rec = await readRec(page);
    expect(rec.dir).toBe('pop');
    expectVarsAt(rec, b.cx, b.cy, tileS);
    const byPseudo = Object.fromEntries(rec.anims.map((a) => [a.pseudo, a.name]));
    expect(byPseudo['::view-transition-old(root)'], 'the page being left shrinks away on top').toBe('ut-page-zoom-out');
    expect(byPseudo['::view-transition-new(root)'], 'the previous page rises from the receded state').toBe('ut-page-return');

    // Menu -> Reports again, then the rail jumps straight to Sell: a pop
    // from /reports that does NOT land on /menu -> bottom-centre fallback.
    await rearm(page);
    await page.locator('a.menu-tile[href="/reports"]').click();
    await expect(page).toHaveURL(/\/reports$/);
    await finishMotion(page);
    await rearm(page);
    await page.locator('[data-testid="nav-till"]').click();
    await expect(page).toHaveURL(/\/$/);
    await finishMotion(page);
    rec = await readRec(page);
    expect(rec.dir).toBe('pop');
    expectVarsAt(rec, vw / 2, vh, 0.3);
    assertClean();
  });

  test('no press (a scripted navigation) grows from the bottom centre; keyboard Enter is a press', async ({ page }) => {
    const assertClean = watchConsole(page);
    await open(page, '/menu');
    await instrument(page);
    const vw = await page.evaluate(() => window.innerWidth);
    const vh = await page.evaluate(() => window.innerHeight);
    // A script click: no pointerdown/keydown ever recorded an origin.
    await page.evaluate(() => (document.querySelector('a.menu-tile[href="/reports"]') as HTMLElement).click());
    await expect(page).toHaveURL(/\/reports$/);
    await finishMotion(page);
    expectVarsAt(await readRec(page), vw / 2, vh, 0.3);

    // Keyboard activation of a focused tile records it as the origin.
    await page.goto('/menu');
    await page.waitForLoadState('networkidle');
    await instrument(page);
    const tile = page.locator('a.menu-tile[href="/reports"]');
    const b = box(await tile.boundingBox());
    await tile.focus();
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/reports$/);
    await finishMotion(page);
    expectVarsAt(await readRec(page), b.cx, b.cy, Math.min(1, Math.max(0.1, b.w / vw)));
    assertClean();
  });

  test('phone width: the rail tap origin is the rail button', async ({ page }) => {
    const assertClean = watchConsole(page);
    await open(page, '/', { width: 360, height: 740 });
    await instrument(page);
    const btn = page.locator('[data-testid="nav-menu"]');
    const b = box(await btn.boundingBox());
    await btn.click();
    await expect(page).toHaveURL(/\/menu$/);
    await finishMotion(page);
    expectVarsAt(await readRec(page), b.cx, b.cy, Math.min(1, Math.max(0.1, b.w / 360)));
    assertClean();
  });

  test('under lang=fa (RTL) the origin is the tapped tile, wherever RTL put it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await open(page, '/menu?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    await instrument(page);
    const tile = page.locator('a.menu-tile[href="/reports"]');
    const b = box(await tile.boundingBox());
    const vw = await page.evaluate(() => window.innerWidth);
    await tile.click();
    await expect(page).toHaveURL(/\/reports/);
    await finishMotion(page);
    const rec = await readRec(page);
    expect(rec.created, 'the hop stayed a boosted same-document transition').toBe(1);
    expectVarsAt(rec, b.cx, b.cy, Math.min(1, Math.max(0.1, b.w / vw)));
    // Restore the default locale for the rest of this worker's specs.
    await page.goto('/menu?lang=en');
    assertClean();
  });

  test('cross-document reveal grows from the box the outgoing page stored', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/menu');
    // A real navigation, so navigation.activation is the browser's genuine
    // Menu -> Reports push; only the outgoing pageswap's store write and
    // the pagereveal delivery are stood in for (see the file comment).
    await page.goto('/reports');
    await page.waitForLoadState('load');
    const supported = await page.evaluate(() => 'navigation' in window && !!window.CSS && CSS.supports('view-transition-name: x'));
    test.skip(!supported, 'engine without View Transitions / Navigation API');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    const r = await page.evaluate(() => {
      const U = (window as unknown as { UT: { zoomRemember: (to: string, from: string, r: unknown) => void } }).UT;
      U.zoomRemember('/reports', '/menu', { left: 300, top: 200, width: 200, height: 80 });
      const ev = new Event('pagereveal') as Event & { viewTransition?: unknown };
      ev.viewTransition = { types: { add: () => {} }, skipTransition: () => {} };
      window.dispatchEvent(ev);
      const cs = getComputedStyle(document.documentElement);
      return {
        dir: document.documentElement.getAttribute('data-nav-dir'),
        x: cs.getPropertyValue('--ut-zoom-x').trim(), y: cs.getPropertyValue('--ut-zoom-y').trim(), s: cs.getPropertyValue('--ut-zoom-s').trim(),
      };
    });
    expect(r.dir).toBe('push');
    expect(r).toMatchObject({ x: '400px', y: '240px' });
    expect(parseFloat(r.s)).toBeCloseTo(200 / 1024, 3);
    assertClean();
  });

  test('the back store keeps only the newest 20 entries, each a path and four numbers', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    const r = await page.evaluate(() => {
      const U = (window as unknown as { UT: { zoomRemember: (to: string, from: string, r: unknown) => void; zoomRecall: (a: string, b: string) => unknown } }).UT;
      sessionStorage.removeItem('ut-zoom-back');
      for (let i = 0; i < 25; i++) U.zoomRemember('/p' + i, '/menu', { left: i, top: 1, width: 10, height: 10 });
      const list = JSON.parse(sessionStorage.getItem('ut-zoom-back') || '[]');
      // Garbage in storage must never throw into the page.
      sessionStorage.setItem('ut-zoom-back', '{not json');
      const junk = U.zoomRecall('/p24', '/menu');
      return { n: list.length, first: list[0][0], last: list[list.length - 1], junk };
    });
    expect(r.n).toBe(20);
    expect(r.first).toBe('/p5');
    expect(r.last).toEqual(['/p24', '/menu', 24, 1, 10, 10]);
    expect(r.junk).toBeNull();
    assertClean();
  });

  test('Light: no View Transition is created and no transform animation runs', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.request.post('/api/settings/effects-level', { form: { level: 'light' } });
    await open(page, '/menu');
    await expect(page.locator('html')).toHaveClass(/(^|\s)fx-light(\s|$)/);
    await instrument(page);
    // Sample every frame for the whole would-be motion: any animation whose
    // keyframes touch `transform` (page, pane or dialog) is a failure.
    await page.evaluate(() => {
      const w = window as unknown as { __tf: string[]; __tfUntil: number };
      w.__tf = [];
      w.__tfUntil = performance.now() + 1500;
      const tick = () => {
        document.getAnimations().forEach((a) => {
          const kf = (a.effect as KeyframeEffect | null)?.getKeyframes?.() || [];
          if (kf.some((k) => 'transform' in k)) w.__tf.push(((a as CSSAnimation).animationName || 'waapi') + ' ' + ((a.effect as KeyframeEffect).pseudoElement || ''));
        });
        if (performance.now() < w.__tfUntil) requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    });
    await page.locator('a.menu-tile[href="/reports"]').click();
    await expect(page).toHaveURL(/\/reports$/);
    await page.waitForTimeout(1600);
    const rec = await readRec(page);
    expect(rec.created, 'Light never creates a View Transition').toBe(0);
    expect(await page.evaluate(() => (window as unknown as { __tf: string[] }).__tf)).toEqual([]);
    // ...and the zoom origin is never even written.
    expect(await page.evaluate(() => document.documentElement.style.getPropertyValue('--ut-zoom-x'))).toBe('');
    assertClean();
  });

  // ADR-0118 §3 / ADR-0122 §7 for the page: with the zoom frozen early (10%
  // of the time -- the ease is front-loaded, so at 50% the page is already
  // ~96% grown -- i.e. the new page mid-growth, far from its final box), a
  // tap aimed at a
  // control's FINAL position lands on that control, for mouse and touch.
  for (const how of ['mouse', 'touch'] as const) {
    test(`a ${how} tap mid-zoom lands on the control at its final position`, async ({ browser, baseURL }) => {
      const ctx = await browser.newContext({ baseURL, hasTouch: how === 'touch', viewport: { width: 1024, height: 600 } });
      const page = await ctx.newPage();
      const assertClean = watchConsole(page);
      try {
        await open(page, '/');
        await instrument(page, 0.1);
        await page.evaluate(() => {
          const w = window as unknown as { __hit: string[] };
          w.__hit = [];
          document.addEventListener('click', (e) => {
            if (!document.documentElement.classList.contains('__probe')) return;
            e.preventDefault();
            e.stopPropagation();
            const t = e.target as Element;
            w.__hit.push(t === document.documentElement ? 'html' : (t.closest('a.menu-tile[href="/reports"]') ? 'reports-tile' : t.tagName.toLowerCase()));
          }, true);
        });
        await page.locator('[data-testid="nav-menu"]').click();
        await expect.poll(async () => (await readRec(page)).ready).toBe('resolved');
        const rec = await readRec(page);
        const [scale] = parseMatrix(rec.frame!.newT);
        expect(scale, 'frozen genuinely mid-growth, far from the final box').toBeLessThan(0.8);
        await page.evaluate(() => document.documentElement.classList.add('__probe'));
        const b = box(await page.locator('a.menu-tile[href="/reports"]').boundingBox());
        expect((await readRec(page)).finished, 'still running when the operator taps').toBe('pending');
        if (how === 'mouse') await page.mouse.click(b.cx, b.cy);
        else await page.touchscreen.tap(b.cx, b.cy);
        await expect.poll(() => page.evaluate(() => (window as unknown as { __hit: string[] }).__hit.length)).toBeGreaterThan(0);
        expect(await page.evaluate(() => (window as unknown as { __hit: string[] }).__hit)).toEqual(['reports-tile']);
        await expect.poll(async () => (await readRec(page)).finished, 'the tap ended the motion').not.toBe('pending');
        await page.evaluate(() => document.documentElement.classList.remove('__probe'));
        assertClean();
      } finally {
        await ctx.close();
      }
    });
  }
});
