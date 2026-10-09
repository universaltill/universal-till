import { test, expect, type Page } from './fixtures';
import { watchConsole, setOrderTypePromptMode, deactivateAllTables, createTable } from './helpers';

// ut-docs#2097 (product owner, 2026-09-28): a showModal() dialog makes the
// rest of the page inert -- including the persistent status bar and the
// rail's Lock (CLAUDE.md "Offline-first": status, lock and exit-to-OS stay
// reachable on every surface; ut-docs#1999). "Unclickable counts as
// blocked, no exception for small dialogs." So every till dialog that used
// to open with showModal() now opens with .show() in an explicit fixed
// frame, over the shared #ut-scrim (ut-docs#2873) that keeps the status bar
// and Lock above it. Only the self-order kiosk (#selforder-modal) stays
// showModal(): in kiosk mode those affordances must be unreachable.
//
// For each of the five converted dialogs, opened the real way:
//  (a) it is not :modal (no top layer, nothing made inert);
//  (b) the status bar and the rail's Lock are the hit-test target at their
//      own centre, and a real click there reaches them;
//  (c) Escape still closes it (base.html's shared handler for opted-in
//      non-modal dialogs -- native Escape only exists for modal ones);
//  (d) a tap beside it lands on the scrim, never on the page underneath
//      (#table-qr-modal opted in to closing on that tap, ut-docs#2873).
// Exit-to-OS lives in Settings on a non-kiosk till (no rail/status-bar
// control exists to hit-test), so (b) covers the status bar and Lock.

const RUN = Date.now().toString(36).toUpperCase();

// The e2e till runs without a PIN session, so session_chip.html renders no
// Lock: add its exact markup (form.session-lock > button.btn-lock) to the
// rail, same as popup-scrim-2873.spec.ts. Its click is recorded.
async function injectLock(page: Page) {
  await page.evaluate(() => {
    let b = document.querySelector('.nav .btn-lock') as HTMLElement | null;
    if (!b) {
      const f = document.createElement('form');
      f.className = 'session-lock';
      f.innerHTML = '<button type="button" class="nav-toggle btn-lock">L</button>';
      document.querySelector('.nav')!.appendChild(f);
      b = f.querySelector('.btn-lock');
    }
    const w = window as any;
    w.__lockClicks = 0;
    w.__sbClicks = 0;
    if (w.__clickProbes) return; // listeners once per document
    w.__clickProbes = true;
    b!.addEventListener('click', () => { w.__lockClicks++; });
    document.querySelector('.statusbar')!.addEventListener('click', () => { w.__sbClicks++; });
  });
}

const scrimOn = (page: Page) =>
  page.evaluate(() => {
    const s = document.getElementById('ut-scrim');
    return !!s && !s.hidden && getComputedStyle(s).display !== 'none' && document.documentElement.classList.contains('ut-scrim-on');
  });

// Centre of el (scrolled into view) and whether el is the hit target there.
async function hitCentre(page: Page, sel: string) {
  return page.evaluate((s) => {
    const el = document.querySelector(s) as HTMLElement;
    el.scrollIntoView({ block: 'nearest' });
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    const at = document.elementFromPoint(x, y);
    return { x, y, hit: !!at && (at === el || el.contains(at)), at: at ? (at.id || at.className || at.tagName) : null };
  }, sel);
}

// A point beside the dialog: outside its box, the rail and the status bar.
async function pointBeside(page: Page, sel: string) {
  return page.evaluate((s) => {
    const d = document.querySelector(s)!.getBoundingClientRect();
    const nav = document.querySelector('.nav')?.getBoundingClientRect();
    const sb = document.querySelector('.statusbar')?.getBoundingClientRect();
    const inside = (r: DOMRect | undefined, x: number, y: number) => !!r && x >= r.left - 4 && x <= r.right + 4 && y >= r.top - 4 && y <= r.bottom + 4;
    for (let y = 40; y < window.innerHeight - 40; y += 20) {
      for (let x = 40; x < window.innerWidth - 20; x += 20) {
        if (!inside(d, x, y) && !inside(nav, x, y) && !inside(sb, x, y)) return { x, y };
      }
    }
    return null;
  }, sel);
}

