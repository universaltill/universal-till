import { execFileSync, spawn, type ChildProcess } from 'child_process';
import fs from 'fs';
import http from 'http';
import os from 'os';
import path from 'path';
import type { APIRequestContext, Browser, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { ADMIN_PIN, gotoTillsSection, watchConsole, type TillsSection } from './helpers';
import { PREBUILT_BIN, REPO_ROOT, isHealthy } from './worker-till';

// ut-docs#7173 (epic #7097): the five-tab /tills page became sections inside
// Settings > Tills & devices, one tree:
//   This till, Shop and sync, Other tills, Old entries (a sub-row with a count
//   tag), Add a till, Card readers, Kiosk, Notices.
// (a) every OLD entry point still lands on the right card, and so does the
//     tree row for it; (b) the tree works from the keyboard; (c) the tree's
//     roles and the count tag's words; (d) the Other tills banner and Old
//     entries for each account state; (e) the remove flow with the inline
//     manager-PIN panel, which is never a dialog.
//
// (a)-(c) run on the shared worker till. (d)/(e) need ghosts, which only exist
// when the till can read a shop account list, so they boot a throwaway till
// pointed at a tiny fake cloud (the same idea as cloud-heal-states-7097 and
// e2e/fake-move-cloud.mjs): its GET /v1/stores/devices answers what the test
// says, and the ghosts are tills really enrolled on that till's network that
// the account does not list.
//
// Hooks the PIN panel is written against (built in parallel): opener buttons
// carry data-pin-open; the panel is [data-pin-panel] with
// input[type=password][name=override_pin] and a [data-pin-confirm] button; a
// wrong PIN keeps the panel open with an error in role=alert.

const TREE_ROWS: TillsSection[] = ['this', 'shop', 'others', 'old', 'add', 'readers'];
const row = (page: Page, section: string) => page.locator(`#settings-tree a[data-section="${section}"]`);

// ---------------------------------------------------------------------------
// (a) old entries -> new places
// ---------------------------------------------------------------------------

test.describe('old URLs and old card ids land on the right card (ut-docs#7173)', () => {
  // GET /tills[?tab=..] is a 303; the fragment is in the Location header.
  const REDIRECTS: Array<[string, string]> = [
    ['/tills', '/settings#settings-tills-this'],
    ['/tills?tab=this', '/settings#settings-tills-this'],
    ['/tills?tab=others', '/settings#settings-tills-others'],
    ['/tills?tab=others&refresh=1', '/settings?refresh=1#settings-tills-others'],
    ['/tills?tab=add', '/settings#settings-tills-add'],
    ['/tills?tab=add&need=join', '/settings?need=join#settings-tills-add'],
    ['/tills?tab=add&need=another', '/settings?need=another#settings-tills-add'],
    ['/tills?tab=add&need=replace', '/settings?need=replace#settings-tills-add'],
    ['/tills?tab=readers', '/settings#settings-tills-readers'],
    ['/tills?tab=settings', '/settings#settings-tills-shop'],
  ];
  for (const [from, to] of REDIRECTS) {
    test(`GET ${from} answers 303 to ${to}`, async ({ request }) => {
      const res = await request.get(from, { maxRedirects: 0 });
      expect(res.status()).toBe(303);
      const loc = (res.headers()['location'] || '').replace(/^https?:\/\/[^/]+/, '');
      expect(loc).toBe(to);
    });
  }

  // Each old way in, followed by a real browser, and the card it must show.
  // nested: an old card id that lives on as an anchor inside the card.
  type Entry = { from: string; card: string; nested?: string; urlAfter?: RegExp };
  const ENTRIES: Entry[] = [
    { from: '/tills', card: 'settings-tills-this', urlAfter: /\/settings#settings-tills-this$/ },
    { from: '/tills?tab=this', card: 'settings-tills-this' },
    { from: '/tills?tab=others', card: 'settings-tills-others' },
    { from: '/tills?tab=add', card: 'settings-tills-add' },
    { from: '/tills?tab=add&need=join', card: 'settings-tills-add', urlAfter: /\/settings\?need=join#settings-tills-add$/ },
    { from: '/tills?tab=readers', card: 'settings-tills-readers' },
    { from: '/tills?tab=settings', card: 'settings-tills-shop' },
    { from: '/settings#registration', card: 'settings-tills-shop', nested: 'registration' },
    { from: '/settings#subscription', card: 'settings-tills-shop', nested: 'subscription' },
    { from: '/settings#pair-details', card: 'settings-tills-shop', nested: 'pair-details' },
    { from: '/settings#settings-tills', card: 'settings-tills-this' },
    { from: '/settings#account', card: 'settings-tills-shop' },
    { from: '/settings#network', card: 'settings-tills-shop' },
    { from: '/settings#kiosk', card: 'settings-kiosk' },
    { from: '/settings#notices', card: 'settings-notices' },
    { from: '/settings#settings-kiosk-idle-reset', card: 'settings-kiosk', nested: 'settings-kiosk-idle-reset' },
    { from: '/settings#settings-kiosk-payment-mode', card: 'settings-kiosk', nested: 'settings-kiosk-payment-mode' },
  ];
  for (const e of ENTRIES) {
    test(`${e.from} shows #${e.card}${e.nested ? ` with #${e.nested}` : ''}`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.goto(e.from);
      // Notices is hidden when the shop has no account link-out; nothing to land on then.
      test.skip(e.card === 'settings-notices' && (await page.locator('#settings-notices').count()) === 0, 'Notices is not offered on this till');
      await expect(page.locator(`#${e.card}`)).toBeVisible();
      for (const other of ['settings-tills-this', 'settings-tills-shop', 'settings-tills-others', 'settings-tills-add', 'settings-tills-readers', 'settings-kiosk']) {
        if (other !== e.card) await expect(page.locator(`#${other}`)).toBeHidden();
      }
      if (e.nested) {
        // Subscription is manager-only: only its place is asserted, not its content.
        if (e.nested === 'subscription') await expect(page.locator(`#${e.card} #${e.nested}`)).toBeAttached();
        else await expect(page.locator(`#${e.card} #${e.nested}`)).toBeVisible();
      }
      await expect(row(page, e.card)).toHaveAttribute('aria-current', 'page');
      if (e.urlAfter) await expect(page).toHaveURL(e.urlAfter);
      assertClean();
    });
  }

  test('?need=join selects "join" in Add a till and the lazy section loads once shown', async ({ page }) => {
    await gotoTillsSection(page, 'add', '?need=join');
    await expect(page.getByTestId('tills-add-join')).toBeVisible();
  });

  // Regression (Tester bug 1): a cold first load straight onto a lazy section
  // (the old /tills?tab= redirects land here) must load it, although the
  // deferred tills-tree.js registers after the page's own hash handling.
  for (const sec of ['this', 'others', 'old'] as const) {
    test(`a cold deep link to #settings-tills-${sec} loads the section without a click`, async ({ page }) => {
      // First navigation of a fresh context: nothing cached, no hashchange.
      await page.goto(`/settings#settings-tills-${sec}`);
      const body = page.locator(`#settings-tills-${sec} [data-tills-body]`).first();
      test.skip((await body.count()) === 0, `${sec} is not offered on this till`);
      await expect(body).toHaveAttribute('data-loaded', '1');
    });
  }

  // Reached through the tree instead of a URL: click each row.
  const TREE_TARGETS = [...TREE_ROWS.map((s) => `settings-tills-${s}`), 'settings-kiosk', 'settings-notices'];
  for (const id of TREE_TARGETS) {
    test(`tree row ${id} opens its card without a page load`, async ({ page }) => {
      const assertClean = watchConsole(page);
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.goto(id === 'settings-tills-this' ? '/settings#settings-tills-shop' : '/settings#settings-tills-this');
      await page.evaluate(() => ((window as any).__tree7173 = 'alive'));
      test.skip((await row(page, id).count()) === 0, `${id} is not offered on this till`);
      await row(page, id).click();
      await expect(page.locator(`#${id}`)).toBeVisible();
      await expect(row(page, id)).toHaveAttribute('aria-current', 'page');
      await expect(page).toHaveURL(new RegExp(`#${id}$`));
      expect(await page.evaluate(() => (window as any).__tree7173), 'the tree row caused a full page load').toBe('alive');
      if (id.startsWith('settings-tills-')) {
        await expect(page.locator(`#${id} [data-tills-body]`).first()).toHaveAttribute('data-loaded', '1');
      }
      assertClean();
    });
  }

  test('no tab bar, no "Manage tills" hop, no Registers link, no my.universaltill.com link in the tills sections', async ({ page }) => {
    for (const s of TREE_ROWS) {
      await gotoTillsSection(page, s);
      const card = page.locator(`#settings-tills-${s}`);
      await expect(card.locator('[role="tablist"][data-testid^="tills-tab"], [data-testid^="tills-tab-"]')).toHaveCount(0);
      await expect(card.locator('a[href="/registers"]')).toHaveCount(0);
      await expect(card.locator('a[href*="my.universaltill.com"]')).toHaveCount(0);
    }
    await page.goto('/settings#settings-tills-this');
    await expect(page.getByRole('button', { name: /manage tills/i })).toHaveCount(0);
    await expect(page.getByRole('link', { name: /manage tills/i })).toHaveCount(0);
  });
});

// ---------------------------------------------------------------------------
// This till: one Save; technical details folded
// ---------------------------------------------------------------------------

test('This till has one Save and the device id sits inside Technical details', async ({ page }) => {
  await gotoTillsSection(page, 'this');
  await expect(page.getByTestId('tills-this-save')).toHaveCount(1);
  await expect(page.locator('#settings-tills-this').getByRole('button', { name: /^save/i })).toHaveCount(1);
  const tech = page.getByTestId('tills-technical-details');
  await expect(tech).toHaveCount(1);
  expect(await tech.evaluate((el) => el.tagName)).toBe('DETAILS');
  await expect(tech).not.toHaveAttribute('open', /.*/);
  await expect(page.getByTestId('register-facts-device')).toBeHidden();
  await tech.locator('summary').click();
  await expect(tech).toHaveAttribute('open', '');
});

// ---------------------------------------------------------------------------
// (b) keyboard
// ---------------------------------------------------------------------------

async function tabTo(page: Page, section: string, max = 80): Promise<void> {
  for (let i = 0; i < max; i++) {
    await page.keyboard.press('Tab');
    const cur = await page.evaluate(() => document.activeElement?.getAttribute('data-section') || '');
    if (cur === section) return;
  }
  throw new Error(`Tab never reached the tree row ${section} in ${max} presses`);
}

test.describe('keyboard (ut-docs#7173)', () => {
  test('Tab walks the tree rows in order, Enter opens a section and focus lands on its heading', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings#settings-tills-this');
    await expect(page.locator('#settings-tills-this')).toBeVisible();
    await page.locator('#settings-q').focus();

    const seen: string[] = [];
    for (let i = 0; i < 80 && seen.length < TREE_ROWS.length; i++) {
      await page.keyboard.press('Tab');
      const cur = await page.evaluate(() => document.activeElement?.getAttribute('data-section') || '');
      if (cur.startsWith('settings-tills-') && !seen.includes(cur)) seen.push(cur);
    }
    expect(seen, 'tree rows reachable by Tab, in tree order').toEqual(TREE_ROWS.map((s) => `settings-tills-${s}`));

    // Back to the search box, then to "Other tills" and open it with Enter.
    await page.locator('#settings-q').focus();
    await tabTo(page, 'settings-tills-others');
    await expect(row(page, 'settings-tills-others')).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.locator('#settings-tills-others')).toBeVisible();
    await expect(page.locator('#settings-tills-others h2')).toBeFocused();
    await expect(page.locator('#settings-tills-others [data-tills-body]').first()).toHaveAttribute('data-loaded', '1');

    // Old entries is a sub-row but a normal stop in the same list.
    await page.locator('#settings-q').focus();
    await tabTo(page, 'settings-tills-old');
    await page.keyboard.press('Enter');
    await expect(page.locator('#settings-tills-old')).toBeVisible();
    await expect(page.locator('#settings-tills-old h2')).toBeFocused();
    assertClean();
  });
});

