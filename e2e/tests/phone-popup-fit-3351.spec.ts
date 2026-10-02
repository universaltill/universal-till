import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { WORKER_TILL_EFFECTS_LEVEL } from './worker-till';

// ut-docs#3351: on the iPhone app (TestFlight v0.30.13/14) the product owner
// found that "all popup menus open in a zoom mode and I have to pinch them to
// become fit". Two things do that on iOS: a popup wider than the screen
// (shown cut off until the user pinches out), and a popup that focuses a
// field iOS zooms into (below). This
// spec opens every popup it can reach at 390 and 440 CSS px with the real
// ADR-0122 popup motion running (the suite is reduced-motion by default, so
// it emulates 'no-preference' at the Full effects level), waits for the
// motion to end and asserts the popup rests fully on screen at scale 1.
// It also asserts every field inside the open popup is at least 16px: iOS
// zooms the page in when a smaller field takes focus, and the record
// dialogs, the item form, #pfand-modal and #table-add-modal focus their
// first field as they open -- on v0.30.13/14 (13.9-14.4px fields, before
// ut-docs#3350) that is exactly "opens zoomed, pinch to fit". 3350's own
// spec cannot see these fields: a closed dialog's fields have no box.
//
// Each route: (1) every popup trigger it renders (record-dialog create and
// tap-to-edit, aria-haspopup="dialog" buttons, the item/import/tax-code
// dialogs, the payment panel, the bug-report panel) is clicked; (2) every
// <dialog> on the page that no trigger opened is opened with .show(), the
// same call every till dialog's own code makes -- its frame must fit even
// before the server fills it.

const ROUTES = ['/', '/admin', '/audit', '/backoffice', '/bluetooth-devices', '/catalog', '/catalog/option-sets',
  '/catalog/tax-codes', '/categories', '/country-settings', '/designer', '/fiscal-device', '/fiscal-register',
  '/help', '/import', '/inventory', '/items', '/journal', '/kitchen-stations', '/locations', '/menu',
  '/modifiers', '/my-reports', '/open-orders', '/orders', '/plugins', '/plugins/store', '/promotions',
  '/receipt-designer', '/registers', '/report-issue', '/reports', '/settings', '/settings/menu', '/shifts',
  '/tables', '/tills', '/translations', '/users', '/users/permissions'];

// Clicked, in this order. Nothing here submits or deletes: these only open.
const TRIGGERS = [
  '[data-record-dialog-open]',
  '[data-record-open]',
  '[aria-haspopup="dialog"]',
  '#item-form-add-btn',
  '#catalog-import-btn',
  '#catalog-taxcodes-btn',
  '.payment-trigger',
  '[aria-controls="bugreport-panel"]',
  '.nav-drawer-toggle',
  '.basket-phonebar',
];

type Fit = {
  name: string;
  vw: number;
  innerWidth: number;
  scale: number;
  box: { l: number; r: number; w: number };
  transforms: string[];
  sw: number;
  offenders: string[];
  smallFields: string[];
  focused: string;
};

// The popups open right now (a <dialog open>, the shown bug-report panel,
// the phone drawer or basket sheet), keyed so the same element is never
// measured twice on one route.
async function openPopups(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const out: string[] = [];
    document.querySelectorAll('dialog[open]:not([data-ut-closing])').forEach((d, i) => out.push(d.id ? `#${d.id}` : `dialog:nth(${i}).${String(d.className).split(/\s+/)[0]}`));
    const br = document.getElementById('bugreport-panel');
    if (br && br.classList.contains('open') && br.getClientRects().length) out.push('#bugreport-panel');
    // The phone tier's ☰ drawer and basket sheet are popups too.
    if (document.body.classList.contains('nav-drawer-open')) out.push('#nav-drawer');
    if (document.body.classList.contains('pos-sheet-open')) out.push('#basket');
    return out;
  });
}

