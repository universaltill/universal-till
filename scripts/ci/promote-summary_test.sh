#!/usr/bin/env bash
# Test for promote-summary.sh with a mocked gh (ut-docs#6407).
# shellcheck disable=SC2015,SC2329
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
S="$ROOT_DIR/scripts/ci/promote-summary.sh"
W="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${W:?}"; }
trap cleanup EXIT
mkdir -p "$W/bin"
cat >"$W/bin/gh" <<'G'
#!/usr/bin/env bash
echo "$*" >>"$MOCK_LOG"
case "$1 $2" in
  "run list") printf '%s' "${MOCK_RELEASE_RUNS:-[]}";;
  "issue list") printf '%s' "${MOCK_ISSUES:-[]}";;
  "issue create") [ "${MOCK_CREATE_FAIL:-0}" = 1 ] && exit 1; echo "https://github.com/o/r/issues/77";;
  "issue comment") [ "${MOCK_COMMENT_FAIL:-0}" = 1 ] && exit 1; for a in "$@"; do case "$a" in --body-file) :;; esac; done
     while [ $# -gt 0 ]; do [ "$1" = --body-file ] && cp "$2" "$MOCK_BODY"; shift; done;;
  *) echo "unexpected gh $*" >&2; exit 9;;
esac
G
chmod +x "$W/bin/gh"
export PATH="$W/bin:$PATH" MOCK_LOG="$W/log" MOCK_BODY="$W/body" REPO=o/r VERSION=1.2.3 RUN_URL=https://github.com/o/r/actions/runs/500
SHA64="$(printf 'a%.0s' $(seq 1 64))"
export DRY_RUN=true PROBE_STORE=false PREFLIGHT_RESULT=success MS_RESULT=success MS_READY=true MS_SHA256="$SHA64"
export STORES_JSON='{"ios":{"state":"ok","url":"https://x/1","detail":""},"play":{"state":"disabled","url":"","detail":"PLAY_PUBLISH_ENABLED is not true"}}'
export MOCK_RELEASE_RUNS='[{"databaseId":300,"headBranch":"v1.2.3","url":"https://github.com/o/r/actions/runs/300","createdAt":"2026-11-26T09:00:00Z"},{"databaseId":299,"headBranch":"v1.2.2","url":"https://github.com/o/r/actions/runs/299","createdAt":"2026-11-25T09:00:00Z"}]'
export MOCK_ISSUES='[{"number":41,"title":"Store release log"},{"number":42,"title":"Store release log (old notes)"}]'
fails=0
t() { # t <name> [want-rc]
  : >"$MOCK_LOG"; rm -f "$MOCK_BODY"; : >"$W/sum"; local rc=0
  GITHUB_STEP_SUMMARY="$W/sum" bash "$S" >"$W/out" 2>&1 || rc=$?
  if [ "$rc" = "${2:-0}" ]; then echo "ok   - $1"; else echo "FAIL - $1 (rc $rc)" >&2; cat "$W/out" >&2; fails=$((fails+1)); fi
}
has() { grep -qF -- "$1" "$W/sum" && echo "ok   -   summary has: $1" || { echo "FAIL - summary lacks '$1'" >&2; cat "$W/sum" >&2; fails=$((fails+1)); }; }
hasnt() { ! grep -qF -- "$1" "$W/sum" && echo "ok   -   summary lacks: $1" || { echo "FAIL - summary has '$1'" >&2; fails=$((fails+1)); }; }
t "dry run, verify only"
has "DRY RUN"; hasnt "REAL PROMOTE"; has "1.2.3"; has "actions/runs/500"; has "actions/runs/300"; hasnt "actions/runs/299"
has "$MS_SHA256"; has "verify only, Store not touched"; has "https://x/1"; has "PLAY_PUBLISH_ENABLED is not true"
[ -s "$MOCK_BODY" ] && cmp -s "$MOCK_BODY" "$W/sum" && echo "ok   -   the issue comment is the same table" || { echo "FAIL - comment body differs" >&2; fails=$((fails+1)); }
grep -q -- '--author app/github-actions' "$MOCK_LOG" && echo "ok   -   only bot-authored log issues are trusted" || { echo "FAIL - author filter" >&2; fails=$((fails+1)); }
grep -q 'issue comment 41' "$MOCK_LOG" && echo "ok   -   posted on the standing issue (exact title match, not the similar one)" || { echo "FAIL - issue choice" >&2; cat "$MOCK_LOG" >&2; fails=$((fails+1)); }
! grep -q 'issue create' "$MOCK_LOG" && echo "ok   -   existing issue is not re-created" || { echo "FAIL - created again" >&2; fails=$((fails+1)); }
! grep -q -- '--label' "$MOCK_LOG" && echo "ok   -   never labels (no pipeline label)" || { echo "FAIL - label" >&2; fails=$((fails+1)); }
DRY_RUN=false PROBE_STORE=false t "real promote, submitted"
has "REAL PROMOTE"; hasnt "DRY RUN"; has "submitted"
DRY_RUN=false PROBE_STORE=false COMMIT=false t "real promote, draft only"; has "package set (draft, not committed)"; hasnt "submitted"
DRY_RUN=false MS_RESULT=failure t "real promote, rejected/failed submit"; has "rejected or failed"
DRY_RUN=false MS_RESULT=cancelled t "real promote, cancelled/expired approval"; has "cancelled"
DRY_RUN=true PROBE_STORE=true t "dry run with a Store probe"; has "DRY RUN"; has "probe"
MS_RESULT=failure MS_READY=false MS_SHA256="" t "verify failed"; has "verify: failed"
PREFLIGHT_RESULT=failure MS_RESULT=skipped MS_READY="" MS_SHA256="" STORES_JSON='' t "preflight refused"; has "preflight refused"
# No identifiers or secrets, whatever the environment holds.
MSSTORE_SELLER_ID=72071950 MSSTORE_APP_ID=XP8M2DCXWBVSC3 GH_TOKEN=ghs_secret t "ids and tokens in the environment are never printed"
hasnt "72071950"; hasnt "XP8M2DCXWBVSC3"; hasnt "ghs_secret"
MOCK_ISSUES='[]' t "no standing issue: created once, then commented"
grep -q 'issue create.*--title Store release log' "$MOCK_LOG" && grep -q 'issue comment 77' "$MOCK_LOG" && echo "ok   -   created the standing issue and commented on it" || { echo "FAIL - create flow" >&2; cat "$MOCK_LOG" >&2; fails=$((fails+1)); }
MOCK_COMMENT_FAIL=1 t "a failing comment is a warning, never a failed run"; grep -q '::warning::' "$W/out" && echo "ok   -   warning printed" || { echo "FAIL - no warning" >&2; fails=$((fails+1)); }
has "DRY RUN"
MOCK_ISSUES='[]' MOCK_CREATE_FAIL=1 t "a failing issue creation is a warning, never a failed run"
VERSION='1.2.3;x' t "a malformed version still produces a summary, no gh call with it" 
! grep -q 'x' "$MOCK_LOG" || true
[ "$fails" = 0 ] || exit 1
echo "✓ promote-summary_test: all cases passed"
