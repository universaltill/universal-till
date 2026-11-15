#!/usr/bin/env bash
#
# PR/push guard for catimport/VERSION (ut-docs#5882). CI cuts the
# catimport/vX.Y.Z tag from catimport/VERSION when a change merges
# (.github/workflows/catimport-tag.yml); this refuses, before merge, a PR that
# would leave that impossible:
#   - catimport/VERSION missing or not a bare X.Y.Z, or below the latest tag;
#   - catimport Go source / go.mod / go.sum changed (the pin check's set, see
#     catimport-version-lib.sh) without VERSION rising above both the latest
#     tag and the VERSION on the base;
#   - a bump-only change that is not above the latest tag.
#
# Usage: guard-catimport-version.sh [base-rev, default HEAD^1] [repo, default .]
# On pull_request actions/checkout checks out the merge commit, whose first
# parent is the base-branch tip; on push to main it is the previous main. So
# the change under test is exactly `diff HEAD^1 HEAD`. Needs that parent and
# the catimport/v* tags locally; without them it fails closed.
set -euo pipefail

# shellcheck source=scripts/ci/catimport-version-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/catimport-version-lib.sh"

BASE="${1:-HEAD^1}"
REPO="${2:-.}"

fail() {
  echo "❌ catimport version: $*" >&2
  exit 1
}

if ! git -C "${REPO}" rev-parse --verify --quiet "${BASE}^{commit}" >/dev/null; then
  fail "checkout lacks the parent commit '${BASE}' — fetch it (depth 2), e.g. git fetch --no-tags --depth=2 origin \"\${GITHUB_SHA}\""
fi

# The function explains itself on stderr; warnings about odd tags pass through.
L="$(catimport_latest_tag "${REPO}")" ||
  fail "no usable catimport/v* tag — fetch them: git fetch --no-tags origin '+refs/tags/catimport/*:refs/tags/catimport/*'"

rc=0
V="$(catimport_read_version "${REPO}" HEAD 2>&1)" || rc=$?
if [[ "${rc}" -eq 3 ]]; then
  fail "catimport/VERSION is missing — add it (bare X.Y.Z above ${L}); CI tags catimport/vX.Y.Z from it on merge"
elif [[ "${rc}" -ne 0 ]]; then
  fail "${V}"
fi

# On the push to main, catimport-tag.yml (seconds) usually tags HEAD before
# this step runs: that tag was cut from this very change, so it is not a
# "latest tag" the change must rise above.
if [[ "$(git -C "${REPO}" rev-parse -q --verify "refs/tags/catimport/v${V}^{commit}" || true)" == \
  "$(git -C "${REPO}" rev-parse HEAD)" ]]; then
  echo "✅ catimport version: ${V} (catimport/v${V} is already on HEAD)"
  exit 0
fi

if [[ "$(semver_cmp "${V}" "${L}")" == -1 ]]; then
  fail "catimport/VERSION ${V} is below the latest tag catimport/v${L}"
fi

changed="$(catimport_changed "${REPO}" "${BASE}" HEAD)" ||
  fail "could not diff ${BASE}..HEAD"

if [[ -n "${changed}" ]]; then
  # The VERSION already on the base: an untagged bump (a failed tag run) must
  # not be reused by a second source change. Absent or malformed there → L.
  need="${L}"
  if Vb="$(catimport_read_version "${REPO}" "${BASE}" 2>/dev/null)"; then
    if [[ "$(semver_cmp "${Vb}" "${need}")" == 1 ]]; then need="${Vb}"; fi
  fi
  if [[ "$(semver_cmp "${V}" "${need}")" != 1 ]]; then
    {
      echo "❌ catimport version: catimport/ source changed:"
      printf '%s\n' "${changed}" | sed 's/^/  /'
      echo "set catimport/VERSION above ${need} (now ${V}): patch for a fix, minor for a new exported name, major for a break."
      echo "CI tags catimport/vX.Y.Z on merge — never push the tag by hand."
    } >&2
    exit 1
  fi
elif ! git -C "${REPO}" diff --quiet "${BASE}" HEAD -- catimport/VERSION; then
  if [[ "$(semver_cmp "${V}" "${L}")" != 1 ]]; then
    fail "catimport/VERSION changed to ${V}, which is not above the latest tag catimport/v${L} — a bump-only change must raise it"
  fi
fi

echo "✅ catimport version: ${V} (latest tag catimport/v${L})"
