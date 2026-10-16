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
//                           excluding *_test.go and testdata/ fixtures —
//                           test-only wasip1 guests, build-tagged out of the
//                           server, ut-docs#1351) EXCEPT a file whose ONLY
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
  const pagesRoot = path.join(repoRoot, 'internal', 'pages');
  const pages = walkFiles(pagesRoot)
    .filter((p) => p.endsWith('.go') && !p.endsWith('_test.go'))
    // testdata/ holds test-only fixtures (e.g. the wasip1 guest plugins
    // compiled per-test into .wasm), build-tagged out of the server binary —
    // they can never change a rendered pixel, and hashing them forced a full
    // screenshot regen for adding a TEST fixture (ut-docs#1351; same
    // false-positive class as #620/#659). Must stay in lockstep with
    // scripts/ci/guard-docs-shots.sh's surface_files.
    //
    // Matched against the path RELATIVE to internal/pages, never the
    // absolute one: guard-docs-shots.sh prunes `testdata` while walking the
    // relative "internal/pages" tree, so a checkout living under a
    // directory literally named `testdata` (a CI workspace, a scratch
    // worktree) would make an absolute-path test here silently drop EVERY
    // internal/pages file from the surface while the .sh kept them — the
    // two would disagree on the hash with nothing in the diff to explain
    // it. Relative matching makes the lockstep hold for any checkout path.
    .filter((p) => !path.relative(pagesRoot, p).split(path.sep).includes('testdata'))
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
      'unscreenshotted, ut-docs#620, and excluding testdata/ fixtures, ' +
      'ut-docs#1351); ' +
      'topic hashes = sha256 of the topic markdown (en fallback). ' +
      'Full spec in e2e/tests-docs/lib.js.',
    surface_sha256: surfaceHash(),
    topics,
  };
}

// FONT PIN (ut-docs#4407). The app ships no webfont: app.css's body stack is
// -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue",
// Arial, sans-serif, so glyphs come from whatever system fonts the generating
// machine has. Shots made on a laptop silently re-rendered reports/invoices
// in a different face. playwright.docs.config.ts therefore runs Chromium
// with e2e/fontconfig/fonts.conf (aliases the stack to Inter, on top of the
// system config), and the harness refuses to run unless the platform really
// resolves the body stack to these fonts (packages fonts-inter,
// fonts-dejavu-core and fonts-noto-color-emoji on Debian/Ubuntu).
//
// ut-docs#4425 extends the pin past body text: monospace (codes, chips,
// plugin ids — app.css's `ui-monospace, monospace` and inline
// `font-family:monospace`) is aliased to DejaVu Sans Mono by fonts.conf, and
// emoji (menu/status icons) must come from Noto Color Emoji, which app.css's
// body stack names itself.
const PINNED_FONTS = { latin: 'Inter', arabic: 'DejaVu Sans', mono: 'DejaVu Sans Mono', emoji: 'Noto Color Emoji' };

