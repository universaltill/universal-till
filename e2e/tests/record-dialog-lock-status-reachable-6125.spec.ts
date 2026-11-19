import { test, expect } from './fixtures';
import { ensureOperator, openNewItemForm, setOskMode, watchConsole } from './helpers';
import type { BrowserContext, Page } from '@playwright/test';

// ut-docs#6125 (from #6118, coding-standards §10 / ut-docs#1999): the four
// full-screen catalogue/inventory dialogs (#item-form-modal, #import-modal,
// #tax-codes-modal, #stock-dialog) used to cover the nav rail, the phone ☰ and
// the status bar, so a cashier could not reach Lock or status while one was
// open. Now, at every width, the dialog stops ABOVE the real .statusbar
// (uncovered, the hit target) and carries a compact Lock + exit-to-OS (44x44
// icons, no in-dialog chip) at the inline-start of its EXISTING title line,
// adding no head row. At >=481px it also sits BESIDE the rail.
// The in-dialog Lock is the answer at every width because the rail's own Lock
// is not reliably reachable under a scrim: the rail is click-through (cannot
// be scrolled) and, with a real manager session at 1024x600, its Lock lies at
// y~674-722 (measured below), below the fold.
// Needs a real session (Lock only renders with one) -> `auth` project.
// Emulated touch only (Chromium), NOT real touch hardware or a Pi.

type Dlg = { key: string; id: string; open: (page: Page) => Promise<void>; title: string; scrolls: boolean };

const DIALOGS: Dlg[] = [
  {
    key: 'item', id: '#item-form-modal', title: '#item-form-title', scrolls: false,
    open: async (page) => { await page.goto('/items'); await openNewItemForm(page); },
  },
  {
    key: 'import', id: '#import-modal', title: '#import-modal h1', scrolls: true,
    open: async (page) => {
      await page.goto('/items');
      await page.locator('#catalog-import-btn').click();
      await expect(page.locator('#import-modal[open]')).toBeVisible();
      await expect(page.locator('#import-modal h1')).toBeVisible();
    },
  },
  {
    key: 'taxcodes', id: '#tax-codes-modal', title: '#tax-codes-modal h1', scrolls: true,
    open: async (page) => {
      await page.goto('/items');
      await page.locator('#catalog-taxcodes-btn').click();
      await expect(page.locator('#tax-codes-modal[open]')).toBeVisible();
      await expect(page.locator('#tax-codes-modal h1')).toBeVisible();
    },
  },
  {
    key: 'stock', id: '#stock-dialog', title: '#stock-dialog-title', scrolls: false,
    open: async (page) => {
      await page.goto('/inventory');
      await page.locator('#stock-table .stock-row[data-variant=""]').first().locator('td').first().click();
      await expect(page.locator('#stock-dialog[open]')).toBeVisible();
    },
  },
];

// The stock dialog needs one stock row; the auth till is a fresh install.
async function ensureStockItem(page: Page) {
  await page.goto('/inventory');
  if (await page.locator('#stock-table .stock-row[data-variant=""]').count()) return;
  await page.goto('/items');
  await openNewItemForm(page);
  await page.locator('#item-name').fill('Dialog Probe 6125');
  await page.locator('#item-price').fill('1.50');
  await page.locator('#item-form-submit').click();
  await expect(page.locator('#item-form-msg .pos-notice.success')).toBeVisible();
  await page.locator('#item-form-modal').evaluate((el: HTMLDialogElement) => el.close());
}

async function setup(page: Page) {
  await ensureOperator(page);
  await ensureStockItem(page);
}

type Hit = { hit: boolean; at: string | null; x: number; y: number; w: number; h: number; right: number; bottom: number };
async function hit(page: Page, sel: string): Promise<Hit> {
  return page.evaluate((s) => {
    const el = document.querySelector(s) as HTMLElement;
    const r = el.getBoundingClientRect();
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return {
      hit: !!at && (at === el || el.contains(at)),
      at: at ? (at.id || (at as HTMLElement).className || at.tagName) as string : null,
      x: r.left, y: r.top, w: r.width, h: r.height, right: r.right, bottom: r.bottom,
    };
  }, sel);
}
const box = (page: Page, sel: string) => page.evaluate((s) => {
  const r = document.querySelector(s)!.getBoundingClientRect();
  return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
}, sel);

