import { test, expect } from './fixtures';
import { watchConsole, waitForStableLayout } from './helpers';

// ut-docs#213: the basket is a full-height first-class panel (>=4 line
// items visible at 1280x800 with no scrolling), carries an always-visible
// item-count badge, the nav logo is legible (rem-sized; ut-docs#298
// reintroduced the light-variant asset retired in ut-docs#290, this time
// specifically for .nav's always-dark background, with no backing plate),
// and errors surface on the single .pos-notice surface, persisting until
// dismissed.
test.use({ viewport: { width: 1280, height: 800 } });

const CODES = [
  '5000000000012',
  '5000000000029',
  '5000000000036',
  '5000000000043',
  '5000000000050',
];

async function scan(page, code: string) {
  // ut-docs#1284: `getByRole('textbox').first()` used to safely skip the
  // basket's qty-input (type="number" -> ARIA role "spinbutton"), but that
  // card's decimal-corruption fix switched it to type="text" (role
  // "textbox") -- so once a basket line exists, `.first()` can resolve to
  // it instead of the scan field, depending on DOM order. Scope directly
  // to the scan-row's own barcode input instead.
  await page.locator('.scan-row input[name="code"]').fill(code);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
    page.locator('.scan-row button[type=submit]').click(),
  ]);
}

async function resetBasket(page) {
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/pos/reset')),
    page.locator('[data-testid="kiosk-checkout-start"]').click(),
  ]);
}

