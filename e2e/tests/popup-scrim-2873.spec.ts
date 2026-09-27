import { test, expect, type Page } from './fixtures';
import { setOskMode, watchConsole } from './helpers';

// ut-docs#2873: every till popup gets a full-screen dimmed backdrop that
// blocks taps behind it. A showModal() dialog paints its native ::backdrop
// in var(--ut-scrim); a NON-modal popup (the .show() family kept non-modal
// for the on-screen keyboard, ut-docs#1385) gets the ONE shared #ut-scrim
// from base.html behind it (z 450: over the page and the nav rail, under
// every popup, #osk and the lifted status bar). Default tap rule: a tap on
// the scrim reaches nothing; data-ut-scrim-dismiss opts a pure picker in to
// "tap outside closes"; data-ut-no-scrim opts a docked panel out.

const SHOTS = process.env.UT_SCRIM_SHOTS || '';

// Dialogs only ever opened with showModal() (native ::backdrop, top layer):
// the sweep below forces .show() on every OTHER dialog a page ships.
const SHOWMODAL_ONLY = ['modifier-modal', 'order-type-prompt-modal', 'barcode-backfill-modal', 'table-modal', 'table-qr-modal', 'selforder-modal'];

async function openSell(page: Page, path = '/') {
  await page.goto(path);
  await page.waitForSelector('.pos-container');
}

// The centre of the first visible product tile that the given popup box
// does not cover -- the thing a cashier's stray tap would hit.
async function tileOutside(page: Page, popupSel: string) {
  return page.evaluate((sel) => {
    const pop = document.querySelector(sel)!.getBoundingClientRect();
    for (const t of Array.from(document.querySelectorAll('.products-tab-panel .btn-tile'))) {
      const r = t.getBoundingClientRect();
      if (!r.width || !r.height) continue;
      const x = r.left + r.width / 2, y = r.top + r.height / 2;
      if (y > window.innerHeight - 40 || y < 0 || x < 0 || x > window.innerWidth) continue;
      if (x >= pop.left && x <= pop.right && y >= pop.top && y <= pop.bottom) continue;
      return { x, y, name: (t as HTMLElement).dataset.name || t.textContent!.trim() };
    }
    return null;
  }, popupSel);
}

const hitId = (page: Page, x: number, y: number) =>
  page.evaluate(([px, py]) => {
    const el = document.elementFromPoint(px, py);
    return el ? (el.id || el.className || el.tagName) : null;
  }, [x, y]);

const scrimOn = (page: Page) =>
  page.evaluate(() => {
    const s = document.getElementById('ut-scrim');
    if (!s || s.hidden || getComputedStyle(s).display === 'none' || !document.documentElement.classList.contains('ut-scrim-on')) return false;
    // It must really cover the viewport (not an unstyled 0x0 div).
    const r = s.getBoundingClientRect();
    return r.left <= 0 && r.top <= 0 && r.width >= window.innerWidth && r.height >= window.innerHeight;
  });

