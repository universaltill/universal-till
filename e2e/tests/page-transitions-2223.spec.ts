import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#2223: page & menu transitions "like an app" — cross-document CSS
// View Transitions with push/pop direction, named groups so the fixed nav
// rail/statusbar don't animate, and a zero-latency in-page ease on htmx
// swaps. See internal/pages/transitions_test.go for the static source
// guards (the CSS/markup actually exists); this file proves the BEHAVIOUR
// in a real Chromium.
//
// Every test is guarded with the same feature check base.html's own
// pagereveal script uses — an engine without View Transitions support must
// fall back to today's instant swap, and this suite must not fail on one.
async function supportsViewTransitions(page: import('@playwright/test').Page): Promise<boolean> {
  return page.evaluate(() =>
    'navigation' in window && !!window.CSS && CSS.supports('view-transition-name: x')
  );
}

// What Playwright's headless Chromium does and does not give us — corrected
// against the real devices, 2026-09-16 (Tester pass on the pilot tablet,
// Chrome Android 153, and the Pi 5's WebKitGTK 2.52):
//
// * On both real engines `pagereveal` fires on EVERY cross-document
//   navigation, carrying `e.viewTransition`, and the shipped listener sets
//   `data-nav-dir` correctly on every hop (Menu->Reports push,
//   Reports->Menu pop, Sell<->Menu, Settings, browser back). So the
//   "pagereveal only fires once" observation the first cut of this spec
//   recorded is a property of this automation environment, not of the
//   product or of Chromium in general.
// * Under Playwright's headless Chromium the cross-document view transition
//   itself does not run for CDP-driven navigations, so `pagereveal` arrives
//   with no `viewTransition` and the listener (correctly) does nothing. That
//   is why this helper drives a REAL navigation (so `navigation.activation`
//   is the browser's genuine value for that hop) and then dispatches a
//   `pagereveal`-shaped event by hand: base.html's real, shipped listener
//   still does the real work — reading the real `navigation.activation`,
//   running the real depth/direction heuristic, and setting the real
//   `data-nav-dir` + `vt.types.add('back')`. Only the browser's own event
//   delivery is stood in for.
// * The real-device pass is what proves the transition runs: it is on the
//   card (ut-docs#2223) with frame timing, and it is also what found the
//   inline-opt-in race in base.html (see the Go guard
//   TestBaseHTMLCarriesTheViewTransitionOptInInline) — a failure mode this
//   spec structurally cannot see, because the transition never runs here.
async function revealAndReadDirection(page: import('@playwright/test').Page): Promise<{ dir: string | null; typesAdded: string[] }> {
  // The navigation itself ran as a reduced-motion user (fixtures.ts), so no
  // real transition was started; the shipped listener bails out under that
  // media query, so flip it to no-preference for the synthetic dispatch
  // only — `matchMedia().matches` is live — and back afterwards.
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  const r = await page.evaluate(() => {
    const typesAdded: string[] = [];
    const fakeViewTransition = { types: { add: (t: string) => typesAdded.push(t) } };
    const ev = new Event('pagereveal') as Event & { viewTransition?: unknown };
    ev.viewTransition = fakeViewTransition;
    window.dispatchEvent(ev);
    return { dir: document.documentElement.getAttribute('data-nav-dir'), typesAdded };
  });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  return r;
}

