import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2869: back-to-back boosted navigations. Under a View Transition
// htmx swaps in the transition's update callback, a frame or more after
// htmx:beforeSwap, and it resolves HX-Retarget (#ut-page) when the response
// arrives. When the second response (B) arrived before the first
// transition's (A) update callback had run, A swapped in and replaced
// #ut-page, then B's swap hit the detached element and threw
// (htmx:swapError): the till showed A's content under B's URL and body
// class, with A's page-level listeners live. Now the later tap wins: A's
// queued swap is skipped and the DOM goes straight to B.
//
// The interleaving depends on frame timing, so the first two tests install a
// startViewTransition stand-in (before base.html wraps it) that queues each
// update callback until the test releases it. That pins the exact order
// above without changing anything else the shell does. Sell (index.html)
// adds body-level htmx:afterSwap listeners in its inline scripts; Menu adds
// none, so after landing on Menu the live count must match where it started.
// The third test drives the same race with the browser's real View
// Transitions by holding both responses and releasing them back to back. It
// is the real-engine sanity check, not the regression guard: on a slow
// runner A's update callback can run before B's response is handled, and
// then it passes without the fix too. The second test is the guard.

async function installQueuedTransitions(page: Page) {
  // fixtures.ts forces reduced motion, under which the shell cancels the
  // transition and swaps synchronously; this spec needs the transition path.
  // The stand-in below never creates a real cross-document transition, so
  // fixtures.ts's headless-reveal hang does not apply.
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await page.addInitScript(() => {
    const w = window as any;
    w.__ll = { add: 0, rem: 0 };
    const a = EventTarget.prototype.addEventListener, r = EventTarget.prototype.removeEventListener;
    const tracked = (t: EventTarget) => t === document.body || t === document || t === window;
    EventTarget.prototype.addEventListener = function (type: string, fn: any, o?: any) {
      if (tracked(this) && type === 'htmx:afterSwap') w.__ll.add++;
      return a.call(this, type, fn, o);
    };
    EventTarget.prototype.removeEventListener = function (type: string, fn: any, o?: any) {
      if (tracked(this) && type === 'htmx:afterSwap') w.__ll.rem++;
      return r.call(this, type, fn, o);
    };
    const queue: Array<() => void> = [];
    w.__vtQueued = () => queue.length;
    w.__vtRelease = () => { while (queue.length) queue.shift()!(); };
    (document as any).startViewTransition = function (arg: any) {
      const update = typeof arg === 'function' ? arg : arg && arg.update;
      let settle: () => void = () => {};
      const done = new Promise<void>((res) => { settle = res; });
      queue.push(() => { Promise.resolve(update && update()).then(settle, settle); });
      return { ready: done, updateCallbackDone: done, finished: done, skipTransition() {} };
    };
  });
}

const liveAfterSwap = (page: Page) =>
  page.evaluate(() => { const l = (window as any).__ll; return l.add - l.rem; });
const queued = (page: Page) => page.evaluate(() => (window as any).__vtQueued() as number);
const release = (page: Page) => page.evaluate(() => (window as any).__vtRelease());

test.describe('shell listener drop under overlapping transitions (ut-docs#2869)', () => {
  test('sequential Sell -> Menu leaves no Sell listeners (control)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await installQueuedTransitions(page);
    await page.goto('/menu');
    const base = await liveAfterSwap(page);

    await page.locator('[data-testid="nav-till"]').click();
    await expect.poll(() => queued(page)).toBe(1);
    await release(page);
    await expect(page.locator('body')).toHaveClass(/sale-screen/);
    expect(await liveAfterSwap(page), 'Sell registers page-level afterSwap listeners').toBeGreaterThan(base);

    await page.locator('[data-testid="nav-menu"]').click();
    await expect.poll(() => queued(page)).toBe(1);
    await release(page);
    await expect(page.locator('body')).toHaveClass(/menu-screen/);
    expect(await liveAfterSwap(page)).toBe(base);
    assertClean();
  });

  test('a second response before the first swap lands on the second page, with no first-page listeners', async ({ page }) => {
    const assertClean = watchConsole(page);
    await installQueuedTransitions(page);
    await page.goto('/menu');
    const base = await liveAfterSwap(page);

    // A: Menu -> Sell. Its response arrives and its transition is queued,
    // but its swap has not run yet.
    await page.locator('[data-testid="nav-till"]').click();
    await expect.poll(() => queued(page)).toBe(1);
    // B: -> Menu, answered before A's update callback runs.
    await page.locator('[data-testid="nav-menu"]').click();
    await expect.poll(() => queued(page)).toBe(2);
    // A's update callback runs first, then B's.
    await release(page);

    await expect(page).toHaveURL(/\/menu$/);
    await expect(page.locator('body')).toHaveClass(/menu-screen/);
    await expect(page.locator('#ut-page .menu-tile').first(), 'Menu\'s content, not Sell\'s, is on screen').toBeVisible();
    await expect(page.locator('#ut-page')).toHaveCount(1);
    expect(await liveAfterSwap(page), 'Sell\'s listeners must not outlive its swap-out').toBe(base);
    assertClean();
  });

  test('real View Transitions: two responses released back to back land on the second page', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.addInitScript(() => {
      const w = window as any;
      w.__swapErrors = 0;
      document.addEventListener('htmx:swapError', () => { w.__swapErrors++; });
    });
    await page.goto('/orders');
    const held: Array<() => Promise<void>> = [];
    await page.route((u) => u.pathname === '/' || u.pathname === '/menu', async (route) => {
      if (route.request().headers()['hx-boosted'] !== 'true') return route.continue();
      const resp = await route.fetch();
      held.push(() => route.fulfill({ response: resp }));
    });
    await page.locator('[data-testid="nav-till"]').click();
    await page.locator('[data-testid="nav-menu"]').click();
    await expect.poll(() => held.length).toBe(2);
    await held[0]();
    await held[1]();

    await expect(page).toHaveURL(/\/menu$/);
    await expect(page.locator('body')).toHaveClass(/menu-screen/);
    await expect(page.locator('#ut-page .menu-tile').first()).toBeVisible();
    expect(await page.evaluate(() => (window as any).__swapErrors)).toBe(0);
    await page.unrouteAll({ behavior: 'ignoreErrors' });
    assertClean();
  });
});
