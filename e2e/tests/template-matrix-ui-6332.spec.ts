import { test, expect, type Page, type APIRequestContext } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#6351 / ut-docs#6332 / ADR-0190: the shop-type surfaces (wizard
// tiles, change preview, undo banner) in the two stress locales and on a
// 360px phone: no sideways scroll, every row a real tap target, a sane
// keyboard order. Runs in the `templates` project (run-till-templates.sh);
// named to sort after template-matrix-6332 (which needs the legacy start).
//
// EMULATED: the till has no German pack (core ships en/ar/fa/tr; de lives in
// ut-translations), so `?lang=de` renders the English fallback here: that still
// proves the layout, not the German wording (the locale-render audit does
// that). fa is real and RTL. 360x740 is a Chromium viewport, not a phone.

const PHONE = { width: 360, height: 740 };
const MIN = 44; // px, the touch-target floor
const LANGS = ['de', 'fa'] as const;

async function setType(request: APIRequestContext, to: string) {
  const res = await request.post('/api/settings/shop-type', { form: { to } });
  expect(res.ok(), `set shop type ${to}: ${res.status()}`).toBe(true);
}

const overflow = (page: Page) =>
  page.evaluate(() => document.scrollingElement!.scrollWidth - document.scrollingElement!.clientWidth);

// Every box fully inside the viewport width (RTL and LTR alike).
async function expectInsideViewport(page: Page, selector: string, what: string) {
  const boxes = await page.locator(selector).evaluateAll((els) =>
    els.map((e) => { const r = e.getBoundingClientRect(); return { l: r.left, r: r.right, w: r.width, h: r.height }; }));
  expect(boxes.length, `${what}: found`).toBeGreaterThan(0);
  for (const b of boxes) {
    expect(b.l, `${what} starts inside the viewport`).toBeGreaterThanOrEqual(-1);
    expect(b.r, `${what} ends inside the viewport`).toBeLessThanOrEqual(PHONE.width + 1);
  }
}

async function expectTall(page: Page, selector: string, what: string, min = MIN) {
  const hs = await page.locator(selector).evaluateAll((els) => els.map((e) => e.getBoundingClientRect().height));
  expect(hs.length, `${what}: found`).toBeGreaterThan(0);
  for (const h of hs) expect(h, `${what} is a real tap target`).toBeGreaterThanOrEqual(min);
}

test.afterAll(async ({ browser }, info) => {
  const ctx = await browser.newContext({ baseURL: info.project.use.baseURL });
  await (await ctx.newPage()).goto('/menu?lang=en'); // reset the locale cookie
  await ctx.close();
});

