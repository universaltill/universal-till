import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { execFileSync } from 'child_process';
import { watchConsole, clickThenReload } from './helpers';
import { REPO_ROOT } from './worker-till';

// ut-docs#3661: /promotions adopts the ut-docs#2010 list + record dialog
// standard (copying locations-record-dialog-2124.spec.ts). The old page put a
// whole edit form in every row, so the table scrolled sideways at 1024x600.
// This spec proves the page's own wiring through a real browser: no sideways
// scroll at the tablet floor, prefill (incl. the German decimal comma), create
// / edit / deactivate / activate, in-dialog refusals, RTL and keyboard.

const DIALOG = '#promotion-dialog';
const FORM = '#promotion-form';
const LONG_DESC = 'Weekend offer on every hot drink and pastry!'.slice(0, 40);

function isoDay(offsetDays: number): string {
  return new Date(Date.now() + offsetDays * 86_400_000).toISOString().slice(0, 10);
}

function row(page: Page, code: string) {
  return page.locator('#promotions-table .promotion-row', { hasText: code }).first();
}

// The e2e till is seeded with no customers, so the customer-bound checks
// (UUID wrapping, read-only prefill, create with a picked customer) need one
// written into THIS worker's till out of band (review of ut-docs#3661: with
// the old "first customer, if any" lookup every such check was skipped).
const CUSTOMER_NAME = 'E2E Promo Customer'; // e2e/promo_customer's name
let customer = '';
const OTHER_NAME = 'E2E Promo Other'; // ut-docs#5574: a second customer to change to
function promoCustomerTool(...args: string[]): string {
  const dataDir = process.env.UT_E2E_WORKER_DATA_DIR;
  if (!dataDir) throw new Error('UT_E2E_WORKER_DATA_DIR unset');
  const id = execFileSync('go', ['run', './e2e/promo_customer', ...args], {
    cwd: REPO_ROOT,
    env: { ...process.env, UT_DATA_DIR: dataDir },
    stdio: ['ignore', 'pipe', 'inherit'],
  }).toString().trim().split('\n').pop()!.trim(); // db.Open logs to stdout first
  expect(id).toMatch(/^[0-9a-f-]{36}$/);
  return id;
}
function customerId(): string {
  if (!customer) customer = promoCustomerTool();
  return customer;
}
function otherCustomerId(): string {
  return promoCustomerTool('-name', OTHER_NAME);
}
function shellCustomerId(promoCode: string): string {
  return promoCustomerTool('-shell', '-promo', promoCode);
}

async function seed(page: Page, code: string, extra: Record<string, string> = {}) {
  const r = await page.request.post('/api/promotions', {
    form: {
      code, type: 'amount', value_amount: '12.50', description: LONG_DESC,
      starts_at: isoDay(-1), ends_at: isoDay(200), customer_id: '', ...extra,
    },
    maxRedirects: 0,
  });
  expect(r.status()).toBe(303);
  expect(r.headers().location).toBe('/promotions');
}

async function noSidewaysScroll(page: Page) {
  const r = await page.evaluate(() => {
    const card = document.querySelector('.list-card .list-scroll') as HTMLElement;
    return {
      cardSw: card.scrollWidth, cardCw: card.clientWidth,
      docSw: document.documentElement.scrollWidth, docCw: document.documentElement.clientWidth,
    };
  });
  return r;
}

