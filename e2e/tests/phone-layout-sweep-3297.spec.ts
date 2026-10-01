import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { openPhoneSheet } from './helpers';

// ut-docs#3297: the product owner walked every page of the iPhone app
// (440 CSS px, TestFlight v0.30.11) and found pages that scroll sideways,
// the /items section list pinned over the catalog while it scrolls, the
// basket sheet's quantity controls stacked into a tower with an unlabelled
// "0" box, and "Takeaway" broken mid-word. This spec is the phone guard for
// every GET page: a new page that scrolls sideways at 360 or 440 fails here.

const ROUTES = ['/', '/admin', '/audit', '/backoffice', '/bluetooth-devices', '/catalog', '/catalog/option-sets',
  '/catalog/tax-codes', '/categories', '/country-settings', '/designer', '/fiscal-device', '/fiscal-register',
  '/help', '/import', '/inventory', '/items', '/journal', '/kitchen-stations', '/locations', '/menu',
  '/modifiers', '/my-reports', '/open-orders', '/orders', '/plugins', '/plugins/store', '/promotions',
  '/receipt-designer', '/registers', '/report-issue', '/reports', '/settings', '/settings/menu', '/shifts',
  '/tables', '/tills', '/translations', '/users', '/users/permissions'];

// What pokes past the viewport's end edge, outermost first, skipping
// anything inside a box that scrolls or clips horizontally within the
// viewport (a tab strip, a table's own scroll card) — those are reachable.
async function offenders(page: Page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    const reachable = (el: Element) => {
      for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
        if (/(auto|scroll|hidden|clip)/.test(getComputedStyle(p).overflowX) && p.getBoundingClientRect().right <= vw + 1) return true;
      }
      return false;
    };
    const out: string[] = [];
    document.querySelectorAll('main *, .page *, .content *').forEach((el) => {
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height || r.right <= vw + 1 || reachable(el)) return;
      if (el.closest('dialog:not([open]), #osk, [hidden]')) return;
      const s = getComputedStyle(el);
      if (s.visibility === 'hidden' || s.position === 'fixed') return;
      out.push(`${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}.${String(el.className).trim().split(/\s+/)[0]} right=${Math.round(r.right)}`);
    });
    return { vw, sw: document.documentElement.scrollWidth, out: out.slice(0, 5) };
  });
}

for (const w of [360, 440]) {
  test.describe(`phone ${w}px (ut-docs#3297)`, () => {
    test.use({ viewport: { width: w, height: 800 }, hasTouch: true, isMobile: true });
    for (const r of ROUTES) {
      test(`${r} does not scroll sideways`, async ({ page }) => {
        await page.goto(r);
        // Not 'networkidle': /orders and the status bar poll forever.
        await page.waitForLoadState('load');
        await page.waitForTimeout(500);
        const o = await offenders(page);
        expect(o.out, `${r}: ${JSON.stringify(o)}`).toEqual([]);
        expect(o.sw, `${r}: page wider than the screen`).toBeLessThanOrEqual(o.vw + 1);
      });
    }
  });
}

test.describe('phone 440px details (ut-docs#3297)', () => {
  test.use({ viewport: { width: 440, height: 956 }, hasTouch: true, isMobile: true });

  test('/items: the section list scrolls away with the page, never over the catalog', async ({ page }) => {
    await page.goto('/items');
    const rail = page.locator('.items-rail-wrap');
    await expect(rail).toBeVisible();
    const before = await rail.evaluate((el) => el.getBoundingClientRect().top);
    const scrolled = await page.evaluate(() => { window.scrollTo(0, 400); return window.scrollY; });
    expect(scrolled).toBeGreaterThan(100);
    // Pinned (the bug), its top stays put; in flow, it moves up by the scroll.
    await expect.poll(() => rail.evaluate((el) => el.getBoundingClientRect().top)).toBeLessThan(before - scrolled + 2);
  });

  test('Dine in / Takeaway: each label stays one unbroken word', async ({ page }) => {
    await page.goto('/');
    const lines = await page.locator('.basket .order-type-option').evaluateAll((opts) => opts.map((o) => {
      const walker = document.createTreeWalker(o, NodeFilter.SHOW_TEXT);
      let n: Node | null; let most = 0;
      while ((n = walker.nextNode())) {
        if (!n.textContent?.trim()) continue;
        const range = document.createRange(); range.selectNodeContents(n);
        const tops = new Set(Array.from(range.getClientRects()).map((r) => Math.round(r.top)));
        most = Math.max(most, tops.size);
      }
      return most;
    }));
    expect(lines.length).toBeGreaterThan(0);
    for (const l of lines) expect(l).toBe(1);
  });
});

// The review's arithmetic: at 360px the fixed price/total tracks left the qty
// area too narrow for − qty +, so run the sheet check at both widths.
for (const w of [360, 440]) {
  test.describe(`phone ${w}px basket sheet (ut-docs#3297)`, () => {
    test.use({ viewport: { width: w, height: 800 }, hasTouch: true, isMobile: true });
    test(`basket sheet at ${w}px: − qty + sit on one row, and a zero discount shows its placeholder, not "0"`, async ({ page }) => {
      await page.request.post('/api/pos/reset');
      await page.goto('/');
      await page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first().click();
      await expect(page.locator('.basket-phonebar')).not.toHaveClass(/is-empty/);
      await openPhoneSheet(page);
      const row = page.locator('.basket tbody tr').first();
      const tops = await row.evaluate((tr) => ['.qty-step-btn:first-child', '.qty-input', '.qty-step-btn:last-child']
        .map((s) => { const r = tr.querySelector(s)!.getBoundingClientRect(); return r.top + r.height / 2; }));
      expect(Math.max(...tops) - Math.min(...tops), `centres ${tops}`).toBeLessThan(4);
      await expect(row.locator('.disc-input')).toHaveValue('');
      await expect(row.locator('.disc-input')).toHaveAttribute('placeholder', /.+/);
      await page.request.post('/api/pos/reset');
    });
  });
}
