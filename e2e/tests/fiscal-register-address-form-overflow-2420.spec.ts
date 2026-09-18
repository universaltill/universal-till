import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2420: found while writing e2e coverage for ut-docs#2186's
// record_dialog work (ut-docs#2403) -- real, reproduced, unrelated to that
// dialog. The per-location address-edit form (.users-inline in
// web/ui/pages/fiscal_register.html) has no intrinsic ability to shrink;
// once its location group also renders a table with a real row, the
// whole document overflows horizontally by ~184px at the 360px floor
// (an empty group -- heading + address form, no rows, no table -- never
// reaches this). Same measurement approach as
// catalog-tax-codes-standalone-overflow-2113.spec.ts (scrollWidth vs
// clientWidth, not trusting the CSS by eye). Reuses this page's own
// createRegister/createLocation/createFiscalEntry helpers, same shape as
// fiscal-register-record-dialog-2186.spec.ts.

const DIALOG = '#fiscal-register-dialog';
const FORM = '#fiscal-register-form';
const REGISTER_SELECT = `${FORM} select[name="register_id"]`;

async function createRegister(page: Page, name: string, locationLabel?: string): Promise<void> {
  await page.goto('/registers');
  await page.locator('#registers-new').click();
  const dlg = page.locator('#register-dialog');
  await expect(dlg).toBeVisible();
  await page.locator('#register-form input[name="name"]').fill(name);
  if (locationLabel) {
    await page.locator('#register-form select[name="location_id"]').selectOption({ label: locationLabel });
  }
  await Promise.all([
    page.waitForURL(/\/registers$/),
    dlg.locator('.record-dialog-save').click(),
  ]);
  await page.waitForLoadState('networkidle');
}

async function createLocation(page: Page, name: string): Promise<void> {
  await page.goto('/locations');
  await page.locator('#locations-new').click();
  const dlg = page.locator('#location-dialog');
  await expect(dlg).toBeVisible();
  await page.locator('#location-form input[name="name"]').fill(name);
  await Promise.all([
    page.waitForURL(/\/locations$/),
    dlg.locator('.record-dialog-save').click(),
  ]);
  await page.waitForLoadState('networkidle');
}

async function createFiscalEntry(page: Page, registerLabel: string, easSerial: string): Promise<void> {
  await page.goto('/fiscal-register');
  await page.locator('#fiscalregister-new').click();
  const dlg = page.locator(DIALOG);
  await expect(dlg).toBeVisible();
  await page.locator(REGISTER_SELECT).selectOption({ label: registerLabel });
  await page.locator(`${FORM} input[name="eas_software"]`).fill('E2E Software');
  await page.locator(`${FORM} input[name="eas_serial"]`).fill(easSerial);
  await page.locator(`${FORM} input[name="tse_serial"]`).fill('TSE-' + easSerial);
  await page.locator(`${FORM} input[name="tse_certification_id"]`).fill('CERT-' + easSerial);
  await page.locator(`${FORM} input[name="tse_type"]`).fill('Cloud-TSE');
  await page.locator(`${FORM} input[name="acquired_on"]`).fill('2020-01-01');
  await Promise.all([
    page.waitForURL(/\/fiscal-register$/),
    page.locator(`${DIALOG} .record-dialog-save`).click(),
  ]);
  await page.waitForLoadState('networkidle');
}

test.describe('fiscal-register per-location address form stays inside the viewport at phone width (ut-docs#2420)', () => {
  test('/fiscal-register never needs horizontal document scroll at 360x800 once a location group has an entry', async ({ page }) => {
    const assertClean = watchConsole(page);
    const locationName = 'E2E Overflow Location ' + Date.now();
    const regName = 'E2E Overflow Till ' + Date.now();
    const serial = 'OVERFLOW-' + Date.now();

    await createLocation(page, locationName);
    await createRegister(page, regName, locationName);
    await createFiscalEntry(page, regName, serial);

    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/fiscal-register');
    // The row (and its group's address-edit form) must actually be on
    // screen for this repro -- an empty group never overflowed.
    await expect(page.locator('#fiscal-register-groups .entry-row', { hasText: serial })).toBeVisible();
    await expect(page.locator('.fiscal-register-group .users-inline').first()).toBeVisible();

    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(
      overflow.scrollWidth,
      `document is ${overflow.scrollWidth - overflow.clientWidth}px wider than the viewport ` +
        `(scrollWidth=${overflow.scrollWidth}, clientWidth=${overflow.clientWidth})`,
    ).toBeLessThanOrEqual(overflow.clientWidth);

    // The address form's own box must not be wider than the viewport
    // either -- it wraps rather than merely being clipped by some
    // ancestor's overflow:hidden.
    const formWidth = await page.evaluate(() => {
      const form = document.querySelector('.fiscal-register-group .users-inline');
      return form ? form.getBoundingClientRect().width : null;
    });
    expect(formWidth).not.toBeNull();
    expect(formWidth!).toBeLessThanOrEqual(360);

    assertClean();
  });

  test('/users inline action forms are unaffected (ut-docs#898 regression guard)', async ({ page }) => {
    // ut-docs#2420's fix is deliberately scoped to
    // .fiscal-register-group .users-inline, not the shared .users-inline
    // class -- a bare flex-wrap: wrap on .users-inline itself was already
    // tried and reverted because it made the /users role-select +
    // "Change role" row wrap even at full desktop width (ut-docs#898).
    // /users carries no .fiscal-register-group ancestor at all, so this
    // pins that its .users-inline forms keep the default nowrap
    // regardless of which one renders for the seeded default operator.
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/users');
    const inlineForm = page.locator('.users-inline').first();
    await expect(inlineForm).toBeVisible();
    const flexWrap = await inlineForm.evaluate((el: HTMLElement) => getComputedStyle(el).flexWrap);
    expect(flexWrap).toBe('nowrap');
    assertClean();
  });
});