test.describe('promotions list + record dialog (ut-docs#3661)', () => {
  // Checked per test, not at describe level: the variable is set by the
  // worker fixture (baseURL pulls it in), which runs after collection.
  test.beforeEach(async ({ baseURL }) => {
    test.skip(!baseURL || !process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir); a reused server has none');
  });

  // AC1: the table card does not scroll sideways at the tablet floor / desktop,
  // in English and German, windowed and kiosk; Status + pencil stay in view.
  for (const vp of [{ width: 1024, height: 600 }, { width: 1920, height: 1080 }]) {
    for (const lang of ['en', 'de']) {
      for (const kiosk of [false, true]) {
        test(`no sideways scroll at ${vp.width}x${vp.height} (${lang}${kiosk ? ', kiosk' : ''})`, async ({ page }) => {
          const code = `W3661${vp.width}${lang}${kiosk ? 'K' : ''}`;
          await seed(page, code, { customer_id: customerId() });
          await page.setViewportSize(vp);
          await page.goto(`/promotions?lang=${lang}`);
          if (kiosk) await page.evaluate(() => document.body.classList.add('kiosk'));
          await expect(row(page, code)).toBeVisible();
          const r = await noSidewaysScroll(page);
          expect(r.cardSw, 'table card must not scroll sideways').toBeLessThanOrEqual(r.cardCw);
          expect(r.docSw).toBeLessThanOrEqual(r.docCw);
          for (const sel of ['td[data-card-keep]', '[data-record-edit]']) {
            const b = (await row(page, code).locator(sel).first().boundingBox())!;
            expect(b.x, sel).toBeGreaterThanOrEqual(0);
            expect(b.x + b.width, sel).toBeLessThanOrEqual(vp.width + 0.5);
          }
        });
      }
    }
  }

  // AC1 at 481px (UX A2): the page never scrolls, any overflow sits only
  // inside .list-scroll, and a tap on the Code cell opens the dialog.
  test('at 481px the page does not scroll and a Code-cell tap opens edit', async ({ page }) => {
    const code = 'N3661W481';
    await seed(page, code);
    await page.setViewportSize({ width: 481, height: 800 });
    await page.goto('/promotions');
    const r = await noSidewaysScroll(page);
    expect(r.docSw).toBeLessThanOrEqual(r.docCw);
    await row(page, code).locator('td').first().click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${FORM} input[name="code"]`)).toHaveValue(code);
  });

  // AC2: pencil and row tap both prefill, in the locale's decimal separator.
  test('pencil and row tap open edit prefilled (de uses decimal comma); saving keeps the customer', async ({ page }) => {
    const assertClean = watchConsole(page);
    const cust = customerId();
    const amount = 'P3661A', pct = 'P3661P';
    await seed(page, amount, { customer_id: cust });
    await seed(page, pct, { type: 'percent', value_percent: '7.5', value_amount: '' });
    await page.goto('/promotions?lang=de');

    await row(page, amount).locator('[data-record-edit]').click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${FORM} input[name="code"]`)).toHaveValue(amount);
    await expect(page.locator(`${FORM} input[name="code"]`)).toHaveAttribute('readonly', '');
    await expect(page.locator(`${FORM} select[name="type"]`)).toHaveValue('amount');
    await expect(page.locator(`${FORM} input[name="value_amount"]`)).toHaveValue('12,50');
    await expect(page.locator(`${FORM} input[name="description"]`)).toHaveValue(LONG_DESC);
    await expect(page.locator(`${FORM} input[name="starts_at"]`)).toHaveValue(isoDay(-1));
    await expect(page.locator(`${FORM} input[name="ends_at"]`)).toHaveValue(isoDay(200));
    // ut-docs#5574: the bound customer's NAME is shown and the field is editable.
    await expect(page.locator('#promo-cust-q')).toHaveValue(CUSTOMER_NAME);
    await expect(page.locator('#promo-cust-q')).not.toHaveAttribute('readonly', '');
    await expect(page.locator(`${FORM} input[name="customer_id"]`)).toHaveValue(cust);
    await page.locator(`${FORM} input[name="description"]`).fill('Edited 3661');
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, amount)).toContainText('Edited 3661');
    await expect(row(page, amount)).toContainText(CUSTOMER_NAME);
    await expect(row(page, amount)).not.toContainText(cust);
    await expect(row(page, amount)).toHaveAttribute('data-field-customer_id', cust);

    // Row tap on a percent promotion: value_percent prefill, comma in de.
    await row(page, pct).locator('td').first().click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${FORM} select[name="type"]`)).toHaveValue('percent');
    await expect(page.locator(`${FORM} input[name="value_percent"]`)).toHaveValue('7,50');
    await expect(page.locator(`${FORM} input[name="value_percent"]`)).toBeVisible();
    await expect(page.locator(`${FORM} input[name="value_amount"]`)).toBeHidden();
    assertClean();
  });

  // AC3: New opens empty; create amount (no customer) and percent (with one);
  // a duplicate code is refused in-dialog.
  test('New creates an amount and a percent promotion; a duplicate is refused in the dialog', async ({ page }) => {
    const assertClean = watchConsole(page, /Failed to load resource:.*400/);
    const cust = customerId();
    const a = 'C3661A' + (Date.now() % 100000), p = 'C3661P' + (Date.now() % 100000);
    await page.goto('/promotions');
    const newBtn = page.locator('#promotions-new');
    await expect(newBtn).toHaveAttribute('aria-label', /.+/);
    await expect(newBtn).toHaveAttribute('title', /.+/);

    await newBtn.click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${FORM} input[name="code"]`)).toHaveValue('');
    await expect(page.locator(`${FORM} input[name="code"]`)).not.toHaveAttribute('readonly', '');
    await page.locator(`${FORM} input[name="code"]`).fill(a);
    await page.locator(`${FORM} input[name="value_amount"]`).fill('4.00');
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, a)).toHaveCount(1);

    await newBtn.click();
    await page.locator(`${FORM} input[name="code"]`).fill(p);
    await page.locator(`${FORM} select[name="type"]`).selectOption('percent');
    await page.locator(`${FORM} input[name="value_percent"]`).fill('10');
    await page.locator('#promo-cust-q').fill(CUSTOMER_NAME);
    const pick = page.locator(`#promo-cust-results [data-cust-pick="${cust}"]`);
    await expect(pick).toBeVisible();
    await pick.click();
    await expect(page.locator('#promo-cust-q')).toHaveValue(CUSTOMER_NAME);
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, p)).toHaveCount(1);
    await expect(row(page, p)).toHaveAttribute('data-field-customer_id', cust);

    // New after a customer-bound row: nothing of that customer leaks in.
    await row(page, p).locator('[data-record-edit]').click();
    await expect(page.locator('#promo-cust-q')).toHaveValue(CUSTOMER_NAME);
    await page.keyboard.press('Escape');
    await newBtn.click();
    await expect(page.locator('#promo-cust-q')).toHaveValue('');
    await expect(page.locator('#promo-cust-q')).not.toHaveAttribute('readonly', '');
    await expect(page.locator('#promo-cust-clear')).toBeHidden();
    await expect(page.locator(`${FORM} input[name="customer_id"]`)).toHaveValue('');
    await page.keyboard.press('Escape');

    // Duplicate: stays open, message in the dialog.
    await newBtn.click();
    await page.locator(`${FORM} input[name="code"]`).fill(a);
    await page.locator(`${FORM} input[name="value_amount"]`).fill('4.00');
    await page.locator(`${DIALOG} .record-dialog-save`).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page).toHaveURL(/\/promotions$/);
    await expect(page.locator(`${DIALOG} [data-record-dialog-msg]`)).toContainText(/already exists/i);
    assertClean();
  });

  // AC4: deactivate (with confirm) then Activate from the dialog.
  test('deactivate then activate round-trips from the dialog', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = 'D3661';
    await seed(page, code);
    await page.goto('/promotions');
    await row(page, code).click();
    await expect(page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`)).toBeVisible();
    page.once('dialog', (d) => d.accept());
    await clickThenReload(page, () => page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`).click());
    await expect(row(page, code)).toContainText(/inactive/i);

    await row(page, code).click();
    await expect(page.locator(`${DIALOG} form[data-record-when="active=0"] button[type="submit"]`)).toBeVisible();
    await clickThenReload(page, () => page.locator(`${DIALOG} form[data-record-when="active=0"] button[type="submit"]`).click());
    await expect(row(page, code)).not.toContainText(/inactive/i);
    assertClean();
  });

  // Review of ut-docs#3661: the code is free text and the row's URL path
  // segment; a "%" in it reached the server raw (an invalid escape, 400)
  // until the row's actions were path-escaped.
  test('a code with % and # in it saves and deactivates from the dialog', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = '10%OFF#3661';
    await seed(page, code);
    await page.goto('/promotions');
    await row(page, code).locator('[data-record-edit]').click();
    await expect(page.locator(`${FORM} input[name="code"]`)).toHaveValue(code);
    await page.locator(`${FORM} input[name="description"]`).fill('Escaped 3661');
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, code)).toContainText('Escaped 3661');

    await row(page, code).locator('[data-record-edit]').click();
    page.once('dialog', (d) => d.accept());
    await clickThenReload(page, () => page.locator(`${DIALOG} form[data-record-when="active=1"] button[type="submit"]`).click());
    await expect(row(page, code)).toContainText(/inactive/i);
    assertClean();
  });

  // AC6 RTL: layout mirrors, no overflow, and the UUID/code is not reordered.
  test('fa at 1024x600: no overflow and the code keeps its LTR order', async ({ page }) => {
    const code = 'R3661-AB-12';
    await seed(page, code, { customer_id: customerId() });
    await page.setViewportSize({ width: 1024, height: 600 });
    await page.goto('/promotions?lang=fa');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    const r = await noSidewaysScroll(page);
    expect(r.cardSw).toBeLessThanOrEqual(r.cardCw);
    expect(r.docSw).toBeLessThanOrEqual(r.docCw);
    await expect(row(page, code).locator('bdi[dir="ltr"]').first()).toHaveText(code);
    // ut-docs#5574: the customer name is bidi-isolated by content, not forced LTR.
    await expect(row(page, code).locator('td.promo-wrap bdi[dir="auto"]').last()).toHaveText(CUSTOMER_NAME);
    await row(page, code).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(`${FORM} input[name="code"]`)).toHaveValue(code);
  });

  // AC7/AC8: keyboard + the dialog never uses showModal; scrim is shown.
  test('keyboard opens the dialog, Escape closes it, focus returns; scrim shown', async ({ page }) => {
    const code = 'K3661';
    await seed(page, code);
    await page.goto('/promotions');
    const pencil = row(page, code).locator('[data-record-edit]');
    await pencil.focus();
    await page.keyboard.press('Enter');
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator('#ut-scrim')).toBeVisible();
    expect(await page.locator(DIALOG).evaluate((d: HTMLDialogElement) => d.matches(':modal'))).toBe(false);
    for (const sel of ['[data-record-dialog-conn]', '[data-record-dialog-exit]', '[data-record-dialog-lock] button']) {
      await expect(page.locator(`${DIALOG} ${sel}`)).toBeVisible();
    }
    await page.keyboard.press('Escape');
    await expect(page.locator(DIALOG)).toBeHidden();
    await expect(pencil).toBeFocused();
  });

  // ut-docs#5574: change the customer, then clear it, from the edit dialog.
  test('edit can change the customer and then remove it', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = 'CH5574';
    const other = otherCustomerId();
    await seed(page, code, { customer_id: customerId() });
    await page.goto('/promotions');

    await row(page, code).locator('[data-record-edit]').click();
    await expect(page.locator('#promo-cust-clear')).toBeVisible();
    await page.locator('#promo-cust-q').fill(OTHER_NAME);
    const pick = page.locator(`#promo-cust-results [data-cust-pick="${other}"]`);
    await expect(pick).toBeVisible();
    await pick.click();
    await expect(page.locator('#promo-cust-q')).toHaveValue(OTHER_NAME);
    // ut-docs#5574: the picked button left the DOM; focus must not fall to BODY.
    await expect(page.locator('#promo-cust-q')).toBeFocused();
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, code)).toContainText(OTHER_NAME);
    await expect(row(page, code)).toHaveAttribute('data-field-customer_id', other);

    await row(page, code).locator('[data-record-edit]').click();
    const clear = page.locator('#promo-cust-clear');
    const box = (await clear.boundingBox())!;
    expect(box.width).toBeGreaterThanOrEqual(44);
    expect(box.height).toBeGreaterThanOrEqual(44);
    await clear.click();
    await expect(page.locator('#promo-cust-q')).toHaveValue('');
    await expect(page.locator(`${FORM} input[name="customer_id"]`)).toHaveValue('');
    await expect(clear).toBeHidden();
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, code)).toContainText(/any customer/i);
    await expect(row(page, code)).toHaveAttribute('data-field-customer_id', '');
    assertClean();
  });

  // ut-docs#5574: typed text with nothing picked is refused, never a silent clear.
  test('typed but unpicked customer text is refused and nothing changes', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = 'UP5574';
    const cust = customerId();
    await seed(page, code, { customer_id: cust });
    await page.goto('/promotions');
    await row(page, code).locator('[data-record-edit]').click();
    await page.locator('#promo-cust-q').fill('zzz');
    await page.locator(`${DIALOG} .record-dialog-save`).click();
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator('#promotion-dialog-msg')).toContainText('Choose a customer from the search results');
    await expect(page.locator('#promo-cust-q')).toHaveAttribute('aria-invalid', 'true');
    // A re-open does not carry the stale refusal (Escape on a dirty form asks
    // to discard first).
    page.once('dialog', (d) => d.accept());
    await page.keyboard.press('Escape');
    await expect(page.locator(DIALOG)).toBeHidden();
    await row(page, code).locator('[data-record-edit]').click();
    await expect(page.locator('#promotion-dialog-msg')).not.toContainText('Choose a customer');
    await page.reload();
    await expect(row(page, code)).toHaveAttribute('data-field-customer_id', cust);
    await expect(row(page, code)).toContainText(CUSTOMER_NAME);
    assertClean();
  });

  // ut-docs#5574 (Tester): the unpicked refusal must be SEEN, not only be in
  // the DOM. The customer field is the last field, so at the 1024x600 tablet
  // floor (and on a phone) the body is scrolled down when Save is pressed;
  // the message region at the top of the body must end up in view, below
  // the pinned dialog head, not hidden above the scroll position.
  for (const vp of [{ width: 1024, height: 600 }, { width: 360, height: 740 }]) {
    test(`unpicked refusal message is visible at ${vp.width}x${vp.height}`, async ({ page }) => {
      const code = `UV${vp.width}`;
      await seed(page, code, { customer_id: customerId() });
      await page.setViewportSize(vp);
      await page.goto('/promotions');
      await row(page, code).locator('[data-record-edit]').click();
      await page.locator('#promo-cust-q').scrollIntoViewIfNeeded();
      await page.locator('#promo-cust-q').fill('zzz');
      await page.locator(`${DIALOG} .record-dialog-save`).click();
      const msg = page.locator('#promotion-dialog-msg');
      await expect(msg).toContainText('Choose a customer from the search results');
      const m = (await msg.boundingBox())!;
      const head = (await page.locator(`${DIALOG} .record-dialog-head`).boundingBox())!;
      expect(m.y, 'message is below the pinned dialog head').toBeGreaterThanOrEqual(head.y + head.height - 1);
      expect(m.y + m.height, 'message is inside the viewport').toBeLessThanOrEqual(vp.height);
    });
  }

  // ut-docs#5574: a promotion bound to a retired shell shows "Deleted customer",
  // never the id, and keeps that binding through an unrelated edit.
  test('a deleted customer shows the label and survives an edit', async ({ page }) => {
    const assertClean = watchConsole(page);
    const code = 'DL5574';
    // The server refuses a shell as a create target, so the tool binds the
    // promotion to the customer first and then retires the customer.
    const shell = shellCustomerId(code);
    await page.goto('/promotions');
    await expect(row(page, code)).toContainText('Deleted customer');
    await expect(row(page, code)).not.toContainText(shell);
    await row(page, code).locator('[data-record-edit]').click();
    await expect(page.locator('#promo-cust-q')).toHaveValue('Deleted customer');
    await page.locator(`${FORM} input[name="description"]`).fill('Edited 5574');
    await clickThenReload(page, () => page.locator(`${DIALOG} .record-dialog-save`).click());
    await expect(row(page, code)).toContainText('Edited 5574');
    await expect(row(page, code)).toContainText('Deleted customer');
    await expect(row(page, code)).toHaveAttribute('data-field-customer_id', shell);
    assertClean();
  });
});
