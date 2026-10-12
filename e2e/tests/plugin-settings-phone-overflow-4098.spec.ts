import { test, expect } from './fixtures';

// ut-docs#4098: at 360px a plugin's ⚙ Settings page scrolled sideways by
// ~27px whenever the typed takeaway-overrides editor was on it — the
// <fieldset> around the stacked overrides cards spilled past its .card.
// The all-routes phone sweep (phone-layout-sweep-3297) never visits a
// plugin's settings page, so this pins it: e2e/seed_faq stores one
// takeaway_rate_overrides row (a live code + a deleted code's leftover) on
// the seeded FAQ plugin, enough for the editor to render both row shapes.

const URL = '/plugins/com.universaltill.ut-faq/settings';

for (const width of [320, 360]) {
  for (const lang of ['en', 'fa', 'tr']) {
    test(`plugin settings at ${width}px (${lang}): no sideways scroll, overrides stay inside the card`, async ({ page }) => {
      await page.setViewportSize({ width, height: 800 });
      await page.goto(`${URL}?lang=${lang}`);
      const fieldset = page.locator('form fieldset', { has: page.locator('input[name="setting_takeaway_typed"]') });
      await expect(fieldset).toBeVisible();
      // Every till's tax codes plus the seeded orphan's row.
      await expect(fieldset.locator('input[name="takeaway_pct_tax-retired-reduced-rate"]')).toBeVisible();

      const r = await fieldset.evaluate((fs) => {
        const card = fs.closest('.card')!.getBoundingClientRect();
        const box = fs.getBoundingClientRect();
        const inputs = Array.from(fs.querySelectorAll('input[name^="takeaway_pct_"]')).map((el) => {
          const b = el.getBoundingClientRect();
          return { name: el.getAttribute('name'), left: Math.round(b.left), right: Math.round(b.right) };
        });
        return {
          sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth,
          card: { left: card.left, right: card.right }, fs: { left: box.left, right: box.right }, inputs,
        };
      });
      expect(r.sw, `page is ${r.sw - r.cw}px wider than the viewport`).toBeLessThanOrEqual(r.cw);
      expect(r.fs.left >= r.card.left - 1 && r.fs.right <= r.card.right + 1,
        `fieldset [${r.fs.left}, ${r.fs.right}] outside card [${r.card.left}, ${r.card.right}]`).toBe(true);
      for (const i of r.inputs) {
        expect(i.left >= r.card.left - 1 && i.right <= r.card.right + 1,
          `${i.name} [${i.left}, ${i.right}] outside card [${r.card.left}, ${r.card.right}]`).toBe(true);
      }
    });
  }
}
