// A stand-in for the GitHub Releases API, for incoming-release-notes-3940
// (ut-docs#3940; run-till-incoming-notes.sh starts it and points the till at
// it with UT_UPDATE_RELEASES_URL). No dependencies — plain node:http.
//
//   GET  /latest              releases/latest: tag OFFERED + a release-notes.json
//                             asset on this server ("ok") or a 404 path ("broken")
//   GET  /release-notes.json  the bundle file
//   POST /_mode?m=ok|broken   switch the asset the next /latest advertises
//   GET  /_info               {"running","versions"} for the spec's assertions
//
// Usage: node fake-release-server.mjs <port> <bundle.json> <running> <offered> <skipped>
import http from 'node:http';
import fs from 'node:fs';

const [port, bundlePath, running, offered, skipped] = process.argv.slice(2);
if (!port || !bundlePath || !running || !offered || !skipped) {
  console.error('usage: fake-release-server.mjs <port> <bundle> <running> <offered> <skipped>');
  process.exit(2);
}
const base = `http://127.0.0.1:${port}`;
let mode = 'ok';

const json = (res, code, body) => {
  res.writeHead(code, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
};

http
  .createServer((req, res) => {
    const url = new URL(req.url, base);
    if (req.method === 'GET' && url.pathname === '/latest') {
      const asset = mode === 'ok' ? '/release-notes.json' : '/missing/release-notes.json';
      return json(res, 200, {
        tag_name: `v${offered}`,
        html_url: `https://github.com/universaltill/universal-till/releases/tag/v${offered}`,
        assets: [{ name: 'release-notes.json', browser_download_url: base + asset }],
      });
    }
    if (req.method === 'GET' && url.pathname === '/release-notes.json') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(fs.readFileSync(bundlePath));
    }
    if (req.method === 'POST' && url.pathname === '/_mode') {
      const m = url.searchParams.get('m');
      if (m !== 'ok' && m !== 'broken') return json(res, 400, { error: 'm must be ok or broken' });
      mode = m;
      return json(res, 200, { mode });
    }
    if (req.method === 'GET' && url.pathname === '/_info') {
      return json(res, 200, { running: `v${running}`, versions: [`v${offered}`, `v${skipped}`] });
    }
    res.writeHead(404);
    res.end();
  })
  .listen(Number(port), '127.0.0.1', () => console.log(`fake release server on ${base} (offering v${offered})`));
