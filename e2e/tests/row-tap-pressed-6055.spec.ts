import { test, expect } from './fixtures';

// ut-docs#6055: a row-tap list row (`[data-record-open]`) gave no feedback on
// touch (the only affordance was :hover, which a finger never produces). The
// shell's ut-docs#3000 press script now marks the pressed row `.ut-pressed`
// synchronously on pointerdown; app.css paints it (colour only).

test.use({ hasTouch: true });

type CDP = import('@playwright/test').CDPSession;
type Page = import('@playwright/test').Page;

async function touch(cdp: CDP, type: 'touchStart' | 'touchMove' | 'touchEnd', x: number, y: number) {
  await cdp.send('Input.dispatchTouchEvent', {
    type,
    touchPoints: type === 'touchEnd' ? [] : [{ x, y, id: 1, radiusX: 4, radiusY: 4, force: 1 }],
  });
}

async function ensureRow(page: Page, path: string, addSel: string, nameSel: string, rowSel: string) {
  await page.goto(path);
  if (await page.locator(rowSel).count() === 0) {
    await page.locator(addSel).first().click();
    await page.locator(nameSel).fill(`Row Tap ${Date.now().toString(36)}`);
    await page.locator('dialog[open] .record-dialog-save').first().click();
    await page.waitForLoadState('networkidle');
    await page.goto(path);
  }
}

const LISTS = [
  { path: '/categories', row: '.category-row', dialog: '#category-dialog', add: '#categories-new', name: '#category-dialog input[name=name]' },
  { path: '/locations', row: '.location-row', dialog: '#location-dialog', add: '#locations-new', name: '#location-dialog input[name=name]' },
];

for (const l of LISTS) {
  test.describe(`pressed row on ${l.path} (ut-docs#6055)`, () => {
    test('touch down paints the row pressed, a pan or release clears it', async ({ page }) => {
      await ensureRow(page, l.path, l.add, l.name, l.row);
      const row = page.locator(l.row).first();
      const cell = row.locator('td').last();
      await row.scrollIntoViewIfNeeded();
      // Park the hover state off the row so the baseline is the resting colour.
      const restBg = await cell.evaluate((el) => getComputedStyle(el).backgroundColor);
      const box = (await cell.boundingBox())!;
      const x = box.x + box.width / 2, y = box.y + box.height / 2;
      const cdp = await page.context().newCDPSession(page);

      await touch(cdp, 'touchStart', x, y);
      const s = await row.evaluate((el) => {
        const td = el.querySelector('td:last-child') as HTMLElement;
        return { cls: el.classList.contains('ut-pressed'), bg: getComputedStyle(td).backgroundColor, tr: getComputedStyle(td).transform };
      });
      expect(s.cls, 'row marked pressed on touch down').toBe(true);
      expect(s.bg, 'pressed is a colour change').not.toBe(restBg);
      expect(s.tr).toBe('none');
      // Pressed must also differ from the hover colour a sticky tap leaves.
      await touch(cdp, 'touchMove', x, y + 60);
      await expect(row, 'moved past the slop: not pressed').not.toHaveClass(/ut-pressed/);
      await touch(cdp, 'touchEnd', x, y + 60);

      await touch(cdp, 'touchStart', x, y);
      await expect(row).toHaveClass(/ut-pressed/);
      await touch(cdp, 'touchEnd', x, y);
      await expect(row, 'cleared on release').not.toHaveClass(/ut-pressed/);
    });

    test('a tap still opens the dialog', async ({ page }) => {
      await ensureRow(page, l.path, l.add, l.name, l.row);
      const cell = page.locator(l.row).first().locator('td').last();
      await cell.tap();
      await expect(page.locator(l.dialog)).toHaveJSProperty('open', true);
    });
  });
}

// Fires one primary touch pointerdown at `sel`'s first match and reports
// whether its row (the closest [data-record-open]) got .ut-pressed, then
// cancels the press so nothing leaks into the next probe.
async function pressMarks(page: Page, sel: string) {
  return page.evaluate((s) => {
    const t = document.querySelector(s) as HTMLElement;
    const row = t.closest('[data-record-open]') as HTMLElement;
    const r = t.getBoundingClientRect();
    t.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true, pointerType: 'touch', isPrimary: true, button: 0, clientX: r.left + 2, clientY: r.top + 2 }));
    const marked = row.classList.contains('ut-pressed');
    window.dispatchEvent(new PointerEvent('pointercancel', { pointerType: 'touch' }));
    return marked;
  }, sel);
}

test('only a press that opens the row marks it: not a link in the row, not a row in a dialog', async ({ page }) => {
  await ensureRow(page, '/locations', '#locations-new', '#location-dialog input[name=name]', '.location-row');
  // A link inside a row keeps its own job (kitchen stations' "View display";
  // record-dialog.js never opens the row for it), so the row is not pressed.
  await page.evaluate(() => {
    const td = document.querySelector('.location-row td:last-child')!;
    td.insertAdjacentHTML('beforeend', '<a id="probe-link" href="/locations">x</a>');
  });
  expect(await pressMarks(page, '.location-row [data-record-edit]'), 'control: the edit button presses the row').toBe(true);
  expect(await pressMarks(page, '#probe-link'), 'a link in the row').toBe(false);

  // The same kind of row inside an open dialog is never pressed.
  await page.locator('#locations-new').first().click();
  const dlg = page.locator('#location-dialog');
  await expect(dlg).toHaveJSProperty('open', true);
  await dlg.evaluate((el) => {
    el.insertAdjacentHTML('beforeend', '<table><tbody><tr id="probe-row" data-record-open="location-dialog"><td id="probe-cell">x</td></tr></tbody></table>');
  });
  expect(await pressMarks(page, '#probe-cell'), 'a row inside a dialog').toBe(false);
});

test.describe('phone card list (ut-docs#6055)', () => {
  test.use({ viewport: { width: 360, height: 740 } });
  test('the pressed colour paints the card itself, not a block of cells', async ({ page }) => {
    await ensureRow(page, '/categories', '#categories-new', '#category-dialog input[name=name]', '.category-row');
    const row = page.locator('.category-row.t-cards-row').first();
    await expect(row).toBeVisible();
    const rest = await row.evaluate((el) => getComputedStyle(el).backgroundColor);
    const s = await row.evaluate((el) => {
      el.classList.add('ut-pressed');
      const out = { tr: getComputedStyle(el).backgroundColor, td: getComputedStyle(el.querySelector('td')!).backgroundColor };
      el.classList.remove('ut-pressed');
      return out;
    });
    expect(s.tr, 'the card is painted pressed').not.toBe(rest);
    expect(s.td, 'its cells stay transparent').toBe('rgba(0, 0, 0, 0)');
  });
});
