import { test, expect } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3523 (ADR-0116 D5/D6): Settings → Till registration → "Pair with
// a shop". The code field + Pair button must sit inside the card without
// horizontal overflow at the 1024x600 kiosk floor and at phone width, in LTR
// and RTL; an empty code must never reach the server (`required`); a code
// the till cannot pair with must land a visible error in #pair-msg. The
// default e2e till has no marketplace endpoint, so the server answers the
// generic "Pairing failed" message without calling any cloud — the
// store-mismatch branch is backend-only (internal/enroll/pair_test.go).

for (const vp of [
  { width: 1024, height: 600 },
  { width: 360, height: 740 },
]) {
  for (const lang of ['en', 'fa']) {
    test(`pair control fits the registration card at ${vp.width}x${vp.height} (${lang})`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize(vp);
      await page.goto(`/settings?lang=${lang}#registration`);
      const input = page.locator('#registration #pair-code');
      const button = page.locator('#registration form[hx-post="/api/enrol/pair"] button[type="submit"]');
      await expect(input).toBeVisible();
      await expect(button).toBeVisible();

      const g = await page.evaluate(() => {
        const box = (e: Element | null) => e!.getBoundingClientRect();
        const card = box(document.querySelector('#registration'));
        const inp = box(document.querySelector('#pair-code'));
        const btnEl = document.querySelector('#pair-code ~ button') as HTMLElement;
        const btn = box(btnEl);
        return {
          overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          inside: [inp, btn].every((r) => r.left >= card.left - 1 && r.right <= card.right + 1),
          overlap: !(btn.left >= inp.right - 1 || btn.right <= inp.left + 1 || btn.top >= inp.bottom - 1),
          btnClipped: btnEl.scrollWidth > btnEl.clientWidth + 1,
          btnHeight: btn.height,
          dir: document.documentElement.dir,
        };
      });
      expect(g.overflow, 'horizontal page overflow').toBeLessThanOrEqual(0);
      expect(g.inside, 'input and button stay inside #registration').toBe(true);
      expect(g.overlap, 'input and button overlap').toBe(false);
      expect(g.btnClipped, 'Pair button label clipped').toBe(false);
      expect(g.btnHeight, 'touch-sized Pair button').toBeGreaterThanOrEqual(44);
      expect(g.dir).toBe(lang === 'fa' ? 'rtl' : 'ltr');
      assertClean();
    });
  }
}

test('empty code is blocked client-side; a code the till cannot pair with shows an error', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  const posts: string[] = [];
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().includes('/api/enrol/pair')) posts.push(r.postData() ?? '');
  });
  await page.goto('/settings#registration');
  const input = page.locator('#pair-code');
  const button = page.locator('#pair-code ~ button');

  await button.click();
  await page.waitForTimeout(300);
  expect(posts, 'an empty code must not be posted').toHaveLength(0);
  expect(await input.evaluate((e: HTMLInputElement) => e.validity.valueMissing)).toBe(true);

  await input.fill('test-3523-garbage');
  await button.click();
  const msg = page.locator('#pair-msg .error');
  await expect(msg).toBeVisible();
  await expect(msg).toContainText('Pairing failed');
  expect(posts).toHaveLength(1);
  assertClean();
});
