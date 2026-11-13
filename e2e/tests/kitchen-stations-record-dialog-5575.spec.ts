import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole, clickThenReload, answerTillConfirm, noNativeDialogs } from './helpers';

// ut-docs#5575: /kitchen-stations adopts the ut-docs#2010 list + record dialog
// standard (modelled on promotions-record-dialog-3661.spec.ts). The old page
// put a whole edit form in every row and a side create form next to the table.
// This proves the wiring through a real browser: no sideways scroll at the
// tablet floor and desktop in en/de/fa (including the routing tables),
// open/close/focus/scrim, create / edit / deactivate / activate, in-dialog
// refusals, and printer discovery inside the dialog.

const DIALOG = '#kitchen-station-dialog';
const FORM = '#kitchen-station-form';
const MSG = '#kitchen-station-dialog-msg';
const LONG_ADDR = 'tcp://printer-with-a-rather-long-hostname.kitchen.example.test:9100';
const GERMAN_NAME = 'Heißgetränke-Ausgabestation Obergeschoss';

function row(page: Page, name: string) {
  return page.locator('#kitchen-stations-table .kitchen-station-row', { hasText: name }).first();
}

async function seed(page: Page, name: string, extra: Record<string, string> = {}) {
  const r = await page.request.post('/api/kitchen-stations', {
    form: { name, destination_type: 'printer', printer_address: '192.0.2.10:9100', ...extra },
    maxRedirects: 0,
  });
  expect(r.status()).toBe(303);
  expect(r.headers().location).toBe('/kitchen-stations');
}

async function stationId(page: Page, name: string): Promise<string> {
  await page.goto('/kitchen-stations');
  const id = await row(page, name).getAttribute('data-id');
  expect(id).toBeTruthy();
  return id!;
}

async function overflow(page: Page) {
  return page.evaluate(() => {
    const wide = (el: Element) => (el as HTMLElement).scrollWidth > (el as HTMLElement).clientWidth;
    return {
      doc: document.documentElement.scrollWidth > document.documentElement.clientWidth,
      scrollers: Array.from(document.querySelectorAll('.list-scroll')).map(wide),
    };
  });
}

