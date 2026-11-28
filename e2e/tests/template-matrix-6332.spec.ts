import { test, expect, type Page, type APIRequestContext } from './fixtures';
import { watchConsole } from './helpers';

// ut-docs#6332 / ADR-0190: the shop-type template matrix, driven in a real
// browser against a real till (run-till-templates.sh, project `templates`).
//
// The Go tests prove resolution, diffing and the endpoints in isolation. What
// only a browser can prove: htmx swaps of the pick/preview panel, Alpine tile
// selection, the settings landing/More settings views that are JS show/hide
// over one server render, and that a hidden tile is still a reachable route.
//
// What is real here: the server, its migrations, the shipped template JSON
// files, the real POST /api/settings/shop-type. What is EMULATED: a shop that
// predates templates (shop.type seeded with no ledger by seed_template_shop;
// there is no older binary to age one for real) and the touch viewport
// (Chromium at 1024x600, not a Pi or tablet: owner-device check, not claimed).
//
// One till, one serial file: the tests build on the state the previous one left
// (legacy shop -> tidied -> every type in turn), so they are deliberately
// ordered. The till starts as a RETAIL shop in legacy mode.

// Tests run in file order in one worker (project workers: 1) but are NOT `serial`: a failing
// test must not skip the rest of the matrix. The legacy block's order matters; later blocks
// set the type they need themselves and compute their baseline from the `other` template.

const KIOSK = { width: 1024, height: 600 };

// Expected Menu tiles each shipped template hides, taken from
// internal/shoptemplate/templates/*.json ("advanced" on a menu key). A tile the
// demo till does not render at all is dropped from the check by `present`.
// Hospitality extends cafe and promotes /tables and /kitchen-stations.
const MATRIX: Record<string, { label: string; hidden: string[]; shown: string[] }> = {
  cafe: {
    label: 'Cafe or takeaway counter',
    hidden: ['/tables', '/kitchen-stations', '/bluetooth-devices', '/vouchers'],
    shown: [],
  },
  hospitality: {
    label: 'Restaurant or bar',
    hidden: ['/bluetooth-devices', '/vouchers'],
    shown: ['/tables', '/kitchen-stations'],
  },
  retail: {
    label: 'Retail shop',
    hidden: ['/tables', '/kitchen-stations', '/bluetooth-devices', '/open-orders'],
    shown: [],
  },
  service: {
    label: 'Barber, salon or service',
    hidden: ['/tables', '/kitchen-stations', '/bluetooth-devices'],
    shown: [],
  },
  market_stall: {
    label: 'Market stall or pop-up',
    hidden: ['/tables', '/kitchen-stations', '/bluetooth-devices', '/open-orders', '/vouchers', '/locations', '/registers'],
    shown: [],
  },
  other: {
    label: 'Something else',
    hidden: ['/tables', '/kitchen-stations'],
    shown: [],
  },
};

// Tiles that no template may ever hide (protected destinations).
const PROTECTED = ['/settings', '/help'];

// What an untidied shop shows. Legacy mode is only observable until the first change, so a
// later test derives it from `other`, which hides only the tiles listed below and no settings.
const OTHER_HIDES = ['/tables', '/kitchen-stations'];
let baselineMenu: string[] = [];
let baselineSettingsRows = 0;
async function ensureBaseline(page: Page, request: APIRequestContext) {
  if (baselineMenu.length && baselineSettingsRows) return;
  await setType(request, 'other');
  baselineMenu = [...(await menuHrefs(page)), ...OTHER_HIDES].sort();
  await page.goto('/settings');
  baselineSettingsRows = await settingsRowCount(page);
}

async function menuHrefs(page: Page): Promise<string[]> {
  await page.goto('/menu');
  await expect(page.locator('a.menu-tile').first()).toBeVisible();
  return page.locator('a.menu-tile').evaluateAll((els) => els.map((e) => e.getAttribute('href') || ''));
}

async function setType(request: APIRequestContext, to: string) {
  const res = await request.post('/api/settings/shop-type', { form: { to } });
  expect(res.ok(), `POST /api/settings/shop-type to=${to} must succeed (status ${res.status()})`).toBe(true);
}

async function settingsRowCount(page: Page): Promise<number> {
  return page.locator('#settings-nav-index li').count();
}

