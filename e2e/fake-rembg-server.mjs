// A stand-in for the shop's own rembg server (`rembg s`), for
// photo-cutout-3127.spec.ts (ut-docs#3127; run-till-cutout.sh starts it and
// points the till at it with UT_AI_IMAGE_ENDPOINT). No dependencies —
// plain node:http + node:zlib.
//
//   POST /api/remove       rembg's endpoint: answers a PNG with alpha — an
//                          opaque red product on a transparent canvas
//                          ("ok"), a fully transparent PNG ("empty": the till
//                          answers 422, no subject), or a 500 ("fail": 502)
//   POST /_mode?m=ok|empty|fail|slow   switch the next answers ("slow":
//                          "ok" after 8 s — a request still in flight)
//   GET  /_last            {"calls", "width", "height", "type", "model"} of
//                          the last photo received — proves the browser
//                          downscaled it to a 1024 px long edge
//   POST /_reset           calls back to 0
//
// Usage: node fake-rembg-server.mjs <port>
import http from 'node:http';
import zlib from 'node:zlib';

const port = Number(process.argv[2]);
if (!port) {
  console.error('usage: fake-rembg-server.mjs <port>');
  process.exit(2);
}

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
function crc(buf) {
  let c = 0xffffffff;
  for (const byte of buf) c = crcTable[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const c = Buffer.alloc(4);
  c.writeUInt32BE(crc(td));
  return Buffer.concat([len, td, c]);
}
// An RGBA PNG, w×h, transparent except an opaque red box (none if !subject).
function cutoutPNG(w, h, subject) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  const stride = w * 4 + 1;
  const raw = Buffer.alloc(stride * h);
  if (subject) {
    for (let y = Math.floor(h / 4); y < Math.floor((3 * h) / 4); y++) {
      for (let x = Math.floor(w / 3); x < Math.floor((2 * w) / 3); x++) {
        const o = y * stride + 1 + x * 4;
        raw[o] = 0xd0; raw[o + 1] = 0x20; raw[o + 2] = 0x20; raw[o + 3] = 0xff;
      }
    }
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', zlib.deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

// Width/height of the PNG or JPEG inside a multipart body (header only).
function imageSize(body) {
  const png = body.indexOf(Buffer.from([0x89, 0x50, 0x4e, 0x47]));
  if (png >= 0) return { type: 'png', width: body.readUInt32BE(png + 16), height: body.readUInt32BE(png + 20) };
  let i = body.indexOf(Buffer.from([0xff, 0xd8, 0xff]));
  if (i < 0) return { type: 'unknown', width: 0, height: 0 };
  i += 2;
  while (i + 9 < body.length) {
    if (body[i] !== 0xff) { i++; continue; }
    const marker = body[i + 1];
    const len = body.readUInt16BE(i + 2);
    if (marker >= 0xc0 && marker <= 0xcf && ![0xc4, 0xc8, 0xcc].includes(marker)) {
      return { type: 'jpeg', height: body.readUInt16BE(i + 5), width: body.readUInt16BE(i + 7) };
    }
    i += 2 + len;
  }
  return { type: 'jpeg', width: 0, height: 0 };
}

let mode = 'ok';
let last = { calls: 0, width: 0, height: 0, type: '', model: '' };
const json = (res, code, body) => {
  res.writeHead(code, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
};

http
  .createServer((req, res) => {
    const url = new URL(req.url, `http://127.0.0.1:${port}`);
    if (req.method === 'POST' && url.pathname === '/api/remove') {
      const parts = [];
      req.on('data', (d) => parts.push(d));
      req.on('end', () => {
        const body = Buffer.concat(parts);
        const size = imageSize(body);
        const m = body.toString('latin1').match(/name="model"\r\n\r\n([^\r]*)/);
        last = { calls: last.calls + 1, ...size, model: m ? m[1] : '' };
        if (mode === 'fail') {
          res.writeHead(500);
          return res.end('boom');
        }
        const answer = () => {
          if (res.destroyed) return;
          res.writeHead(200, { 'Content-Type': 'image/png' });
          res.end(cutoutPNG(96, 64, mode !== 'empty'));
        };
        if (mode === 'slow') return void setTimeout(answer, 8000);
        answer();
      });
      return;
    }
    if (req.method === 'POST' && url.pathname === '/_mode') {
      const m = url.searchParams.get('m');
      if (!['ok', 'empty', 'fail', 'slow'].includes(m)) return json(res, 400, { error: 'm must be ok, empty, fail or slow' });
      mode = m;
      return json(res, 200, { mode });
    }
    if (req.method === 'POST' && url.pathname === '/_reset') {
      last = { calls: 0, width: 0, height: 0, type: '', model: '' };
      mode = 'ok';
      return json(res, 200, last);
    }
    if (req.method === 'GET' && url.pathname === '/_last') return json(res, 200, last);
    res.writeHead(404);
    res.end();
  })
  .listen(port, '127.0.0.1', () => console.log(`fake rembg on http://127.0.0.1:${port}`));
