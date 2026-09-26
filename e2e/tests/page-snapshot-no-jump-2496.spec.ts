import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2496 (reopened 2026-09-25) / ADR-0118: "it first removes the lower
// part of the page, the page goes down and then changes."
//
// Root cause: base.html's htmx:beforeSwap listener synced <html lang/dir/
// --ui-scale> and the <body> class list/data-* from the response BEFORE htmx
// called document.startViewTransition. Sell -> Menu therefore dropped
// body.sale-screen (height:100dvh + the #ut-page flex rules) before the
// OLD-state snapshot was captured, so the outgoing page collapsed and that
// collapsed image is what animated out. The fix defers the sync into the
// transition's update callback (or htmx:afterSwap when no transition runs):
// snapshot first, then mutate.
//
// How the "old snapshot" moment is observed: the old state is captured in
// the rendering step that follows startViewTransition(), after that frame's
// requestAnimationFrame callbacks. A rAF queued inside the startViewTransition
// call therefore sees exactly the DOM the old snapshot is taken from.
//
// Headless Chromium runs same-document View Transitions (unlike the
// cross-document ones — see page-transitions-2223.spec.ts), so this is the
// real product path.

type Rec = {
  created: boolean;
  atSnapshot: { saleScreen: boolean; menuScreen: boolean; pageH: number; mainH: number; scrollH: number } | null;
  atUpdateStart: { saleScreen: boolean; menuScreen: boolean; swapped: boolean } | null;
  ready: string;
  finished: string;
  anims: { pseudo: string; name: string; duration: number; easing: string }[];
  navDir: string | null;
};

async function instrument(page: import('@playwright/test').Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __vt: Rec | null; __arm: () => void };
    w.__arm = () => {
      w.__vt = { created: false, atSnapshot: null, atUpdateStart: null, ready: 'pending', finished: 'pending', anims: [], navDir: null };
    };
    w.__arm();
    const layout = () => {
      const pg = document.getElementById('ut-page')!.getBoundingClientRect();
      const main = document.querySelector('#ut-page > main')!.getBoundingClientRect();
      return {
        saleScreen: document.body.classList.contains('sale-screen'),
        menuScreen: document.body.classList.contains('menu-screen'),
        pageH: Math.round(pg.height),
        mainH: Math.round(main.height),
        scrollH: document.documentElement.scrollHeight,
      };
    };
    (w as unknown as { __layout: typeof layout }).__layout = layout;
    const orig = document.startViewTransition.bind(document);
    // Wraps base.html's wrapper (so the shipped watchdog and the deferred
    // shell sync both run); our callback runs INSIDE base.html's, i.e. after
    // its sync and before htmx's own swap.
    (document as unknown as { startViewTransition: unknown }).startViewTransition = (arg: unknown) => {
      const rec = w.__vt!;
      rec.created = true;
      requestAnimationFrame(() => { if (!rec.atSnapshot) rec.atSnapshot = layout(); });
      let a = arg;
      const wrap = (fn: () => unknown) => () => {
        rec.atUpdateStart = {
          saleScreen: document.body.classList.contains('sale-screen'),
          menuScreen: document.body.classList.contains('menu-screen'),
          swapped: !!document.querySelector('#ut-page a.menu-tile'),
        };
        const r = fn();
        return r;
      };
      if (typeof arg === 'function') a = wrap(arg as () => unknown);
      const vt = (orig as (x: unknown) => ViewTransition)(a);
      vt.ready.then(() => {
        rec.ready = 'resolved';
        rec.navDir = document.documentElement.getAttribute('data-nav-dir');
        rec.anims = document.getAnimations()
          .filter((x) => /::view-transition-(old|new)\(root\)/.test((x.effect as KeyframeEffect | null)?.pseudoElement || ''))
          .map((x) => ({
            pseudo: (x.effect as KeyframeEffect).pseudoElement || '',
            name: (x as CSSAnimation).animationName,
            duration: Number((x.effect as KeyframeEffect).getTiming().duration),
            easing: String((x.effect as KeyframeEffect).getTiming().easing),
          }));
      }, () => { rec.ready = 'rejected'; });
      vt.finished.then(() => { rec.finished = 'resolved'; }, () => { rec.finished = 'rejected'; });
      return vt;
    };
  });
}

const readRec = (page: import('@playwright/test').Page) =>
  page.evaluate(() => (window as unknown as { __vt: Rec }).__vt);

