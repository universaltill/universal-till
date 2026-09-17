#!/usr/bin/env bash
#
# Regression test for prune-nightly-assets.sh (ut-docs#2360): proves the
# prune loop's own exit status is always 0 after a normal pass — including,
# and especially, the exact case that broke the real nightly workflow: the
# LAST asset iterated is one to KEEP. Also proves it actually deletes what
# it should and leaves KEEP assets alone.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

FAIL_COUNT=0

fail() {
  echo "FAIL: $1" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
}

# Source, don't execute: sets $0 != ${BASH_SOURCE[0]} inside the sourced
# file's own guard, so its `if` at the bottom never runs prune() against a
# real `dist/`/`gh` — only the function definitions land in this shell.
# shellcheck disable=SC1091
source scripts/ci/prune-nightly-assets.sh

# --- fixtures ---------------------------------------------------------

# Real incident shape: the release holds two assets, both of which today's
# build kept (no actual pruning to do) — the LAST one iterated is a keeper.
ASSETS_LAST_IS_KEPT="unitill-linux-amd64.tar.gz
unitill-windows-amd64.zip"
KEEP_LAST_IS_KEPT=(unitill-linux-amd64.tar.gz unitill-windows-amd64.zip)

# A real prune: one stale asset from a previous run, one kept.
ASSETS_MIXED="unitill-linux-amd64-OLD.tar.gz
unitill-linux-amd64.tar.gz"
KEEP_MIXED=(unitill-linux-amd64.tar.gz)

deleted_log=""

list_assets() { printf '%s\n' "${FIXTURE_ASSETS}"; }
delete_asset() { deleted_log="${deleted_log}${1}"$'\n'; }

# --- case 1: the exact regression -- last asset iterated is a keeper ---

FIXTURE_ASSETS="${ASSETS_LAST_IS_KEPT}"
deleted_log=""
if ! prune "${KEEP_LAST_IS_KEPT[@]}"; then
  fail "prune() exited non-zero when every asset (including the last one iterated) is a keeper -- this is the exact ut-docs#2360 regression"
fi
if [ -n "${deleted_log}" ]; then
  fail "prune() deleted something when nothing needed pruning, got: ${deleted_log}"
fi

# --- case 2: a real prune actually deletes the stale one, keeps the rest ---

FIXTURE_ASSETS="${ASSETS_MIXED}"
deleted_log=""
if ! prune "${KEEP_MIXED[@]}"; then
  fail "prune() exited non-zero on a normal mixed keep/delete pass"
fi
if [ "${deleted_log}" != "unitill-linux-amd64-OLD.tar.gz"$'\n' ]; then
  fail "prune() should have deleted exactly unitill-linux-amd64-OLD.tar.gz, got: ${deleted_log:-<nothing>}"
fi

# --- case 3: nothing to keep at all -- every listed asset is deleted ---

FIXTURE_ASSETS="stale-a.zip
stale-b.zip"
deleted_log=""
if ! prune; then
  fail "prune() exited non-zero with an empty KEEP list"
fi
if [ "${deleted_log}" != "stale-a.zip"$'\n'"stale-b.zip"$'\n' ]; then
  fail "prune() with no KEEP args should delete every listed asset, got: ${deleted_log:-<nothing>}"
fi

# --- case 4: a genuinely empty release -- zero assets, zero deletions ---

FIXTURE_ASSETS=""
deleted_log=""
if ! prune "unitill-linux-amd64.tar.gz"; then
  fail "prune() exited non-zero against a release with zero assets"
fi
if [ -n "${deleted_log}" ]; then
  fail "prune() deleted something on a release with zero assets, got: ${deleted_log}"
fi

# --- case 5: list_assets itself failing must propagate, not look like ---
# --- "the release has zero assets" (independent review, ut-docs#2360)  ---
#
# Deliberately a real SEPARATE bash process, not `prune ... || status=$?`
# in-process: `set -e` disables errexit for the ENTIRE execution of a
# command that is the tested part of an `if`/`&&`/`||` -- including every
# statement inside a FUNCTION called that way, transitively (verified
# empirically while writing this test: a function whose only job is
# `x="$(false)"` still returns 0 and keeps running past that line when
# invoked as `f || true`). The real script never calls prune() that way
# (it's a bare statement inside the `if [ "$BASH_SOURCE" = "$0" ]; then`
# block, where errexit is fully active) — so testing the failure path
# in-process, shielded by `||`, would silently test nothing. A child
# `bash -c` process gives `prune` a fresh top-level, unshielded `set -e`
# context that actually matches the real invocation shape.

status=0
bash -c '
  set -euo pipefail
  cd "'"${ROOT_DIR}"'"
  # shellcheck disable=SC1091
  source scripts/ci/prune-nightly-assets.sh
  list_assets() { return 1; }
  prune "unitill-linux-amd64.tar.gz"
' || status=$?
if [ "${status}" -eq 0 ]; then
  fail "prune() must fail (propagate a non-zero exit via set -e) when list_assets() fails, not silently succeed as if there were nothing to prune"
fi

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "prune-nightly-assets_test.sh: ${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ prune-nightly-assets_test.sh: all cases pass"
