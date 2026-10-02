import { test, expect } from './fixtures';

// ut-docs#3359: the product owner (iPhone) rejected #3297's sideways-
// scrolling tables — "in the mobile view, grid view is not a good design,
// we shouldn't scroll to the left and right". At the phone tier (≤480px)
// every data table is a stacked card list instead (web/public/table-cards.js
// + the `table.t-cards` rules in app.css): the first cell is the card's
// title, every other cell carries its column header as a label, and a row
// with more than four labelled cells keeps the rest behind a More toggle.
// Desktop/kiosk widths keep the real table.
//
// /country-settings is the page under test: the migrations seed its
// built-in countries (so it always has rows), it has five labelled columns
// (so every row collapses), and its rows are tap-to-edit record rows — the
// More button must never open the record dialog underneath it.

const PAGE = '/country-settings';
const TABLE = '#country-settings-table';

test.describe('phone 360px: tables are card lists (ut-docs#3359)', () => {
  test.use({ viewport: { width: 360, height: 800 }, hasTouch: true, isMobile: true });

  test('rows render as labelled cards, never wider than the screen', async ({ page }) => {
    await page.goto(PAGE);
    const table = page.locator(TABLE);
    await expect(table).toHaveClass(/\bt-cards\b/);
    const rows = table.locator('tbody tr.country-row');
    expect(await rows.count()).toBeGreaterThan(0);

    const headers = await table.locator('thead th').evaluateAll((ths) => ths.map((th) => (th.textContent || '').replace(/\s+/g, ' ').trim()));
    const first = rows.first();
    const box = await first.boundingBox();
    expect(box!.width).toBeLessThan(360);
    expect(box!.x).toBeGreaterThanOrEqual(0);

    // Every labelled cell after the title shows its header as a ::before
    // label (checked on the cells visible before expanding).
    const labels = await first.evaluate((tr) => Array.from(tr.children)
      .filter((c) => (c as HTMLElement).offsetParent !== null && c.hasAttribute('data-label') && !c.classList.contains('t-cards-title'))
      .map((c) => ({ attr: c.getAttribute('data-label'), before: getComputedStyle(c, '::before').content })));
    expect(labels.length).toBeGreaterThan(0);
    for (const l of labels) {
      expect(headers).toContain(l.attr);
      expect(l.before).toBe(JSON.stringify(l.attr));
    }

    // The header row itself is visually hidden (still in the a11y tree).
    const head = await table.locator('thead').boundingBox();
    expect(head === null || head.height <= 1).toBe(true);
    // Nothing on the page scrolls sideways.
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(361);
  });

  test('More reveals the extra fields, flips aria-expanded, and never opens the record dialog', async ({ page }) => {
    await page.goto(PAGE);
    const row = page.locator(`${TABLE} tbody tr.country-row`).first();
    const more = row.locator('button.t-cards-toggle');
    await expect(more).toBeVisible();
    await expect(more).toHaveAttribute('aria-expanded', 'false');
    await expect(more).toHaveAttribute('type', 'button');
    // The caption is painted from data-caption (never row text — see the
    // filter test below) and is also the accessible name.
    const caption = () => more.evaluate((b) => ({ shown: getComputedStyle(b, '::before').content, name: b.getAttribute('aria-label') }));
    const before = await caption();
    expect(before.name!.length).toBeGreaterThan(0);
    expect(before.shown).toBe(JSON.stringify(before.name));

    const extra = row.locator('.t-cards-extra');
    expect(await extra.count()).toBeGreaterThan(0);
    await expect(extra.first()).toBeHidden();

    await more.click();
    await expect(more).toHaveAttribute('aria-expanded', 'true');
    await expect(extra.first()).toBeVisible();
    const after = await caption();
    expect(after.name).not.toBe(before.name);
    expect(after.shown).toBe(JSON.stringify(after.name));
    // The row is a tap-to-edit record row: the toggle must not open it.
    await expect(page.locator('#country-dialog')).not.toHaveAttribute('open', /.*/);

    // Keyboard: Enter on the focused toggle collapses it again.
    await more.focus();
    await page.keyboard.press('Enter');
    await expect(more).toHaveAttribute('aria-expanded', 'false');
    await expect(extra.first()).toBeHidden();
    await expect(page.locator('#country-dialog')).not.toHaveAttribute('open', /.*/);
  });

  test('the list filter still hides cards, and the empty-state row is a plain line', async ({ page }) => {
    await page.goto(PAGE);
    const empty = page.locator('#country-settings-no-results');
    await expect(empty).toBeHidden();
    const rows = page.locator(`${TABLE} tbody tr.country-row`);
    // The toggle's caption is not row text: filtering for it matches nothing.
    const filter = page.locator('[data-list-filter]');
    await filter.fill(await rows.first().locator('button.t-cards-toggle').evaluate((b) => b.getAttribute('aria-label') || ''));
    await expect(rows.first()).toBeHidden();
    await expect(empty).toBeVisible();
    await expect(empty).not.toHaveClass(/t-cards-row/);
    await filter.fill('');
    await expect(rows.first()).toBeVisible();
  });

  test('the sale basket keeps its own phone layout (data-cards="off")', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.basket table')).not.toHaveClass(/\bt-cards\b/);
  });
});

