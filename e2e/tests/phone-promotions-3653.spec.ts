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
      starts_at: '2026-01-01', ends_at: '2026-12-31', customer_id: '', // fixed-date:ok layout seed; /promotions lists by is_active, not dates
    },
    maxRedirects: 0,
  });
  // Every outcome is a 303; an error lands on /promotions?err=….
  expect(r.status()).toBe(303);
  expect(r.headers().location).toBe('/promotions');
});

for (const width of [360, 440]) {
  for (const lang of ['en', 'fa']) {
    test(`/promotions at ${width}px (${lang}): the card's title is the code button, and the dialog fits`, async ({ page }) => {
      await page.setViewportSize({ width, height: 800 });
      await page.goto(`/promotions?lang=${lang}`);
      const card = page.locator('table.t-cards tr.t-cards-row', { hasText: CODE });
      await expect(card).toBeVisible();
      const r = await card.evaluate((tr) => {
        const box = tr.getBoundingClientRect();
        const nameBtn = tr.querySelector('[data-record-edit]')!.getBoundingClientRect();
        const inTitle = !!tr.querySelector('.t-cards-title [data-record-edit]');
        return {
          left: box.left, right: box.right,
          codeBtn: { left: Math.round(nameBtn.left), right: Math.round(nameBtn.right) },
          inTitle,
          // ut-docs#3661: no inline edit form is left in the row.
          inlineControls: tr.querySelectorAll('form, select, input').length,
          sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth,
        };
      });
      expect(r.sw, 'page must not scroll horizontally').toBeLessThanOrEqual(r.cw);
      expect(r.inlineControls, 'no inline edit form in the card').toBe(0);
      expect(r.inTitle, 'the code button is the card title').toBe(true);
      expect(r.codeBtn.left >= r.left - 1 && r.codeBtn.right <= r.right + 1, `code button [${r.codeBtn.left}, ${r.codeBtn.right}] outside card [${r.left}, ${r.right}]`).toBe(true);

      // The More toggle never opens the dialog; the card title (code button) does.
      const toggle = card.locator('.t-cards-toggle');
      await expect(toggle).toHaveCount(1);
      await toggle.click();
      await expect(page.locator('#promotion-dialog')).toBeHidden();

      // The edit controls moved into the record dialog: open it and check
      // the dates are wide enough to show the whole year.
      await card.locator('[data-record-edit]').click();
      await expect(page.locator('#promotion-dialog')).toBeVisible();
      for (const name of ['starts_at', 'ends_at']) {
        const b = (await page.locator(`#promotion-form input[name="${name}"]`).boundingBox())!;
        expect(b.width, `${name} too narrow to show its year`).toBeGreaterThanOrEqual(150);
        expect(b.x).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width).toBeLessThanOrEqual(width + 0.5);
      }
    });
  }
}