test.describe('kitchen stations list + record dialog (ut-docs#5575)', () => {
  // AC3: realistic fixture, no sideways scroll, every name button inside the viewport.
  for (const vp of [{ width: 1024, height: 600 }, { width: 1920, height: 1080 }]) {
    for (const lang of ['en', 'de', 'fa']) {
      test(`no sideways scroll at ${vp.width}x${vp.height} (${lang})`, async ({ page }) => {
        const tag = `${vp.width}${lang}`;
        await seed(page, `Printer ${tag}`, { printer_address: LONG_ADDR });
        await seed(page, `Display ${tag}`, { destination_type: 'display', printer_address: '' });
        await seed(page, `Both ${tag}`, { destination_type: 'both' });
        await seed(page, `${GERMAN_NAME} ${tag}`);
        await seed(page, `Old ${tag}`);
        for (const c of ['Starters', 'Mains', 'Desserts']) {
          await page.request.post('/api/categories', { form: { name: `${c} ${tag}` }, maxRedirects: 0 });
        }
        // One item override, when the demo till has an item to attach it to.
        const sid = await stationId(page, `Printer ${tag}`);
        const q = await page.request.get('/kitchen-stations?q=a');
        const m = /item-add-([^"]+)"/.exec(await q.text());
        if (m) await page.request.post(`/api/kitchen-stations/routes/items/${m[1]}`, { form: { station_id: sid }, maxRedirects: 0 });
        const oid = await stationId(page, `Old ${tag}`);
        await page.request.post(`/api/kitchen-stations/${oid}/active`, { form: { active: '0' }, maxRedirects: 0 });

        await page.setViewportSize(vp);
        await page.goto(`/kitchen-stations?lang=${lang}`);
        if (lang === 'fa') await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
        await expect(row(page, `Printer ${tag}`)).toBeVisible();
        const o = await overflow(page);
        expect(o.doc, 'document must not scroll sideways').toBe(false);
        expect(o.scrollers, 'the stations list must not scroll sideways').not.toContain(true);
        // The routing matrix gains a column per station and the worker's till
        // keeps every earlier test's stations, so it may scroll INSIDE its own
        // wrapper (a >6-station matrix is a documented non-goal, ut-docs#5575);
        // the document-level check above is what proves it never spills out.
        const nameBtns = page.locator('#kitchen-stations-table .record-row-open');
        for (let i = 0; i < (await nameBtns.count()); i++) {
          const b = (await nameBtns.nth(i).boundingBox())!;
          expect(b.x).toBeGreaterThanOrEqual(0);
          expect(b.x + b.width).toBeLessThanOrEqual(vp.width + 0.5);
        }
      });
    }
  }

  // AC2: +, name button and row tap all open the dialog; scrim, status, Escape, focus return.
  test('+, name button and row tap open the dialog; Escape discards with a guard; focus returns', async ({ page }) => {
    noNativeDialogs(page);
    const assertClean = watchConsole(page);
    const name = 'Open5575';
    await seed(page, name);
    await page.goto('/kitchen-stations');
    const plus = page.locator('#kitchen-stations-new');
    await expect(plus).toHaveAttribute('aria-label', /.+/);
    await expect(plus).toHaveAttribute('title', /.+/);

    await plus.click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator('#ut-scrim')).toBeVisible();
    expect(await page.locator(DIALOG).evaluate((d: HTMLDialogElement) => d.matches(':modal'))).toBe(false);
    for (const sel of ['[data-record-dialog-conn]', '[data-record-dialog-exit]', '[data-record-dialog-lock] button']) {
      await expect(page.locator(`${DIALOG} ${sel}`)).toBeVisible();
    }
    await expect(page.locator(`${FORM} input[name="name"]`)).toHaveValue('');
    await expect(page.locator(`${FORM} select[name="destination_type"]`)).toHaveValue('printer');
    await page.keyboard.press('Escape');
    await expect(page.locator(DIALOG)).toBeHidden();
    await expect(plus).toBeFocused();

    const nameBtn = row(page, name).locator('.record-row-open');
    await nameBtn.click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${FORM} input[name="name"]`)).toHaveValue(name);
    await expect(page.locator(`${FORM} input[name="printer_address"]`)).toHaveValue('192.0.2.10:9100');
    // Dirty edit: Escape asks first; declining keeps the dialog open.
    await page.locator(`${FORM} input[name="name"]`).fill('Changed');
    await page.keyboard.press('Escape');
    await answerTillConfirm(page, 'cancel');
    await expect(page.locator(DIALOG)).toBeVisible();
    await page.keyboard.press('Escape');
    await answerTillConfirm(page, 'confirm');
    await expect(page.locator(DIALOG)).toBeHidden();
    await expect(nameBtn).toBeFocused();

    await row(page, name).locator('td').first().click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${FORM} input[name="name"]`)).toHaveValue(name);
    assertClean();
  });

  // AC5: create (printer, display with no address), edit, and an in-dialog refusal.
  test('create, edit, and a refusal that keeps the dialog open with the typed values', async ({ page }) => {
    const assertClean = watchConsole(page, /Failed to load resource:.*400/);
    const name = 'Create5575' + (Date.now() % 100000);
    await page.goto('/kitchen-stations');
    const plus = page.locator('#kitchen-stations-new');

    // Blank name is blocked by the browser; a printer with no address is
    // refused by the server and shown in the dialog.
    await plus.click();
    await page.locator(`${FORM} input[name="name"]`).fill(name);
    await page.locator(`${DIALOG} .record-dialog-save`).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page).toHaveURL(/\/kitchen-stations$/);
    await expect(page.locator(MSG)).toContainText(/address is required/i);
    await expect(page.locator(`${FORM} input[name="name"]`)).toHaveValue(name);

    await page.locator(`${FORM} input[name="printer_address"]`).fill('192.0.2.77:9100');
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, name)).toHaveCount(1);
    await expect(row(page, name)).toContainText('192.0.2.77:9100');

    // A display needs no address.
    const screen = name + 'D';
    await plus.click();
    await page.locator(`${FORM} input[name="name"]`).fill(screen);
    await page.locator(`${FORM} select[name="destination_type"]`).selectOption('display');
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, screen)).toContainText(/display/i);

    // Edit.
    await row(page, name).locator('[data-record-edit]').click();
    await page.locator(`${FORM} select[name="destination_type"]`).selectOption('both');
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, name)).toHaveAttribute('data-field-destination_type', 'both');
    assertClean();
  });

  // AC5: deactivate (with confirm text) then Activate.
  test('deactivate then activate round-trips from the dialog', async ({ page }) => {
    const assertClean = watchConsole(page);
    const name = 'Deact5575';
    await seed(page, name);
    await page.goto('/kitchen-stations');
    await row(page, name).click();
    await expect(page.locator(`${DIALOG} form[data-record-when="active=0"]`)).toBeHidden();
    const trash = page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`);
    await expect(trash).toBeVisible();
    await clickThenReload(page, async () => {
      await trash.click();
      await answerTillConfirm(page, 'confirm', /default kitchen printer/i);
    });
    await expect(row(page, name)).toContainText(/inactive/i);

    await row(page, name).click();
    const act = page.locator(`${DIALOG} form[data-record-when="active=0"] button[type="submit"]`);
    await expect(act).toBeVisible();
    await clickThenReload(page, () => act.click());
    await expect(row(page, name)).not.toContainText(/inactive/i);
    assertClean();
  });

  // AC7: discovery inside the dialog (stubbed), in create and edit mode.
  test('Find printers fills the address without submitting, and clears on reopen', async ({ page }) => {
    noNativeDialogs(page);
    const assertClean = watchConsole(page);
    const name = 'Disc5575';
    await seed(page, name);
    let posts = 0;
    page.on('request', (rq) => { if (rq.method() === 'POST' && /\/api\/kitchen-stations(\/(?!discover-printers)[^/]+)?$/.test(new URL(rq.url()).pathname)) posts++; });
    await page.route('**/api/kitchen-stations/discover-printers', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json',
        body: JSON.stringify({ data: { printers: [{ name: 'Bar Printer', address: '192.0.2.99:9100' }] }, error: null }) }));
    await page.goto('/kitchen-stations');

    for (const open of [() => page.locator('#kitchen-stations-new').click(), () => row(page, name).locator('[data-record-edit]').click()]) {
      await open();
      await page.locator('#discover-printers-btn').click();
      const use = page.locator('#discover-printers-results button', { hasText: /use this address/i });
      await expect(use).toBeVisible();
      await use.click();
      await expect(page.locator(`${FORM} input[name="printer_address"]`)).toHaveValue('192.0.2.99:9100');
      await expect(page.locator('#discover-printers-msg')).toContainText(/tap save/i);
      expect(posts, 'Use this address must not submit').toBe(0);
      // The address change counts as dirty: Escape asks before discarding.
      await page.keyboard.press('Escape');
      await answerTillConfirm(page, 'confirm');
      await expect(page.locator(DIALOG)).toBeHidden();
    }
    await page.locator('#kitchen-stations-new').click();
    await expect(page.locator('#discover-printers-results')).toBeEmpty();
    await expect(page.locator('#discover-printers-msg')).toHaveText('');
    assertClean();
  });

  // AC8: View display navigates and does not open the dialog.
  test('tapping View display navigates and does not open the dialog', async ({ page }) => {
    const name = 'View5575';
    await seed(page, name, { destination_type: 'display', printer_address: '' });
    await page.goto('/kitchen-stations');
    const link = row(page, name).locator('a.kitchen-view-display');
    const b = (await link.boundingBox())!;
    expect(b.height, 'hit area >= 44px tall').toBeGreaterThanOrEqual(43);
    await link.click();
    await expect(page).toHaveURL(/\/kitchen-display\//);
  });

  // ut-docs#6046: the name is the keyboard path; row tap focus-returns to it too.
  test('name button opens on Enter and Space, a non-name cell tap opens, focus returns to the name; View display never opens the dialog', async ({ page }) => {
    const name = 'Kbd6046' + Date.now();
    await seed(page, name, { destination_type: 'display' });
    await page.goto('/kitchen-stations');
    const nameBtn = row(page, name).locator('.record-row-open');
    for (const key of ['Enter', ' ']) {
      await nameBtn.focus();
      await page.keyboard.press(key);
      await expect(page.locator(DIALOG)).toBeVisible();
      await expect(page.locator(`${FORM} input[name="name"]`)).toHaveValue(name);
      await page.keyboard.press('Escape');
      await expect(page.locator(DIALOG)).toBeHidden();
      await expect(nameBtn).toBeFocused();
    }
    await row(page, name).locator('td').nth(2).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator(DIALOG)).toBeHidden();
    await expect(nameBtn).toBeFocused();
    const link = row(page, name).locator('.kitchen-view-display');
    await expect(link).toHaveCount(1);
    // Hold the display request so the page stays here: the dialog check
    // below then runs on this page, not vacuously after navigating away.
    await page.route(/\/kitchen-display\//, (r) => r.abort());
    await link.click();
    await expect(page.locator(DIALOG)).toHaveCount(1);
    await expect(page.locator(DIALOG)).toBeHidden();
    await expect(page).not.toHaveURL(/\/kitchen-display\//);
    await page.unroute(/\/kitchen-display\//);
    await Promise.all([page.waitForURL(/\/kitchen-display\//), link.click()]);
  });
});