async function assertNonBlocking(page: Page, sel: string, opts: { scrimCloses?: boolean; reopen?: () => Promise<void> } = {}) {
  const dlg = page.locator(sel);
  await expect(dlg).toBeVisible();
  // (a) not a top-layer modal.
  expect(await dlg.evaluate((d) => d.matches(':modal')), `${sel} must not be :modal`).toBe(false);
  // Focus moved into the dialog on open, as showModal() did.
  expect(await dlg.evaluate((d) => !!document.activeElement && d.contains(document.activeElement)), `${sel} holds focus`).toBe(true);
  // The shared scrim is up BEHIND it: the dialog itself paints above it
  // (a fixed dialog trapped in a low stacking context would not).
  await expect.poll(() => scrimOn(page), `${sel} raises #ut-scrim`).toBe(true);
  const onTop = await dlg.evaluate((d) => {
    const r = d.getBoundingClientRect();
    return d.contains(document.elementFromPoint(r.left + r.width / 2, r.top + Math.min(r.height / 2, 30)));
  });
  expect(onTop, `${sel} paints above the scrim`).toBe(true);

  // (b) status bar and Lock: hit-testable and a real click reaches them.
  await injectLock(page);
  const lock = await hitCentre(page, '.nav .btn-lock');
  expect(lock.hit, `Lock is the hit target while ${sel} is open (got ${lock.at})`).toBe(true);
  await page.mouse.click(lock.x, lock.y);
  expect(await page.evaluate(() => (window as any).__lockClicks || 0), `a real click on Lock reaches it over ${sel}`).toBe(1);
  const sb = await hitCentre(page, '#sb-conn');
  expect(sb.hit, `status bar is the hit target while ${sel} is open (got ${sb.at})`).toBe(true);
  await page.mouse.click(sb.x, sb.y);
  expect(await page.evaluate(() => (window as any).__sbClicks || 0), `a real click on the status bar reaches it over ${sel}`).toBe(1);
  await expect(dlg).toBeVisible();

  // (d) a tap beside it lands on the scrim, not the page underneath.
  const p = await pointBeside(page, sel);
  expect(p, `a point beside ${sel}`).not.toBeNull();
  const beside = await page.evaluate(([x, y]) => document.elementFromPoint(x, y)?.id || null, [p!.x, p!.y]);
  expect(beside, `a tap beside ${sel} hits the scrim`).toBe('ut-scrim');
  await page.evaluate(() => {
    (window as any).__tapTargets = [];
    document.addEventListener('click', (e) => { (window as any).__tapTargets.push((e.target as Element).id || (e.target as Element).tagName); }, { capture: true, once: true });
  });
  await page.mouse.click(p!.x, p!.y);
  expect(await page.evaluate(() => (window as any).__tapTargets)).toEqual(['ut-scrim']);
  if (opts.scrimCloses) {
    await expect(dlg).toBeHidden();
    await opts.reopen!();
    await expect(dlg).toBeVisible();
  } else {
    await page.waitForTimeout(200);
    await expect(dlg).toBeVisible();
  }

  // (e) review finding: the keyboard can't reach the page behind either --
  // Tab / Shift+Tab wrap inside the dialog (showModal() made the page
  // inert; the scrim only blocks the pointer).
  await page.evaluate((s) => {
    const d = document.querySelector(s) as HTMLElement;
    const f = d.querySelector('a[href], button, input, select, textarea, [tabindex]') as HTMLElement | null;
    (f || d).focus();
  }, sel);
  for (const key of ['Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Shift+Tab', 'Shift+Tab', 'Shift+Tab', 'Shift+Tab']) {
    await page.keyboard.press(key);
    const inside = await page.evaluate((s) => !!document.querySelector(s)?.contains(document.activeElement), sel);
    expect(inside, `${key} keeps focus inside ${sel}`).toBe(true);
  }

  // (c) Escape closes it, and the scrim goes with it.
  await page.keyboard.press('Escape');
  await expect(dlg).toBeHidden();
  await expect.poll(() => scrimOn(page), `${sel} scrim gone after Escape`).toBe(false);
}

type Seeded = { itemId: string; name: string; barcode: string; optionName: string };
let seeded: Seeded | null = null;

