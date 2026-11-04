// A stand-in for ut-cloud's /v1/stores/sync, for the notices spec
// (notices-5366.spec.ts, ADR-0176; run-till-notices.sh starts it and points
// the e2e till at it with UT_MARKETPLACE_ENDPOINT_URL). No dependencies --
// plain node:http. It speaks the ADR-0176 §4 wire contract only:
//
//   POST /v1/stores/sync   request  {notices: {lang, have, rev, acks[]}}
//                          response {notices: {unchanged|items, rev}} -- only
//                          to a request that carried a notices object, like
//                          the real cloud, plus a paid entitlement block so
//                          the till (ADR-0148) keeps checking in.
//   GET  /v1/stores/checkin -> 404 (an "old" cloud: the till POSTs each tick)
//
// Test control (the spec drives these; the till never calls them):
//   POST /__notices   body: JSON array of ADR-0176 items -> becomes the live
//                     set, rev bumps
//   GET  /__acks      -> {acks: every ack the till has sent, asks: count of
//                     sync requests that carried a notices object}
//
// Usage: node fake-notices-cloud.mjs <port> [docs]
//   "docs" preloads the fixed set the manual's Notices screenshot shows (fixed
//   dates around the docs-shots clock, UT_DOCS_SHOTS_NOW, so the PNG is
//   byte-stable); the e2e spec starts empty and pushes its own.
import http from 'node:http';

const [port, fixture] = process.argv.slice(2);
if (!port) {
  console.error('usage: fake-notices-cloud.mjs <port> [docs]');
  process.exit(2);
}

const DOCS_ITEMS = [
  {
    id: '5366d000-0000-4000-8000-000000000001', rev: 1, severity: 'critical',
    sender: { kind: 'platform', name: 'Universal Till' },
    texts: { 'en-gb': { title: 'Update the till before Friday: security fix', body: 'A security fix is available for this till. Open **Settings**, Updates, and install version 1.42.3. It takes about 2 minutes, and you can keep selling while it downloads.' } },
    link: { kind: 'route', target: '/settings', label: 'Open Settings' },
    starts_at: '2026-01-01T09:00:00Z', expires_at: '2026-01-16T09:00:00Z', created_at: '2026-01-02T14:30:00Z',
  },
  {
    id: '5366d000-0000-4000-8000-000000000002', rev: 1, severity: 'important',
    sender: { kind: 'partner', name: 'Acme Partners' },
    texts: { 'en-gb': { title: 'Your on-site training is booked for Tuesday', body: 'Hi, this is Acme Partners. Your two-hour session starts at 10:00. Please have one cashier and one manager free.' } },
    starts_at: '2026-01-01T09:00:00Z', expires_at: '2026-01-10T09:00:00Z', created_at: '2026-01-02T13:00:00Z',
  },
  {
    id: '5366d000-0000-4000-8000-000000000003', rev: 1, severity: 'info',
    sender: { kind: 'platform', name: 'Universal Till' },
    texts: { 'en-gb': { title: 'New: split a bill by seat', body: 'Version 1.42 lets you split a table bill by seat. Find it under Pay, Split.' } },
    starts_at: '2026-01-01T09:00:00Z', expires_at: '2026-02-01T09:00:00Z', created_at: '2026-01-02T12:00:00Z',
  },
];

let items = fixture === 'docs' ? DOCS_ITEMS : [];
let rev = fixture === 'docs' ? 1 : 0;
const acks = [];
let asks = 0;

const json = (res, code, body) => {
  res.writeHead(code, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
};

http
  .createServer((req, res) => {
    let raw = '';
    req.on('data', (c) => (raw += c));
    req.on('end', () => {
      const path = (req.url ?? '').split('?')[0];
      let body = {};
      try {
        body = raw ? JSON.parse(raw) : {};
      } catch {
        /* an empty or non-JSON body is fine for the endpoints that ignore it */
      }
      if (path === '/__notices' && req.method === 'POST') {
        items = Array.isArray(body) ? body : [];
        rev += 1;
        return json(res, 200, { rev });
      }
      if (path === '/__acks') return json(res, 200, { acks, asks });
      if (path === '/v1/stores/checkin') return json(res, 404, { error: 'old cloud' });
      if (path === '/v1/stores/sync') {
        const data = {
          directives: [],
          entitlement: {
            plan: 'shop',
            subscription_status: 'active',
            expires_at: new Date(Date.now() + 30 * 864e5).toISOString(),
            refreshed_at: new Date().toISOString(),
          },
          server_time: new Date().toISOString(),
        };
        const n = body.notices;
        if (n && typeof n === 'object') {
          asks += 1;
          for (const a of n.acks ?? []) acks.push(a);
          data.notices =
            n.rev === String(rev) && rev > 0 && n.have !== ''
              ? { unchanged: true, rev: String(rev) }
              : { rev: String(rev), more: false, items };
        }
        return json(res, 200, { data });
      }
      return json(res, 200, { data: {} });
    });
  })
  .listen(Number(port), '127.0.0.1');
