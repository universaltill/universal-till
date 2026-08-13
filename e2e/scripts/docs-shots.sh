#!/usr/bin/env bash
# Runs `make docs-shots`'s actual work — split out of the Makefile so the
# pre-installed-Chromium fallback (ut-docs#622) has somewhere to live besides
# a wall of inline Makefile shell.
#
# Normal machines (local dev, the e2e GitHub Actions runner) are unaffected:
# resolve-chromium.sh finds nothing there and this falls through to the
# original `playwright install --with-deps chromium` behavior. Only a
# session with a pre-installed, smoke-test-launchable Chromium (e.g. a cloud
# pipeline session's /opt/pw-browsers) skips the network install.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

npm ci

chromium="$(bash scripts/resolve-chromium.sh || true)"
if [ -n "$chromium" ]; then
  echo "docs-shots: reusing pre-installed Chromium at $chromium (skipping playwright install — ut-docs#622)"
  PLAYWRIGHT_CHROMIUM_EXECUTABLE="$chromium" npx playwright test --config=playwright.docs.config.ts
else
  npx playwright install --with-deps chromium
  npx playwright test --config=playwright.docs.config.ts
fi

node tests-docs/write-manifest.js
