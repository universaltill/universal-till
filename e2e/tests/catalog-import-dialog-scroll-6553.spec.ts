import { test, expect } from './fixtures';
import { setOskMode, watchConsole } from './helpers';

// ut-docs#6553: #import-modal and #tax-codes-modal reuse .item-form-modal
// (flex column, overflow hidden, padding 0), but only #item-form-modal supplies
// its own pinned head + .catalog-form-body scroller. The two fetched pages were
// therefore clipped: no scroll on touch, no padding. Fix: the dialog itself is
// the scroller (CSS scoped by id). Playwright hasTouch is emulation only; the
// real-device check is the tablet run.
const DIALOGS = [
  { name: 'import', button: '#catalog-import-btn', dialog: '#import-modal' },
  { name: 'tax codes', button: '#catalog-taxcodes-btn', dialog: '#tax-codes-modal' },
];
const VIEWPORTS = [
  { label: '1280x800', width: 1280, height: 800, osk: false },
  { label: '360x800 with OSK', width: 360, height: 800, osk: true },
];

test.use({ hasTouch: true });

test.describe('#import-modal / #tax-codes-modal scroll and pad (ut-docs#6553)', () => {
  test.afterEach(async ({ page }) => {
    await setOskMode(page, 'auto');
  });

  for (const vp of VIEWPORTS) {
    for (const d of DIALOGS) {
      test(`${d.name} dialog scrolls with padding at ${vp.label}`, async ({ page }) => {
        const assertClean = watchConsole(page);
        await page.setViewportSize({ width: vp.width, height: vp.height });
        if (vp.osk) await setOskMode(page, 'on');
        await page.goto('/items');
        await page.locator(d.button).click();
        const dialog = page.locator(d.dialog);
        await expect(dialog).toBeVisible();
        await expect(dialog.locator('.page-head a.btn')).toBeVisible();
        // The fetched page may still be growing (lazy sections); measure once settled.
        await page.waitForLoadState('networkidle');

        // Deterministic overflow regardless of seed: a 2000px spacer then a
        // last text field (the OSK case needs a field to focus; the OSK's own
        // focus handling keeps a focused field in view, so it must be the last).
        await dialog.evaluate((el) => {
          const spacer = document.createElement('div');
          spacer.style.height = '2000px';
          const last = document.createElement('input');
          last.type = 'text';
          last.id = 'scroll-6553-last';
          el.append(spacer, last);
        });
        if (vp.osk) {
          await dialog.locator('#scroll-6553-last').tap();
          await expect(page.locator('#osk.osk-open')).toBeVisible();
          await expect(page.locator('body')).toHaveClass(/osk-padded/);
        }

        const m = await dialog.evaluate((el) => ({
          sh: el.scrollHeight,
          ch: el.clientHeight,
          oy: getComputedStyle(el).overflowY,
        }));
        expect(m.sh).toBeGreaterThan(m.ch);
        expect(m.oy).toBe('auto');


        // Real scroll to the end.
        // Polled: the OSK's own focus handling may scroll the field into view
        // after open; scroll again until the end sticks.
        await expect
          .poll(() =>
            dialog.evaluate((el) => {
              el.scrollTop = el.scrollHeight;
              return el.scrollHeight - el.clientHeight - el.scrollTop < 1;
            }),
          )
          .toBe(true);
        expect(await dialog.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
        const last = dialog.locator('#scroll-6553-last');
        await expect(last).toBeInViewport();
        if (vp.osk) {
          const osk = await page.locator('#osk').boundingBox();
          const box = await last.boundingBox();
          expect(osk).not.toBeNull();
          expect(box!.y + box!.height).toBeLessThanOrEqual(osk!.y + 0.5);
        }

        // Padding: first child is inset from the dialog edge.
        const inset = await dialog.evaluate((el) => {
          const first = el.firstElementChild as HTMLElement;
          return first.getBoundingClientRect().left - el.getBoundingClientRect().left;
        });
        expect(inset).toBeGreaterThanOrEqual(12);

        // Back link stays reachable (sticky head) and closes the dialog.
        const back = dialog.locator('.page-head a.btn');
        await expect(back).toBeInViewport();
        await back.click();
        await expect(dialog).toBeHidden();
        assertClean();
      });
    }
  }

  test('regression guards: closed dialogs hidden, #item-form-modal untouched', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/items');
    await expect(page.locator('#import-modal')).toBeHidden();
    await expect(page.locator('#tax-codes-modal')).toBeHidden();
    // Still hidden after being opened and closed again.
    await page.locator('#catalog-import-btn').click();
    await expect(page.locator('#import-modal')).toBeVisible();
    await page.locator('#import-modal .page-head a.btn').click();
    await expect(page.locator('#import-modal')).toBeHidden();

    const item = page.locator('#item-form-modal');
    const css = () =>
      item.evaluate((el) => {
        const s = getComputedStyle(el);
        return { overflowY: s.overflowY, padding: s.paddingTop + s.paddingLeft };
      });
    await page.locator('#item-form-add-btn').click();
    await expect(item).toBeVisible();
    const head = page.locator('#item-form-modal .catalog-form-head');
    const before = await head.boundingBox();
    await page.locator('#item-form-modal .catalog-form-body').evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    expect(await head.boundingBox()).toEqual(before);
    expect(await css()).toEqual({ overflowY: 'hidden', padding: '0px0px' });
  });
});
