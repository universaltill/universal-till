// Shared plumbing for the manual's screenshot harness (`make docs-shots`):
// which topics get a screenshot, and the freshness hashes recorded in
// web/help/img/manifest.json.
//
// CommonJS on purpose — required both by docs-shots.spec.ts (Playwright
// transpiles specs to CJS) and by the plain-node write-manifest.js entry.
//
// HASH ALGORITHM — must stay in lockstep with scripts/ci/guard-docs-shots.sh
// (the CI side recomputes the same values in python3):
//
//   sha256 of a file      = lowercase hex sha256 of its raw bytes.
//   surface fileset       = every regular file under web/ui/ (recursive)
//                           PLUS every regular file under web/public/
//                           (recursive) — the CSS/JS that actually paints
//                           the templates, e.g. app.css's statusbar colors,
//                           which docs-shots.spec.ts's mask relies on
//                           PLUS every *.go under internal/pages/ (recursive,
//                           excluding *_test.go) EXCEPT a file whose ONLY
//                           registered mux routes are all unscreenshotted
//                           (ut-docs#620) — it registers at least one route
//                           but none is any topic's routes[0], the one URL
//                           docs-shots.spec.ts actually visits for that
//                           topic (e.g. import_page.go registers /import,
//                           catalog's routes[1], not any topic's routes[0]).
//                           A file with ZERO route registrations (a shared
//                           helper like init.go's baseMenu or themes.go's
//                           availableThemes) is KEPT, not excluded — it can
//                           still feed a screenshotted page's template data
//                           without registering a route itself (proven in
//                           review: excluding route-less files missed a
//                           real menu-tile change). A file that registers
//                           ANY screenshotted route is hashed as a whole
//                           file, not split by function — see the code
//                           review for why finer granularity was rejected
//                           as unsafe.
//                           web/locales/**/*.json is deliberately excluded —
//                           see the matching note in guard-docs-shots.sh.
//   surface_sha256        = sha256 of the UTF-8 concatenation of one line per
//                           fileset file, sorted lexicographically by its
//                           repo-relative POSIX path (paths are ASCII, so JS
//                           and python sort identically):
//                               "<sha256(file)>  <relpath>\n"
//                           (two spaces — the sha256sum output format).
//   topic hash            = sha256 of web/help/<locale>/<id>.md, falling back
//                           to web/help/en/<id>.md when the locale has no
//                           translation (mirroring manual.FallbackLocale).
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const repoRoot = path.resolve(__dirname, '..', '..');

// The locales the till ships its manual in (web/help/<locale>/).
const LOCALES = ['en', 'fa', 'ar', 'tr'];

function sha256Hex(buf) {
  return crypto.createHash('sha256').update(buf).digest('hex');
}

// routedTopics parses the same tiny front-matter subset internal/manual's
// parseTopic does ("key: value", "key: [a, b]") — keep in sync with it — and
// returns only the topics that declare a routes: field, each with its FIRST
// route (the screenshot target). Route-less topics get no screenshot (a
// follow-up backlog card covers them).
//
// A topic can declare MORE than one route (e.g. one topic covering a
// closely related cluster of pages), but only routes[0] is ever
// screenshotted — internal/manual.go injects at most one generated image
// (web/help/img/<locale>/<id>.png) per topic, keyed by topic id, right
// after the topic's <h1>. There is no per-route screenshot slot. A topic
// whose secondary routes (routes[1:]) show meaningfully different screens
// from routes[0] will simply never get those screens captured for the
// manual — confirmed, accepted gap for "inventory" (routes[0] /inventory;
// /locations never shown) and "multitill" (routes[0] /tills; /registers
// never shown), ut-docs#900. Their prose already documents the /locations
// and /registers screens well enough without a dedicated screenshot,
// consistent with option (b) in ut-docs#900's own acceptance criteria. A
// real fix needs either splitting a secondary route out into its own
// topic (its own id, its own routes: entry — the exclusive-route-
// ownership check in internal/manual.go and guard-help-topics.sh already
// allow this) or extending this harness (plus write-manifest.js and
// scripts/ci/guard-docs-shots.sh, which must stay in lockstep with this
// file's hash algorithm) to support more than one screenshot per topic —
// neither is done here; left as a future backlog card if it's ever worth
// the cost for a specific topic.
function routedTopics() {
  const dir = path.join(repoRoot, 'web', 'help', 'en');
  const out = [];
  for (const name of fs.readdirSync(dir).filter((n) => n.endsWith('.md')).sort()) {
    const text = fs.readFileSync(path.join(dir, name), 'utf8').replace(/\r\n/g, '\n');
    if (!text.startsWith('---\n')) continue;
    const end = text.indexOf('\n---', 4);
    if (end < 0) continue;
    let id = null;
    let routes = null;
    for (const rawLine of text.slice(4, end).split('\n')) {
      const m = /^([A-Za-z_]+):\s*(.*)$/.exec(rawLine.trim());
      if (!m) continue;
      if (m[1] === 'id') id = m[2].trim();
      if (m[1] === 'routes') {
        routes = m[2]
          .replace(/^\[/, '')
          .replace(/\]$/, '')
          .split(',')
          .map((s) => s.trim().replace(/^["']|["']$/g, ''))
          .filter(Boolean);
      }
    }
    if (id && routes && routes.length > 0) out.push({ id, route: routes[0] });
  }
  return out;
}

// Dotfiles are skipped so the surface hash is a property of the REPO, not of
// the machine that generated it. macOS drops `.DS_Store` into any browsed
// directory; it is gitignored, so it exists in a developer's web/public but
// never in CI's checkout — and hashing it made `make docs-shots` on a Mac
// emit a manifest CI would then reject with "the app surface changed", with
// no visible cause in the diff (ut-docs#659). No shipped asset or template
// is a dotfile, so nothing real is excluded.
function walkFiles(dir) {
  const out = [];
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.name.startsWith('.')) continue;
    const p = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...walkFiles(p));
    else if (e.isFile()) out.push(p);
  }
  return out;
}

