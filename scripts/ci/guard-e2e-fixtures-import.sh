#!/usr/bin/env bash
#
# Guard: every e2e spec (except the auth-project-only specs in
# $EXEMPT_FILES) imports `test`/`expect` from e2e/tests/fixtures.ts, not
# directly from '@playwright/test' (ut-docs#1315).
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
# login.spec.ts is the original deliberate exception (see fixtures.ts's
# own comment): it drives the separate `auth` project against a
# genuinely fresh, never-set-up till, and resetting `/api/pos/reset`
# before the setup wizard has even run is meaningless there.
#
# nav-rail-lock-reachable-1346.spec.ts (ut-docs#1346) is exempt for the
# same underlying reason, not a copy-paste of it: it also runs only on
# the `auth` project (playwright.config.ts's AUTH_ONLY_SPECS) — the
# `#session-chip` fragment it measures never renders on the default
# (UT_AUTH=off) project at all, since nothing ever populates
# `auth.FromContext` there (see that spec's own header comment). The
# fixtures.ts `resetPosOncePerFile` auto-fixture posts through a bare
# `request` context that carries no session cookie, so it would 401
# against the `auth` project instead of the harmless (if pointless) no-op
# it is for login.spec.ts's fresh install — importing `test` from
# fixtures.ts here would break the fixture itself, not just be redundant.
#
# nav-rail-svg-icons-lock-1423.spec.ts (ut-docs#1423) is exempt for exactly
# the #1346 reason above: it measures the same `#session-chip` (the 🔒 lock
# icon's box vs. every other rail icon), so it is auth-project-only too and
# the reset fixture would 401 there the same way.
#
# session-expiry-redirect-2144.spec.ts (ut-docs#2144) is exempt on the same
# objective criterion, not a copy-paste of it: it proves that an htmx-driven
# /api/pos/* request on a REVOKED session redirects to /login instead of
# answering a JSON 401 htmx throws away, so it needs a real session to
# revoke and therefore runs only on the `auth` project
# (playwright.config.ts's AUTH_ONLY_SPECS). fixtures.ts's
# `resetPosOncePerFile` posts /api/pos/reset through a bare, cookie-less
# `request` context, which that project answers 401 — the very response
# this spec exists to characterise — so the reset would silently no-op
# rather than reset anything, exactly as for #1346/#1423 above. Nothing
# leaks either way: the spec never successfully mutates the basket (every
# request it makes after revocation is rejected by the middleware before
# any handler runs), and it sorts last among the auth project's specs.
#
# session-expiry-redirect-admin-2157.spec.ts (ut-docs#2157) is exempt for
# the identical #2144 reason above, not a copy-paste of it: it proves the
# same session-expired-401 redirect on the admin pages' own raw fetch()
# call sites (plugins/settings/catalog/tills/bluetooth-devices/promotions),
# so it also revokes the session and also runs only on the `auth` project.
# fixtures.ts's `resetPosOncePerFile` would 401 against the revoked
# session the same way, and none of this spec's tests touch the sale
# basket at all (they drive admin pages, not the sale screen), so nothing
# leaks either way. Named to sort alphabetically AFTER login.spec.ts
# (ut-docs#2157 review fallout, found live in CI): login.spec.ts's own
# first test requires running against the auth project's server while it
# is still genuinely unconfigured, and any earlier-sorting spec file whose
# ensureOperator() call completes the setup wizard first (as this one's
# does) steals that fresh-install state out from under it.
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

EXEMPT_FILES=('login.spec.ts' 'nav-rail-lock-reachable-1346.spec.ts' 'nav-rail-svg-icons-lock-1423.spec.ts' 'session-expiry-redirect-2144.spec.ts' 'session-expiry-redirect-admin-2157.spec.ts')

is_exempt() {
  local base="$1" f
  for f in "${EXEMPT_FILES[@]}"; do
    [ "$base" = "$f" ] && return 0
  done
  return 1
}

failed=0
checked=0

while IFS= read -r -d '' spec; do
  base="$(basename "$spec")"
  is_exempt "$base" && continue
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

echo "✓ e2e-fixtures-import guard: ${checked} spec(s) checked, all import test/expect from ./fixtures (${EXEMPT_FILES[*]} exempt)"
