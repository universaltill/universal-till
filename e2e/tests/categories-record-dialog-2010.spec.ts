import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { watchConsole } from './helpers';

// ut-docs#2010: /categories is the reference implementation of the app-wide
// list/edit standard (ut-docs/reference/list-and-dialog-pattern.md):
//   (a) the list header's icon-only New button opens the record dialog
//       empty, in create mode, posting to the create endpoint;
//   (b) tapping a row opens it prefilled, in edit mode, posting to that
//       row's own endpoint, with the destructive control visible;
//   (c) Escape (and Close) close a clean dialog silently, and ask first
//       when the form has unsaved changes — settling ut-docs#1999 for every
//       dialog on this pattern (they are .show()n, so Escape is inert
//       unless implemented by hand);
//   (d) the search box filters rows client-side and shows the translated
//       no-results row when nothing matches;
//   (e) at the 1024×600 kiosk floor and at 360px the head is pinned and
//       every head control is fully inside the viewport — real geometry,
//       not "an element exists". Deliberately NOT scrollWidth vs
//       clientWidth on the body: a hidden-overflow box always reports them
//       equal (the false pass ut-docs#1956's review caught last week).

const DIALOG = '#category-dialog';
const NAME = '#category-form input[name="name"]';

async function createCategory(page: Page, name: string) {
  await page.locator('#categories-new').click();
  await expect(page.locator(DIALOG)).toBeVisible();
  await page.locator(NAME).fill(name);
  await Promise.all([
    page.waitForURL(/\/categories$/),
    page.locator(`${DIALOG} .record-dialog-save`).click(),
  ]);
  await expect(page.locator('#categories-table .category-row', { hasText: name })).toHaveCount(1);
}

function row(page: Page, name: string) {
  return page.locator('#categories-table .category-row', { hasText: name }).first();
}