for (const lang of LANGS) {
  test.describe(`${lang} at 360px`, () => {
    test.beforeEach(async ({ page }) => { await page.setViewportSize(PHONE); });

    test('setup wizard shop-type tiles: no sideways scroll, whole tile tappable, keyboard moves the pick', async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.goto(`/setup?lang=${lang}`);
      if (lang === 'fa') await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
      await page.locator('[data-step="1"] [data-next]').click();
      const country = page.locator('[data-step="2"]');
      const gb = country.locator('button.picker-tile[value="GB"]');
      if (!(await gb.isVisible())) await country.locator('button[\\@click*="showAllCountries = true"]').click();
      await gb.click();
      await country.locator('[data-next]').click();
      await page.locator('input[name=store_name]').fill('Template Test Shop');
      await page.locator('input[name=till_name]').fill('Front till');
      await page.locator('[data-step="4"] [data-next]').click();

      const step = page.locator('[data-step="5"]');
      await expect(step.locator('button.shop-type-tile')).toHaveCount(6);
      expect(await overflow(page), `wizard step 5 in ${lang}`).toBeLessThanOrEqual(1);
      await expectInsideViewport(page, '[data-step="5"] button.shop-type-tile', 'wizard tile');
      await expectTall(page, '[data-step="5"] button.shop-type-tile', 'wizard tile', 72);
      await expectTall(page, '[data-step="5"] .shop-type-bar .btn', 'wizard action button');
      await expectInsideViewport(page, '[data-step="5"] .shop-type-bar .btn', 'wizard action button');

      // Whole tile is the target: a tap near the tile's far edge, off the label text, picks it.
      const tile = step.locator('button[data-value="service"]');
      // 360x740: the pinned action bar (3 stacked buttons, ~179px) covers the lower tiles at the
      // default scroll position, so centre the tile before tapping its far edge.
      await tile.evaluate((el) => el.scrollIntoView({ block: 'center' }));
      const box = (await tile.boundingBox())!;
      await tile.click({ position: { x: box.width - 5, y: box.height / 2 } });
      await expect(tile).toHaveAttribute('aria-checked', 'true');
      await expect(step.locator('.setup-nav [data-next]')).toBeEnabled();

      // Keyboard: one tab stop in the group, arrows move the pick, then Tab leaves to the action bar.
      await tile.focus();
      await page.keyboard.press(lang === 'fa' ? 'ArrowLeft' : 'ArrowRight');
      await expect(step.locator('button.shop-type-tile[aria-checked="true"]')).toHaveCount(1);
      await expect(step.locator('button.shop-type-tile[aria-checked="true"]')).not.toHaveAttribute('data-value', 'service');
      await page.keyboard.press('Tab');
      await expect(step.locator('.shop-type-bar .btn').first()).toBeFocused(); // Back
      await page.screenshot({ path: test.info().outputPath(`wizard-step5-${lang}-360.png`), fullPage: true });
      assertClean();
    });

    test('change preview: rows >= 44px and tappable edge to edge, nothing overflows, Tab order follows the page', async ({ page, request }) => {
      const assertClean = watchConsole(page);
      await setType(request, 'retail');
      // Pick a target whose preview offers value suggestions, so the rows are exercised.
      let target = '';
      for (const t of ['hospitality', 'cafe', 'service', 'market_stall', 'other']) {
        const html = await (await request.get(`/api/settings/shop-type/preview?to=${t}`)).text();
        if (html.includes('apply_values[]')) { target = t; break; }
      }
      test.info().annotations.push({ type: 'value-suggestion target', description: target || 'none: row checks skipped' });

      await page.goto(`/settings?lang=${lang}#settings-shop-type`);
      await page.locator('#shop-type-change').click();
      const group = page.locator('.shop-type-tiles');
      await expect(group.locator('button[role="radio"]')).toHaveCount(6);
      expect(await overflow(page), `pick step in ${lang}`).toBeLessThanOrEqual(1);
      await expectInsideViewport(page, '.shop-type-tiles button.shop-type-tile', 'pick tile');
      await expectTall(page, '.shop-type-tiles button.shop-type-tile', 'pick tile', 72);

      await group.locator(`button[data-value="${target || 'hospitality'}"]`).click();
      await page.locator('.shop-type-pick .shop-type-bar .btn.primary').click();
      const preview = page.locator('[data-testid="shop-type-preview"]');
      await expect(preview).toBeVisible();
      expect(await overflow(page), `preview in ${lang}`).toBeLessThanOrEqual(1);
      await expectInsideViewport(page, '[data-testid="shop-type-preview"] .shop-type-bar .btn', 'preview action');
      await expectTall(page, '[data-testid="shop-type-preview"] .shop-type-bar .btn', 'preview action');
      if (await preview.locator('.shop-type-more summary').count()) {
        await expectTall(page, '.shop-type-more summary', 'expand row');
      }

      const rows = preview.locator('label.shop-type-value');
      const n = await rows.count();
      if (target) expect(n, 'the preview offers value suggestions').toBeGreaterThan(0);
      for (let i = 0; i < n; i++) {
        const row = rows.nth(i);
        const b = (await row.boundingBox())!;
        expect(b.height, `value row ${i} height`).toBeGreaterThanOrEqual(MIN);
        // Whole row: the end edge, away from the checkbox, still toggles it.
        const cb = row.locator('input[type=checkbox]');
        await expect(cb).not.toBeChecked();
        await row.click({ position: { x: Math.max(b.width - 8, 1), y: b.height / 2 } });
        await expect(cb, `tapping the far edge of row ${i} ticks it`).toBeChecked();
        await row.click({ position: { x: Math.max(b.width - 8, 1), y: b.height / 2 } });
        await expect(cb).not.toBeChecked();
      }

      // Keyboard order = reading order: value checkboxes, the expander if any, Cancel, Confirm.
      const order = preview.locator('input[type=checkbox], summary, button');
      const count = await order.count();
      expect(count, 'Cancel + Confirm at least').toBeGreaterThanOrEqual(2);
      await page.locator('#shop-type-preview-title').focus();
      for (let i = 0; i < count; i++) {
        await page.keyboard.press('Tab');
        await expect(order.nth(i), `Tab stop ${i + 1} of ${count} is the ${i + 1}th control in the page`).toBeFocused();
      }
      await page.screenshot({ path: test.info().outputPath(`preview-${lang}-360.png`), fullPage: true });
      assertClean();
    });

    test('undo banner: fits, both actions are real targets, Undo then Dismiss by keyboard', async ({ page, request }) => {
      const assertClean = watchConsole(page);
      await setType(request, 'other');
      await setType(request, 'hospitality');
      for (const path of ['/settings', '/menu']) {
        await page.goto(`${path}?lang=${lang}`);
        const banner = page.locator('[data-testid="shop-type-undo"]');
        await expect(banner, `undo strip on ${path}`).toBeVisible();
        expect(await overflow(page), `${path} with the strip in ${lang}`).toBeLessThanOrEqual(1);
        await expectInsideViewport(page, '[data-testid="shop-type-undo"]', 'undo strip');
        await expectInsideViewport(page, '[data-testid="shop-type-undo"] .btn', 'undo action');
        await expectTall(page, '[data-testid="shop-type-undo"] .btn', 'undo action');
        const widths = await banner.locator('.btn').evaluateAll((els) => els.map((e) => e.getBoundingClientRect().width));
        for (const w of widths) expect(w, 'undo action width').toBeGreaterThanOrEqual(MIN);
        await banner.locator('.btn').first().focus();
        await page.keyboard.press('Tab');
        await expect(banner.locator('.btn').nth(1), 'Tab goes Undo -> Dismiss').toBeFocused();
      }
      await page.screenshot({ path: test.info().outputPath(`undo-banner-${lang}-360.png`) });
      assertClean();
    });
  });
}
