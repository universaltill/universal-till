import { test, expect, type Page } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#3060: the portrait and phone no-clip sweep. #3050 fixed the sale
// screen on an upright tablet and #3059 the <=480px phone tier, but nothing
// walked the surfaces a cashier or a shop owner reaches FROM there at those
// sizes: the payment overlay (each tab and tender method), the item
// pickers, the line discount, the Hold (customer tab name) dialog, and the
// Settings, Reports, Catalog and Menu pages. At 360x800 (phone) and
// 800x1280 (10in tablet upright), in light and dark, German (the longest
// translation) and Persian (RTL), this asserts:
//  - the page never scrolls sideways;
//  - every control is inside the viewport once scrolled to, and the topmost
//    element at its centre is the control itself (elementFromPoint), so
//    nothing covers it;
//  - no box clips a control with overflow hidden (only a box that scrolls
//    may hold it out of sight);
//  - a dialog's own box fits the viewport, so anything taller scrolls
//    inside the dialog, never off the screen;
//  - on the phone, scrolled to the end, nothing of the receipt stays under
//    the pinned tender panel while the status row is up.
// German is a language-pack plugin (ut-plugin-language-de), not a core
// locale: a till without the pack renders `?lang=de` in English. Locally,
// run with UT_TEST_I18N_OVERLAY_DIR=<ut-plugin-language-de>/locales to get
// the real German strings; the test records which one it saw.
//
// The owner's extra case: at display scale (ui_scale) 2 the stacked basket
// overflowed sideways at 540-900px wide (measured on main: 800x1280 +7px,
// 768x1024 +39px, 600x960 +207px, 540x720 +267px) and clipped the remove
// button. The last block covers those four viewports: the page, the basket
// card and its lines never scroll sideways, the remove button stays inside
// the basket. (The rest of the sale screen at that scale on a 540-600px
// window -- the tender panel and the product tabs -- has no room left at
// all; ut-docs#4625 bounds the scale by the window width, and the second
// test of the last block asserts it: the tender panel and the product tabs
// pass the sweep, Pay shows its full label, and the scan-row and footer
// controls sit inside the tender card.)

// Demo-seed barcodes (portrait-tablet-sale-3050's list); some are variant
// items, whose scan opens the variant picker.
const CODES = [
  '5000000000012', '5000000000029', '5000000000036', '5000000000043', '5000000000050',
  '5000000000067', '5000000000074', '5000000000081', '5000000000098',
];

const VIEWPORTS = [
  { width: 360, height: 800, label: 'phone' },
  { width: 800, height: 1280, label: '10in tablet portrait' },
];
const COMBOS = [
  { lang: 'de', theme: 'light' },
  { lang: 'de', theme: 'dark' },
  { lang: 'fa', theme: 'light' },
  { lang: 'fa', theme: 'dark' },
] as const;

const CONTROLS = 'button, a.btn, input:not([type=hidden]), select, textarea, [role=tab]';

async function useTheme(page: Page, theme: string) {
  if (theme !== 'dark') return;
  await page.evaluate(async () => {
    const link = document.getElementById('theme-css') as HTMLLinkElement;
    if (link.getAttribute('href') === '/themes/dark.css') return;
    await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
  });
}

async function open(page: Page, path: string, lang: string, theme: string) {
  const sep = path.includes('?') ? '&' : '?';
  await page.goto(`${path}${sep}lang=${lang}`);
  await page.waitForLoadState('load');
  await useTheme(page, theme);
  await expect(page.locator('html')).toHaveAttribute('dir', lang === 'fa' ? 'rtl' : 'ltr');
  if (lang === 'de' && !test.info().annotations.some((a) => a.type === 'de-pack')) {
    const german = await page.evaluate(() => /Einstellungen|Zahlung|Warenkorb|Berichte|Katalog/.test(document.body.innerText));
    test.info().annotations.push({ type: 'de-pack', description: german
      ? 'German pack loaded: real German strings'
      : 'German pack not loaded (UT_TEST_I18N_OVERLAY_DIR unset): ?lang=de rendered English' });
  }
}

