import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { answerTillConfirm, drainParkedOrders, noNativeDialogs, watchConsole } from './helpers';

// ut-docs#6117: an hx-confirm opener names its action on the dialog's confirm
// button (data-confirm-ok) and may rename Cancel (data-confirm-dismiss). The
// held/parked-order cancel is the case that motivated it: [Cancel][Cancel
// order] became [Keep order][Cancel order]. Labels reset on every ask.

test.afterEach(async ({ page }) => {
  await drainParkedOrders(page.request);
});

const dlg = (page: Page) => page.getByTestId('till-confirm');
const ok = (page: Page) => page.getByTestId('till-confirm-ok');
const cancel = (page: Page) => page.getByTestId('till-confirm-cancel');

async function probe(page: Page, html: string) {
  await page.evaluate((h) => {
    const host = document.createElement('div');
    host.style.cssText = 'position:fixed;inset-block-start:0;inset-inline-start:0;z-index:520;background:#fff;padding:2px';
    document.body.appendChild(host);
    host.innerHTML = h;
    (window as any).htmx.process(host);
  }, html);
}

async function parkASale(page: Page, label: string) {
  await page.locator('.scan-row input[name="code"]').fill('5000000000012');
  await page.locator('.scan-row button[type=submit]').click();
  await expect(page.locator('#basket')).toContainText('Coca-Cola');
  await page.locator('.tender-default-footer button', { hasText: 'Hold Sale' }).click();
  await expect(page.locator('#hold-modal')).toBeVisible();
  await page.locator('#hold-label-input').fill(label);
  await page.locator('#hold-modal button[type=submit]').click();
  await expect(page.locator('#hold-modal')).toBeHidden();
  await expect(page.locator('#basket')).not.toContainText('Coca-Cola');
}

// Counts POSTs to the cancel endpoint without answering them: the real
// server handles them, the counter only observes.
function countCancelPosts(page: Page) {
  let n = 0;
  page.on('request', (r) => { if (r.method() === 'POST' && r.url().includes('/api/pos/held/cancel')) n++; });
  return () => n;
}

async function labels(page: Page) {
  return [(await cancel(page).textContent())?.trim(), (await ok(page).textContent())?.trim()];
}

