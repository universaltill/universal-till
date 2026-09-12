#!/usr/bin/env bash
#
# ut-docs#2152: the e2e job's "Checkout docs repo for docs-hub tests" step
# used `repository: ${{ github.repository_owner }}/docs`, which resolves
# to `universaltill/docs` — a repo that does not exist. The real docs repo
# is `universaltill/ut-docs`. Because the step is gated on
# `if: env.DOCS_READ_TOKEN != ''`, a broken target never failed the job
# loudly — it just meant the docs-hub e2e specs silently never ran for
# real. The adr-taxonomy-guard job's own docs checkout (same file) already
# hardcodes `universaltill/ut-docs` correctly; this guard keeps both sites
# pinned to the real repo instead of a derived-but-wrong one.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

CI_FILE=".github/workflows/ci.yml"

# The checkout step whose `path:` is the docs-hub scratch directory must
# target the real docs repo, hardcoded — never a derived expression
# (github.repository_owner interpolates to the *org*, not the docs repo's
# name, so `${{ github.repository_owner }}/docs` silently resolves to a
# repo that doesn't exist).
#
# Isolate the "Checkout docs repo for docs-hub tests" step's own body: from
# its `- name:` line up to (not including) the NEXT step at the same
# indentation, wherever that falls — a structural boundary, not a fixed
# line-count window or a match on some other line inside the step (e.g.
# its `path:` value) that could stop applying after an unrelated future
# edit and silently widen the window to include a later, unrelated step's
# own correct `repository:` line (independent review finding, ut-docs#2152).
step_body="$(awk '
  found && NR > start && /^      - /{ exit }
  /^      - name: Checkout docs repo for docs-hub tests/{ found=1; start=NR }
  found{ print }
' "${CI_FILE}")"

if [[ -z "${step_body}" ]]; then
  echo "❌ docs-hub-checkout guard: no 'Checkout docs repo for docs-hub tests' step found in ${CI_FILE}" >&2
  exit 1
fi

if ! grep -q 'repository: universaltill/ut-docs' <<<"${step_body}"; then
  echo "❌ docs-hub-checkout guard: the docs-hub checkout step in ${CI_FILE} does not target 'repository: universaltill/ut-docs'" >&2
  exit 1
fi

echo "✓ docs-hub-checkout guard: e2e job's docs-hub checkout targets universaltill/ut-docs"
