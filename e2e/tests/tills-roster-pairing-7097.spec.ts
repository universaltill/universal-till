import * as crypto from 'crypto';
import { test, expect } from './fixtures';
import { answerTillConfirm } from './helpers';

// ut-docs#7097 / ut-docs#7057 (T7): one pending pair request per device, and
// the Manage-tills page refresh never wipes what a manager is working on.

const DEVICE = 'till-7097aaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';

async function pairRequest(request: any, name: string, secret: string, deviceId: string): Promise<string> {
  const commitment = crypto.createHash('sha256').update(secret).digest('hex');
  // The pair-request limiter allows 5 a minute per source and this file sends
  // 6: on a 429 wait for the window to open instead of failing.
  let res = await request.post('/api/sync/pair-request', { data: { device_name: name, commitment, device_id: deviceId } });
  for (let i = 0; i < 8 && res.status() === 429; i++) {
    await new Promise((r) => setTimeout(r, 10_000));
    res = await request.post('/api/sync/pair-request', { data: { device_name: name, commitment, device_id: deviceId } });
  }
  expect(res.status(), await res.text()).toBe(200);
  return (await res.json()).data.id;
}

test.describe('roster and pairing requests (ut-docs#7097)', () => {
  test('three requests from one phone leave one pending row, the newest', async ({ page, request }) => {
    const name = 'e2e-7097-phone';
    const ids: string[] = [];
    for (let i = 1; i <= 3; i++) ids.push(await pairRequest(request, name, `e2e-7097-secret-${i}`, DEVICE));
    try {
      await page.goto('/tills?tab=add');
      await expect(page.locator('#tills-pairing-card tr', { hasText: name })).toHaveCount(1);
      // The older requests are gone for the phone too: only the newest can be approved.
      expect((await request.get(`/api/sync/pair-requests/${ids[0]}`, { headers: { 'X-UT-Pair-Request-Secret': 'e2e-7097-secret-1' } })).status()).toBe(404);
    } finally {
      await request.post(`/api/sync/pair-requests/${ids[2]}/deny`);
    }
  });

  test('a half-used approve box is not re-rendered by the refresh or a push', async ({ page, request }) => {
    const name = 'e2e-7097-approve-box';
    const id = await pairRequest(request, name, 'e2e-7097-secret-box', 'till-7097bbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb');
    try {
      await page.goto('/tills?tab=add');
      const row = page.locator('#tills-pairing-card tr', { hasText: name });
      await expect(row).toHaveCount(1);
      await page.evaluate(() => { (document.getElementById('pending-pairings') as any).__kept = true; });
      // Touch the box (no focus kept, as with a tap), then a push arrives.
      await row.locator('input[name="manager_pin"]').dispatchEvent('pointerdown');
      await page.evaluate(() => (window as any).htmx.trigger(document.body, 'tills-changed'));
      await page.waitForTimeout(1500);
      expect(await page.evaluate(() => (document.getElementById('pending-pairings') as any).__kept), 'the box was re-rendered').toBe(true);
    } finally {
      await request.post(`/api/sync/pair-requests/${id}/deny`);
    }
  });

  test('the roster refresh updates status cells and keeps the rows', async ({ page, request }) => {
    const token = await request.post('/api/sync/enroll-token');
    const m = (await token.text()).match(/<code[^>]*>([^<]+)<\/code>/);
    const { token: tok } = JSON.parse(Buffer.from(m![1].trim(), 'base64url').toString('utf8'));
    const name = 'e2e-7097-roster-till';
    expect((await request.post('/api/sync/enroll', { data: { token: tok, name } })).ok()).toBe(true);
    try {
      await page.goto('/tills?tab=others');
      const row = page.locator('#tills-roster tr', { hasText: name });
      await expect(row).toHaveCount(1);
      await page.evaluate((n) => {
        const tr = Array.from(document.querySelectorAll('#tills-roster tr')).find((r) => r.textContent!.includes(n)) as any;
        tr.__kept = true;
      }, name);
      await page.evaluate(() => (window as any).htmx.ajax('GET', '/ui/tills/roster', { target: '#tills-roster', swap: 'innerHTML' }));
      await page.waitForTimeout(1000);
      const kept = await page.evaluate((n) => {
        const tr = Array.from(document.querySelectorAll('#tills-roster tr')).find((r) => r.textContent!.includes(n)) as any;
        return !!tr && tr.__kept === true;
      }, name);
      expect(kept, 'the roster row was re-rendered instead of only its status cells').toBe(true);
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

  test('a till that joins again under a new name keeps its one row', async ({ request }) => {
    test.setTimeout(180_000);
    // A rename takes the row only for a request a manager approved that NAMED
    // this device (review 2026-11-27, finding 1): a minted code never does.
    const dev = 'till-7097cccc-cccc-4ccc-8ccc-cccccccccccc';
    const joinApproved = async (name: string, secret: string) => {
      const id = await pairRequest(request, name, secret, dev);
      const approve = await request.post(`/api/sync/pair-requests/${id}/approve`);
      expect(approve.ok(), await approve.text()).toBe(true);
      const poll = await request.get(`/api/sync/pair-requests/${id}`, { headers: { 'X-UT-Pair-Request-Secret': secret } });
      expect(poll.ok(), await poll.text()).toBe(true);
      const token = (await poll.json()).data.token as string;
      return request.post('/api/sync/enroll', { data: { token, name, device_id: dev } });
    };
    const first = await joinApproved('e2e-7097-old-name', 'e2e-7097-secret-old');
    expect(first.ok(), await first.text()).toBe(true);
    const firstId = (await first.json()).data.till_id;
    const second = await joinApproved('e2e-7097-new-name', 'e2e-7097-secret-new');
    expect(second.ok(), await second.text()).toBe(true);
    expect((await second.json()).data.till_id, 'rename keeps the row').toBe(firstId);
    expect((await second.json()).data.replaced).toBe(true);
    const revoke = await request.post(`/api/sync/tills/${firstId}/revoke`);
    expect(revoke.ok()).toBe(true);
  });
});
