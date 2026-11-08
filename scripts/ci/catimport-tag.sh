#!/usr/bin/env bash
#
# Cuts the catimport/vX.Y.Z tag from catimport/VERSION (ut-docs#5882,
# ADR-0164 §1; precedent ut-docs#4735 where a lane's tag push was refused).
# Run by .github/workflows/catimport-tag.yml on a push to main touching
# catimport/**, and on workflow_dispatch, in a full-history checkout; tags
# HEAD and pushes to origin. Idempotent: a re-run never errors on its own
# earlier success, and an existing tag is never moved or deleted.
#
# L = highest catimport/vX.Y.Z tag, V = catimport/VERSION at HEAD:
#   VERSION missing/malformed ........................ red
#   V < L ............................................ red
#   V == L, tag already on HEAD ...................... no-op
#   V == L, no catimport source change since the tag . no-op (README-only
#                                                       push, hand-cut tag)
#   V == L, source changed since the tag ............. red (VERSION not bumped)
#   V > L ............................................ annotated tag on HEAD,
#                                                       pushed without force
# Usage: catimport-tag.sh [repo, default .]
set -euo pipefail

# shellcheck source=scripts/ci/catimport-version-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/catimport-version-lib.sh"

REPO="${1:-.}"
ACTOR_NAME="github-actions[bot]"
ACTOR_EMAIL="github-actions[bot]@users.noreply.github.com"

summary() {
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then printf '%s\n' "$*" >>"${GITHUB_STEP_SUMMARY}"; fi
}
die() {
  echo "::error::catimport tag: $*"
  summary "catimport tag failed: $*"
  exit 1
}
ok() {
  echo "catimport tag: $*"
  summary "catimport tag: $*"
  exit 0
}

# Same fail-closed ancestry check as release.yml's require_ancestor.
if [[ "$(git -C "${REPO}" rev-parse --is-shallow-repository)" != "false" ]]; then
  die "checkout is shallow — cannot trust an ancestry check against it (the workflow checkout must be fetch-depth: 0)"
fi
git -C "${REPO}" rev-parse -q --verify origin/main >/dev/null ||
  die "origin/main not found in this checkout — cannot verify HEAD is on main"
rc=0
git -C "${REPO}" merge-base --is-ancestor HEAD origin/main || rc=$?
case "${rc}" in
  0) ;;
  1) die "HEAD is not an ancestor of origin/main — catimport tags are cut from main only" ;;
  *) die "git merge-base could not compare HEAD against origin/main (exit ${rc}) — failing closed" ;;
esac

head_sha="$(git -C "${REPO}" rev-parse HEAD)"

rc=0
V="$(catimport_read_version "${REPO}" HEAD 2>&1)" || rc=$?
if [[ "${rc}" -eq 3 ]]; then
  die "catimport/VERSION is missing at ${head_sha:0:12} — add it (bare X.Y.Z)"
elif [[ "${rc}" -ne 0 ]]; then
  die "${V}"
fi
L="$(catimport_latest_tag "${REPO}")" ||
  die "no catimport/vX.Y.Z tag found — the checkout must fetch tags"

cmp="$(semver_cmp "${V}" "${L}")"
if [[ "${cmp}" == -1 ]]; then
  die "catimport/VERSION ${V} is below the latest tag catimport/v${L}"
fi

if [[ "${cmp}" == 0 ]]; then
  tag_sha="$(git -C "${REPO}" rev-parse "refs/tags/catimport/v${L}^{commit}")"
  if [[ "${tag_sha}" == "${head_sha}" ]]; then
    ok "catimport/v${L} is already on ${head_sha:0:12} — nothing to do"
  fi
  changed="$(catimport_changed "${REPO}" "catimport/v${L}" HEAD)" ||
    die "could not diff catimport/v${L}..HEAD"
  if [[ -z "${changed}" ]]; then
    ok "no catimport source change since catimport/v${L} — nothing to tag"
  fi
  printf '  %s\n' "${changed//$'\n'/$'\n  '}"
  die "catimport/ changed since catimport/v${L} but catimport/VERSION was not bumped — open a PR setting catimport/VERSION above ${L}; the next merge tags it"
fi

TAG="catimport/v${V}"
subject="$(git -C "${REPO}" log -1 --format=%s HEAD)"
git -C "${REPO}" -c "user.name=${ACTOR_NAME}" -c "user.email=${ACTOR_EMAIL}" \
  tag -a "${TAG}" -m "catimport v${V}" -m "${subject} (${head_sha:0:12})" HEAD

push_err=""
if ! push_err="$(git -C "${REPO}" push origin "refs/tags/${TAG}" 2>&1)"; then
  # A concurrent run (or an earlier attempt) may have tagged this very commit.
  remote_sha="$(git -C "${REPO}" ls-remote origin "refs/tags/${TAG}^{}" | cut -f1)" || remote_sha=""
  if [[ "${remote_sha}" == "${head_sha}" ]]; then
    ok "${TAG} is already on origin at ${head_sha:0:12} — nothing to do"
  fi
  die "could not push ${TAG} (origin has it at '${remote_sha:-nothing}', HEAD is ${head_sha:0:12}): ${push_err}"
fi
ok "created ${TAG} on ${head_sha:0:12} (${subject})"