test.describe('phone 360px: htmx-swapped tables become cards too (ut-docs#3359)', () => {
  test.use({ viewport: { width: 360, height: 800 }, hasTouch: true, isMobile: true });

  test('every /reports tab: its tables are card lists and nothing scrolls sideways', async ({ page }) => {
    // One real sale so the report tabs have rows, not only empty states.
    await page.request.post('/api/pos/reset');
    await page.goto('/');
    await page.locator('.pos-container .btn-tile[hx-post="/api/pos/scan"]').first().click();
    await expect(page.locator('.basket-phonebar')).not.toHaveClass(/is-empty/);
    const paid = await page.request.post('/api/pos/tender', {
      headers: { 'Content-Type': 'application/json' },
      data: { payments: [{ method: 'cash', amount: 100000 }] },
    });
    expect(paid.ok(), await paid.text()).toBeTruthy();

    await page.goto('/reports');
    const tabs = page.locator('button[role="tab"][hx-get^="/ui/reports/tab/"]');
    const n = await tabs.count();
    expect(n).toBeGreaterThan(0);
    let carded = 0;
    for (let i = 0; i < n; i++) {
      const tab = tabs.nth(i);
      const id = await tab.getAttribute('id');
      await Promise.all([page.waitForResponse((r) => r.url().includes('/ui/reports/tab/')), tab.click()]);
      await page.waitForTimeout(150);
      const r = await page.evaluate(() => {
        const tables = Array.from(document.querySelectorAll('#report-tab-panel table'))
          .filter((t) => t.tHead && !t.parentElement!.closest('table'));
        return {
          plain: tables.filter((t) => !t.classList.contains('t-cards')).length,
          cards: tables.length,
          sw: document.documentElement.scrollWidth,
        };
      });
      expect(r.plain, `${id}: a swapped-in table was not turned into cards`).toBe(0);
      expect(r.sw, `${id}: page wider than the screen`).toBeLessThanOrEqual(361);
      carded += r.cards;
    }
    expect(carded).toBeGreaterThan(0);
    await page.request.post('/api/pos/reset');
  });
});

test.describe('desktop 1280px: the real table stays (ut-docs#3359)', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('display: table, header visible, no More button shown', async ({ page }) => {
    await page.goto(PAGE);
    const table = page.locator(TABLE);
    await expect(table).toHaveClass(/\bt-cards\b/);
    expect(await table.evaluate((t) => getComputedStyle(t).display)).toBe('table');
    await expect(table.locator('thead th').first()).toBeVisible();
    await expect(table.locator('button.t-cards-toggle').first()).toBeHidden();
    // Every cell (including the "extra" ones) is visible on desktop.
    await expect(table.locator('tbody tr.country-row').first().locator('.t-cards-extra').first()).toBeVisible();
  });
});
