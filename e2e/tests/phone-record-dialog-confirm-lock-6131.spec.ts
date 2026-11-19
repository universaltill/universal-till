import { test, expect } from './fixtures';
import { ensureOperator, watchConsole, answerTillConfirm, noNativeDialogs } from './helpers';
import type { Page } from '@playwright/test';

// ut-docs#6131: at <=480px the till-confirm question (#ut-confirm) is pinned
// just under the top bar (#6118, to keep ☰ clear). Over a full-screen record
// dialog there is no ☰, and that offset lands the question on the dialog's own
// head, which holds the only Lock / exit-to-OS / status chip on screen. The
// question must sit clear of the head so Lock stays the hit target. Needs a
// real session (Lock only renders with one) -> auth project.

const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';
const LOCK = `${DIALOG} [data-record-dialog-lock] button`;

async function openDiscardQuestion(page: Page) {
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
  await page.locator(NAME).fill('Scratch');
  await page.locator(`${DIALOG} .record-dialog-close`).click();
  await expect(page.getByTestId('till-confirm')).toBeVisible();
}

async function geometry(page: Page) {
  return page.evaluate((lockSel) => {
    const q = document.getElementById('ut-confirm')!.getBoundingClientRect();
    const head = document.querySelector('#category-dialog .record-dialog-head')!.getBoundingClientRect();
    const lock = document.querySelector(lockSel) as HTMLElement;
    const lr = lock.getBoundingClientRect();
    const at = document.elementFromPoint(lr.left + lr.width / 2, lr.top + lr.height / 2);
    const sb = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--phone-sb-h')) || 0;
    return {
      qTop: q.top, qBottom: q.bottom, qLeft: q.left, qRight: q.right,
      headBottom: head.bottom, headH: head.height,
      lockHit: !!at && (at === lock || lock.contains(at)),
      at: at ? (at.id || at.className || at.tagName) : null,
      innerH: window.innerHeight, innerW: window.innerWidth, sb,
    };
  }, LOCK);
}

test.describe('phone: discard question clear of the record dialog head (#6131)', () => {
  test.use({ viewport: { width: 360, height: 640 }, hasTouch: true, isMobile: true });

  test('light: clear of head and status row, Cancel keeps edit, Lock logs out', async ({ page }) => {
    const done = watchConsole(page);
    const nativeOk = noNativeDialogs(page);
    await ensureOperator(page);
    await openDiscardQuestion(page);
    const g = await geometry(page);
    console.log('6131 geometry', JSON.stringify(g));
    expect(g.qTop, `question top ${g.qTop} >= head bottom ${g.headBottom}`).toBeGreaterThanOrEqual(g.headBottom);
    expect(g.lockHit, `Lock is the hit target (got ${g.at})`).toBe(true);
    expect(g.qBottom).toBeLessThanOrEqual(g.innerH - g.sb);

    await answerTillConfirm(page, 'cancel');
    await expect(page.locator(DIALOG)).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue('Scratch');

    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(page.getByTestId('till-confirm')).toBeVisible();
    await page.locator(LOCK).click(); // real click, no force
    await expect(page).toHaveURL(/\/login/);
    nativeOk();
    done();
    await ensureOperator(page);
  });

  test('long question scrolls inside the box, never under the head', async ({ page }) => {
    await ensureOperator(page);
    await openDiscardQuestion(page);
    await page.evaluate(() => {
      document.getElementById('ut-confirm-msg')!.textContent = 'Lorem ipsum dolor sit amet. '.repeat(60);
    });
    const g = await geometry(page);
    expect(g.qTop).toBeGreaterThanOrEqual(g.headBottom);
    expect(g.lockHit).toBe(true);
    expect(g.qBottom).toBeLessThanOrEqual(g.innerH - g.sb);
  });

  test('dark theme', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/categories');
    await page.evaluate(async () => {
      const link = document.getElementById('theme-css') as HTMLLinkElement;
      await new Promise((res) => { link.onload = res; link.setAttribute('href', '/themes/dark.css'); });
    });
    await page.locator('#categories-new').click();
    await page.locator(NAME).fill('Scratch');
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(page.getByTestId('till-confirm')).toBeVisible();
    const g = await geometry(page);
    expect(g.qTop).toBeGreaterThanOrEqual(g.headBottom);
    expect(g.lockHit, `got ${g.at}`).toBe(true);
  });

  test('RTL: clear of head, centred', async ({ page }) => {
    await ensureOperator(page);
    await page.goto('/categories');
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    await page.locator('#categories-new').click();
    await page.locator(NAME).fill('Scratch');
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(page.getByTestId('till-confirm')).toBeVisible();
    const g = await geometry(page);
    expect(g.qTop).toBeGreaterThanOrEqual(g.headBottom);
    expect(g.lockHit, `got ${g.at}`).toBe(true);
    expect(Math.abs(g.qLeft - (g.innerW - g.qRight))).toBeLessThanOrEqual(1);
  });

  test('osk-padded: ends above the reserved keyboard band', async ({ page }) => {
    await ensureOperator(page);
    await openDiscardQuestion(page);
    await page.evaluate(() => { document.body.classList.add('osk-padded'); });
    const g = await geometry(page);
    const reserved = await page.evaluate(() => {
      const v = getComputedStyle(document.body).paddingBlockEnd;
      return parseFloat(v) || 0;
    });
    expect(reserved).toBeGreaterThan(0);
    expect(g.qBottom).toBeLessThanOrEqual(g.innerH - reserved + 0.5);
    expect(g.qTop).toBeGreaterThanOrEqual(g.headBottom);
  });

  test('osk-padded + long question: still scrolls below the head', async ({ page }) => {
    await ensureOperator(page);
    await openDiscardQuestion(page);
    await page.evaluate(() => {
      document.body.classList.add('osk-padded');
      document.getElementById('ut-confirm-msg')!.textContent = 'Lorem ipsum dolor sit amet. '.repeat(60);
    });
    const g = await geometry(page);
    expect(g.qTop, `question top ${g.qTop} >= head bottom ${g.headBottom}`).toBeGreaterThanOrEqual(g.headBottom);
    expect(g.lockHit, `got ${g.at}`).toBe(true);
  });
});

test.describe('1024x600 unchanged (#6131)', () => {
  test.use({ viewport: { width: 1024, height: 600 } });

  test('question stays at 8vh', async ({ page }) => {
    await ensureOperator(page);
    await openDiscardQuestion(page);
    const top = await page.locator('#ut-confirm').evaluate((el) => el.getBoundingClientRect().top);
    expect(Math.abs(top - 48)).toBeLessThanOrEqual(1);
    await answerTillConfirm(page, 'cancel');
  });
});