// Waits until the popup's own motion (and any descendant's finite one) has
// ended, then measures it. Infinite animations (a spinner) never end and
// are not motion of the popup, so they are not waited for.
async function measure(page: Page, sel: string): Promise<Fit> {
  return page.evaluate(async (sel) => {
    const find = (): Element | null => {
      if (sel.startsWith('#')) return document.querySelector(sel);
      const m = /^dialog:nth\((\d+)\)/.exec(sel);
      return m ? document.querySelectorAll('dialog[open]:not([data-ut-closing])')[Number(m[1])] : null;
    };
    const el = find()!;
    const finite = () => el.getAnimations({ subtree: true }).filter((a) => {
      const t = a.effect?.getComputedTiming();
      return a.playState !== 'finished' && t && Number.isFinite(Number(t.endTime));
    });
    for (let i = 0; i < 40 && finite().length; i++) {
      await Promise.race([Promise.all(finite().map((a) => a.finished.catch(() => null))), new Promise((r) => setTimeout(r, 100))]);
    }
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    const vw = document.documentElement.clientWidth;
    const r = el.getBoundingClientRect();
    const transforms: string[] = [];
    for (let p: Element | null = el; p && p !== document.documentElement; p = p.parentElement) {
      const t = getComputedStyle(p).transform;
      if (t !== 'none' && t !== 'matrix(1, 0, 0, 1, 0, 0)') {
        // A popup's own centring translate (e.g. .shrinkage-sheet's
        // translateY(-50%)) is not a zoom: only scale/skew fails.
        const m = new DOMMatrixReadOnly(t);
        if (Math.abs(m.a - 1) > 1e-3 || Math.abs(m.d - 1) > 1e-3 || Math.abs(m.b) > 1e-3 || Math.abs(m.c) > 1e-3) {
          transforms.push(`${p.tagName.toLowerCase()}${p.id ? '#' + p.id : ''}: ${t}`);
        }
      }
    }
    // What pokes past either inline edge, outermost first, skipping anything
    // inside a box that scrolls or clips horizontally within the viewport
    // (a table's own scroll card) -- those are reachable without a pinch.
    // hidden/clip ancestors are waved through deliberately: clipped content
    // is a layout bug, not the zoom this card guards (ut-docs#3351).
    const reachable = (e: Element) => {
      for (let p = e.parentElement; p && p !== el.parentElement; p = p.parentElement) {
        const b = p.getBoundingClientRect();
        if (/(auto|scroll|hidden|clip)/.test(getComputedStyle(p).overflowX) && b.right <= vw + 1 && b.left >= -1) return true;
      }
      return false;
    };
    const offenders: string[] = [];
    el.querySelectorAll('*').forEach((e) => {
      const b = e.getBoundingClientRect();
      if (!b.width || !b.height || (b.right <= vw + 1 && b.left >= -1) || reachable(e)) return;
      if (e.closest('dialog:not([open]), [hidden]')) return;
      const s = getComputedStyle(e);
      if (s.visibility === 'hidden' || s.display === 'none') return;
      offenders.push(`${e.tagName.toLowerCase()}${e.id ? '#' + e.id : ''}.${String(e.className).trim().split(/\s+/)[0]} [${Math.round(b.left)},${Math.round(b.right)}]`);
    });
    // iOS WebKit zooms the page in when a field under 16px takes focus --
    // a popup that focuses one on open "opens zoomed" (ut-docs#3350's rule,
    // which a closed dialog's fields escaped: they have no box at load).
    const skip = new Set(['checkbox', 'radio', 'range', 'color', 'hidden', 'button', 'submit', 'reset', 'image', 'file']);
    const smallFields: string[] = [];
    el.querySelectorAll('input, select, textarea').forEach((f) => {
      if (f instanceof HTMLInputElement && skip.has(f.type)) return;
      const b = f.getBoundingClientRect();
      if (!b.width || !b.height) return;
      const fs = parseFloat(getComputedStyle(f).fontSize);
      if (fs < 16) smallFields.push(`${f.tagName.toLowerCase()}[${f.getAttribute('name') || f.id || (f as HTMLElement).className}] ${fs.toFixed(1)}px`);
    });
    const a = document.activeElement;
    const focused = a && el.contains(a) && a.matches('input, select, textarea')
      ? `${a.tagName.toLowerCase()}[${a.getAttribute('name') || a.id}] ${parseFloat(getComputedStyle(a).fontSize).toFixed(1)}px` : '';
    return {
      smallFields, focused,
      name: sel, vw, innerWidth: window.innerWidth, scale: window.visualViewport ? window.visualViewport.scale : 1,
      box: { l: Math.round(r.left), r: Math.round(r.right), w: Math.round(r.width) },
      transforms, sw: document.documentElement.scrollWidth, offenders: offenders.slice(0, 5),
    };
  }, sel);
}

