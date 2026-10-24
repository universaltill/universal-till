import fs from 'fs';
import path from 'path';
import zlib from 'zlib';
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { watchConsole, openNewItemForm, closeItemForm, clickThenReload } from './helpers';

// ut-docs#3127: "Remove background" in the item, category and variant photo
// pickers (docs repo architecture/ai-product-photo.md §6, §8 C). Runs on
// its own till (run-till-cutout.sh, project `cutout`) whose background
// removal points at fake-rembg-server.mjs on 8102 — a stand-in for the
// shop's `rembg s` that answers a transparent PNG with an opaque red
// "product", and can be switched to fail (→ 502) or to keep no subject
// (→ 422). Pinned in a real browser:
//   - nothing is sent until Remove background is tapped;
//   - the browser downscales (a 12 MP photo reaches rembg at 1024 px);
//   - original and result side by side, the three backgrounds (the tile
//     colour is the item's own palette colour), a change re-requests and a
//     repeat is cached;
//   - Use this saves the PNG through the page's own upload (item, variant)
//     or the category's one Save; Use original photo keeps the original;
//   - errors are inline and the original stays selected.
//
// HONESTY NOTE: Chromium with synthetic clicks and a fake rembg. A real
// touchscreen and a real rembg model are the hardware lane's to confirm.

const FAKE = 'http://127.0.0.1:8102';
const RUN = Date.now().toString(36).toUpperCase();

async function fakeLast(): Promise<{ calls: number; width: number; height: number; type: string; model: string }> {
  return (await fetch(`${FAKE}/_last`)).json();
}
async function fakeMode(m: 'ok' | 'empty' | 'fail' | 'slow') {
  const r = await fetch(`${FAKE}/_mode?m=${m}`, { method: 'POST' });
  expect(r.ok).toBe(true);
}
async function fakeReset() {
  await fetch(`${FAKE}/_reset`, { method: 'POST' });
}

// A real w×h solid-colour RGB PNG, built by hand (zlib is Node's own).
function solidPNG(r: number, g: number, b: number, w = 320, h = 240): Buffer {
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
  const raw = Buffer.alloc((w * 3 + 1) * h);
  for (let y = 0; y < h; y++) {
    const o = y * (w * 3 + 1);
    for (let x = 0; x < w; x++) {
      raw[o + 1 + x * 3] = r;
      raw[o + 2 + x * 3] = g;
      raw[o + 3 + x * 3] = b;
    }
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', zlib.deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}
const photo = (name: string) => ({ name, mimeType: 'image/png', buffer: solidPNG(40, 120, 200) });

// The result tile's corner pixel: the background the till composited.
async function cornerPixel(page: Page, panel: string): Promise<number[]> {
  return page.locator(`${panel} [data-cutout-result]`).evaluate(async (img: HTMLImageElement) => {
    if (!img.complete) await new Promise((r) => img.addEventListener('load', r, { once: true }));
    const c = document.createElement('canvas');
    c.width = img.naturalWidth;
    c.height = img.naturalHeight;
    const ctx = c.getContext('2d')!;
    ctx.drawImage(img, 0, 0);
    return Array.from(ctx.getImageData(1, 1, 1, 1).data);
  });
}

async function openItemImageTab(page: Page, name: string, lang = 'en') {
  await page.goto(`/catalog?lang=${lang}`);
  await page.locator('.catalog-row', { hasText: name }).first().click();
  await page.locator('#item-form-tab-image').click();
}

async function removeBackground(page: Page, panel: string) {
  await page.locator(`${panel} [data-cutout-action="remove"]`).click();
  await expect(page.locator(`${panel} [data-cutout-result]`)).toBeVisible();
  await expect(page.locator(`${panel} [data-cutout-result]`)).toHaveJSProperty('naturalWidth', 512);
}

async function createItemWithVariant(page: Page, name: string, lang = 'en'): Promise<string> {
  await page.goto(`/catalog?lang=${lang}`);
  await openNewItemForm(page);
  await page.locator('#item-name').fill(name);
  await page.locator('#item-price').fill('4.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item') && r.request().method() === 'POST'),
    page.locator('#item-form-submit').click(),
  ]);
  await closeItemForm(page);
  const id = (await page.locator('.catalog-row', { hasText: name }).getAttribute('data-id'))!;
  await page.locator('.catalog-row', { hasText: name }).click();
  await page.locator('#item-form-tab-variants').click();
  await page.locator('input[form="vf-new"][name="name"]').fill('Large');
  await page.locator('input[form="vf-new"].variant-price-major').fill('2.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
    page.locator('button[form="vf-new"][type=submit]').click(),
  ]);
  await expect(page.locator('.vg-row:not(.vg-new)')).toBeVisible();
  return id;
}

