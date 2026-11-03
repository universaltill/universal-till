import { test, expect } from './fixtures';
import type { Page, Route } from '@playwright/test';
import { execFileSync } from 'child_process';
import { watchConsole } from './helpers';
import { REPO_ROOT } from './worker-till';

// ut-docs#5288: while a prompt's OWN request is in flight (the confirm
// dialog's retry POST), Cancel and Escape must do nothing and Cancel must be
// disabled: closing then would leave the action done on the server with no
// answer shown, and a late re-prompt could re-open a dialog the operator had
// closed. We never abort (htmx:abort is client-side only; the server may
// already have acted). Once the request ends -- answer or failure -- the
// operator can cancel again.
//
// Here: the confirm dialog on the display-mode "make this a satellite?" ask
// (default project, worker till; same setup as the 5275 spec). The pair
// caller is in pair-confirm-escape-5275.spec.ts (diagnostics project); the
// PIN dialog is in prompt-inflight-pin-5288.spec.ts (auth project).

function setting(op: 'set' | 'delete', key: string, value?: string) {
  const dataDir = process.env.UT_E2E_WORKER_DATA_DIR;
  if (!dataDir) throw new Error('UT_E2E_WORKER_DATA_DIR unset');
  execFileSync('go', ['run', './e2e/till_setting', op, key, ...(value === undefined ? [] : [value])], {
    cwd: REPO_ROOT,
    env: { ...process.env, UT_DATA_DIR: dataDir },
    stdio: ['ignore', 'ignore', 'inherit'],
  });
}

// The first POST raises the prompt (let it through); the second is the
// dialog's own confirmed retry, which is held until `release()`.
async function holdSecondPost(page: Page) {
  let posts = 0;
  let release!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  let held!: (r: Route) => void;
  const heldRoute = new Promise<Route>((r) => { held = r; });
  await page.route('**/api/settings/display-mode', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    posts++;
    if (posts === 1) return route.continue();
    held(route);
    await gate;
    return route.fallback();
  });
  return { posts: () => posts, release, heldRoute };
}

async function openConfirm(page: Page) {
  await page.goto('/settings#settings-display');
  const form = page.locator('form.set-gap[hx-post="/api/settings/display-mode"]');
  await form.locator('select[name=mode]').selectOption('self_order');
  await form.locator('button[type=submit]').click();
  const dlg = page.locator('#confirm-modal');
  await expect(dlg).toHaveJSProperty('open', true);
  return dlg;
}

test.describe('prompt in flight: Cancel and Escape are inert (ut-docs#5288)', () => {
  test.beforeEach(() => {
    test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir)');
    setting('set', 'sync.primary_url', 'http://127.0.0.1:9');
  });
  const restore = () => {
    if (!process.env.UT_E2E_WORKER_DATA_DIR) return;
    try { setting('delete', 'sync.primary_url'); } catch { setting('delete', 'sync.primary_url'); }
  };
  test.afterEach(restore);
  test.afterAll(restore);

  test('Escape and Cancel mid-request keep the dialog open; the answer then shows', async ({ page }) => {
    const assertClean = watchConsole(page);
    const h = await holdSecondPost(page);
    const dlg = await openConfirm(page);
    const cancel = dlg.locator('[data-action="close-closest"]');
    const confirm = dlg.locator('button[type=submit]');
    await expect(cancel).toBeEnabled();

    await confirm.click();
    await h.heldRoute; // the request is now in flight
    await expect(confirm).toBeDisabled();
    await expect(cancel).toBeDisabled();
    // ut-docs#5291: visible busy state while the prompt's own request runs.
    await expect(dlg.locator('form')).toHaveAttribute('aria-busy', 'true');
    expect(await confirm.evaluate((el) => getComputedStyle(el, '::after').content)).not.toBe('none');

    await page.keyboard.press('Escape');
    await expect(dlg).toHaveJSProperty('open', true);
    // A disabled button takes no click; force it so a regression to an
    // enabled Cancel shows up as a closed dialog rather than a timeout.
    await cancel.click({ force: true });
    await expect(dlg).toHaveJSProperty('open', true);
    await expect(page.locator('#ut-scrim')).toBeVisible();
    expect(h.posts()).toBe(2);

    h.release();
    // The server's answer (the main till is unreachable: a refusal text) lands
    // in the target and the dialog closes only now.
    await expect(dlg).toHaveJSProperty('open', false);
    await expect(page.locator('#display-mode-msg')).not.toBeEmpty();
    assertClean();
  });

  test('a failed request never leaves the dialog stuck open with Cancel disabled', async ({ page }) => {
    let posts = 0;
    await page.route('**/api/settings/display-mode', (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      posts++;
      return posts === 1 ? route.continue() : route.abort('failed');
    });
    const dlg = await openConfirm(page);
    const cancel = dlg.locator('[data-action="close-closest"]');
    await dlg.locator('button[type=submit]').click();
    await expect.poll(() => posts).toBe(2);
    // htmx re-enables the buttons when the request errors out; today the
    // prompt's own after-request hook then closes the dialog (the generic
    // error toast carries the failure). Either way the operator is never
    // stranded behind a disabled Cancel.
    await expect.poll(async () => (await dlg.evaluate((d: HTMLDialogElement) => d.open)) === false
      || (await cancel.isEnabled())).toBe(true);
    if (await dlg.evaluate((d: HTMLDialogElement) => d.open)) {
      await page.keyboard.press('Escape');
      await expect(dlg).toHaveJSProperty('open', false);
    }
  });

  test('Escape on an idle prompt still cancels (no regression)', async ({ page }) => {
    const dlg = await openConfirm(page);
    await expect(dlg.locator('[data-action="close-closest"]')).toBeEnabled();
    await page.keyboard.press('Escape');
    await expect(dlg).toHaveJSProperty('open', false);
  });
});