test.describe('page transition snapshots the old page before the shell sync (ut-docs#2496)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('Sell -> Menu keeps sale-screen and its layout in the old snapshot; Menu -> Sell pops', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const supported = await page.evaluate(() => typeof document.startViewTransition === 'function');
    test.skip(!supported, 'engine without same-document View Transitions');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await expect(page.locator('body')).toHaveClass(/sale-screen/);
    await instrument(page);
    const before = await page.evaluate(() => (window as unknown as { __layout: () => Rec['atSnapshot'] }).__layout());

    // --- push: Sell -> Menu
    await page.locator('[data-testid="nav-menu"]').click();
    await expect(page).toHaveURL(/\/menu$/);
    await expect.poll(async () => (await readRec(page)).finished).toBe('resolved');
    let rec = await readRec(page);
    expect(rec.created, 'the boosted hop runs as a same-document View Transition').toBe(true);
    expect(rec.atSnapshot, 'the old-state snapshot frame was observed').not.toBeNull();
    // The core regression: the outgoing page is captured as it LOOKED.
    expect(rec.atSnapshot!.saleScreen, `body.sale-screen must still be set when the old snapshot is captured (before click ${JSON.stringify(before)}, at snapshot ${JSON.stringify(rec.atSnapshot)})`).toBe(true);
    expect(rec.atSnapshot!.menuScreen, 'the incoming page\'s body class must not be applied before the old snapshot').toBe(false);
    expect({ pageH: rec.atSnapshot!.pageH, mainH: rec.atSnapshot!.mainH, scrollH: rec.atSnapshot!.scrollH },
      'no layout shift before the old snapshot').toEqual({ pageH: before!.pageH, mainH: before!.mainH, scrollH: before!.scrollH });
    // ...and the sync then happens inside the update callback, before the swap.
    expect(rec.atUpdateStart, 'shell sync runs at the start of the update callback').toEqual({ saleScreen: false, menuScreen: true, swapped: false });
    await expect(page.locator('body')).toHaveClass(/menu-screen/);
    await expect(page.locator('body')).not.toHaveClass(/sale-screen/);
    expect(rec.navDir).toBe('push');
    const byPseudo = (r: Rec) => Object.fromEntries(r.anims.map((a) => [a.pseudo, a]));
    let m = byPseudo(rec);
    expect(m['::view-transition-new(root)']?.name, 'incoming page slides in over the old one').toBe('ut-page-slide-in');
    expect(m['::view-transition-old(root)']?.name, 'outgoing page recedes').toBe('ut-page-recede');
    for (const a of rec.anims) {
      expect(a.duration, `${a.pseudo} duration`).toBeGreaterThanOrEqual(350);
      expect(a.duration, `${a.pseudo} duration`).toBeLessThanOrEqual(500);
    }

    // --- pop: Menu -> Sell reverses it
    await page.evaluate(() => (window as unknown as { __arm: () => void }).__arm());
    await page.locator('[data-testid="nav-till"]').click();
    await expect(page).toHaveURL(/\/$/);
    await expect.poll(async () => (await readRec(page)).finished).toBe('resolved');
    rec = await readRec(page);
    expect(rec.atSnapshot!.menuScreen, 'menu-screen still set at the old snapshot on the way back').toBe(true);
    expect(rec.atSnapshot!.saleScreen).toBe(false);
    expect(rec.navDir).toBe('pop');
    m = byPseudo(rec);
    expect(m['::view-transition-new(root)']?.name, 'the previous page comes back up from the receded state').toBe('ut-page-return');
    expect(m['::view-transition-old(root)']?.name, 'the page being left slides back out').toBe('ut-page-slide-out');
    await expect(page.locator('body')).toHaveClass(/sale-screen/);
    assertClean();
  });

  // ::view-transition { pointer-events: none } is the spec'd way to keep the
  // page live, but Chromium 141 still hit-tests every pointer to <html> while
  // a transition runs; base.html therefore ends the transition on the first
  // pointerdown and hands a mouse click to the element under the pointer.
  // Both input kinds are exercised: a touch tap is re-hit-tested at tap time
  // (native path), a mouse click needs the hand-over.
  // Freeze the motion the moment it starts, in the page itself: polling for
  // vt.ready from the test backs off (100/250/500 ms), long enough on a CI
  // runner for the whole 400 ms transition to finish before a test-side pause.
  async function freezeOnReady(page: import('@playwright/test').Page) {
    await page.evaluate(() => {
      const orig = document.startViewTransition.bind(document);
      (document as unknown as { startViewTransition: unknown }).startViewTransition = (...args: unknown[]) => {
        const vt = (orig as (...a: unknown[]) => ViewTransition)(...args);
        vt.ready.then(() => {
          document.getAnimations()
            .filter((a) => ((a.effect as KeyframeEffect | null)?.pseudoElement || '').startsWith('::view-transition'))
            .forEach((a) => a.pause());
        }, () => {});
        return vt;
      };
    });
  }

  async function tapDuringTransition(page: import('@playwright/test').Page, how: 'mouse' | 'touch') {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    const supported = await page.evaluate(() => typeof document.startViewTransition === 'function');
    test.skip(!supported, 'engine without same-document View Transitions');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await instrument(page);
    await freezeOnReady(page);
    await page.evaluate(() => {
      const w = window as unknown as { __hit: string[] };
      w.__hit = [];
      document.addEventListener('click', (e) => {
        if (!document.documentElement.classList.contains('__probe')) return;
        e.preventDefault();
        e.stopPropagation();
        const t = e.target as Element;
        w.__hit.push(t === document.documentElement ? 'html' : (t.closest('.menu-tile') ? 'menu-tile' : t.tagName.toLowerCase()));
      }, true);
    });

    await page.locator('[data-testid="nav-menu"]').click();
    // Hold the motion mid-flight (deterministic, independent of runner speed).
    await expect.poll(async () => (await readRec(page)).ready).toBe('resolved');
    await page.evaluate(() => {
      document.documentElement.classList.add('__probe');
    });
    const box = await page.locator('a.menu-tile').first().boundingBox();
    expect(box).not.toBeNull();
    const x = box!.x + box!.width / 2;
    const y = box!.y + box!.height / 2;
    expect((await readRec(page)).finished, 'the transition is still running when the operator taps').toBe('pending');
    if (how === 'mouse') await page.mouse.click(x, y);
    else await page.touchscreen.tap(x, y);
    await expect.poll(() => page.evaluate(() => (window as unknown as { __hit: string[] }).__hit.length)).toBeGreaterThan(0);
    const hits = await page.evaluate(() => (window as unknown as { __hit: string[] }).__hit);
    expect(hits, 'exactly one click, on the swapped-in page element, never on the ::view-transition overlay').toEqual(['menu-tile']);
    await expect.poll(async () => (await readRec(page)).finished, 'the tap ended the transition').not.toBe('pending');
    await page.evaluate(() => document.documentElement.classList.remove('__probe'));
  }

  test('a mouse click during the transition lands on the new page, never on the overlay', async ({ page }) => {
    const assertClean = watchConsole(page);
    await tapDuringTransition(page, 'mouse');
    assertClean();
  });

  test('a touch tap during the transition lands on the new page, never on the overlay', async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, hasTouch: true });
    const page = await ctx.newPage();
    const assertClean = watchConsole(page);
    try {
      await tapDuringTransition(page, 'touch');
      assertClean();
    } finally {
      await ctx.close();
    }
  });
  // Review M1: the trusted mousedown is hit-tested to <html> as well, so the
  // hand-over must focus the field under the pointer -- a mouse click on the
  // Sell screen's scan field mid-motion leaves the caret there.
  test('a mouse click on a field during the transition focuses it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/menu');
    await page.waitForLoadState('networkidle');
    const supported = await page.evaluate(() => typeof document.startViewTransition === 'function');
    test.skip(!supported, 'engine without same-document View Transitions');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await instrument(page);
    await freezeOnReady(page);
    await page.locator('[data-testid="nav-till"]').click();
    await expect.poll(async () => (await readRec(page)).ready).toBe('resolved');
    const box = await page.locator('input[name="code"]').first().boundingBox();
    expect(box).not.toBeNull();
    expect((await readRec(page)).finished, 'the transition is still running when the operator clicks').toBe('pending');
    await page.mouse.click(box!.x + box!.width / 2, box!.y + box!.height / 2);
    await expect.poll(() => page.evaluate(() => document.activeElement && document.activeElement.getAttribute('name'))).toBe('code');
    assertClean();
  });
});
