import * as crypto from 'crypto';
import { test, expect } from './fixtures';
import { watchConsole, answerTillConfirm, ADMIN_PIN } from './helpers';

// ut-docs#7057: ONE "Tills & devices" page with five tabs. Add a till (code),
// a stale enrolment re-pairing without a rename, the roster tab, and the tab
// bar itself (real links, keyboard, no horizontal scroll).

const SEED_COMMITMENT = 'b'.repeat(64);

test.describe('tills & devices page (ut-docs#7057)', () => {
  test('five tabs are real links and the keyboard moves between them', async ({ page }) => {
    const assertClean = watchConsole(page);
    await page.goto('/tills');
    for (const id of ['this', 'others', 'add', 'readers', 'settings']) {
      await expect(page.getByTestId(`tills-tab-${id}`)).toHaveAttribute('href', `/tills?tab=${id}`);
    }
    await page.getByTestId('tills-tab-this').focus();
    await page.keyboard.press('End');
    await expect(page.getByTestId('tills-tab-settings')).toBeFocused();
    await page.keyboard.press('Home');
    await expect(page.getByTestId('tills-tab-this')).toBeFocused();
    await page.getByTestId('tills-tab-others').click();
    await expect(page).toHaveURL(/tab=others/);
    await expect(page.getByTestId('tills-tab-others')).toHaveAttribute('aria-selected', 'true');
    assertClean();
  });

  for (const vp of [{ width: 1024, height: 600 }, { width: 390, height: 844 }]) {
    test(`no tab needs sideways scrolling at ${vp.width}x${vp.height}`, async ({ page }) => {
      await page.setViewportSize(vp);
      for (const id of ['this', 'others', 'add', 'readers', 'settings']) {
        await page.goto(`/tills?tab=${id}`);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(overflow, `tab ${id} scrolls sideways`).toBeLessThanOrEqual(0);
      }
    });
  }

  test('add a till: the code and QR appear on the Add a till tab', async ({ page }) => {
    await page.goto('/tills?tab=add&need=another');
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
      await page.goto('/tills?tab=add');
      await expect(page.locator('#tills-pairing-card tr', { hasText: name })).toHaveCount(1);
      await expect(page.locator('#tills-pairing-card')).toContainText(/already enrolled/i);
    } finally {
      await request.post(`/api/sync/pair-requests/${id}/deny`);
      // Put the shared till back: revoke the enrolment through the page.
      await page.goto('/tills?tab=others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      if (await row.count()) {
        await row.getByRole('button', { name: /revoke/i }).click();
        await answerTillConfirm(page, 'confirm');
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

      await page.goto('/tills?tab=add');
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

      await page.goto('/tills?tab=others');
      await expect(page.locator('#tills-roster tr', { hasText: name })).toHaveCount(1);
    } finally {
      await page.goto('/tills?tab=others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      if (await row.count()) {
        await row.getByRole('button', { name: /revoke/i }).click();
        await answerTillConfirm(page, 'confirm');
        await expect(row).toHaveCount(0);
      }
    }
  });

  test('revoke an enrolled till from the Other tills tab', async ({ page, request }) => {
    const token = await request.post('/api/sync/enroll-token');
    const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
    const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
    const name = 'test-7057-revoke-till';
    expect((await request.post('/api/sync/enroll', { data: { token: tok, name } })).ok()).toBe(true);
    await page.goto('/tills?tab=others');
    const row = page.locator('#tills-roster tr', { hasText: name });
    await expect(row).toHaveCount(1);
    await row.getByRole('button', { name: /revoke/i }).click();
    await answerTillConfirm(page, 'confirm');
    await expect(row).toHaveCount(0);
  });

  test('change an enrolled till\'s role from the Other tills tab', async ({ page, request }) => {
    const token = await request.post('/api/sync/enroll-token');
    const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
    const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
    const name = 'test-7057-role-till';
    expect((await request.post('/api/sync/enroll', { data: { token: tok, name } })).ok()).toBe(true);
    try {
      await page.goto('/tills?tab=others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      await expect(row).toHaveCount(1);
      await row.getByRole('button', { name: /change role/i }).click();
      await answerTillConfirm(page, 'confirm');
      await expect(page.locator('#tills-roster tr', { hasText: name })).toContainText(/satellite/i);
    } finally {
      await page.goto('/tills?tab=others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      if (await row.count()) {
        await row.getByRole('button', { name: /revoke/i }).click();
        await answerTillConfirm(page, 'confirm');
        await expect(row).toHaveCount(0);
      }
    }
  });
});
