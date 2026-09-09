import { test, expect } from './fixtures';

// ADR-0088 / ut-docs#1904 — a `layout` plugin restructures the Menu.
//
// This spec drives the REAL plugins/layout-salon plugin, installed into a
// dedicated till by run-till-layout.sh (see e2e/seed_layout_salon). Its
// amendments: hide /tables, hide /kitchen-stations, and re-label /items to
// "Services" with the scissors icon at order 50.
//
// The Go tests cover resolution and refusal in isolation. What only a
// driven run can prove is the claim the whole hide design rests on —
// Decision D's "hiding removes a TILE, never a ROUTE" — because that is a
// statement about the router and the renderer disagreeing on purpose, and
// a rendered-HTML assertion cannot see the router at all.

const KIOSK = { width: 1024, height: 600 }; // the 10-inch till target

test.beforeEach(async ({ page }) => {
  await page.setViewportSize(KIOSK);
});

test('the salon layout hides its tiles from the Menu but leaves every route reachable', async ({ page, request }) => {
  await page.goto('/menu');

  const hrefs = await page.locator('a.menu-tile').evaluateAll((els) =>
    els.map((e) => e.getAttribute('href')),
  );

  // Hidden by the plugin.
  expect(hrefs).not.toContain('/tables');
  expect(hrefs).not.toContain('/kitchen-stations');

  // Guard against passing for the wrong reason (an empty or broken menu):
  // the untouched tiles must still be there.
  expect(hrefs).toContain('/items');
  expect(hrefs).toContain('/settings');
  expect(hrefs).toContain('/help');

  // Decision D: the ROUTE is untouched. A hidden destination is one URL
  // away, and must not 404 — this is the assertion that distinguishes
  // "hidden from the menu" from "removed from the product".
  for (const route of ['/tables', '/kitchen-stations']) {
    const res = await request.get(route);
    expect(res.status(), `${route} must stay reachable while hidden`).toBe(200);
  }
});

test('the re-labelled tile shows the plugin string and its icon, and is a real touch target', async ({ page }) => {
  await page.goto('/menu');

  const items = page.locator('a.menu-tile[href="/items"]');
  await expect(items).toHaveCount(1);

  // The plugin's own locale file supplies this, via syncLocales — not the
  // core label, and not the raw key (Decision G's fallback would render
  // "Artikel"/"Items", and a broken lookup would render the key itself).
  await expect(items.locator('.menu-label')).toHaveText('Services');

  // Decision H: the icon comes from core's set by NAME. scissors is the
  // salon plugin's choice; the generic puzzle fallback would mean the
  // name never resolved.
  await expect(items.locator('.menu-ico svg')).toHaveCount(1);

  // Geometry, not just markup (the ut-docs#300 lesson, and the
  // sale-screen-213 precedent): the tile must be a genuine hit-test
  // target at the kiosk size, not a zero-height row or an overlapped one.
  const box = await items.boundingBox();
  expect(box, 'the re-labelled tile must be laid out').not.toBeNull();
  expect(box!.height).toBeGreaterThanOrEqual(44);
  expect(box!.width).toBeGreaterThanOrEqual(44);
  const hit = await page.evaluate(([x, y]) => {
    const el = document.elementFromPoint(x, y);
    return el ? (el.closest('a.menu-tile') as HTMLAnchorElement | null)?.getAttribute('href') ?? null : null;
  }, [box!.x + box!.width / 2, box!.y + box!.height / 2] as [number, number]);
  expect(hit, 'tapping the tile centre must hit the tile itself').toBe('/items');

  // The re-label is not allowed to smash the layout at the longest locale.
  // German is the one market with a real pilot merchant.
  await page.goto('/menu?lang=de');
  const deTiles = page.locator('a.menu-tile');
  await expect(deTiles.first()).toBeVisible();
  const overflow = await page.evaluate(() => {
    const el = document.scrollingElement!;
    return el.scrollWidth - el.clientWidth;
  });
  expect(overflow, 'the menu must not scroll horizontally at 1024px in German').toBeLessThanOrEqual(1);
});

test('a merchant can find what the plugin hid, and restore it, without uninstalling', async ({ page }) => {
  await page.goto('/settings/menu');

  const rows = page.locator('table.table tbody tr');
  await expect(rows).toHaveCount(2);

  // Decision D requires the surface to NAME the plugin responsible — a
  // list of hidden things with no attribution does not tell a merchant
  // what to uninstall.
  await expect(page.locator('table.table')).toContainText('Salon layout');

  const tablesRow = rows.filter({ has: page.locator('input[name="key"][value="/tables"]') });
  await expect(tablesRow).toHaveCount(1);

  // Restore it.
  await tablesRow.getByRole('button').click();
  await page.waitForLoadState('networkidle');

  // The tile is back on the Menu...
  await page.goto('/menu');
  const hrefs = await page.locator('a.menu-tile').evaluateAll((els) =>
    els.map((e) => e.getAttribute('href')),
  );
  expect(hrefs).toContain('/tables');
  // ...and the plugin's OTHER hide is untouched — restore is per-entry,
  // not "turn the whole plugin off".
  expect(hrefs).not.toContain('/kitchen-stations');

  // The surface reflects the new state rather than silently forgetting it.
  await page.goto('/settings/menu');
  const restoredRow = page
    .locator('table.table tbody tr')
    .filter({ has: page.locator('input[name="key"][value="/tables"]') });
  await expect(restoredRow).toContainText('Restored');
});

// ut-docs#300's lesson, applied: the screenshot existed and nobody looked.
// Looking at this page found the "Open" link rendered inline against the
// destination name, so a merchant read "Tables & floor plan Open" as one
// string. Fixed by putting it on its own line — and pinned here as real
// geometry, because a visual defect fixed without a geometric assertion
// comes back.
test('the Open link is a separate line from the destination name, not run into it', async ({ page }) => {
  await page.goto('/settings/menu');
  const row = page
    .locator('table.table tbody tr')
    .filter({ has: page.locator('input[name="key"][value="/tables"]') });
  const dest = row.locator('.menulayout-dest');
  const goto = row.locator('.menulayout-goto');
  await expect(dest).toBeVisible();
  await expect(goto).toBeVisible();

  const d = await dest.boundingBox();
  const g = await goto.boundingBox();
  expect(d, 'destination name must be laid out').not.toBeNull();
  expect(g, 'Open link must be laid out').not.toBeNull();

  // The link starts below the label's baseline rather than beside it. This
  // is what "not run into it" means geometrically — a shared line would put
  // g.y within the label's own vertical band.
  expect(
    g!.y,
    `the Open link must sit below the destination name, not on the same line (label y=${d!.y} h=${d!.height}, link y=${g!.y})`,
  ).toBeGreaterThanOrEqual(d!.y + d!.height);

  // And it must still be a usable target, not a 2px sliver.
  expect(g!.height).toBeGreaterThan(8);
});

test('the hidden-tiles surface survives RTL without physical-property breakage', async ({ page }) => {
  await page.goto('/settings/menu?lang=fa');
  await expect(page.locator('table.table')).toBeVisible();
  const dir = await page.evaluate(() => document.documentElement.getAttribute('dir'));
  expect(dir).toBe('rtl');
  const overflow = await page.evaluate(() => {
    const el = document.scrollingElement!;
    return el.scrollWidth - el.clientWidth;
  });
  expect(overflow, 'the hidden-tiles page must not scroll horizontally in fa/RTL').toBeLessThanOrEqual(1);
});
