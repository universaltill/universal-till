import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';

// ut-docs#2762: a successful save on /users and /shifts used to call
// window.location.reload(), re-rendering the whole shell (a visible
// flash on the till). It now swaps only the changed region
// (UT.refreshRegion, web/public/app.js). The proof that no reload
// happened is a marker set on `window` before the action: a real reload
// gives a fresh window without it.

async function markWindow(page: Page) {
  await page.evaluate(() => { (window as any).__ut2762 = 'still-here'; });
}

async function expectSameWindow(page: Page) {
  expect(await page.evaluate(() => (window as any).__ut2762), 'page was reloaded').toBe('still-here');
}

test('users: create and deactivate update the list in place, no page reload', async ({ page }) => {
  const username = `swap2762_${Date.now()}`;
  await page.goto('/users');
  await markWindow(page);

  const form = page.locator('form[hx-post="/api/users"]');
  await form.locator('input[name="username"]').fill(username);
  await form.locator('input[name="display_name"]').fill('Swap 2762');
  await form.locator('select[name="role"]').selectOption('cashier');
  await form.locator('button[type=submit]').click();

  const row = page.locator('#users-list tr', { hasText: username });
  await expect(row).toHaveCount(1);
  await expect(page.locator('#new-user-msg')).toContainText('Saved.');
  await expect(form.locator('input[name="username"]')).toHaveValue(''); // form cleared, as a reload did
  await expectSameWindow(page);

  // Only the triggering row's message survives the swap: a stale message
  // planted in another row is dropped, as a reload dropped it.
  const otherMsg = page.locator('#users-list tr', { hasNotText: username }).locator('div[id^="user-msg-"]').first();
  await otherMsg.evaluate((m) => { m.textContent = 'stale-2762'; });

  // A row action: the row re-renders (Deactivate -> Activate) and its own
  // confirmation survives the swap.
  await expect(row.locator('td').nth(3)).toContainText('active');
  await row.locator('form[hx-post$="/active"] button[type=submit]').click();
  await expect(row.locator('td').nth(3)).toContainText('inactive');
  await expect(row.locator('div[id^="user-msg-"]')).toContainText('Saved.');
  await expect(page.locator('#users-list')).not.toContainText('stale-2762');
  // The swapped-in forms are live htmx forms again: activate it back.
  await row.locator('form[hx-post$="/active"] button[type=submit]').click();
  await expect(row.locator('td').nth(3)).not.toContainText('inactive');
  await expectSameWindow(page);
});

test('shifts: open and close swap the page region in place, no page reload', async ({ page }) => {
  await page.goto('/shifts');
  if (await page.locator('#close-shift-form').count()) {
    await page.evaluate(async () => {
      const shiftId = (document.querySelector('#close-shift-form input[name="shift_id"]') as HTMLInputElement).value;
      await fetch('/api/shifts/close', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ shift_id: shiftId, closing_cash: '0' }),
      });
    });
    await page.goto('/shifts');
  }
  await markWindow(page);

  await page.locator('#opening-cash').fill('10.00');
  await page.locator('#open-shift-form button[type=submit]').click();
  await expect(page.locator('#close-shift-form')).toBeVisible();
  await expect(page.locator('#open-shift-form')).toHaveCount(0);
  await expect(page.locator('#shift-result')).not.toBeEmpty();
  await expectSameWindow(page);

  // The denomination grid arrived by swap, not page load: its delegated
  // change listener must still build count_protocol.
  await page.locator('#close-shift-form details.catalog-extra summary').first().click();
  const firstDenom = page.locator('#denom-grid .denom-count').first();
  const denom = await firstDenom.getAttribute('data-denom');
  await firstDenom.fill('2');
  await firstDenom.dispatchEvent('change');
  await expect(page.locator('#count-protocol')).toHaveValue(JSON.stringify({ [denom!]: 2 }));

  await page.locator('#closing-cash').fill('10.00');
  await page.locator('#close-shift-form button[type=submit]').click();
  await expect(page.locator('#open-shift-form')).toBeVisible();
  await expect(page.locator('#close-shift-form')).toHaveCount(0);
  await expectSameWindow(page);
});