// Every problem with `root` (a page's <main> or an open dialog) at the
// current viewport, as readable strings with the measured numbers.
async function sweep(page: Page, label: string, root: string, isDialog = false): Promise<string[]> {
  // Let any popup motion end (the suite runs reduced-motion; this is a guard).
  await page.waitForFunction(() => !document.querySelector('[data-ut-closing]'), undefined, { timeout: 3_000 }).catch(() => {});
  return page.evaluate(({ label, root, isDialog, CONTROLS }) => {
    const out: string[] = [];
    const de = document.documentElement;
    const vw = de.clientWidth;
    const vh = window.innerHeight;
    if (de.scrollWidth > vw + 1) out.push(`${label}: page scrolls sideways (scrollWidth ${de.scrollWidth} > ${vw})`);
    const el = document.querySelector(root) as HTMLElement | null;
    if (!el) return [`${label}: ${root} not found`];
    const name = (e: Element) => {
      const t = (e.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 24);
      return `${e.tagName.toLowerCase()}${e.id ? '#' + e.id : ''}${e.className && typeof e.className === 'string' ? '.' + e.className.trim().split(/\s+/)[0] : ''}${t ? ` "${t}"` : ''}`;
    };
    if (isDialog) {
      const r = el.getBoundingClientRect();
      if (r.left < -1 || r.right > vw + 1 || r.top < -1 || r.bottom > vh + 1) {
        out.push(`${label}: dialog box [${Math.round(r.left)},${Math.round(r.top)} - ${Math.round(r.right)},${Math.round(r.bottom)}] outside the ${vw}x${vh} viewport`);
      }
    }
    const shown = (c: Element) => {
      const r = c.getBoundingClientRect();
      if (!r.width || !r.height) return false;
      if (c.closest('dialog:not([open]), [hidden], #osk')) return false;
      if (!isDialog && c.closest('dialog')) return false;
      const s = getComputedStyle(c);
      return s.visibility !== 'hidden' && s.display !== 'none';
    };
    const controls = [...el.querySelectorAll(CONTROLS)].filter(shown);
    // Not vacuous: a surface that renders no controls is a broken sweep.
    if (!controls.length) out.push(`${label}: no controls found in ${root}`);
    for (const c of controls) {
      // Every box that cuts the control off, per axis, inside out: one that
      // scrolls (auto/scroll) can bring it into its own box, so from there
      // on that box is what must stay visible; one that hides overflow
      // (hidden/clip) makes it unreachable.
      for (const axis of ['x', 'y'] as const) {
        let cur = c.getBoundingClientRect();
        let via = name(c);
        for (let p = c.parentElement; p && p !== el.parentElement; p = p.parentElement) {
          const s = getComputedStyle(p);
          const ov = axis === 'x' ? s.overflowX : s.overflowY;
          if (ov === 'visible') continue;
          const pr = p.getBoundingClientRect();
          const cut = axis === 'x' ? (cur.left < pr.left - 1 || cur.right > pr.right + 1) : (cur.top < pr.top - 1 || cur.bottom > pr.bottom + 1);
          if (cut && /(hidden|clip)/.test(ov)) {
            const [a, b, pa, pb] = axis === 'x' ? [cur.left, cur.right, pr.left, pr.right] : [cur.top, cur.bottom, pr.top, pr.bottom];
            out.push(`${label}: ${name(c)}${via === name(c) ? '' : ` (in scroller ${via.split(' ')[0]})`} [${Math.round(a)}-${Math.round(b)}] clipped (${axis}) by ${name(p).split(' ')[0]} [${Math.round(pa)}-${Math.round(pb)}]`);
            break;
          }
          if (cut) { cur = pr; via = name(p); }
        }
      }
    }
    for (const c of controls) {
      (c as HTMLElement).scrollIntoView({ block: 'center', inline: 'nearest' });
      const r = c.getBoundingClientRect();
      if (r.left < -1 || r.right > vw + 1 || r.top < -1 || r.bottom > vh + 1) {
        out.push(`${label}: ${name(c)} at [${Math.round(r.left)},${Math.round(r.top)} - ${Math.round(r.right)},${Math.round(r.bottom)}] outside the ${vw}x${vh} viewport`);
        continue;
      }
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      const ok = hit && (hit === c || c.contains(hit)
        || (c instanceof HTMLInputElement && hit.closest('label') && hit.closest('label') === c.closest('label')));
      if (!ok) out.push(`${label}: ${name(c)} covered by ${hit ? name(hit) : 'nothing'}`);
    }
    window.scrollTo(0, 0);
    // One line per distinct problem (a table repeats the same row).
    return [...new Set(out)];
  }, { label, root, isDialog, CONTROLS });
}

