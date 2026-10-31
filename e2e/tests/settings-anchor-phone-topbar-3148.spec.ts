import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3148: at phone width (<=480px) the top bar is a sticky row
// (--phone-top-h). Following a /settings#<section> link scrolled the card's
// top edge to y=0, under that bar, so the section's heading was hidden and
// the page opened mid-card. Above 480px the nav is a side rail and nothing
// sits over the panel, so the landing there must not move.

// Cards far enough down the page that the browser has to scroll to them.
const DEEP_SECTIONS = ['settings-about', 'settings-backup'];

async function headingBelowBar(page: Page, id: string) {
  return page.evaluate((cardId) => {
    const bar = document.querySelector('.nav')!.getBoundingClientRect();
    const h = document.getElementById(cardId)!.querySelector('h2, h3')!.getBoundingClientRect();
    return { barBottom: bar.bottom, headTop: h.top, scrollY: window.scrollY };
  }, id);
}

test.describe('settings section anchors clear the phone top bar (ut-docs#3148)', () => {
  for (const id of DEEP_SECTIONS) {
    test(`360px: /settings#${id} shows the heading below the sticky bar`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: 360, height: 740 });
      await page.goto(`/settings#${id}`);
      await expect(page.locator(`#${id}`)).toBeVisible();
      await expect.poll(async () => (await headingBelowBar(page, id)).scrollY,
        { message: 'the deep link scrolled the page' }).toBeGreaterThan(0);
      const r = await headingBelowBar(page, id);
      expect(r.headTop, `heading top ${r.headTop} vs bar bottom ${r.barBottom}`).toBeGreaterThanOrEqual(r.barBottom);
      assertClean();
    });
  }

  test('360px: a same-page hash change also lands below the bar', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto('/settings#settings-backup');
    await expect(page.locator('#settings-backup')).toBeVisible();
    await page.evaluate(() => { location.hash = '#settings-about'; });
    await expect(page.locator('#settings-about')).toBeVisible();
    await expect.poll(async () => (await headingBelowBar(page, 'settings-about')).scrollY).toBeGreaterThan(0);
    const r = await headingBelowBar(page, 'settings-about');
    expect(r.headTop).toBeGreaterThanOrEqual(r.barBottom);
  });

  test('1024x600: no offset added where the nav is a side rail', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/settings#settings-about');
    await expect(page.locator('#settings-about')).toBeVisible();
    const pad = await page.evaluate(() => getComputedStyle(document.documentElement).scrollPaddingBlockStart);
    expect(pad, 'the phone-tier scroll padding must not apply to the rail layout').toBe('auto');
    const r = await headingBelowBar(page, 'settings-about');
    expect(r.headTop, 'the heading is in view').toBeGreaterThanOrEqual(0);
    expect(r.headTop, 'the card still lands at the top, as before').toBeLessThan(40);
  });
});