// ---------------------------------------------------------------------------
// (c) roles and names
// ---------------------------------------------------------------------------

test.describe('a11y (ut-docs#7173)', () => {
  test('the tree is a labelled list of links, the current row says so, Old entries is a sub-row', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/settings#settings-tills-others');
    const nav = page.locator('aside#settings-nav');
    await expect(nav).toHaveAttribute('aria-label', /.+/);
    const tree = nav.locator('ul#settings-tree');
    await expect(tree).toBeVisible();
    for (const s of TREE_ROWS) {
      const r = row(page, `settings-tills-${s}`);
      await expect(r).toHaveCount(1);
      expect(await r.evaluate((el) => el.tagName)).toBe('A');
      await expect(r).toHaveAccessibleName(/\S/);
    }
    await expect(row(page, 'settings-tills-others')).toHaveAttribute('aria-current', 'page');
    await expect(row(page, 'settings-tills-this')).not.toHaveAttribute('aria-current', 'page');
    // Old entries sits directly under Other tills, indented (a flat list item, no nested ARIA tree).
    const oldLi = row(page, 'settings-tills-old').locator('xpath=ancestor::li[1]');
    await expect(oldLi).toHaveClass(/settings-tree-sub/);
    const othersLi = row(page, 'settings-tills-others').locator('xpath=ancestor::li[1]');
    expect(await oldLi.evaluate((el) => el.previousElementSibling?.querySelector('a')?.getAttribute('data-section'))).toBe('settings-tills-others');
    expect(await othersLi.evaluate((el) => el.classList.contains('settings-tree-sub'))).toBe(false);
  });

  test('a section opens with its heading focusable and no tab roles on the page', async ({ page }) => {
    await gotoTillsSection(page, 'others');
    await expect(page.locator('#settings-tills-others h2')).toHaveCount(1);
    await expect(page.locator('#settings-tills-others [role="tab"], #settings-tills-others [role="tablist"]')).toHaveCount(0);
    await row(page, 'settings-tills-readers').click();
    await expect(page.locator('#settings-tills-readers h2')).toBeFocused();
  });
});