test.beforeEach(async () => {
  await fakeReset();
});

test('item photo: offer → remove background (tile colour / white / none, cached) → Use this saves the tile', async ({ page }) => {
  const assertClean = watchConsole(page);
  await openItemImageTab(page, 'Apple Juice 1L');
  const P = '#image-cutout';
  await expect(page.locator(P)).toBeHidden();

  // The item's tile colour: its own palette value (the first swatch).
  const palette = await page.locator('#item-color-grid .item-color-tile[data-color^="#"]').first().getAttribute('data-color');
  await page.locator('#item-color').evaluate((el: HTMLInputElement, v) => { el.value = v!; }, palette);

  await page.locator('#image-file').setInputFiles(photo('juice.png'));
  await expect(page.locator(P)).toBeVisible();
  await expect(page.locator(`${P} [data-cutout-action="remove"]`)).toBeVisible();
  await expect(page.locator(`${P} [data-cutout-action="original"]`)).toBeVisible();
  await expect(page.locator(`${P} [data-cutout-action="accept"]`)).toBeHidden();
  // Nothing leaves the browser until the operator asks.
  expect((await fakeLast()).calls).toBe(0);

  await removeBackground(page, P);
  let last = await fakeLast();
  expect(last.calls).toBe(1);
  expect(last.model).toBe('u2netp');
  await expect(page.locator(`${P} [data-cutout-original]`)).toBeVisible();
  await expect(page.locator(`${P} [data-cutout-bg-choice="tile"]`)).toHaveAttribute('aria-pressed', 'true');
  const hex = palette!.replace('#', '');
  const want = [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16));
  expect((await cornerPixel(page, P)).slice(0, 3)).toEqual(want);

  // White, then no background: each a new request…
  await page.locator(`${P} [data-cutout-bg-choice="white"]`).click();
  await expect(page.locator(`${P} [data-cutout-bg-choice="white"]`)).toHaveAttribute('aria-pressed', 'true');
  await expect.poll(async () => (await cornerPixel(page, P)).slice(0, 3)).toEqual([255, 255, 255]);
  await page.locator(`${P} [data-cutout-bg-choice="transparent"]`).click();
  await expect.poll(async () => (await cornerPixel(page, P))[3]).toBe(0);
  expect((await fakeLast()).calls).toBe(3);
  // …and back to the tile colour comes from the cache.
  await page.locator(`${P} [data-cutout-bg-choice="tile"]`).click();
  await expect.poll(async () => (await cornerPixel(page, P)).slice(0, 3)).toEqual(want);
  expect((await fakeLast()).calls).toBe(3);

  // Use this: the PNG goes into #image-file and through the item's own
  // upload, at once. (Playwright cannot read a multipart File body, so the
  // input's own file is recorded as the upload starts.)
  await page.locator('#image-form').evaluate((form: HTMLFormElement) => {
    form.addEventListener('submit', () => {
      const f = (document.getElementById('image-file') as HTMLInputElement).files![0];
      (window as any).__uploaded = f ? [f.name, f.type] : null;
    }, { capture: true });
  });
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item/image') && r.request().method() === 'POST'),
    page.locator(`${P} [data-cutout-action="accept"]`).click(),
  ]);
  expect(await page.evaluate(() => (window as any).__uploaded)).toEqual(['juice-cutout.png', 'image/png']);
  await expect(page.locator('#image-msg')).toContainText('updated');
  await expect(page.locator(P)).toBeHidden();
  const thumb = page.locator('.catalog-row', { hasText: 'Apple Juice 1L' }).locator('img.thumb');
  await expect(thumb).toHaveJSProperty('complete', true, { timeout: 15_000 });
  const dims = await thumb.evaluate((img: HTMLImageElement) => [img.naturalWidth, img.naturalHeight]);
  expect(dims[0]).toBeGreaterThan(2);
  expect(dims[0]).toBe(dims[1]); // the square tile, not the 4:3 photo
  assertClean();
});