// One item with one real modifier group/option as a sell-screen shortcut
// (trimmed from popup-zoom-2944.spec.ts / order-type-prompt-placement-2282).
// A fresh item per test: afterEach deactivates the previous one, and a
// re-import of the same SKU would land on that inactive row.
let seq = 0;
async function seedModifierItem(page: Page): Promise<Seeded> {
  const tag = `${RUN}${++seq}`;
  const name = `Sm2097 Mod ${tag}`;
  const barcode = `SM2097BC-${tag}`;
  const csv = `Name,SKU,Barcode,Price,Category,In stock\n${name},SM2097${tag},${barcode},1.00,Sm2097 Cat,1\n`;
  await page.goto('/import');
  await page.setInputFiles('input[type=file]', { name: `import-sm2097-${tag}.csv`, mimeType: 'text/csv', buffer: Buffer.from(csv) });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/import')),
    page.getByRole('button', { name: /Import/i }).last().click(),
  ]);
  await page.goto('/catalog');
  const itemId = (await page.locator(`.catalog-row[data-name="${name}"]`).first().getAttribute('data-id'))!;
  expect((await page.request.post('/api/buttons/add', { form: { itemId, label: name, code: barcode } })).ok()).toBe(true);
  const groupName = `Size ${tag}`;
  expect((await page.request.post('/api/catalog/modifier-group', { form: { itemId, name: groupName, minSelect: '0', maxSelect: '1' } })).ok()).toBe(true);
  const html = await (await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`)).text();
  const at = html.indexOf(`>${groupName}<`);
  expect(at).toBeGreaterThan(-1);
  const ids = [...html.slice(0, at).matchAll(/data-group-id="([^"]*)"/g)];
  const groupId = ids[ids.length - 1][1];
  const optionName = `Large ${tag}`;
  expect((await page.request.post('/api/catalog/modifier-option', { form: { groupId, itemId, name: optionName } })).ok()).toBe(true);
  return { itemId, name, barcode, optionName };
}

test.describe('ut-docs#2097 till dialogs never block the status bar or Lock', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await setOrderTypePromptMode(page, 'top').catch(() => {});
    await page.request.post('/api/pos/reset').catch(() => {});
    if (seeded) {
      await page.request.post('/api/buttons/remove', { form: { code: seeded.barcode } });
      await page.request.post('/api/catalog/item/deactivate', { form: { id: seeded.itemId } });
      seeded = null;
    }
  });

  test('#modifier-modal: the tile opener and the scan-retarget opener', async ({ page }) => {
    const assertClean = watchConsole(page);
    seeded = await seedModifierItem(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    const tile = page.locator('.btn-tile:visible', { hasText: seeded.name });
    const openByTile = async () => {
      await Promise.all([page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')), tile.click()]);
    };
    await openByTile();
    await assertNonBlocking(page, '#modifier-modal');

    // The picker still works: its own Add lands the line.
    await openByTile();
    const m = page.locator('#modifier-modal');
    await m.locator('.modifier-option', { hasText: seeded.optionName }).locator('input').check();
    await m.getByRole('button', { name: /Add to cart/i }).click();
    await expect(page.locator('#basket')).toContainText(seeded.optionName);
    await expect(m).toBeHidden();

    // ut-docs#2227's HX-Trigger-After-Swap `open-modifier-modal` opener.
    await page.evaluate(async () => {
      const tileEl = document.querySelector(`.btn-tile[hx-target="#modifier-modal"]`) as HTMLElement;
      const path = tileEl.getAttribute('hx-get')!;
      await (window as any).htmx.ajax('get', path, { target: '#modifier-modal', swap: 'innerHTML' });
      document.body.dispatchEvent(new CustomEvent('open-modifier-modal'));
    });
    await assertNonBlocking(page, '#modifier-modal');
    assertClean();
  });

  test('#order-type-prompt-modal: sale-start prompt, and the gated modifier path opens the picker above nothing modal', async ({ page }) => {
    const assertClean = watchConsole(page);
    seeded = await seedModifierItem(page);
    await setOrderTypePromptMode(page, 'before_item');
    await page.goto('/');
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    await assertNonBlocking(page, '#order-type-prompt-modal');
    // Escape counted as Cancel for this sale: the sale-start nag does not
    // immediately reopen itself.
    await page.waitForTimeout(400);
    await expect(page.locator('#order-type-prompt-modal')).toBeHidden();

    // The per-item gate still fires; answering opens the picker via app.js's
    // htmx.ajax path -- also non-modal, and painted where it can be used.
    await page.locator('.btn-tile:visible', { hasText: seeded.name }).click();
    await expect(page.locator('#order-type-prompt-modal')).toBeVisible();
    expect(await page.locator('#order-type-prompt-modal').evaluate((d) => d.matches(':modal'))).toBe(false);
    await page.getByTestId('order-type-prompt-takeaway').click();
    await expect(page.locator('#order-type-prompt-modal')).toBeHidden();
    await assertNonBlocking(page, '#modifier-modal');
    assertClean();
  });

  test('#order-type-prompt-modal paints above an open #modifier-modal', async ({ page }) => {
    seeded = await seedModifierItem(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/ui/pos/modifiers')),
      page.locator('.btn-tile:visible', { hasText: seeded.name }).click(),
    ]);
    await expect(page.locator('#modifier-modal')).toBeVisible();
    await page.evaluate(() => (document.getElementById('order-type-prompt-modal') as HTMLDialogElement).show());
    const onTop = await page.evaluate(() => {
      const d = document.getElementById('order-type-prompt-modal')!;
      const r = d.getBoundingClientRect();
      return d.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2));
    });
    expect(onTop).toBe(true);
    // Escape closes only the topmost one.
    await page.keyboard.press('Escape');
    await expect(page.locator('#order-type-prompt-modal')).toBeHidden();
    await expect(page.locator('#modifier-modal')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('#modifier-modal')).toBeHidden();
  });

  test('#table-modal (basket table picker)', async ({ page }) => {
    const label = `T2097 ${RUN}`;
    await deactivateAllTables(page);
    await createTable(page, label);
    try {
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await page.locator('.scan-row input[name="code"]').fill('5000000000012');
      await Promise.all([
        page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
        page.locator('.scan-row button[type=submit]').click(),
      ]);
      await expect(page.locator('#basket')).toContainText('Coca-Cola');
      await page.getByTestId('table-picker-open').click();
      await assertNonBlocking(page, '#table-modal');
    } finally {
      await deactivateAllTables(page);
    }
  });

  test('#barcode-backfill-modal (catalog, admin)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/catalog');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/catalog/barcode-backfill')),
      page.locator('#catalog-barcode-backfill-btn').click(),
    ]);
    await assertNonBlocking(page, '#barcode-backfill-modal');
    assertClean();
  });

  test('#table-qr-modal (tables, admin)', async ({ page }) => {
    const label = `Q2097 ${RUN}`;
    await deactivateAllTables(page);
    await createTable(page, label);
    try {
      const openQR = async () => {
        await Promise.all([
          page.waitForResponse((r) => r.url().includes('/qr')),
          page.getByTestId('table-qr-open').first().click(),
        ]);
      };
      await openQR();
      await assertNonBlocking(page, '#table-qr-modal', { scrimCloses: true, reopen: openQR });
    } finally {
      await deactivateAllTables(page);
    }
  });
});

// ut-docs#3212: the older non-modal till dialogs (opened with .show() since
// long before #2097) never had the keyboard half of showModal()'s inertness
// either: with one open, Tab walked out to a product tile or Pay behind the
// scrim and Enter acted on it. They opt into base.html's trap with
// data-ut-focus-trap -- the trap without Escape-to-close (data-ut-escape-
// close still implies both). Same Tab / Shift+Tab walk as (e) above, plus
// focus forced onto the page behind is pulled back into the dialog; the
// on-screen keyboard, status bar and Lock stay focusable.
async function assertFocusTrapped(page: Page, sel: string) {
  const dlg = page.locator(sel);
  await expect(dlg).toBeVisible();
  await page.evaluate((s) => {
    const d = document.querySelector(s) as HTMLElement;
    const f = d.querySelector('a[href], button, input, select, textarea, [tabindex]') as HTMLElement | null;
    (f || d).focus();
  }, sel);
  for (const key of ['Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Tab', 'Shift+Tab', 'Shift+Tab', 'Shift+Tab', 'Shift+Tab', 'Shift+Tab', 'Shift+Tab']) {
    await page.keyboard.press(key);
    const inside = await page.evaluate((s) => !!document.querySelector(s)?.contains(document.activeElement), sel);
    expect(inside, `${key} keeps focus inside ${sel}`).toBe(true);
  }
  // Focus that lands on the page behind (a script, a stray click target)
  // is pulled back into the dialog.
  const pulledBack = await page.evaluate((s) => {
    const d = document.querySelector(s)!;
    const behind = Array.from(document.querySelectorAll('main button, main a[href], main input, .nav a[href]'))
      .find((el) => !d.contains(el) && !(el as Element).closest('dialog, .statusbar, .session-lock, #osk') && (el as HTMLElement).getClientRects().length > 0) as HTMLElement | undefined;
    if (!behind) return 'no focusable element behind the dialog';
    behind.focus();
    return d.contains(document.activeElement) ? 'ok' : `focus stayed on ${behind.outerHTML.slice(0, 80)}`;
  }, sel);
  expect(pulledBack, `focus behind ${sel} is pulled back`).toBe('ok');
  // The status bar stays focusable (CLAUDE.md "Offline-first").
  const sbFocus = await page.evaluate(() => {
    const el = document.querySelector('.statusbar a[href], .statusbar button, .statusbar [tabindex]') as HTMLElement | null;
    if (!el) return 'none';
    el.focus();
    return document.activeElement === el ? 'ok' : 'stolen';
  });
  expect(sbFocus, 'the status bar keeps focus while a trapped dialog is open').not.toBe('stolen');
}

test.describe('ut-docs#3212 older non-modal till dialogs trap Tab like the #2097 ones', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset').catch(() => {});
  });

  async function scanCoke(page: Page) {
    await page.locator('.scan-row input[name="code"]').fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
  }

  test('#hold-modal and #parked-orders-modal (sale screen)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await scanCoke(page);
    await page.getByTestId('tender-footer-hold').click();
    await assertFocusTrapped(page, '#hold-modal');
    // Still a working dialog: holding lands the sale in Open orders.
    await page.locator('#hold-label-input').fill(`F3212 ${RUN}`);
    await page.locator('#hold-modal button[type=submit]').click();
    await expect(page.locator('#hold-modal')).toBeHidden();

    await page.getByTestId('parked-orders-open').click();
    await expect(page.locator('#parked-orders-body')).toContainText(`F3212 ${RUN}`);
    await assertFocusTrapped(page, '#parked-orders-modal');
    await page.locator('#parked-orders-modal button', { hasText: 'Close' }).last().click();
    await expect(page.locator('#parked-orders-modal')).toBeHidden();
    assertClean();
  });

  test('#category-items-modal (sale screen), and a dialog opened over it keeps its own focus', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.evaluate(() => (document.getElementById('category-items-modal') as HTMLDialogElement).show());
    await assertFocusTrapped(page, '#category-items-modal');
    // Review S1: native show() focuses the new dialog BEFORE it becomes the
    // topmost scrim popup; the trap must not pull that focus back into the
    // trapped popup underneath (category popup -> modifier / order-type).
    const focusIn = await page.evaluate(() => {
      const d = document.getElementById('order-type-prompt-modal') as HTMLDialogElement;
      d.show();
      return d.contains(document.activeElement);
    });
    expect(focusIn, 'a dialog shown over a trapped one holds its own focus').toBe(true);
    await page.keyboard.press('Escape');
    await expect(page.locator('#order-type-prompt-modal')).toBeHidden();
  });

  test('#parked-orders-modal: Tab from the Move-table toggle enters its options (review S2)', async ({ page }) => {
    const label = `M3212 ${RUN}`;
    await deactivateAllTables(page);
    await createTable(page, label);
    try {
      await page.goto('/');
      await page.waitForSelector('.pos-container');
      await scanCoke(page);
      await page.getByTestId('tender-footer-hold').click();
      await page.locator('#hold-modal button[type=submit]').click();
      await expect(page.locator('#hold-modal')).toBeHidden();
      await page.getByTestId('parked-orders-open').click();
      const toggle = page.locator('#parked-orders-modal .parked-move-toggle').first();
      await expect(toggle).toBeVisible();
      await toggle.focus();
      await page.keyboard.press('Enter');
      await expect(page.locator('#parked-orders-modal .parked-move-option').first()).toBeVisible();
      await page.keyboard.press('Tab');
      expect(await page.evaluate(() => !!document.activeElement?.classList.contains('parked-move-option')),
        'Tab from the open toggle lands on its first table option').toBe(true);
      await page.keyboard.press('Shift+Tab');
      expect(await page.evaluate(() => !!document.activeElement?.classList.contains('parked-move-toggle')),
        'Shift+Tab returns to the toggle').toBe(true);
    } finally {
      await deactivateAllTables(page);
    }
  });

  test('#pfand-modal (menu)', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    await page.waitForSelector('.menu-grid');
    await page.locator('[data-testid="menu-pfand-open"]').click();
    await assertFocusTrapped(page, '#pfand-modal');
    assertClean();
  });
});
