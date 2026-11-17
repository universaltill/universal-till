import { test, expect } from './fixtures';
import { setOskMode, watchConsole } from './helpers';

// ut-docs#5775: osk.js show() centred the focused field in the FULL
// viewport, but the keyboard covers the bottom ~288px, so at 1024x600 the
// field's bottom ~11px ended up under #osk. The field must end up fully
// above the keyboard.

test.afterEach(async ({ page }) => {
  await setOskMode(page, 'auto');
  await page.request.post('/api/pos/reset').catch(() => {});
});

test('focused input sits fully above the keyboard at 1024x600 (ut-docs#5775)', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.setViewportSize({ width: 1024, height: 600 });
  await setOskMode(page, 'on');
  await page.goto('/settings');
  await page.waitForLoadState('networkidle');
  // Reproduction inside a real page: a tall block with an 80px field far
  // enough down that scrollIntoView({block:'center'}) places it at the
  // viewport middle (y=300 -> bottom ~340, ~28px below the keyboard top ~312).
  await page.evaluate(() => {
    const wrap = document.createElement('div');
    wrap.style.cssText = 'height:1400px;position:relative';
    const input = document.createElement('input');
    input.type = 'text';
    input.id = 'repro-5775';
    input.setAttribute('aria-label', 'repro');
    input.style.cssText = 'position:absolute;inset-block-start:700px;inset-inline-start:24px;inline-size:300px;block-size:80px;box-sizing:border-box';
    wrap.appendChild(input);
    // First child: content that loads in later below it can't shift it.
    document.body.prepend(wrap);
  });
  await page.locator('#repro-5775').click();
  await expect(page.locator('#osk.osk-open')).toBeVisible();
  // Let show()'s 60ms deferred scroll land.
  await page.waitForTimeout(400);

  const m = await page.evaluate(() => {
    const el = document.getElementById('repro-5775')!.getBoundingClientRect();
    const osk = document.getElementById('osk')!.getBoundingClientRect();
    return { inputTop: el.top, inputBottom: el.bottom, oskTop: osk.top };
  });
  expect(m.inputTop, 'field must not be scrolled off the top').toBeGreaterThanOrEqual(0);
  expect(m.inputBottom, `field bottom (${m.inputBottom}) must be at or above the keyboard top (${m.oskTop})`).toBeLessThanOrEqual(m.oskTop);
  assertClean();
});