// ServeMux route registrations, e.g. mux.HandleFunc("GET /reports", ...) or
// the method-prefix-less mux.HandleFunc("/backoffice", ...) form — both are
// in real use in this package. Must stay in lockstep with
// scripts/ci/guard-docs-shots.sh's own copy of this regex.
const ROUTE_CALL_RE = /mux\.(?:HandleFunc|Handle)\(\s*"(?:[A-Z]+\s+)?([^"]*)"/g;

function registeredRoutes(fileContent) {
  const routes = new Set();
  let m;
  const re = new RegExp(ROUTE_CALL_RE);
  while ((m = re.exec(fileContent))) routes.add(m[1]);
  return routes;
}

function surfaceFiles() {
  const ui = walkFiles(path.join(repoRoot, 'web', 'ui'));
  const publicAssets = walkFiles(path.join(repoRoot, 'web', 'public'));
  const screenshotted = new Set(routedTopics().map((t) => t.route));
  const pages = walkFiles(path.join(repoRoot, 'internal', 'pages'))
    .filter((p) => p.endsWith('.go') && !p.endsWith('_test.go'))
    .filter((p) => {
      const routes = registeredRoutes(fs.readFileSync(p, 'utf8'));
      // Exclude ONLY a file that registers routes and none of them is
      // screenshotted. A file with NO route registrations at all is kept
      // — it may be a shared helper feeding a screenshotted page's
      // template data without registering any route itself.
      if (routes.size === 0) return true;
      for (const r of routes) if (screenshotted.has(r)) return true;
      return false;
    });
  return ui
    .concat(publicAssets, pages)
    .map((p) => path.relative(repoRoot, p).split(path.sep).join('/'))
    .sort();
}

function surfaceHash() {
  let lines = '';
  for (const rel of surfaceFiles()) {
    lines += `${sha256Hex(fs.readFileSync(path.join(repoRoot, rel)))}  ${rel}\n`;
  }
  return sha256Hex(Buffer.from(lines, 'utf8'));
}

function topicHash(locale, id) {
  let p = path.join(repoRoot, 'web', 'help', locale, `${id}.md`);
  if (!fs.existsSync(p)) p = path.join(repoRoot, 'web', 'help', 'en', `${id}.md`);
  return sha256Hex(fs.readFileSync(p));
}

function buildManifest() {
  const topics = {};
  for (const t of routedTopics()) {
    topics[t.id] = {};
    for (const locale of LOCALES) topics[t.id][locale] = topicHash(locale, t.id);
  }
  return {
    _comment:
      'Generated by `make docs-shots` (e2e/tests-docs/write-manifest.js); ' +
      'checked by scripts/ci/guard-docs-shots.sh. Do not edit by hand.',
    algorithm:
      'sha256; surface_sha256 = sha256 of concat of "<sha256(file)>  <relpath>\\n" ' +
      'for the sorted fileset (web/ui/** + non-test internal/pages/**.go, ' +
      'excluding a file whose only registered routes are all ' +
      'unscreenshotted, ut-docs#620); ' +
      'topic hashes = sha256 of the topic markdown (en fallback). ' +
      'Full spec in e2e/tests-docs/lib.js.',
    surface_sha256: surfaceHash(),
    topics,
  };
}

module.exports = { repoRoot, LOCALES, routedTopics, buildManifest };
