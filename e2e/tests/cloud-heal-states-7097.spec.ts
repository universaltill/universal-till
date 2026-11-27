import { test, expect } from './fixtures';
import { execFileSync, spawn, type ChildProcess } from 'child_process';
import fs from 'fs';
import os from 'os';
import path from 'path';
import { PREBUILT_BIN, REPO_ROOT, isHealthy } from './worker-till';

// ut-docs#7097 (ADR-0222, UX notes): a till whose cloud credential was
// refused says so in plain words, in ONE status-bar control (short words, a
// dot, the button label) and the same sentence and button on Tills > This
// till. The button lands on the code field, on screen at 1024x600 without
// scrolling. A till that is reconnecting by itself shows no banner and no
// button. Each state boots its own throwaway till with the engine's saved
// state seeded, because the engine reads it at start-up. The states that
// need a live cloud answer to be reached (cannot_prove, store_deleted,
// role_conflict, in_use_elsewhere) are covered by the Go page tests.

const HEAL = 'till_identity.heal.';
const PORT_BASE = Number(process.env.UT_E2E_HEAL_PORT_BASE) || 9391;

type State = {
  name: string;
  seed: Record<string, string>;
  short: string;
  sentence: RegExp;
  button?: string; // undefined = no button
  href?: RegExp;
};

const STATES: State[] = [
  {
    name: 'removed',
    seed: { state: 'needs_person', reason: 'removed' },
    short: 'Removed from your shop',
    sentence: /This till was removed from your shop's account\. Its sales are saved on this till, and it can still sell\. To connect it again, ask the shop owner for a code and type it here\./,
    button: 'Enter a code',
    href: /\/settings\?reconnect=1#pair-details$/,
  },
  {
    name: 'replaced',
    seed: { state: 'needs_person', reason: 'replaced', detail: 'Counter Mac' },
    short: 'Replaced by Counter Mac',
    sentence: /Counter Mac replaced this till and is now the main till of your shop\. This till can still sell, but its sales no longer reach your account\. To keep using it, join it to Counter Mac as an additional till\./,
    button: 'Join as an additional till',
    href: /\/tills\?tab=add&need=join$/,
  },
  {
    name: 'moved',
    seed: { state: 'needs_person', reason: 'moved', detail: 'Other Shop' },
    short: 'Moved to another shop',
    sentence: /This till now belongs to another shop\. Its connection to the old shop has ended\./,
    button: 'Open Till registration',
    href: /\/settings#registration$/,
  },
  {
    name: 'reconnecting',
    seed: { state: 'healing', reason: 'reconnecting' },
    short: 'Reconnecting…',
    sentence: /This till is reconnecting to your shop\. Selling works as normal\. Nothing needs doing\./,
  },
];

function bootTill(port: number, seed: Record<string, string>) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ut-heal-'));
  const env = { ...process.env, UT_DATA_DIR: dataDir };
  for (const [k, v] of Object.entries(seed)) {
    execFileSync('go', ['run', './e2e/till_setting', 'set', HEAL + k, v], { cwd: REPO_ROOT, env, stdio: ['ignore', 'ignore', 'inherit'] });
  }
  const bin = fs.existsSync(PREBUILT_BIN) ? PREBUILT_BIN : path.join(dataDir, '.ut-heal-bin');
  if (bin !== PREBUILT_BIN) execFileSync('go', ['build', '-o', bin, REPO_ROOT], { cwd: REPO_ROOT, stdio: 'inherit' });
  const child: ChildProcess = spawn(bin, [], {
    cwd: dataDir,
    stdio: 'ignore',
    env: { ...env, UT_AUTH: 'off', UT_LISTEN_ADDR: `127.0.0.1:${port}`, UT_OPEN_BROWSER: '0', UT_LAN_DISCOVERY: '0', UT_UPDATE_CHECK: '0', UT_NEVER_REGISTER: '1' },
  });
  const url = `http://127.0.0.1:${port}`;
  return {
    url,
    async ready() {
      for (let i = 0; i < 120; i++) {
        if (await isHealthy(url)) return;
        await new Promise((r) => setTimeout(r, 500));
      }
      throw new Error(`heal till ${url} did not come up`);
    },
    stop() {
      child.kill('SIGTERM'); // our own child, by handle
      fs.rmSync(dataDir, { recursive: true, force: true });
    },
  };
}

test.describe('till self-heal states (ut-docs#7097)', () => {
  test.setTimeout(180_000);
  STATES.forEach((st, i) => {
    for (const vp of [
      { width: 1024, height: 600 },
      { width: 360, height: 740 },
    ]) {
      test(`${st.name} at ${vp.width}x${vp.height}: words, one button, no overflow`, async ({ page }) => {
        const till = bootTill(PORT_BASE + i * 2 + (vp.width === 360 ? 1 : 0), st.seed);
        try {
          await till.ready();
          await page.setViewportSize(vp);
          await page.goto(`${till.url}/tills?tab=this`);

          // The chip: ONE control; short words; the button label only when there is a button.
          const chip = page.locator('[data-testid="sb-cloud-auth"]');
          await expect(chip).toHaveCount(1);
          await expect(chip).toHaveAttribute('data-cloud-auth', 'heal');
          await expect(chip.locator('.sb-cloud-auth-text')).toHaveText(st.short);
          await expect(chip).toHaveAttribute('title', st.sentence);
          const chipButton = chip.locator('[data-testid="sb-cloud-heal-action"]');
          if (st.button) {
            await expect(chipButton).toHaveText(st.button);
            await expect(chip).toHaveAttribute('href', st.href!);
          } else {
            await expect(chipButton).toHaveCount(0);
          }
          await expect(page.getByText(/internet/i)).toHaveCount(0);

          // The page: same sentence, one button (a self-healing till has neither banner nor button).
          const banner = page.locator('[data-testid="tills-heal-banner"]');
          if (st.button) {
            await expect(banner).toHaveCount(1);
            await expect(banner).toContainText(st.sentence);
            await expect(banner.locator('a, button')).toHaveCount(1);
            await expect(banner.locator('a')).toHaveText(st.button);
            const box = await banner.locator('a').boundingBox();
            expect(box!.height, 'button touch target >= 44px').toBeGreaterThanOrEqual(44);
          } else {
            await expect(banner).toHaveCount(0);
            await expect(page.locator('[data-testid="tills-this-sentence"]')).toContainText('Nothing needs doing.');
          }

          // Nothing scrolls sideways.
          const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
          expect(overflow).toBeLessThanOrEqual(0);

          // The button's one tap shows the code field without scrolling (code buttons only).
          if (st.button && /reconnect=1/.test(String(st.href))) {
            await banner.locator('a').click();
            await expect(page).toHaveURL(/\/settings\?reconnect=1#pair-details$/);
            await expect(page.locator('#pair-code')).toBeInViewport();
            await expect(page.locator('[data-testid="heal-notice"]')).toContainText(st.sentence);
          }
        } finally {
          till.stop();
        }
      });
    }
  });
});
