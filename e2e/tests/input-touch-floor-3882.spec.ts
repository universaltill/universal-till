import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { openNewItemForm } from './helpers';

// ut-docs#3882: the shared `input, select, textarea` rule (app.css) gave text,
// number and select fields ~40-42px of height, and `body.kiosk input,
// body.kiosk select { min-height: 2.4rem }` shrank them to ~38px in kiosk
// mode -- both below the 46px touch floor `.btn-touch` already enforces. The
// fix is one zero-specificity floor in the shared stylesheet (so any class
// rule that sizes a field still wins) plus removing the kiosk shrink.
//
// This spec measures REAL rendered border-box heights of every visible
// text-like input and <select> on a handful of real forms, windowed and with
// body.kiosk set (kiosk is a body class the server adds from UT_KIOSK; the CSS
// keys only on that class, so toggling it is equivalent), at the 1024x600
// kiosk floor and at 360px.
//
// Named exceptions (deliberately compact, own tier rules, excluded below):
//  - the basket line's `.qty-input` / `.disc-input` (budgeted compact row);
//  - the sale screen's `.scan-row` barcode/qty fields (fixed 2.5rem by the
//    product owner, ut-docs#2702; the row's buttons are its touch targets);
//  - the borderless `.option-set-value-edit` text inside a value chip.
// textarea is out of scope (multi-line, already tall).

const FLOOR = 46;
const TOLERANCE = 0.5;

const SELECTOR =
  'input:not([type=checkbox]):not([type=radio]):not([type=range]):not([type=color])' +
  ':not([type=file]):not([type=hidden]):not([type=image]):not([type=button])' +
  ':not([type=submit]):not([type=reset]), select';

async function undersized(page: Page, opts: { requireFields?: boolean } = {}): Promise<string[]> {
  const out = await page.evaluate(
    ({ sel, floor, tol, requireFields }) => {
      const out: string[] = [];
      let measured = 0;
      for (const el of Array.from(document.querySelectorAll<HTMLElement>(sel))) {
        if (el.matches('.qty-input, .disc-input, .scan-row input, .option-set-value-edit input')) continue;
        const cs = getComputedStyle(el);
        if (cs.visibility === 'hidden' || cs.display === 'none') continue;
        const r = el.getBoundingClientRect();
        if (r.width === 0 || r.height === 0) continue; // not rendered
        measured++;
        if (r.height < floor - tol) {
          out.push(
            `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}` +
              `${el.className ? '.' + String(el.className).trim().split(/\s+/).join('.') : ''}` +
              `[type=${(el as HTMLInputElement).type || ''}]${el.getAttribute('name') ? '[name=' + el.getAttribute('name') + ']' : ''} in ${el.parentElement?.tagName.toLowerCase()}${el.parentElement?.className ? '.' + String(el.parentElement.className).trim().split(/\s+/).join('.') : ''} ${r.height.toFixed(1)}px`,
          );
        }
      }
      // Guard against a vacuous pass: at least one field must actually have
      // been measured (excluded and unrendered fields don't count).
      if (requireFields && measured === 0) out.push('NO FIELDS MEASURED (page did not render a form?)');
      return out;
    },
    { sel: SELECTOR, floor: FLOOR, tol: TOLERANCE, requireFields: opts.requireFields ?? true },
  );
  return out;
}

const VIEWPORTS = [
  { width: 1024, height: 600, label: '1024x600' },
  { width: 360, height: 740, label: '360px' },
];

const PAGES: Array<{ name: string; path: string; prep?: (p: Page) => Promise<void> }> = [
  { name: 'settings', path: '/settings' },
  // A settings section with real form fields (the removed .settings-grid
  // form 2.3rem cap lived here).
  { name: 'settings payments', path: '/settings#settings-payments' },
  // The manual's search box (.manual-search input had its own 44px cap).
  { name: 'help', path: '/help' },
  { name: 'users', path: '/users' },
  { name: 'inventory', path: '/inventory' },
  {
    name: 'catalog item form',
    path: '/catalog',
    prep: async (p) => {
      await openNewItemForm(p);
    },
  },
];

test.describe('inputs and selects meet the 46px touch floor (ut-docs#3882)', () => {
  for (const vp of VIEWPORTS) {
    for (const kiosk of [false, true]) {
      for (const pg of PAGES) {
        test(`${pg.name} at ${vp.label}${kiosk ? ' (kiosk)' : ''}`, async ({ page }) => {
          await page.setViewportSize({ width: vp.width, height: vp.height });
          await page.goto(pg.path);
          if (kiosk) await page.evaluate(() => document.body.classList.add('kiosk'));
          if (pg.prep) await pg.prep(page);
          const bad = await undersized(page);
          expect(bad, `fields under ${FLOOR}px on ${pg.path}`).toEqual([]);
        });
      }
    }
  }

  test('sale screen: basket line stays compact (named exceptions)', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('.basket table tbody tr')).toHaveCount(1);
    // The floor must not grow the budgeted basket row: its qty field keeps
    // its compact size (well under the 46px form floor) at this tier.
    const qty = await page.locator('.basket .qty-input').first().boundingBox();
    expect(qty, 'basket qty field rendered').not.toBeNull();
    expect(qty!.height).toBeLessThan(FLOOR - TOLERANCE);
    expect(await undersized(page, { requireFields: false })).toEqual([]);
    await page.request.post('/api/pos/reset');
  });
});
