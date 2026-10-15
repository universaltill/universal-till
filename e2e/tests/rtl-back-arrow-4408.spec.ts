import { test, expect } from './fixtures';

// ut-docs#4408: a page's text "← Back" link is a bidi-neutral glyph, so on
// an RTL page it kept pointing left (away from "back"). It now sits in
// .back-arrow, mirrored under dir="rtl". Read from the real layout: the
// arrow's computed transform, and the button it backs out through sits on
// the side the arrow points to.

test.afterEach(async ({ page }) => {
  await page.request.post('/api/pos/reset');
});

async function receiptHref(page): Promise<string> {
  await page.request.post('/api/pos/reset');
  await page.request.post('/api/pos/scan', { form: { code: '5000000000012', qty: '1' } });
  const tender = await page.request.post('/api/pos/tender', {
    headers: { 'Content-Type': 'application/json' },
    data: { payments: [{ method: 'cash', amount: 100000 }] },
  });
  expect(tender.ok()).toBeTruthy();
  await page.goto('/journal');
  const href = await page.locator('a[href^="/journal/"]').first().getAttribute('href');
  expect(href).toBeTruthy();
  return href!;
}

test('back arrows point the way the page reads, en and fa (ut-docs#4408)', async ({ page }) => {
  const href = await receiptHref(page);
  // /invoices needs seller details set (else it redirects to /settings);
  // /tills renders the same .back-arrow markup with no setup.
  for (const path of ['/tills', href, '/settings']) {
    for (const lang of ['en', 'fa']) {
      // Settings' Back shows at phone width with a section deep-linked
      // (settings-two-pane-1960.spec.ts).
      const phone = path === '/settings';
      await page.setViewportSize(phone ? { width: 375, height: 740 } : { width: 1280, height: 800 });
      const sep = path.includes('?') ? '&' : '?';
      await page.goto(`${path}${sep}lang=${lang}${phone ? '#settings-display' : ''}`);
      await expect(page.locator('html')).toHaveAttribute('dir', lang === 'fa' ? 'rtl' : 'ltr');
      const arrow = page.locator('.back-arrow:visible').first();
      await expect(arrow, `${path} ${lang}`).toBeVisible();
      const transform = await arrow.evaluate((el) => getComputedStyle(el).transform);
      if (lang === 'fa') {
        const name = `back-arrow-fa-${path === href ? 'journal-detail' : path.slice(1)}`;
        await test.info().attach(name, {
          body: await arrow.locator('xpath=..').screenshot({ path: test.info().outputPath(`${name}.png`) }),
          contentType: 'image/png',
        });
        // scaleX(-1) → matrix(-1, 0, 0, 1, 0, 0): the glyph points right.
        expect(transform, `${path} fa transform`).toMatch(/^matrix\(-1, 0, 0, 1, 0, 0\)$/);
      } else {
        expect(transform, `${path} en transform`).toBe('none');
      }
    }
  }
});
