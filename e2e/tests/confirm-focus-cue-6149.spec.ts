import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#6149: the till confirm dialog (#ut-confirm) moves focus to its
// default button, but a pointer/touch open got no visible cue (UA
// :focus-visible heuristics). Every open path must show a >=2px outline with
// >=3:1 contrast (WCAG 1.4.11) against the dialog surface, light and dark.

const SHOTS = process.env.SHOTS_DIR || '';

async function probe(page: Page) {
  await page.evaluate(() => {
    const host = document.createElement('div');
    host.style.cssText = 'position:fixed;inset-block-start:0;inset-inline-start:0;z-index:520;background:#fff;padding:2px';
    document.body.appendChild(host);
    host.innerHTML = '<button id="p6149" hx-post="/__ut6149" hx-confirm="Delete it?">del</button>';
    (window as any).htmx.process(host);
  });
}

const lum = (rgb: number[]) => {
  const [r, g, b] = rgb.map((v) => { const c = v / 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const contrast = (a: number[], b: number[]) => {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

for (const theme of ['default', 'dark']) {
  for (const how of ['tap', 'key']) {
    test.describe(`${theme} / ${how}-open`, () => {
      test.use({ viewport: { width: 360, height: 640 }, hasTouch: true });

      test('focused confirm button shows a visible, contrasting ring (ut-docs#6149)', async ({ page }, testInfo) => {
        await page.route('**/__ut6149', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: 'x' }));
        await page.goto('/');
        if (theme === 'dark') {
          await page.evaluate(async () => {
            const link = document.getElementById('theme-css') as HTMLLinkElement;
            await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
          });
          const t = await page.evaluate(() => ({
            surface: getComputedStyle(document.documentElement).getPropertyValue('--surface').trim(),
            scheme: getComputedStyle(document.documentElement).colorScheme,
          }));
          expect(t.surface).toBe('#111827');
          expect(t.scheme).toContain('dark');
        }
        await probe(page);
        if (how === 'tap') {
          await page.locator('#p6149').tap();
        } else {
          await page.locator('#p6149').focus();
          await page.keyboard.press('Enter');
        }
        const dlg = page.getByTestId('till-confirm');
        await expect(dlg).toBeVisible();
        const btn = dlg.locator('.btn:focus');
        await expect(btn).toHaveCount(1);

        const info = await page.evaluate(() => {
          const d = document.getElementById('ut-confirm')!;
          const b = d.querySelector('.btn:focus') as HTMLElement;
          const cs = getComputedStyle(b);
          // Effective dialog surface: first non-transparent background up the tree,
          // else the canvas colour (UA Canvas follows color-scheme).
          let bg = '';
          for (let e: Element | null = d; e; e = e.parentElement) {
            const c = getComputedStyle(e).backgroundColor;
            if (c && c !== 'rgba(0, 0, 0, 0)' && c !== 'transparent') { bg = c; break; }
          }
          return { style: cs.outlineStyle, width: parseFloat(cs.outlineWidth), color: cs.outlineColor, bg };
        });
        const parse = (s: string) => (s.match(/[\d.]+/g) || []).slice(0, 3).map(Number);
        expect(info.style, 'outline-style').not.toBe('none');
        expect(info.width, 'outline-width').toBeGreaterThanOrEqual(2);
        const ring = parse(info.color);
        const surface = info.bg ? parse(info.bg) : (theme === 'dark' ? [17, 24, 39] : [255, 255, 255]);
        expect(ring.length).toBe(3);
        expect(contrast(ring, surface), `ring ${info.color} vs ${info.bg || 'canvas'}`).toBeGreaterThanOrEqual(3);

        const png = await dlg.screenshot();
        await testInfo.attach(`confirm-${theme}-${how}.png`, { body: png, contentType: 'image/png' });
        if (SHOTS) require('fs').writeFileSync(`${SHOTS}/confirm-${theme}-${how}.png`, png);
      });
    });
  }
}
