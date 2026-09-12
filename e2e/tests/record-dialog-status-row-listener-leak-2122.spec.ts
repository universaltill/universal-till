import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2122: record-dialog.js's bindStatusRow() bound a fresh pair of
// `window` online/offline listeners to every [data-record-dialog-conn]
// element it saw, guarded only by a bound-flag on that ELEMENT. That guard
// is worthless against the real re-render path this partial actually goes
// through: the /items rail (ut-docs#1950, web/ui/partials/items_rail.html)
// hx-gets each section into #items-panel in place, so navigating away from
// /categories and back destroys the old status-row element and htmx
// fragments in a BRAND NEW one — which has never seen the bound-flag and
// gets a fresh listener pair, while the previous element's pair (and the
// detached element it closed over) is never released. A till left open
// for a shift, with an operator tabbing between /items rail sections many
// times, would accumulate one leaked listener pair per visit to a section
// carrying this dialog (today: /categories).
//
// Reproduced here via the real production path — clicking between real
// rail sections, not a synthetic DOM/event simulation — and measured via
// the Chrome DevTools Protocol (`DOMDebugger.getEventListeners`), the only
// way to actually count listeners registered on `window` from outside the
// script that added them.

async function countWindowListeners(page: Page, type: 'online' | 'offline'): Promise<number> {
  const session = await page.context().newCDPSession(page);
  try {
    // Runtime.evaluate (not page.evaluateHandle) so the RemoteObject's
    // objectId comes straight from the CDP response — no reliance on
    // Playwright's own JSHandle internals to dig one out.
    const evalResult = await session.send('Runtime.evaluate', { expression: 'window' });
    const objectId = evalResult.result.objectId;
    if (!objectId) throw new Error('could not resolve a remote objectId for window');
    const { listeners } = await session.send('DOMDebugger.getEventListeners', { objectId });
    return listeners.filter((l: { type: string }) => l.type === type).length;
  } finally {
    await session.detach();
  }
}

test.describe('record-dialog.js status-row listener leak (ut-docs#2122)', () => {
  test('repeated /items-rail visits to /categories never grow the window online/offline listener count', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/items');

    const categoriesLink = page.locator('a.items-row[href="/categories"]');
    const inventoryLink = page.locator('a.items-row[href="/inventory"]');

    // `window` already carries its own baseline of online/offline
    // listeners unrelated to this dialog (base.html's #sb-conn chip,
    // app.js's offline-flag and visibility handlers) — the fix's contract
    // is "revisiting /categories adds no MORE listeners than the first
    // visit did", not "exactly one, period". So take the baseline right
    // after the FIRST real visit, then assert it never grows.
    await categoriesLink.click();
    await expect(page.locator('#categories-new')).toBeVisible();
    await expect(page.locator('[data-record-dialog-conn]')).toHaveCount(1);
    const baselineOnline = await countWindowListeners(page, 'online');
    const baselineOffline = await countWindowListeners(page, 'offline');

    // Cycle through the real rail navigation several more times — each
    // visit to /categories re-renders record_dialog.html's status row from
    // scratch via a genuine htmx swap of #items-panel, exactly the path
    // #2122 describes, no full page reload in between (hx-push-url, not a
    // real navigation).
    for (let i = 0; i < 4; i++) {
      await inventoryLink.click();
      await expect(page.locator('#stock-dialog-open')).toBeVisible();
      // The status-row element only exists on the /categories fragment —
      // confirms this cycle is genuinely destroying and recreating it,
      // not just leaving it in place hidden.
      await expect(page.locator('[data-record-dialog-conn]')).toHaveCount(0);

      await categoriesLink.click();
      await expect(page.locator('#categories-new')).toBeVisible();
      await expect(page.locator('[data-record-dialog-conn]')).toHaveCount(1);
    }

    // Fixed behaviour: the count stays flat at the baseline however many
    // times the section carrying the dialog was revisited. Before the fix
    // this grew by one for every extra visit to /categories (4 more here),
    // each still holding a reference to that visit's now-detached element.
    expect(await countWindowListeners(page, 'online')).toBe(baselineOnline);
    expect(await countWindowListeners(page, 'offline')).toBe(baselineOffline);

    assertClean();
  });
});
