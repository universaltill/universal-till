#!/usr/bin/env bash
#
# iOS TestFlight + Google Play status for one release (ut-docs#6407), the
# `stores` job of promote-release.yml. Reads run status; dispatches a missing or
# failed run ONCE, only when DRY_RUN is false. Never waits on a human approval.
#
#   promote-stores.sh <X.Y.Z> <published_at> <dry_run:true|false>
#
# Env: REPO, PLAY_ENABLED (the repo variable PLAY_PUBLISH_ENABLED), GITHUB_OUTPUT
# (optional; gets one line `stores=<single-line JSON>`), POLL_SECONDS (60),
# IOS_WAIT_MINUTES (30).
#
#   iOS   a run of ios-testflight.yml counts when its headBranch is vX.Y.Z (the
#         release dispatches --ref "$TAG") and it was created at/after the
#         publish. success = ok; queued/in progress = wait; failed or none =
#         real run: re-dispatch once (as release.yml does) and wait; dry run:
#         report only.
#   Play  disabled unless PLAY_ENABLED=true. Else match a play-publish.yml run by
#         its run-name "play-publish X.Y.Z dry_run=false". success = ok; waiting
#         or in progress = reported with its URL (the owner's two play-internal
#         approvals), NEVER blocked on; failed or none = real run: re-dispatch
#         once (internal testing only, from main), report "dispatched".
#
# States: ok, waiting, dispatched, disabled, missing, failed, timeout.
# Exit 1 only for a real run whose iOS state is failed/timeout.
set -euo pipefail

version=${1:?usage: promote-stores.sh <X.Y.Z> <published_at> <dry_run>}
published=${2:?published_at}
dry=${3:?dry_run}
REPO="${REPO:?REPO is required}"
fail() { echo "::error::promote-stores: $1" >&2; exit 1; }
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "version must be X.Y.Z"
[[ "$published" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+Z$ ]] || fail "published_at must be an ISO UTC timestamp"
[ "$dry" = true ] || [ "$dry" = false ] || fail "dry_run must be true or false"
tag="v${version}"
poll="${POLL_SECONDS:-60}"
[[ "$poll" =~ ^[0-9]+$ ]] || fail "POLL_SECONDS must be a number"
wait_min="${IOS_WAIT_MINUTES:-30}"
[[ "$wait_min" =~ ^[0-9]+$ ]] || fail "IOS_WAIT_MINUTES must be a number"
max_polls=$(( wait_min * 60 / (poll > 0 ? poll : 1) ))

# newest <workflow> <jq-filter-on-.> -> compact JSON of the newest matching run or empty
newest() {
  local runs
  runs="$(gh run list --repo "$REPO" --workflow "$1" -L 30 --json "databaseId,headBranch,displayTitle,status,conclusion,createdAt,url")" || return 0
  jq -c --arg pub "$published" "[.[] | select(.createdAt >= \$pub) | select($2)] | sort_by(.createdAt) | last // empty" <<<"$runs" 2>/dev/null || true
}
# classify <run-json> -> ok | waiting | failed | missing
classify() {
  [ -n "$1" ] || { echo missing; return; }
  case "$(jq -r '.status' <<<"$1")" in
    completed) [ "$(jq -r '.conclusion' <<<"$1")" = success ] && echo ok || echo failed;;
    *) echo waiting;;
  esac
}
result() { # result <state> <url> <detail> -> JSON object
  jq -nc --arg s "$1" --arg u "${2:-}" --arg d "${3:-}" '{state:$s,url:$u,detail:$d}'
}

# --- iOS ---
ios_filter=".headBranch == \"${tag}\""
run="$(newest ios-testflight.yml "$ios_filter")"
state="$(classify "$run")"
old_id=""
dispatched=false
ios_bad=false
if [ "$state" = failed ] || [ "$state" = missing ]; then
  if [ "$dry" = true ]; then
    ios="$(result "$state" "$(jq -r '.url // empty' <<<"${run:-null}" 2>/dev/null || true)" "dry run: nothing dispatched")"
  else
    old_id="$(jq -r '.databaseId // empty' <<<"${run:-null}" 2>/dev/null || true)"
    if gh workflow run ios-testflight.yml --repo "$REPO" --ref "$tag" -f version="$version"; then
      dispatched=true
      state=waiting
    else
      ios="$(result failed "" "re-dispatch of ios-testflight.yml failed")"; ios_bad=true
    fi
  fi
fi
if [ -z "${ios:-}" ]; then
  n=0
  while :; do
    if [ "$dispatched" = true ] && [ -n "$old_id" ] && [ "$(jq -r '.databaseId' <<<"${run:-null}" 2>/dev/null)" = "$old_id" ]; then state=waiting; fi
    [ "$state" != waiting ] && break
    n=$((n + 1))
    [ "$n" -le "$max_polls" ] || { state=timeout; break; }
    sleep "$poll"
    run="$(newest ios-testflight.yml "$ios_filter")"
    state="$(classify "$run")"
    # after a re-dispatch the old failed run is not the answer; a missing one is still pending
    if [ "$dispatched" = true ] && { [ "$state" = missing ] || [ "$(jq -r '.databaseId // empty' <<<"${run:-null}" 2>/dev/null)" = "$old_id" ]; }; then state=waiting; fi
  done
  detail=""; [ "$dispatched" = true ] && detail="re-dispatched once"
  ios="$(result "$state" "$(jq -r '.url // empty' <<<"${run:-null}" 2>/dev/null || true)" "$detail")"
  { [ "$state" = failed ] || [ "$state" = timeout ]; } && [ "$dry" = false ] && ios_bad=true
fi

# --- Play ---
if [ "${PLAY_ENABLED:-}" != true ]; then
  play="$(result disabled "" "PLAY_PUBLISH_ENABLED is not true")"
else
  prun="$(newest play-publish.yml ".displayTitle == \"play-publish ${version} dry_run=false\"")"
  pstate="$(classify "$prun")"
  purl="$(jq -r '.url // empty' <<<"${prun:-null}" 2>/dev/null || true)"
  case "$pstate" in
    ok) play="$(result ok "$purl" "")";;
    waiting) play="$(result waiting "$purl" "waiting for the owner's two play-internal approvals")";;
    *)
      if [ "$dry" = true ]; then
        play="$(result "$pstate" "$purl" "dry run: nothing dispatched")"
      elif gh workflow run play-publish.yml --repo "$REPO" --ref main -f version="$version" -f dry_run=false; then
        play="$(result dispatched "$purl" "dispatched once; awaiting the owner's approvals")"
      else
        play="$(result failed "$purl" "re-dispatch of play-publish.yml failed")"
      fi;;
  esac
fi

json="$(jq -nc --argjson i "$ios" --argjson p "$play" '{ios:$i,play:$p}')"
echo "stores: ${json}"
[ -z "${GITHUB_OUTPUT:-}" ] || echo "stores=${json}" >>"$GITHUB_OUTPUT"
[ "$ios_bad" = false ] || exit 1
