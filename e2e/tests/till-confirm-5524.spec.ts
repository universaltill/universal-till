import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { answerTillConfirm, noNativeDialogs, watchConsole } from './helpers';

// ut-docs#5524: every hx-confirm (and the jiggle-mode / inline-actions
// confirm step) opens the till's own in-page dialog, #ut-confirm, instead of
// the browser's window.confirm. Mechanics are driven through injected probes
// whose routes are fulfilled locally, so no server state is touched.

async function probe(page: Page, html: string) {
  await page.evaluate((h) => {
    const host = document.createElement('div');
    // Fixed above the scrim's siblings, below the dialog, so a probe is
    // clickable at any viewport and (for the nested-dialog case) over #ut-scrim.
    host.style.cssText = 'position:fixed;inset-block-start:0;inset-inline-start:0;z-index:520;background:#fff;padding:2px';
    document.body.appendChild(host);
    host.innerHTML = h;
    (window as any).htmx.process(host);
  }, html);
}

async function counter(page: Page, glob: string) {
  let n = 0;
  await page.route(glob, async (route) => {
    n++;
    await route.fulfill({ status: 200, contentType: 'text/html', body: 'done' });
  });
  return () => n;
}

const dlg = (page: Page) => page.getByTestId('till-confirm');
const ok = (page: Page) => page.getByTestId('till-confirm-ok');
const cancel = (page: Page) => page.getByTestId('till-confirm-cancel');