function problems(f: Fit, width: number): string[] {
  const p: string[] = [];
  if (f.box.l < -1) p.push(`starts ${-f.box.l}px off the screen`);
  if (f.box.r > f.vw + 1) p.push(`ends ${f.box.r - f.vw}px past the screen (w=${f.box.w}, vw=${f.vw})`);
  if (f.box.w > f.vw + 1) p.push(`wider than the screen (w=${f.box.w})`);
  if (f.transforms.length) p.push(`still transformed: ${f.transforms.join('; ')}`);
  if (f.sw > f.vw + 1) p.push(`page scrolls sideways while open (scrollWidth=${f.sw})`);
  if (f.innerWidth !== width || Math.abs(f.scale - 1) > 1e-3) p.push(`page zoomed out to fit (innerWidth=${f.innerWidth}, scale=${f.scale})`);
  if (f.offenders.length) p.push(`content past the screen: ${f.offenders.join(', ')}`);
  if (f.smallFields.length) p.push(`fields iOS zooms into on focus: ${f.smallFields.join(', ')}${f.focused ? ` (focused on open: ${f.focused})` : ''}`);
  return p;
}

async function closeAll(page: Page) {
  await page.evaluate(() => {
    document.querySelectorAll('dialog[open]').forEach((d) => (d as HTMLDialogElement).close());
    const br = document.getElementById('bugreport-panel');
    const close = document.getElementById('bugreport-close');
    if (br && br.classList.contains('open') && close) (close as HTMLElement).click();
    if (document.body.classList.contains('nav-drawer-open')) (document.querySelector('.nav-drawer-toggle') as HTMLElement | null)?.click();
    if (document.body.classList.contains('pos-sheet-open')) (document.querySelector('.basket-sheet-close') as HTMLElement | null)?.click();
  });
  // Let the closing shrink (ADR-0123) end before the next popup opens.
  await page.waitForFunction(() => !document.querySelector('[data-ut-closing]'), undefined, { timeout: 3_000 }).catch(() => {});
}

async function gotoRoute(page: Page, route: string) {
  await page.goto(route);
  await page.waitForLoadState('load');
  await page.waitForTimeout(300);
}

