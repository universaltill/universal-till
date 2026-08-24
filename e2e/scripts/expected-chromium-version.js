#!/usr/bin/env node
// Prints the Chromium browserVersion playwright-core actually expects for
// the currently-installed @playwright/test — i.e. the version a normal
// `playwright install --with-deps chromium` would fetch. Read straight from
// playwright-core's own browsers.json (the same file `playwright install`
// itself reads), never hardcoded, so this can never drift from the
// installed package version on its own: bump @playwright/test and this
// prints whatever that bump now expects, no edit needed here.
//
// Used by resolve-chromium.sh (ut-docs#622) to report — loudly, not
// silently — when a reused pre-installed Chromium's actual version diverges
// from this.
//
// Takes an optional argv[2] naming which browsers.json entry to read —
// "chromium" (default, full Chrome build) or "chromium-headless-shell"
// (ut-docs#632: the variant a normal *headless* launch with no explicit
// executablePath override actually uses). The two entries pin independent
// revisions and aren't guaranteed to share a browserVersion, so a caller
// resolving a headless-shell candidate must compare it against its own
// entry, not "chromium"'s.
'use strict';
const fs = require('fs');
const path = require('path');

const browserName = process.argv[2] || 'chromium';

// playwright-core's package.json "exports" map doesn't expose
// "playwright-core/browsers.json" as an importable subpath (confirmed:
// require() throws ERR_PACKAGE_PATH_NOT_EXPORTED) even though the file
// ships in the package — so resolve the package directory the normal way
// and read the file straight off disk instead.
const pkgJsonPath = require.resolve('playwright-core/package.json');
const browsersJsonPath = path.join(path.dirname(pkgJsonPath), 'browsers.json');
const data = JSON.parse(fs.readFileSync(browsersJsonPath, 'utf8'));
const entry = data.browsers.find((b) => b.name === browserName);
if (!entry) {
  console.error(`expected-chromium-version: no "${browserName}" entry in ${browsersJsonPath}`);
  process.exit(1);
}
console.log(entry.browserVersion);