test.describe('ut-docs#2873 popup scrim', () => {
  test.beforeEach(async ({ page }) => { await page.request.post('/api/pos/reset'); });
  test.afterEach(async ({ page }) => { await page.request.post('/api/pos/reset'); });

  test('a non-modal popup on Sell puts the scrim between it and the sale screen', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1024, height: 600 });
    await openSell(page);
    expect(await scrimOn(page)).toBe(false);

    await page.getByTestId('tender-footer-hold').click();
    const modal = page.locator('#hold-modal');
    await expect(modal).toBeVisible();
    expect(await scrimOn(page)).toBe(true);

    // A background product tile is covered by the scrim ...
    const tile = await tileOutside(page, '#hold-modal');
    expect(tile, 'a product tile outside the popup').not.toBeNull();
    expect(await hitId(page, tile!.x, tile!.y)).toBe('ut-scrim');
    // ... and a REAL click there adds nothing and leaves the popup open.
    await page.mouse.click(tile!.x, tile!.y);
    await page.waitForTimeout(400);
    await expect(page.locator('#basket')).not.toContainText(tile!.name);
    await expect(page.getByTestId('payment-open')).toBeDisabled(); // still an empty basket
    await expect(modal).toBeVisible();
    // The popup itself stays on top of the scrim (its own centre hits it).
    const inPopup = await page.evaluate(() => {
      const m = document.getElementById('hold-modal')!;
      const r = m.getBoundingClientRect();
      return m.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2));
    });
    expect(inPopup).toBe(true);
    // The nav rail is behind the scrim too.
    const rail = await page.evaluate(() => {
      const r = document.querySelector('.nav')!.getBoundingClientRect();
      return document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.id;
    });
    expect(rail).toBe('ut-scrim');
    // ... except Lock (ut-docs#1999): the rail's Lock button is still the
    // hit target at its own centre, above the scrim. The e2e till runs
    // without a PIN session, so session_chip.html renders no Lock here:
    // add its exact markup (form.session-lock > button.btn-lock) to the rail.
    const lock = await page.evaluate(() => {
      let b = document.querySelector('.nav .btn-lock');
      if (!b) {
        const f = document.createElement('form');
        f.className = 'session-lock';
        f.innerHTML = '<button type="button" class="nav-toggle btn-lock">L</button>';
        document.querySelector('.nav')!.appendChild(f);
        b = f.querySelector('.btn-lock');
      }
      b!.scrollIntoView({ block: 'nearest' });
      const r = b!.getBoundingClientRect();
      const el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!el && !!el.closest('.btn-lock');
    });
    expect(lock).toBe(true);

    // Status stays reachable (ut-docs#1999): the status bar is lifted above
    // the scrim, still under the popup.
    const sb = await page.evaluate(() => {
      const c = document.getElementById('sb-conn')!.getBoundingClientRect();
      const el = document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2);
      return !!el && !!el.closest('.statusbar');
    });
    expect(sb).toBe(true);

    // Cancel closes it and the scrim goes with it.
    await modal.locator('button', { hasText: 'Cancel' }).click();
    await expect(modal).toBeHidden();
    await expect.poll(() => scrimOn(page)).toBe(false);
    expect(await hitId(page, tile!.x, tile!.y)).not.toBe('ut-scrim');
    assertClean();
  });

  test('the scrim never sticks: open=false, removeAttribute, a swap that removes the popup', async ({ page }) => {
    await openSell(page);
    for (const how of ['close', 'open=false', 'removeAttribute', 'remove', 'innerHTML']) {
      await page.evaluate(() => (document.getElementById('parked-orders-modal') as HTMLDialogElement).show());
      await expect.poll(() => scrimOn(page), how).toBe(true);
      await page.evaluate((h) => {
        const d = document.getElementById('parked-orders-modal') as HTMLDialogElement;
        if (h === 'close') d.close();
        else if (h === 'open=false') d.open = false;
        else if (h === 'removeAttribute') d.removeAttribute('open');
        else if (h === 'remove') { const c = d.cloneNode(false) as HTMLElement; c.removeAttribute('open'); d.replaceWith(c); }
        else { const p = d.parentElement!; const html = d.outerHTML.replace(/\sopen(="")?/, ''); d.remove(); p.insertAdjacentHTML('beforeend', html); }
      }, how);
      await expect.poll(() => scrimOn(page), how).toBe(false);
    }
    // A popup that opens via setAttribute('open') (no .show()) is caught by
    // the MutationObserver backstop.
    await page.evaluate(() => document.getElementById('parked-orders-modal')!.setAttribute('open', ''));
    await expect.poll(() => scrimOn(page)).toBe(true);
    await page.evaluate(() => (document.getElementById('parked-orders-modal') as HTMLDialogElement).close());
    await expect.poll(() => scrimOn(page)).toBe(false);
    // Two popups: the scrim stays until the LAST one closes.
    await page.getByTestId('tender-footer-hold').click();
    await page.evaluate(() => (document.getElementById('parked-orders-modal') as HTMLDialogElement).show());
    await page.evaluate(() => (document.getElementById('parked-orders-modal') as HTMLDialogElement).close());
    await page.waitForTimeout(100);
    expect(await scrimOn(page)).toBe(true);
    await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).close());
    await expect.poll(() => scrimOn(page)).toBe(false);
  });

  test('opt-outs and opt-ins: the payment panel keeps no scrim; a scrim tap only closes an opted-in picker', async ({ page }) => {
    await openSell(page);
    // data-ut-no-scrim: the payment panel docked beside the basket.
    await page.evaluate(() => (document.getElementById('payment-overlay') as HTMLDialogElement).show());
    await page.waitForTimeout(100);
    expect(await scrimOn(page)).toBe(false);
    // ... but a popup opened FROM it (Hold) blocks the panel too: it drops
    // under the scrim while that popup is up (review finding, ut-docs#2873).
    await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).show());
    await expect.poll(() => scrimOn(page)).toBe(true);
    const panelHit = await page.evaluate(() => {
      const p = document.getElementById('payment-overlay')!;
      const h = document.getElementById('hold-modal')!.getBoundingClientRect();
      const r = p.getBoundingClientRect();
      // a point in the panel outside the hold popup's box
      const top = Math.max(r.top, 0), bottom = Math.min(r.bottom, window.innerHeight);
      const left = Math.max(r.left, 0), right = Math.min(r.right, window.innerWidth);
      // from the middle up: the lifted status bar sits along the bottom
      for (let y = (top + bottom) / 2; y > top; y -= 20) {
        for (let x = left + 5; x < right; x += 20) {
          if (x < h.left || x > h.right || y < h.top || y > h.bottom) return document.elementFromPoint(x, y)?.id || '(none)';
        }
      }
      return 'no-point';
    });
    expect(panelHit).toBe('ut-scrim');
    await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).close());
    await expect.poll(() => scrimOn(page)).toBe(false);
    await page.evaluate(() => (document.getElementById('payment-overlay') as HTMLDialogElement).close());

    // Default: a tap on the scrim does NOT close the (hold) popup.
    await page.getByTestId('tender-footer-hold').click();
    await page.mouse.click(5, 300);
    await expect(page.locator('#hold-modal')).toBeVisible();
    await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).close());

    // Opt-in: a picker marked data-ut-scrim-dismiss closes on a scrim tap.
    await page.evaluate(() => {
      const d = document.createElement('dialog');
      d.id = 'scrim-probe'; d.setAttribute('data-ut-scrim-dismiss', '');
      d.style.cssText = 'position:fixed;inset-block-start:10px;inset-inline:0;margin-inline:auto;z-index:500;inline-size:10rem';
      d.textContent = 'probe';
      document.body.appendChild(d);
      d.show();
    });
    expect(await scrimOn(page)).toBe(true);
    await page.mouse.click(5, 300);
    await expect(page.locator('#scrim-probe')).toBeHidden();
    await expect.poll(() => scrimOn(page)).toBe(false);
  });

  test('a showModal() dialog paints the same token on its ::backdrop; opt-in closes on a backdrop tap', async ({ page }) => {
    await openSell(page);
    const r = await page.evaluate(() => {
      const probe = document.createElement('div');
      probe.style.background = 'var(--ut-scrim)';
      document.body.appendChild(probe);
      const token = getComputedStyle(probe).backgroundColor;
      probe.remove();
      const d = document.createElement('dialog');
      d.id = 'modal-probe';
      d.textContent = 'probe';
      document.body.appendChild(d);
      d.showModal();
      const plain = getComputedStyle(d, '::backdrop').backgroundColor;
      const mm = document.getElementById('modifier-modal') as HTMLDialogElement;
      return { token, plain, mm: getComputedStyle(mm, '::backdrop').backgroundColor };
    });
    expect(r.token).toBe('rgba(15, 23, 42, 0.45)');
    expect(r.plain).toBe(r.token);
    expect(r.mm).toBe(r.token);
    // No opt-in: a backdrop tap does nothing, and the shared div is not used.
    expect(await scrimOn(page)).toBe(false);
    await page.mouse.click(5, 5);
    await expect(page.locator('#modal-probe')).toBeVisible();
    // Opt-in: a backdrop tap closes it; a tap inside does not.
    await page.evaluate(() => document.getElementById('modal-probe')!.setAttribute('data-ut-scrim-dismiss', ''));
    await page.locator('#modal-probe').click();
    await expect(page.locator('#modal-probe')).toBeVisible();
    await page.mouse.click(5, 5);
    await expect(page.locator('#modal-probe')).toBeHidden();

    // Dark theme retunes the token (web/public/themes/dark.css).
    await page.evaluate(async () => {
      const link = document.getElementById('theme-css') as HTMLLinkElement;
      await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
    });
    const dark = await page.evaluate(() => {
      const d = document.getElementById('modal-probe') as HTMLDialogElement;
      d.showModal();
      return getComputedStyle(d, '::backdrop').backgroundColor;
    });
    expect(dark).toBe('rgba(0, 0, 0, 0.6)');
  });

  test('every non-modal popup on Sell paints above the scrim', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await openSell(page);
    // A line with a value, for the void/comp/waste sheet.
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    const check = async (open: () => Promise<void>, sel: string) => {
      await open();
      await expect(page.locator(sel)).toBeVisible();
      expect(await scrimOn(page), sel).toBe(true);
      const onTop = await page.evaluate((s) => {
        const m = document.querySelector(s)!;
        const r = m.getBoundingClientRect();
        return m.contains(document.elementFromPoint(r.left + r.width / 2, r.top + Math.min(r.height / 2, 30)));
      }, sel);
      expect(onTop, `${sel} paints above the scrim`).toBe(true);
      await page.evaluate((s) => (document.querySelector(s) as HTMLDialogElement).close(), sel);
      await expect.poll(() => scrimOn(page), sel).toBe(false);
    };
    await check(() => page.getByTestId('tender-footer-hold').click(), '#hold-modal');
    await check(() => page.getByTestId('parked-orders-open').click(), '#parked-orders-modal');
    await check(() => page.locator('#basket .shrinkage-remove-toggle').first().click(), '#basket .shrinkage-sheet');
    await check(async () => { await page.evaluate(() => (document.getElementById('category-items-modal') as HTMLDialogElement).show()); }, '#category-items-modal');
    await check(async () => { await page.evaluate(() => (document.getElementById('category-overflow-dialog') as HTMLDialogElement).show()); }, '#category-overflow-dialog');
  });

  // The inventory sweep: every <dialog> each till page ships, opened the
  // non-modal way, must paint above the scrim (a popup nested in a pane
  // that forms its own low stacking context would not) -- and raise it,
  // unless it opted out.
  test('sweep: every non-modal dialog on every page paints above the scrim', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    const pages = ['/', '/menu', '/catalog', '/inventory', '/tables', '/categories', '/locations', '/registers', '/country-settings', '/fiscal-register', '/settings', '/items', '/admin'];
    const seen: string[] = [];
    for (const path of pages) {
      await page.goto(path);
      await page.waitForLoadState('load');
      const res = await page.evaluate((modalOnly) => {
        const out: { id: string; onTop: boolean; scrim: boolean; noScrim: boolean }[] = [];
        for (const d of Array.from(document.querySelectorAll('dialog')) as HTMLDialogElement[]) {
          if (d.open || modalOnly.includes(d.id)) continue;
          if (!d.childElementCount) d.innerHTML = '<p>probe</p>';
          d.show();
          const r = d.getBoundingClientRect();
          const hit = document.elementFromPoint(r.left + Math.min(r.width / 2, 40), r.top + Math.min(r.height / 2, 20));
          const s = document.getElementById('ut-scrim')!;
          out.push({ id: d.id || d.className, onTop: d.contains(hit), scrim: !s.hidden, noScrim: d.hasAttribute('data-ut-no-scrim') });
          d.close();
        }
        return out;
      }, SHOWMODAL_ONLY);
      for (const r of res) {
        seen.push(`${path} ${r.id}`);
        expect(r.onTop, `${path} ${r.id} paints above the scrim`).toBe(true);
        expect(r.scrim, `${path} ${r.id} raises the scrim`).toBe(!r.noScrim);
      }
      await expect.poll(() => scrimOn(page), path).toBe(false);
    }
    expect(seen.length).toBeGreaterThan(10);
    console.log('[scrim sweep]\n' + seen.join('\n'));
  });

  test('the on-screen keyboard stays above the scrim and types into the popup', async ({ page }) => {
    await setOskMode(page, 'on');
    try {
      await openSell(page);
      await page.getByTestId('tender-footer-hold').click();
      await page.locator('#hold-label-input').click();
      const osk = page.locator('#osk.osk-open');
      await expect(osk).toBeVisible();
      expect(await scrimOn(page)).toBe(true);
      const key = page.locator('#osk button[data-k="a"]');
      const box = (await key.boundingBox())!;
      const inOsk = await page.evaluate(([x, y]) => !!document.elementFromPoint(x, y)?.closest('#osk'), [box.x + box.width / 2, box.y + box.height / 2]);
      expect(inOsk).toBe(true);
      await key.click();
      await expect(page.locator('#hold-label-input')).toHaveValue(/a/i);
      await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).close());
    } finally {
      await setOskMode(page, 'auto');
    }
  });

  test('category filter popover: a scrim tap closes it and resets the trigger', async ({ page }) => {
    await page.goto('/catalog');
    const trigger = page.locator('[data-category-filter-trigger]').first();
    await expect(trigger).toBeVisible();
    await trigger.click();
    const dlg = page.locator('[data-category-filter-dialog]').first();
    await expect(dlg).toBeVisible();
    expect(await scrimOn(page)).toBe(true);
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await page.mouse.click(5, 590);
    await expect(dlg).toBeHidden();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');
    // Focus goes back to the trigger, as the close button / Escape do.
    await expect(trigger).toBeFocused();
    await expect.poll(() => scrimOn(page)).toBe(false);
  });

  // Layout at the two till sizes, LTR and RTL, light and dark: the popup is
  // fully hit-testable (nothing, the lifted status bar included, paints over
  // it), the status bar stays reachable. Screenshots when UT_SCRIM_SHOTS is set.
  for (const vp of [{ w: 1024, h: 600 }, { w: 360, h: 800 }]) {
    for (const lang of ['en', 'fa']) {
      for (const theme of ['light', 'dark']) {
        test(`layout ${vp.w}x${vp.h} ${lang} ${theme}`, async ({ page }) => {
          await page.setViewportSize({ width: vp.w, height: vp.h });
          await openSell(page, `/?lang=${lang}`);
          if (theme === 'dark') {
            await page.evaluate(async () => {
              const link = document.getElementById('theme-css') as HTMLLinkElement;
              await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
            });
          }
          await page.getByTestId('tender-footer-hold').click();
          await expect(page.locator('#hold-modal')).toBeVisible();
          expect(await scrimOn(page)).toBe(true);
          const geo = await page.evaluate(() => {
            const m = document.getElementById('hold-modal')!;
            const r = m.getBoundingClientRect();
            const pts = [[r.left + 4, r.top + 4], [r.right - 4, r.top + 4], [r.left + 4, r.bottom - 4], [r.right - 4, r.bottom - 4], [r.left + r.width / 2, r.top + r.height / 2]];
            const covered = pts.filter(([x, y]) => y < window.innerHeight && !m.contains(document.elementFromPoint(x, y)));
            const c = document.getElementById('sb-conn')!.getBoundingClientRect();
            const sbEl = document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2);
            return { covered, sb: !!sbEl && !!sbEl.closest('.statusbar'), dir: document.documentElement.dir };
          });
          expect(geo.covered, 'no corner of the popup is painted over').toEqual([]);
          expect(geo.sb, 'status bar reachable').toBe(true);
          expect(geo.dir).toBe(lang === 'fa' ? 'rtl' : 'ltr');
          if (SHOTS) await page.screenshot({ path: `${SHOTS}/scrim-${vp.w}x${vp.h}-${lang}-${theme}.png` });
          await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).close());
        });
      }
    }
  }
});