test('a 12 MP phone photo is downscaled in the browser to a 1024 px long edge', async ({ page }) => {
  const assertClean = watchConsole(page);
  await openItemImageTab(page, 'Orange Juice 1L');
  await page.locator('#image-file').evaluate(async (input: HTMLInputElement) => {
    const c = document.createElement('canvas');
    c.width = 4000;
    c.height = 3000;
    const ctx = c.getContext('2d')!;
    ctx.fillStyle = '#3366aa';
    ctx.fillRect(0, 0, c.width, c.height);
    const blob: Blob = await new Promise((r) => c.toBlob((b) => r(b!), 'image/jpeg', 0.9));
    const dt = new DataTransfer();
    dt.items.add(new File([blob], 'IMG_0001.jpg', { type: 'image/jpeg' }));
    input.files = dt.files;
    input.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await removeBackground(page, '#image-cutout');
  const last = await fakeLast();
  expect(last.type).toBe('jpeg');
  expect([last.width, last.height]).toEqual([1024, 768]);
  assertClean();
});

test('errors are inline and keep the original; Use original photo keeps it too', async ({ page }) => {
  // The browser itself logs the refused cutout calls this test provokes.
  const assertClean = watchConsole(page, /status of (502|422) /);
  await openItemImageTab(page, 'Still Water 1.5L');
  const P = '#image-cutout';
  const status = page.locator(`${P} [data-cutout-status]`);
  const selected = () => page.locator('#image-file').evaluate((i: HTMLInputElement) => i.files && i.files[0] && i.files[0].name);

  try {
    await fakeMode('fail');
    await page.locator('#image-file').setInputFiles(photo('water.png'));
    await page.locator(`${P} [data-cutout-action="remove"]`).click();
    await expect(status).toHaveText("Background removal isn't available right now. You can use the photo as it is.");
    await expect(page.locator(`${P} [data-cutout-result]`)).toBeHidden();
    expect(await selected()).toBe('water.png');

    await fakeMode('empty');
    await page.locator(`${P} [data-cutout-action="remove"]`).click();
    await expect(status).toContainText('No product found in the photo');
    expect(await selected()).toBe('water.png');
  } finally {
    await fakeMode('ok');
  }

  // Use original photo: the offer goes, the chosen original stays for the
  // normal Upload Image (a chosen file was never auto-saved).
  await page.locator(`${P} [data-cutout-action="original"]`).click();
  await expect(page.locator(P)).toBeHidden();
  expect(await selected()).toBe('water.png');
  await expect(page.locator('#image-msg')).toHaveText('');
  await page.locator('#image-form button[type=submit]').click();
  await expect(page.locator('#image-msg')).toContainText('updated');
  assertClean();
});

test('viewfinder capture: the offer comes first; Use original photo saves the capture as before', async ({ page }) => {
  const assertClean = watchConsole(page);
  await page.addInitScript(() => {
    Object.defineProperty(HTMLVideoElement.prototype, 'videoWidth', { configurable: true, get: () => 64 });
    Object.defineProperty(HTMLVideoElement.prototype, 'videoHeight', { configurable: true, get: () => 48 });
    const canvas = document.createElement('canvas');
    canvas.width = 64;
    canvas.height = 48;
    canvas.getContext('2d')!.fillRect(0, 0, 64, 48);
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: async () => (canvas as any).captureStream() },
    });
  });
  await openItemImageTab(page, 'Pepsi Can 330ml');
  let uploads = 0;
  page.on('request', (r) => { if (r.url().includes('/api/catalog/item/image') && r.method() === 'POST') uploads++; });
  await page.locator('#image-camera-btn').click();
  await page.locator('#image-viewfinder-capture-btn').click();
  await expect(page.locator('#image-cutout')).toBeVisible();
  expect(uploads).toBe(0);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item/image') && r.request().method() === 'POST'),
    page.locator('#image-cutout [data-cutout-action="original"]').click(),
  ]);
  await expect(page.locator('#image-msg')).toContainText('updated');
  assertClean();
});