// At <= 480px the scan row, basket and Pay live in the bottom sheet
// (ut-docs#3059); open it unless it is already up. A no-op on a tablet.
async function ensureSheet(page: Page) {
  const bar = page.locator('.basket-phonebar');
  if (await bar.isVisible() && !(await page.evaluate(() => document.body.classList.contains('pos-sheet-open')))) {
    await bar.click();
    await expect(page.locator('body')).toHaveClass(/pos-sheet-open/);
  }
}

async function scan(page: Page, code: string) {
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

async function modifierOpen(page: Page) {
  return page.locator('#modifier-modal').evaluate((d) => (d as HTMLDialogElement).open);
}

async function closeModifier(page: Page) {
  await page.evaluate(() => (document.getElementById('modifier-modal') as HTMLDialogElement).close());
}

// The variant item's picker, the modifier groups on it, the line discount,
// the Hold dialog and the payment overlay (both tabs, every split method,
// then a real cash tender and its receipt).
async function sweepSale(page: Page, label: string): Promise<string[]> {
  const fails: string[] = [];
  let groupId = '';
  try {
    // Variant picker: the first demo code that opens it.
    await ensureSheet(page);
    let variantCode = '';
    for (const code of CODES) {
      await scan(page, code);
      if (await modifierOpen(page)) { variantCode = code; break; }
    }
    expect(variantCode, 'a demo item opens the variant picker').not.toBe('');
    fails.push(...await sweep(page, `${label} variant picker`, '#modifier-modal', true));
    const itemId = await page.locator('#modifier-modal input[name="itemId"]').inputValue();
    await closeModifier(page);

    // Modifiers: a group with many options on the same item, so the picker
    // is taller than a phone and must scroll inside itself.
    const groupName = `Sweep3060 ${Date.now().toString(36)}`;
    expect((await page.request.post('/api/catalog/modifier-group', { form: { itemId, name: groupName, minSelect: '0', maxSelect: '8' } })).ok()).toBe(true);
    const html = await (await page.request.get(`/api/catalog/modifier-groups-panel?item_id=${itemId}`)).text();
    const at = html.indexOf(`>${groupName}<`);
    expect(at, 'the new group is listed').toBeGreaterThan(-1);
    const ids = [...html.slice(0, at).matchAll(/data-group-id="([^"]*)"/g)];
    groupId = ids[ids.length - 1][1];
    for (const opt of ['Extra Käse', 'Ohne Zwiebeln', 'Doppelte Portion', 'Glutenfrei', 'Scharfe Soße', 'Große Beilage', 'Zum Mitnehmen', 'Laktosefrei']) {
      expect((await page.request.post('/api/catalog/modifier-option', { form: { groupId, itemId, name: opt, priceDelta: '0.50' } })).ok()).toBe(true);
    }
    await scan(page, variantCode);
    expect(await modifierOpen(page), 'the picker reopens with the modifier group').toBe(true);
    await expect(page.locator('#modifier-modal .modifier-group')).toHaveCount(2);
    fails.push(...await sweep(page, `${label} modifiers picker`, '#modifier-modal', true));
    await page.locator('#modifier-modal input[name="variantId"]').first().check();
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan-with-modifiers')),
      page.locator('#modifier-modal button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket tbody tr').first()).toBeVisible({ timeout: 5_000 }).catch(() => {});
  } finally {
    if (groupId) await page.request.post('/api/catalog/modifier-group/delete', { form: { groupId } });
  }

  // Basket with lines, a line discount set (the discount control).
  await ensureSheet(page);
  const disc = page.locator('#basket .disc-input').first();
  await disc.fill('50');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/line')),
    disc.press('Tab'),
  ]);
  fails.push(...await sweep(page, `${label} basket + discount`, '#basket'));

  // Hold (the customer's tab name).
  await page.getByTestId('tender-footer-hold').click();
  await expect(page.locator('#hold-modal')).toHaveJSProperty('open', true);
  fails.push(...await sweep(page, `${label} hold dialog`, '#hold-modal', true));
  await page.evaluate(() => (document.getElementById('hold-modal') as HTMLDialogElement).close());

  // Payment overlay.
  await page.getByTestId('payment-open').click();
  await expect(page.locator('#payment-overlay')).toHaveJSProperty('open', true);
  fails.push(...await sweep(page, `${label} payment: pay tab`, '#payment-overlay', true));
  await page.locator('#tab-split').click();
  const methods = await page.locator('#split-tender-method option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value).filter(Boolean));
  expect(methods.length, 'split offers tender methods').toBeGreaterThan(0);
  for (const m of methods) {
    await page.locator('#split-tender-method').selectOption(m);
    fails.push(...await sweep(page, `${label} payment: split ${m}`, '#payment-overlay', true));
  }
  await page.locator('#tab-pay').click();
  // A long sale, so the receipt is taller than the sheet and scrolls.
  await page.locator('[data-testid="payment-close"]').click();
  for (const code of CODES) {
    await scan(page, code);
    if (await modifierOpen(page)) await closeModifier(page);
  }
  await page.getByTestId('payment-open').click();
  await expect(page.locator('#payment-overlay')).toHaveJSProperty('open', true);
  await page.locator('#tab-pay').click();
  // A problem chip puts the status row over the phone sheet's foot (it is
  // hidden there otherwise). Show one, as on a till whose disk is low, so
  // the receipt is checked with the row up: its last button once sat under
  // Pay for good (the sheet did not reserve the row's height).
  await page.evaluate(() => {
    const bar = document.querySelector('.statusbar')!;
    if (bar.querySelector('.sb-disk-low')) return;
    const chip = document.createElement('a');
    chip.className = 'sb-item sb-disk-low'; chip.href = '#'; chip.textContent = 'Disk space low';
    bar.insertBefore(chip, bar.firstChild);
  });
  if (await page.locator('.basket-phonebar').count() && page.viewportSize()!.width <= 480) {
    await expect.poll(() => page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--phone-sb-h')) || 0)).toBeGreaterThan(0);
  }
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/tender')),
    page.getByTestId('pay-default').click(),
  ]);
  await expect(page.getByTestId('receipt-new-customer')).toBeVisible();
  fails.push(...await sweep(page, `${label} receipt`, '#basket'));
  // The phone sheet scrolls with Pay pinned at its foot: scrolled to the
  // end, nothing of the basket/receipt may stay under the tender panel.
  const end = await page.evaluate(() => {
    const pc = document.querySelector('.pos-container') as HTMLElement;
    const tender = document.querySelector('.pos-container > .tender') as HTMLElement;
    if (getComputedStyle(tender).position !== 'sticky') return null;
    pc.scrollTop = pc.scrollHeight;
    return { basketBottom: document.getElementById('basket')!.getBoundingClientRect().bottom, tenderTop: tender.getBoundingClientRect().top };
  });
  if (end && end.basketBottom > end.tenderTop + 1) {
    fails.push(`${label} receipt: scrolled to the end, the last ${Math.round(end.basketBottom - end.tenderTop)}px of the receipt stay under the tender panel`);
  }
  return fails;
}

