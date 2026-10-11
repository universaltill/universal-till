import zlib from 'zlib';
import type { Page, Request } from '@playwright/test';
import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm } from './helpers';

// ut-docs#4137 (ADR-0154): a shop with NO background-removal service (the
// shared default till configures none) still gets "Remove background" on
// the item, category and variant photo pickers — the built-in engine: the
// vendored u2netp model runs in the browser (a Web Worker), and the till
// only squares the cut-out (POST /api/catalog/image/tile). Pinned in a real
// browser:
//   - nothing (runtime, wasm, model, worker) loads until the tap;
//   - every request stays on the till's own origin (no CDN, no third party
//     — the flow works with the internet off) and /cutout is never called;
//   - the product is cut out (corner = tile background, centre = product);
//   - a background change re-tiles the same cut-out: one model download;
//   - Use this saves through the page's own upload;
//   - a variant pick waits for the offer instead of uploading at once.
// The rembg-configured case is photo-cutout-3127.spec.ts (project `cutout`).
//
// HONESTY NOTE: desktop Chromium with synthetic clicks. Timings on the Pi /
// tablet are recorded in the review record, not here.

const RUN = Date.now().toString(36).toUpperCase();

// A w×h white RGB PNG with a solid dark-blue square "product" in the middle.
function productPNG(w = 480, h = 360): Buffer {
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (buf: Buffer) => {
    let c = 0xffffffff;
    for (const byte of buf) c = crcTable[(c ^ byte) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type: string, data: Buffer) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type, 'ascii'), data]);
    const c = Buffer.alloc(4);
    c.writeUInt32BE(crc(td));
    return Buffer.concat([len, td, c]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  const raw = Buffer.alloc((w * 3 + 1) * h, 255);
  const s = Math.round(h * 0.45);
  const x0 = Math.round((w - s) / 2), y0 = Math.round((h - s) / 2);
  for (let y = 0; y < h; y++) {
    const o = y * (w * 3 + 1);
    raw[o] = 0;
    for (let x = x0; x < x0 + s && y >= y0 && y < y0 + s; x++) {
      raw[o + 1 + x * 3] = 20;
      raw[o + 2 + x * 3] = 40;
      raw[o + 3 + x * 3] = 160;
    }
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', zlib.deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}
const photo = (name: string) => ({ name, mimeType: 'image/png', buffer: productPNG() });

async function pixel(page: Page, panel: string, x: number, y: number): Promise<number[]> {
  return page.locator(`${panel} [data-cutout-result]`).evaluate(async (img: HTMLImageElement, [px, py]) => {
    if (!img.complete) await new Promise((r) => img.addEventListener('load', r, { once: true }));
    const c = document.createElement('canvas');
    c.width = img.naturalWidth;
    c.height = img.naturalHeight;
    const ctx = c.getContext('2d')!;
    ctx.drawImage(img, 0, 0);
    return Array.from(ctx.getImageData(px, py, 1, 1).data);
  }, [x, y]);
}

function recordRequests(page: Page): Request[] {
  const seen: Request[] = [];
  page.context().on('request', (r) => seen.push(r));
  return seen;
}
const paths = (seen: Request[]) => seen.map((r) => new URL(r.url()).pathname);

test('item photo, no service configured: built-in removal runs in the browser, only on the tap, on the till origin', async ({ page, baseURL }) => {
  test.setTimeout(120_000);
  const assertClean = watchConsole(page);
  const seen = recordRequests(page);
  await page.goto('/catalog');
  await page.locator('.catalog-row', { hasText: 'Whole Milk 1L' }).click();
  await page.locator('#item-form-tab-image').click();
  const P = '#image-cutout';
  await expect(page.locator(P)).toHaveAttribute('data-cutout-engine', 'builtin');

  await page.locator('#image-file').setInputFiles(photo('milk.png'));
  await expect(page.locator(P)).toBeVisible();
  // Nothing of the engine loads before the operator asks.
  expect(paths(seen).filter((p) => p.includes('bg-removal') || p.includes('photo-cutout-worker'))).toEqual([]);

  await page.locator(`${P} [data-cutout-action="remove"]`).click();
  await expect(page.locator(`${P} [data-cutout-result]`)).toBeVisible({ timeout: 90_000 });
  await expect(page.locator(`${P} [data-cutout-result]`)).toHaveJSProperty('naturalWidth', 512);

  const got = paths(seen);
  for (const want of ['/public/photo-cutout-worker.js', '/public/vendor/bg-removal/ort.wasm.bundle.min.mjs',
    '/public/vendor/bg-removal/ort-wasm-simd-threaded.wasm', '/public/vendor/bg-removal/u2netp.onnx', '/api/catalog/image/tile']) {
    expect(got, `expected a request to ${want}`).toContain(want);
  }
  expect(got).not.toContain('/api/catalog/image/cutout');
  const origin = new URL(baseURL!).origin;
  const foreign = seen.map((r) => r.url()).filter((u) => !u.startsWith(origin) && !u.startsWith('data:') && !u.startsWith('blob:'));
  expect(foreign, 'every request stays on the till origin').toEqual([]);

  // The product is cut out and centred on the default (white) tile.
  expect((await pixel(page, P, 1, 1)).slice(0, 3)).toEqual([255, 255, 255]);
  const mid = await pixel(page, P, 256, 256);
  expect(mid[3]).toBe(255);
  expect(mid[2]).toBeGreaterThan(mid[0] + 60); // the blue product, not white

  // No background: re-tiled from the same cut-out, no second model run.
  await page.locator(`${P} [data-cutout-bg-choice="transparent"]`).click();
  await expect.poll(async () => (await pixel(page, P, 1, 1))[3]).toBe(0);
  expect(paths(seen).filter((p) => p === '/public/vendor/bg-removal/u2netp.onnx')).toHaveLength(1);
  expect(paths(seen).filter((p) => p === '/api/catalog/image/tile')).toHaveLength(2);

  await page.locator(`${P} [data-cutout-bg-choice="white"]`).click();
  await expect.poll(async () => (await pixel(page, P, 1, 1))[3]).toBe(255);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item/image') && r.request().method() === 'POST'),
    page.locator(`${P} [data-cutout-action="accept"]`).click(),
  ]);
  await expect(page.locator('#image-msg')).toContainText('updated');
  await expect(page.locator(P)).toBeHidden();

  // The rembg route is still off on this till.
  const res = await page.request.post('/api/catalog/image/cutout', {
    multipart: { photo: { name: 'p.png', mimeType: 'image/png', buffer: Buffer.from('x') } },
  });
  expect(res.status()).toBe(404);
  assertClean();
});