test('category photo: Use this puts the tile in the form; the one Save stores it', async ({ page }) => {
  const assertClean = watchConsole(page);
  const name = `Cutout Cat ${RUN}`;
  const P = '#category-image-cutout';
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await expect(page.locator('#category-dialog')).toBeVisible();
  await page.locator('#category-form input[name="name"]').fill(name);
  await page.locator('#category-image-file').setInputFiles(photo('soup.png'));
  await expect(page.locator(P)).toBeVisible();
  // No preview of its own on this page: the panel shows the pick.
  await expect(page.locator(`${P} [data-cutout-original]`)).toBeVisible();
  expect((await fakeLast()).calls).toBe(0);
  await removeBackground(page, P);
  await page.locator(`${P} [data-cutout-action="accept"]`).click();
  await expect(page.locator(P)).toBeHidden();
  await expect(page.locator('#category-image-file-name')).toHaveText('soup-cutout.png');
  await expect(page.locator('#category-image-name')).toHaveValue('soup-cutout.png');
  await clickThenReload(page, () => page.locator('#category-dialog .record-dialog-save').click());
  const row = page.locator('#categories-table .category-row', { hasText: name }).first();
  const id = await row.getAttribute('data-id');
  await expect(row).toHaveAttribute('data-image', `/public/assets/categories/${id}/thumb.png`);
  await page.request.post(`/api/categories/${id}/active`, { form: { active: '0' } });
  assertClean();
});

// Review finding (ut-docs#3127): a slow or hung service must never take
// away the way out — Use original photo stays live while a request is in
// flight, and the late answer is dropped.
test('a request in flight: Use original photo still works, the late answer is dropped', async ({ page }) => {
  const assertClean = watchConsole(page);
  await openItemImageTab(page, 'Still Water 1.5L');
  const P = '#image-cutout';
  try {
    await fakeMode('slow');
    await page.locator('#image-file').setInputFiles(photo('slow.png'));
    await page.locator(`${P} [data-cutout-action="remove"]`).click();
    await expect(page.locator(P)).toHaveAttribute('aria-busy', 'true');
    const original = page.locator(`${P} [data-cutout-action="original"]`);
    // An immediate read, not a polling expect: polling would pass once the
    // slow answer re-enabled the button.
    expect(await original.isEnabled()).toBe(true);
    await original.click({ timeout: 1000 });
    await expect(page.locator(P)).toBeHidden();
    expect(await page.locator('#image-file').evaluate((i: HTMLInputElement) => i.files && i.files[0] && i.files[0].name)).toBe('slow.png');
    // Past the fake's 8 s: nothing comes back into a closed panel.
    await page.waitForTimeout(9000);
    await expect(page.locator(P)).toBeHidden();
    await expect(page.locator(`${P} [data-cutout-result]`)).toBeHidden();
  } finally {
    await fakeMode('ok');
  }
  assertClean();
});

// Review finding (ut-docs#3127): one panel serves every variant row, so a
// pick left undecided on one row is dropped when another row picks — it
// would otherwise sit in that row's input and never upload.
test('variant photo: picking on a second row drops the first row\'s undecided pick', async ({ page }) => {
  const assertClean = watchConsole(page);
  const itemId = await createItemWithVariant(page, `Cutout Jug ${RUN}`);
  await page.locator('input[form="vf-new"][name="name"]').fill('Small');
  await page.locator('input[form="vf-new"].variant-price-major').fill('1.00');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant')),
    page.locator('button[form="vf-new"][type=submit]').click(),
  ]);
  const rows = page.locator('.vg-row:not(.vg-new)');
  await expect(rows).toHaveCount(2);
  const large = rows.filter({ has: page.locator('input[value="Large"]') }).locator('.vg-img-upload input[type=file]');
  const small = rows.filter({ has: page.locator('input[value="Small"]') }).locator('.vg-img-upload input[type=file]');
  const P = '#variant-image-cutout';
  await large.setInputFiles(photo('large.png'));
  await expect(page.locator(`${P} [data-cutout-target]`)).toHaveText('Large');
  await small.setInputFiles(photo('small.png'));
  await expect(page.locator(`${P} [data-cutout-target]`)).toHaveText('Small');
  expect(await large.evaluate((i: HTMLInputElement) => i.files ? i.files.length : 0)).toBe(0);
  expect(await small.evaluate((i: HTMLInputElement) => i.files ? i.files.length : 0)).toBe(1);
  await page.request.post('/api/catalog/item/deactivate', { form: { id: itemId } });
  assertClean();
});

