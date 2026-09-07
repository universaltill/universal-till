import { test, expect } from './fixtures';
import { deactivateAllTables, createTable } from './helpers';

// ut-docs#1638: table_picker.html's .table-picker-clear/.table-picker-option
// buttons used to close #table-modal via a plain onclick that ran
// SYNCHRONOUSLY on click -- before their hx-post="/api/pos/table" request
// even started, let alone completed. Same antipattern ut-docs#1624 fixed for
// New Customer/#payment-overlay in index.html. The fix moves both buttons to
// the exact hx-on::after-request gate New Sale/New Customer already use
// (index.html), retargeted at #table-modal.
//
// Note on what "stays open" can actually mean here, unlike #1624:
// #table-modal is rendered INSIDE #table-picker, which is itself inside
// #basket (basket.html) -- so ANY response to /api/pos/table (success or
// error) replaces #basket's whole outerHTML, which tears down and rebuilds
// #table-picker (hx-trigger="load" on the fresh placeholder span) and with
// it the open dialog, regardless of the gate. #payment-overlay in #1624 is a
// sibling of #basket, not a descendant, so it survives a #basket swap
// untouched -- #table-modal structurally cannot. So the bug this card
// actually fixes, and the only thing the gate can change, is the PREMATURE
// synchronous close firing before the request is even sent: the first spec
// below drives that directly, with a manually-released route so the request
// is provably still in flight when we check.
test.describe('Table picker respects the #table-modal close gate (ut-docs#1638)', () => {
  const TABLE_LABEL = 'E2E Picker 1638';

  test.beforeEach(async ({ page }) => {
    // Shared server-global engine across specs (ut-docs#1310) — start clean.
    await page.request.post('/api/pos/reset');
  });
  test.afterEach(async ({ page }) => {
    await page.request.post('/api/pos/reset');
  });

  const openPickerWithFreeTable = async (page: import('@playwright/test').Page) => {
    // deactivateAllTables/createTable navigate through /tables themselves —
    // do this before touching the POS page so the picker below is
    // Configured (ADR-0054) with exactly one, known, free table.
    await deactivateAllTables(page);
    await createTable(page, TABLE_LABEL);

    await page.goto('/');
    await page.waitForSelector('.pos-container');

    // A dine-in line (the default order type) is what makes the picker
    // eligible (ADR-0073 Decision 5) — same scan used by the #1624 spec.
    await page.getByRole('textbox').first().fill('5000000000012');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/scan')),
      page.locator('.scan-row button[type=submit]').click(),
    ]);
    await expect(page.locator('#basket')).toContainText('Coca-Cola');

    await page.getByTestId('table-picker-open').click();
    await expect(page.locator('#table-modal')).toBeVisible();
  };

  test('picking a free table closes #table-modal and assigns it (unchanged behavior)', async ({ page }) => {
    await openPickerWithFreeTable(page);

    const optionBtn = page.locator('.table-picker-option', { hasText: TABLE_LABEL });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/table')),
      optionBtn.click(),
    ]);

    await expect(page.locator('#table-modal')).not.toBeVisible();
    await expect(page.getByTestId('table-picker-open')).toContainText(TABLE_LABEL);
  });

  test('does not close #table-modal before the /api/pos/table response completes (regression: was a synchronous onclick)', async ({ page }) => {
    await openPickerWithFreeTable(page);

    // Hold the response open under our control -- fulfils only once this
    // test releases it, so we can inspect the dialog while the request is
    // still genuinely in flight. Body shape mirrors the real error render
    // (internal/pages/pos_api.go's table handler on a lost claim race sets
    // ToastMessage="basket.table.occupied"/ToastLevel="error", rendered by
    // basket.html into .pos-notice.error#toast-message), since hx-swap=
    // "outerHTML" replaces the whole #basket element either way.
    let releaseRoute!: () => void;
    const routeGate = new Promise<void>((resolve) => {
      releaseRoute = resolve;
    });
    await page.route('**/api/pos/table', async (route) => {
      await routeGate;
      await route.fulfill({
        status: 200,
        contentType: 'text/html',
        body:
          '<div class="basket" id="basket">' +
          '<div class="pos-notice error" id="toast-message" role="alert">' +
          '<span class="notice-text">simulated table claim failure</span>' +
          '<button type="button" class="notice-dismiss">✕</button></div>' +
          '<span id="table-picker" hx-get="/ui/pos/table-picker" hx-trigger="load" hx-swap="outerHTML"></span>' +
          '</div>',
      });
    });

    const optionBtn = page.locator('.table-picker-option', { hasText: TABLE_LABEL });
    await optionBtn.click();

    // The old unconditional onclick called #table-modal.close() the instant
    // the button was clicked, well before this still-pending request even
    // resolves — so under the pre-fix code this assertion fails here
    // (dialog already gone). Under the fix, nothing closes the dialog until
    // htmx:afterRequest fires on a real response, so it must still be open
    // while the request is deliberately held open.
    await expect(page.locator('#table-modal')).toBeVisible();

    releaseRoute();
    await page.waitForResponse((r) => r.url().includes('/api/pos/table'));
    await expect(page.locator('#toast-message.error')).toBeVisible();
  });

  test('clearing an assigned table closes #table-modal (unchanged behavior)', async ({ page }) => {
    await openPickerWithFreeTable(page);

    const optionBtn = page.locator('.table-picker-option', { hasText: TABLE_LABEL });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/table')),
      optionBtn.click(),
    ]);
    await expect(page.getByTestId('table-picker-open')).toContainText(TABLE_LABEL);

    await page.getByTestId('table-picker-open').click();
    await expect(page.locator('#table-modal')).toBeVisible();

    const clearBtn = page.locator('.table-picker-clear');
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/pos/table')),
      clearBtn.click(),
    ]);

    await expect(page.locator('#table-modal')).not.toBeVisible();
    await expect(page.getByTestId('table-picker-open')).not.toContainText(TABLE_LABEL);
  });
});
