import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { ensureOperator, markTourDone, setOskMode, DIALOGS, openOsk, actionsClearance } from './helpers';
import type { Dlg } from './helpers';

// ut-docs#5309: same bug class as #5304 (manager-PIN dialog). On a 1024x600
// till with the on-screen keyboard open, the OSK (z 1000, fixed to the bottom)
// covered the Cancel/submit row of the non-modal #hold-modal, #pfand-modal and
// #table-add-modal. With body.osk-padded each dialog is now top-anchored,
// capped to the band above the OSK, scrolls inside itself, and its action row
// is pinned to the bottom edge.
//
// Chromium touch emulation only (hasTouch), NOT real Pi touch hardware.
// OSK mode is forced to 'on' (tapping a field opens it; pfand/table-add have
// no per-dialog toggle) and restored to 'auto' afterwards.

test.use({ hasTouch: true, viewport: { width: 1024, height: 600 } });

// Dialog, Cancel and the submit button lie wholly above the OSK, inside the
// viewport, and are what elementFromPoint finds at their centre.
async function assertClear(page: Page, id: string) {
  await expect.poll(() => page.evaluate((id) => {
    const dlg = document.getElementById(id)!;
    const oskTop = document.getElementById('osk')!.getBoundingClientRect().top;
    const d = dlg.getBoundingClientRect();
    const out: Record<string, unknown> = { oskTop, dlgTop: d.top, dlgBottom: d.bottom };
    out.dlgOk = d.top >= 0 && d.bottom <= oskTop;
    for (const [name, sel] of [['cancel', '.modifier-actions button[type=button]'], ['submit', '.modifier-actions button[type=submit]']]) {
      const el = dlg.querySelector(sel) as HTMLElement;
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      out[name] = r.top >= 0 && r.bottom <= oskTop && !!hit && el.contains(hit);
      out[name + 'Rect'] = [r.top, r.bottom];
    }
    return out;
  }, id), { message: `#${id}: dialog, Cancel and submit above the OSK and hit-testable` })
    .toMatchObject({ dlgOk: true, cancel: true, submit: true });
}

const snap = (page: Page, id: string) => page.evaluate((id) => {
  const s = getComputedStyle(document.getElementById(id)!);
  return { top: s.insetBlockStart, max: s.maxBlockSize, ov: s.overflowY };
}, id);

test.describe('hold / pfand / table-add dialogs stay above the OSK at 1024x600 (ut-docs#5309)', () => {
  test.afterEach(async ({ page }) => {
    await setOskMode(page, 'auto');
    await page.request.post('/api/pos/reset');
  });

  for (const d of DIALOGS.filter((x) => x.id !== 'elevation-modal')) {
    test.describe(`#${d.id}`, () => {
      test.beforeEach(async ({ page }) => {
        await ensureOperator(page);
        await markTourDone(page);
        await setOskMode(page, 'on');
      });

      test('Cancel/submit clear of the OSK, Cancel is really tappable', async ({ page }) => {
        await d.open(page);
        await openOsk(page, d);
        await assertClear(page, d.id);
        const dlg = page.locator(`#${d.id}`);
        await dlg.locator('.modifier-actions button[type=button]').tap(); // real tap, no force
        await expect(dlg).not.toHaveAttribute('open', '');
      });

      test('focused field is in view above the OSK and clear of the action row', async ({ page }) => {
        // ut-docs#5334 R5: assert osk.js's own centring of the tapped field (no
        // scrollIntoViewIfNeeded, which would let Playwright do the scrolling).
        await d.open(page);
        await openOsk(page, d);
        await expect.poll(() => actionsClearance(page, d.id, d.field), { message: `#${d.id} ${d.field}` }).toEqual({ ok: true });
      });

      test('closing the OSK restores 8vh; reopening is clear again', async ({ page }) => {
        await d.open(page);
        const before = await snap(page, d.id);
        expect(before.top).toBe('48px');
        await openOsk(page, d);
        await page.locator(d.field).evaluate((el) => (el as HTMLElement).blur());
        await expect(page.locator('body')).not.toHaveClass(/osk-padded/);
        expect(await snap(page, d.id)).toEqual(before);
        await openOsk(page, d);
        await assertClear(page, d.id);
      });
    });
  }

  test('#table-add-modal (tallest) scrolls inside itself with the OSK open', async ({ page }) => {
    await ensureOperator(page);
    await markTourDone(page);
    await setOskMode(page, 'on');
    const d = DIALOGS.find((x) => x.id === 'table-add-modal')!;
    await d.open(page);
    await openOsk(page, d);
    const m = await page.locator('#table-add-modal').evaluate((el) => ({ sh: el.scrollHeight, ch: el.clientHeight, oy: getComputedStyle(el).overflowY }));
    expect(m.oy).toBe('auto');
    expect(m.sh).toBeGreaterThan(m.ch);
  });

  test('#hold-modal measurement (numbers in the failure/annotation)', async ({ page }) => {
    await ensureOperator(page);
    await markTourDone(page);
    await setOskMode(page, 'on');
    const d = DIALOGS.find((x) => x.id === 'hold-modal')!;
    await d.open(page);
    await openOsk(page, d);
    const m = await page.evaluate(() => {
      const dlg = document.getElementById('hold-modal')!;
      const b = dlg.querySelector('.modifier-actions')!.getBoundingClientRect().bottom;
      return { actionsBottom: b, dlgBottom: dlg.getBoundingClientRect().bottom, oskTop: document.getElementById('osk')!.getBoundingClientRect().top };
    });
    test.info().annotations.push({ type: 'hold-measure', description: JSON.stringify(m) });
    console.log('HOLD-MEASURE', JSON.stringify(m));
    expect(m.actionsBottom).toBeLessThanOrEqual(m.oskTop);
  });
});
