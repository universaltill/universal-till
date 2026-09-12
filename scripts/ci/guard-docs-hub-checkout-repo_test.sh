#!/usr/bin/env bash
# Tests scripts/ci/guard-docs-hub-checkout-repo.sh against the real
# .github/workflows/ci.yml (passes on current main) and against a
# reverted-to-the-bug copy (fails), so this test would have caught
# ut-docs#2152 before it shipped.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

fail() { echo "❌ $1" >&2; exit 1; }

# 1. Current tree (fixed) must pass.
if ! bash scripts/ci/guard-docs-hub-checkout-repo.sh >/tmp/guard-docs-hub-checkout-repo.ok 2>&1; then
  cat /tmp/guard-docs-hub-checkout-repo.ok >&2
  fail "guard failed against the current (fixed) ci.yml"
fi

# 2. A copy with the bug reintroduced must fail.
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "${TMP_ROOT}"' EXIT
mkdir -p "${TMP_ROOT}/.github/workflows" "${TMP_ROOT}/scripts/ci"
cp scripts/ci/guard-docs-hub-checkout-repo.sh "${TMP_ROOT}/scripts/ci/"
sed 's#repository: universaltill/ut-docs#repository: ${{ github.repository_owner }}/docs#' \
  .github/workflows/ci.yml > "${TMP_ROOT}/.github/workflows/ci.yml"

if (cd "${TMP_ROOT}" && bash scripts/ci/guard-docs-hub-checkout-repo.sh) >/tmp/guard-docs-hub-checkout-repo.bug 2>&1; then
  cat /tmp/guard-docs-hub-checkout-repo.bug >&2
  fail "guard passed against a ci.yml carrying the ut-docs#2152 bug — should have failed"
fi

# 3. Adversarial case (independent review finding): ONLY the docs-hub
# step's own `repository:` line is broken — the sibling adr-taxonomy-guard
# job's own, unrelated `repository: universaltill/ut-docs` line stays
# correct — and the docs-hub step's `path:` line is simultaneously renamed
# to something other than `./.docs-hub`. A guard that isolates the step by
# matching on the `path:` value (rather than the next step boundary) would
# keep scanning past the step's end, find the sibling job's correct line,
# and false-pass. This must still fail.
TMP_ROOT2="$(mktemp -d)"
trap 'rm -rf "${TMP_ROOT}" "${TMP_ROOT2}"' EXIT
mkdir -p "${TMP_ROOT2}/.github/workflows" "${TMP_ROOT2}/scripts/ci"
cp scripts/ci/guard-docs-hub-checkout-repo.sh "${TMP_ROOT2}/scripts/ci/"
python3 - ".github/workflows/ci.yml" "${TMP_ROOT2}/.github/workflows/ci.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
lines = open(src).readlines()
out = []
in_step = False
step_start_seen_body = False
for i, line in enumerate(lines):
    if line.startswith('      - name: Checkout docs repo for docs-hub tests'):
        in_step = True
        step_start_seen_body = False
        out.append(line)
        continue
    if in_step and line.startswith('      - ') and step_start_seen_body:
        in_step = False
    if in_step:
        step_start_seen_body = True
        if 'repository: universaltill/ut-docs' in line:
            line = line.replace('repository: universaltill/ut-docs',
                                 "repository: ${{ github.repository_owner }}/docs")
        elif 'path: ./.docs-hub' in line:
            line = line.replace('path: ./.docs-hub', 'path: ./.docshub-scratch')
    out.append(line)
open(dst, 'w').writelines(out)
PY

if (cd "${TMP_ROOT2}" && bash scripts/ci/guard-docs-hub-checkout-repo.sh) >/tmp/guard-docs-hub-checkout-repo.adversarial 2>&1; then
  cat /tmp/guard-docs-hub-checkout-repo.adversarial >&2
  fail "guard false-passed: docs-hub step still broken (path: renamed too), but a sibling job's unrelated correct 'repository: universaltill/ut-docs' line let it through"
fi

echo "✓ guard-docs-hub-checkout-repo_test: catches the bug (including the path-renamed adversarial case), passes on the fix"