for (const [w, h] of [[390, 844], [440, 956]] as const) {
  test.describe(`phone ${w}px: every popup opens fitted to the screen (ut-docs#3351)`, () => {
    test.use({ viewport: { width: w, height: h }, hasTouch: true, isMobile: true });

    test.beforeEach(async ({ page }) => {
      const res = await page.request.post('/api/settings/effects-level', { form: { level: 'full' } });
      expect(res.status()).toBe(204);
      await page.emulateMedia({ reducedMotion: 'no-preference' });
    });
    test.afterEach(async ({ page }) => {
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await page.request.post('/api/settings/effects-level', { form: { level: WORKER_TILL_EFFECTS_LEVEL } });
      await page.request.post('/api/pos/reset').catch(() => {});
    });

    test(`every reachable popup fits at ${w}px with scale 1 once its motion ends`, async ({ page }) => {
      test.setTimeout(600_000);
      const failures: string[] = [];
      const measured: string[] = [];
      // The sale screen's payment panel needs a basket line to open.
      await page.request.post('/api/pos/reset');

      for (const route of ROUTES) {
        await gotoRoute(page, route);
        if (route === '/') {
          await page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first().click();
          await page.waitForTimeout(300);
        }
        const seen = new Set<string>();
        const check = async (via: string) => {
          for (const sel of await openPopups(page)) {
            if (seen.has(sel)) continue;
            const f = await measure(page, sel);
            // Open but not rendered (its container is hidden, e.g. Pay from
            // the closed basket sheet): nothing on screen to fit -- measured
            // again when reached the way a phone user reaches it.
            if (!f.box.w) continue;
            seen.add(sel);
            measured.push(`${route} ${sel} w=${f.box.w}${f.focused ? ` focus=${f.focused}` : ''}`);
            const p = problems(f, w);
            if (p.length) failures.push(`${route} ${sel} (${via}): ${p.join(' | ')}`);
          }
        };

        // (1) Real triggers: one per popup they open (a list's rows all open
        // the same dialog), marked so a reload finds the same ones again.
        const markTriggers = () => page.evaluate((sels) => {
          const keys = new Set<string>();
          let n = 0;
          sels.forEach((s) => document.querySelectorAll(s).forEach((el) => {
            if (el.closest('dialog, [hidden]') || (el as HTMLButtonElement).disabled) return;
            const key = el.getAttribute('aria-controls') || el.getAttribute('data-record-dialog-open')
              || el.getAttribute('data-record-open') || el.id || `${s}|${el.className}`;
            if (keys.has(key)) return;
            keys.add(key);
            el.setAttribute('data-pf3351', String(n++));
          }));
          return n;
        }, TRIGGERS);
        // The sale screen's basket popups (Pay, a line's remove sheet) open
        // from inside the phone's basket sheet, so run its triggers again
        // with the sheet up, the way a phone user reaches them.
        for (const sheet of route === '/' ? [false, true] : [false]) {
          const count = await markTriggers();
          for (let i = 0; i < count; i++) {
            if (sheet && !(await page.evaluate(() => document.body.classList.contains('pos-sheet-open')))) {
              await page.locator('.basket-phonebar').click();
              await expect(page.locator('body')).toHaveClass(/pos-sheet-open/);
              await page.waitForTimeout(400);
            }
            const url = page.url();
            const el = page.locator(`[data-pf3351="${i}"]`);
            if (!(await el.count())) continue;
            const via = await el.evaluate((e) => e.outerHTML.slice(0, 80));
            if (await el.isVisible()) await el.click({ timeout: 3_000 }).catch(() => el.evaluate((e) => (e as HTMLElement).click()));
            else await el.evaluate((e) => (e as HTMLElement).click());
            await page.waitForTimeout(400);
            if (page.url() !== url) { await gotoRoute(page, route); await markTriggers(); continue; }
            await check(via);
            await closeAll(page);
          }
        }

        // (2) Every other <dialog> on the page, opened the way its own code does.
        const rest = await page.evaluate(() => Array.from(document.querySelectorAll('dialog'))
          .filter((d) => d.id && !d.closest('[hidden]')).map((d) => ({ id: d.id })));
        for (const d of rest) {
          if (seen.has(`#${d.id}`)) continue;
          const opened = await page.evaluate((id) => {
            const el = document.getElementById(id) as HTMLDialogElement | null;
            if (!el || el.open) return false;
            try { el.show(); } catch { return false; }
            return el.open;
          }, d.id);
          if (!opened) continue;
          await page.waitForTimeout(50);
          await check('show()');
          await closeAll(page);
        }
      }
      const distinct = new Set(measured.map((m) => m.split(' ')[1]));
      console.log(`ut-docs#3351 @${w}px measured ${measured.length} popups (${distinct.size} distinct):\n  ${measured.join('\n  ')}`);
      // Not vacuous: a sweep that stops finding popups would pass for nothing.
      // 29 distinct today; the floor lets at most one silently drop out.
      expect(distinct.size, [...distinct].join(' ')).toBeGreaterThanOrEqual(28);
      expect(failures, failures.join('\n')).toEqual([]);
    });
  });
}
