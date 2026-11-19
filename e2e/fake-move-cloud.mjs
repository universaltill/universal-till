// A stand-in for ut-cloud's /v1/stores/sync, for the manual's `claim`
// screenshot (ut-docs#5127 UT-1b; run-till-move.sh starts it and points the
// docs-shots move till at it with UT_MARKETPLACE_ENDPOINT_URL). No
// dependencies — plain node:http.
//
// The till learns of a move waiting for its approval only from a sync answer
// (pending_moves, held in memory by internal/enroll — the same
// enroll.ApplySyncMoves path internal/pages/move_approval_5127_test.go
// seeds). So this answers every sync with ONE fixed pending move, and every
// other request (snapshots, results, reachability probes) with an empty 200.
// Every value is fixed so the screenshot is byte-stable; the expiry is a
// fixed date far enough ahead that the till never treats the move as gone.
//
// Usage: node fake-move-cloud.mjs <port> <source store id> <move id>
import http from 'node:http';

const [port, sourceStore, moveId] = process.argv.slice(2);
if (!port || !sourceStore || !moveId) {
  console.error('usage: fake-move-cloud.mjs <port> <source store id> <move id>');
  process.exit(2);
}

const json = (res, code, body) => {
  res.writeHead(code, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
};

http
  .createServer((req, res) => {
    req.resume();
    req.on('end', () => {
      const path = (req.url ?? '').split('?')[0];
      if (path === '/v1/stores/sync') {
        return json(res, 200, {
          data: {
            directives: [],
            pending_moves: [
              {
                move_id: moveId,
                source_store: sourceStore,
                target_store: 'docs-target-store',
                target_name: 'Second Demo Shop',
                nonce: 'docs-shots-nonce',
                expires_at: '2030-01-03T15:04:05Z',
              },
            ],
          },
        });
      }
      return json(res, 200, { data: {} });
    });
  })
  .listen(Number(port), '127.0.0.1');
