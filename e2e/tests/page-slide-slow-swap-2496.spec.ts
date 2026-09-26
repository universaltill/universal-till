import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2496: the ADR-0097 page slide stopped playing on the pilot tablet.
// Root cause: the "never hold the screen" watchdog was armed when the View
// Transition was CREATED, and a boosted (ADR-0098) hop creates it before
// htmx swaps the page in — on a slow device the swap alone used most of the
// then-600ms budget, so the then-200ms slide was cut short or skipped
// outright. (ADR-0118 since: 400ms motion, 1000ms post-ready watchdog.)
//
// This spec stands in for the slow device deterministically: a listener
// busies the main thread for 750ms inside the transition's update callback
// (htmx fires afterSwap there), i.e. longer than the old 600ms budget. The
// slide must still start (ready resolves) and play to the end without the
// watchdog skipping it. Headless Chromium runs same-document transitions
// (unlike cross-document ones — see fixtures.ts), so this is the real
// product path, not a synthetic event.
test.describe('page slide survives a slow swap (ut-docs#2496)', () => {
  test('a boosted Menu -> Reports hop plays the slide even when the swap takes 750ms', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    await page.waitForLoadState('networkidle');
    const supported = await page.evaluate(() => typeof document.startViewTransition === 'function');
    test.skip(!supported, 'engine without same-document View Transitions');
    await page.emulateMedia({ reducedMotion: 'no-preference' });

    await page.evaluate(() => {
      const w = window as unknown as { __vt: Record<string, unknown> };
      const rec: Record<string, unknown> = { created: false, ready: 'pending', finished: 'pending', skipped: false, slide: [], stalledInside: false, readyAfterMs: -1 };
      w.__vt = rec;
      const orig = document.startViewTransition.bind(document);
      // Wraps base.html's wrapper, so the transition carries the shipped
      // watchdog; skipTransition is looked up at call time, so this sees it.
      (document as unknown as { startViewTransition: unknown }).startViewTransition = (...args: unknown[]) => {
        const vt = (orig as (...a: unknown[]) => ViewTransition)(...args);
        const t0 = performance.now();
        rec.created = true;
        rec.cbPending = true;
        vt.updateCallbackDone.then(() => { rec.cbPending = false; }, () => { rec.cbPending = false; });
        const skip = vt.skipTransition.bind(vt);
        vt.skipTransition = () => { rec.skipped = true; skip(); };
        vt.ready.then(() => {
          rec.ready = 'resolved';
          rec.readyAfterMs = Math.round(performance.now() - t0);
          rec.slide = document.getAnimations()
            .filter((a) => (a.effect as KeyframeEffect | null)?.pseudoElement === '::view-transition-new(root)')
            .map((a) => (a as CSSAnimation).animationName);
        }, () => { rec.ready = 'rejected'; });
        vt.finished.then(() => { rec.finished = 'resolved'; }, () => { rec.finished = 'rejected'; });
        return vt;
      };
      // Only the boosted #ut-page swap: /menu's load/poll chips also bubble
      // afterSwap to document, and stalling one of those instead would let
      // this spec pass on the unfixed watchdog (review finding).
      const stall = (e: Event) => {
        const d = (e as CustomEvent).detail || {};
        if (!d.requestConfig?.boosted || d.target?.id !== 'ut-page') return;
        document.removeEventListener('htmx:afterSwap', stall);
        // Proves the stall runs INSIDE the transition's update callback.
        rec.stalledInside = rec.created === true && rec.cbPending === true;
        const until = performance.now() + 750;
        while (performance.now() < until) { /* a slow tablet's swap */ }
      };
      document.addEventListener('htmx:afterSwap', stall);
    });

    await page.locator('a.menu-tile[href="/reports"]').click();
    await expect(page).toHaveURL(/\/reports$/);
    await expect.poll(() => page.evaluate(() => (window as unknown as { __vt: { finished: string } }).__vt.finished)).toBe('resolved');

    const rec = await page.evaluate(() => (window as unknown as { __vt: Record<string, unknown> }).__vt);
    expect(rec.created, 'the boosted hop runs as a same-document View Transition').toBe(true);
    expect(rec.stalledInside, 'the 750ms stall ran inside the transition update callback').toBe(true);
    expect(rec.ready, 'the slide starts after the slow swap instead of being skipped').toBe('resolved');
    expect(rec.slide).toContain('ut-page-slide-in');
    expect(rec.skipped, `the watchdog must not cut a slide that started late (create->ready ${rec.readyAfterMs}ms; a value near 2000 means the runner hit the creation backstop)`).toBe(false);
    assertClean();
  });
});