// Cancel in the preview/pick panel. Soft-asserts that it really closes the panel: a
// click that raises a JS error and does nothing is a defect to report, not a reason to
// hide every later assertion, so on failure the helper reloads Settings and goes on.
async function cancelPanel(page: Page, within: string) {
  await page.locator(within).getByRole('button', { name: 'Cancel' }).click();
  const closed = await page.locator(within).waitFor({ state: 'detached', timeout: 3000 }).then(() => true, () => false);
  expect.soft(closed, 'Cancel closes the panel and shows the Shop type card again').toBe(true);
  if (!closed) await page.goto('/settings#settings-shop-type');
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize(KIOSK);
});

test.describe('existing shop (legacy, no template yet)', () => {
  test('menu and settings are unchanged, and the one-time offer card appears on Settings only', async ({ page }) => {
    const assertClean = watchConsole(page);
    baselineMenu = (await menuHrefs(page)).sort();
    // Legacy = nothing tidied: the tiles a template would hide are all there.
    expect(baselineMenu).toContain('/tables');
    expect(baselineMenu).toContain('/kitchen-stations');
    await expect(page.locator('[data-testid="template-offer"]')).toHaveCount(0); // never on the menu
    await expect(page.locator('[data-testid="shop-type-undo"]')).toHaveCount(0);

    await page.goto('/settings');
    baselineSettingsRows = await settingsRowCount(page);
    expect(baselineSettingsRows).toBeGreaterThan(20);
    const offer = page.locator('[data-testid="template-offer"]');
    await expect(offer).toBeVisible();
    await expect(offer).toContainText('Tidy your menu for your shop type?');
    await expect(offer).toContainText('Your shop type: Retail shop');
    // Never a dialog, never blocking: it is a card in the page.
    await expect(page.locator('dialog[open], [role="dialog"]:visible')).toHaveCount(0);
    for (const b of await offer.locator('a.btn, button').all()) {
      const box = await b.boundingBox();
      expect(box!.height, 'offer actions are real touch targets').toBeGreaterThanOrEqual(44);
    }
    await page.screenshot({ path: test.info().outputPath('offer-card-1024x600.png') });
    assertClean();
  });

  test('"See what changes" only previews: nothing is written until Confirm, Cancel leaves it all alone', async ({ page }) => {
    const before = (await menuHrefs(page)).sort();
    await page.goto('/settings');
    await page.locator('[data-testid="template-offer"]').getByText('See what changes').click();
    const preview = page.locator('[data-testid="shop-type-preview"]');
    await expect(preview).toBeVisible();
    await expect(preview.locator('#shop-type-preview-title')).toContainText('Retail shop');
    // Retail hides Tables and Kitchen stations from the menu; the preview says so, and says nothing is lost.
    await expect(preview).toContainText('Will be tucked away');
    await expect(preview).toContainText('Nothing about payments, tax, staff or PINs is ever changed by a shop type.');
    // Value suggestions are opt-in: never pre-ticked.
    for (const cb of await preview.locator('input[name="apply_values[]"]').all()) {
      await expect(cb).not.toBeChecked();
    }
    await page.screenshot({ path: test.info().outputPath('legacy-preview-1024x600.png') });

    // Pinned action bar stays on screen at the kiosk height even with a long body.
    const confirm = preview.getByRole('button', { name: 'Change shop type' });
    // Soft: a failure here is a real UX defect to report (UX 11), and must not hide the rest of the flow.
    await expect.soft(confirm, 'pinned Confirm bar is on screen at 1024x600 without scrolling').toBeInViewport();

    await cancelPanel(page, '[data-testid="shop-type-preview"]');
    expect((await menuHrefs(page)).sort(), 'Cancel changed nothing').toEqual(before);
  });

  test('"Not now" hides the card and keeps it hidden on reload; nothing about the shop changes', async ({ page }) => {
    const before = (await menuHrefs(page)).sort();
    await page.goto('/settings');
    const offer = page.locator('[data-testid="template-offer"]');
    await offer.getByRole('button', { name: /Not now/ }).click();
    await expect(offer).toHaveCount(0);
    await page.reload();
    await expect(page.locator('[data-testid="template-offer"]')).toHaveCount(0);
    expect((await menuHrefs(page)).sort()).toEqual(before);
  });

  test('the Shop type card still offers the tidy-up for a legacy shop, and accepting it turns templates on', async ({ page }) => {
    const assertClean = watchConsole(page);
    const before = await menuHrefs(page);
    await page.goto('/settings#settings-shop-type');
    const tidy = page.locator('#shop-type-tidy');
    await expect(tidy).toContainText('Tidy my menu for');
    await tidy.click();
    const preview = page.locator('[data-testid="shop-type-preview"]');
    await expect(preview).toBeVisible();
    await preview.getByRole('button', { name: 'Change shop type' }).click();

    // Success = redirect to Settings with the 7-day undo strip.
    await expect(page).toHaveURL(/\/settings/);
    await expect(page.locator('[data-testid="shop-type-undo"]')).toContainText('Shop type changed to Retail shop.');

    const hrefs = await menuHrefs(page);
    for (const h of MATRIX.retail.hidden.filter((h) => before.includes(h))) {
      expect(hrefs, `${h} is tucked away for retail`).not.toContain(h);
    }
    // Hiding removes a TILE, never a ROUTE.
    for (const route of ['/tables', '/kitchen-stations']) {
      const res = await page.request.get(route);
      expect(res.status(), `${route} stays reachable while hidden`).toBe(200);
    }
    // Manager-only one-line note + the way back, on the menu.
    await expect(page.locator('main')).toContainText('Some tiles are tucked away because they are rarely used in a shop like yours.');
    await expect(page.getByRole('link', { name: 'Show all tiles' })).toHaveAttribute('href', '/settings/menu');
    assertClean();
  });
});

