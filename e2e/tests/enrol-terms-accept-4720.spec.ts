import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#4720: Settings → Till registration → the cloud-terms accept step
// above Register now. The tick box, its label and the "Read the terms" link
// must sit inside the card without horizontal overflow at the 1024x600 kiosk
// floor and at phone width, in LTR and RTL; the box starts unticked; the link
// points at the locale's region-tagged terms page; and Register now without
// the tick answers with the translated "accept the terms" message instead of
// registering (the default e2e till has no marketplace endpoint, so a ticked
// press would only show the generic failure — the ticked path is covered by
// internal/pages/enrol_terms_test.go against a fake cloud).

for (const vp of [
  { width: 1024, height: 600 },
  { width: 360, height: 740 },
]) {
  for (const lang of ['en', 'fa']) {
    test(`terms accept step fits the registration card at ${vp.width}x${vp.height} (${lang})`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await page.goto(`/settings?lang=${lang}#registration`);
      const box = page.locator('#registration #enrol-accept-terms');
      const link = page.locator('#registration .terms-accept a').first();
      await expect(box).toBeVisible();
      await expect(link).toBeVisible();
      expect(await box.isChecked(), 'accept box must start unticked').toBe(false);
      await expect(link).toHaveAttribute('href', 'https://universaltill.com/en-gb/legal/cloud-terms/');
      await expect(link).toHaveAttribute('target', '_blank');

      const g = await page.evaluate(() => {
        const r = (e: Element | null) => e!.getBoundingClientRect();
        const card = r(document.querySelector('#registration'));
        const step = r(document.querySelector('#registration .terms-accept'));
        return {
          overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          inside: step.left >= card.left - 1 && step.right <= card.right + 1,
          dir: document.documentElement.dir,
        };
      });
      expect(g.overflow, 'horizontal page overflow').toBeLessThanOrEqual(0);
      expect(g.inside, 'accept step stays inside #registration').toBe(true);
      expect(g.dir).toBe(lang === 'fa' ? 'rtl' : 'ltr');
      if (process.env.UT_SHOT_DIR) {
        await page.locator('#registration').screenshot({ path: `${process.env.UT_SHOT_DIR}/terms-${lang}-${vp.width}.png` });
      }
      assertClean();
    });
  }
}

test('Register now without the tick asks to accept the terms and does not register', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  const posts: string[] = [];
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().includes('/api/enrol/now')) posts.push(r.postData() ?? '');
  });
  await page.goto('/settings#registration');
  await page.locator('#registration button[hx-post="/api/enrol/now"]').click();
  const msg = page.locator('#enrol-msg .error');
  await expect(msg).toBeVisible();
  await expect(msg).toContainText('Accept the Universal Till cloud service terms');
  expect(posts).toHaveLength(1);
  expect(posts[0]).not.toContain('accept_terms');
  if (process.env.UT_SHOT_DIR) {
    await page.locator('#registration').screenshot({ path: `${process.env.UT_SHOT_DIR}/terms-refused-en.png` });
  }
  assertClean();
});
