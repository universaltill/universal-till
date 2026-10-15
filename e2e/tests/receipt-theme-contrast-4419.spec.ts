// ut-docs#4419: the on-screen receipt after a tender was a fixed white card
// whose text partly came from the theme, so in the dark theme the Payments
// heading, the payment method and the voucher block were light grey on
// white. The card now takes the theme's own --surface/--text/--muted, so
// every piece of receipt text must read against whatever sits behind it --
// at the two till sizes, LTR and RTL, light and dark.
import { test, expect, type Page } from './fixtures';
import { openPhoneSheet } from './helpers';

const SHOTS = process.env.UT_RECEIPT_SHOTS || '';

async function useDarkTheme(page: Page) {
  await page.evaluate(async () => {
    const link = document.getElementById('theme-css') as HTMLLinkElement;
    await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
  });
}

// Every visible element in the receipt card that owns text, with its WCAG
// contrast ratio against the first opaque background behind it. Buttons
// and the printer warning paint their own backgrounds and are app.css's
// concern, not the receipt's.
async function receiptContrast(page: Page) {
  return page.evaluate(() => {
    const parse = (c: string) => {
      const m = c.match(/rgba?\(([^)]+)\)/);
      if (!m) return null;
      const [r, g, b, a = '1'] = m[1].split(/[,\s/]+/).filter(Boolean);
      return { r: +r, g: +g, b: +b, a: +a };
    };
    const lum = ({ r, g, b }: { r: number; g: number; b: number }) => {
      const ch = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
      return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b);
    };
    const bgOf = (el: Element | null) => {
      for (; el; el = el.parentElement) {
        const c = parse(getComputedStyle(el).backgroundColor);
        if (c && c.a >= 0.99) return c;
      }
      return { r: 255, g: 255, b: 255, a: 1 };
    };
    const card = document.querySelector('#basket .receipt-card')!;
    const out: { text: string; ratio: number; color: string }[] = [];
    for (const el of [card, ...Array.from(card.querySelectorAll('*'))]) {
      if (el.closest('.btn, button, .receipt-printer-warning, svg')) continue;
      const own = Array.from(el.childNodes).filter((n) => n.nodeType === Node.TEXT_NODE).map((n) => n.textContent!.trim()).join(' ').trim();
      if (!own) continue;
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height) continue;
      const fg = parse(getComputedStyle(el).color)!;
      const l1 = lum(fg), l2 = lum(bgOf(el));
      const ratio = (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
      out.push({ text: own.slice(0, 30), ratio: Math.round(ratio * 100) / 100, color: getComputedStyle(el).color });
    }
    return out;
  });
}

test.describe('on-screen receipt reads in every theme (ut-docs#4419)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of [{ w: 1024, h: 600 }, { w: 360, h: 800 }]) {
    for (const lang of ['en', 'fa']) {
      // dark-plugin: a theme plugin with a dark --surface that, unlike
      // dark.css, leaves app.css's `h2 { color: var(--brand) }` alone.
      for (const theme of ['light', 'dark', 'dark-plugin']) {
        test(`receipt text contrast ${vp.w}x${vp.h} ${lang} ${theme}`, async ({ page }) => {
          await page.request.post('/api/pos/reset');
          await page.setViewportSize({ width: vp.w, height: vp.h });
          await page.goto(`/?lang=${lang}`);
          await page.waitForSelector('.pos-container');
          if (theme !== 'light') await useDarkTheme(page);
          if (theme === 'dark-plugin') {
            await page.addStyleTag({ content: 'h2 { color: var(--brand); }' });
          }
          const tile = page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first();
          await Promise.all([page.waitForResponse((r) => r.url().includes('/api/pos/scan')), tile.click()]);
          await openPhoneSheet(page);
          await page.getByTestId('payment-open').click();
          await Promise.all([
            page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
            page.locator('#payment-overlay').getByTestId('pay-default').click(),
          ]);
          const receipt = page.locator('#basket.receipt-view .receipt-card');
          await expect(receipt).toBeVisible();
          await expect(receipt.locator('.receipt-payments h3'), 'the Payments block is on the receipt').toBeVisible();
          expect(await page.evaluate(() => document.documentElement.dir)).toBe(lang === 'fa' ? 'rtl' : 'ltr');

          const texts = await receiptContrast(page);
          expect(texts.length, 'the receipt has text to measure').toBeGreaterThan(5);
          const poor = texts.filter((t) => t.ratio < 4.5);
          expect(poor, 'receipt text under WCAG 1.4.3 4.5:1').toEqual([]);
          if (SHOTS) await page.screenshot({ path: `${SHOTS}/receipt-${vp.w}x${vp.h}-${lang}-${theme}.png` });

          // The browser-printed receipt is paper: white with dark text, whatever the theme.
          await page.emulateMedia({ media: 'print' });
          const card = await receipt.evaluate((el) => getComputedStyle(el).backgroundColor);
          expect(card).toBe('rgb(255, 255, 255)');
          const printPoor = (await receiptContrast(page)).filter((t) => t.ratio < 4.5);
          expect(printPoor, 'printed receipt text under 4.5:1').toEqual([]);
          await page.emulateMedia({ media: 'screen' });
        });
      }
    }
  }
});
