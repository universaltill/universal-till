import { test, expect } from './fixtures';

// ut-docs#3653: a shop reported that on a phone-width till the Promotions
// list "goes out of the display" once a discount exists. The page itself
// never scrolled sideways, so the all-routes sweep (which also only visits
// the EMPTY list) stayed green: each card's inline edit form (type, value,
// description, two dates, Save) was one non-wrapping row ~715px wide inside
// a flex-end actions cell, so it ran off the card's inline-START edge —
// every control but the last date and Save sat off-screen, unreachable.
// Seed a real promotion and check every control against the card's own
// box on both edges, in LTR and RTL.

const CODE = 'PHONE3653';

test.beforeAll(async ({ request }) => {
  const r = await request.post('/api/promotions', {
    form: {
      code: CODE, type: 'amount', value_amount: '2.50',
      description: 'Weekend offer on every hot drink and pastry',
      starts_at: '2026-01-01', ends_at: '2026-12-31', customer_id: '',
    },
    maxRedirects: 0,
  });
  // Every outcome is a 303; an error lands on /promotions?err=….
  expect(r.status()).toBe(303);
  expect(r.headers().location).toBe('/promotions');
});

for (const width of [360, 440]) {
  for (const lang of ['en', 'fa']) {
    test(`/promotions at ${width}px (${lang}): every edit control sits inside its card`, async ({ page }) => {
      await page.setViewportSize({ width, height: 800 });
      await page.goto(`/promotions?lang=${lang}`);
      const card = page.locator('table.t-cards tr.t-cards-row', { hasText: CODE });
      await expect(card).toBeVisible();
      const r = await card.evaluate((tr) => {
        const box = tr.getBoundingClientRect();
        const controls = Array.from(tr.querySelectorAll('form.users-inline :is(select, input:not([type="hidden"]), button)'))
          .filter((el) => getComputedStyle(el).display !== 'none')
          .map((el) => {
            const b = el.getBoundingClientRect();
            return { name: el.getAttribute('name') || (el.textContent || '').trim(), type: el.getAttribute('type'), left: Math.round(b.left), right: Math.round(b.right), w: Math.round(b.width) };
          });
        return { left: box.left, right: box.right, controls, sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth };
      });
      expect(r.sw, 'page must not scroll horizontally').toBeLessThanOrEqual(r.cw);
      // select + value + description + 2 dates + Save + Deactivate.
      expect(r.controls.length, JSON.stringify(r.controls)).toBe(7);
      for (const c of r.controls) {
        expect(c.left >= r.left - 1 && c.right <= r.right + 1, `${c.name} [${c.left}, ${c.right}] outside card [${r.left}, ${r.right}]`).toBe(true);
        // The shared 7.5rem input width cut a date off at "31/12/202"; a
        // manager must read the whole year they are editing.
        if (c.type === 'date') expect(c.w, `${c.name} too narrow to show its year`).toBeGreaterThanOrEqual(150);
      }
    });
  }
}
