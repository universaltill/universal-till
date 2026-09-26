import { test, expect } from './fixtures';
import { watchConsole } from './helpers';
import { WORKER_TILL_EFFECTS_LEVEL } from './worker-till';

// ut-docs#2859 / ADR-0119: the per-till visual effects level. Under Light
// the ADR-0118 page motion must not even be CREATED -- htmx:beforeTransition
// is cancelled through UT.motionOff(), so no View Transition and no
// whole-page snapshot (the costly part on a Pi) ever happens -- while Full
// still runs it, and Balanced runs it at 200 ms.
//
// The e2e suite runs as a reduced-motion user (fixtures.ts), under which
// motion is off at EVERY level; without first emulating 'no-preference'
// the Light assertion would pass vacuously. Every test does that before it
// navigates, and the Full case proves the transition really does run in
// this engine with the same instrumentation.
//
// Headless Chromium runs same-document View Transitions (boosted shell
// navigation, ADR-0098) -- see page-snapshot-no-jump-2496.spec.ts -- so
// this is the real product path.

type Rec = { created: number; durations: number[] };

async function setLevel(page: import('@playwright/test').Page, level: string) {
  const res = await page.request.post('/api/settings/effects-level', { form: { level } });
  expect(res.status(), `POST effects-level=${level}`).toBe(204);
}

// Count every document.startViewTransition call (wrapping base.html's own
// wrapper, so the shipped watchdog still runs) and record the root pair's
// animation durations once the motion starts.
async function instrument(page: import('@playwright/test').Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __fx: Rec; __fxMarker: boolean };
    w.__fx = { created: 0, durations: [] };
    w.__fxMarker = true; // survives a boosted swap, gone after a full load
    if (typeof document.startViewTransition !== 'function') return;
    const orig = document.startViewTransition.bind(document);
    (document as unknown as { startViewTransition: unknown }).startViewTransition = (arg: unknown) => {
      w.__fx.created++;
      const vt = (orig as (x: unknown) => ViewTransition)(arg);
      vt.ready.then(() => {
        w.__fx.durations = document.getAnimations()
          .filter((x) => /::view-transition-(old|new)\(root\)/.test((x.effect as KeyframeEffect | null)?.pseudoElement || ''))
          .map((x) => Number((x.effect as KeyframeEffect).getTiming().duration));
      }, () => {});
      return vt;
    };
  });
}

async function boostedHopToMenu(page: import('@playwright/test').Page): Promise<Rec> {
  await page.locator('[data-testid="nav-menu"]').click();
  await expect(page).toHaveURL(/\/menu$/);
  await expect(page.locator('body')).toHaveClass(/menu-screen/);
  // Let a transition (if any) reach ready so its durations are recorded.
  await page.waitForTimeout(700);
  const marker = await page.evaluate(() => (window as unknown as { __fxMarker?: boolean }).__fxMarker === true);
  expect(marker, 'the hop must be a boosted (same-document) navigation, not a full load').toBe(true);
  return page.evaluate(() => (window as unknown as { __fx: Rec }).__fx);
}

async function openSell(page: import('@playwright/test').Page, level: string) {
  await page.goto('/');
  await page.waitForLoadState('networkidle');
  await expect(page.locator('html')).toHaveClass(new RegExp(`(^|\\s)fx-${level}(\\s|$)`));
  const supported = await page.evaluate(() => typeof document.startViewTransition === 'function');
  test.skip(!supported, 'engine without same-document View Transitions');
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await instrument(page);
}

test.describe('visual effects level gates the page motion (ut-docs#2859, ADR-0119)', () => {
  test.afterEach(async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await setLevel(page, WORKER_TILL_EFFECTS_LEVEL);
    await page.request.post('/api/pos/reset');
  });

  test('Full: a boosted hop runs the ADR-0118 View Transition', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setLevel(page, 'full');
    await openSell(page, 'full');
    expect(await page.evaluate(() => (window as unknown as { UT: { fxLevel: string; motionOff: () => boolean } }).UT.fxLevel)).toBe('full');
    expect(await page.evaluate(() => (window as unknown as { UT: { motionOff: () => boolean } }).UT.motionOff())).toBe(false);
    const rec = await boostedHopToMenu(page);
    expect(rec.created, 'Full creates the View Transition').toBeGreaterThan(0);
    expect(rec.durations.length, 'the root pair animated').toBeGreaterThan(0);
    for (const d of rec.durations) expect(d).toBeGreaterThanOrEqual(350);
    assertClean();
  });

  test('Balanced: the page push runs at 200 ms', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setLevel(page, 'balanced');
    await openSell(page, 'balanced');
    const rec = await boostedHopToMenu(page);
    expect(rec.created).toBeGreaterThan(0);
    expect(rec.durations.length).toBeGreaterThan(0);
    for (const d of rec.durations) {
      expect(d).toBeGreaterThanOrEqual(150);
      expect(d).toBeLessThanOrEqual(250);
    }
    assertClean();
  });

  test('Light: no View Transition is ever created, flat paint, no opt-in', async ({ page }) => {
    const assertClean = watchConsole(page);
    await setLevel(page, 'light');
    await openSell(page, 'light');
    // Server layer: no @view-transition opt-in rendered at all.
    const optIn = await page.evaluate(() => Array.from(document.querySelectorAll('style')).some((s) => /@view-transition/.test(s.textContent || '')));
    expect(optIn, 'the inline @view-transition opt-in must not be rendered under Light').toBe(false);
    // JS layer: one predicate, true under Light even with motion allowed.
    const ut = await page.evaluate(() => {
      const U = (window as unknown as { UT: { fxLevel: string; motionOff: () => boolean } }).UT;
      return { level: U.fxLevel, off: U.motionOff(), reduce: matchMedia('(prefers-reduced-motion: reduce)').matches };
    });
    expect(ut).toEqual({ level: 'light', off: true, reduce: false });
    // CSS layer: shadows and transitions gone on a real tile.
    const tile = page.locator('.btn-tile').first();
    await expect(tile).toBeVisible();
    const css = await tile.evaluate((el) => {
      const cs = getComputedStyle(el);
      return { shadow: cs.boxShadow, transition: cs.transitionDuration };
    });
    expect(css.shadow).toBe('none');
    expect(css.transition.split(',').every((d) => d.trim() === '0s')).toBe(true);

    const rec = await boostedHopToMenu(page);
    expect(rec.created, 'Light must not create a View Transition (no whole-page snapshot)').toBe(0);
    expect(rec.durations).toEqual([]);

    // The cross-document path too: a pagereveal carrying a transition is
    // skipped by the shipped listener under Light.
    const skipped = await page.evaluate(() => {
      let s = false;
      const ev = new Event('pagereveal') as Event & { viewTransition?: unknown };
      ev.viewTransition = { skipTransition: () => { s = true; }, types: { add: () => {} } };
      window.dispatchEvent(ev);
      return s;
    });
    expect(skipped, 'pagereveal must skip the transition under Light').toBe(true);
    assertClean();
  });
});
