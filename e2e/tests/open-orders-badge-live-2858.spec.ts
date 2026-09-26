import { test, expect } from './fixtures';
import { watchConsole, clearAllHeldSales, listHeldSaleIds } from './helpers';

// ut-docs#2858 (owner, satellite + main till): an order held on the Pi was
// paid on the main till, but the Pi's Open orders badge kept its red count
// until the sale screen was reloaded -- the badge only re-fetched on the
// held-changed its OWN document's hold/resume answered with.
//
// Every held_sales link nudge (sent by the main till, received by a
// satellite) now moves the till's held generation; each badge render seeds a
// hidden #open-orders-watch span out of band, which polls
// GET /ui/open-orders-badge/watch every 3 s and gets HX-Trigger:
// held-changed (on a 204) when the generation moved. The Go tests cover the
// cross-till link end to end; this spec proves the browser half -- the OOB
// seed, the poll, the trigger on a 204 -- with page.request standing in for
// the other till: its hold/resume never answers THIS document.

test.describe('ut-docs#2858 Open orders badge follows changes made elsewhere', () => {
  // Waits out the real 3 s poll, twice.
  test.describe.configure({ timeout: 60_000 });

  test.beforeEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await clearAllHeldSales(page).catch(() => {});
    await page.request.post('/api/pos/reset').catch(() => {});
  });

  test('an order parked and then resumed by another client moves this screen\'s badge, with no reload', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/');
    await clearAllHeldSales(page);
    await page.reload();

    const badge = page.getByTestId('open-orders-badge');
    await expect(badge).toHaveAttribute('data-count', '0');
    // The first badge render swapped its watcher in over the placeholder.
    const watch = page.locator('#open-orders-watch');
    await expect(watch).toHaveAttribute('hx-get', /\/ui\/open-orders-badge\/watch\?v=/);
    await expect(watch).toBeHidden();

    let navigations = 0;
    page.on('framenavigated', (f) => { if (f === page.mainFrame()) navigations++; });

    // Another client parks an order: scan + hold, none of it through this
    // document.
    const scan = await page.request.post('/api/pos/scan', { form: { code: '5000000000012' } });
    expect(scan.ok(), 'scan elsewhere').toBe(true);
    const hold = await page.request.post('/api/pos/hold', { form: { label: `Elsewhere 2858` } });
    expect(hold.ok(), 'hold elsewhere').toBe(true);
    await expect(badge, 'badge counts an order parked elsewhere').toHaveText('1', { timeout: 10_000 });
    await expect(badge).toBeVisible();

    // ...and resumes it (the main till taking the payment).
    const ids = await listHeldSaleIds(page);
    expect(ids, 'exactly the order parked above').toHaveLength(1);
    const id = ids[0];
    const resume = await page.request.post('/api/pos/resume', { form: { id } });
    expect(resume.ok(), 'resume elsewhere').toBe(true);
    await expect(badge, 'badge clears once the order was resolved elsewhere').toBeHidden({ timeout: 10_000 });
    await expect(badge).toHaveAttribute('data-count', '0');

    expect(navigations, 'no reload').toBe(0);
    assertClean();
  });
});