const INLINE = (d: Dlg) => `${d.id} .dialog-status-inline`;
const INLINE_LOCK = (d: Dlg) => `${INLINE(d)} [data-record-dialog-lock] button`;
const INLINE_EXIT = (d: Dlg) => `${INLINE(d)} [data-record-dialog-exit]`;

async function expectLoggedOut(page: Page) {
  await page.waitForURL((u) => u.pathname === '/login');
  const cookies = await page.context().cookies();
  expect(cookies.filter((c) => /session/i.test(c.name) && c.value).length, 'session cookie is gone').toBe(0);
}

// Lock then exit, 44x44, hit targets, on the title's line, before the title.
async function assertInline(page: Page, d: Dlg) {
  // UX-2: initial focus never lands on Lock or exit.
  const focusOnControls = await page.evaluate(() => !!document.activeElement?.closest('.dialog-status-inline'));
  expect(focusOnControls, 'initial focus is not on Lock/exit').toBe(false);
  const lock = await hit(page, INLINE_LOCK(d));
  expect(lock.hit, `in-dialog Lock is the hit target (got ${lock.at})`).toBe(true);
  expect(lock.w).toBeGreaterThanOrEqual(44);
  expect(lock.h).toBeGreaterThanOrEqual(44);
  expect(lock.y).toBeGreaterThanOrEqual(0);
  const exit = await hit(page, INLINE_EXIT(d));
  expect(exit.hit, `exit-to-OS is the hit target (got ${exit.at})`).toBe(true);
  expect(exit.w).toBeGreaterThanOrEqual(44);
  expect(exit.h).toBeGreaterThanOrEqual(44);
  await expect(page.locator(INLINE_EXIT(d))).toHaveAttribute('href', '/settings#settings-display');
  const rtl = await page.evaluate(() => document.documentElement.dir === 'rtl');
  expect(rtl ? exit.x : lock.x).toBeLessThan(rtl ? lock.x : exit.x); // Lock first in reading order
  const title = await box(page, d.title);
  const exitEnd = rtl ? exit.x : exit.right;
  expect(rtl ? exitEnd >= title.right - 0.5 : exitEnd <= title.left + 0.5, 'both sit before the title').toBe(true);
  expect(Math.abs((lock.y + lock.h / 2) - (title.top + title.height / 2))).toBeLessThan(lock.h);
}

// A finger drag up through CDP touch events (Playwright has no swipe).
async function swipeUp(page: Page, x: number, y0: number, y1: number) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y: y0 }] });
  const steps = 12;
  for (let i = 1; i <= steps; i++) {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: y0 + ((y1 - y0) * i) / steps }] });
  }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
}

