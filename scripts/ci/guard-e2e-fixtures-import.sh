#!/usr/bin/env bash
#
# Guard: every e2e spec (except login.spec.ts) imports `test`/`expect`
# from e2e/tests/fixtures.ts, not directly from '@playwright/test'
# (ut-docs#1315).
#
# fixtures.ts wraps `test` with an auto fixture that resets the shared
# till's basket once per spec FILE, before that file's first test BODY
# runs (but after a `test.beforeAll` in that file, if it has one — see
# fixtures.ts's own comment) — the systemic fix for cross-spec state
# leakage (ut-docs#1310 was one
# instance, hand-fixed in the one file that got bitten; this backstops
# every OTHER file, present and future). That protection only applies to
# a file that actually imports `test` from fixtures.ts — a new spec that
# copies an old file's `import { test, expect } from '@playwright/test';`
# opts back out silently, with no runtime signal (both imports satisfy
# the same TypeScript types), which is exactly how #1310-class bugs
# reappear one file at a time. This guard makes that opt-out a build
# failure instead.
#
# login.spec.ts is the one deliberate exception (see fixtures.ts's own
# comment): it drives the separate `auth` project against a genuinely
# fresh, never-set-up till, and resetting `/api/pos/reset` before the
# setup wizard has even run is meaningless there.
#
# Explicit first argument runs this guard against a fixture directory
# instead of the real tree (see guard-e2e-fixtures-import_test.sh).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TESTS_DIR="${ROOT_DIR}/e2e/tests"

if [ "$#" -ge 1 ]; then TESTS_DIR="$1"; fi

if [ ! -d "$TESTS_DIR" ]; then
  echo "❌ e2e-fixtures-import guard: ${TESTS_DIR} does not exist" >&2
  exit 1
fi

EXEMPT='login.spec.ts'

failed=0
checked=0

while IFS= read -r -d '' spec; do
  base="$(basename "$spec")"
  [ "$base" = "$EXEMPT" ] && continue
  checked=$((checked + 1))

  # Flag any import statement that pulls `test` (as a bound name, not just
  # substring-containing "test") from '@playwright/test' directly. A file
  # is free to import OTHER names (types like Page) from '@playwright/test'
  # alongside importing test/expect from './fixtures' — only `test` itself
  # coming from the wrong place is the violation.
  #
  # A slurped (-0777) perl match, not a line-oriented grep: this repo has
  # no formatter pinning import quote style or line-wrapping (checked —
  # no prettier/eslint/editorconfig anywhere), so both "@playwright/test"
  # (double quotes) and a multi-line
  #   import {
  #     test,
  #     expect,
  #   } from '@playwright/test';
  # are things a contributor or an IDE auto-import could plausibly write,
  # and a single-line single-quote-only grep silently passes both while
  # the spec quietly opts out of the reset — precisely the failure mode
  # this guard exists to prevent (found in review, ut-docs#1315).
  if perl -0777 -ne 'exit(m/import\s*\{[^}]*\btest\b[^}]*\}\s*from\s*[\x27"]\@playwright\/test[\x27"]/ ? 0 : 1)' "$spec"; then
    rel="${spec#"${ROOT_DIR}/"}"
    echo "❌ e2e-fixtures-import guard: ${rel} imports \`test\` directly from" >&2
    echo "   '@playwright/test' instead of './fixtures' — it won't get the" >&2
    echo "   per-file basket reset (ut-docs#1315), and can leak state into" >&2
    echo "   or inherit it from whichever spec runs next to it." >&2
    echo "   Fix: import { test, expect } from './fixtures';" >&2
    failed=1
  fi
done < <(find "$TESTS_DIR" -maxdepth 1 -name '*.spec.ts' -print0)

if [ "$checked" -eq 0 ]; then
  # Fail closed: if this ever finds nothing to check, the spec directory
  # moved or the naming convention drifted, and the guard is no longer
  # guarding.
  echo "❌ e2e-fixtures-import guard: no non-exempt *.spec.ts found under ${TESTS_DIR#"${ROOT_DIR}/"}." >&2
  exit 1
fi

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "✓ e2e-fixtures-import guard: ${checked} spec(s) checked, all import test/expect from ./fixtures (${EXEMPT} exempt)"
