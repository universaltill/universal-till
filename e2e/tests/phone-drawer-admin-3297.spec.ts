import { test, expect } from '@playwright/test';
import { ensureOperator, openPhoneDrawer } from './helpers';

// ut-docs#3297: signed in as a manager on an admin page, the phone ☰
// drawer squeezed Users/Promotions/Translations/name/Lock into one row of
// icon-sized boxes with the labels over each other, and the bug-report
// item showed only its icon. Every drawer item is a full-width, labelled
// row that overlaps no other.
test.use({ viewport: { width: 440, height: 956 }, hasTouch: true, isMobile: true });

test('phone drawer: every item is a full-width labelled row, none overlap', async ({ page }) => {
  await ensureOperator(page);
  await page.goto('/users');
  await openPhoneDrawer(page);
  await expect(page.locator('#nav-drawer .session-admin-link').first()).toBeVisible();
  const items = await page.locator('#nav-drawer .nav-toggle').evaluateAll((els) => {
    const drawer = document.getElementById('nav-drawer')!.getBoundingClientRect();
    return els.filter((e) => e.getClientRects().length > 0).map((e) => {
      const r = e.getBoundingClientRect();
      // The visible label: the text actually painted inside the button.
      const label = Array.from(e.querySelectorAll('span')).find((s) => !s.classList.contains('nav-toggle-ico') && s.getBoundingClientRect().width > 8);
      return { text: e.textContent!.trim().replace(/\s+/g, ' '), top: r.top, bottom: r.bottom, width: r.width, drawer: drawer.width, labelW: label ? label.getBoundingClientRect().width : 0 };
    });
  });
  expect(items.length).toBeGreaterThanOrEqual(8);
  for (const it of items) {
    expect(it.width, `${it.text} is a full-width row`).toBeGreaterThan(it.drawer * 0.8);
    expect(it.labelW, `${it.text} shows its label`).toBeGreaterThan(8);
  }
  const sorted = [...items].sort((a, b) => a.top - b.top);
  for (let i = 1; i < sorted.length; i++) {
    expect(sorted[i].top, `${sorted[i].text} overlaps ${sorted[i - 1].text}`).toBeGreaterThanOrEqual(sorted[i - 1].bottom - 1);
  }
});
