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
#
# ut-docs#632 follow-up: reused-vs-normally-installed diverges on a SECOND
# axis besides version — browser *variant*. A normal headless Playwright
# launch with no PLAYWRIGHT_CHROMIUM_EXECUTABLE override actually runs
# `chromium-headless-shell`, not full Chrome (playwright-core's own
# browsers.json installs both by default). So once the explicit override is
# ruled out, prefer resolving a pre-installed headless-shell binary before
# falling back to full Chrome, so the reused browser matches what a normal
# fallback run would actually use on both axes, not just version.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Tries each candidate path in order; on the first that exists, is
# executable, and smoke-launches, prints "<path>\t<actual_version>" and
# returns 0. Returns 1 if every candidate fails (including "no candidates").
try_candidates() {
  local c actual_version
  for c in "$@"; do
    [ -n "$c" ] || continue
    [ -x "$c" ] || continue
    if actual_version="$(node "$SCRIPT_DIR/smoke-launch.js" "$c" 2>/dev/null)"; then
      printf '%s\t%s\n' "$c" "$actual_version"
      return 0
    fi
  done
  return 1
}

# Globs for the first existing `chromium_headless_shell-*/chrome-linux/
# headless_shell` under a browsers root. Unlike the full-build install,
# there's no stable `chromium` convenience symlink for the headless-shell
# variant, and its directory name is revision-suffixed — so this can't be a
# single fixed path the way the full-chromium candidate is. Never hardcodes
# a specific revision: whatever's actually on disk is what gets tried, and
# "no match" (a future playwright-core dropping/renaming the layout) just
# means this candidate is absent, falling through exactly like a missing
# file does everywhere else in this script.
find_headless_shell() {
  local browsers_root="$1" match
  for match in "$browsers_root"/chromium_headless_shell-*/chrome-linux/headless_shell; do
    if [ -e "$match" ]; then
      echo "$match"
      return 0
    fi
  done
  return 1
}

# Reports the version-mismatch warning for a resolved candidate against
# whichever playwright-core/browsers.json entry actually corresponds to it
# ("chromium" or "chromium-headless-shell") — the two variants don't share a
# revision and aren't guaranteed to share a browserVersion either, so
# comparing a headless-shell candidate against the "chromium" entry (or vice
# versa) would be comparing the wrong pin. Never fatal, same reasoning as
# the rest of this script.
warn_if_version_mismatch() {
  local browser_entry="$1" c="$2" actual_version="$3" expected_version
  expected_version="$(node "$SCRIPT_DIR/expected-chromium-version.js" "$browser_entry" 2>/dev/null || echo "")"
  if [ -n "$expected_version" ] && [ "$actual_version" != "$expected_version" ]; then
    {
      echo "════════════════════════════════════════════════════════════════"
      echo "docs-shots: WARNING — reused Chromium version does not match the"
      echo "@playwright/test pin (ut-docs#622):"
      echo "  reused (in use now): $actual_version  ($c)"
      echo "  pin expects:         $expected_version  ($browser_entry)"
      echo "Screenshots captured this run may render subtly differently from"
      echo "what a fully-pinned browser would produce. Not treated as fatal —"
      echo "failing here would make docs-shots unrunnable again, the exact"
      echo "problem ut-docs#622 exists to fix — but this is not silent drift."
      echo "════════════════════════════════════════════════════════════════"
    } >&2
  fi
}

resolve_and_exit() {
  local browser_entry="$1"; shift
  local resolved c actual_version
  if resolved="$(try_candidates "$@")"; then
    c="${resolved%%$'\t'*}"
    actual_version="${resolved#*$'\t'}"
    warn_if_version_mismatch "$browser_entry" "$c" "$actual_version"
    echo "$c"
    exit 0
  fi
}

# 1. PLAYWRIGHT_CHROMIUM_EXECUTABLE, if set, is an explicit caller override
#    and takes absolute precedence over any variant preference below — it is
#    tried alone, first, exactly as before this ut-docs#632 change.
if [ -n "${PLAYWRIGHT_CHROMIUM_EXECUTABLE:-}" ]; then
  resolve_and_exit "chromium" "$PLAYWRIGHT_CHROMIUM_EXECUTABLE"
fi

# 2. Prefer the headless-shell variant — what a normal fallback headless
#    launch (no override, no channel) actually uses.
hs_candidates=()
[ -n "${PLAYWRIGHT_BROWSERS_PATH:-}" ] && hs_candidates+=("$(find_headless_shell "$PLAYWRIGHT_BROWSERS_PATH" || true)")

# The real fallback every actual caller gets. Overridable ONLY when
# UT_DOCS_SHOTS_TEST=1 is also explicitly set, so resolve-chromium_test.sh
# can exercise (and deliberately suppress) this path deterministically
# without an accidentally-exported UT_DOCS_SHOTS_FALLBACK_CHROMIUM_HEADLESS_SHELL
# ever being able to silently redirect a real `make docs-shots` run.
if [ "${UT_DOCS_SHOTS_TEST:-}" = "1" ] && [ -n "${UT_DOCS_SHOTS_FALLBACK_CHROMIUM_HEADLESS_SHELL:-}" ]; then
  hs_candidates+=("$UT_DOCS_SHOTS_FALLBACK_CHROMIUM_HEADLESS_SHELL")
else
  hs_candidates+=("$(find_headless_shell "/opt/pw-browsers" || true)")
fi
resolve_and_exit "chromium-headless-shell" "${hs_candidates[@]}"

# 3. Fall back to the full Chrome build — today's original behavior,
#    unchanged, for a session with no headless-shell installed.
candidates=()
[ -n "${PLAYWRIGHT_BROWSERS_PATH:-}" ] && candidates+=("$PLAYWRIGHT_BROWSERS_PATH/chromium")

fallback="/opt/pw-browsers/chromium"
if [ "${UT_DOCS_SHOTS_TEST:-}" = "1" ] && [ -n "${UT_DOCS_SHOTS_FALLBACK_CHROMIUM:-}" ]; then
  fallback="${UT_DOCS_SHOTS_FALLBACK_CHROMIUM}"
fi
candidates+=("$fallback")
resolve_and_exit "chromium" "${candidates[@]}"

exit 1
