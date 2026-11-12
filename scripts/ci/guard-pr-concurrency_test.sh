#!/usr/bin/env bash
# Regression tests for guard-pr-concurrency.sh.
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
failures=0
case_() {
  local name="$1" expect="$2" body="$3" dir rc=0
  dir="$(mktemp -d)"
  printf '%s\n' "$body" >"$dir/w.yml"
  bash "$here/guard-pr-concurrency.sh" "$dir" >/dev/null 2>&1 || rc=$?
  rm -f "$dir/w.yml"; rmdir "$dir"
  if { [ "$expect" = pass ] && [ "$rc" -ne 0 ]; } || { [ "$expect" = fail ] && [ "$rc" -eq 0 ]; }; then
    echo "FAIL: $name (expected $expect, rc=$rc)"; failures=$((failures + 1))
  else
    echo "ok: $name ($expect)"
  fi
}
case_ pr-without-concurrency fail 'on:
  pull_request:
jobs:
  a:
    runs-on: homelab'
case_ pr-with-concurrency pass 'on:
  pull_request:
concurrency:
  group: x
jobs:
  a:
    runs-on: homelab'
case_ push-only-no-concurrency pass 'on:
  push:
jobs:
  a:
    runs-on: homelab'
[ "$failures" -eq 0 ]
