import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { openPhoneDrawer } from './helpers';

// ut-docs#3353: the product owner, on an iPhone: "bug report doesn't have a
// mobile design". At <=480px the bug-report panel was a 26rem floating card
// with a drag handle: Send squeezed beside the note, ~34px head buttons, a
// ~21px thumbnail ✕, and a head that ate the swipe. It is now a full-screen
// sheet under the top bar. Bounding boxes and hit-tests, not eyeballs.

async function openPanel(page: Page) {
  await openPhoneDrawer(page); // ut-docs#3059: the 🐞 chip lives in the ☰ drawer
  await page.getByTestId('bugreport-toggle').click();
  await expect(page.getByTestId('bugreport-panel')).toBeVisible();
  // The drawer closes on the tap; wait out its slide so it is not measured.
  await expect(page.locator('body')).not.toHaveClass(/nav-drawer-open/);
}

async function panelBox(page: Page) {
  return page.getByTestId('bugreport-panel').evaluate((el) => {
    const r = el.getBoundingClientRect();
    return { l: r.left, r: r.right, t: r.top, b: r.bottom };
  });
}

for (const [w, h] of [[360, 640], [390, 844], [440, 956]] as const) {
  test.describe(`${w}x${h}`, () => {
    test.use({ viewport: { width: w, height: h }, hasTouch: true, isMobile: true });

    for (const route of ['/', '/settings']) {
      test(`${route}: the panel is a full-screen sheet with thumb-sized controls`, async ({ page }) => {
        await page.goto(route);
        const nav = (await page.locator('.nav').boundingBox())!;
        await openPanel(page);

        // Edge to edge, from under the top bar to the bottom of the screen.
        const box = await panelBox(page);
        expect(box.l).toBeLessThanOrEqual(1);
        expect(box.r).toBeGreaterThanOrEqual(w - 1);
        expect(box.t).toBeGreaterThanOrEqual(nav.y + nav.height - 1);
        expect(box.t).toBeLessThanOrEqual(nav.y + nav.height + 1);
        expect(box.b).toBeGreaterThanOrEqual(h - 1);

        // Nothing scrolls sideways.
        const sw = await page.evaluate(() => document.documentElement.scrollWidth);
        expect(sw).toBeLessThanOrEqual(w);

        // Every visible control is at least 44px tall; the head's two are
        // at least 44px wide too.
        const small = await page.getByTestId('bugreport-panel').evaluate((el) =>
          Array.from(el.querySelectorAll('button, a[href]'))
            .filter((b) => b.getClientRects().length > 0)
            .map((b) => ({ id: b.id || b.textContent!.trim(), r: b.getBoundingClientRect() }))
            .filter((x) => x.r.height < 44 - 0.5)
            .map((x) => `${x.id} ${Math.round(x.r.height)}px`));
        expect(small, 'controls under 44px').toEqual([]);
        for (const id of ['bugreport-close', 'bugreport-discard']) {
          const b = (await page.getByTestId(id).boundingBox())!;
          expect(b.width, id).toBeGreaterThanOrEqual(44 - 0.5);
          await expect(page.getByTestId(id)).toBeInViewport();
        }

        // The note gets its own line; Send sits under it at full width and
        // is on screen without scrolling.
        const note = (await page.locator('#ir-note').boundingBox())!;
        const send = (await page.locator('#ir-save-btn').boundingBox())!;
        expect(send.y).toBeGreaterThanOrEqual(note.y + note.height - 1);
        expect(send.width).toBeGreaterThan(w * 0.8);
        await expect(page.locator('#ir-save-btn')).toBeInViewport({ ratio: 1 });

        // ☰ is still the top-most element at its own centre: the drawer (and
        // Lock, exit and status in it) stays reachable over the sheet.
        const toggleOnTop = await page.locator('.nav-drawer-toggle').evaluate((el) => {
          const r = el.getBoundingClientRect();
          const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
          return !!hit && el.contains(hit);
        });
        expect(toggleOnTop).toBe(true);
        await page.locator('.nav-drawer-toggle').click();
        await expect(page.locator('body')).toHaveClass(/nav-drawer-open/);
        const lockOnTop = await page.locator('#nav-drawer').evaluate((el) => {
          const r = el.getBoundingClientRect();
          const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
          return !!hit && el.contains(hit);
        });
        expect(lockOnTop, 'the open drawer paints over the sheet').toBe(true);
      });
    }

    test('the head is not a drag handle, and an old dragged position cannot move the sheet', async ({ page }) => {
      await page.goto('/');
      await openPanel(page);
      const before = await panelBox(page);

      const head = (await page.locator('.bugreport-head h2').boundingBox())!;
      await page.mouse.move(head.x + 5, head.y + head.height / 2);
      await page.mouse.down();
      await page.mouse.move(head.x + 5 - 80, head.y + head.height / 2 + 150, { steps: 5 });
      await page.mouse.up();
      expect(await panelBox(page)).toEqual(before);
      expect(await page.getByTestId('bugreport-panel').getAttribute('style')).toBeNull();

      // A position dragged on a wider layout is restored as inline left/top
      // (ut-docs#2342); on the phone it must not pull the sheet off its edges.
      await page.getByTestId('bugreport-panel').evaluate((el: HTMLElement) => {
        el.style.insetInlineEnd = 'auto'; el.style.left = '120px'; el.style.top = '300px';
      });
      expect(await panelBox(page)).toEqual(before);
    });

    test('/report-issue (server-opened) paints the sheet under the bar from the first frame', async ({ page }) => {
      await page.goto('/report-issue');
      await expect(page.getByTestId('bugreport-panel')).toBeVisible();
      const nav = (await page.locator('.nav').boundingBox())!;
      const box = await panelBox(page);
      expect(box.t).toBeLessThanOrEqual(nav.y + nav.height + 1);
      expect(box.t).toBeGreaterThanOrEqual(nav.y + nav.height - 1);
      // CSS alone, before app.js measures anything: the rule must not rest
      // on the measured --topbar-h (12rem until JS runs).
      const top = await page.evaluate(() => {
        document.documentElement.style.setProperty('--topbar-h', '12rem');
        return document.getElementById('bugreport-panel')!.getBoundingClientRect().top;
      });
      expect(top).toBeLessThanOrEqual(nav.y + nav.height + 1);
    });

    test('a dragged position saved on a wider layout survives the phone untouched', async ({ page }) => {
      await page.goto('/');
      await page.evaluate(() => {
        const m = JSON.parse(sessionStorage.getItem('ut-bugreport-draft') || 'null') || {};
        sessionStorage.setItem('ut-bugreport-draft', JSON.stringify({ note: '', ...m, id: m.id || 'seed3353-x', pos: [300, 200], open: true }));
      });
      await page.reload();
      await expect(page.getByTestId('bugreport-panel')).toBeVisible();
      // Opening and a resize (rotation, a keyboard) both re-clamp a dragged
      // panel on wider layouts; on the phone they must leave pos alone.
      await page.setViewportSize({ width: w, height: h - 100 });
      await page.setViewportSize({ width: w, height: h });
      const pos = await page.evaluate(() => JSON.parse(sessionStorage.getItem('ut-bugreport-draft') || 'null')?.pos);
      expect(pos).toEqual([300, 200]);
      const box = await panelBox(page);
      expect(box.l).toBeLessThanOrEqual(1);
      expect(box.r).toBeGreaterThanOrEqual(w - 1);
    });

    test('sale screen: a status problem stays visible over the open sheet', async ({ page }) => {
      await page.goto('/');
      await openPanel(page);
      // The row only shows on the phone sale screen for a problem
      // (offline, main till unreachable, power); fake the offline light.
      await page.locator('#sb-conn').evaluate((el) => el.classList.add('is-offline'));
      const onTop = await page.locator('.statusbar').evaluate((el) => {
        const r = el.getBoundingClientRect();
        if (r.height === 0) return 'hidden';
        const hit = document.elementFromPoint(r.left + 8, r.top + r.height / 2);
        return !!hit && el.contains(hit) ? 'top' : `covered by ${hit && hit.id}`;
      });
      expect(onTop).toBe('top');
      const sb = (await page.locator('.statusbar').boundingBox())!;
      expect(sb.y + sb.height).toBeGreaterThanOrEqual(h - 1);
    });

    test('RTL mirrors nothing out of place: the sheet still spans the width', async ({ page }) => {
      await page.goto('/');
      await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
      await openPanel(page);
      const box = await panelBox(page);
      expect(box.l).toBeLessThanOrEqual(1);
      expect(box.r).toBeGreaterThanOrEqual(w - 1);
      const sw = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(sw).toBeLessThanOrEqual(w);
      // The title starts at the reading-start (right) edge.
      const title = (await page.locator('.bugreport-head h2').boundingBox())!;
      const close = (await page.getByTestId('bugreport-close').boundingBox())!;
      expect(close.x).toBeLessThan(title.x);
    });
  });
}