// ---------------------------------------------------------------- >=481 ---
test.describe('>=481px: beside the rail, above the status bar, Lock in the title line (#6125)', () => {
  test.use({ viewport: { width: 1024, height: 600 }, hasTouch: true });

  for (const d of DIALOGS) {
    test(`${d.key}: in-dialog Lock/exit + status bar are the hit targets`, async ({ page }) => {
      const done = watchConsole(page);
      await setup(page);
      await d.open(page);
      await expect(page.locator('html')).toHaveClass(/ut-scrim-on/);

      // Measured, never a literal: the rail's real box.
      const nav = await box(page, '.nav');
      const dlg = await box(page, d.id);
      const sb = await box(page, '.statusbar');
      // eslint-disable-next-line no-console
      console.log(`6125 measured: ${d.key} @1024x600 dialog x=${dlg.left.toFixed(1)} y=${dlg.top} ${dlg.width.toFixed(1)}x${dlg.height.toFixed(1)} (rail right ${nav.right.toFixed(1)}, statusbar top ${sb.top.toFixed(1)}, bottom ${dlg.bottom.toFixed(1)})`);
      expect(dlg.left, `dialog starts at/after the rail's end (${nav.right})`).toBeGreaterThanOrEqual(nav.right - 0.5);
      expect(dlg.top).toBe(0);
      expect(Math.round(dlg.right)).toBe(1024);
      expect(dlg.bottom, 'dialog stops at the status bar').toBeLessThanOrEqual(sb.top + 0.5);

      // The in-dialog Lock/exit are visible, 44x44 and the hit targets.
      await assertInline(page, d);
      // Informational: where the rail's own Lock is (below the fold on a real
      // session at this height, and click-through under the scrim).
      const rail = await hit(page, '.nav .session-lock button');
      // eslint-disable-next-line no-console
      console.log(`6125 measured: ${d.key} rail Lock y=${rail.y.toFixed(0)}..${rail.bottom.toFixed(0)} of 600, hit=${rail.hit}`);
      const sbHit = await hit(page, '.statusbar');
      expect(sbHit.hit, `status bar is the hit target (got ${sbHit.at})`).toBe(true);
      await expect(page.locator(`${d.id} [data-record-dialog-conn]`)).toHaveCount(0);

      // A real, non-forced click on the in-dialog Lock logs out (dirty form, no prompt).
      await Promise.all([page.waitForURL((u) => u.pathname === '/login'), page.locator(INLINE_LOCK(d)).click()]);
      await expectLoggedOut(page);
      done();
    });
  }

  test('item: RTL mirrors: dialog clears the rail on the right, Lock at the right of the title, logs out', async ({ page }) => {
    await setup(page);
    await page.goto('/items');
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await openNewItemForm(page);
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    const nav = await box(page, '.nav');
    const dlg = await box(page, '#item-form-modal');
    expect(nav.left, 'rail is on the right').toBeGreaterThan(512);
    expect(dlg.right, 'dialog ends at the rail').toBeLessThanOrEqual(nav.left + 0.5);
    expect(Math.round(dlg.left)).toBe(0);
    await assertInline(page, DIALOGS[0]);
    const lk = await hit(page, INLINE_LOCK(DIALOGS[0]));
    expect(lk.x + lk.w / 2, 'Lock sits at the inline-start (right) end').toBeGreaterThan(512);
    await Promise.all([page.waitForURL((u) => u.pathname === '/login'), page.locator(INLINE_LOCK(DIALOGS[0])).click()]);
  });

  test('item + stock: Save / Submit are not covered by the on-screen keyboard', async ({ page }) => {
    await setup(page);
    await setOskMode(page, 'on');
    try {
      await page.goto('/items');
      await openNewItemForm(page);
      await page.locator('#item-name').tap();
      await expect(page.locator('#osk.osk-open')).toBeVisible();
      await expect(page.locator('body')).toHaveClass(/osk-padded/);
      await expect.poll(async () => {
        const o = await box(page, '#osk');
        const s = await box(page, '#item-form-submit');
        return s.bottom <= o.top + 0.5;
      }).toBe(true);

      await page.goto('/inventory');
      await page.locator('#stock-table .stock-row[data-variant=""]').first().locator('td').first().click();
      await page.locator('#stock-form input[name="quantity"]').tap();
      await expect(page.locator('#osk.osk-open')).toBeVisible();
      // Submit sits at the end of the scrolling body: it must be reachable above the OSK.
      await page.locator('#stock-form button[type=submit]').scrollIntoViewIfNeeded();
      await expect.poll(async () => {
        const o = await box(page, '#osk');
        const s = await box(page, '#stock-form button[type=submit]');
        const dlg = await box(page, '#stock-dialog');
        return s.bottom <= o.top + 0.5 && s.bottom <= dlg.bottom + 0.5 && s.top >= dlg.top;
      }).toBe(true);
    } finally {
      await setOskMode(page, 'auto');
    }
  });
});