test.describe('till-styled hx-confirm (ut-docs#5524)', () => {
  test('Cancel sends nothing, closes dialog and scrim, returns focus to the opener', async ({ page }) => {
    const none = noNativeDialogs(page);
    const reqs = await counter(page, '**/__ut5524/c');
    await page.goto('/');
    await probe(page, '<button id="p1" hx-post="/__ut5524/c" hx-confirm="Delete it?" hx-target="#t5524">del</button><div id="t5524"></div>');
    await page.locator('#p1').click();
    await answerTillConfirm(page, 'cancel', 'Delete it?');
    await expect(page.locator('#ut-scrim')).toBeHidden();
    await expect(page.locator('#p1')).toBeFocused();
    expect(reqs()).toBe(0);
    none();
  });

  test('Escape is Cancel and does not close the dialog underneath', async ({ page }) => {
    const none = noNativeDialogs(page);
    const reqs = await counter(page, '**/__ut5524/e');
    await page.goto('/');
    await probe(page, '<dialog id="under5524" class="modifier-modal" data-ut-escape-close>under'
      + '<button id="p2" hx-post="/__ut5524/e" hx-confirm="Sure?">go</button></dialog>');
    await page.evaluate(() => (document.getElementById('under5524') as HTMLDialogElement).show());
    await page.locator('#p2').click();
    await answerTillConfirm(page, 'escape', 'Sure?');
    await expect(page.locator('#under5524')).toHaveJSProperty('open', true);
    await expect(page.locator('#p2')).toBeFocused();
    expect(reqs()).toBe(0);
    none();
  });

  test('Confirm sends exactly one request with the opener values', async ({ page }) => {
    const none = noNativeDialogs(page);
    let body = '';
    let n = 0;
    await page.route('**/__ut5524/ok', async (route) => {
      n++;
      body = route.request().postData() || '';
      await route.fulfill({ status: 200, contentType: 'text/html', body: 'done' });
    });
    await page.goto('/');
    await probe(page, '<button id="p3" hx-post="/__ut5524/ok" hx-vals=\'{"k":"v1"}\' hx-confirm="Go?" hx-target="#t5524">go</button><div id="t5524"></div>');
    await page.locator('#p3').click();
    await answerTillConfirm(page, 'confirm', 'Go?');
    await expect(page.locator('#t5524')).toHaveText('done');
    expect(n).toBe(1);
    expect(body).toContain('k=v1');
    none();
  });

  test('tone and initial focus: danger focuses Cancel, neutral focuses Confirm', async ({ page }) => {
    await page.goto('/');
    await probe(page, '<button id="pd" hx-post="/__ut5524/t" hx-confirm="Danger?">d</button>'
      + '<button id="pn" hx-post="/__ut5524/t" hx-confirm="Neutral?" data-confirm-tone="neutral">n</button>');
    await page.route('**/__ut5524/t', (r) => r.fulfill({ status: 200, body: 'x' }));
    await page.locator('#pd').click();
    await expect(dlg(page)).toBeVisible();
    await expect(ok(page)).toHaveClass(/danger/);
    await expect(ok(page)).not.toHaveClass(/primary/);
    await expect(cancel(page)).toBeFocused();
    await expect(ok(page)).toHaveText('Confirm');
    await expect(cancel(page)).toHaveText('Cancel');
    await answerTillConfirm(page, 'cancel');
    await page.locator('#pn').click();
    await expect(dlg(page)).toBeVisible();
    await expect(ok(page)).toHaveClass(/primary/);
    await expect(ok(page)).not.toHaveClass(/danger/);
    await expect(ok(page)).toBeFocused();
    await answerTillConfirm(page, 'cancel');
  });

  test('keyboard: Tab cycles Cancel and Confirm only; Enter activates; held Enter does not confirm', async ({ page }) => {
    const reqs = await counter(page, '**/__ut5524/k');
    await page.goto('/');
    await probe(page, '<button id="pk" hx-post="/__ut5524/k" hx-confirm="Keys?">k</button>'
      + '<button id="pkn" hx-post="/__ut5524/k" hx-confirm="Keys neutral?" data-confirm-tone="neutral">kn</button>');
    await page.locator('#pk').click();
    await expect(cancel(page)).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(ok(page)).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(cancel(page)).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(ok(page)).toBeFocused();
    await expect(dlg(page)).toHaveAttribute('data-armed', '');
    await page.keyboard.press('Enter');
    await expect(dlg(page)).toBeHidden();
    await expect.poll(reqs).toBe(1);
    // A repeated keydown (key held) never activates the focused button.
    await page.locator('#pkn').click();
    await expect(ok(page)).toBeFocused();
    await expect(dlg(page)).toHaveAttribute('data-armed', '');
    await page.evaluate(() => {
      document.getElementById('ut-confirm')!.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', repeat: true, bubbles: true, cancelable: true }));
    });
    await expect(dlg(page)).toBeVisible();
    await answerTillConfirm(page, 'cancel');
    expect(reqs()).toBe(1);
  });

  test('non-modal: not :modal, scrim visible, status-bar controls stay reachable', async ({ page }) => {
    await page.goto('/');
    await probe(page, '<button id="pm" hx-post="/__ut5524/m" hx-confirm="Modal?">m</button>');
    await page.locator('#pm').click();
    await expect(dlg(page)).toBeVisible();
    expect(await dlg(page).evaluate((el) => el.matches(':modal'))).toBe(false);
    await expect(page.locator('#ut-scrim')).toBeVisible();
    // The status bar sits above the scrim: its first control is hit-testable.
    const reachable = await page.evaluate(() => {
      const bar = document.querySelector('footer.statusbar');
      if (!bar) return 'no-bar';
      const target = bar.querySelector('button, a') as HTMLElement | null;
      if (!target) return 'no-control';
      const r = target.getBoundingClientRect();
      const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!at && (at === target || target.contains(at));
    });
    expect(reachable).toBe(true);
    await answerTillConfirm(page, 'cancel');
  });

  test('double-tap: one dialog; Confirm ignored until armed; double Confirm sends one request', async ({ page }) => {
    const none = noNativeDialogs(page);
    const reqs = await counter(page, '**/__ut5524/d');
    await page.goto('/');
    await probe(page, '<button id="pdt" hx-post="/__ut5524/d" hx-confirm="Twice?">t</button>');
    await page.locator('#pdt').dblclick();
    await expect(page.locator('#ut-confirm')).toHaveCount(1);
    await expect(dlg(page)).toBeVisible();
    // Confirm before the arm: ignored, dialog stays.
    await page.evaluate(() => {
      const d = document.getElementById('ut-confirm')!;
      d.removeAttribute('data-armed');
      (d.querySelector('[data-ut-confirm="ok"]') as HTMLElement).click();
    });
    await expect(dlg(page)).toBeVisible();
    expect(reqs()).toBe(0);
    await expect(dlg(page)).toHaveAttribute('data-armed', '');
    await ok(page).dblclick();
    await expect(dlg(page)).toBeHidden();
    await expect.poll(reqs).toBe(1);
    await page.waitForTimeout(300);
    expect(reqs()).toBe(1);
    none();
  });

  test('scrim tap does nothing', async ({ page }) => {
    const reqs = await counter(page, '**/__ut5524/s');
    await page.goto('/');
    await probe(page, '<button id="ps" hx-post="/__ut5524/s" hx-confirm="Scrim?">s</button>');
    await page.locator('#ps').click();
    await expect(dlg(page)).toBeVisible();
    await page.locator('#ut-scrim').click({ position: { x: 3, y: 300 }, force: true });
    await expect(dlg(page)).toBeVisible();
    expect(reqs()).toBe(0);
    await answerTillConfirm(page, 'cancel');
  });

  test('the question is literal text, never HTML', async ({ page }) => {
    await page.goto('/');
    await probe(page, '<button id="px" hx-post="/__ut5524/x" hx-confirm="Delete &lt;b&gt;x&lt;/b&gt;?">x</button>');
    await page.locator('#px').click();
    await expect(dlg(page)).toBeVisible();
    await expect(page.locator('#ut-confirm-msg')).toHaveText('Delete <b>x</b>?');
    await expect(dlg(page).locator('b')).toHaveCount(0);
    await answerTillConfirm(page, 'cancel');
  });

  for (const lang of ['en', 'fa']) {
    for (const vp of [{ width: 360, height: 640 }, { width: 1024, height: 600 }]) {
      test(`geometry ${lang} ${vp.width}x${vp.height}: targets >=44px, no h-scroll, Confirm at inline-end`, async ({ page }) => {
        await page.setViewportSize(vp);
        await page.goto('/');
        if (lang === 'fa') await page.evaluate(() => { document.documentElement.dir = 'rtl'; document.documentElement.lang = 'fa'; });
        await probe(page, '<button id="pg" hx-post="/__ut5524/g" hx-confirm="Delete this thing and everything attached to it? It cannot be brought back.">g</button>');
        await page.locator('#pg').click();
        await expect(dlg(page)).toBeVisible();
        const g = await page.evaluate(() => {
          const r = (s: string) => document.querySelector(s)!.getBoundingClientRect();
          const o = r('[data-ut-confirm="ok"]'), c = r('[data-ut-confirm="cancel"]'), d = r('#ut-confirm');
          const de = document.documentElement;
          return { ow: o.width, oh: o.height, cw: c.width, ch: c.height, ox: o.left, cx: c.left,
            dl: d.left, dr: d.right, dt: d.top, db: d.bottom, vw: innerWidth, vh: innerHeight,
            hscroll: de.scrollWidth > de.clientWidth, rtl: getComputedStyle(de).direction === 'rtl' };
        });
        expect(g.ow).toBeGreaterThanOrEqual(44); expect(g.oh).toBeGreaterThanOrEqual(44);
        expect(g.cw).toBeGreaterThanOrEqual(44); expect(g.ch).toBeGreaterThanOrEqual(44);
        expect(g.hscroll).toBe(false);
        expect(g.dl).toBeGreaterThanOrEqual(0); expect(g.dr).toBeLessThanOrEqual(g.vw);
        expect(g.db).toBeLessThanOrEqual(g.vh);
        if (lang === 'fa') { expect(g.rtl).toBe(true); expect(g.ox).toBeLessThan(g.cx); }
        else expect(g.ox).toBeGreaterThan(g.cx);
        await answerTillConfirm(page, 'cancel');
      });
    }
  }

  test('an opener added by an htmx swap after load uses the dialog', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.route('**/__ut5524/swap', (r) => r.fulfill({ status: 200, contentType: 'text/html',
      body: '<button id="late" hx-post="/__ut5524/late" hx-confirm="Late?">late</button>' }));
    const reqs = await counter(page, '**/__ut5524/late');
    await page.goto('/');
    await probe(page, '<button id="sw" hx-get="/__ut5524/swap" hx-target="#t5524s">load</button><div id="t5524s"></div>');
    await page.locator('#sw').click();
    await page.locator('#late').click();
    await answerTillConfirm(page, 'confirm', 'Late?');
    await expect.poll(reqs).toBe(1);
    none();
  });

  test('poll guard: a region refresh that would detach the opener is held while the dialog is open', async ({ page }) => {
    const polls = await counter(page, '**/__ut5524/poll');
    const acts = await counter(page, '**/__ut5524/act');
    await page.goto('/');
    await probe(page, '<div id="reg5524" hx-get="/__ut5524/poll" hx-trigger="every 1s" hx-swap="none">'
      + '<button id="pp" hx-post="/__ut5524/act" hx-confirm="Pinned?">p</button></div>');
    await page.locator('#pp').click();
    await expect(dlg(page)).toBeVisible();
    const before = polls();
    await page.waitForTimeout(2500);
    expect(polls()).toBe(before);
    await answerTillConfirm(page, 'confirm', 'Pinned?');
    await expect.poll(acts).toBe(1);
  });

  test('fallback: with till-confirm.js blocked, the native confirm still gates the request', async ({ page }) => {
    const reqs = await counter(page, '**/__ut5524/f');
    await page.route('**/public/till-confirm.js*', (r) => r.abort());
    const seen: string[] = [];
    page.on('dialog', (d) => { seen.push(d.message()); void d.dismiss(); });
    await page.goto('/');
    await probe(page, '<button id="pf" hx-post="/__ut5524/f" hx-confirm="Native?">f</button>');
    await page.locator('#pf').click();
    await expect.poll(() => seen.length).toBe(1);
    expect(seen[0]).toBe('Native?');
    expect(reqs()).toBe(0);
  });

  test('data-submit="confirm" form (tables release): Cancel posts nothing, Confirm posts once', async ({ page }) => {
    const none = noNativeDialogs(page);
    const reqs = await counter(page, '**/__ut5524/release');
    await page.goto('/');
    await probe(page, '<form id="rel" method="post" action="/__ut5524/release" data-submit="confirm" data-confirm-text="Release this table?">'
      + '<input type="hidden" name="x" value="1"><button id="relb" type="submit" class="btn secondary">Release</button></form>');
    await page.locator('#relb').click();
    await answerTillConfirm(page, 'cancel', 'Release this table?');
    await expect(page.locator('#relb')).toBeFocused();
    expect(reqs()).toBe(0);
    await page.locator('#relb').click();
    await Promise.all([
      page.waitForRequest('**/__ut5524/release'),
      answerTillConfirm(page, 'confirm', 'Release this table?'),
    ]);
    await expect.poll(reqs).toBe(1);
    none();
  });

  test('a question chained after an answer stays open (late close event is not its Cancel)', async ({ page }) => {
    const none = noNativeDialogs(page);
    await page.goto('/');
    await page.evaluate(() => {
      const w = window as any;
      w.__chain5524 = 'pending';
      w.UT.confirm('First?').then(() => w.UT.confirm('Second?')).then((a: boolean) => { w.__chain5524 = a; });
    });
    await expect(page.locator('#ut-confirm-msg')).toHaveText('First?');
    await cancel(page).click(); // not answerTillConfirm: the dialog reopens at once
    await expect(page.locator('#ut-confirm-msg')).toHaveText('Second?');
    await page.waitForTimeout(100); // the first close event has been dispatched by now
    await expect(dlg(page)).toBeVisible();
    expect(await page.evaluate(() => (window as any).__chain5524)).toBe('pending');
    await answerTillConfirm(page, 'confirm', 'Second?');
    await expect.poll(() => page.evaluate(() => (window as any).__chain5524)).toBe(true);
    none();
  });

  test('self-order customer page has no till confirm and a clean console', async ({ page }) => {
    const clean = watchConsole(page);
    await page.goto('/self-order');
    await expect(page.locator('#ut-confirm')).toHaveCount(0);
    clean();
  });
});