test.describe('sale screen basket layout + count + notices (ut-docs#213)', () => {
  // Server-side reset regardless of UI state, ALWAYS — a failed assertion
  // must not leave basket lines that cascade into the next specs on this
  // shared server (e2e/README.md rule; this exact cascade turned one CI
  // layout failure into three red specs on the first PR run).
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('>=4 basket lines visible without scrolling at 1280x800', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    for (const code of CODES) {
      await scan(page, code);
    }
    await expect(page.locator('.basket table tbody tr')).toHaveCount(CODES.length);

    // ut-docs#320: wait for layout to actually settle before measuring.
    // Root-caused (not guessed): `.basket-scroll` is `flex: 1` inside the
    // `.basket` flex column (app.css) — measured directly, its resolved
    // height lags a fresh row swap by exactly one frame (623.78px ->
    // 635.38px) while the rows inside it are already final. The margin
    // between the 4th (last fully-visible) row's bottom edge and the
    // box's OWN bottom edge is razor-thin (~11px settled, briefly
    // *negative* unsettled) — so a measurement taken before the box
    // itself finishes resizing intermittently undercounts. Must include
    // the box itself in the selector, not just the rows — the rows never
    // move, so watching only them would "stabilize" after the very first
    // frame without ever having watched the element that actually moves.
    await waitForStableLayout(page, '.basket-scroll, .basket-scroll tbody tr');

    // Fully-visible rows inside .basket-scroll's box, not merely in-DOM.
    // Diagnostics attached to the assertion (not just the boolean) so a
    // future occurrence is self-diagnosing instead of another bare
    // "Received: 3" — this AC's margin is real (~2-3px per row) and CI
    // font metrics are already known to sometimes wrap these names to an
    // extra line (see .line-name's own comment in app.css), so a future
    // failure here may be a genuine margin exhaustion, not a settle race.
    const measured = await page.evaluate(() => {
      const scroll = document.querySelector('.basket-scroll') as HTMLElement;
      const box = scroll.getBoundingClientRect();
      let n = 0;
      const rows: { top: number; bottom: number; height: number }[] = [];
      scroll.querySelectorAll('tbody tr').forEach((tr) => {
        const r = (tr as HTMLElement).getBoundingClientRect();
        rows.push({ top: r.top, bottom: r.bottom, height: r.height });
        if (r.height > 0 && r.top >= box.top - 1 && r.bottom <= box.bottom + 1) n++;
      });
      return { fullyVisible: n, box: { top: box.top, bottom: box.bottom, height: box.height }, rows, rootFontSize: getComputedStyle(document.documentElement).fontSize };
    });
    expect(
      measured.fullyVisible,
      `at least 4 line items fully visible without scrolling — got ${measured.fullyVisible}. ` +
        `box=${JSON.stringify(measured.box)} rootFontSize=${measured.rootFontSize} rows=${JSON.stringify(measured.rows)}`,
    ).toBeGreaterThanOrEqual(4);

    await resetBasket(page);
    assertClean();
  });

  test('count badge tracks add, remove and clear', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const badge = page.locator('[data-testid="basket-count"]');
    await expect(badge).toHaveText('0');

    await scan(page, CODES[0]);
    await expect(badge).toHaveText('1');
    await scan(page, CODES[0]); // same item again -> qty 2
    await expect(badge).toHaveText('2');
    await scan(page, CODES[1]);
    await expect(badge).toHaveText('3');

    // Remove the second line entirely. A prior version of this test worked
    // around ut-docs#239 (htmx defers listener binding on freshly-swapped
    // content into its "settle" phase, so a click landing in that window
    // was silently dropped) with a waitForFunction poll for
    // 'htmx-internal-data' before clicking; that workaround is no longer
    // needed now that the fix (web/ui/layouts/base.html,
    // web/ui/pages/self_order_shop.html) sets defaultSettleDelay to 0. NOTE:
    // this immediate click is NOT itself the #239 regression guard — a real
    // Playwright click goes through a CDP round trip too slow to reliably
    // land inside the original ~20ms window even pre-fix (confirmed: this
    // exact assertion still passed with the fix reverted). The dedicated,
    // deterministic guard is the next test below, which races a synthetic
    // click against htmx's internal settle timer directly.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/remove')),
      page.locator('.basket .btn-x').last().click(),
    ]);
    await expect(badge).toHaveText('2');

    await resetBasket(page);
    await expect(page.locator('[data-testid="basket-count"]')).toHaveText('0');
    assertClean();
  });

  test('a click landing in the htmx settle window on freshly-swapped #basket is not dropped (ut-docs#239)', async ({ page }) => {
    // Deterministic version of the race above: a real Playwright .click()
    // goes through a CDP round trip slow enough that it usually lands
    // AFTER htmx's settle timer even pre-fix, so it can't reliably prove
    // this on its own. Instead race a synthetic click scheduled via
    // setTimeout(fn, 0) directly against htmx's internal
    // setTimeout(s, settleDelay) that binds listeners on the swapped-in
    // content (see htmx.min.js's swap(): "htmx:afterSwap" fires, THEN
    // settleDelay>0 schedules listener binding via setTimeout, or runs it
    // synchronously when settleDelay is 0). Pre-fix (settleDelay=20), our
    // 0ms timer is scheduled first and fires first — clicking a still-
    // unbound button, so nothing happens. Post-fix (settleDelay=0), htmx
    // binds listeners synchronously inside the swap call, before our timer
    // even gets a turn on the event loop — so the click always lands bound.
    const assertClean = watchConsole(page);
    await page.goto('/');
    await scan(page, CODES[0]);
    await expect(page.locator('[data-testid="basket-count"]')).toHaveText('1');

    await page.evaluate(() => {
      const onAfterSwap = (ev: Event) => {
        const target = ev.target as HTMLElement;
        if (!target || target.id !== 'basket') return;
        document.body.removeEventListener('htmx:afterSwap', onAfterSwap);
        setTimeout(() => {
          (document.querySelector('.basket .btn-x') as HTMLElement | null)?.click();
        }, 0);
      };
      document.body.addEventListener('htmx:afterSwap', onAfterSwap);
    });
    const removeRequestSeen = page
      .waitForResponse((r) => r.url().includes('/api/pos/remove'), { timeout: 2000 })
      .then(() => true)
      .catch(() => false);
    await scan(page, CODES[1]); // swaps #basket, firing the listener registered above

    expect(await removeRequestSeen, 'a click racing the settle window must still reach the server, not be silently dropped').toBe(true);
    await expect(page.locator('[data-testid="basket-count"]')).toHaveText('1'); // scanned 2 lines, one removed by the race

    await resetBasket(page);
    assertClean();
  });

  test('scan error persists on the notice surface until dismissed', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await scan(page, '0000000000000'); // no such item/customer/promo
    const notice = page.locator('#toast-message.pos-notice.error');
    await expect(notice).toBeVisible();
    await expect(notice).toHaveAttribute('role', 'alert');

    // Well past the info auto-expire window: an error must still be there.
    await page.waitForTimeout(3200);
    await expect(notice).toBeVisible();

    await notice.locator('.notice-dismiss').click();
    await expect(notice).toHaveCount(0);
    assertClean();
  });

  test('nav logo renders legibly large with the light-glyph asset', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const logo = page.locator('.nav .logo img');
    await expect(logo).toHaveAttribute('src', /unitill-logo-light\.svg/);
    const h = await logo.evaluate((el) => el.getBoundingClientRect().height);
    expect(h, 'logo must render at a legible size').toBeGreaterThanOrEqual(36);
    assertClean();
  });

  test('products grid keeps its floor under vertical pressure (OSK padding)', async ({ page }) => {
    // Independent review of #213: with rows "minmax(0,1fr) minmax(0,auto)"
    // the auto (tender) row is maximized BEFORE the fr (products) row gets
    // leftover space, so all vertical pressure — the OSK's 15.5rem
    // body.osk-padded, high ui-scale — would drain the products grid to a
    // rendered height of ZERO (invisible, not clipped: the same failure
    // class .basket-scroll's and .tab-panel's 6rem floors guard). The fix
    // is an 8rem floor on the products row; this emulates the OSK's body
    // padding directly since the failure is pure CSS track sizing.
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container .products');
    await page.evaluate(() => document.body.classList.add('osk-padded'));
    const h = await page.evaluate(
      () => (document.querySelector('.pos-container > .products') as HTMLElement).getBoundingClientRect().height,
    );
    expect(h, 'products grid must keep a usable height with the OSK open').toBeGreaterThan(100);
    await page.evaluate(() => document.body.classList.remove('osk-padded'));
    assertClean();
  });

  test('tender panel sits under products; basket owns the full left column', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container .tender');
    const boxes = await page.evaluate(() => {
      const b = (document.querySelector('.pos-container > .basket') as HTMLElement).getBoundingClientRect();
      const p = (document.querySelector('.pos-container > .products') as HTMLElement).getBoundingClientRect();
      const t = (document.querySelector('.pos-container > .tender') as HTMLElement).getBoundingClientRect();
      return { basket: b, products: p, tender: t };
    });
    expect(boxes.tender.top, 'tender starts below the products grid').toBeGreaterThan(boxes.products.bottom - 2);
    expect(boxes.basket.bottom, 'basket reaches down past the tender top').toBeGreaterThan(boxes.tender.top);
    assertClean();
  });
});

