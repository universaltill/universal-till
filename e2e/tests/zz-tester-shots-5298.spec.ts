import { test, expect } from './fixtures';
import { execFileSync } from 'child_process';
import { REPO_ROOT } from './worker-till';
// TEMPORARY tester spec for ut-docs#5298 screenshots. Not to be committed.
const OUT = '/tmp/claude-0/-home-user/16960c60-6634-57f0-b2dd-972d7351401c/scratchpad/5298-shots';
function setting(op: 'set' | 'delete', key: string, value?: string) {
  execFileSync('go', ['run', './e2e/till_setting', op, key, ...(value === undefined ? [] : [value])], {
    cwd: REPO_ROOT, env: { ...process.env, UT_DATA_DIR: process.env.UT_E2E_WORKER_DATA_DIR! }, stdio: ['ignore', 'ignore', 'inherit'],
  });
}
const SIZES = [{ w: 1024, h: 600 }, { w: 360, h: 740 }];
test.describe('5298 shots', () => {
  test.beforeEach(() => { test.skip(!process.env.UT_E2E_WORKER_DATA_DIR); setting('set', 'sync.primary_url', 'http://127.0.0.1:9'); });
  test.afterEach(() => { if (process.env.UT_E2E_WORKER_DATA_DIR) setting('delete', 'sync.primary_url'); });
  for (const lang of ['en', 'fa']) for (const s of SIZES) {
    test(`pos-alert ${lang} ${s.w}`, async ({ page }) => {
      await page.setViewportSize({ width: s.w, height: s.h });
      let posts = 0;
      await page.route('**/api/settings/display-mode', (route) => {
        if (route.request().method() !== 'POST') return route.continue();
        posts++; return posts === 1 ? route.continue() : route.abort('failed');
      });
      await page.goto(`/settings?lang=${lang}#settings-display`);
      await expect(page.locator('html')).toHaveAttribute('dir', lang === 'fa' ? 'rtl' : 'ltr');
      const form = page.locator('form.set-gap[hx-post="/api/settings/display-mode"]');
      await form.locator('select[name=mode]').selectOption('self_order');
      await form.locator('button[type=submit]').click();
      const dlg = page.locator('#confirm-modal');
      await expect(dlg).toHaveJSProperty('open', true);
      await dlg.locator('button[type=submit]').click();
      await expect(dlg).toHaveJSProperty('open', false);
      const alert = page.locator('#pos-alert');
      await expect(alert).toBeVisible();
      const txt = await alert.locator('.notice-text').innerText();
      console.log(`ALERT[${lang}] ${txt}`);
      const box = await alert.boundingBox();
      const tb = await alert.locator('.notice-text').boundingBox();
      const db = await alert.locator('.notice-dismiss').boundingBox();
      console.log(`GEOM ${lang} ${s.w} alert=${JSON.stringify(box)} text=${JSON.stringify(tb)} dismiss=${JSON.stringify(db)} vw=${s.w} sw=${await page.evaluate(() => document.documentElement.scrollWidth)}`);
      await page.screenshot({ path: `${OUT}/pos-alert-${lang}-${s.w}x${s.h}.png` });
    });
    test(`data-reset ${lang} ${s.w}`, async ({ page }) => {
      await page.setViewportSize({ width: s.w, height: s.h });
      await page.route('**/api/data/reset-transactions', (route) => route.request().method() === 'POST' ? route.abort('failed') : route.continue());
      await page.goto(`/settings?lang=${lang}#settings-data`);
      await page.locator('[data-testid=data-reset]').click();
      const msg = page.locator('#data-reset-msg .pos-notice.error .notice-text');
      await expect(msg).toBeVisible();
      console.log(`RESET[${lang}] ${await msg.innerText()}`);
      await page.locator('#data-reset-msg').scrollIntoViewIfNeeded();
      const card = page.locator('#settings-data');
      await card.screenshot({ path: `${OUT}/data-reset-${lang}-${s.w}x${s.h}-card.png` });
      await page.screenshot({ path: `${OUT}/data-reset-${lang}-${s.w}x${s.h}.png` });
      console.log(`RESETGEOM ${lang} ${s.w} msg=${JSON.stringify(await page.locator('#data-reset-msg').boundingBox())} sw=${await page.evaluate(() => document.documentElement.scrollWidth)}`);
    });
  }
});
