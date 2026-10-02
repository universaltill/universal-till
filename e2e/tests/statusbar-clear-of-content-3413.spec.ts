import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#3413: reported that the sticky `.statusbar` covers the last
// control on tall admin pages at narrow widths, once the page is scrolled
// to the end. It didn't reproduce: on admin pages the bar is
// `position: sticky; bottom: 0` and the last in-flow element of #ut-page
// (its sticky containing block), so at the end of the scroll it sits in its
// own row below <main>. The report's probe (elementFromPoint at
// innerHeight - 10) lands on that row. Only the phone sale screen takes the
// bar out of flow (`body.sale-screen`, app.css), and reserves its height
// there itself.
//
// This spec keeps admin pages that way: with every scroller run to its
// end, <main>'s content box must end at or above the bar. Taking the bar
// out of flow (fixed positioning, a negative margin) without reserving its
// height fails wherever the bar is taller than <main>'s 2rem bottom padding
// (always at 360px, where it wraps to two rows; at 1024x600 its one row fits
// in the padding, so no content is covered). Reserving it as <main>'s
// bottom padding or on <body> passes. `overlap` is the load-bearing
// assertion; `covered` names the controls a shop owner would actually lose,
// for the failure message.

const ROUTES = ['/items', '/catalog', '/catalog/option-sets', '/inventory', '/settings', '/reports',
  '/users/permissions', '/translations', '/audit', '/menu'];

const VIEWPORTS = [
  { width: 360, height: 740, phone: true },
  { width: 1024, height: 600, phone: false },
];

async function coveredByStatusbar(page: Page) {
  return page.evaluate(async () => {
    const bar = document.querySelector('.statusbar');
    const main = document.querySelector('main');
    if (!bar || !main || getComputedStyle(bar).display === 'none') return { skipped: true, overlap: 0, covered: [] as string[] };
    // Run the page and every box that scrolls vertically to its end.
    const scrollers = [document.scrollingElement as Element, main, ...Array.from(main.querySelectorAll('*'))].filter((el) =>
      el === document.scrollingElement || (/(auto|scroll)/.test(getComputedStyle(el).overflowY) && el.scrollHeight > el.clientHeight + 1));
    scrollers.forEach((el) => { el.scrollTop = el.scrollHeight; });
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    const barTop = bar.getBoundingClientRect().top;
    const contentBottom = main.getBoundingClientRect().bottom - parseFloat(getComputedStyle(main).paddingBlockEnd);
    const covered: string[] = [];
    main.querySelectorAll('a[href], button, input:not([type=hidden]), select, textarea, summary').forEach((el) => {
      if (el.closest('details:not([open]) > :not(summary), dialog:not([open]), [hidden], #osk')) return;
      const s = getComputedStyle(el);
      if (s.visibility === 'hidden' || s.display === 'none') return;
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height || r.bottom <= barTop + 1 || r.top >= innerHeight) return;
      const hit = document.elementFromPoint(r.left + r.width / 2, Math.min(r.bottom - 1, innerHeight - 1));
      if (hit && hit.closest('.statusbar')) {
        covered.push(`${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}.${String(el.className).trim().split(/\s+/)[0]} [${Math.round(r.top)},${Math.round(r.bottom)}] bar=${Math.round(barTop)}`);
      }
    });
    return { skipped: false, overlap: Math.round(contentBottom - barTop), covered: covered.slice(0, 5) };
  });
}

for (const vp of VIEWPORTS) {
  test.describe(`status bar clear of page content at ${vp.width}x${vp.height} (ut-docs#3413)`, () => {
    test.use({ viewport: { width: vp.width, height: vp.height }, hasTouch: vp.phone, isMobile: vp.phone });
    for (const r of ROUTES) {
      test(`${r}: nothing in <main> sits under the status bar at the end of the scroll`, async ({ page }) => {
        const assertClean = watchConsole(page);
        const resp = await page.goto(r);
        expect(resp?.status(), `${r}: HTTP status`).toBe(200);
        // A redirect to another page would silently test the wrong one.
        expect(new URL(page.url()).pathname, `${r}: redirected`).toBe(r);
        // Not 'networkidle': the status bar polls forever.
        await page.waitForLoadState('load');
        await page.waitForTimeout(500);
        const res = await coveredByStatusbar(page);
        expect(res.skipped, `${r}: no visible .statusbar`).toBe(false);
        expect(res.overlap, `${r}: <main>'s content runs under the status bar`).toBeLessThanOrEqual(1);
        expect(res.covered, `${r}: controls covered by the status bar`).toEqual([]);
        assertClean();
      });
    }
  });
}
