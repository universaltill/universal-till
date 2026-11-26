#!/usr/bin/env bash
# Test for promote-preflight.sh with a mocked gh and a stubbed verify-release-tag.sh
# in a throwaway tree (ut-docs#6407).
# shellcheck disable=SC2015
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
W="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${W:?}"; }
trap cleanup EXIT
T="$W/tree"; mkdir -p "$T/scripts/ci" "$W/bin"
cp "$ROOT_DIR/scripts/ci/promote-preflight.sh" "$T/scripts/ci/"
cat >"$T/scripts/ci/verify-release-tag.sh" <<'S'
#!/usr/bin/env bash
[ "${MOCK_TAG_OK:-1}" = 1 ] && echo deadbeef || { echo "tag not on main" >&2; exit 1; }
S
cat >"$W/bin/gh" <<'G'
#!/usr/bin/env bash
echo "$*" >>"$MOCK_LOG"
case "$*" in
  "release view v1.2.3 "*) [ -n "${MOCK_RELEASE-x}" ] && printf '%s' "$MOCK_RELEASE" || exit 1;;
  "api repos/o/r/releases/latest") printf '{"tag_name":"%s"}' "$MOCK_LATEST";;
  "api repos/o/r/environments/msstore-submit/deployment-branch-policies") [ -n "$MOCK_POLICIES" ] && printf '%s' "$MOCK_POLICIES" || exit 1;;
  "api repos/o/r/environments/msstore-submit") [ -n "$MOCK_ENV" ] && printf '%s' "$MOCK_ENV" || exit 1;;
  *) echo "unexpected gh $*" >&2; exit 9;;
esac
G
chmod +x "$W/bin/gh" "$T/scripts/ci/verify-release-tag.sh"
GOODENV='{"can_admins_bypass":false,"protection_rules":[{"type":"required_reviewers","prevent_self_review":false,"reviewers":[{"type":"User"}]},{"type":"branch_policy"}],"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}'
export PATH="$W/bin:$PATH" MOCK_LOG="$W/log" REPO=o/r GITHUB_REF=refs/heads/main VERSION=1.2.3 DRY_RUN=true PROBE_STORE=false
export MOCK_RELEASE='{"isDraft":false,"isPrerelease":false,"publishedAt":"2026-11-26T10:00:00Z"}' MOCK_LATEST=v1.2.3 MOCK_TAG_OK=1
export MOCK_ENV="$GOODENV" MOCK_POLICIES='{"total_count":1,"branch_policies":[{"name":"main","type":"branch"}]}'
S="$T/scripts/ci/promote-preflight.sh"
fails=0
t() { # t <want-rc> <name>
  : >"$MOCK_LOG"; : >"$W/gho"; local rc=0
  GITHUB_OUTPUT="$W/gho" bash "$S" >"$W/out" 2>&1 || rc=$?
  if [ "$rc" = "$1" ]; then echo "ok   - $2"; else echo "FAIL - $2 (rc $rc)" >&2; cat "$W/out" >&2; fails=$((fails+1)); fi
}
r() { grep -q -- "$1" "$W/out" && echo "ok   - refusal says: $1" || { echo "FAIL - refusal text '$1'" >&2; cat "$W/out" >&2; fails=$((fails+1)); }; }
t 0 "a dry run on main for the published latest release passes"
grep -qx 'tag=v1.2.3' "$W/gho" && grep -qx 'published_at=2026-11-26T10:00:00Z' "$W/gho" && grep -qx 'need_store=false' "$W/gho" && echo "ok   - outputs tag, published_at, need_store=false" || { echo "FAIL - outputs" >&2; cat "$W/gho" >&2; fails=$((fails+1)); }
! grep -q 'environments' "$MOCK_LOG" && echo "ok   - dry run without probe never reads the environment" || { echo "FAIL - env read on pure dry run" >&2; fails=$((fails+1)); }
GITHUB_REF=refs/heads/feat t 1 "refuses a non-main ref (even for dry run)"; r 'main'
GITHUB_REF=refs/tags/v1.2.3 t 1 "refuses a tag ref"
VERSION='1.2.3; rm -rf x' t 1 "refuses a malformed version"; r 'X.Y.Z'
VERSION=v1.2.3 t 1 "refuses a v-prefixed version (input is X.Y.Z)"
MOCK_TAG_OK=0 t 1 "refuses a tag that is not on main"; r 'tag'
MOCK_RELEASE='{"isDraft":true,"isPrerelease":false,"publishedAt":null}' t 1 "refuses a draft release"; r 'published'
MOCK_RELEASE='{"isDraft":false,"isPrerelease":true,"publishedAt":"2026-11-26T10:00:00Z"}' t 1 "refuses a prerelease"
MOCK_RELEASE='' t 1 "refuses an unreadable release"
MOCK_RELEASE='{"isDraft":false,"isPrerelease":false,"publishedAt":""}' t 1 "refuses a release without publishedAt"
MOCK_LATEST=v1.2.4 t 1 "refuses a release that is not the latest"; r 'latest'
DRY_RUN=maybe t 1 "refuses DRY_RUN that is not true/false"
PROBE_STORE=maybe t 1 "refuses PROBE_STORE that is not true/false"
# Store-reaching runs need the protected environment.
PROBE_STORE=true t 0 "a probe run with a protected environment passes"
grep -qx 'need_store=true' "$W/gho" && echo "ok   - need_store=true for a probe" || { echo "FAIL - need_store" >&2; fails=$((fails+1)); }
DRY_RUN=false t 0 "a real run with a protected environment passes"
grep -qx 'need_store=true' "$W/gho" && echo "ok   - need_store=true for a real run" || { echo "FAIL - need_store real" >&2; fails=$((fails+1)); }
for mode in probe real; do
  if [ $mode = probe ]; then export PROBE_STORE=true DRY_RUN=true; else export PROBE_STORE=false DRY_RUN=false; fi
  MOCK_ENV='' t 1 "[$mode] environment missing/unreadable is refused"; r 'ut-infra'
  MOCK_ENV='{"can_admins_bypass":false,"protection_rules":[],"deployment_branch_policy":{"custom_branch_policies":true}}' t 1 "[$mode] environment without required reviewers is refused"; r 'reviewer'
  MOCK_ENV='{"can_admins_bypass":false,"protection_rules":[{"type":"required_reviewers","reviewers":[]}],"deployment_branch_policy":{"custom_branch_policies":true}}' t 1 "[$mode] zero reviewers is refused"
  MOCK_ENV="${GOODENV/\"can_admins_bypass\":false/\"can_admins_bypass\":true}" t 1 "[$mode] admin bypass true is refused"; r 'bypass'
  MOCK_ENV="${GOODENV/\"custom_branch_policies\":true/\"custom_branch_policies\":false}" t 1 "[$mode] no custom branch policy is refused"
  MOCK_POLICIES='' t 1 "[$mode] unreadable branch policies are refused"
  MOCK_POLICIES='{"total_count":2,"branch_policies":[{"name":"main","type":"branch"},{"name":"dev","type":"branch"}]}' t 1 "[$mode] an extra branch policy is refused"; r 'main'
  MOCK_POLICIES='{"total_count":1,"branch_policies":[{"name":"dev","type":"branch"}]}' t 1 "[$mode] branch policy other than main is refused"
  MOCK_ENV='not json' t 1 "[$mode] unparseable environment is refused"
done
[ "$fails" = 0 ] || exit 1
echo "✓ promote-preflight_test: all cases passed"