test('variant photo: a pick waits for the offer; Use this uploads the tile as the variant photo', async ({ page }) => {
  const assertClean = watchConsole(page);
  const itemId = await createItemWithVariant(page, `Cutout Mug ${RUN}`);
  let uploads = 0;
  page.on('request', (r) => { if (r.url().includes('/api/catalog/variant/image')) uploads++; });
  const P = '#variant-image-cutout';
  const input = page.locator('.vg-row:not(.vg-new) .vg-img-upload input[type=file]');
  await input.setInputFiles(photo('mug.png'));
  await expect(page.locator(P)).toBeVisible();
  await expect(page.locator(`${P} [data-cutout-target]`)).toHaveText('Large');
  expect(uploads).toBe(0);
  await removeBackground(page, P);
  expect(uploads).toBe(0);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/variant/image') && r.request().method() === 'POST'),
    page.locator(`${P} [data-cutout-action="accept"]`).click(),
  ]);
  const img = page.locator('.vg-row:not(.vg-new) .vg-img img');
  await expect(img).toHaveAttribute('src', new RegExp(`/public/assets/items/${itemId}/variants/[^/]+/thumb\\.png`));
  await expect(page.locator(P)).toBeHidden();
  await page.request.post('/api/catalog/item/deactivate', { form: { id: itemId } });
  assertClean();
});

// ut-docs#4839: "Remove background" on the photo a record ALREADY has. A
// button next to the current photo re-offers the saved image to the same
// panel; Use original photo changes nothing (no upload, no pending file).

// Save `name` as an item photo through the normal path (offer → Use original
// photo → Upload Image) and wait for it; the item then has a custom photo.
async function uploadItemPhoto(page: Page, item: string, file = 'saved.png') {
  await openItemImageTab(page, item);
  await page.locator('#image-file').setInputFiles(photo(file));
  await page.locator('#image-cutout [data-cutout-action="original"]').click();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item/image') && r.request().method() === 'POST'),
    page.locator('#image-form button[type=submit]').click(),
  ]);
  await expect(page.locator('#image-msg')).toContainText('updated');
}

// The corner pixel of an <img> (what the till composited).
async function imgCorner(page: Page, sel: string): Promise<number[]> {
  return page.locator(sel).evaluate(async (img: HTMLImageElement) => {
    if (!img.complete) await new Promise((r) => img.addEventListener('load', r, { once: true }));
    const c = document.createElement('canvas');
    c.width = img.naturalWidth;
    c.height = img.naturalHeight;
    const ctx = c.getContext('2d')!;
    ctx.drawImage(img, 0, 0);
    return Array.from(ctx.getImageData(1, 1, 1, 1).data);
  });
}

test('existing item photo: the button offers it, Use this saves the cutout tile', async ({ page }) => {
  const assertClean = watchConsole(page);
  const item = 'Apple Juice 1L';
  const btn = page.locator('#image-current-cutout-btn');
  await uploadItemPhoto(page, item);
  // The saved photo is the flat blue upload.
  await page.reload();
  await page.locator('.catalog-row', { hasText: item }).first().click();
  await page.locator('#item-form-tab-image').click();
  await expect(btn).toBeVisible();
  await expect(btn).toHaveText('Remove background');
  await expect.poll(async () => (await imgCorner(page, '#image-current')).slice(0, 3)).toEqual([40, 120, 200]);
  await fakeReset();

  // Tap → the panel, with the saved photo as "original"; nothing sent yet.
  await btn.click();
  const P = '#image-cutout';
  await expect(page.locator(P)).toBeVisible();
  await expect(btn).toBeHidden();
  expect((await fakeLast()).calls).toBe(0);
  await removeBackground(page, P);
  expect((await fakeLast()).calls).toBe(1);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item/image') && r.request().method() === 'POST'),
    page.locator(`${P} [data-cutout-action="accept"]`).click(),
  ]);
  await expect(page.locator('#image-msg')).toContainText('updated');
  await expect(page.locator(P)).toBeHidden();
  expect((await fakeLast()).calls).toBe(1);
  // The saved photo is now the tile (no longer the flat blue upload), and
  // the button is back for it.
  await expect.poll(async () => (await imgCorner(page, '#image-current')).slice(0, 3)).not.toEqual([40, 120, 200]);
  expect(await page.locator('#image-current').evaluate((i: HTMLImageElement) => [i.naturalWidth, i.naturalHeight])).toEqual([512, 512]);
  await expect(btn).toBeVisible();
  assertClean();
});