// A second automation-only artifact, recorded by the first cut of this
// spec: after a link navigation in this headless Chromium, Playwright's
// actionability wait (`elementFromPoint` must resolve to the target,
// stably) sometimes never resolved again on the new document, while a
// JS-dispatched `el.click()` still worked. Its cause was not established —
// and since no cross-document transition runs here (see above), it is NOT
// the transition overlay. On the real tablet six consecutive real touch
// taps across transitioned pages all navigated (Tester pass, 2026-09-16).
// The tests below simply never hit-test a second link on the same
// document: a fresh `page`, `goBack`, or a JS-dispatched click instead.
test.describe('cross-document page transition direction (ut-docs#2223)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  // ut-docs#2224 / ADR-0098: a rail/tile tap is now a BOOSTED navigation —
  // the document persists and the shell script sets `data-nav-dir` on the
  // swap itself (motion or not), so the direction is read straight off
  // <html> once the URL has changed. Browser back is a full reload
  // (refreshOnHistoryMiss), i.e. still the cross-document path, so that hop
  // keeps the synthetic-pagereveal read.
  const boostedDir = async (page: import('@playwright/test').Page, url: RegExp) => {
    await expect(page).toHaveURL(url);
    return page.evaluate(() => document.documentElement.getAttribute('data-nav-dir'));
  };

  test('Menu -> Reports is push; browser back from there is pop', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    test.skip(!(await supportsViewTransitions(page)), 'browser has no View Transitions support');

    // Menu (depth 1) -> Reports (depth 2): push, boosted.
    await page.locator('.menu-tile[href="/reports"]').click();
    expect(await boostedDir(page, /\/reports/)).toBe('push');

    // Browser back after a boosted hop is a full reload of the local
    // server's page (ADR-0098 rule 5: no client history cache). A reload is
    // not a move anywhere, so the shipped pagereveal listener deliberately
    // skips the motion and sets no direction — the same hard cut a reload
    // always had.
    await page.goBack();
    await page.waitForLoadState('load');
    await expect(page).toHaveURL(/\/menu/);
    const r = await revealAndReadDirection(page);
    expect(r.dir).toBeNull();
    expect(r.typesAdded).toEqual([]);

    assertClean();
  });

  test('Reports -> Menu via the rail is pop (shallower)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/reports');
    test.skip(!(await supportsViewTransitions(page)), 'browser has no View Transitions support');

    await page.locator('[data-testid="nav-menu"]').click();
    expect(await boostedDir(page, /\/menu/)).toBe('pop');

    assertClean();
  });

  test('Sell -> Menu (rail) is push', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    test.skip(!(await supportsViewTransitions(page)), 'browser has no View Transitions support');

    await page.locator('[data-testid="nav-menu"]').click();
    expect(await boostedDir(page, /\/menu/)).toBe('push');

    assertClean();
  });

  test('Menu -> Sell (back-to-sale) is pop', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    test.skip(!(await supportsViewTransitions(page)), 'browser has no View Transitions support');

    await page.locator('[data-testid="nav-till"]').click();
    expect(await boostedDir(page, /\/$/)).toBe('pop');

    assertClean();
  });
});

test.describe('only the fixed nav rail / statusbar are named view-transition groups (ut-docs#2223)', () => {
  test('computed view-transition-name matches the design tokens', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');

    const names = await page.evaluate(() => ({
      rail: getComputedStyle(document.querySelector('.nav') as Element).viewTransitionName,
      statusbar: getComputedStyle(document.querySelector('.statusbar') as Element).viewTransitionName,
      page: getComputedStyle(document.querySelector('main.container') as Element).viewTransitionName,
    }));
    expect(names.rail).toBe('ut-rail');
    expect(names.statusbar).toBe('ut-statusbar');
    // <main> must NOT be named: a view-transition-name makes its element
    // the containing block for every position:fixed descendant, and the
    // payment overlay / hold / elevation dialogs all live inside <main>
    // (bugreport-panel.spec.ts is the regression test that caught it).
    expect(names.page).toBe('none');

    assertClean();
  });
});

test.describe('RTL mirrors the nav-direction variable (ut-docs#2223)', () => {
  test('--ut-nav-dir is -1 under lang=fa', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');

    const navDir = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--ut-nav-dir').trim()
    );
    expect(navDir).toBe('-1');

    assertClean();
  });
});

// The ease is proven by the ANIMATION actually starting (`animationstart`
// with animationName ut-swap-in), never by a class appearing: htmx's settle
// step restores an id-matched target's attributes right after afterSwap, so
// a class added too early is wiped before the first frame — a
// MutationObserver still sees the transient add and passes (which is
// exactly how the first cut of this spec passed with an ease that never
// ran once). Installed at document start so `load`-triggered swaps are
// covered too.
const animRecorder = () => {
  (window as unknown as { __swapAnims: string[] }).__swapAnims = [];
  document.addEventListener('animationstart', (e) => {
    const ae = e as AnimationEvent;
    if (ae.animationName === 'ut-swap-in') {
      const el = ae.target as Element;
      (window as unknown as { __swapAnims: string[] }).__swapAnims.push(el.id || el.tagName + '.' + el.className);
    }
  }, true);
};
const swapAnims = (page: import('@playwright/test').Page) =>
  page.evaluate(() => (window as unknown as { __swapAnims: string[] }).__swapAnims.slice());

