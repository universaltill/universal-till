// ut-docs#6184: the receipt's "Printer unavailable" block hard-coded a light
// orange (#fff7ed / #fdba74 / #9a3412), a bright slab on the dark theme. It
// now takes the theme's --warning-tint / --warning-tint-border / --warning-text,
// so its text must read (WCAG 1.4.3, 4.5:1) on the tint composited over the
// receipt card, in a light and the dark theme. The e2e till has no printer
// configured, so every tender's receipt carries the warning.
import { test, expect, type Page } from './fixtures';
import { openPhoneSheet } from './helpers';

async function useDarkTheme(page: Page) {
  await page.evaluate(async () => {
    const link = document.getElementById('theme-css') as HTMLLinkElement;
    await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
  });
}

async function openTill(page: Page) {
  await page.request.post('/api/pos/reset');
  await page.setViewportSize({ width: 1024, height: 600 });
  await page.goto('/?lang=en');
  await page.waitForSelector('.pos-container');
}

test.describe('receipt printer-unavailable warning follows the theme (ut-docs#6184)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const theme of ['light', 'dark']) {
    test(`warning contrast and tokens ${theme}`, async ({ page }) => {
      await openTill(page);
      if (theme === 'dark') await useDarkTheme(page);
      const tile = page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first();
      await Promise.all([page.waitForResponse((r) => r.url().includes('/api/pos/scan')), tile.click()]);
      await openPhoneSheet(page);
      await page.getByTestId('payment-open').click();
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
        page.locator('#payment-overlay').getByTestId('pay-default').click(),
      ]);
      const warn = page.locator('#basket.receipt-view .receipt-printer-warning');
      await expect(warn).toBeVisible();

      const m = await warn.evaluate((el) => {
        const parse = (c: string) => {
          const x = c.match(/rgba?\(([^)]+)\)/)!;
          const [r, g, b, a = '1'] = x[1].split(/[,\s/]+/).filter(Boolean);
          return { r: +r, g: +g, b: +b, a: +a };
        };
        const lum = ({ r, g, b }: { r: number; g: number; b: number }) => {
          const ch = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
          return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b);
        };
        // Effective card background: first opaque ancestor behind the tint.
        let under = { r: 255, g: 255, b: 255, a: 1 };
        for (let p = el.parentElement; p; p = p.parentElement) {
          const c = parse(getComputedStyle(p).backgroundColor);
          if (c.a >= 0.99) { under = c; break; }
        }
        const cs = getComputedStyle(el);
        const tint = parse(cs.backgroundColor);
        const bg = {
          r: tint.r * tint.a + under.r * (1 - tint.a),
          g: tint.g * tint.a + under.g * (1 - tint.a),
          b: tint.b * tint.a + under.b * (1 - tint.a),
        };
        const l1 = lum(parse(cs.color)), l2 = lum(bg);
        return {
          ratio: (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05),
          background: cs.backgroundColor, borderColor: cs.borderTopColor, color: cs.color,
          probe: ['--warning-tint', '--warning-tint-border', '--warning-text'].map((v) => {
            const s = document.createElement('span');
            s.style.cssText = v === '--warning-text' ? `color: var(${v})` : `background: var(${v})`;
            document.body.appendChild(s);
            const out = v === '--warning-text' ? getComputedStyle(s).color : getComputedStyle(s).backgroundColor;
            s.remove();
            return out;
          }),
        };
      });
      expect(m.ratio, `warning text vs tint over card (${theme})`).toBeGreaterThanOrEqual(4.5);
      // Not the old literals, and exactly the theme's tokens.
      expect(m.background).not.toBe('rgb(255, 247, 237)');
      expect(m.color).not.toBe('rgb(154, 52, 18)');
      expect(m.background).toBe(m.probe[0]);
      expect(m.borderColor).toBe(m.probe[1]);
      expect(m.color).toBe(m.probe[2]);
      // Review #6184: prove the dark theme really loaded (the probes resolve under
      // whatever theme is active, so they alone can't tell light from dark).
      if (theme === 'dark') expect(m.color).toBe('rgb(217, 119, 6)');

      // Paper keeps hiding it.
      await page.emulateMedia({ media: 'print' });
      await expect(warn).toBeHidden();
      await page.emulateMedia({ media: 'screen' });
    });
  }
});