test('existing item photo: Use original photo uploads nothing and the button comes back', async ({ page }) => {
  const assertClean = watchConsole(page);
  const item = 'Orange Juice 1L';
  await uploadItemPhoto(page, item);
  await page.reload();
  await page.locator('.catalog-row', { hasText: item }).first().click();
  await page.locator('#item-form-tab-image').click();
  const btn = page.locator('#image-current-cutout-btn');
  await expect(btn).toBeVisible();
  const src = await page.locator('#image-current').getAttribute('src');
  let uploads = 0;
  page.on('request', (r) => { if (r.url().includes('/api/catalog/item/image') && r.method() === 'POST') uploads++; });
  await fakeReset();

  await btn.click();
  await expect(page.locator('#image-cutout')).toBeVisible();
  await page.locator('#image-cutout [data-cutout-action="original"]').click();
  await expect(page.locator('#image-cutout')).toBeHidden();
  await expect(btn).toBeVisible();
  // Nothing pending: no file in the input, no name, the same picture.
  expect(await page.locator('#image-file').evaluate((i: HTMLInputElement) => i.files!.length)).toBe(0);
  await expect(page.locator('#image-file-name')).toHaveText('');
  expect(await page.locator('#image-current').getAttribute('src')).toBe(src);
  // A stray Upload Image tap finds nothing to send.
  await page.locator('#image-form button[type=submit]').click();
  await expect(page.locator('#image-msg')).not.toContainText('updated');
  expect(uploads).toBe(0);
  expect((await fakeLast()).calls).toBe(0);

  // Retake clears the copy of the saved photo before the camera opens.
  await btn.click();
  await expect(page.locator('#image-cutout')).toBeVisible();
  await page.locator('#image-cutout [data-cutout-action="remove"]').click();
  await expect(page.locator('#image-cutout [data-cutout-action="retake"]')).toBeVisible();
  await page.locator('#image-file').evaluate((i: HTMLInputElement) => {
    document.getElementById('image-camera-btn')!.addEventListener('click', () => {
      (window as any).__atRetake = i.files!.length;
    }, { capture: true });
  });
  await page.locator('#image-cutout [data-cutout-action="retake"]').click();
  expect(await page.evaluate(() => (window as any).__atRetake)).toBe(0);
  // Review of ut-docs#4839: with the camera not used, the saved photo's
  // button is back (photo-cutout:cleared repaints it).
  await expect(btn).toBeVisible();
  expect(uploads).toBe(0);
  assertClean();
});

test('existing item photo: a fetch that lands after an item switch is dropped', async ({ page }) => {
  // Review of ut-docs#4839 (finding 1): item A's photo, still loading when
  // the operator opens item B, must never land in B's input or panel.
  const assertClean = watchConsole(page);
  const a = 'Apple Juice 1L';
  const b = 'Orange Juice 1L';
  await uploadItemPhoto(page, a);
  await page.reload();
  await page.locator('.catalog-row', { hasText: a }).first().click();
  await page.locator('#item-form-tab-image').click();
  const btn = page.locator('#image-current-cutout-btn');
  await expect(btn).toBeVisible();
  const src = (await page.locator('#image-current').getAttribute('src'))!;
  const photoPath = new URL(src, page.url()).pathname;
  let release!: () => void;
  const held = new Promise<void>((r) => { release = r; });
  await page.route((u) => u.pathname === photoPath, async (route) => { await held; await route.continue(); });
  let uploads = 0;
  page.on('request', (r) => { if (r.url().includes('/api/catalog/item/image') && r.method() === 'POST') uploads++; });

  const fetched = page.waitForResponse((r) => new URL(r.url()).pathname === photoPath);
  await btn.click();
  await closeItemForm(page);
  await page.locator('.catalog-row', { hasText: b }).first().click();
  await page.locator('#item-form-tab-image').click();
  await expect(page.locator('#image-item-label')).toHaveValue(b);
  release();
  await fetched;
  // Give the page a moment to (wrongly) act on the late answer.
  await page.waitForTimeout(500);
  await expect(page.locator('#image-cutout')).toBeHidden();
  expect(await page.locator('#image-file').evaluate((i: HTMLInputElement) => i.files!.length)).toBe(0);
  expect(uploads).toBe(0);
  await page.unroute((u) => u.pathname === photoPath);
  assertClean();
});