// Platform font families Chromium really used for hidden probes
// (CSS.getPlatformFontsForNode, via CDP). Returns
// { latin, mono, emoji: string[], arabic: string[] | null } (arabic only
// when asked for).
async function probePlatformFonts(page, { arabic }) {
  // `family` null = inherit the body stack, like real content.
  const probes = [
    { key: 'latin', text: 'Sales report 0123456789 \u20ac', dir: 'ltr', family: null },
    // Both monospace stacks the app uses: the chips' and plain inline/<code>.
    { key: 'mono', text: 'GV-7Q2K-91XZ 0123456789', dir: 'ltr', family: 'ui-monospace, monospace' },
    { key: 'mono2', text: 'ut-plugin-tax-de 0123456789', dir: 'ltr', family: 'monospace' },
    // Emoji the nav and menu really show (bug report, receipts, lock).
    { key: 'emoji', text: '\u{1F41E}\u{1F9FE}\u{1F512}', dir: 'ltr', family: null },
  ];
  if (arabic) {
    probes.push({ key: 'arabic', text: '\u06af\u0632\u0627\u0631\u0634 \u0641\u0631\u0648\u0634 \u062a\u0642\u0631\u064a\u0631 \u0627\u0644\u0645\u0628\u064a\u0639\u0627\u062a', dir: 'rtl', family: null });
  }
  await page.evaluate((ps) => {
    for (const p of ps) {
      const el = document.createElement('p');
      el.id = `ut-font-probe-${p.key}`;
      el.dir = p.dir;
      el.textContent = p.text;
      el.style.cssText =
        'position:absolute;visibility:hidden;inset-inline-start:0;top:0;margin:0;white-space:nowrap';
      if (p.family) el.style.fontFamily = p.family;
      document.body.appendChild(el);
    }
  }, probes);
  // Let a frame lay the probes out first: on a cold fontconfig cache the
  // query can otherwise come back with no fonts at all.
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))));
  const cdp = await page.context().newCDPSession(page);
  const out = { latin: [], mono: [], mono2: [], emoji: [], arabic: arabic ? [] : null };
  try {
    await cdp.send('DOM.enable');
    await cdp.send('CSS.enable');
    const { root } = await cdp.send('DOM.getDocument', { depth: 0 });
    for (const p of probes) {
      const { nodeId } = await cdp.send('DOM.querySelector', {
        nodeId: root.nodeId,
        selector: `#ut-font-probe-${p.key}`,
      });
      const { fonts } = await cdp.send('CSS.getPlatformFontsForNode', { nodeId });
      out[p.key] = [...new Set(fonts.map((f) => f.familyName))];
    }
  } finally {
    await page
      .evaluate((ps) => ps.forEach((p) => document.getElementById(`ut-font-probe-${p.key}`)?.remove()), probes)
      .catch(() => {});
    await cdp.detach().catch(() => {});
  }
  const mono = [...new Set([...out.mono, ...out.mono2])];
  return { latin: out.latin, mono, emoji: out.emoji, arabic: out.arabic };
}

// Returns null when the resolved fonts match the pin, else a failure message.
function fontPinProblem(resolved, pinned) {
  const bad = [];
  const l = resolved.latin;
  if (l.length !== 1 || l[0] !== pinned.latin) {
    bad.push(`Latin text resolved to [${l.join(', ') || 'none'}], expected exactly [${pinned.latin}]`);
  }
  const m = resolved.mono;
  if (m.length !== 1 || m[0] !== pinned.mono) {
    bad.push(`monospace text resolved to [${m.join(', ') || 'none'}], expected exactly [${pinned.mono}]`);
  }
  const e = resolved.emoji;
  if (e.length !== 1 || e[0] !== pinned.emoji) {
    bad.push(`emoji resolved to [${e.join(', ') || 'none'}], expected exactly [${pinned.emoji}]`);
  }
  if (resolved.arabic) {
    const a = resolved.arabic;
    const ok = a.includes(pinned.arabic) && a.every((f) => f === pinned.arabic || f === pinned.latin);
    if (!ok) {
      bad.push(
        `Arabic-script text resolved to [${a.join(', ') || 'none'}], expected ${pinned.arabic} (spaces/punctuation may come from ${pinned.latin})`,
      );
    }
  }
  if (!bad.length) return null;
  return (
    `docs-shots font pin failed (ut-docs#4407, ut-docs#4425): ${bad.join('; ')}.\n` +
    'The app ships no webfont, so shots render in whatever system fonts the generating machine has; ' +
    'committed shots are pinned to the reference environment.\n' +
    'Fix: install fonts-inter, fonts-dejavu-core and fonts-noto-color-emoji (Debian/Ubuntu: ' +
    '`sudo apt-get install -y fonts-inter fonts-dejavu-core fonts-noto-color-emoji`); the harness applies e2e/fontconfig/fonts.conf ' +
    'itself via FONTCONFIG_FILE. The reference environment is Linux; ' +
    'macOS/Windows resolve the app\'s font stack to their own OS font first and cannot match - ' +
    'run docs-shots in a Linux container or the cloud lane instead.'
  );
}

module.exports = { repoRoot, LOCALES, routedTopics, buildManifest, PINNED_FONTS, probePlatformFonts, fontPinProblem };
