#!/usr/bin/env node
// Writes web/help/img/manifest.json — the freshness manifest the CI guard
// (scripts/ci/guard-docs-shots.sh) diffs against the working tree. Run by
// `make docs-shots` AFTER the Playwright screenshot run succeeds, so the
// manifest can never describe screenshots that were not actually captured.
const fs = require('fs');
const path = require('path');
const { repoRoot, buildManifest } = require('./lib');

const out = path.join(repoRoot, 'web', 'help', 'img', 'manifest.json');
fs.mkdirSync(path.dirname(out), { recursive: true });
const manifest = buildManifest();
fs.writeFileSync(out, `${JSON.stringify(manifest, null, 2)}\n`);
console.log(
  `wrote ${path.relative(repoRoot, out)}: ${Object.keys(manifest.topics).length} topics, surface ${manifest.surface_sha256.slice(0, 12)}…`,
);
