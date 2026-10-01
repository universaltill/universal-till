import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { openPhoneSheet } from './helpers';

// ut-docs#3350: on the iPhone app, tapping a text field (Change PIN, sale
// search, item fields) zoomed the page in. iOS WebKit zooms on focus of any
// field whose computed font-size is under 16px. The app shell now locks the
// zoom (ios/UniversalTill/TillWebView.swift, pinned by
// scripts/ci/ios-webview-zoom_test.sh); this spec keeps every field at 16px+
// at the phone tier too, so Safari and the home-screen web app don't zoom
// in portrait either — without taking pinch-zoom away from browser users.

// Pages that render a field at 390px; the sale screen's fields are the
// basket line's, covered by its own case below.
const ROUTES = ['/admin', '/catalog', '/catalog/option-sets', '/catalog/tax-codes', '/categories',
  '/country-settings', '/help', '/inventory', '/items', '/journal', '/kitchen-stations', '/locations',
  '/modifiers', '/promotions', '/receipt-designer', '/registers', '/report-issue', '/reports', '/settings',
  '/shifts', '/tables', '/tills', '/translations', '/users'];

// Every rendered field iOS would zoom into, with its computed size.
async function smallFields(page: Page) {
  return page.evaluate(() => {
    const skip = new Set(['checkbox', 'radio', 'range', 'color', 'hidden', 'button', 'submit', 'reset', 'image', 'file']);
    const out: string[] = [];
    let seen = 0;
    document.querySelectorAll('input, select, textarea').forEach((el) => {
      if (el instanceof HTMLInputElement && skip.has(el.type)) return;
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height) return;
      seen++;
      const fs = parseFloat(getComputedStyle(el).fontSize);
      if (fs < 16) {
        const name = el.getAttribute('name') || el.id || (el as HTMLElement).className || '';
        out.push(`${el.tagName.toLowerCase()}[${name}] ${fs.toFixed(1)}px`);
      }
    });
    return { seen, out };
  });
}

test.describe('phone 390px: no field is small enough for iOS to zoom into (ut-docs#3350)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  for (const r of ROUTES) {
    test(`${r}: every field is at least 16px`, async ({ page }) => {
      const resp = await page.goto(r);
      expect(resp?.ok(), `${r} answered ${resp?.status()}`).toBe(true);
      await page.waitForLoadState('load');
      await page.waitForTimeout(300);
      const f = await smallFields(page);
      // A route that stops rendering fields would pass vacuously.
      expect(f.seen, `${r}: no field rendered`).toBeGreaterThan(0);
      expect(f.out, r).toEqual([]);
    });
  }

  test('basket line: the quantity and discount fields are at least 16px', async ({ page }) => {
    await page.request.post('/api/pos/reset');
    await page.goto('/');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first().click(),
    ]);
    await openPhoneSheet(page);
    await expect(page.locator('.basket .qty-input').first()).toBeVisible();
    const f = await smallFields(page);
    expect(f.out).toEqual([]);
    await page.request.post('/api/pos/reset');
  });
});