test.describe('categories list + record dialog (ut-docs#2010)', () => {

  test('(a) New opens the dialog empty in create mode', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    // Nothing steals focus on load.
    expect(await page.evaluate(() => document.activeElement === document.body)).toBe(true);
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeHidden();

    const newBtn = page.locator('#categories-new');
    await expect(newBtn).toHaveAttribute('aria-label', /.+/);
    await expect(newBtn).toHaveAttribute('title', /.+/);
    await expect(newBtn).toHaveText('');                 // icon-only: no caption
    await newBtn.click();

    await expect(dlg).toBeVisible();
    await expect(dlg).toHaveAttribute('data-record-mode', 'create');
    await expect(dlg).toHaveAttribute('open', '');
    await expect(page.locator(`${DIALOG} [data-record-dialog-title]`)).toHaveText('Create category');
    await expect(page.locator(NAME)).toHaveValue('');
    await expect(page.locator(NAME)).toBeFocused();
    await expect(page.locator('#category-form')).toHaveAttribute('action', '/api/categories');
    await expect(page.locator(`${DIALOG} [data-record-dialog-destructive]`)).toBeHidden();
    // Opened with show(), not showModal(): no top layer, so the OSK (a
    // <body> child) stays reachable. A modal dialog matches :modal.
    expect(await dlg.evaluate((d) => d.matches(':modal'))).toBe(false);

    // osk.js's central guardSweep (ut-docs#1022) covers the dialog's fields
    // because they are in the DOM at page load — verified, not assumed: no
    // per-field inputmode="none" is written in the template, yet the field
    // carries it. And when the operator taps the field the keyboard shows
    // (opening is click-only) and the dialog shortens to stay clear of it
    // (body.osk-padded), so a field can never sit under the keyboard.
    await expect(page.locator(NAME)).toHaveAttribute('inputmode', 'none');
    await page.locator(NAME).click();
    await expect(page.locator('#osk')).toBeVisible();
    await expect(page.locator('body')).toHaveClass(/osk-padded/);
    const vp = page.viewportSize()!;
    const shrunk = (await dlg.boundingBox())!;
    const osk = (await page.locator('#osk').boundingBox())!;
    const field = (await page.locator(NAME).boundingBox())!;
    // Shortened by the reserved band (--osk-reserved-height, set by osk.js
    // from the real keyboard height since universal-till#1027 / ut-docs#1998;
    // the CSS fallback is 17rem, matching every other reservation). This
    // asserts the property the operator actually needs — the field being
    // typed into is above the keyboard — not the dialog's bottom edge.
    expect(shrunk.height).toBeLessThan(vp.height - 200);
    expect(field.y + field.height, 'the focused field must sit above the keyboard').toBeLessThanOrEqual(osk.y);
    assertClean();
  });

  test('(b) tapping a row opens it prefilled in edit mode with that row\'s action', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const name = 'Edit Probe ' + Date.now();
    await createCategory(page, name);
    const r = row(page, name);
    const id = await r.getAttribute('data-id');
    expect(id).toBeTruthy();

    // The row's first cell — not a button inside it.
    await r.locator('td').first().click();
    const dlg = page.locator(DIALOG);
    await expect(dlg).toBeVisible();
    await expect(dlg).toHaveAttribute('data-record-mode', 'edit');
    await expect(page.locator(`${DIALOG} [data-record-dialog-title]`)).toHaveText('Edit category');
    await expect(page.locator(NAME)).toHaveValue(name);
    await expect(page.locator('#category-form')).toHaveAttribute('action', `/api/categories/${id}`);
    const destructive = page.locator(`${DIALOG} [data-record-dialog-destructive]`);
    await expect(destructive).toBeVisible();
    // Active row: the trash (deactivate) form shows, the activate one does not.
    const trash = destructive.locator('form[data-record-when="active=1"]');
    await expect(trash).toBeVisible();
    await expect(trash).toHaveAttribute('action', `/api/categories/${id}/active`);
    await expect(trash.locator('button')).toHaveAttribute('aria-label', /.+/);
    await expect(destructive.locator('form[data-record-when="active=0"]')).toBeHidden();
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();

    // A reorder button inside the row keeps its own job: it must NOT open.
    // The real POST is driven, not stubbed (ut-docs#2018: the script sends
    // multipart FormData and the handler used to ParseForm only → 400; a
    // stub here hid that from the suite).
    const [reorderRes] = await Promise.all([
      page.waitForResponse((res) => res.url().includes('/api/categories/reorder')),
      r.locator('.move-up').click(),
    ]);
    expect(reorderRes.status(), 'reorder must accept the multipart body the browser sends').toBe(204);
    await expect(dlg).toBeHidden();
    // The pencil is the explicit (keyboard-reachable) path to the same edit.
    await r.locator('[data-record-edit]').focus();
    await page.keyboard.press('Enter');
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toHaveValue(name);
    assertClean();
  });

  test('(b2) saving a rename from the dialog persists it', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const name = 'Rename Probe ' + Date.now();
    await createCategory(page, name);
    await row(page, name).locator('td').first().click();
    await page.locator(NAME).fill(name + ' B');
    await Promise.all([
      page.waitForURL(/\/categories$/),
      page.locator(`${DIALOG} .record-dialog-save`).click(),
    ]);
    await expect(row(page, name + ' B')).toHaveCount(1);
    await expect(page.locator('.login-error')).toHaveCount(0);
    assertClean();
  });

  test('(b3) the trash button deactivates (after a confirm) and an inactive row offers Activate', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const name = 'Deactivate Probe ' + Date.now();
    await createCategory(page, name);
    await row(page, name).locator('td').first().click();
    let msg = '';
    page.once('dialog', (d) => { msg = d.message(); d.accept(); });
    await Promise.all([
      page.waitForURL(/\/categories$/),
      page.locator(`${DIALOG} form[data-record-when="active=1"] button`).click(),
    ]);
    expect(msg).toContain('Deactivate');
    await expect(row(page, name)).toContainText('inactive');

    // Reopen: no trash icon that would quietly reactivate — Activate instead.
    await row(page, name).locator('td').first().click();
    await expect(page.locator(`${DIALOG} form[data-record-when="active=1"]`)).toBeHidden();
    const activate = page.locator(`${DIALOG} form[data-record-when="active=0"] button`);
    await expect(activate).toBeVisible();
    await Promise.all([page.waitForURL(/\/categories$/), activate.click()]);
    await expect(row(page, name)).toContainText(/\bactive\b/);
    await expect(row(page, name)).not.toContainText('inactive');
    assertClean();
  });

  test('(c) Escape closes a clean dialog silently and asks first when dirty', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const dlg = page.locator(DIALOG);
    await page.locator('#categories-new').click();
    await expect(dlg).toBeVisible();

    // Clean: no confirm, just closes.
    let prompts = 0;
    page.on('dialog', (d) => { prompts++; d.dismiss(); });
    await page.locator(NAME).press('Escape');
    await expect(dlg).toBeHidden();
    expect(prompts).toBe(0);

    // Dirty + dismiss the confirm: stays open with the text intact.
    await page.locator('#categories-new').click();
    await expect(dlg).toBeVisible();
    await page.locator(NAME).fill('unsaved text');
    await page.locator(NAME).press('Escape');
    await expect(dlg).toBeVisible();
    expect(prompts).toBe(1);
    await expect(page.locator(NAME)).toHaveValue('unsaved text');

    // Dirty + accept: closes. Same guard on the Close button.
    page.removeAllListeners('dialog');
    let msg = '';
    page.once('dialog', (d) => { msg = d.message(); d.accept(); });
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();
    // The resolved English value, not /discard/i — that regex also matched
    // the raw key name `common.discard_confirm`, so a missing translation
    // passed (review nit).
    expect(msg).toBe('Discard your unsaved changes?');
    assertClean();
  });

  test('(c2) opening create over a dirty edit asks first — open() honours the same guard as close()', async ({ page }) => {
    // Review S1: open() used to skip the discard guard, so New over an
    // edit with unsaved text silently emptied the field and flipped mode.
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const name = 'Dirty Probe ' + Date.now();
    await createCategory(page, name);
    const dlg = page.locator(DIALOG);
    await row(page, name).locator('td').first().click();
    await expect(dlg).toHaveAttribute('data-record-mode', 'edit');
    await page.locator(NAME).fill(name + ' edited');

    // dispatchEvent, not click(): the opaque full-bleed dialog covers New,
    // so no pointer reaches it — but before the focus trap the keyboard
    // could (Tab out, Enter), and the JS is shared by nine screens.
    let prompts = 0;
    page.on('dialog', (d) => { prompts++; d.dismiss(); });
    await page.locator('#categories-new').dispatchEvent('click');
    expect(prompts, 'New over a dirty edit must ask').toBe(1);
    await expect(dlg).toHaveAttribute('data-record-mode', 'edit');
    await expect(page.locator(NAME)).toHaveValue(name + ' edited');

    page.removeAllListeners('dialog');
    page.once('dialog', (d) => d.accept());
    await page.locator('#categories-new').dispatchEvent('click');
    await expect(dlg).toHaveAttribute('data-record-mode', 'create');
    await expect(page.locator(NAME)).toHaveValue('');
    assertClean();
  });

  test('(c3) form actions never carry over from a previously opened row', async ({ page }) => {
    // Review B1: the fallbacks read the LIVE action attribute, which a
    // previous open() had already overwritten — so a row with no
    // data-record-action, or New with data-create-action="", posted to the
    // last-opened record and Save silently overwrote it. Not reachable on
    // /categories' own markup, so the mis-authored rows are made here; the
    // shared JS is what nine screens copy.
    const assertClean = watchConsole(page, /data-record-action/);
    await page.goto('/categories');
    const stamp = Date.now();
    const nameA = `Carry A ${stamp}`;
    const nameB = `Carry B ${stamp}`;
    await createCategory(page, nameA);
    await createCategory(page, nameB);
    const a = row(page, nameA);
    const b = row(page, nameB);
    const idA = await a.getAttribute('data-id');
    const dlg = page.locator(DIALOG);
    const form = page.locator('#category-form');
    const trash = page.locator(`${DIALOG} form[data-record-when="active=1"]`);

    // 1. New after an edit, with the create action blanked: must fall back
    //    to the SERVER-rendered default, never to row A's action.
    await a.locator('td').first().click();
    await expect(form).toHaveAttribute('action', `/api/categories/${idA}`);
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();
    await dlg.evaluate((d) => d.setAttribute('data-create-action', ''));
    await page.locator('#categories-new').click();
    await expect(dlg).toHaveAttribute('data-record-mode', 'create');
    await expect(form, 'create mode must not point at the last-edited record').toHaveAttribute('action', '/api/categories');
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();

    // 2. A row with an action but NO destructive action: the destructive
    //    forms must be cleared, not left pointing at row A's.
    await a.locator('td').first().click();
    await expect(trash).toHaveAttribute('action', `/api/categories/${idA}/active`);
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();
    await b.evaluate((el) => el.removeAttribute('data-record-destructive-action'));
    await b.locator('td').first().click();
    await expect(dlg).toBeVisible();
    // Cleared outright (attribute removed), not merely different from A's.
    expect(await trash.getAttribute('action'), 'destructive action carried over from row A').toBeNull();
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();

    // 3. A row with data-record-open but NO data-record-action is a page
    //    authoring bug: refuse to open, and say so on the console.
    const errors: string[] = [];
    page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
    await b.evaluate((el) => el.removeAttribute('data-record-action'));
    await b.locator('td').first().click();
    await expect(dlg).toBeHidden();
    expect(errors.filter((e) => /data-record-action/.test(e)), 'a console.error naming the missing attribute').toHaveLength(1);
    assertClean();
  });

  test('(c4) keyboard focus stays inside the open dialog and returns to the opener on close', async ({ page }) => {
    // Review B3: from the name field, Tab used to walk out under the
    // opaque dialog (nav rail, search box, row buttons) and never reach
    // Close/Save — WCAG 2.4.3/2.4.7 and rule 3 of the pattern doc. The
    // dialog is .show()n (not modal, for the OSK — ut-docs#1385), so the
    // trap is by hand: Tab/Shift+Tab cycle, a focusin backstop, #osk
    // whitelisted, focus restored to the opener on close.
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const name = 'Trap Probe ' + Date.now();
    await createCategory(page, name);
    const dlg = page.locator(DIALOG);
    const opener = row(page, name).locator('[data-record-edit]');
    await opener.focus();
    await page.keyboard.press('Enter');
    await expect(dlg).toBeVisible();
    await expect(page.locator(NAME)).toBeFocused();

    const where = () => page.evaluate(() => {
      const a = document.activeElement as HTMLElement | null;
      const d = document.getElementById('category-dialog')!;
      const osk = document.getElementById('osk');
      const desc = !a ? 'null' : a.tagName.toLowerCase() + (a.id ? '#' + a.id : '') +
        (a.className ? '.' + String(a.className).trim().split(/\s+/).join('.') : '');
      return { inside: !!a && (d.contains(a) || !!(osk && osk.contains(a))), desc };
    });
    const seen: string[] = [];
    for (let i = 1; i <= 12; i++) {
      await page.keyboard.press('Tab');
      const w = await where();
      expect(w.inside, `Tab #${i} landed on ${w.desc}, outside the open dialog`).toBe(true);
      seen.push(w.desc);
    }
    expect(seen.some((d) => d.includes('record-dialog-close')), `Close never reached: ${seen.join(' → ')}`).toBe(true);
    expect(seen.some((d) => d.includes('record-dialog-save')), `Save never reached: ${seen.join(' → ')}`).toBe(true);
    expect(seen.some((d) => d.includes('btn-icon-danger')), `the trash button never reached: ${seen.join(' → ')}`).toBe(true);
    for (let i = 1; i <= 12; i++) {
      await page.keyboard.press('Shift+Tab');
      const w = await where();
      expect(w.inside, `Shift+Tab #${i} landed on ${w.desc}, outside the open dialog`).toBe(true);
    }

    // The focusin backstop: focus programmatically forced onto the page
    // behind (what a stray tap on the shortened-for-OSK dialog's uncovered
    // strip could do) is pulled straight back inside.
    await page.evaluate(() => (document.getElementById('categories-search') as HTMLElement).focus());
    const pulled = await where();
    expect(pulled.inside, `focus forced outside stayed on ${pulled.desc}`).toBe(true);

    // Close (via Escape) hands focus back to the button that opened it,
    // and the trap is gone: the next Tab moves on normally.
    await page.keyboard.press('Escape');
    await expect(dlg).toBeHidden();
    await expect(opener).toBeFocused();
    await page.keyboard.press('Tab');
    const after = await where();
    expect(after.inside, 'trap still active after close').toBe(false);
    expect(after.desc).not.toContain('data-record-edit');

    // Same restore for the New button.
    await page.locator('#categories-new').focus();
    await page.keyboard.press('Enter');
    await expect(dlg).toBeVisible();
    await page.locator(`${DIALOG} .record-dialog-close`).click();
    await expect(dlg).toBeHidden();
    await expect(page.locator('#categories-new')).toBeFocused();
    assertClean();
  });

  test('(c5) a reorder made with the arrow buttons survives a reload', async ({ page }) => {
    // Review S4 / ut-docs#2018: the DOM swap happens client-side BEFORE the
    // request, so "the rows swapped" passes against a 400. Only the order
    // after a reload proves the server accepted the multipart body.
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const stamp = Date.now();
    const first = `Order A ${stamp}`;
    const second = `Order B ${stamp}`;
    await createCategory(page, first);
    await createCategory(page, second);
    const indexOf = async (n: string) =>
      page.locator('#categories-table .category-row').evaluateAll(
        (els, needle) => els.findIndex((e) => (e.textContent || '').includes(needle)), n);
    expect(await indexOf(second)).toBe((await indexOf(first)) + 1);

    const [res] = await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/categories/reorder')),
      row(page, second).locator('.move-up').click(),
    ]);
    expect(res.status()).toBe(204);
    await page.reload();
    expect(await indexOf(second), 'order after reload').toBe((await indexOf(first)) - 1);
    assertClean();
  });

  test('(d) the search box filters rows and shows the no-results row', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const stamp = Date.now();
    await createCategory(page, `Filter Apple ${stamp}`);
    await createCategory(page, `Filter Banana ${stamp}`);
    const rows = page.locator('#categories-table .category-row');
    const total = await rows.count();
    expect(total).toBeGreaterThanOrEqual(2);
    const noResults = page.locator('#categories-no-results');
    await expect(noResults).toBeHidden();

    const q = page.locator('#categories-search');
    await expect(q).toHaveAttribute('aria-label', /.+/);
    await q.fill('filter apple');                       // case-insensitive
    await expect(row(page, `Filter Apple ${stamp}`)).toBeVisible();
    await expect(row(page, `Filter Banana ${stamp}`)).toBeHidden();
    expect(await rows.evaluateAll((els) => els.filter((e) => !(e as HTMLElement).hidden).length)).toBe(1);
    await expect(noResults).toBeHidden();

    await q.fill('zzzz-no-such-category');
    expect(await rows.evaluateAll((els) => els.filter((e) => !(e as HTMLElement).hidden).length)).toBe(0);
    await expect(noResults).toBeVisible();
    await expect(noResults).toContainText(/no matches/i);

    await q.fill('');
    expect(await rows.evaluateAll((els) => els.filter((e) => !(e as HTMLElement).hidden).length)).toBe(total);
    await expect(noResults).toBeHidden();
    assertClean();
  });

  for (const vp of [
    { width: 1024, height: 600, label: 'kiosk floor 1024x600', oneRow: true },
    { width: 360, height: 740, label: 'phone 360px', oneRow: false },
  ]) {
    test(`(e) at ${vp.label} the dialog is full-bleed, the head is pinned and every control is on screen`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/categories');
      const name = 'Viewport Probe ' + Date.now();
      await createCategory(page, name);

      // The list header itself fits: search + New inside the viewport, and
      // the New button is a real ≥3rem-ish square.
      const newBox = (await page.locator('#categories-new').boundingBox())!;
      expect(newBox.x + newBox.width).toBeLessThanOrEqual(vp.width + 0.5);
      expect(newBox.width).toBeGreaterThanOrEqual(newBox.height - 1);
      expect(newBox.height).toBeGreaterThanOrEqual(40);

      await row(page, name).locator('td').first().click();
      const dlg = page.locator(DIALOG);
      await expect(dlg).toBeVisible();
      const box = (await dlg.boundingBox())!;
      expect(box.x).toBe(0);
      expect(box.y).toBe(0);
      expect(Math.round(box.width)).toBe(vp.width);
      expect(Math.round(box.height)).toBe(vp.height);

      const head = (await page.locator(`${DIALOG} .record-dialog-head`).boundingBox())!;
      const body = (await page.locator(`${DIALOG} .record-dialog-body`).boundingBox())!;
      expect(head.y).toBe(0);
      expect(body.y).toBeGreaterThanOrEqual(head.y + head.height - 0.5);
      // The pinned head stays short — ut-docs#2000's finding was a head
      // eating 42% of a 360px screen; this one is exactly one row.
      expect(head.height / vp.height).toBeLessThan(0.3);

      // Every head control fully inside the viewport, none overlapping.
      const sels = [
        `${DIALOG} form[data-record-when="active=1"] button`,
        `${DIALOG} .record-dialog-close`,
        `${DIALOG} .record-dialog-save`,
      ];
      const boxes: Array<{ id: string; x: number; y: number; w: number; h: number }> = [];
      for (const s of sels) {
        const b = (await page.locator(s).boundingBox())!;
        expect(b, s).not.toBeNull();
        expect(b.x, s).toBeGreaterThanOrEqual(0);
        expect(b.y, s).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width, s).toBeLessThanOrEqual(vp.width + 0.5);
        expect(b.y + b.height, s).toBeLessThanOrEqual(vp.height + 0.5);
        boxes.push({ id: s, x: b.x, y: b.y, w: b.width, h: b.height });
      }
      for (let i = 0; i < boxes.length; i++) {
        for (let j = i + 1; j < boxes.length; j++) {
          const a = boxes[i], b = boxes[j];
          const overlap = a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
          expect(overlap, `${a.id} overlaps ${b.id}`).toBe(false);
        }
      }
      if (vp.oneRow) {
        expect(new Set(boxes.map((b) => Math.round(b.y))).size, 'head controls share one row at the kiosk floor').toBe(1);
      }
      // The name field is on screen and reachable too.
      const field = (await page.locator(NAME).boundingBox())!;
      expect(field.x + field.width).toBeLessThanOrEqual(vp.width + 0.5);
      expect(field.y + field.height).toBeLessThanOrEqual(vp.height + 0.5);
      // Real geometry, not scrollWidth: nothing in the body sits past its
      // right edge.
      const past = await page.evaluate((sel) => {
        const b = document.querySelector(`${sel} .record-dialog-body`) as HTMLElement;
        const r = b.getBoundingClientRect();
        return Array.from(b.querySelectorAll<HTMLElement>('input, button, select, label'))
          .filter((el) => el.getBoundingClientRect().right > r.right + 1)
          .map((el) => el.tagName + (el.getAttribute('name') || ''));
      }, DIALOG);
      expect(past).toEqual([]);
      // And the whole page gained no horizontal overflow from opening it.
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
      assertClean();
    });
  }

  // ut-docs#2010, found by the Tester pass on the real pilot tablet: the
  // list card was full-bleed but its table only painted to its own intrinsic
  // content width, leaving over half the card empty (measured 495px of table
  // inside a 1155px card at 1280x800). Cause: `.list-card .table { display:
  // block }` — the scroll-container trick copied from .users-list — turns the
  // <table> into a block box, so `table.table`'s own `width: 100%` sizes the
  // BOX while the internal table boxes still shrink-to-fit and sit at the
  // start edge. The fix keeps the table a real table and moves the sideways
  // scroll onto a wrapper. Asserted as geometry, not as a class name, because
  // the defect was purely visual and any future re-styling can reintroduce it.
  for (const vp of [
    { width: 1024, height: 600, label: 'kiosk floor 1024x600' },
    { width: 1280, height: 800, label: 'pilot tablet 1280x800' },
  ]) {
    test(`(f) at ${vp.label} the list table fills its card instead of leaving it half empty`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto('/categories');
      await createCategory(page, 'Fill Probe ' + Date.now());

      const m = await page.evaluate(() => {
        const card = document.querySelector('.list-card') as HTMLElement;
        const head = document.querySelector('#categories-table thead tr') as HTMLElement;
        const cs = getComputedStyle(card);
        const inner =
          card.getBoundingClientRect().width -
          parseFloat(cs.paddingInlineStart) -
          parseFloat(cs.paddingInlineEnd);
        return { inner, painted: head.getBoundingClientRect().width };
      });
      // The header row is what actually paints the column rule the merchant
      // sees stopping short, so measure that rather than the table element.
      expect(m.painted / m.inner).toBeGreaterThan(0.95);
      // …and it must not have gained overflow to achieve it.
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
      assertClean();
    });
  }

  test('icon-only buttons have an accessible name and a visible focus ring', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/categories');
    const name = 'Focus Probe ' + Date.now();
    await createCategory(page, name);
    for (const sel of ['#categories-new', '#categories-table .category-row .move-down', '#categories-table .category-row [data-record-edit]']) {
      const b = page.locator(sel).first();
      await expect(b).toHaveAttribute('aria-label', /.+/);
      await expect(b).toHaveAttribute('title', /.+/);
      await b.focus();
      const ring = await b.evaluate((el) => {
        const cs = getComputedStyle(el);
        return { style: cs.outlineStyle, width: parseFloat(cs.outlineWidth) };
      });
      expect(ring.style, sel).not.toBe('none');
      expect(ring.width, sel).toBeGreaterThanOrEqual(2);
    }
    assertClean();
  });
});