// ---------------------------------------------------------------------------
// (d) + (e) ghosts: a throwaway till with a fake shop account
// ---------------------------------------------------------------------------

const PORT_BASE = Number(process.env.UT_E2E_TREE_PORT_BASE) || 9431;
const WRONG_PIN = '111111';

type Cloud = {
  url: string;
  devices: Array<Record<string, string>>;
  mode: { down: boolean };
  stop: () => Promise<void>;
};

// Stand-in for ut-cloud's GET /v1/stores/devices (enroll.Fleet). Everything
// else answers an empty 200, like fake-move-cloud.mjs.
async function startFakeCloud(port: number): Promise<Cloud> {
  const state = { devices: [] as Array<Record<string, string>>, mode: { down: false } };
  const server = http.createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      res.setHeader('Content-Type', 'application/json');
      if ((req.url || '').startsWith('/v1/stores/devices')) {
        if (state.mode.down) {
          res.writeHead(503);
          res.end(JSON.stringify({ data: null, error: 'down' }));
          return;
        }
        res.writeHead(200);
        res.end(JSON.stringify({ data: { devices: state.devices }, error: null }));
        return;
      }
      res.writeHead(200);
      res.end(JSON.stringify({ data: {} }));
    });
  });
  await new Promise<void>((r) => server.listen(port, '127.0.0.1', () => r()));
  return {
    url: `http://127.0.0.1:${port}`,
    devices: state.devices,
    mode: state.mode,
    stop: () => new Promise<void>((r) => server.close(() => r())),
  };
}