test.describe('portrait + phone no-clip sweep (ut-docs#3060)', () => {
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  for (const vp of VIEWPORTS) {
    for (const { lang, theme } of COMBOS) {
      const tag = `${vp.width}x${vp.height} ${lang} ${theme}`;

      test(`${tag}: sale dialogs and the payment overlay fit, nothing clipped or covered`, async ({ page }) => {
        const assertClean = watchConsole(page);
        await page.setViewportSize({ width: vp.width, height: vp.height });
        await page.request.post('/api/pos/reset');
        await open(page, '/', lang, theme);
        const fails = await sweepSale(page, tag);
        expect(fails, fails.join('\n')).toEqual([]);
        assertClean();
      });

      test(`${tag}: Settings, Reports (every tab), Catalog and Menu, nothing clipped or covered`, async ({ page }) => {
        const assertClean = watchConsole(page);
        await page.setViewportSize({ width: vp.width, height: vp.height });
        const fails: string[] = [];
        for (const path of ['/catalog', '/menu', '/settings']) {
          await open(page, path, lang, theme);
          await page.waitForTimeout(200);
          fails.push(...await sweep(page, `${tag} ${path}`, 'main'));
        }
        // Settings shows one section at a time (ut-docs#1960, #3090): open
        // each through its deep link, the way a search hit or a link does.
        const sections = await page.locator('#settings-nav-index li').evaluateAll((lis) =>
          lis.map((li) => li.getAttribute('data-key') || '').filter((k) => k && document.getElementById(k)));
        expect(sections.length, 'settings sections').toBeGreaterThan(10);
        for (const id of sections) {
          await page.evaluate((id) => { location.hash = id; }, id);
          await expect(page.locator(`[id="${id}"]`)).toBeVisible();
          fails.push(...await sweep(page, `${tag} /settings#${id}`, 'main'));
        }
        await open(page, '/reports', lang, theme);
        const tabs = await page.locator('main [role=tab][id^="report-tab-"]').evaluateAll((ts) => ts.map((t) => t.id));
        expect(tabs.length, 'reports has tabs').toBeGreaterThan(3);
        fails.push(...await sweep(page, `${tag} /reports`, 'main'));
        for (const id of tabs) {
          await Promise.all([
            page.waitForResponse((r) => r.url().includes('/ui/reports/tab/')),
            page.locator(`#${id}`).click(),
          ]);
          await page.waitForTimeout(100);
          fails.push(...await sweep(page, `${tag} /reports ${id}`, 'main'));
        }
        expect(fails, fails.join('\n')).toEqual([]);
        assertClean();
      });
    }
  }

  // The owner's measurements on main (ut-docs#3060 comment).
  test.describe('ui_scale 2: the stacked basket does not overflow sideways', () => {
    test.afterEach(async ({ page }) => {
      // The worker till is shared by every spec file the worker runs.
      await page.request.post('/api/settings/ui-scale', { form: { scale: '1' } });
    });

    test('540-900px wide at ui_scale 2: no horizontal page scroll, remove button whole', async ({ page }) => {
      expect((await page.request.post('/api/settings/ui-scale', { form: { scale: '2' } })).ok()).toBe(true);
      const fails: string[] = [];
      for (const [w, h] of [[800, 1280], [768, 1024], [600, 960], [540, 720]]) {
        await page.setViewportSize({ width: w, height: h });
        await page.request.post('/api/pos/reset');
        await page.goto('/');
        await page.waitForSelector('.pos-container');
        expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--ui-scale').trim())).toBe('2');
        // Two lines (a variant item's picker is closed unused).
        let lines = 0;
        for (const code of CODES) {
          await scan(page, code);
          if (await modifierOpen(page)) { await closeModifier(page); continue; }
          if (++lines === 2) break;
        }
        await expect(page.locator('#basket tbody tr')).toHaveCount(2);
        const m = await page.evaluate(() => {
          const sc = document.querySelector('.basket-scroll')!.getBoundingClientRect();
          return {
            sw: document.documentElement.scrollWidth,
            cw: document.documentElement.clientWidth,
            basketOver: document.querySelector('.basket-scroll')!.scrollWidth - document.querySelector('.basket-scroll')!.clientWidth,
            // The basket card itself scrolls too (overflow-y: auto makes its
            // overflow-x auto): the order-type toggle once overflowed it.
            cardOver: document.querySelector('.pos-container > .basket')!.scrollWidth - document.querySelector('.pos-container > .basket')!.clientWidth,
            btns: [...document.querySelectorAll('#basket tbody .btn-x')].map((b) => {
              const r = b.getBoundingClientRect();
              return { l: r.left, r: r.right, w: r.width, scL: sc.left, scR: sc.right };
            }),
          };
        });
        const tag = `${w}x${h} ui_scale 2`;
        if (m.sw > m.cw + 1) fails.push(`${tag}: page scrolls sideways by ${m.sw - m.cw}px`);
        if (m.basketOver > 1) fails.push(`${tag}: basket lines scroll sideways by ${m.basketOver}px`);
        if (m.cardOver > 1) fails.push(`${tag}: basket card scrolls sideways by ${m.cardOver}px`);
        for (const b of m.btns) {
          if (b.r > Math.min(m.cw, b.scR) + 1 || b.l < Math.max(0, b.scL) - 1) {
            fails.push(`${tag}: remove button [${Math.round(b.l)}-${Math.round(b.r)}] outside the basket [${Math.round(b.scL)}-${Math.round(b.scR)}] / screen ${m.cw}`);
          }
        }
        // The basket only; the tender panel and the products have their own
        // test below (ut-docs#4625).
        const swept = await sweep(page, tag, '#basket');
        fails.push(...swept);
      }
      expect(fails, fails.join('\n')).toEqual([]);
    });

    // ut-docs#4625: ui_scale scales rem but breakpoints are px, so at
    // scale 2 a 540px window had less rem-width than a 360px phone at scale
    // 1 (the scan-row buttons clipped by the tender card, Pay read "Pa...",
    // the Pay row below the card's clipped bottom), and a 1000-1550px
    // window could not hold the side-by-side grid (the page scrolled
    // sideways by up to 556px). app.css bounds the scale by the window's
    // width: 28.3rem stacked (<= 900px), 46rem side by side. The root size
    // is pinned too, so a change that dropped the scale altogether, or
    // capped a window that has room (1920 wide), fails here.
    test('540-1920px wide at ui_scale 2: the scale fits the window, tender panel and products whole, Pay reads in full', async ({ page }) => {
      expect((await page.request.post('/api/settings/ui-scale', { form: { scale: '2' } })).ok()).toBe(true);
      const fails: string[] = [];
      for (const [w, h] of [[540, 720], [600, 960], [768, 1024], [800, 1280], [1024, 600], [1280, 800], [1440, 900], [1920, 1080]]) {
        await page.setViewportSize({ width: w, height: h });
        expect((await page.request.post('/api/pos/reset')).ok()).toBe(true);
        await page.goto('/');
        await page.waitForSelector('.pos-container');
        // One plain line (a variant item's picker is closed unused).
        for (const code of CODES) {
          await scan(page, code);
          if (!(await modifierOpen(page))) break;
          await closeModifier(page);
        }
        await expect(page.locator('#basket tbody tr')).toHaveCount(1);
        const tag = `${w}x${h} ui_scale 2`;
        // Expected root size: twice the fluid size, unless the window is
        // narrower than the tier needs (the --fluid-fs curve from app.css).
        const fluid = Math.min(20, Math.max(17, Math.min(13.57 + 0.00335 * w, 13.25 + 0.00625 * h)));
        const want = Math.min(2 * fluid, Math.max(fluid, w / (w <= 900 ? 28.3 : 46)));
        const root = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize));
        if (Math.abs(root - want) > 0.25) fails.push(`${tag}: root font-size ${root}px, want ${want.toFixed(2)}px`);
        const page_ = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }));
        if (page_.sw > page_.cw + 1) fails.push(`${tag}: page scrolls sideways by ${page_.sw - page_.cw}px`);
        fails.push(...await sweep(page, tag, '.pos-container > .tender'));
        fails.push(...await sweep(page, tag, '.pos-container > .products'));
        const m = await page.evaluate(() => {
          const card = document.querySelector('.pos-container > .tender') as HTMLElement;
          const c = card.getBoundingClientRect();
          const pay = document.querySelector('[data-testid=payment-open]') as HTMLElement;
          const outside: string[] = [];
          for (const root of ['.scan-row', '.tender-default-footer']) {
            for (const el of document.querySelectorAll(`.pos-container > .tender ${root} :is(button, a, input, select, textarea)`)) {
              const r = el.getBoundingClientRect();
              if (r.width === 0 || r.height === 0) continue; // hidden control
              if (r.left < c.left - 1 || r.right > c.right + 1 || r.top < c.top - 1 || r.bottom > c.bottom + 1) {
                const n = (el.getAttribute('data-testid') || el.id || el.className || el.tagName).toString().slice(0, 30);
                outside.push(`${root} ${n} [${Math.round(r.left)}-${Math.round(r.right)} x ${Math.round(r.top)}-${Math.round(r.bottom)}] outside the tender card [${Math.round(c.left)}-${Math.round(c.right)} x ${Math.round(c.top)}-${Math.round(c.bottom)}]`);
              }
            }
          }
          return { payOver: pay.scrollWidth - pay.clientWidth, payText: (pay.textContent || '').trim().replace(/\s+/g, ' '), outside };
        });
        // Stacked tier only: side by side, Pay's ellipsis at a high scale
        // is the footer's own layout (Pay + three icons in the products
        // column), not the window bound: ut-docs#4897.
        if (w <= 900 && m.payOver > 1) fails.push(`${tag}: Pay label "${m.payText}" is clipped by ${m.payOver}px`);
        for (const o of m.outside) fails.push(`${tag}: ${o}`);
      }
      expect(fails, fails.join('\n')).toEqual([]);
    });

    // ut-docs#4897: side by side (wider than 900px) the tender column is too
    // narrow at a high scale for Pay's label plus the three icons; Pay used
    // to ellipsise to "Pa...". The row wraps instead.
    test('side by side at ui_scale 1.5 and 2: Pay shows its full label, icons stay in the tender column', async ({ page }) => {
      const fails: string[] = [];
      for (const scale of ['1.5', '2']) {
        expect((await page.request.post('/api/settings/ui-scale', { form: { scale } })).ok()).toBe(true);
        for (const [w, h] of [[1024, 600], [1280, 800], [1920, 1080]]) {
          await page.setViewportSize({ width: w, height: h });
          await page.request.post('/api/pos/reset');
          await page.goto('/');
          await page.waitForSelector('.pos-container');
          let lines = 0;
          for (const code of CODES) {
            await scan(page, code);
            if (await modifierOpen(page)) { await closeModifier(page); continue; }
            if (++lines === 1) break;
          }
          await expect(page.locator('#basket tbody tr')).toHaveCount(1);
          const m = await page.evaluate(() => {
            const pay = document.querySelector('[data-testid="payment-open"]') as HTMLElement;
            // Bounded by the tender column, not the viewport: at 1024px and
            // scale 1.5 the whole sale grid is wider than the window on main
            // (a separate overflow, filed on its own card).
            const t = document.querySelector('.tender')!.getBoundingClientRect();
            return {
              clipped: pay.scrollWidth - pay.clientWidth,
              payBox: (({ left, right }) => ({ l: left, r: right }))(pay.getBoundingClientRect()),
              tenderOver: document.querySelector('.tender')!.scrollWidth - document.querySelector('.tender')!.clientWidth,
              icons: [...document.querySelectorAll('.tender-default-footer .tender-icon-btn')].map((b) => {
                const r = b.getBoundingClientRect();
                return { l: r.left, r: r.right, tl: t.left, tr: t.right };
              }),
            };
          });
          const tag = `${w}x${h} ui_scale ${scale}`;
          if (m.clipped > 0) fails.push(`${tag}: Pay label clipped by ${m.clipped}px`);
          if (m.tenderOver > 1) fails.push(`${tag}: tender column scrolls sideways by ${m.tenderOver}px`);
          if (m.payBox.r > m.icons[0].tr + 1) fails.push(`${tag}: Pay pokes out of the tender column`);
          for (const i of m.icons) {
            if (i.l < i.tl - 1 || i.r > i.tr + 1) fails.push(`${tag}: tender icon [${Math.round(i.l)}-${Math.round(i.r)}] outside the tender column [${Math.round(i.tl)}-${Math.round(i.tr)}]`);
          }
        }
      }
      expect(fails, fails.join('\n')).toEqual([]);
    });
  });
});