// ut-docs#1339: gap exposed by review of ut-docs#1314 (universal-till PR
// #667, docs/code-reviews/2026-08-30-basket-item-name-column-width.md).
// #1314's fix (stacking qty above discount inside .line-inputs) has a
// trade-off that ONLY bites under `body.kiosk` -- its 2.1rem .qty-input/
// .disc-input min-height (app.css) eats vertical space the non-kiosk
// 1024x600 test above never spends; that review's own measurement (six
// long-named "Cheddar Cheese 400g" lines) found kiosk mode dropping the
// 1024x600 floor to 1 fully-visible row. This suite has no server running
// with UT_KIOSK=1 (grep confirms it -- none of e2e/run-till*.sh set it), so
// rather than stand up a 6th webServer+project pair for one CSS-only
// assertion, this applies the identical class-injection technique the
// 'products grid keeps its floor under vertical pressure' test above
// already uses for `osk-padded`: `body.kiosk`'s CSS rules apply the same
// regardless of whether the class was server-rendered or added by a script,
// since nothing in app.js reads it (verified: no `classList.*kiosk` in any
// web/public/*.js) -- confirmed directly: the min-height computed style
// really does jump from `auto` (~27-28px) to 35.7px on these inputs once
// the class is added, matching the review's own 35.7px figure exactly.
//
// NOT copying the review's "1" here. Re-measured against current `main`
// with THIS suite's own CODES/scan() (five short-named demo items, not the
// review's six long-named ones): today's actual floor is 2, fully-visible,
// identically for both kiosk and non-kiosk, at this viewport -- run 3x,
// byte-for-byte stable. The gap from the review's own numbers is real and
// most likely comes from the different item names (this suite's CODES
// happen not to wrap to a second line at this width, so row height here is
// governed by something other than the input min-height the review's
// longer names were sensitive to) rather than anything having regressed
// since that review; basket-item-name-width-1314.spec.ts is the suite that
// exercises the long-name wrapping case directly, this one is a plain
// row-count floor for whatever this suite's own existing scan helper
// produces. The AC asks for TODAY's number as the floor, not the review's,
// specifically so a FUTURE drop below today's real number is caught --
// asserting the review's stale "1" here would silently pass a regression
// from 2 down to 1.
test.describe('kiosk-mode basket layout floor (ut-docs#1339)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('basket rows visible under body.kiosk at the 1024x600 kiosk floor do not regress below today\'s count', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    await page.waitForSelector('.pos-container');
    await page.evaluate(() => document.body.classList.add('kiosk'));
    for (const code of CODES) {
      await scan(page, code);
    }
    await expect(page.locator('.basket table tbody tr')).toHaveCount(CODES.length);

    // Same settle wait as the non-kiosk assertion above -- ut-docs#320.
    await waitForStableLayout(page, '.basket-scroll, .basket-scroll tbody tr');

    const measured = await page.evaluate(() => {
      const scroll = document.querySelector('.basket-scroll') as HTMLElement;
      const box = scroll.getBoundingClientRect();
      let n = 0;
      const rows: { top: number; bottom: number; height: number }[] = [];
      scroll.querySelectorAll('tbody tr').forEach((tr) => {
        const r = (tr as HTMLElement).getBoundingClientRect();
        rows.push({ top: r.top, bottom: r.bottom, height: r.height });
        if (r.height > 0 && r.top >= box.top - 1 && r.bottom <= box.bottom + 1) n++;
      });
      return { fullyVisible: n, box: { top: box.top, bottom: box.bottom, height: box.height }, rows };
    });
    // Floor, not the non-kiosk AC's >=4: today's measured kiosk-mode budget
    // at this viewport, with this suite's own CODES, is 2 (see the
    // test.describe comment above for the measurement and why it differs
    // from ut-docs#1314's review record). This guards against a FUTURE
    // regression below today's number, same spirit as the #213/#391 guards
    // this AC references. Independent review confirmed today's non-kiosk
    // floor at this same viewport/CODES also measures 2 -- this assertion's
    // job right now is catching a future drop below 2 in EITHER mode, not
    // distinguishing kiosk from non-kiosk (basket-item-name-width-1314.
    // spec.ts's long-name cases are what actually stress that difference).
    expect(
      measured.fullyVisible,
      `at least 2 line items fully visible without scrolling under body.kiosk at 1024x600 (today's measured floor) — got ${measured.fullyVisible}. ` +
        `box=${JSON.stringify(measured.box)} rows=${JSON.stringify(measured.rows)}`,
    ).toBeGreaterThanOrEqual(2);

    // Explicit symmetry with the 'osk-padded' class-injection test above
    // (line ~227) -- functionally unnecessary since Playwright gives every
    // test() a fresh page/context (confirmed: no storageState/context reuse
    // in playwright.config.ts), but keeps this file's two synthetic-class
    // tests reading the same way.
    await page.evaluate(() => document.body.classList.remove('kiosk'));
    await resetBasket(page);
    assertClean();
  });
});
