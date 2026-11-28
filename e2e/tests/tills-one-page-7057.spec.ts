import * as crypto from 'crypto';
import { test, expect } from './fixtures';
import { watchConsole, answerTillConfirm, answerPinPanel, gotoTillsSection, ADMIN_PIN, ensureManagerPin } from './helpers';

// ut-docs#7057 / ut-docs#7173: "Tills & devices" is a set of sections in the
// Settings tree (it was a five-tab page at /tills). Add a till (code), a stale
// enrolment re-pairing without a rename, the Other tills roster, and the
// sections themselves (no horizontal scroll).

const SEED_COMMITMENT = 'b'.repeat(64);

test.describe('tills & devices page (ut-docs#7057)', () => {
  // Revoke now asks for the manager PIN: make sure the till has a manager
  // PIN set, whatever spec ran first (helpers.ensureManagerPin).
  test.beforeEach(async ({ page }) => { await ensureManagerPin(page); });

  test('the tree lists the Tills & devices sections in order and the keyboard moves between them', async ({ page }) => {
    // ut-docs#7173: the five tabs became rows of the Settings tree (this is
    // the short check; tills-tree-7173.spec.ts holds the full tree spec).
    const assertClean = watchConsole(page);
    await page.goto('/settings#settings-tills-this');
    const rows = page.locator('#settings-tree a[data-section^="settings-tills-"]');
    await expect(rows).toHaveCount(6); // this, shop, others, old, add, readers
    await expect(rows.nth(0)).toHaveAttribute('data-section', 'settings-tills-this');
    await expect(rows.nth(0)).toHaveAttribute('aria-current', 'page');
    await page.locator('#settings-tree a[data-section="settings-tills-others"]').click();
    await expect(page).toHaveURL(/#settings-tills-others$/);
    await expect(page.locator('#settings-tills-others')).toBeVisible();
    await expect(page.locator('#settings-tills-this')).toBeHidden();
    await expect(page.locator('#settings-tree a[data-section="settings-tills-others"]')).toHaveAttribute('aria-current', 'page');
    assertClean();
  });

  for (const vp of [{ width: 1024, height: 600 }, { width: 390, height: 844 }]) {
    test(`no section needs sideways scrolling at ${vp.width}x${vp.height}`, async ({ page }) => {
      await page.setViewportSize(vp);
      for (const id of ['this', 'others', 'old', 'add', 'readers', 'shop'] as const) {
        await gotoTillsSection(page, id);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(overflow, `section ${id} scrolls sideways`).toBeLessThanOrEqual(0);
      }
    });
  }

  test('add a till: the code and QR appear in the Add a till section', async ({ page }) => {
    await gotoTillsSection(page, 'add', '?need=another');
    await page.getByRole('button', { name: /get a new code/i }).click();
    await expect(page.locator('#till-qr .pairing-short-code')).toBeVisible();
  });

  test('a device that is already enrolled and asks to pair again is flagged, not hidden', async ({ page, request }) => {
    const token = await request.post('/api/sync/enroll-token');
    expect(token.ok()).toBe(true);
    const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
    const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
    const name = 'test-7057-bar-tablet';
    const enrol = await request.post('/api/sync/enroll', { data: { token: tok, name } });
    expect(enrol.ok(), await enrol.text()).toBe(true);

    const seeded = await request.post('/api/sync/pair-request', { data: { device_name: name, commitment: SEED_COMMITMENT } });
    expect(seeded.status(), 'the pair request must be accepted, never hidden').toBe(200);
    const id = (await seeded.json()).data.id;
    try {
      await gotoTillsSection(page, 'add');
      await expect(page.locator('#tills-pairing-card tr', { hasText: name })).toHaveCount(1);
      await expect(page.locator('#tills-pairing-card')).toContainText(/already enrolled/i);
    } finally {
      await request.post(`/api/sync/pair-requests/${id}/deny`);
      // Put the shared till back: revoke the enrolment through the page.
      await gotoTillsSection(page, 'others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      if (await row.count()) {
        await row.getByRole('button', { name: /revoke/i }).click();
        await answerPinPanel(row);
        await expect(row).toHaveCount(0);
      }
    }
  });

  test('approving a pair request from an already enrolled name replaces that till (same till id)', async ({ page, request }) => {
    const token = await request.post('/api/sync/enroll-token');
    const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
    const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
    const name = 'test-7057-replaced-till';
    const first = await request.post('/api/sync/enroll', { data: { token: tok, name } });
    expect(first.ok(), await first.text()).toBe(true);
    const firstId = (await first.json()).data.till_id;
    try {
      // The same device name asks to pair again (a new device, a stale enrolment).
      const secret = 'e2e-7057-secret';
      const commitment = crypto.createHash('sha256').update(secret).digest('hex');
      const seeded = await request.post('/api/sync/pair-request', { data: { device_name: name, commitment } });
      expect(seeded.status()).toBe(200);
      const reqId = (await seeded.json()).data.id;

      await gotoTillsSection(page, 'add');
      const card = page.locator('#tills-pairing-card tr', { hasText: name });
      await expect(card).toHaveCount(1);
      await card.locator('input[name="manager_pin"]').fill(ADMIN_PIN);
      await card.getByRole('button', { name: /approve/i }).click();
      await expect(page.locator('#tills-pairing-card tr', { hasText: name }).getByRole('button', { name: /approve/i })).toHaveCount(0);

      // The joining device collects its token and enrols under the same name.
      const polled = await request.get(`/api/sync/pair-requests/${reqId}`, { headers: { 'X-UT-Pair-Request-Secret': secret } });
      expect(polled.ok(), await polled.text()).toBe(true);
      const approvedTok = (await polled.json()).data.token;
      const second = await request.post('/api/sync/enroll', { data: { token: approvedTok, name } });
      expect(second.ok(), await second.text()).toBe(true);
      expect((await second.json()).data.till_id, 'the till keeps its id').toBe(firstId);
      expect((await second.json()).data.bearer).not.toBe((await first.json()).data.bearer);

      await gotoTillsSection(page, 'others');
      await expect(page.locator('#tills-roster tr', { hasText: name })).toHaveCount(1);
    } finally {
      await gotoTillsSection(page, 'others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      if (await row.count()) {
        await row.getByRole('button', { name: /revoke/i }).click();
        await answerPinPanel(row);
        await expect(row).toHaveCount(0);
      }
    }
  });

  test('revoke an enrolled till from the Other tills section', async ({ page, request }) => {
    const token = await request.post('/api/sync/enroll-token');
    const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
    const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
    const name = 'test-7057-revoke-till';
    expect((await request.post('/api/sync/enroll', { data: { token: tok, name } })).ok()).toBe(true);
    await gotoTillsSection(page, 'others');
    const row = page.locator('#tills-roster tr', { hasText: name });
    await expect(row).toHaveCount(1);
    await row.getByRole('button', { name: /revoke/i }).click();
    await answerPinPanel(row);
    await expect(row).toHaveCount(0);
  });

  test('change an enrolled till\'s role from the Other tills section', async ({ page, request }) => {
    const token = await request.post('/api/sync/enroll-token');
    const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
    const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
    const name = 'test-7057-role-till';
    expect((await request.post('/api/sync/enroll', { data: { token: tok, name } })).ok()).toBe(true);
    try {
      await gotoTillsSection(page, 'others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      await expect(row).toHaveCount(1);
      await row.getByRole('button', { name: /change role/i }).click();
      await answerTillConfirm(page, 'confirm');
      await expect(page.locator('#tills-roster tr', { hasText: name })).toContainText(/satellite/i);
    } finally {
      await gotoTillsSection(page, 'others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      if (await row.count()) {
        await row.getByRole('button', { name: /revoke/i }).click();
        await answerPinPanel(row);
        await expect(row).toHaveCount(0);
      }
    }
  });
});