test('variant and category pickers offer it too; a variant pick waits for the choice', async ({ page }) => {
  const assertClean = watchConsole(page);
  const name = `Cutout Builtin ${RUN}`;
  await page.goto('/catalog');
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('3.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
    page.locator('#item-form-submit').click(),
  ]);
  await closeItemForm(page);
  const itemId = (await page.locator('.catalog-row', { hasText: name }).getAttribute('data-id'))!;
  await page.locator('.catalog-row', { hasText: name }).click();
  await page.locator('#item-form-tab-variants').click();
  await page.locator('input[form="vf-new"][name="name"]').fill('Small');
  await page.locator('input[form="vf-new"].variant-price-major').fill('1.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
    page.locator('button[form="vf-new"][type=submit]').click(),
  ]);
  const V = '#variant-image-cutout';
  await expect(page.locator(V)).toHaveAttribute('data-cutout-engine', 'builtin');
  let uploads = 0;
  page.on('request', (r) => { if (r.url().includes('/api/catalog/variant/image') && r.method() === 'POST') uploads++; });
  await page.locator('.vg-row:not(.vg-new) .vg-img-upload input[type=file]').setInputFiles(photo('small.png'));
  await expect(page.locator(V)).toBeVisible();
  expect(uploads).toBe(0);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant/image') && r.request().method() === 'POST'),
    page.locator(`${V} [data-cutout-action="original"]`).click(),
  ]);
  await page.request.post('/api/catalog/item/deactivate', { form: { id: itemId } });

  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await page.locator('#category-image-file').setInputFiles(photo('cat.png'));
  await expect(page.locator('#category-image-cutout')).toBeVisible();
  await expect(page.locator('#category-image-cutout')).toHaveAttribute('data-cutout-engine', 'builtin');
  assertClean();
});
