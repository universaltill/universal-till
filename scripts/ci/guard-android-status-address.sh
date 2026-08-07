#!/usr/bin/env bash
#
# ut-docs#412: an external tester found a persistent bar reading
# "Running at 127.0.0.1:41855" on every screen of the Android wrapper, in
# release builds — an internal loopback bind address with no use to a shop
# worker, eating scarce vertical space on a phone screen. The fix has two
# load-bearing parts and either regressing independently reopens the bug:
#   1. MainActivity's on-screen status bar must stay gated behind
#      BuildConfig.DEBUG (real users never see it; it's a developer
#      convenience only).
#   2. TillService's foreground-service notification — a genuine Android
#      requirement, shown in every build — must never interpolate the raw
#      address into its text, even though the on-screen bar's own string
#      resource (status_running) still carries one for the debug case.
#
# Static grep, not an instrumented Android test: this repo has no Android
# test/CI job (release.yml's android-app job only ever runs at release time,
# with a real SDK/NDK this environment doesn't have) — a source-level guard
# that runs in the normal `go`-only CI is the cheap, always-on equivalent.
#
# All checks below run against CODE lines only (comments and blank lines
# stripped) so a reverted fix can't hide behind a comment still mentioning it
# (same lesson as guard-kiosk-launch-flags.sh).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MAIN_ACTIVITY="${ROOT_DIR}/android/app/src/main/java/com/universaltill/pos/MainActivity.kt"
TILL_SERVICE="${ROOT_DIR}/android/app/src/main/java/com/universaltill/pos/TillService.kt"

code_lines() {
  grep -v -E '^[[:space:]]*(//|\*)' "$1" | grep -v -E '^[[:space:]]*$'
}

MAIN_CODE="$(code_lines "$MAIN_ACTIVITY")"
SERVICE_CODE="$(code_lines "$TILL_SERVICE")"

if ! printf '%s\n' "$MAIN_CODE" | grep -q 'statusView\.visibility[[:space:]]*=.*BuildConfig\.DEBUG'; then
  echo "❌ android-status-address guard: ${MAIN_ACTIVITY} no longer gates" >&2
  echo "   statusView's visibility on BuildConfig.DEBUG (as code, not just a" >&2
  echo "   comment) — the debug-only address bar would ship visible to real" >&2
  echo "   users again (ut-docs#412)." >&2
  exit 1
fi

# Polarity, not just co-occurrence (independent review, 2026-08-07): the
# check above alone would still pass an inverted
# "if (!BuildConfig.DEBUG) View.VISIBLE else View.GONE" — visible in
# RELEASE, hidden in debug, the exact bug this card fixes, just flipped.
if ! printf '%s\n' "$MAIN_CODE" | grep -q 'if[[:space:]]*(BuildConfig\.DEBUG)[[:space:]]*View\.VISIBLE[[:space:]]*else[[:space:]]*View\.GONE'; then
  echo "❌ android-status-address guard: ${MAIN_ACTIVITY}'s BuildConfig.DEBUG" >&2
  echo "   check no longer reads exactly 'if (BuildConfig.DEBUG) View.VISIBLE" >&2
  echo "   else View.GONE' — the two tokens can co-occur with inverted" >&2
  echo "   polarity (bar shown in RELEASE, hidden in debug) and still slip" >&2
  echo "   past a looser check (ut-docs#412)." >&2
  exit 1
fi

if printf '%s\n' "$SERVICE_CODE" | grep -q 'updateNotification(getString(R\.string\.status_running'; then
  echo "❌ android-status-address guard: ${TILL_SERVICE} builds the foreground" >&2
  echo "   notification from status_running, which interpolates the raw bind" >&2
  echo "   address — the notification must use notification_running (no" >&2
  echo "   address) instead (ut-docs#412)." >&2
  exit 1
fi

if ! printf '%s\n' "$SERVICE_CODE" | grep -q 'updateNotification(getString(R\.string\.notification_running))'; then
  echo "❌ android-status-address guard: ${TILL_SERVICE} no longer calls" >&2
  echo "   updateNotification with notification_running on a successful" >&2
  echo "   start — the running notification's text source changed; update" >&2
  echo "   this guard if that's intentional, otherwise the address may have" >&2
  echo "   leaked back in (ut-docs#412)." >&2
  exit 1
fi

echo "✓ android-status-address guard: debug-only status bar + address-free notification both in place"