test('existing category photo: the button offers it; Use original changes nothing, Use this is saved by Save', async ({ page }) => {
  const assertClean = watchConsole(page);
  const name = `Cutout Existing Cat ${RUN}`;
  const P = '#category-image-cutout';
  const btn = page.locator('#category-image-current-cutout-btn');
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await page.locator('#category-form input[name="name"]').fill(name);
  await expect(btn).toBeHidden();
  await page.locator('#category-image-file').setInputFiles(photo('soup.png'));
  await page.locator(`${P} [data-cutout-action="original"]`).click();
  await clickThenReload(page, () => page.locator('#category-dialog .record-dialog-save').click());
  const row = page.locator('#categories-table .category-row', { hasText: name }).first();
  const id = (await row.getAttribute('data-id'))!;
  await row.locator('.category-row-open').click();
  await expect(page.locator('#category-image-current')).toBeVisible();
  await expect(btn).toBeVisible();
  await expect(btn).toHaveText('Remove background');
  await fakeReset();

  // Use original photo: the form stays as it was.
  await btn.click();
  await expect(page.locator(P)).toBeVisible();
  await expect(btn).toBeHidden();
  expect((await fakeLast()).calls).toBe(0);
  await page.locator(`${P} [data-cutout-action="original"]`).click();
  await expect(page.locator(P)).toBeHidden();
  await expect(btn).toBeVisible();
  await expect(page.locator('#category-image-name')).toHaveValue('');
  await expect(page.locator('#category-image-file-name')).toHaveText('');
  expect(await page.locator('#category-image-file').evaluate((i: HTMLInputElement) => i.files!.length)).toBe(0);
  expect((await fakeLast()).calls).toBe(0);

  // Remove background → Use this: the tile is in the form; Save stores it.
  await btn.click();
  await removeBackground(page, P);
  await page.locator(`${P} [data-cutout-action="accept"]`).click();
  await expect(page.locator(P)).toBeHidden();
  expect((await fakeLast()).calls).toBe(1);
  await expect(page.locator('#category-image-name')).toHaveValue('photo-cutout.png');
  await expect(btn).toBeHidden(); // a new file is pending
  await clickThenReload(page, () => page.locator('#category-dialog .record-dialog-save').click());
  const saved = page.locator('#categories-table .category-row', { hasText: name }).first();
  await saved.locator('.category-row-open').click();
  await expect(page.locator('#category-image-preview')).toHaveJSProperty('complete', true);
  await expect.poll(async () => (await imgCorner(page, '#category-image-preview')).slice(0, 3)).toEqual([255, 255, 255]);
  await expect(btn).toBeVisible();
  await page.request.post(`/api/categories/${id}/active`, { form: { active: '0' } });
  assertClean();
});

