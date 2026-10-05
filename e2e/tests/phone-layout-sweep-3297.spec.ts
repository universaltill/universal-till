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

// What pokes past either viewport edge, outermost first, skipping
// anything inside a box that scrolls or clips horizontally within the
// viewport (a tab strip, a table's own scroll card) — those are reachable.
async function offenders(page: Page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    const reachable = (el: Element) => {
      for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
        // ut-docs#3653: only a box that SCROLLS makes its overflow
        // reachable. hidden/clip (table.table is overflow:hidden) just cuts
        // the controls off — the #3359 review's accepted finding 4, and
        // exactly how the promotions edit form went missing.
        if (/(auto|scroll)/.test(getComputedStyle(p).overflowX) && p.getBoundingClientRect().right <= vw + 1) return true;
      }
      return false;
    };
    const out: string[] = [];
    document.querySelectorAll('main *, .page *, .content *').forEach((el) => {
      const r = el.getBoundingClientRect();
      // ut-docs#3653: both edges. A flex-end row too wide for its box
      // spills past the inline-START edge (left in LTR), which a right-only
      // check never saw.
      if (!r.width || !r.height || (r.right <= vw + 1 && r.left >= -1) || reachable(el)) return;
      if (el.closest('dialog:not([open]), #osk, [hidden]')) return;
      const s = getComputedStyle(el);
      if (s.visibility === 'hidden' || s.position === 'fixed') return;
      out.push(`${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}.${String(el.className).trim().split(/\s+/)[0]} left=${Math.round(r.left)} right=${Math.round(r.right)}`);
    });
    return { vw, sw: document.documentElement.scrollWidth, out: out.slice(0, 5) };
  });
}

// ut-docs#3359: "we shouldn't scroll to the left and right" — not the page
// (above) and not a box inside it either: a table is a card list at this
// tier, so any element in the page content that scrolls sideways AND has
// something to scroll to fails, unless it (or an ancestor) is marked
// data-hscroll-ok — reserved for the single-row tab strips and chip rows
// that are meant to swipe, each with a comment saying why.
async function sideScrollers(page: Page) {
  return page.evaluate(() => {
    const out: string[] = [];
    document.querySelectorAll('main, main *').forEach((el) => {
      if (!(/(auto|scroll)/.test(getComputedStyle(el).overflowX))) return;
      if (el.scrollWidth <= el.clientWidth + 1 || !el.clientWidth) return;
      if (el.closest('[data-hscroll-ok], dialog:not([open]), [hidden]')) return;
      out.push(`${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}.${String(el.className).trim().split(/\s+/).join('.')} sw=${el.scrollWidth} cw=${el.clientWidth}`);
    });
    // The exemption is for tab strips and chip rows, never a table.
    document.querySelectorAll('[data-hscroll-ok] table, table[data-hscroll-ok]').forEach((t) => out.push(`table inside data-hscroll-ok: ${t.id || t.className}`));
    return out.slice(0, 5);
  });
}

// ut-docs#3653: a list page swept empty proves nothing about its rows.
// /promotions and /kitchen-stations have no demo data, and both carry a
// per-row inline edit form that ran off the phone card's start edge, so
// give each one row to lay out. The handlers answer every outcome with a
// 303 (errors as ?err=…), so check the redirect itself, not the followed
// 200 — a drifted field name must fail here, not quietly sweep an empty list.
test.beforeAll(async ({ request }) => {
  const seeds: [string, Record<string, string>, string][] = [
    ['/api/promotions', { code: 'SWEEP3653', type: 'percent', value_percent: '10', description: 'Phone sweep row', starts_at: '2026-01-01', ends_at: '2026-12-31', customer_id: '' }, '/promotions'],
    ['/api/kitchen-stations', { name: 'Sweep station 3653', destination_type: 'printer', printer_address: '192.0.2.10:9100' }, '/kitchen-stations'],
  ];
  for (const [url, form, back] of seeds) {
    const r = await request.post(url, { form, maxRedirects: 0 });
    expect(r.status(), url).toBe(303);
    expect(r.headers().location, url).toBe(back);
  }
});

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
        expect(await sideScrollers(page), `${r}: a box inside the page scrolls sideways (ut-docs#3359)`).toEqual([]);
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

// ut-docs#3361: the post-sale receipt (web/ui/partials/receipt.html) is an
// htmx swap into #basket, never its own GET route, so it can't join the
// ROUTES sweep above -- this is its guard. The owner reported the
// rightmost action button (Print / New Customer / Refund) clipped off the
// right edge at phone width; reproduce the same tender flow phone-sell-3059
// uses and check every button in the row against the viewport directly,
// rather than trusting the all-routes sweep to somehow reach a view it
// structurally cannot.
for (const w of [360, 440]) {
  test.describe(`phone ${w}px post-sale receipt actions (ut-docs#3361)`, () => {
    test.use({ viewport: { width: w, height: 844 }, hasTouch: true, isMobile: true });
    test(`receipt action row at ${w}px: no button clips past the right edge`, async ({ page }) => {
      await page.request.post('/api/pos/reset');
      await page.goto('/');
      await page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first().click();
      await page.locator('.basket-phonebar').click();
      await page.getByTestId('payment-open').click();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
        page.locator('#payment-overlay').getByTestId('pay-default').click(),
      ]);
      await expect(page.locator('#basket.receipt-view')).toBeVisible();
      // toBeInViewport() only needs partial overlap (its default ratio is
      // 0), so it would pass a button already clipped most of the way off
      // -- this needs the exact edges. Review finding (2026-10-02): the
      // element that actually clips is .pos-container (overflow:hidden in
      // app.css), not the bare viewport width -- comparing against vw left
      // a gap between .pos-container's real edge and the viewport where a
      // button could still visibly clip and this test would miss it.
      // Compare against .pos-container's own rect on both sides, so this
      // also catches a left-edge clip in an RTL locale.
      const info = await page.evaluate(() => {
        const box = document.querySelector('.pos-container')!.getBoundingClientRect();
        const rects = Array.from(document.querySelectorAll('.receipt-wrap .actions .btn'))
          .map((b) => { const r = b.getBoundingClientRect(); return { text: (b.textContent || '').trim(), left: r.left, right: r.right }; });
        return { boxLeft: box.left, boxRight: box.right, rects };
      });
      expect(info.rects.length).toBeGreaterThan(0);
      for (const r of info.rects) {
        expect(r.right, `"${r.text}" right=${r.right} containerRight=${info.boxRight}`).toBeLessThanOrEqual(info.boxRight + 1);
        expect(r.left, `"${r.text}" left=${r.left} containerLeft=${info.boxLeft}`).toBeGreaterThanOrEqual(info.boxLeft - 1);
      }
      await page.request.post('/api/pos/reset');
    });
  });
}
