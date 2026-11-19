import { test, expect } from './fixtures';
import { ensureOperator, watchConsole } from './helpers';
import type { Page } from '@playwright/test';

// ut-docs#6118: at <=480px Lock lives in the ☰ drawer, and ☰ sat under the
// popup scrim (html.ut-scrim-on .nav is pointer-events:none), so a cashier
// could not reach Lock while a popup was open. Mirror the 1024px rail (#1999,
// #2873): ☰ lifted above the dim, the opened drawer floats over the popup,
// only Lock is live. Needs a real session (Lock only renders with one) -> auth project.

const POPUP = '#parked-orders-modal';

async function openPopup(page: Page) {
  await page.goto('/');
  await page.locator('.basket-phonebar').click();
  await page.getByTestId('parked-orders-open').click();
  await expect(page.locator(`${POPUP}[open]`)).toBeVisible();
  await expect(page.locator('html')).toHaveClass(/ut-scrim-on/);
}

// Drawer open over a popup: Lock is scrolled into the clear by app.js.
async function hit(page: Page, sel: string) {
  return page.evaluate((s) => {
    const el = document.querySelector(s) as HTMLElement;
    const r = el.getBoundingClientRect();
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return { hit: !!at && (at === el || el.contains(at)), at: at ? (at.id || at.className || at.tagName) : null, left: r.left, right: r.right };
  }, sel);
}

async function openDrawer(page: Page) {
  await page.locator('.nav-drawer-toggle').click(); // real click, no force
  await expect(page.locator('body')).toHaveClass(/nav-drawer-open/);
  await expect.poll(() => page.locator('#nav-drawer').evaluate((el) => getComputedStyle(el).transform)).toBe('none');
  // app.js scrolls Lock clear of the status foot a frame or two later.
  await expect.poll(async () => (await hit(page, '.session-lock .btn-lock')).hit).toBe(true);
}

test.describe('phone: Lock reachable over a scrim popup (#6118)', () => {
  test.use({ viewport: { width: 360, height: 640 }, hasTouch: true, isMobile: true });

  test('☰ -> Lock logs out; dead rows, Escape, status', async ({ page }) => {
    const done = watchConsole(page);
    await ensureOperator(page);
    await openPopup(page);
    await openDrawer(page);
    const lk = await hit(page, '.session-lock .btn-lock');
    expect(lk.hit, `Lock is the hit target (got ${lk.at})`).toBe(true);
    expect((await hit(page, '.statusbar')).hit, 'status bar is the hit target').toBe(true);
    // Focus lands on Lock; every other row is aria-disabled (A3).
    await expect(page.locator('.session-lock button')).toBeFocused();
    await expect(page.locator('#nav-drawer a[data-testid=help-hint]')).toHaveAttribute('aria-disabled', 'true');
    await expect(page.locator('.session-lock button')).not.toHaveAttribute('aria-disabled', 'true');
    // Dead row: tapping Help does nothing.
    const url = page.url();
    const help = page.locator('#nav-drawer [data-testid=help-hint]');
    const b = (await help.boundingBox())!;
    await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
    const at = await hit(page, '#nav-drawer [data-testid=help-hint]');
    expect(at.hit, 'Help row is not a hit target while the scrim is on').toBe(false);
    expect(page.url()).toBe(url);
    await expect(page.locator('body')).toHaveClass(/nav-drawer-open/);
    await expect(page.locator(`${POPUP}[open]`)).toBeVisible();
    // AT activation (synthetic click) on a dead row must not navigate either (F3).
    // htmx boosts the link asynchronously: count its requests, not just the URL.
    await page.evaluate(() => {
      (window as any).__req = 0;
      document.body.addEventListener('htmx:beforeRequest', () => { (window as any).__req++; });
    });
    await help.dispatchEvent('click');
    await page.waitForTimeout(500);
    expect(await page.evaluate(() => (window as any).__req)).toBe(0);
    expect(page.url()).toBe(url);
    await expect(page.locator(`${POPUP}[open]`)).toBeVisible();
    // Escape closes the drawer only.
    await page.keyboard.press('Escape');
    await expect(page.locator('body')).not.toHaveClass(/nav-drawer-open/);
    await expect(page.locator(`${POPUP}[open]`)).toBeVisible();
    // Lock.
    await openDrawer(page);
    await page.locator('.session-lock .btn-lock').click();
    await expect(page).toHaveURL(/\/login/);
    await expect(page.locator('.login-exit-os')).toBeVisible();
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/);
    done();
    await ensureOperator(page);
  });

  test('backdrop closes only the drawer', async ({ page }) => {
    await ensureOperator(page);
    await openPopup(page);
    await openDrawer(page);
    const w = page.viewportSize()!.width;
    await page.mouse.click(w - 5, 300); // inline-end of the 20rem drawer
    await expect(page.locator('body')).not.toHaveClass(/nav-drawer-open/);
    await expect(page.locator(`${POPUP}[open]`)).toBeVisible();
  });

  test('RTL: Lock hit at centre on the right half', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await openPopup(page);
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await openDrawer(page);
    const r = await hit(page, '.session-lock .btn-lock');
    expect(r.hit, `got ${r.at}`).toBe(true);
    expect((r.left + r.right) / 2).toBeGreaterThan(page.viewportSize()!.width / 2);
  });

  test('OSK-padded popup does not cover ☰ (A1)', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/');
    await page.evaluate(() => {
      document.body.classList.add('osk-padded');
      (document.getElementById('hold-modal') as HTMLDialogElement).show();
    });
    await expect(page.locator('html')).toHaveClass(/ut-scrim-on/);
    const top = await page.locator('#hold-modal').evaluate((el) => el.getBoundingClientRect().top);
    expect(top).toBeGreaterThanOrEqual(56);
    await page.locator('.nav-drawer-toggle').click();
    await expect(page.locator('body')).toHaveClass(/nav-drawer-open/);
  });

  test('focus ring on Lock (A2)', async ({ page }) => {
    await ensureOperator(page);
    await openPopup(page);
    // Open by keyboard so :focus-visible really matches.
    await page.locator('.nav-drawer-toggle').focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('body')).toHaveClass(/nav-drawer-open/);
    await expect(page.locator('.session-lock button')).toBeFocused();
    const ring = await page.locator('.session-lock button').evaluate((el) => {
      const s = getComputedStyle(el);
      return { fv: el.matches(':focus-visible'), style: s.outlineStyle, w: parseFloat(s.outlineWidth) };
    });
    expect(ring.fv).toBe(true);
    expect(ring.style).not.toBe('none');
    expect(ring.w).toBeGreaterThanOrEqual(3);
  });

  test('kiosk: no ☰ and no Lock', async ({ page }) => {
    await page.goto('/self-order');
    await expect(page.locator('.nav-drawer-toggle')).toHaveCount(0);
    await expect(page.locator('.session-lock')).toHaveCount(0);
  });
});

test.describe('1024x600 unchanged (#6118)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('rail z 460, Lock live, logout', async ({ page }) => {
    await ensureOperator(page);
    // Basket is visible at 1024: open Open orders directly.
    await page.goto('/');
    await page.getByTestId('parked-orders-open').click();
    await expect(page.locator(`${POPUP}[open]`)).toBeVisible();
    await expect(page.locator('.nav-drawer-toggle')).not.toBeVisible();
    expect(await page.locator('.nav').evaluate((el) => getComputedStyle(el).zIndex)).toBe('460');
    expect((await hit(page, '.nav .btn-lock')).hit).toBe(true);
    await page.locator('.nav .btn-lock').click();
    await expect(page).toHaveURL(/\/login/);
    await ensureOperator(page);
  });
});
