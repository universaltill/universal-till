#!/usr/bin/env bash
# Retries a command with exponential backoff. Written for ut-docs#1933:
# ci.yml's `e2e` and `desktop-shell` jobs were both failing `apt-get
# update` / `npx playwright install --with-deps chromium` with
# "Hash Sum mismatch" fetching Google's Chrome apt repo
# (dl.google.com/linux/chrome-stable) -- a well-known, recurring,
# external GitHub-hosted-Ubuntu-runner-image issue (a CDN edge serving a
# Packages.gz that doesn't match its own Release file), not anything in
# this repo. A retry can land on a different mirror/edge, and clearing the
# local apt index cache first (--clear-apt-lists) forces a genuinely fresh
# fetch instead of re-serving the same stale index -- the two standard
# workarounds for this failure mode, combined.
#
# Usage: retry-with-backoff.sh <max_attempts> <initial_delay_seconds> [--clear-apt-lists] -- <command...>
#
# Exits 0 the moment <command...> succeeds. Exits 1 once <max_attempts>
# have all failed. The delay doubles after each failed attempt.
set -euo pipefail

usage() {
  echo "usage: retry-with-backoff.sh <max_attempts> <initial_delay_seconds> [--clear-apt-lists] -- <command...>" >&2
  exit 2
}

[ "$#" -ge 1 ] || usage

max_attempts="$1"; shift
[ "$#" -ge 1 ] || usage
delay="$1"; shift

clear_apt_lists=0
if [ "${1:-}" = "--clear-apt-lists" ]; then
  clear_apt_lists=1
  shift
fi

[ "${1:-}" = "--" ] || usage
shift

[ "$#" -ge 1 ] || usage

# Overridable so the regression test can exercise --clear-apt-lists without
# touching a real system apt cache or needing sudo/root at all. The real
# default is deliberately loud (echoed) rather than a bare `sudo rm -rf` so
# CI logs show exactly what happened when a run needed a retry.
: "${RETRY_WITH_BACKOFF_APT_CLEAR_CMD:=sudo rm -rf /var/lib/apt/lists/*}"

attempt=1
while true; do
  if "$@"; then
    exit 0
  fi
  if [ "${attempt}" -ge "${max_attempts}" ]; then
    echo "retry-with-backoff: '$*' failed after ${attempt} attempt(s), giving up" >&2
    exit 1
  fi
  echo "retry-with-backoff: '$*' failed (attempt ${attempt}/${max_attempts}), retrying in ${delay}s..." >&2
  if [ "${clear_apt_lists}" -eq 1 ]; then
    eval "${RETRY_WITH_BACKOFF_APT_CLEAR_CMD}" || true
  fi
  sleep "${delay}"
  delay=$(( delay * 2 ))
  attempt=$(( attempt + 1 ))
done
