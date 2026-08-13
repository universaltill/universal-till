#!/usr/bin/env bash
# Resolves a usable pre-installed Chromium binary for `make docs-shots`, so a
# cold cloud pipeline session — network-restricted, unable to run
# `playwright install` — can still run the docs-shots harness against
# whatever Chromium build is already on disk (ut-docs#622).
#
# Prints the resolved executable path on stdout and exits 0 if a
# pre-installed browser was found AND smoke-tested launchable (see
# smoke-launch.js). Prints nothing on stdout and exits 1 if none is usable,
# so the caller (docs-shots.sh) falls back to the normal
# `playwright install --with-deps chromium` path unchanged — this script
# never touches that path for a developer or CI machine that doesn't set any
# of the env vars/paths below.
#
# Launchability alone says nothing about whether the reused browser is
# actually the version @playwright/test was written against — a pinned
# Chromium 8 majors newer than the reused one still launches fine and still
# renders, just not necessarily identically. So on a resolved candidate,
# this ALSO compares its real version (from smoke-launch.js, via CDP) against
# what playwright-core/browsers.json says the current @playwright/test pin
# actually expects (via expected-chromium-version.js — read from
# playwright-core's own data file, not hardcoded here, so it tracks the pin
# on its own) and prints a loud stderr warning — never a failure; failing
# here would just reintroduce the exact "can't run at all" problem this
# script exists to avoid — when they differ. That warning is the AC #2 "cannot
# drift apart silently" signal for a cloud session that has no way to fetch
# the expected browser and must proceed regardless.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

candidates=()
[ -n "${PLAYWRIGHT_CHROMIUM_EXECUTABLE:-}" ] && candidates+=("$PLAYWRIGHT_CHROMIUM_EXECUTABLE")
[ -n "${PLAYWRIGHT_BROWSERS_PATH:-}" ] && candidates+=("$PLAYWRIGHT_BROWSERS_PATH/chromium")

# The real fallback every actual caller gets. Overridable ONLY when
# UT_DOCS_SHOTS_TEST=1 is also explicitly set, so resolve-chromium_test.sh
# can exercise the "nothing resolves" path deterministically without an
# accidentally-exported UT_DOCS_SHOTS_FALLBACK_CHROMIUM ever being able to
# silently redirect a real `make docs-shots` run.
fallback="/opt/pw-browsers/chromium"
if [ "${UT_DOCS_SHOTS_TEST:-}" = "1" ] && [ -n "${UT_DOCS_SHOTS_FALLBACK_CHROMIUM:-}" ]; then
  fallback="${UT_DOCS_SHOTS_FALLBACK_CHROMIUM}"
fi
candidates+=("$fallback")

for c in "${candidates[@]}"; do
  [ -x "$c" ] || continue
  if actual_version="$(node "$SCRIPT_DIR/smoke-launch.js" "$c" 2>/dev/null)"; then
    expected_version="$(node "$SCRIPT_DIR/expected-chromium-version.js" 2>/dev/null || echo "")"
    if [ -n "$expected_version" ] && [ "$actual_version" != "$expected_version" ]; then
      {
        echo "════════════════════════════════════════════════════════════════"
        echo "docs-shots: WARNING — reused Chromium version does not match the"
        echo "@playwright/test pin (ut-docs#622):"
        echo "  reused (in use now): $actual_version  ($c)"
        echo "  pin expects:         $expected_version"
        echo "Screenshots captured this run may render subtly differently from"
        echo "what a fully-pinned browser would produce. Not treated as fatal —"
        echo "failing here would make docs-shots unrunnable again, the exact"
        echo "problem ut-docs#622 exists to fix — but this is not silent drift."
        echo "════════════════════════════════════════════════════════════════"
      } >&2
    fi
    echo "$c"
    exit 0
  fi
done

exit 1
