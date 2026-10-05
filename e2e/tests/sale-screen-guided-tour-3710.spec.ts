import { test, expect } from './fixtures';
import type { Page, Locator } from '@playwright/test';
import { ensureOperator } from './helpers';

// ut-docs#3710: the sale screen's guided tour (web/public/tour.js,
// web/ui/partials/tour.html, internal/pages/tour.go). It starts by itself the
// first time a signed-in operator opens "/", walks real anchors with a
// balloon + arrow, and — finished or skipped — never starts by itself again
// for that operator; /?tour=1 (Help → Take the tour) always restarts it.
//
// Needs the `auth` project: the tour is per user, and the default project
// runs UT_AUTH=off (no user, so no automatic start — internal/pages
// tour_test.go pins that). Every other auth spec marks the tour done right
// after logging in (helpers.ts markTourDone); this one logs in as its own
// brand-new cashier so the tour is genuinely unseen. Named to sort AFTER
// login.spec.ts: the auth till is shared and serial.

const RUN = Date.now().toString(36).toUpperCase();
const CASHIER_USERNAME = `tour3710${RUN}`;
const CASHIER_PIN = '371035';

async function createCashier(page: Page): Promise<void> {
  const createResp = await page.request.post('/api/users', {
    form: { username: CASHIER_USERNAME, display_name: 'Tour 3710', role: 'cashier' },
  });
  expect(createResp.ok(), 'create cashier user').toBe(true);
  await page.goto('/users');
  const row = page.locator('tr', { hasText: CASHIER_USERNAME });
  const pinAction = await row.locator('form[hx-post$="/pin"]').getAttribute('hx-post');
  expect(pinAction, 'cashier row has a pin-set form').not.toBeNull();
  const id = pinAction!.match(/\/api\/users\/([^/]+)\/pin/)![1];
  const pinResp = await page.request.post(`/api/users/${id}/pin`, { form: { pin: CASHIER_PIN } });
  expect(pinResp.ok(), 'set cashier PIN').toBe(true);
}

async function loginAsCashier(page: Page): Promise<void> {
  await page.request.post('/api/auth/logout');
  await page.goto('/login');
  for (const d of CASHIER_PIN.split('')) {
    await page.locator('.pin-pad button').getByText(d, { exact: true }).click();
  }
  await page.locator('button[type=submit].pin-key').click();
  await page.waitForURL((u) => !u.pathname.includes('/login'));
}

type Box = { x: number; y: number; width: number; height: number };

async function box(l: Locator): Promise<Box> {
  const b = await l.boundingBox();
  expect(b, 'element has a box').not.toBeNull();
  return b!;
}

// The balloon sits next to its target on the side data-placement names, a
// short gap away, and overlaps it along the other axis (the arrow's span).
function expectAnchored(placement: string | null, bal: Box, t: Box) {
  const GAP_MAX = 40;
  const overlapX = Math.min(bal.x + bal.width, t.x + t.width) - Math.max(bal.x, t.x);
  const overlapY = Math.min(bal.y + bal.height, t.y + t.height) - Math.max(bal.y, t.y);
  switch (placement) {
    case 'below':
      expect(bal.y - (t.y + t.height)).toBeGreaterThanOrEqual(0);
      expect(bal.y - (t.y + t.height)).toBeLessThan(GAP_MAX);
      expect(overlapX).toBeGreaterThan(0);
      break;
    case 'above':
      expect(t.y - (bal.y + bal.height)).toBeGreaterThanOrEqual(0);
      expect(t.y - (bal.y + bal.height)).toBeLessThan(GAP_MAX);
      expect(overlapX).toBeGreaterThan(0);
      break;
    case 'right':
      expect(bal.x - (t.x + t.width)).toBeGreaterThanOrEqual(0);
      expect(bal.x - (t.x + t.width)).toBeLessThan(GAP_MAX);
      expect(overlapY).toBeGreaterThan(0);
      break;
    case 'left':
      expect(t.x - (bal.x + bal.width)).toBeGreaterThanOrEqual(0);
      expect(t.x - (bal.x + bal.width)).toBeLessThan(GAP_MAX);
      expect(overlapY).toBeGreaterThan(0);
      break;
    default:
      throw new Error(`unexpected placement ${placement}`);
  }
}

function expectInViewport(b: Box, w: number, h: number) {
  expect(b.x).toBeGreaterThanOrEqual(0);
  expect(b.y).toBeGreaterThanOrEqual(0);
  expect(b.x + b.width).toBeLessThanOrEqual(w);
  expect(b.y + b.height).toBeLessThanOrEqual(h);
}

