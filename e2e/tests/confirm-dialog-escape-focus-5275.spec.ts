import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
import { execFileSync } from 'child_process';
import { watchConsole } from './helpers';
import { REPO_ROOT } from './worker-till';

// ut-docs#5275: the confirm dialog (confirm_prompt.html, here the
// display-mode "make this a satellite?" ask) must (1) close on Escape as
// Cancel with no request, (2) hand focus back to the control that opened
// it, (3) remove only the hint node it placed, never a real answer. Same
// for the Cancel button. The dialog stays .show() over #ut-scrim, so the
// status bar is never inert.
//
// The pair caller (hint swapped into #pair-msg, needs a registered till)
// is pair-confirm-escape-5275.spec.ts in the diagnostics project. Display
// mode is hx-swap="none", so it places no hint: the "hint survives" test
// below proves the hook removes only what the prompt itself placed.

function setting(op: 'set' | 'delete', key: string, value?: string) {
  const dataDir = process.env.UT_E2E_WORKER_DATA_DIR;
  if (!dataDir) throw new Error('UT_E2E_WORKER_DATA_DIR unset');
  execFileSync('go', ['run', './e2e/till_setting', op, key, ...(value === undefined ? [] : [value])], {
    cwd: REPO_ROOT,
    env: { ...process.env, UT_DATA_DIR: dataDir },
    stdio: ['ignore', 'ignore', 'inherit'],
  });
}

async function openConfirm(page: Page) {
  await page.goto('/settings#settings-display');
  const form = page.locator('form.set-gap[hx-post="/api/settings/display-mode"]');
  await form.locator('select[name=mode]').selectOption('self_order');
  const apply = form.locator('button[type=submit]');
  await apply.focus();
  await apply.click();
  await expect(page.locator('#confirm-modal')).toHaveJSProperty('open', true);
  return { form, apply };
}

test.describe('confirm dialog Escape / focus return / hint (ut-docs#5275)', () => {
  test.beforeEach(() => {
    test.skip(!process.env.UT_E2E_WORKER_DATA_DIR, 'needs a worker till this run spawned (its data dir)');
    setting('set', 'sync.primary_url', 'http://127.0.0.1:9');
  });
  // The worker till is shared with later specs: it must not stay a
  // replica if one delete fails (review, ut-docs#5275), so retry once and
  // sweep again after the block.
  const restore = () => {
    if (!process.env.UT_E2E_WORKER_DATA_DIR) return;
    try { setting('delete', 'sync.primary_url'); } catch { setting('delete', 'sync.primary_url'); }
  };
  test.afterEach(restore);
  test.afterAll(restore);

  for (const how of ['Escape', 'Cancel'] as const) {
    test(`${how} closes with no request, returns focus, leaves the target alone`, async ({ page }) => {
      const assertClean = watchConsole(page);
      let posts = 0;
      await page.route('**/api/settings/display-mode', (route) => {
        if (route.request().method() === 'POST') posts++;
        return route.continue();
      });
      const { apply } = await openConfirm(page);
      expect(posts).toBe(1);
      // A hint planted before Cancel/Escape must survive: display mode places
      // none, and the hook may only remove a node the prompt itself placed.
      await page.evaluate(() => {
        const m = document.getElementById('display-mode-msg')!;
        m.innerHTML = '<span class="elevation-hint muted">planted</span>';
      });
      // Not inert: the status bar is still focusable while the dialog is open.
      await expect(page.locator('#ut-scrim')).toBeVisible();
      if (how === 'Escape') await page.keyboard.press('Escape');
      else await page.locator('#confirm-modal [data-action="close-closest"]').click();
      await expect(page.locator('#confirm-modal')).toHaveJSProperty('open', false);
      await expect(page.locator('#ut-scrim')).toBeHidden();
      expect(posts).toBe(1);
      await expect(apply).toBeFocused();
      await expect(page.locator('#display-mode-msg .elevation-hint')).toHaveText('planted');
      assertClean();
    });
  }

  // Dialog open => status bar not inert and clickable (no showModal).
  test('status bar stays reachable while the dialog is open', async ({ page }) => {
    await openConfirm(page);
    expect(await page.evaluate(() => document.querySelectorAll('[inert]').length)).toBe(0);
    expect(await page.evaluate(() => !!document.querySelector('dialog:modal'))).toBe(false);
    const bar = page.locator('footer.statusbar');
    await expect(bar).toBeVisible();
    // The element at the bar's centre is the bar (or a child), not the dialog/scrim.
    const hit = await bar.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!top && el.contains(top);
    });
    expect(hit).toBe(true);
    await page.screenshot({ path: process.env.UT_5275_SHOT || 'test-results/confirm-open-5275.png' });
  });

  test('confirming keeps the answer in the target and closes the dialog', async ({ page }) => {
    await openConfirm(page);
    await page.locator('#confirm-modal button[type=submit]').click();
    await expect(page.locator('#confirm-modal')).toHaveJSProperty('open', false);
    // The main till is unreachable: the server refuses with text that must stay.
    await expect(page.locator('#display-mode-msg')).not.toBeEmpty();
  });
});
