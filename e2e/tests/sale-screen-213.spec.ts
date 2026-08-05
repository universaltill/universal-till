import { test, expect } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#213: the basket is a full-height first-class panel (>=4 line
// items visible at 1280x800 with no scrolling), carries an always-visible
// item-count badge, the nav logo is legible (rem-sized, on the .logo white
// plate — the separate light-variant asset was retired in ut-docs#290),
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
  await page.getByRole('textbox').first().fill(code);
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

    // Fully-visible rows inside .basket-scroll's box, not merely in-DOM.
    const fullyVisible = await page.evaluate(() => {
      const scroll = document.querySelector('.basket-scroll') as HTMLElement;
      const box = scroll.getBoundingClientRect();
      let n = 0;
      scroll.querySelectorAll('tbody tr').forEach((tr) => {
        const r = (tr as HTMLElement).getBoundingClientRect();
        if (r.height > 0 && r.top >= box.top - 1 && r.bottom <= box.bottom + 1) n++;
      });
      return n;
    });
    expect(fullyVisible, 'at least 4 line items fully visible without scrolling').toBeGreaterThanOrEqual(4);

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

    // Remove the second line entirely. The ✕ lives INSIDE the re-swapped
    // #basket: vendored htmx 1.9.12 binds listeners on fresh content a
    // settle-tick after it appears, and a click landing in that window is
    // silently dropped (reproduced 25/25 with a tight loop; exists on main
    // too — pre-existing, not a #213 regression). Wait for the button to be
    // htmx-bound before clicking.
    await page.waitForFunction(() => {
      const btns = document.querySelectorAll('.basket .btn-x');
      const b = btns[btns.length - 1] as any;
      return !!(b && b['htmx-internal-data']);
    });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/remove')),
      page.locator('.basket .btn-x').last().click(),
    ]);
    await expect(badge).toHaveText('2');

    await resetBasket(page);
    await expect(page.locator('[data-testid="basket-count"]')).toHaveText('0');
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

  test('nav logo renders legibly large with the canonical asset', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/');
    const logo = page.locator('.nav .logo img');
    await expect(logo).toHaveAttribute('src', /unitill-logo\.svg/);
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
