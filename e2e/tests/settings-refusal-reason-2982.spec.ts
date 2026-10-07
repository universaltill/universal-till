import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';

// ut-docs#2982: a refused Settings save shows the server's own, already
// translated reason inside the card that made the request, instead of only
// the page-wide "Could not save — please try again." banner. The reason is
// shown only when the server marks it X-UT-Response: refused
// (httpx.RefuseText); an unmarked text body is an untranslated developer
// string and still falls back to the generic banner.
//
// A real refusal needs an additional till whose main till is unreachable —
// the server side of that is pinned in Go
// (internal/pages/settings_refusal_reason_2982_test.go). Here the response
// is mocked at the network layer with exactly that shape (same approach as
// htmx-panel-swap-error-banner-2179.spec.ts).

const UNREACHABLE = "Can't reach the main till — change this setting on the main till, or try again when it's back.";

function refuse(body: string, status = 502, marked = true) {
  return (route: Route) =>
    route.fulfill({
      status,
      contentType: 'text/plain; charset=utf-8',
      headers: marked ? { 'X-UT-Response': 'refused' } : {},
      body: body + '\n',
    });
}

async function submitIdleLock(page: Page, status: number) {
  const card = page.locator('#settings-idle-lock');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/settings/idle-lock') && r.status() === status),
    card.locator('button[type=submit]').click(),
  ]);
  return card;
}

test.describe('Settings: a refused save shows the reason (ut-docs#2982)', () => {
  test('a marked refusal shows its reason in the card, not the generic banners', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-idle-lock');
    await page.route('**/api/settings/idle-lock', refuse(UNREACHABLE));
    const card = await submitIdleLock(page, 502);

    const reason = card.getByTestId('save-error-inline');
    await expect(reason).toBeVisible();
    await expect(reason).toHaveText(UNREACHABLE);
    await expect(reason).toHaveAttribute('role', 'alert');
    await expect(page.locator('#settings-save-error')).toBeHidden();
    await expect(page.locator('#pos-alert')).toBeHidden();
  });

  test('an unmarked text refusal keeps the generic banner and never shows the developer string', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-idle-lock');
    await page.route('**/api/settings/idle-lock', refuse('could not save', 500, false));
    const card = await submitIdleLock(page, 500);

    await expect(page.locator('#settings-save-error')).toBeVisible();
    await expect(card.getByTestId('save-error-inline')).toHaveCount(0);
    await expect(card).not.toContainText('could not save');
  });

  test('a later attempt clears the stale reason', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-idle-lock');
    await page.route('**/api/settings/idle-lock', refuse(UNREACHABLE));
    const card = await submitIdleLock(page, 502);
    await expect(card.getByTestId('save-error-inline')).toBeVisible();

    // The next try fails differently (unmarked): the old reason must not
    // linger next to the generic banner.
    await page.unroute('**/api/settings/idle-lock');
    await page.route('**/api/settings/idle-lock', refuse('could not save', 500, false));
    await submitIdleLock(page, 500);
    await expect(card.getByTestId('save-error-inline')).toHaveCount(0);
    await expect(page.locator('#settings-save-error')).toBeVisible();
  });

  test('a refused toggle reverts the box and shows the reason in its card', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-stock-tracking');
    const card = page.locator('#settings-stock-tracking');
    const cb = page.locator('#allow-negative-inventory-cb');
    const before = await cb.isChecked();
    await page.route('**/api/settings/allow-negative-inventory', refuse(UNREACHABLE));
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/allow-negative-inventory') && r.status() === 502),
      cb.click(),
    ]);
    await expect(cb).toBeChecked({ checked: before });
    await expect(card.getByTestId('save-error-inline')).toHaveText(UNREACHABLE);
    await expect(page.locator('#settings-save-error')).toBeHidden();
  });

  test('right-to-left: the reason sits in the card and reads in the page language', async ({ page }) => {
    await page.goto('/settings?lang=fa#settings-idle-lock');
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    const fa = 'دسترسی به صندوق اصلی ممکن نیست';
    await page.route('**/api/settings/idle-lock', refuse(fa));
    const card = await submitIdleLock(page, 502);
    const reason = card.getByTestId('save-error-inline');
    await expect(reason).toHaveText(fa);
    // Inside the card's box, on the reading side.
    const c = await card.boundingBox();
    const r = await reason.boundingBox();
    expect(c && r).toBeTruthy();
    expect(r!.x).toBeGreaterThanOrEqual(c!.x);
    expect(r!.x + r!.width).toBeLessThanOrEqual(c!.x + c!.width + 1);
    await expect(reason).toHaveCSS('text-align', 'start');
  });
  test('a card with several forms puts the reason right under the form that was refused', async ({ page }) => {
    // #settings-display holds seven forms; the end of the card is far
    // below the fold (review of ut-docs#2982).
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto('/settings?lang=en#settings-display');
    const form = page.locator('#settings-display form[hx-post="/api/settings/ui-scale"]');
    await page.route('**/api/settings/ui-scale', refuse(UNREACHABLE));
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/ui-scale') && r.status() === 502),
      form.locator('button[type=submit]').click(),
    ]);
    const reason = page.locator('#settings-display').getByTestId('save-error-inline');
    await expect(reason).toHaveText(UNREACHABLE);
    expect(await reason.evaluate((el) => el.previousElementSibling?.getAttribute('hx-post'))).toBe('/api/settings/ui-scale');
    await expect(reason).toBeInViewport();
  });

  test('the shop-name card shows the reason once', async ({ page }) => {
    await page.goto('/settings?lang=en#settings-store-name');
    const card = page.locator('#settings-store-name');
    await page.route('**/api/settings/store-name', refuse(UNREACHABLE));
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/settings/store-name') && r.status() === 502),
      card.getByTestId('store-name-save').click(),
    ]);
    await expect(card.getByTestId('save-error-inline')).toHaveText(UNREACHABLE);
    await expect(card.getByText(UNREACHABLE)).toHaveCount(1);
  });
});