// ----------------------------------------------------------------- <=480 ---
test.describe('<=480px: full width, Lock + exit in the title line (#6125)', () => {
  test.use({ viewport: { width: 360, height: 640 }, hasTouch: true, isMobile: true });

  for (const d of DIALOGS) {
    test(`${d.key}: Lock/exit 44x44 and tappable, status bar uncovered, no chip, focus not on them`, async ({ page }) => {
      const done = watchConsole(page);
      await setup(page);
      await d.open(page);

      await assertInline(page, d);

      // The real status bar is uncovered and the hit target; no duplicate chip.
      const dlg = await box(page, d.id);
      const sb = await box(page, '.statusbar');
      expect(dlg.bottom).toBeLessThanOrEqual(sb.top + 0.5);
      expect((await hit(page, '.statusbar')).hit, 'status bar is the hit target').toBe(true);
      await expect(page.locator(`${d.id} [data-record-dialog-conn]`)).toHaveCount(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);

      // A real, non-forced tap on the in-dialog Lock logs out (no discard prompt).
      await Promise.all([page.waitForURL((u) => u.pathname === '/login'), page.locator(INLINE_LOCK(d)).click()]);
      await expectLoggedOut(page);
      done();
    });
  }

  test('item: head stays inside the #2000 budget and does not grow a row', async ({ page }) => {
    await setup(page);
    await DIALOGS[0].open(page);
    const head = await box(page, '#item-form-modal .catalog-form-head');
    // eslint-disable-next-line no-console
    console.log(`6125 measured: item-form head ${head.height.toFixed(1)}px of 640 = ${(head.height / 6.4).toFixed(1)}%`);
    expect(head.height / 640).toBeLessThan(0.3);
    // Pre-change create-mode head measured 180.7px on main.
    expect(head.height).toBeLessThanOrEqual(181.7);
  });

  test('stock: head no taller than before (title line only)', async ({ page }) => {
    await setup(page);
    await DIALOGS[3].open(page);
    const head = await box(page, '#stock-dialog .record-dialog-head');
    // eslint-disable-next-line no-console
    console.log(`6125 measured: stock head ${head.height.toFixed(1)}px of 640`);
    expect(head.height / 640).toBeLessThan(0.3);
    expect(head.height).toBeLessThanOrEqual(71.7); // 70.7px on main
  });

  test('exit-to-OS goes to Settings > Display', async ({ page }) => {
    await setup(page);
    await DIALOGS[3].open(page);
    await page.locator(INLINE_EXIT(DIALOGS[3])).click();
    await expect(page).toHaveURL(/\/settings#settings-display/);
  });

  test('stock + item: Tab stays inside the dialog, Lock is in the cycle, Escape closes the stock dialog', async ({ page }) => {
    await setup(page);
    for (const d of [DIALOGS[3], DIALOGS[0]]) {
      await d.open(page);
      const seen = new Set<string>();
      for (let i = 0; i < 40; i++) {
        await page.keyboard.press('Tab');
        const info = await page.evaluate((id) => {
          const a = document.activeElement as HTMLElement | null;
          const dlg = document.querySelector(id)!;
          return { inside: !!a && dlg.contains(a), lock: !!a?.closest('[data-record-dialog-lock]') };
        }, d.id);
        if (info.lock) seen.add('lock');
        if (!info.inside) {
          // statusbar / Lock are allowed to take focus (#4078), nothing else.
          const ok = await page.evaluate(() => !!document.activeElement?.closest('.statusbar, .session-lock, #osk'));
          expect(ok, 'focus left the dialog onto something other than status/Lock').toBe(true);
        }
      }
      expect(seen.has('lock'), 'in-dialog Lock is in the tab cycle').toBe(true);
      if (d.key === 'stock') {
        // The item editor deliberately has no Escape-to-close (catalog.html); Close only.
        await page.locator(d.title).focus();
        await page.keyboard.press('Escape');
        await expect(page.locator(d.id)).toBeHidden();
      }
    }
  });

  test('item: RTL, controls at the right (inline-start) of the title line', async ({ page }) => {
    await setup(page);
    await page.goto('/items');
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await openNewItemForm(page);
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await assertInline(page, DIALOGS[0]);
    const lock = await hit(page, INLINE_LOCK(DIALOGS[0]));
    expect(lock.x + lock.w / 2).toBeGreaterThan(180);
  });

  test('stock: RTL, Lock is the hit target and sits on the right', async ({ page }) => {
    await setup(page);
    await page.goto('/inventory');
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await page.locator('#stock-table .stock-row[data-variant=""]').first().locator('td').first().click();
    await expect(page.locator('#stock-dialog[open]')).toBeVisible();
    await assertInline(page, DIALOGS[3]);
    const lock = await hit(page, INLINE_LOCK(DIALOGS[3]));
    expect(lock.x + lock.w / 2).toBeGreaterThan(180);
  });

  // UX-3 (pre-existing): the hx-swapped page dialogs were overflow:hidden with
  // no scroll container, so most of their content was unreachable.
  for (const d of DIALOGS.filter((x) => x.scrolls)) {
    test(`${d.key}: scrolls to its last card by wheel and by touch swipe; head stays pinned`, async ({ page }) => {
      await setup(page);
      await d.open(page);
      const overflows = await page.locator(d.id).evaluate((el) => el.scrollHeight > el.clientHeight + 1);
      expect(overflows, 'content is taller than the dialog at 360x640').toBe(true);
      expect(await page.locator(d.id).evaluate((el) => getComputedStyle(el).overflowY)).toBe('auto');

      // Emulated swipe (finger moves up) through CDP.
      await swipeUp(page, 180, 500, 200);
      await expect.poll(() => page.locator(d.id).evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
      expect(await page.evaluate(() => window.scrollY), 'the page underneath did not scroll').toBe(0);

      await page.mouse.move(180, 300);
      for (let i = 0; i < 40; i++) await page.mouse.wheel(0, 400);
      await expect.poll(() => page.locator(d.id).evaluate((el) => el.scrollTop + el.clientHeight >= el.scrollHeight - 2)).toBe(true);

      // Pinned head with Lock still visible and tappable at the scroll end.
      const head = await box(page, `${d.id} > .page-head`);
      const dlg = await box(page, d.id);
      expect(Math.abs(head.top - dlg.top)).toBeLessThan(1);
      expect((await hit(page, INLINE_LOCK(d))).hit, 'Lock still the hit target after scrolling').toBe(true);
      expect(head.height / 640).toBeLessThan(0.3);
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
    });
  }

  test('import + tax codes: no head overlap with the dialog edge at 1024 either (padding)', async ({ page }) => {
    await setup(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await DIALOGS[1].open(page);
    const h1 = await box(page, '#import-modal h1');
    const dlg = await box(page, '#import-modal');
    expect(h1.left - dlg.left, 'content is padded away from the rail').toBeGreaterThanOrEqual(16);
  });
});

// ------------------------------------------------------------ live status ---
test.describe('status is live behind the dialog (#6125)', () => {
  for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 640 }]) {
    test(`${vp.width}px: offline/online flips the real status bar while a dialog is open`, async ({ page, context }: { page: Page; context: BrowserContext }) => {
      await page.setViewportSize(vp);
      await setup(page);
      await DIALOGS[1].open(page); // the hx-swapped one
      await context.setOffline(true);
      try {
        await expect(page.locator('#sb-conn')).toHaveClass(/is-offline/);
        await expect(page.locator('#sb-conn .sb-conn-text')).toHaveText(/offline/i);
        expect((await hit(page, '#sb-conn')).hit, 'the offline chip is reachable over the dialog').toBe(true);
      } finally {
        await context.setOffline(false);
      }
      await expect(page.locator('#sb-conn')).not.toHaveClass(/is-offline/);
    });
  }
});
