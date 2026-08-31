import { test, expect } from './fixtures';

// ut-docs#1337 (product owner, live report): add items in Dine-in, switch to
// Takeaway, add more items, delete some -- items sometimes randomly
// disappear and/or stale ones reappear.
//
// Root cause: every #basket-mutating trigger across the sale screen
// independently POSTs and hx-swap="outerHTML"s the SAME `#basket` element,
// with zero `hx-sync` anywhere. htmx resolves an element's hx-target to a
// concrete DOM node once, at the moment the request is SENT -- not again
// when the response lands. If two triggers fire close together (both still
// targeting the same not-yet-replaced `#basket` node) and their responses
// arrive out of order:
//   1. Whichever response lands FIRST does a real, visible outerHTML swap of
//      the shared original node -- fine on its own.
//   2. But that swap detaches the original node from the document. The
//      OTHER response's target reference is that same original (now
//      detached) node -- so when it finally arrives, applying its swap to a
//      node no longer in the document has NO VISIBLE EFFECT. Its content
//      (a newly-added item, a removed line, ...) never renders, even though
//      the server processed it correctly.
// If the older-fired request happens to be the one whose response lands
// first, the operator's LATER action's effect silently never appears on
// screen -- exactly the "items sometimes randomly disappear" symptom.
//
// This spec forces that exact interleaving DETERMINISTICALLY. A real
// network race (two browser-timed requests genuinely crossing in flight) is
// not reliably reproducible under Playwright's normal await-each-step
// idiom, so instead this holds BOTH requests' RESPONSE DELIVERY under
// explicit control via route interception (the real server-side request
// still happens immediately via `route.fetch()` -- only the browser's
// receipt of the response is held back), then releases them in a chosen
// order to prove the mechanism rather than hope for it.
test.describe('ut-docs#1337 basket hx-sync race', () => {
  // Server-side reset regardless of UI state, ALWAYS -- specs in this
  // project share ONE till server, and a basket left non-empty cascades
  // into whichever spec runs next (e2e/README.md rule;
  // sale-screen-213.spec.ts's own comment documents an earlier real
  // instance of exactly this cascade).
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  test('an older-fired order-type response that lands first must not swallow a newer scan (ut-docs#1337)', async ({ page }) => {
    await page.goto('/');

    // Baseline: one item already in the basket, Dine-in (default).
    await page.getByRole('textbox').first().fill('5000000000012');
    await page.locator('.scan-row button[type=submit]').click();
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    // Hold BOTH responses' delivery under our own explicit control. Each
    // route handler performs the REAL request immediately (route.fetch())
    // -- the server-side mutation happens right away, same as any real
    // request -- and only withholds delivering the response to the page
    // until we release it, letting this test choose the delivery order
    // deterministically instead of hoping for a real race to land right.
    function holdable() {
      let release: () => void = () => {};
      const held = new Promise<void>((resolve) => {
        release = resolve;
      });
      let committed: () => void = () => {};
      const committedPromise = new Promise<void>((resolve) => {
        committed = resolve;
      });
      return { held, release: () => release(), committed: () => committed(), committedPromise };
    }
    const orderType = holdable();
    const scan = holdable();

    await page.route('**/api/pos/order-type', async (route) => {
      const response = await route.fetch();
      orderType.committed();
      await orderType.held;
      try {
        await route.fulfill({ response });
      } catch {
        // Expected once the fix is in place: hx-sync aborts htmx's
        // underlying browser-side request before we ever get to deliver
        // it, so fulfilling an already-aborted request throws. That's the
        // fix working, not a test bug.
      }
    });
    await page.route('**/api/pos/scan', async (route) => {
      // Only the SECOND scan (the Pepsi one fired below) needs holding;
      // let any other /api/pos/scan traffic (none expected here) through
      // untouched so this stays scoped to the interleaving under test.
      const postData = route.request().postData() || '';
      if (!postData.includes('5000000000029')) {
        await route.continue();
        return;
      }
      const response = await route.fetch();
      scan.committed();
      await scan.held;
      try {
        await route.fulfill({ response });
      } catch {
        // See the order-type handler's comment above.
      }
    });

    // Fire both actions close together, WITHOUT awaiting either's swap --
    // exactly how an operator tapping "Takeaway" then quickly adding
    // another item behaves. Both are dispatched while the ORIGINAL #basket
    // node (from the Coca-Cola scan above) is still the current one, so
    // htmx resolves both requests' hx-target to that SAME node.
    // 2026-08-31 (ut-docs#1379): back to an explicit two-segment control
    // (the single switch this test briefly targeted after ut-docs#1332
    // turned out ambiguous in practice) -- click the Takeaway segment
    // directly by testid, same original intent (Dine-in -> Takeaway).
    await page.locator('[data-testid="order-type-takeaway"]').click();
    await page.getByRole('textbox').first().fill('5000000000029');
    await page.locator('.scan-row button[type=submit]').click();

    // Both real requests have now genuinely round-tripped the server (both
    // mutations are committed) -- only delivery to the browser is held.
    await orderType.committedPromise;
    await scan.committedPromise;

    // Release the OLDER-fired request's response FIRST. Pre-fix, this is
    // exactly the ordering the live report describes: an earlier action's
    // response wins the delivery race.
    orderType.release();
    // Give it a real chance to land and swap before releasing the other.
    await page.waitForTimeout(500);

    // Now release the NEWER-fired (scan) response.
    scan.release();
    await page.waitForTimeout(500);

    // The fix must hold: the final DOM reflects BOTH the order-type switch
    // AND the newly-scanned Pepsi. Pre-fix, this is exactly where Pepsi
    // silently never appears -- its response's target had already gone
    // stale by the time it landed.
    await expect(page.locator('#basket')).toContainText('Coca-Cola');
    await expect(page.locator('#basket')).toContainText('Pepsi');
  });
});
