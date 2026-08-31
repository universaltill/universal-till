#!/usr/bin/env bash
#
# Regression test for guard-e2e-fixtures-import.sh (ut-docs#1315): proves
# the guard rejects a spec importing `test` straight from
# '@playwright/test', passes a spec importing it from './fixtures' (even
# alongside an unrelated named import still pulled from
# '@playwright/test', e.g. a type-only Page import), exempts
# login.spec.ts unconditionally, and passes the real, unmodified repo
# tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-e2e-fixtures-import.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

fresh_dir() {
  local dir="${TMPDIR}/$1"
  mkdir -p "${dir}"
  printf '%s' "${dir}"
}

expect_pass() {
  local label="$1" dir="$2"
  if bash "${GUARD}" "${dir}" >/tmp/guard_e2e_fixtures_test_out.$$ 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "✗ guard incorrectly rejected ${label}:"
    cat /tmp/guard_e2e_fixtures_test_out.$$
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_e2e_fixtures_test_out.$$
}

expect_fail() {
  local label="$1" dir="$2"
  if bash "${GUARD}" "${dir}" >/tmp/guard_e2e_fixtures_test_out.$$ 2>&1; then
    echo "✗ guard incorrectly passed ${label} (should have failed):"
    cat /tmp/guard_e2e_fixtures_test_out.$$
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_e2e_fixtures_test_out.$$
}

# 1. A spec importing test directly from '@playwright/test' -> reject.
d="$(fresh_dir bad_direct)"
cat >"${d}/bad.spec.ts" <<'EOF'
import { test, expect } from '@playwright/test';
test('x', async ({ page }) => {});
EOF
expect_fail "a spec importing test directly from '@playwright/test'" "${d}"

# 1b. Same, but double-quoted and/or wrapped across multiple lines — the
#     repo has no formatter pinning quote style or line-wrapping (checked:
#     no prettier/eslint/editorconfig anywhere), so both are things a
#     contributor or an IDE auto-import could plausibly write, and a
#     naive single-line single-quote-only match would silently let a spec
#     opt back out of the reset (found in review, ut-docs#1315).
d="$(fresh_dir bad_double_quoted)"
cat >"${d}/bad.spec.ts" <<'EOF'
import { test, expect } from "@playwright/test";
test('x', async ({ page }) => {});
EOF
expect_fail "a spec importing test from '@playwright/test' with double quotes" "${d}"

d="$(fresh_dir bad_multiline)"
cat >"${d}/bad.spec.ts" <<'EOF'
import {
  test,
  expect,
} from '@playwright/test';
test('x', async ({ page }) => {});
EOF
expect_fail "a spec importing test from '@playwright/test' across multiple lines" "${d}"

# 2. A spec importing test from './fixtures' -> pass.
d="$(fresh_dir good_fixtures)"
cat >"${d}/good.spec.ts" <<'EOF'
import { test, expect } from './fixtures';
test('x', async ({ page }) => {});
EOF
expect_pass "a spec importing test from './fixtures'" "${d}"

# 3. A spec importing test from './fixtures' plus an unrelated type-only
#    import still from '@playwright/test' -> pass (only `test` itself
#    coming from the wrong place is the violation).
d="$(fresh_dir good_fixtures_plus_type)"
cat >"${d}/good.spec.ts" <<'EOF'
import { test, expect } from './fixtures';
import type { Page } from '@playwright/test';
test('x', async ({ page }: { page: Page }) => {});
EOF
expect_pass "a spec importing test from fixtures plus a type-only Page import" "${d}"

# 4. login.spec.ts is exempt even though it imports test directly —
#    alongside an ordinary compliant spec, matching the real tree where
#    login.spec.ts is never the only file present.
d="$(fresh_dir exempt_login)"
cat >"${d}/login.spec.ts" <<'EOF'
import { test, expect, Page } from '@playwright/test';
test('x', async ({ page }) => {});
EOF
cat >"${d}/other.spec.ts" <<'EOF'
import { test, expect } from './fixtures';
test('x', async ({ page }) => {});
EOF
expect_pass "login.spec.ts importing test directly (deliberately exempt)" "${d}"

# 5. A directory with no *.spec.ts at all -> fail closed.
d="$(fresh_dir empty)"
expect_fail "a directory with no *.spec.ts files (fail closed)" "${d}"

# 6. The real, unmodified repo tree -> pass.
expect_pass "the real e2e/tests tree" "e2e/tests"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-e2e-fixtures-import_test.sh case(s) failed" >&2
  exit 1
fi
echo "✓ all guard-e2e-fixtures-import_test.sh cases passed"