function bootTill(port: number, cloudUrl: string) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ut-tree-'));
  const bin = fs.existsSync(PREBUILT_BIN) ? PREBUILT_BIN : path.join(dataDir, '.ut-tree-bin');
  if (bin !== PREBUILT_BIN) execFileSync('go', ['build', '-o', bin, REPO_ROOT], { cwd: REPO_ROOT, stdio: 'inherit' });
  const child: ChildProcess = spawn(bin, [], {
    cwd: dataDir,
    stdio: 'ignore',
    env: {
      ...process.env,
      UT_DATA_DIR: dataDir,
      UT_AUTH: 'off',
      UT_LISTEN_ADDR: `127.0.0.1:${port}`,
      UT_OPEN_BROWSER: '0',
      UT_LAN_DISCOVERY: '0',
      UT_UPDATE_CHECK: '0',
      UT_NEVER_REGISTER: '1',
      // A linked till in a made-up shop, talking only to the fake.
      UT_MARKETPLACE_ENDPOINT_URL: cloudUrl,
      UT_MARKETPLACE_STORE_ID: 'tree-7173-store',
      UT_MARKETPLACE_DEVICE_ID: 'tree-7173-device',
      UT_MARKETPLACE_MERCHANT_TOKEN: 'tree-7173-token',
    },
  });
  const url = `http://127.0.0.1:${port}`;
  return {
    url,
    // A till that just joined is never an "old entry" (ut-docs#7098: Remove only
    // appears once it was unseen for a day), so a ghost is aged in the throwaway DB.
    ageTill(id: string) {
      const dbs: string[] = [];
      const walk = (d: string) => {
        for (const e of fs.readdirSync(d, { withFileTypes: true })) {
          const f = path.join(d, e.name);
          if (e.isDirectory()) walk(f);
          else if (e.name.endsWith('.db') && !e.name.startsWith('backup')) dbs.push(f);
        }
      };
      walk(dataDir);
      const past = new Date(Date.now() - 72 * 3600 * 1000).toISOString();
      for (const f of dbs) {
        try {
          execFileSync('sqlite3', [f, `UPDATE tills SET last_seen_at='${past}', enrolled_at='${past}' WHERE id='${id.replace(/[^A-Za-z0-9_-]/g, '')}';`]);
        } catch { /* not the till database */ }
      }
    },
    async ready() {
      for (let i = 0; i < 120; i++) {
        if (await isHealthy(url)) return;
        await new Promise((r) => setTimeout(r, 500));
      }
      throw new Error(`tree till ${url} did not come up`);
    },
    stop() {
      child.kill('SIGTERM'); // our own child, by handle
      fs.rmSync(dataDir, { recursive: true, force: true });
    },
  };
}

