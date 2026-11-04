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
// Usage: node fake-notices-cloud.mjs <port>
import http from 'node:http';

const [port] = process.argv.slice(2);
if (!port) {
  console.error('usage: fake-notices-cloud.mjs <port>');
  process.exit(2);
}

let items = [];
let rev = 0;
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
