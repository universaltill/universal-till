import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4628: at 800x1280 (10" tablet portrait) the two-pane Settings shell
// kept the section nav beside the panel, leaving the panel ~355px wide: the
// Payments fee rows scrolled sideways inside their own box and the printer
// card's discovery help was squeezed to ~150px. Below 900px wide Settings now
// uses the phone's list-first pattern (the list, then the section with a back
// button) so the section gets the full width. At 1024x600 (landscape kiosk)
// the panes stay side by side.
//
// German is a language-pack plugin: without UT_TEST_I18N_OVERLAY_DIR, ?lang=de
// renders English (see portrait-no-clip-sweep-3060.spec.ts). fa is core.

const VIEWPORTS = [
  { width: 800, height: 1280 },
  { width: 600, height: 960 },
];

for (const vp of VIEWPORTS) {
  for (const lang of ['de', 'fa']) {
    test(`${vp.width}x${vp.height} ${lang}: every Settings section gets the full width and nothing scrolls sideways`, async ({ page }) => {
      test.setTimeout(120_000);
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto(`/settings?lang=${lang}`);
      await expect(page.locator('html')).toHaveAttribute('dir', lang === 'fa' ? 'rtl' : 'ltr');

      const sections = await page.locator('#settings-nav-index li').evaluateAll((lis) =>
        lis.map((li) => li.getAttribute('data-key') || '').filter((k) => k && document.getElementById(k)));
      expect(sections.length, 'settings sections').toBeGreaterThan(10);

      const fails: string[] = [];
      for (const id of sections) {
        await page.evaluate((id) => { location.hash = id; }, id);
        await expect(page.locator(`[id="${id}"]`)).toBeVisible();
        const tag = `${vp.width}x${vp.height} ${lang} #${id}`;

        // (a) list-first: the nav is gone while a section is open, back shows.
        if (await page.locator('.settings-nav').isVisible()) fails.push(`${tag}: nav still displayed beside the section`);
        if (!(await page.locator('#settings-back').isVisible())) fails.push(`${tag}: back button not visible`);

        const r = await page.evaluate((id) => {
          const out: string[] = [];
          const card = document.getElementById(id)!;
          const shell = document.getElementById('settings-shell')!.getBoundingClientRect();
          const panel = document.getElementById('settings-panel')!.getBoundingClientRect();
          const cb = card.getBoundingClientRect();
          const rtl = document.documentElement.dir === 'rtl';
          // (b) the panel is (nearly) as wide as the shell.
          if (panel.width < shell.width * 0.9) out.push(`panel ${Math.round(panel.width)}px < 90% of shell ${Math.round(shell.width)}px`);
          const name = (e: Element) => `${e.tagName.toLowerCase()}${e.id ? '#' + e.id : ''}${typeof e.className === 'string' && e.className ? '.' + e.className.trim().split(/\s+/)[0] : ''}`;
          const wide = card.classList.contains('settings-wide');
          for (const el of [card, ...Array.from(card.querySelectorAll<HTMLElement>('*'))]) {
            const cs = getComputedStyle(el);
            if (cs.display === 'none') continue;
            // (c1) nothing scrolls sideways inside the card. Documented
            // exception: the .settings-wide cards (backup list, raw settings
            // table) deliberately scroll their wide table inside itself.
            if (!wide && (cs.overflowX === 'auto' || cs.overflowX === 'scroll') && el.scrollWidth > el.clientWidth + 1) {
              out.push(`${name(el)} scrolls sideways (scrollWidth ${el.scrollWidth} > clientWidth ${el.clientWidth})`);
            }
            // (c2) no box pokes past the card's inline end. Skip boxes that
            // sit inside a deliberately scrolling container (they are clipped
            // by it) and visually hidden helpers.
            const b = el.getBoundingClientRect();
            if (b.width === 0 || b.height === 0) continue;
            let clipped = false;
            for (let p = el.parentElement; p && p !== card; p = p.parentElement) {
              const o = getComputedStyle(p).overflowX;
              if (o === 'auto' || o === 'scroll' || o === 'hidden') { clipped = true; break; }
            }
            if (clipped || wide) continue;
            const past = rtl ? cb.left - b.left : b.right - cb.right;
            if (past > 1) out.push(`${name(el)} extends ${Math.round(past)}px past the card's inline end`);
          }
          return out;
        }, id);
        for (const m of r) fails.push(`${tag}: ${m}`);
      }
      expect(fails, fails.join('\n')).toEqual([]);
      assertClean();
    });
  }
}

test('1024x600 landscape: nav and panel stay side by side', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/settings#settings-printer');
  await expect(page.locator('#settings-printer')).toBeVisible();
  await expect(page.locator('.settings-nav')).toBeVisible();
  await expect(page.locator('#settings-back')).toBeHidden();
  const nav = (await page.locator('#settings-nav').boundingBox())!;
  const panel = (await page.locator('#settings-panel').boundingBox())!;
  expect(nav.x + nav.width).toBeLessThanOrEqual(panel.x + 1);
  assertClean();
});