test.describe('confirm verb labels (ut-docs#6117)', () => {
  test('open orders: [Keep order][Cancel order]; Keep order and Escape send nothing, Cancel order sends one POST', async ({ page }) => {
    const assertClean = watchConsole(page);
    const none = noNativeDialogs(page);
    await page.goto('/');
    await drainParkedOrders(page.request);
    await page.reload();
    await parkASale(page, 'Six one one seven');
    const posts = countCancelPosts(page);

    await page.goto('/open-orders?tab=hold');
    const cancelBtn = page.getByTestId('open-order-cancel');
    await expect(cancelBtn).toHaveCount(1);

    await cancelBtn.click();
    await expect(dlg(page)).toBeVisible();
    expect(await labels(page)).toEqual(['Keep order', 'Cancel order']);
    // Cancel sits at inline-start, the confirm at inline-end (LTR here).
    const [cx, ox] = await page.evaluate(() => [
      document.querySelector('[data-ut-confirm="cancel"]')!.getBoundingClientRect().left,
      document.querySelector('[data-ut-confirm="ok"]')!.getBoundingClientRect().left,
    ]);
    expect(cx).toBeLessThan(ox);
    // Danger dialog: the safe answer (Keep order) has focus.
    await expect(cancel(page)).toBeFocused();
    await answerTillConfirm(page, 'cancel');
    expect(posts()).toBe(0);
    await expect(page.locator('#open-orders-table tbody tr[data-held-id]')).toHaveCount(1);

    await cancelBtn.click();
    await answerTillConfirm(page, 'escape');
    expect(posts()).toBe(0);
    await expect(page.locator('#open-orders-table tbody tr[data-held-id]')).toHaveCount(1);

    await Promise.all([page.waitForURL(/open_orders\.cancel\.done/), (async () => {
      await cancelBtn.click();
      await answerTillConfirm(page, 'confirm');
    })()]);
    expect(posts()).toBe(1);
    await expect(page.getByTestId('open-orders-empty')).toBeVisible();
    none();
    assertClean();
  });

  test('parked orders popup: same labels, one POST on Cancel order', async ({ page }) => {
    const assertClean = watchConsole(page);
    const none = noNativeDialogs(page);
    await page.goto('/');
    await drainParkedOrders(page.request);
    await page.reload();
    await parkASale(page, 'Popup order');
    const posts = countCancelPosts(page);

    await page.getByTestId('parked-orders-open').click();
    const modal = page.locator('#parked-orders-modal');
    await expect(modal.locator('.parked-order')).toHaveCount(1);
    const btn = modal.locator('.parked-order-cancel');

    await btn.click();
    await expect(dlg(page)).toBeVisible();
    expect(await labels(page)).toEqual(['Keep order', 'Cancel order']);
    await answerTillConfirm(page, 'cancel');
    expect(posts()).toBe(0);
    await expect(modal.locator('.parked-order')).toHaveCount(1);

    await btn.click();
    await answerTillConfirm(page, 'confirm');
    await expect(modal.locator('.parked-order')).toHaveCount(0);
    expect(posts()).toBe(1);
    none();
    assertClean();
  });

  test('labels never leak into the next question', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.route('**/__ut6117/**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: 'done' }));
    await page.goto('/');
    await probe(page,
      '<button id="a" hx-post="/__ut6117/a" hx-confirm="A?" data-confirm-ok="Remove it" data-confirm-dismiss="Keep it">a</button>'
      + '<button id="b" hx-post="/__ut6117/b" hx-confirm="B?">b</button>'
      + '<button id="c" hx-post="/__ut6117/c" hx-confirm="C?" data-confirm-ok="Only ok">c</button>'
      + '<div data-confirm-ok="Wrapped ok" data-confirm-dismiss="Wrapped no"><button id="d" hx-post="/__ut6117/d" hx-confirm="D?">d</button></div>');

    await page.locator('#a').click();
    await expect(dlg(page)).toBeVisible();
    expect(await labels(page)).toEqual(['Keep it', 'Remove it']);
    await answerTillConfirm(page, 'cancel');

    await page.locator('#b').click();
    await expect(dlg(page)).toBeVisible();
    expect(await labels(page)).toEqual(['Cancel', 'Confirm']);
    await answerTillConfirm(page, 'cancel');

    await page.locator('#c').click();
    await expect(dlg(page)).toBeVisible();
    expect(await labels(page)).toEqual(['Cancel', 'Only ok']);
    await answerTillConfirm(page, 'cancel');

    // closest(): an ancestor carries the labels too.
    await page.locator('#d').click();
    await expect(dlg(page)).toBeVisible();
    expect(await labels(page)).toEqual(['Wrapped no', 'Wrapped ok']);
    await answerTillConfirm(page, 'cancel');

    await page.locator('#b').click();
    await expect(dlg(page)).toBeVisible();
    expect(await labels(page)).toEqual(['Cancel', 'Confirm']);
    await answerTillConfirm(page, 'cancel');
    none();
  });

  test('360x640: long verb pair stays inside the dialog with >=44px targets', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.setViewportSize({ width: 360, height: 640 });
    await page.goto('/');
    // German-length labels, forced: the de pack may not be loaded locally.
    await probe(page,
      '<button id="g" hx-post="/__ut6117/g" hx-confirm="Bestellung wirklich stornieren?" '
      + 'data-confirm-ok="Bestellung stornieren" data-confirm-dismiss="Bestellung behalten">g</button>');
    await page.locator('#g').click();
    await expect(dlg(page)).toBeVisible();
    const g = await page.evaluate(() => {
      const d = document.getElementById('ut-confirm')!;
      const r = (s: string) => document.querySelector(s)!.getBoundingClientRect();
      const o = r('[data-ut-confirm="ok"]'), c = r('[data-ut-confirm="cancel"]'), dr = d.getBoundingClientRect();
      return {
        dialogH: d.scrollWidth <= d.clientWidth, hscroll: document.documentElement.scrollWidth > document.documentElement.clientWidth,
        oh: o.height, ch: c.height, ow: o.width, cw: c.width,
        inside: o.left >= dr.left - 1 && o.right <= dr.right + 1 && c.left >= dr.left - 1 && c.right <= dr.right + 1,
        okClipped: (document.querySelector('[data-ut-confirm="ok"]') as HTMLElement).scrollWidth > (document.querySelector('[data-ut-confirm="ok"]') as HTMLElement).clientWidth,
        cancelClipped: (document.querySelector('[data-ut-confirm="cancel"]') as HTMLElement).scrollWidth > (document.querySelector('[data-ut-confirm="cancel"]') as HTMLElement).clientWidth,
      };
    });
    expect(g.dialogH).toBe(true);
    expect(g.hscroll).toBe(false);
    expect(g.inside).toBe(true);
    expect(g.okClipped).toBe(false);
    expect(g.cancelClipped).toBe(false);
    expect(g.oh).toBeGreaterThanOrEqual(44);
    expect(g.ch).toBeGreaterThanOrEqual(44);
    expect(g.ow).toBeGreaterThanOrEqual(44);
    expect(g.cw).toBeGreaterThanOrEqual(44);
    await expect(ok(page)).toBeVisible();
    await expect(cancel(page)).toBeVisible();
    await answerTillConfirm(page, 'cancel');
    none();
  });
});