test.describe('reduced motion kills the in-page swap ease and pre-existing motion (ut-docs#2223)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('no ut-swap-in animation on an htmx-swapped #basket, and .menu-tile transitions are instant', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(animRecorder);
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/menu');

    const tileDuration = await page.evaluate(() => {
      const tile = document.querySelector('.menu-tile') as Element;
      return getComputedStyle(tile).transitionDuration;
    });
    expect(tileDuration).toBe('0s');

    await page.goto('/');
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await page.waitForTimeout(250);
    expect(await swapAnims(page), 'ut-swap-in must never run under prefers-reduced-motion: reduce').toEqual([]);

    assertClean();
  });

  test('WITHOUT reduced motion, an htmx-swapped #basket really runs ut-swap-in', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(animRecorder);
    // The suite runs as a reduced-motion user (playwright.config.ts); this
    // case is about the ease actually running, so opt back in.
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.goto('/');
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect.poll(async () => (await swapAnims(page)).includes('basket')).toBe(true);

    assertClean();
  });
});

test.describe('a second navigation interrupts an in-flight transition cleanly (ut-docs#2223)', () => {
  test('navigating away mid-transition still lands on the final URL, interactive', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    test.skip(!(await supportsViewTransitions(page)), 'browser has no View Transitions support');

    await page.locator('.menu-tile[href="/reports"]').click();
    // Immediately interrupt with a second, different navigation — do not
    // await the boosted swap first, that's the point of this test.
    await page.goto('/settings');
    await page.waitForLoadState('load');
    await expect(page).toHaveURL(/\/settings/);
    // The page must be genuinely interactive, not hung mid-transition.
    await expect(page.locator('.nav')).toBeVisible();

    assertClean();
  });
});

test.describe('only operator-caused swaps ease; polls, load triggers and hx-swap=none do not (ut-docs#2223, review)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('a `load`/`every` poll swap never eases; a user swap on the same page does', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(animRecorder);
    await page.emulateMedia({ reducedMotion: 'no-preference' }); // suite default is 'reduce'
    await page.goto('/');
    // base.html's #pairing-notice-mount polls with hx-trigger="load, every 30s";
    // the sale screen's basket/buttons/held-sales/chips all fetch on load.
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(300);
    expect(await swapAnims(page), 'no load-triggered swap may pulse — the rail chips and polled lists must read as fixed').toEqual([]);

    // Control: the SAME recorder sees the operator's swap, so the empty
    // list above is evidence, not a dead recorder.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect.poll(async () => (await swapAnims(page)).includes('basket')).toBe(true);
    assertClean();
  });

  test('hx-swap="none" fires afterSettle on its issuing element but must not ease it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.addInitScript(animRecorder);
    await page.emulateMedia({ reducedMotion: 'no-preference' }); // suite default is 'reduce'
    await page.goto('/menu');
    // Two probes processed by the live htmx: one swaps nothing, one swaps a
    // target. Both POST the same harmless endpoint.
    await page.evaluate(() => {
      const host = document.createElement('div');
      host.innerHTML =
        '<button id="probe-none" hx-post="/api/pos/reset" hx-swap="none">none</button>' +
        '<button id="probe-swap" hx-post="/api/pos/reset" hx-swap="innerHTML" hx-target="#probe-target">swap</button>' +
        '<div id="probe-target"></div>';
      document.querySelector('main')!.appendChild(host);
      (window as unknown as { htmx: { process: (el: Element) => void } }).htmx.process(host);
    });
    await page.locator('#probe-none').click();
    await page.waitForTimeout(300);
    expect(await swapAnims(page)).not.toContain('probe-none');
    await page.locator('#probe-swap').click();
    await expect.poll(async () => (await swapAnims(page)).includes('probe-target')).toBe(true);
    assertClean();
  });
});

test.describe('a reload is not a navigation direction (ut-docs#2223, review)', () => {
  test('after page.reload() the shipped listener skips the transition and sets no direction', async ({ page }) => {
    test.skip(!(await supportsViewTransitions(page)), 'engine without View Transitions / Navigation API');
    await page.goto('/menu');
    await page.reload();
    await page.emulateMedia({ reducedMotion: 'no-preference' }); // so the skip below is the RELOAD branch, not reduced motion's
    // navigation.activation.navigationType is the browser's genuine 'reload'
    // here; only the event delivery is stood in for (see the note at the top).
    const r = await page.evaluate(() => {
      let skipped = 0;
      const fake = { types: { add: () => {} }, skipTransition: () => { skipped++; } };
      const ev = new Event('pagereveal') as Event & { viewTransition?: unknown };
      ev.viewTransition = fake;
      window.dispatchEvent(ev);
      return { type: (window as unknown as { navigation: { activation: { navigationType: string } } }).navigation.activation.navigationType, skipped, dir: document.documentElement.getAttribute('data-nav-dir') };
    });
    expect(r.type).toBe('reload');
    expect(r.skipped).toBe(1);
    expect(r.dir).toBeNull();
  });
});
