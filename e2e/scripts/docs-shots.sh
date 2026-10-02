#!/usr/bin/env bash
# Runs `make docs-shots`'s actual work — split out of the Makefile so the
# pre-installed-Chromium fallback (ut-docs#622) has somewhere to live besides
# a wall of inline Makefile shell.
#
# A machine with a pre-installed, smoke-test-launchable Chromium — a cloud
# pipeline session's /opt/pw-browsers, or a developer's Playwright cache
# (~/.cache/ms-playwright, ut-docs#3257) — skips the network install. One
# with none (the e2e GitHub Actions runner) falls through to the original
# `playwright install --with-deps chromium` behavior.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

npm ci

chromium="$(bash scripts/resolve-chromium.sh || true)"
if [ -n "$chromium" ]; then
  echo "docs-shots: reusing pre-installed Chromium at $chromium (skipping playwright install — ut-docs#622)"
  PLAYWRIGHT_CHROMIUM_EXECUTABLE="$chromium" npx playwright test --config=playwright.docs.config.ts
else
  # `--with-deps` runs `sudo apt-get` as a non-root user. With no TTY and no
  # passwordless sudo that blocks forever on a password prompt nobody can
  # answer (ut-docs#3257) — fail fast with the way out instead. CI runners
  # (root, or passwordless sudo) are unaffected. Stdin, not /dev/tty, on
  # purpose: the observed hang was an agent session that HAD a controlling
  # terminal (so sudo prompted on it) with nobody there to type.
  if [ "$(id -u)" -ne 0 ] && [ ! -t 0 ] && ! sudo -n true 2>/dev/null; then
    {
      echo "docs-shots: no pre-installed Chromium found, and"
      echo "  'npx playwright install --with-deps chromium' needs sudo, which would"
      echo "  wait for a password in this non-interactive session."
      echo "  Either install the browser without system deps"
      echo "  (cd e2e && npx playwright install chromium) and re-run, or set"
      echo "  PLAYWRIGHT_CHROMIUM_EXECUTABLE=<path to a Chromium/headless-shell binary>."
    } >&2
    exit 1
  fi
  npx playwright install --with-deps chromium
  npx playwright test --config=playwright.docs.config.ts
fi

node tests-docs/write-manifest.js
