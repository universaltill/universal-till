#!/usr/bin/env bash
#
# Preflight of promote-release.yml (ut-docs#6407). Every check fails closed.
# Holds no secret: the job's GITHUB_TOKEN reads the public repo's release and
# environment protection rules.
#
# Env: VERSION (X.Y.Z), GITHUB_REF, REPO, DRY_RUN (true|false), PROBE_STORE
# (true|false), GITHUB_OUTPUT (optional). Never an argument: no input reaches a
# shell as code.
#
#   1. runs from refs/heads/main (the reviewed code), even for a dry run
#   2. VERSION is X.Y.Z; tag vX.Y.Z exists and is on origin/main (verify-release-tag.sh)
#   3. the release is published (not draft/prerelease) and is the repo's LATEST
#      release: a promote can never submit an older build over a newer one
#   4. only when the run can reach the Store (PROBE_STORE=true or DRY_RUN=false):
#      the `msstore-submit` environment exists with a required reviewer, admin
#      bypass off, and deployments restricted to exactly `main`. A missing
#      environment is auto-created UNPROTECTED on first reference, and the Entra
#      federated credential trusts it by name. Honest limit: this is checked at
#      run start; an admin could still change it before the approval.
# Outputs: tag, published_at, need_store.
set -euo pipefail

REPO="${REPO:?REPO is required}"
VERSION="${VERSION:-}"
DRY_RUN="${DRY_RUN:-true}"
PROBE_STORE="${PROBE_STORE:-false}"
ENV_NAME=msstore-submit
fail() { echo "::error::promote-preflight: $1" >&2; exit 1; }
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

[ "$DRY_RUN" = true ] || [ "$DRY_RUN" = false ] || fail "dry_run must be true or false"
[ "$PROBE_STORE" = true ] || [ "$PROBE_STORE" = false ] || fail "probe_store must be true or false"
[ "${GITHUB_REF:-}" = "refs/heads/main" ] || fail "promote-release must be dispatched from main (got '${GITHUB_REF:-}'): the Store path runs only reviewed code"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "version must be X.Y.Z"
tag="v${VERSION}"

bash "${ROOT_DIR}/scripts/ci/verify-release-tag.sh" "$VERSION" >/dev/null || fail "tag ${tag} is missing or not on main"

rel="$(gh release view "$tag" --repo "$REPO" --json isDraft,isPrerelease,publishedAt)" || fail "cannot read release ${tag}"
published_at="$(jq -r '.publishedAt // empty' <<<"$rel" 2>/dev/null)" || fail "cannot parse release ${tag}"
[ "$(jq -r '.isDraft' <<<"$rel" 2>/dev/null)" = false ] && [ "$(jq -r '.isPrerelease' <<<"$rel" 2>/dev/null)" = false ] && [ -n "$published_at" ] \
  || fail "release ${tag} is not published (draft, prerelease or no publish date)"
latest="$(gh api "repos/${REPO}/releases/latest" | jq -r '.tag_name // empty' 2>/dev/null)" || fail "cannot read the latest release"
[ "$latest" = "$tag" ] || fail "${tag} is not the latest release (latest is '${latest:-unknown}'): a promote never submits an older build over a newer one"

need_store=false
if [ "$PROBE_STORE" = true ] || [ "$DRY_RUN" = false ]; then
  need_store=true
  hint="run the ut-infra msstore apply first (see its README)"
  envj="$(gh api "repos/${REPO}/environments/${ENV_NAME}")" || fail "environment ${ENV_NAME} is missing or unreadable: ${hint}"
  jq -e . >/dev/null 2>&1 <<<"$envj" || fail "environment ${ENV_NAME} answered something that is not JSON: ${hint}"
  jq -e '[.protection_rules[]? | select(.type=="required_reviewers") | (.reviewers // []) | length] | any(. >= 1)' >/dev/null <<<"$envj" \
    || fail "environment ${ENV_NAME} has no required reviewer: ${hint}"
  [ "$(jq -r '.can_admins_bypass' <<<"$envj")" = false ] || fail "environment ${ENV_NAME} lets admins bypass its protection (can_admins_bypass must be false): ${hint}"
  [ "$(jq -r '.deployment_branch_policy.custom_branch_policies // false' <<<"$envj")" = true ] \
    || fail "environment ${ENV_NAME} has no custom deployment branch policy (must be main only): ${hint}"
  pol="$(gh api "repos/${REPO}/environments/${ENV_NAME}/deployment-branch-policies")" || fail "cannot read the deployment branch policies of ${ENV_NAME}: ${hint}"
  [ "$(jq -c '[.branch_policies[]?.name]' <<<"$pol" 2>/dev/null)" = '["main"]' ] \
    || fail "environment ${ENV_NAME} must allow deployments from exactly 'main': ${hint}"
fi

echo "promote-preflight ok: ${tag} published ${published_at}, latest, need_store=${need_store}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "tag=${tag}"
    echo "published_at=${published_at}"
    echo "need_store=${need_store}"
  } >>"$GITHUB_OUTPUT"
fi
