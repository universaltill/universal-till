import { test, expect } from '../support/fixtures';

test.describe('Plugin Install Flow (CLI + Marketplace + POS)', () => {
  test('CLI install intent API accepts plugin id+version', async ({ request, pluginFactory }) => {
    const intent = pluginFactory.createInstallIntent();

    const response = await request.post('/v1/install/intents', {
      data: intent,
    });

    expect(response.status()).toBe(201);
    const body = await response.json();
    // { data: …, error: null } envelope (universal-till/CLAUDE.md,
    // ut-docs#387) -- the payload moved under data, error stays null.
    expect(body.error).toBeNull();
    expect(body.data).toMatchObject({
      plugin_id: intent.plugin_id,
      version: intent.version,
      merchant_id: intent.merchant_id,
    });
  });

  test('CLI status API returns health for plugin install', async ({ request, pluginFactory }) => {
    const intent = pluginFactory.createInstallIntent();

    const response = await request.get(
      `/v1/install/status?plugin_id=${encodeURIComponent(intent.plugin_id)}&merchant_id=${encodeURIComponent(intent.merchant_id)}`,
    );

    expect(response.status()).toBe(200);
    const body = await response.json();
    // { data: …, error: null } envelope (universal-till/CLAUDE.md,
    // ut-docs#387) -- the payload moved under data, error stays null.
    expect(body.error).toBeNull();
    expect(body.data).toMatchObject({
      plugin_id: intent.plugin_id,
      status: expect.any(String),
    });
  });

  test('Offline bundle export/import validates checksum', async ({ request, pluginFactory }) => {
    const bundle = pluginFactory.createBundle();

    const exportResponse = await request.post('/v1/install/bundles/export', {
      data: bundle,
    });
    expect(exportResponse.status()).toBe(200);

    const importResponse = await request.post('/v1/install/bundles/import', {
      data: bundle,
    });
    expect(importResponse.status()).toBe(202);
  });

  test('POS plugin host reports install status back to marketplace', async ({ request, pluginFactory }) => {
    const payload = {
      ...pluginFactory.createInstallIntent(),
      state: 'active',
      error: null,
      timestamp: new Date().toISOString(),
    };

    const response = await request.post('/v1/telemetry/plugins', {
      data: payload,
    });

    expect(response.status()).toBe(202);
  });

  test('FAQ plugin entrypoint is visible in POS UI', async ({ page }) => {
    await page.goto('/');

    // Touch nav: reach Help/Support (and its FAQ plugin entry) via the ☰ Menu
    // button → the Help tile on the touch menu page.
    await page.getByTestId('nav-menu').click();
    await page.locator('.menu-tile[href="/help"]').click();
    await expect(page.getByTestId('plugin-faq-entry')).toBeVisible();
  });
});