test.describe('the six shop types', () => {
  for (const [type, want] of Object.entries(MATRIX)) {
    test(`${type}: menu, settings landing, More settings`, async ({ page, request }) => {
      const assertClean = watchConsole(page);
      await ensureBaseline(page, request);
      await setType(request, type);

      // --- Menu launcher
      const hrefs = await menuHrefs(page);
      expect(hrefs.length, 'the menu must not be emptied').toBeGreaterThan(3);
      for (const h of PROTECTED) expect(hrefs, `${h} is never hidden`).toContain(h);
      for (const h of want.hidden.filter((h) => baselineMenu.includes(h))) {
        expect(hrefs, `${type} tucks ${h} away`).not.toContain(h);
      }
      for (const h of want.shown.filter((h) => baselineMenu.includes(h))) {
        expect(hrefs, `${type} shows ${h}`).toContain(h);
      }
      // Nothing outside the template's own list disappeared.
      const expectedGone = new Set(want.hidden);
      for (const h of baselineMenu.filter((h) => !expectedGone.has(h))) {
        expect(hrefs, `${type} must not hide ${h}`).toContain(h);
      }
      await page.screenshot({ path: test.info().outputPath(`menu-${type}-1024x600.png`) });

      // --- Settings landing
      await page.goto('/settings');
      const footer = page.locator('[data-testid="settings-footer"]');
      await expect(footer).toContainText(`Set up for: ${want.label}`);
      await expect(footer).toContainText('Cannot find a setting? Search, or open More settings.');
      await expect(page.locator('#shop-type-panel')).toContainText(want.label);
      const tiles = page.locator('#settings-home .settings-cat-tile');
      await expect(tiles.last()).toHaveAttribute('data-cat', 'advanced');
      await expect(tiles.last()).toContainText('More settings');
      await page.screenshot({ path: test.info().outputPath(`settings-${type}-1024x600.png`), fullPage: true });

      // No setting row is ever lost to a template.
      expect(await settingsRowCount(page), `${type}: every settings row still exists`).toBe(baselineSettingsRows);
      const tucked = await page.locator('#settings-nav-index li[data-tag="tucked"]').count();
      if (type === 'other') {
        expect(tucked, 'other tucks no settings').toBe(0);
      } else {
        expect(tucked, `${type} tucks some settings into More settings`).toBeGreaterThan(0);
      }

      // --- More settings: everything is there, tucked ones are named.
      await tiles.last().click();
      await expect(page).toHaveURL(/#cat-advanced$/);
      await expect(page.locator('#settings-cat-title')).toHaveText('More settings');
      if (type !== 'other') {
        await expect(page.locator('#settings-more-intro')).toBeVisible();
        await expect(page.locator('#settings-more-intro')).toContainText('Every setting is here.');
      }
      await page.screenshot({ path: test.info().outputPath(`more-settings-${type}-1024x600.png`) });

      // A demoted card is still deep-linkable (a bookmark or a help link keeps working).
      if (type === 'market_stall') {
        await page.goto('/settings#settings-order-no');
        await expect(page.locator('#settings-order-no')).toBeVisible();
      }
      assertClean();
    });
  }

  test('longest locale (German) and RTL (Persian) menus do not scroll sideways on a tidied shop', async ({ page, request }) => {
    await setType(request, 'hospitality');
    for (const lang of ['de', 'fa']) {
      await page.goto(`/menu?lang=${lang}`);
      await expect(page.locator('a.menu-tile').first()).toBeVisible();
      const overflow = await page.evaluate(() => document.scrollingElement!.scrollWidth - document.scrollingElement!.clientWidth);
      expect(overflow, `menu at 1024px in ${lang}`).toBeLessThanOrEqual(1);
      await page.goto(`/settings?lang=${lang}`);
      const o2 = await page.evaluate(() => document.scrollingElement!.scrollWidth - document.scrollingElement!.clientWidth);
      expect(o2, `settings at 1024px in ${lang}`).toBeLessThanOrEqual(1);
      await page.screenshot({ path: test.info().outputPath(`settings-hospitality-${lang}.png`) });
    }
    await page.goto('/menu?lang=en');
  });
});

test.describe('changing the shop type: pick, preview, confirm, undo', () => {
  test('pick a tile, preview, Cancel changes nothing; confirm applies; Undo puts it back', async ({ page, request }) => {
    const assertClean = watchConsole(page);
    await setType(request, 'retail');
    const retailMenu = await menuHrefs(page);

    await page.goto('/settings#settings-shop-type');
    await page.locator('#shop-type-change').click();
    const title = page.locator('#shop-type-title');
    await expect(title).toHaveText('Choose a new shop type');

    // Radiogroup with the current type disabled and the other five selectable.
    const group = page.locator('.shop-type-tiles');
    await expect(group).toHaveAttribute('role', 'radiogroup');
    await expect(group.locator('button[role="radio"]')).toHaveCount(6);
    await expect(group.locator('button[data-value="retail"]')).toBeDisabled();
    for (const box of await group.locator('button[role="radio"]').evaluateAll((els) => els.map((e) => e.getBoundingClientRect().toJSON()))) {
      expect(box.height, 'tiles are >= 72px tall').toBeGreaterThanOrEqual(72);
    }
    // Next is disabled until a pick.
    const next = page.locator('.shop-type-pick').getByRole('button', { name: 'Next' });
    await expect(next).toBeDisabled();
    await page.screenshot({ path: test.info().outputPath('pick-1024x600.png') });

    const tile = group.locator('button[data-value="hospitality"]');
    await tile.click();
    await expect(tile).toHaveAttribute('aria-checked', 'true');
    await expect(next).toBeEnabled();
    await next.click();

    const preview = page.locator('[data-testid="shop-type-preview"]');
    await expect(preview.locator('#shop-type-preview-title')).toHaveText('Change shop type: Retail shop to Restaurant or bar');
    await expect(preview).toContainText('Will appear in your menu');
    await page.screenshot({ path: test.info().outputPath('preview-retail-to-hospitality-1024x600.png') });

    // Cancel: back on the card, nothing written.
    await cancelPanel(page, '[data-testid="shop-type-preview"]');
    await expect(page.locator('#shop-type-panel')).toContainText('Retail shop');
    expect(await menuHrefs(page), 'Cancel wrote nothing').toEqual(retailMenu);

    // Again, and confirm.
    await page.goto('/settings#settings-shop-type');
    await page.locator('#shop-type-change').click();
    await page.locator('.shop-type-tiles button[data-value="hospitality"]').click();
    await page.locator('.shop-type-pick').getByRole('button', { name: 'Next' }).click();
    await page.locator('[data-testid="shop-type-preview"]').getByRole('button', { name: 'Change shop type' }).click();
    await expect(page).toHaveURL(/\/settings/);
    const banner = page.locator('[data-testid="shop-type-undo"]');
    await expect(banner).toContainText('Shop type changed to Restaurant or bar.');
    await expect(banner.getByRole('button', { name: 'Undo' })).toBeVisible();
    await expect(banner.getByRole('button', { name: /Dismiss/ })).toBeVisible(); // the word, not only an icon
    await page.screenshot({ path: test.info().outputPath('undo-banner-1024x600.png') });

    // The same strip is on the Menu, and the menu now follows the new type.
    const hrefs = await menuHrefs(page);
    if (retailMenu.length) expect(hrefs).toContain('/tables');
    await expect(page.locator('[data-testid="shop-type-undo"]')).toBeVisible();

    // Undo from the menu strip puts the old type back.
    await page.locator('[data-testid="shop-type-undo"]').getByRole('button', { name: 'Undo' }).click();
    // Undo redirects to Settings with a status note (the strip itself is gone).
    await expect(page).toHaveURL(/\/settings\?shop_type_undone=/);
    await expect(page.getByText('Shop type changed back to Retail shop.')).toBeVisible();
    await expect(page.locator('[data-testid="shop-type-undo"]')).toHaveCount(0);
    expect(await menuHrefs(page), 'Undo restored the retail menu').toEqual(retailMenu);
    await page.goto('/settings');
    await expect(page.locator('[data-testid="settings-footer"]')).toContainText('Set up for: Retail shop');
    assertClean();
  });

  test('Dismiss closes the undo strip and it stays closed', async ({ page, request }) => {
    await setType(request, 'cafe');
    await page.goto('/settings');
    const banner = page.locator('[data-testid="shop-type-undo"]');
    await expect(banner).toBeVisible();
    await banner.getByRole('button', { name: /Dismiss/ }).click();
    await expect(page.locator('[data-testid="shop-type-undo"]')).toHaveCount(0);
    await page.reload();
    await expect(page.locator('[data-testid="shop-type-undo"]')).toHaveCount(0);
  });

  test('picking the same type twice is a no-op (a double tap cannot apply twice)', async ({ request }) => {
    await setType(request, 'service');
    const again = await request.post('/api/settings/shop-type', { form: { to: 'service' } });
    expect(again.ok()).toBe(true);
  });

  test('an unknown shop type is refused with 400 and changes nothing', async ({ request, page }) => {
    await setType(request, 'cafe');
    const before = await menuHrefs(page);
    const res = await request.post('/api/settings/shop-type', { form: { to: 'spaceship' } });
    expect(res.status()).toBe(400);
    expect(await menuHrefs(page)).toEqual(before);
  });

  test('the preview endpoint never writes (GET only, same menu afterwards)', async ({ request, page }) => {
    await setType(request, 'cafe');
    const before = await menuHrefs(page);
    const res = await request.get('/api/settings/shop-type/preview?to=market_stall');
    expect(res.status()).toBe(200);
    expect(await menuHrefs(page)).toEqual(before);
  });
});

test.describe('setup wizard shop-type step', () => {
  test('six tiles, Continue disabled until a pick, a pick enables it, Skip keeps everything visible', async ({ page }) => {
    await page.setViewportSize(KIOSK);
    await page.goto('/setup');
    await page.locator('[data-step="1"] .setup-nav button', { hasText: 'Next' }).click();
    const countryStep = page.locator('[data-step="2"]');
    await countryStep.locator('h1').waitFor();
    const showAll = countryStep.locator('button', { hasText: 'Show all countries' });
    if (await showAll.isVisible()) await showAll.click();
    await countryStep.locator('button.picker-tile[value="GB"]').click();
    await countryStep.locator('.setup-nav button', { hasText: 'Next' }).click();
    await page.locator('input[name=store_name]').fill('Template Test Shop');
    await page.locator('input[name=till_name]').fill('Front till');
    await page.locator('[data-step="4"] .setup-nav button', { hasText: 'Next' }).click();

    const step = page.locator('[data-step="5"]');
    await expect(step.locator('h1')).toHaveText('What kind of shop is this?');
    await expect(step.locator('button.shop-type-tile')).toHaveCount(6);
    for (const [type, want] of Object.entries(MATRIX)) {
      await expect(step.locator(`button[data-value="${type}"]`)).toContainText(want.label);
    }
    await expect(step.locator('button.shop-type-tile .shop-type-tile-sum').first()).toBeVisible(); // one-line summary per tile
    const cont = step.locator('.setup-nav button[data-next]');
    await expect(cont).toBeDisabled();
    await expect(step).toContainText('Skipping keeps everything visible.');

    const cafe = step.locator('button[data-value="cafe"]');
    await cafe.click();
    await expect(cafe).toHaveAttribute('aria-checked', 'true');
    await expect(step.locator('input[name=shop_type]')).toHaveValue('cafe');
    await expect(cont).toBeEnabled();
    await page.screenshot({ path: test.info().outputPath('wizard-step5-1024x600.png') });

    // Keyboard: arrows move the selection inside the one-tab-stop group.
    await cafe.focus();
    await page.keyboard.press('ArrowRight');
    await expect(step.locator('button.shop-type-tile[aria-checked="true"]')).toHaveCount(1);

    // Skip clears the pick and moves on with no type.
    await step.getByRole('button', { name: 'Skip for now' }).click();
    await expect(page.locator('[data-step="6"]')).toBeVisible();
    await expect(step.locator('input[name=shop_type]')).toHaveValue('');
  });
});