test('a built-in icon is not a photo: no Remove background button on item or category', async ({ page }) => {
  const assertClean = watchConsole(page);
  const item = 'Still Water 1.5L';
  const btn = page.locator('#image-current-cutout-btn');
  await openItemImageTab(page, item);
  const tile = page.locator('#builtin-icon-grid .builtin-icon-tile:not([data-icon="none"])').first();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item/') && r.request().method() === 'POST'),
    tile.click(),
  ]);
  await page.reload();
  await page.locator('.catalog-row', { hasText: item }).first().click();
  await page.locator('#item-form-tab-image').click();
  await expect(page.locator('#image-current')).toBeVisible(); // the icon is shown…
  await expect(btn).toBeHidden(); // …but is not offered for cutout
  // A photo uploaded later brings the button; picking an icon over it removes it.
  await uploadItemPhoto(page, item, 'then-photo.png');
  await page.reload();
  await page.locator('.catalog-row', { hasText: item }).first().click();
  await page.locator('#item-form-tab-image').click();
  await expect(btn).toBeVisible();
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/catalog/item/') && r.request().method() === 'POST'),
    tile.click(),
  ]);
  await expect(btn).toBeHidden();

  // Category: a dialog opened for a category with an icon, then an icon tapped.
  const name = `Cutout Icon Cat ${RUN}`;
  await page.goto('/categories');
  await page.locator('#categories-new').click();
  await page.locator('#category-form input[name="name"]').fill(name);
  await page.locator('#category-icon-grid .builtin-icon-tile:not([data-icon="none"])').first().click();
  await expect(page.locator('#category-image-current-cutout-btn')).toBeHidden();
  await clickThenReload(page, () => page.locator('#category-dialog .record-dialog-save').click());
  const row = page.locator('#categories-table .category-row', { hasText: name }).first();
  const id = (await row.getAttribute('data-id'))!;
  await row.locator('.category-row-open').click();
  await expect(page.locator('#category-image-current-cutout-btn')).toBeHidden();
  await page.request.post(`/api/categories/${id}/active`, { form: { active: '0' } });
  assertClean();
});

// Screenshots of the offer panel for the UX gate (ut-docs#3127): the result
// state on all three surfaces at the 1024×600 floor and at 360 px, in
// English and German (the longest shipped locale; a till only has German
// with the language pack, so run the till with UT_TEST_I18N_OVERLAY_DIR at
// ut-plugin-language-de/locales), plus one RTL (fa). Off unless
// UT_CUTOUT_SHOTS_DIR names where to write them — they are for a person to
// look at, not a CI assertion.
const SHOTS = process.env.UT_CUTOUT_SHOTS_DIR || '';
test('offer panel screenshots (UT_CUTOUT_SHOTS_DIR)', async ({ page }) => {
  test.skip(!SHOTS, 'set UT_CUTOUT_SHOTS_DIR to write the UX screenshots');
  test.setTimeout(240_000);
  fs.mkdirSync(SHOTS, { recursive: true });
  const itemName = `Shot Mug ${RUN}`;
  await createItemWithVariant(page, itemName);
  const sizes = [
    { tag: '1024x600', width: 1024, height: 600 },
    { tag: '360x780', width: 360, height: 780 },
  ];
  const runs: { lang: string; sizes: typeof sizes }[] = [
    { lang: 'en', sizes },
    { lang: 'de', sizes },
    { lang: 'fa', sizes: [sizes[0]] },
  ];
  for (const { lang, sizes: ss } of runs) {
    for (const s of ss) {
      await page.setViewportSize({ width: s.width, height: s.height });
      // Item form.
      await openItemImageTab(page, itemName, lang);
      await page.locator('#image-file').setInputFiles(photo('mug.png'));
      await page.locator('#image-cutout').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `item-offer-${lang}-${s.tag}.png`) });
      await removeBackground(page, '#image-cutout');
      await page.locator('#image-cutout').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `item-${lang}-${s.tag}.png`) });
      // Variant panel.
      await page.locator('#item-form-tab-variants').click();
      await page.locator('.vg-row:not(.vg-new) .vg-img-upload input[type=file]').setInputFiles(photo('mug.png'));
      await page.locator('#variant-image-cutout').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `variant-offer-${lang}-${s.tag}.png`) });
      await removeBackground(page, '#variant-image-cutout');
      await page.locator('#variant-image-cutout').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `variant-${lang}-${s.tag}.png`) });
      // Category form.
      await page.goto(`/categories?lang=${lang}`);
      await page.locator('#categories-new').click();
      await page.locator('#category-image-file').setInputFiles(photo('mug.png'));
      await page.locator('#category-image-cutout').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `category-offer-${lang}-${s.tag}.png`) });
      await removeBackground(page, '#category-image-cutout');
      await page.locator('#category-image-cutout').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `category-${lang}-${s.tag}.png`) });
    }
  }
  await page.goto('/catalog?lang=en');
});
