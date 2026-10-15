import { test, expect } from './fixtures';

// ut-docs#4279: at a 360px phone viewport /journal/{receipt}'s .page-head
// (title + Refund / Print receipt / ← Journal) never wrapped, so the page
// was ~518px wide, scrolled sideways, and the Journal button sat off-screen.
// The header now wraps (page-head-wrap, as catalog.html, ut-docs#2092).

test.use({ viewport: { width: 360, height: 740 } });

test.afterEach(async ({ page }) => {
  await page.request.post('/api/pos/reset');
});

test('journal detail header fits a 360px phone, en and fa (ut-docs#4279)', async ({ page }) => {
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

  for (const lang of ['en', 'fa']) {
    await page.goto(`${href}?lang=${lang}`);
    await expect(page.locator('html')).toHaveAttribute('lang', lang);
    const btns = page.locator('.page-head .btn');
    expect(await btns.count()).toBe(3);
    await test.info().attach(`journal-detail-${lang}`, {
      body: await page.screenshot({ path: test.info().outputPath(`journal-detail-${lang}.png`) }),
      contentType: 'image/png',
    });
    const sw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(sw, `${lang} scrollWidth`).toBeLessThanOrEqual(360);
    for (let i = 0; i < 3; i++) {
      const box = await btns.nth(i).boundingBox();
      expect(box!.x, `${lang} button ${i} left`).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width, `${lang} button ${i} right`).toBeLessThanOrEqual(360);
    }
  }
});