async function enrollGhost(api: APIRequestContext, name: string): Promise<string> {
  const token = await api.post('/api/sync/enroll-token');
  expect(token.ok(), await token.text()).toBe(true);
  const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
  const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
  const enrol = await api.post('/api/sync/enroll', { data: { token: tok, name } });
  expect(enrol.ok(), await enrol.text()).toBe(true);
  return (await enrol.json()).data.till_id;
}

async function newPage(browser: Browser, baseURL: string): Promise<Page> {
  const ctx = await browser.newContext({ baseURL });
  return ctx.newPage();
}

const oldCountTag = (page: Page) => row(page, 'settings-tills-old').locator('[data-tree-count]');
const oldEntries = (page: Page) => page.locator('#settings-tills-old').getByTestId('till-old-entry');

// Count the browser's own dialogs from now on; assert none at the end.
function watchDialogs(page: Page): () => void {
  const seen: string[] = [];
  page.on('dialog', (d) => {
    seen.push(`${d.type()}: ${d.message()}`);
    void d.dismiss().catch(() => {});
  });
  return () => expect(seen, 'a native browser dialog opened').toEqual([]);
}

test.describe.serial('Other tills banner, Old entries and removal (ut-docs#7173)', () => {
  test.setTimeout(180_000);
  let cloud: Cloud;
  let till: ReturnType<typeof bootTill>;
  let api: APIRequestContext;
  let page: Page;
  let ghostIds: string[] = [];

  test.beforeAll(async ({ browser, playwright }) => {
    cloud = await startFakeCloud(PORT_BASE + 1);
    till = bootTill(PORT_BASE, cloud.url);
    await till.ready();
    api = await playwright.request.newContext({ baseURL: till.url });
    // Finish setup and set the manager PIN the inline panel asks for (the same
    // POST the wizard's Finish button sends; helpers.ensureRegisteredAutomatically).
    const setup = await api.post('/api/setup', {
      form: { store_name: 'Tree 7173 Shop', till_name: 'Tree till', country: 'GB', pin: ADMIN_PIN, pin_confirm: ADMIN_PIN },
      maxRedirects: 0,
    });
    expect([200, 204, 302, 303], `finish setup (status ${setup.status()})`).toContain(setup.status());
    page = await newPage(browser, till.url);
  });

  test.afterAll(async () => {
    await page?.context().close();
    await api?.dispose();
    till?.stop();
    await cloud?.stop();
  });

  test.afterEach(async () => {
    // Put the till back: revoke whatever a test left behind.
    for (const id of ghostIds) await api.post(`/api/sync/tills/${id}/revoke`, { form: { override_pin: ADMIN_PIN } }).catch(() => undefined);
    ghostIds = [];
    cloud.devices.length = 0;
    cloud.mode.down = false;
  });

  // The count tag is built from the cached account list when /settings loads,
  // so warm the cache (reveal Other tills with refresh=1), then load afresh.
  async function warmThenOpen(section: TillsSection) {
    await gotoTillsSection(page, 'others', '?refresh=1');
    await page.goto(`/settings#settings-tills-${section}`);
    await expect(page.locator(`#settings-tills-${section}`)).toBeVisible();
    await expect(page.locator(`#settings-tills-${section} [data-tills-body]`).first()).toHaveAttribute('data-loaded', '1');
  }

  // -- (d) states -----------------------------------------------------------

  test('nothing to tidy: no banner, "Nothing is needed", Old entries says so, no count tag', async () => {
    const assertClean = watchConsole(page);
    await warmThenOpen('others');
    await expect(page.getByTestId('tills-old-banner')).toHaveCount(0);
    await expect(page.getByTestId('tills-nothing-needed')).toBeVisible();
    await expect(oldCountTag(page)).toHaveCount(0);
    await row(page, 'settings-tills-old').click();
    await expect(page.getByTestId('tills-old-none')).toBeVisible();
    await expect(oldEntries(page)).toHaveCount(0);
    // The tree keeps its shape: the row stays even with nothing to show.
    await expect(row(page, 'settings-tills-old')).toHaveCount(1);
    assertClean();
  });

  test('the shop account cannot be read: a plain warning with Try again, nothing marked, no count tag', async () => {
    cloud.mode.down = true;
    await gotoTillsSection(page, 'others', '?refresh=1');
    const warn = page.getByTestId('tills-account-unavailable');
    await expect(warn).toBeVisible();
    await expect(warn).toHaveAttribute('role', 'alert');
    await expect(warn.getByRole('button', { name: /try again/i })).toBeVisible();
    await expect(page.getByTestId('tills-old-banner')).toHaveCount(0);
    await page.goto('/settings#settings-tills-old');
    await expect(page.getByTestId('tills-old-offline')).toBeVisible();
    await expect(oldEntries(page)).toHaveCount(0);
    await expect(oldCountTag(page)).toHaveCount(0);
    // Back online: Try again reads the account again.
    cloud.mode.down = false;
    await gotoTillsSection(page, 'others', '?refresh=1');
    await expect(page.getByTestId('tills-account-unavailable')).toHaveCount(0);
  });

  test('two old entries: banner names them and has a fix button; Old entries lists both; the tree counts 2', async () => {
    const assertClean = watchConsole(page);
    ghostIds.push(await enrollGhost(api, 'tree-ghost-a'), await enrollGhost(api, 'tree-ghost-b'));
    ghostIds.forEach((id) => till.ageTill(id));
    await warmThenOpen('others');

    const banner = page.getByTestId('tills-old-banner');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('tree-ghost-a');
    await expect(banner).toContainText('tree-ghost-b');
    await expect(banner).toHaveAttribute('role', 'alert');
    await expect(banner).not.toContainText(/disagree/i);
    await expect(page.locator('#settings-tills-others a[href*="my.universaltill.com"]')).toHaveCount(0);
    // The banner's own button fixes it on the spot (it does not link to Old entries).
    await expect(page.getByTestId('tills-banner-remove')).toBeVisible();
    await expect(page.getByTestId('tills-banner-remove')).not.toHaveAttribute('href', /.*/);

    // (c) the count tag: the digit is decoration, the words are what is read.
    const tag = oldCountTag(page);
    await expect(tag).toHaveCount(1);
    await expect(tag.locator('[aria-hidden="true"]')).toHaveText('2');
    await expect(tag.locator('.visually-hidden')).toHaveText(/2\s+to tidy up/i);
    await expect(row(page, 'settings-tills-old')).toHaveAccessibleName(/old entries.*2\s+to tidy up/i);

    await row(page, 'settings-tills-old').click();
    await expect(oldEntries(page)).toHaveCount(2);
    await expect(oldEntries(page).filter({ hasText: 'tree-ghost-a' })).toHaveCount(1);
    await expect(page.getByTestId('tills-remove-all')).toBeVisible();
    await expect(oldEntries(page).first()).not.toContainText(/disagree|local_only|stale/i);
    assertClean();
  });

  test('a cloud-only ghost is listed with "Nothing to do" while the shop list removal is off (no dead link, no placeholder promise)', async () => {
    const now = new Date().toISOString();
    cloud.devices.push({ device_id: 'tree-cloud-ghost', till_id: 'tree-cloud-ghost-till', name: 'phone till', version: '1.0.0', first_seen: now, last_seen: now });
    await warmThenOpen('old');
    const entry = oldEntries(page).filter({ hasText: 'phone till' });
    await expect(entry).toHaveCount(1);
    await expect(entry).toHaveAttribute('data-kind', 'cloud');
    // The shop's removal (tills.cloud_remove_enabled, default off, ut-docs#7098) is off here:
    // the row says there is nothing to do and offers no button. With it on, the row gets the
    // same PIN opener and posts to cloud-remove: covered by the Go test
    // TestOldSection_CloudOnlyGhostIsRemovableWithThePinWhenSwitchedOn (the flag has no UI).
    await expect(entry).toContainText('Nothing to do');
    await expect(entry.locator('[data-pin-open]')).toHaveCount(0);
    await expect(entry).not.toContainText('next update');
    await expect(entry.locator('a[href*="my.universaltill.com"]')).toHaveCount(0);
  });

  // -- (e) remove flow ------------------------------------------------------

  test('Remove opens the PIN panel inline; a wrong PIN errors, the right one removes the row and the count follows without a reload', async () => {
    const assertClean = watchConsole(page, /^Failed to load resource:.*40[13]/);
    const noNativeDialogs = watchDialogs(page);
    ghostIds.push(await enrollGhost(api, 'tree-ghost-a'), await enrollGhost(api, 'tree-ghost-b'));
    ghostIds.forEach((id) => till.ageTill(id));
    await warmThenOpen('old');
    await page.evaluate(() => ((window as any).__tree7173 = 'alive'));
    await expect(oldEntries(page)).toHaveCount(2);
    await expect(oldCountTag(page).locator('[aria-hidden="true"]')).toHaveText('2');

    const entry = oldEntries(page).filter({ hasText: 'tree-ghost-a' });
    const opener = entry.locator('[data-pin-open]').first();
    await opener.click();

    // Inline: a panel in the page, not a dialog of any kind.
    const panel = page.locator('[data-pin-panel]:visible');
    await expect(panel).toHaveCount(1);
    await expect(page.locator('dialog[open]')).toHaveCount(0);
    await expect(page.getByTestId('till-confirm')).toBeHidden();
    const pin = panel.locator('input[type="password"][name="override_pin"]');
    await expect(pin).toBeVisible();
    await expect(pin).toBeFocused();

    // Wrong PIN: the panel stays, with the reason in an alert, and nothing is removed.
    await pin.fill(WRONG_PIN);
    await panel.locator('[data-pin-confirm]').click();
    await expect(panel.getByRole('alert')).toBeVisible();
    await expect(panel).toBeVisible();
    await expect(oldEntries(page)).toHaveCount(2);

    // Right PIN (Enter confirms, as the design says).
    await pin.fill(ADMIN_PIN);
    await pin.press('Enter');
    await expect(oldEntries(page).filter({ hasText: 'tree-ghost-a' })).toHaveCount(0);
    await expect(oldEntries(page)).toHaveCount(1);
    await expect(page.locator('[data-pin-panel]:visible')).toHaveCount(0);

    // The tree's count tag dropped 2 -> 1 and its words followed, with no reload.
    await expect(oldCountTag(page).locator('[aria-hidden="true"]')).toHaveText('1');
    await expect(oldCountTag(page).locator('.visually-hidden')).toHaveText(/1\s+to tidy up/i);
    expect(await page.evaluate(() => (window as any).__tree7173), 'the removal reloaded the page').toBe('alive');
    // Removed from the till's own list too.
    const roster = await api.get('/ui/tills/section/others');
    expect(await roster.text()).not.toContain('tree-ghost-a');

    // The Other tills banner now names one entry.
    await row(page, 'settings-tills-others').click();
    const banner = page.getByTestId('tills-old-banner');
    await expect(banner).toContainText('tree-ghost-b');
    await expect(banner).not.toContainText('tree-ghost-a');

    // Remove the last one: the tag goes, the banner turns into "Nothing is needed".
    await row(page, 'settings-tills-old').click();
    await oldEntries(page).first().locator('[data-pin-open]').first().click();
    const panel2 = page.locator('[data-pin-panel]:visible');
    await panel2.locator('input[type="password"][name="override_pin"]').fill(ADMIN_PIN);
    await panel2.locator('[data-pin-confirm]').click();
    await expect(page.getByTestId('tills-old-none')).toBeVisible();
    await expect(oldCountTag(page)).toHaveCount(0);
    await expect(row(page, 'settings-tills-old')).toHaveCount(1);
    await row(page, 'settings-tills-others').click();
    await expect(page.getByTestId('tills-old-banner')).toHaveCount(0);
    await expect(page.getByTestId('tills-nothing-needed')).toBeVisible();
    expect(await page.evaluate(() => (window as any).__tree7173), 'the removal reloaded the page').toBe('alive');
    noNativeDialogs();
    assertClean();
  });

  test('Escape closes the PIN panel and puts focus back on the button that opened it; nothing is removed', async () => {
    ghostIds.push(await enrollGhost(api, 'tree-ghost-a'), await enrollGhost(api, 'tree-ghost-b'));
    ghostIds.forEach((id) => till.ageTill(id));
    await warmThenOpen('old');
    const opener = oldEntries(page).filter({ hasText: 'tree-ghost-b' }).locator('[data-pin-open]').first();
    await opener.focus();
    await page.keyboard.press('Enter');
    const panel = page.locator('[data-pin-panel]:visible');
    await expect(panel).toHaveCount(1);
    await expect(panel.locator('input[type="password"][name="override_pin"]')).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(page.locator('[data-pin-panel]:visible')).toHaveCount(0);
    await expect(opener).toBeFocused();
    await expect(oldEntries(page)).toHaveCount(2);
    await expect(page.locator('dialog[open]')).toHaveCount(0);
  });

  test('the banner button and Remove all use the same inline PIN panel and clear every entry', async () => {
    const noNativeDialogs = watchDialogs(page);
    ghostIds.push(await enrollGhost(api, 'tree-ghost-a'), await enrollGhost(api, 'tree-ghost-b'));
    ghostIds.forEach((id) => till.ageTill(id));
    await warmThenOpen('others');
    await page.evaluate(() => ((window as any).__tree7173 = 'alive'));
    await expect(page.getByTestId('tills-old-banner')).toBeVisible();
    const fix = page.getByTestId('tills-banner-remove');
    await expect(fix).toHaveAttribute('data-pin-open', /.*/);
    await fix.click();
    const panel = page.locator('[data-pin-panel]:visible');
    await expect(panel).toHaveCount(1);
    await expect(page.locator('dialog[open]')).toHaveCount(0);
    await panel.locator('input[type="password"][name="override_pin"]').fill(ADMIN_PIN);
    await panel.locator('[data-pin-confirm]').click();
    await expect(page.getByTestId('tills-old-banner')).toHaveCount(0);
    await expect(page.getByTestId('tills-nothing-needed')).toBeVisible();
    await expect(oldCountTag(page)).toHaveCount(0);
    await row(page, 'settings-tills-old').click();
    await expect(page.getByTestId('tills-old-none')).toBeVisible();
    expect(await page.evaluate(() => (window as any).__tree7173), 'the removal reloaded the page').toBe('alive');
    noNativeDialogs();
  });
});
