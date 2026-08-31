import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#413: an external tester on a real ~360dp-wide Android phone found
// the till's server-rendered UI has no phone-width responsive layout — only
// one breakpoint existed (@media max-width: 900px, a tablet floor) and
// nothing below it. Root causes actually observed live at 360x640 (via
// page.evaluate() geometry against the pre-fix build, not guessed):
//
//  1. `.nav` (web/ui/partials/nav.html) sets `flex-wrap: nowrap` (app.css
//     ~line 104) — its content needed ~415px at 360px viewport, so the
//     right-most chips (bugreport toggle, sync/session chips, and the
//     session chip's own Lock button) rendered past the right edge of the
//     viewport entirely.
//  2. `.kiosk-header` (index.html) and `.kiosk-actions` (its child) are also
//     un-wrapped flex rows. `.kiosk-actions` alone needed ~411px of width at
//     this viewport (three buttons: New Sale / Inventory / Deposit refund);
//     since it doesn't wrap, flexbox instead shrank each button below its
//     own single-line content width, forcing the button's own longest WORD
//     to wrap onto a second line inside a squeezed box — confirmed live:
//     "New Sale" and "Deposit refund" both measured a rendered height of
//     ~66px (two text lines) instead of the normal ~48px one-line button.
//  3. Both of the above push `document.documentElement`'s content wider
//     than the viewport; `body.sale-screen` sets `overflow: hidden` (by
//     design, so the sale screen itself never gains a page scrollbar), so
//     the excess width isn't a visible/reachable scroll — it's simply
//     invisible, off-canvas content. This is the shared mechanism behind
//     the ticket's "blank bar" / "clipped nav item" / "Lock button clipped
//     to Lo" reports, and (per the ticket's own note) is suspected to be
//     the same root cause behind the /menu page reading as mostly empty
//     white space.
//
// Fix (app.css, new @media (max-width: 480px) tier next to each existing
// rule, logical properties only): `.nav`, `.nav-right`, `.kiosk-header` and
// `.kiosk-actions` gain `flex-wrap: wrap` at this tier, so overflowing
// content moves to additional rows instead of either shrinking below its
// own content's minimum size or running off-canvas. `.pay-grid`,
// `.tender-footer`, `.split-grid` and `.split-controls` (all `1fr 1fr` two-
// column grids feeding the money-path Cash/Card/Hold/New-customer/split-
// tender buttons) collapse to a single column so no tender button is ever
// squeezed. `.tender-actions` (named in the original ticket) turned out to
// be dead CSS — no template applies that class — so it's updated for
// parity with the ticket but isn't load-bearing; see the dev notes in
// docs/code-reviews for the live-verified detail.
test.describe('phone-width layout (ut-docs#413)', () => {
  test.use({ viewport: { width: 360, height: 640 } });

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('sale screen (/) never needs horizontal scroll at 360px', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(
      overflow.scrollWidth,
      `document must not overflow horizontally (scrollWidth ${overflow.scrollWidth} vs clientWidth ${overflow.clientWidth})`,
    ).toBeLessThanOrEqual(overflow.clientWidth);
    assertClean();
  });

  test('/menu never needs horizontal scroll at 360px', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/menu');
    await page.waitForSelector('.menu-grid');

    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(
      overflow.scrollWidth,
      `document must not overflow horizontally (scrollWidth ${overflow.scrollWidth} vs clientWidth ${overflow.clientWidth})`,
    ).toBeLessThanOrEqual(overflow.clientWidth);
    assertClean();
  });

  test('every .menu-tile stays fully within the viewport width at 360px', async ({ page }) => {
    await page.goto('/menu');
    await page.waitForSelector('.menu-tile');

    const tiles = await page.evaluate(() =>
      Array.from(document.querySelectorAll('.menu-tile')).map((t) => {
        const r = t.getBoundingClientRect();
        return { label: t.textContent?.trim() || '', left: r.left, right: r.right };
      }),
    );
    expect(tiles.length, 'menu must render its tiles').toBeGreaterThan(0);
    for (const t of tiles) {
      expect(t.left, `tile "${t.label}" left edge inside the viewport`).toBeGreaterThanOrEqual(-0.5);
      expect(t.right, `tile "${t.label}" right edge inside the viewport (360px)`).toBeLessThanOrEqual(360.5);
    }
  });

  test('the deposit-refund (pfand) modal and its manager-PIN-gated controls stay fully on-screen', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    // 2026-08-30 (nav rail, ut-docs#1332): the rail's own trigger
    // (data-testid="kiosk-pfand-open", nav.html) is hidden at this file's
    // 360px width -- index.html's phone-only fallback row
    // (data-testid="kiosk-pfand-open-phone") is what's actually on-screen
    // here, same real handler/#pfand-modal target either way.
    await page.getByTestId('kiosk-pfand-open-phone').click();
    await expect(page.locator('#pfand-modal')).toBeVisible();

    const geometry = await page.evaluate(() => {
      const rect = (el: Element | null) => {
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width };
      };
      return {
        innerWidth: window.innerWidth,
        modal: rect(document.getElementById('pfand-modal')),
        amount: rect(document.getElementById('pfand-amount')),
        pin: rect(document.querySelector('#pfand-modal input[name="manager_pin"]')),
        payBtn: rect(document.querySelector('#pfand-modal button[type="submit"]')),
        cancelBtn: rect(document.querySelector('#pfand-modal .modifier-actions .btn.secondary')),
      };
    });

    for (const [name, box] of Object.entries(geometry)) {
      if (name === 'innerWidth' || !box) continue;
      const b = box as { left: number; right: number };
      expect(b.left, `${name} left edge on-screen (>= 0)`).toBeGreaterThanOrEqual(-0.5);
      expect(
        b.right,
        `${name} right edge on-screen (<= viewport width ${geometry.innerWidth})`,
      ).toBeLessThanOrEqual(geometry.innerWidth + 0.5);
    }

    await page.locator('#pfand-modal .modifier-actions .btn.secondary').click();
    assertClean();
  });

  // NOTE: this suite drives the 'default' project (UT_AUTH=off, same as
  // every other spec here bar login.spec.ts) — /ui/session-chip renders
  // empty with no session, so the real Lock button the ticket describes as
  // "clipped to Lo" never exists to assert on in this project. It shares
  // the exact same overflowing ancestor (.nav) as every OTHER always-
  // present nav/header control, so this checks those instead: the same fix
  // that keeps them fully labelled and on-screen is what keeps Lock
  // reachable on a real, logged-in till.
  test('every visible header/nav .btn / .nav-toggle has a non-empty, fully-rendered, on-screen, single-line label', async ({
    page,
  }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    // The chips render async (htmx hx-trigger="load") — give them a turn.
    await expect(page.getByTestId('bugreport-toggle')).toBeVisible();

    const controls = await page.evaluate(() => {
      const els = Array.from(
        document.querySelectorAll('.kiosk-header .btn, .kiosk-header .nav-toggle, .nav .btn, .nav .nav-toggle'),
      ) as HTMLElement[];
      return els
        .filter((el) => el.offsetParent !== null) // actually visible, not [hidden]
        .map((el) => {
          const r = el.getBoundingClientRect();
          return { text: (el.textContent || '').trim(), left: r.left, right: r.right, width: r.width, height: r.height };
        });
    });
    expect(controls.length, 'expected at least one header/nav control').toBeGreaterThan(0);
    for (const c of controls) {
      // Independent review (2026-08-07): the original version of this test
      // asserted only `text !== ''` (server-rendered, always true
      // regardless of layout) and `width > 0` (true even for a control
      // rendered entirely off-canvas by an overflowing ancestor) — a
      // false-pass against the pre-fix CSS for the exact AC bullet it was
      // meant to cover. Pre-fix, "Deposit refund" measured left=361 (wholly
      // outside a 360px viewport) at height=67px (wrapped to two text
      // lines); this now asserts both directly instead of relying on a
      // proxy that happened not to notice.
      expect(c.text, `control "${c.text}" must have a visible label`).not.toBe('');
      expect(c.width, `control "${c.text}" must have non-zero rendered width`).toBeGreaterThan(0);
      expect(c.left, `control "${c.text}" left edge on-screen`).toBeGreaterThanOrEqual(-0.5);
      expect(c.right, `control "${c.text}" right edge on-screen (360px viewport)`).toBeLessThanOrEqual(360.5);
      // Single-line buttons in this file measure ~46-51px tall; a button
      // squeezed below its own longest word wraps to two lines and
      // measures ~66-67px (both real, live-measured values, not guesses).
      // 60px sits strictly between the two.
      expect(c.height, `control "${c.text}" must render on a single line, not wrapped`).toBeLessThanOrEqual(60);
    }
    assertClean();
  });

  // The 🐞 bug-report toggle is the right-most nav-right chip that's always
  // present with auth off — it sits exactly where the real Lock button
  // would (end of .nav-right, same overflowing ancestor), so it's the best
  // available live proxy for "a right-most nav chip is fully reachable, not
  // run off-canvas by nav overflow" in this project.
  test('the right-most nav-right chip is fully reachable on-screen, not run off-canvas by nav overflow', async ({ page }) => {
    await page.goto('/');
    const toggle = page.getByTestId('bugreport-toggle');
    await expect(toggle).toBeVisible();
    const box = await toggle.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x, 'chip left edge on-screen').toBeGreaterThanOrEqual(-0.5);
    expect(box!.x + box!.width, 'chip right edge on-screen').toBeLessThanOrEqual(360.5);
    // Not just geometrically present — a real hit-test target, not covered
    // or run off-canvas by an overflowing ancestor.
    const hit = await toggle.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === el || el.contains(at));
    });
    expect(hit, 'chip must be the real hit-test target').toBe(true);
  });

  test('basket and tender panels never overlap on the sale screen', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const rects = await page.evaluate(() => {
      const rect = (sel: string) => {
        const el = document.querySelector(sel);
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { top: r.top, bottom: r.bottom, left: r.left, right: r.right };
      };
      return {
        basket: rect('.pos-container > .basket'),
        tender: rect('.pos-container > .tender'),
        products: rect('.pos-container > .products'),
      };
    });
    expect(rects.basket).not.toBeNull();
    expect(rects.tender).not.toBeNull();

    function overlaps(a: { top: number; bottom: number; left: number; right: number }, b: typeof a) {
      return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
    }
    expect(overlaps(rects.basket!, rects.tender!), 'basket must not overlap tender').toBe(false);
    if (rects.products) {
      expect(overlaps(rects.basket!, rects.products), 'basket must not overlap products').toBe(false);
    }
    assertClean();
  });

  test('the Subtotal/Total row is visible and not obscured', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    const totals = page.locator('.basket .totals');
    await expect(totals).toBeVisible();

    const hit = await totals.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      const cy = r.top + Math.min(10, r.height / 2); // near its own top edge
      const at = document.elementFromPoint(cx, cy);
      return !!at && (at === el || el.contains(at));
    });
    expect(hit, 'totals row must not be covered by another panel').toBe(true);
    assertClean();
  });

  test('the "Report an Issue" panel keeps its textarea and voice-note control on-screen', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.getByTestId('bugreport-toggle').click();
    await expect(page.getByTestId('bugreport-panel')).toBeVisible();

    const geometry = await page.evaluate(() => {
      const rect = (el: Element | null) => {
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
      };
      return {
        innerWidth: window.innerWidth,
        innerHeight: window.innerHeight,
        textarea: rect(document.getElementById('ir-note')),
        voiceBtn: rect(document.getElementById('ir-voice-btn')),
      };
    });
    for (const name of ['textarea', 'voiceBtn'] as const) {
      const b = geometry[name]!;
      expect(b, `${name} must exist`).not.toBeNull();
      expect(b.left, `${name} left edge on-screen`).toBeGreaterThanOrEqual(-0.5);
      expect(b.right, `${name} right edge on-screen`).toBeLessThanOrEqual(geometry.innerWidth + 0.5);
      expect(b.top, `${name} top edge on-screen`).toBeGreaterThanOrEqual(-0.5);
      expect(b.bottom, `${name} bottom edge on-screen`).toBeLessThanOrEqual(geometry.innerHeight + 0.5);
    }
    assertClean();
  });

  // ut-docs#413 design brief: the tender action grids (Cash/Card, Hold/New
  // customer, split-tender's own field + action grids) are all `1fr 1fr`
  // two-column layouts today — safe at kiosk/tablet widths, but exactly the
  // kind of thing that squeezes a button below its label at 360px. Assert
  // they've collapsed to one column at the phone tier rather than measuring
  // individual button widths (fragile against locale string length).
  test('tender action grids collapse to one column at 360px', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const cols = await page.evaluate(() => {
      const oneCol = (sel: string) => {
        const el = document.querySelector(sel);
        if (!el) return null;
        return getComputedStyle(el).gridTemplateColumns.trim().split(/\s+/).length;
      };
      return {
        payGrid: oneCol('.pay-grid'),
        tenderFooter: oneCol('.tender-footer'),
      };
    });
    expect(cols.payGrid, '.pay-grid must be a single column at 360px').toBe(1);
    expect(cols.tenderFooter, '.tender-footer must be a single column at 360px').toBe(1);
  });

  // ut-docs#413 AC: truncation must be deliberate, not native mid-character
  // clipping. Independent review (2026-08-07) found this AC had no direct
  // test — `.scan-row input[name="code"]`'s own `min-width: 0` (unlike the
  // nav/kiosk buttons elsewhere in this file) deliberately lets it shrink
  // below its placeholder's content width, which is exactly what produced
  // the reported "Barco" instead of "Barcode". Asserting the computed style
  // this fix actually sets, since a real placeholder-clipping comparison
  // would depend on font metrics this suite has no other reason to pin.
  test('the barcode input truncates its placeholder deliberately (ellipsis), not mid-character', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.pos-container');

    const style = await page.evaluate(() => {
      const el = document.querySelector('.scan-row input[name="code"]');
      if (!el) return null;
      const s = getComputedStyle(el);
      return { overflow: s.overflow, textOverflow: s.textOverflow, whiteSpace: s.whiteSpace };
    });
    expect(style, 'barcode input must exist').not.toBeNull();
    // Chromium normalizes an authored `overflow: hidden` on a single-line
    // text <input> to the computed value "clip" (a UA quirk specific to
    // form controls, confirmed empirically here) — both values disable
    // scrolling/reveal, so either is correct; "visible" is the one value
    // that would actually be a regression.
    expect(['hidden', 'clip']).toContain(style!.overflow);
    expect(style!.textOverflow, 'barcode input must use ellipsis, not a hard mid-character clip').toBe('ellipsis');
    expect(style!.whiteSpace, 'barcode input must not wrap (ellipsis requires nowrap)').toBe('nowrap');
  });
});