test.describe('Sale-screen guided tour (ut-docs#3710)', () => {
  test.describe.configure({ mode: 'serial' });

  test('a new operator gets the tour; Next walks real anchors; Skip persists; ?tour=1 restarts', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await ensureOperator(page); // admin — wizard or PIN; marks the admin's tour done
    await createCashier(page);
    await loginAsCashier(page);

    // 1. Fresh operator on "/": the tour starts by itself.
    await page.goto('/');
    const balloon = page.locator('[data-testid=tour-balloon]');
    await expect(balloon).toBeVisible();
    await expect(balloon).toHaveAttribute('role', 'dialog');
    await expect(balloon).toHaveAttribute('data-step', 'intro');
    await expect(balloon).toHaveAttribute('data-placement', 'centre');
    await expect(page.locator('[data-testid=tour-next]')).toBeFocused(); // never an input
    await expect(page.locator('[data-testid=tour-back]')).toBeHidden();
    await expect(page.locator('[data-testid=tour-counter]')).toHaveText(/^1 \/ \d+$/);
    expectInViewport(await box(balloon), 1024, 600);

    // The backdrop is visual only: the status bar under it still takes the
    // tap (CLAUDE.md: status, lock and exit stay reachable).
    const conn = await box(page.locator('#sb-conn'));
    const hit = await page.evaluate(([x, y]) => {
      const el = document.elementFromPoint(x, y);
      return !!(el && el.closest('#sb-conn'));
    }, [conn.x + conn.width / 2, conn.y + conn.height / 2]);
    expect(hit, 'a tap on the status bar reaches it through the backdrop').toBe(true);

    // 2. Next → the balloon moves to the nav rail, beside it, arrow facing it.
    await page.locator('[data-testid=tour-next]').click();
    await expect(balloon).toHaveAttribute('data-step', 'nav');
    await expect(page.locator('[data-testid=tour-counter]')).toHaveText(/^2 \/ \d+$/);
    const navBox = await box(page.locator('.nav-primary'));
    expectAnchored(await balloon.getAttribute('data-placement'), await box(balloon), navBox);
    // The ring outlines that same target.
    const ring = await box(page.locator('[data-testid=tour-ring]'));
    expect(Math.abs(ring.x - navBox.x)).toBeLessThanOrEqual(8);
    expect(Math.abs(ring.width - navBox.width)).toBeLessThanOrEqual(14);
    await expect(page.locator('.ut-tour-arrow')).toBeVisible();

    // 3. → (keyboard) moves on again to a different anchor; ← comes back.
    await page.keyboard.press('ArrowRight');
    await expect(balloon).not.toHaveAttribute('data-step', 'nav');
    const third = await balloon.getAttribute('data-step');
    expect(['products', 'search', 'basket']).toContain(third);
    await page.keyboard.press('ArrowLeft');
    await expect(balloon).toHaveAttribute('data-step', 'nav');

    // 3b. Leaving mid-tour (a rail link, hx-boosted) is an interruption, not
    //     a skip: nothing is recorded and "/" offers the tour again (review
    //     finding — only Skip / Done / Esc count as seen).
    const donePosts: string[] = [];
    page.on('request', (r) => { if (r.url().endsWith('/api/tour/done')) donePosts.push(r.method()); });
    await page.locator('a[data-testid=nav-menu]').click();
    await expect.poll(() => new URL(page.url()).pathname).toBe('/menu');
    await expect(balloon).toHaveCount(0);
    await page.waitForTimeout(1000); // > the tour's 400 ms watch tick
    expect(donePosts, 'an interrupted tour must not be recorded as done').toEqual([]);
    await page.goto('/');
    await expect(balloon).toBeVisible();
    await expect(balloon).toHaveAttribute('data-step', 'intro');

    // 4. Skip → gone, and recorded for this operator: a reload doesn't
    //    bring it back (the server says so, and no balloon appears).
    const done = page.waitForResponse((r) => r.url().endsWith('/api/tour/done') && r.request().method() === 'POST');
    await page.locator('[data-testid=tour-skip]').click();
    expect((await done).status()).toBe(200);
    await expect(balloon).toHaveCount(0);
    await expect(page.locator('[data-testid=tour-ring]')).toHaveCount(0);
    await page.reload();
    await expect(page.locator('#basket')).toBeVisible();
    await expect(page.locator('#ut-tour-steps')).toHaveAttribute('data-start', '0');
    await page.waitForTimeout(1500); // longer than the tour's own settle wait on a ready page
    await expect(balloon).toHaveCount(0);

    // 5. /?tour=1 restarts it (and drops the parameter, so a reload doesn't).
    await page.goto('/?tour=1');
    await expect(balloon).toBeVisible();
    await expect(balloon).toHaveAttribute('data-step', 'intro');
    await expect.poll(() => new URL(page.url()).searchParams.get('tour')).toBeNull();
    // Esc skips.
    await page.keyboard.press('Escape');
    await expect(balloon).toHaveCount(0);
  });

  test('Done on the last step closes it; Help → Take the tour restarts it', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 600 });
    await loginAsCashier(page);
    await page.goto('/help');
    await page.locator('[data-testid=help-take-tour]').first().click();
    const balloon = page.locator('[data-testid=tour-balloon]');
    await expect(balloon).toBeVisible();
    // Walk to the end with Next; every anchored step stays inside the screen.
    for (let i = 0; i < 20; i++) {
      expectInViewport(await box(balloon), 1024, 600);
      if ((await balloon.getAttribute('data-step')) === 'end') break;
      await page.locator('[data-testid=tour-next]').click();
    }
    await expect(balloon).toHaveAttribute('data-step', 'end');
    await expect(page.locator('[data-testid=tour-next]')).toHaveText('Done');
    await page.locator('[data-testid=tour-next]').click();
    await expect(balloon).toHaveCount(0);
  });

  test('phone width: the menu step points at the ☰ button and the balloon fits', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 740 });
    await loginAsCashier(page);
    await page.goto('/?tour=1');
    const balloon = page.locator('[data-testid=tour-balloon]');
    await expect(balloon).toBeVisible();
    await page.locator('[data-testid=tour-next]').click();
    await expect(balloon).toHaveAttribute('data-step', 'nav');
    const toggle = await box(page.locator('.nav-drawer-toggle'));
    const b = await box(balloon);
    expectAnchored(await balloon.getAttribute('data-placement'), b, toggle);
    expectInViewport(b, 360, 740);
    expect(b.width).toBeLessThanOrEqual(360 - 32 + 1);
    await page.locator('[data-testid=tour-skip]').click();
    await expect(balloon).toHaveCount(0);
  });
});
