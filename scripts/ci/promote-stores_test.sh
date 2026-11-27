#!/usr/bin/env bash
# Test for promote-stores.sh with a mocked gh (ut-docs#6407).
# shellcheck disable=SC2015
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
S="$ROOT_DIR/scripts/ci/promote-stores.sh"
W="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${W:?}"; }
trap cleanup EXIT
mkdir -p "$W/bin"
# gh run list --workflow X.yml answers with the next queued file $W/<wf>.<n> (the last one repeats).
cat >"$W/bin/gh" <<'G'
#!/usr/bin/env bash
echo "$*" >>"$MOCK_LOG"
case "$1 $2" in
  "run list")
    wf=""; while [ $# -gt 0 ]; do case "$1" in --workflow) wf=${2%.yml}; shift 2;; *) shift;; esac; done
    n=$(( $(cat "$MOCK_DIR/$wf.n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$MOCK_DIR/$wf.n"
    f="$MOCK_DIR/$wf.$n"; [ -f "$f" ] || f="$(ls "$MOCK_DIR/$wf".[0-9]* | grep -v '\.n$' | sort -V | tail -1)"
    cat "$f";;
  "workflow run") [ "${MOCK_DISPATCH_FAIL:-0}" = 1 ] && exit 1; echo "dispatched" >>"$MOCK_DIR/dispatches";;
  *) echo "unexpected gh $*" >&2; exit 9;;
esac
G
chmod +x "$W/bin/gh"
export PATH="$W/bin:$PATH" MOCK_LOG="$W/log" MOCK_DIR="$W" REPO=o/r POLL_SECONDS=0 IOS_WAIT_MINUTES=1 MAX_POLLS=3
PUB=2026-11-26T10:00:00Z
ios() { printf '%s' "$1" >"$W/ios-testflight.${2:-1}"; }
play() { printf '%s' "$1" >"$W/play-publish.${2:-1}"; }
irun() { printf '{"databaseId":%s,"headBranch":"%s","status":"%s","conclusion":"%s","createdAt":"%s","url":"https://x/%s"}' "$1" "$2" "$3" "$4" "$5" "$1"; }
prun() { printf '{"databaseId":%s,"displayTitle":"%s","status":"%s","conclusion":"%s","createdAt":"%s","url":"https://x/%s"}' "$1" "$2" "$3" "$4" "$5" "$1"; }
fails=0
reset() { rm -f "$W"/ios-testflight.* "$W"/play-publish.* "$W/dispatches" "$W/gho"; : >"$MOCK_LOG"; ios '[]'; play '[]'; }
run() { # run <want-rc> <name> <dry_run> ; PLAY_ENABLED from env
  local rc=0; : >"$W/gho"
  GITHUB_OUTPUT="$W/gho" bash "$S" 1.2.3 "$PUB" "$3" >"$W/out" 2>&1 || rc=$?
  if [ "$rc" = "$1" ]; then echo "ok   - $2"; else echo "FAIL - $2 (rc $rc)" >&2; cat "$W/out" >&2; fails=$((fails+1)); fi
}
st() { # st <ios|play> <state>
  local got; got="$(sed -n 's/^stores=//p' "$W/gho" | jq -r ".$1.state" 2>/dev/null || true)"
  [ "$got" = "$2" ] && echo "ok   -   $1 state = $2" || { echo "FAIL - $1 state '$got', want $2" >&2; cat "$W/gho" >&2; fails=$((fails+1)); }
}
ndisp() { [ -f "$W/dispatches" ] && wc -l <"$W/dispatches" | tr -d ' ' || echo 0; }
disp() { [ "$(ndisp)" = "$1" ] && echo "ok   -   dispatches = $1" || { echo "FAIL - dispatches $(ndisp), want $1" >&2; fails=$((fails+1)); }; }
export PLAY_ENABLED=false
# --- iOS ---
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"
run 0 "ios: a successful run for the tag after publish" false; st ios ok; disp 0
reset; ios "[$(irun 9 v1.2.3 completed success 2026-11-26T09:00:00Z)]"
run 1 "ios: a success from BEFORE publish does not count (re-dispatch then never appears)" false; st ios timeout; disp 1
reset; ios "[$(irun 11 v1.2.4 completed success 2026-11-26T10:05:00Z)]"
run 0 "ios: dry run, run of another version is missing, nothing dispatched" true; st ios missing; disp 0
reset; ios "[$(irun 11 v1.2.3 completed failure 2026-11-26T10:05:00Z)]"
run 0 "ios: dry run with a failed run reports failed, never dispatches" true; st ios failed; disp 0
reset; ios "[$(irun 11 v1.2.3 completed failure 2026-11-26T10:05:00Z)]"; ios "[$(irun 12 v1.2.3 in_progress "" 2026-11-26T10:30:00Z),$(irun 11 v1.2.3 completed failure 2026-11-26T10:05:00Z)]" 2; ios "[$(irun 12 v1.2.3 completed success 2026-11-26T10:30:00Z)]" 3
run 0 "ios: real run, failed -> re-dispatch once -> waits -> success" false; st ios ok; disp 1
reset; ios '[]'
run 1 "ios: real run, none found -> re-dispatches once, new run never shows -> timeout" false; st ios timeout; disp 1
reset; ios "[$(irun 11 v1.2.3 in_progress "" 2026-11-26T10:05:00Z)]"; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]" 2
run 0 "ios: in progress then success waits without dispatching" false; st ios ok; disp 0
reset; ios "[$(irun 11 v1.2.3 in_progress "" 2026-11-26T10:05:00Z)]"
run 1 "ios: stuck in progress past the wait is a timeout, no dispatch" false; st ios timeout; disp 0
reset; ios "[$(irun 11 v1.2.3 completed failure 2026-11-26T10:05:00Z)]"; MOCK_DISPATCH_FAIL=1 run 1 "ios: a failed re-dispatch is reported, not retried" false; st ios failed; disp 0
# --- Play ---
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"; PLAY_ENABLED=false run 0 "play: disabled when PLAY_PUBLISH_ENABLED is not true" false; st play disabled
! grep -q 'play-publish' "$MOCK_LOG" && echo "ok   -   disabled play is not even queried" || { echo "FAIL - play queried while disabled" >&2; fails=$((fails+1)); }
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"; PLAY_ENABLED=true; play "[$(prun 21 'play-publish 1.2.3 dry_run=false' completed success 2026-11-26T10:06:00Z)]"
run 0 "play: enabled and a successful run" false; st play ok; disp 0
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"; play "[$(prun 21 'play-publish 1.2.3 dry_run=false' waiting "" 2026-11-26T10:06:00Z)]"
run 0 "play: waiting for approval is reported, never waited on, never dispatched" false; st play waiting; disp 0
grep -q 'https://x/21' "$W/gho" && echo "ok   -   waiting play carries the run URL" || { echo "FAIL - play url" >&2; fails=$((fails+1)); }
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"
run 0 "play: enabled but no run -> dispatched once (dry_run=false, from main)" false; st play dispatched; disp 1
grep -q 'workflow run play-publish.yml.*--ref main.*-f version=1.2.3.*-f dry_run=false' "$MOCK_LOG" && echo "ok   -   dispatch is internal, main, dry_run=false" || { echo "FAIL - dispatch args" >&2; cat "$MOCK_LOG" >&2; fails=$((fails+1)); }
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"; play "[$(prun 21 'play-publish 1.2.3 dry_run=false' completed failure 2026-11-26T10:06:00Z)]"
run 0 "play: failed run -> re-dispatched once" false; st play dispatched; disp 1
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"; play "[$(prun 21 'play-publish 1.2.30 dry_run=false' completed success 2026-11-26T10:06:00Z),$(prun 22 'play-publish 1.2.3 dry_run=true' completed success 2026-11-26T10:06:00Z)]"
run 0 "play: 1.2.30 and a dry_run=true run do not match 1.2.3 (dry_run=false)" false; st play dispatched
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"
run 0 "play: DRY run reports missing and never dispatches" true; st play missing; disp 0
reset; ios "[$(irun 11 v1.2.3 completed success 2026-11-26T10:05:00Z)]"; play "[$(prun 21 'play-publish 1.2.3 dry_run=false' completed failure 2026-11-26T10:06:00Z)]"
run 0 "play: DRY run with a failed run reports failed" true; st play failed; disp 0
# --- Arguments ---
reset; rc=0; bash "$S" 1.2.3 "$PUB" maybe >/dev/null 2>&1 || rc=$?; [ "$rc" != 0 ] && echo "ok   - refuses dry_run that is not true/false" || { echo "FAIL - dry_run arg" >&2; fails=$((fails+1)); }
rc=0; bash "$S" '1.2.3;x' "$PUB" true >/dev/null 2>&1 || rc=$?; [ "$rc" != 0 ] && echo "ok   - refuses a malformed version" || { echo "FAIL - version arg" >&2; fails=$((fails+1)); }
[ "$fails" = 0 ] || exit 1
echo "✓ promote-stores_test: all cases passed"
