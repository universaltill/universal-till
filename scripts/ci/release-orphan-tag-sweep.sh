#!/usr/bin/env bash
# ut-docs#5211: deletes a release tag (and its draft release) that
# cleanup-orphaned-tag kept for a runner retry which never came.
#
# cleanup-orphaned-tag (release.yml, workflow_dispatch runs only) keeps the
# tag and draft when a pre-publish job was starved of a runner on attempt 1,
# expecting release-runner-retry.yml to re-run it. If that retry never
# happens (workflow_run not delivered, retry job starved, re-run POST failed,
# retry refused) the kept tag would burn that version forever. Called hourly
# by .github/workflows/release-orphan-tag-sweep.yml.
#
# A run is swept only when ALL hold (each skip logs one line):
#   - it is the NEWEST Release run (by created_at, any event). A newer run
#     means the version moved on, and a re-dispatch may have recreated the
#     same tag name with a draft still in flight, which must not be deleted;
#   - event workflow_dispatch (a human-pushed tag is never touched), status
#     completed (a re-run in progress is not), conclusion not success;
#   - created within LOOKBACK_HOURS (default 72) and last updated at least
#     MIN_AGE_MINUTES (default 120) ago: the retry has had its chance;
#   - its latest attempt's publish-release job is not success;
#   - its latest attempt's cleanup-orphaned-tag carries the keep warning
#     (release_tag from release-runner-retry.sh is the single reader of that
#     phrase; a delete warning or no cleanup job yields no tag);
#   - the tag still exists and passes the identity check below, and the
#     releases lookups succeed with NO published release for it.
# Then each draft release is deleted, then the tag. A delete failure is an
# ::error::, the script carries on and exits 1 at the end. A list_runs failure
# exits 1 with nothing deleted.
#
# Tag identity: the tag must be the annotated tag this run's `prepare` made
# (object sha == the run's head_sha, tagger github-actions[bot], message
# "Release <tag>"); a lightweight or foreign tag is left alone. A published
# release is checked twice (the releases listing, which is paginated, and
# releases/tags/<tag>, published only; HTTP 404 is the expected answer, any
# other error skips).
#
# An orphan left by a run that is no longer the newest Release run is NOT
# swept (the version is already skipped by then): delete it by hand if wanted.
#
# Inputs (env): REPO, GH_TOKEN (read by `gh`), optional LOOKBACK_HOURS (72)
# and MIN_AGE_MINUTES (120). Dates are parsed with GNU `date -d`.
#
# Every network call is its own function so release-orphan-tag-sweep_test.sh
# can redefine them and run main() with no network. Each `gh` result is
# captured with `x="$(...)"`, never read off a process substitution, so a
# failing call fails the script (a process substitution hides its status).
set -euo pipefail

SWEEP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Sourcing leaves the retry script's main() unrun (it runs only when executed).
# shellcheck source=scripts/ci/release-runner-retry.sh
source "${SWEEP_DIR}/release-runner-retry.sh"

list_runs() {
  gh api "repos/${REPO}/actions/workflows/release.yml/runs?per_page=30"
}

# jobs_of_run <run id>: the latest attempt's jobs.
jobs_of_run() {
  gh api "repos/${REPO}/actions/runs/$1/jobs?filter=latest&per_page=100"
}

# tag_info <tag>: prints "sha<TAB>tagger<TAB>message first line" of the
# annotated tag object. Returns 2 when the ref is gone (HTTP 404), 3 for a
# lightweight tag (not one prepare creates), 1 for any other failure.
tag_info() {
  local ref out type sha obj
  if ! ref="$(gh api "repos/${REPO}/git/ref/tags/$1" 2>&1)"; then
    case "${ref}" in
      *"HTTP 404"*) return 2 ;;
    esac
    return 1
  fi
  type="$(jq -r '.object.type' <<<"${ref}")" || return 1
  sha="$(jq -r '.object.sha' <<<"${ref}")" || return 1
  [ "${type}" = tag ] || return 3
  obj="$(gh api "repos/${REPO}/git/tags/${sha}")" || return 1
  out="$(jq -r '[.sha, (.tagger.name // ""), ((.message // "") | split("\n")[0])] | @tsv' <<<"${obj}")" || return 1
  printf '%s\n' "${out}"
}

# published_release <tag>: GET releases/tags/<tag> returns published releases
# only. 0 = one exists, 1 = HTTP 404 (the expected case), 2 = any other error.
published_release() {
  local out
  if out="$(gh api "repos/${REPO}/releases/tags/$1" 2>&1)"; then
    return 0
  fi
  case "${out}" in
    *"HTTP 404"*) return 1 ;;
  esac
  return 2
}

# releases_for_tag <tag>: "id<TAB>draft(true|false)" per release with that
# tag_name. An API error is a failure (the caller then deletes nothing).
releases_for_tag() {
  local tag="$1" all
  all="$(gh api --paginate "repos/${REPO}/releases?per_page=100")" || return 1
  jq -r --arg t "${tag}" '.[] | select(.tag_name == $t) | [(.id | tostring), (.draft | tostring)] | @tsv' <<<"${all}"
}

delete_release() {
  gh api -X DELETE "repos/${REPO}/releases/$1" >/dev/null
}

delete_tag() {
  gh api -X DELETE "repos/${REPO}/git/refs/tags/$1" >/dev/null
}

now_epoch() {
  date +%s
}

main() {
  local lookback="${LOOKBACK_HOURS:-72}" minage="${MIN_AGE_MINUTES:-120}"
  local runs rows row now newest head_sha info isha itagger imsg irc id event status conclusion created updated jobs pub tag rels
  local failed=0 draft_ids line rid rdraft published rc

  # release_tag reads the sweep's runs as dispatch runs (keep warning path).
  EVENT=workflow_dispatch
  runs="$(list_runs)" || { echo "::error::could not list Release runs; nothing swept." >&2; return 1; }
  now="$(now_epoch)"
  rows="$(jq -c '.workflow_runs[]' <<<"${runs}")" || return 1
  # Only the newest Release run (by created_at, whatever the event) is ever
  # eligible: a newer run means the version already moved on, and it may have
  # recreated the same tag name with a draft that is still in flight.
  newest="$(jq -r '[.workflow_runs[]] | max_by(.created_at) | .id // empty' <<<"${runs}")" || return 1

  while IFS= read -r row; do
    [ -n "${row}" ] || continue
    id="$(jq -r '.id' <<<"${row}")"
    if [ "${id}" != "${newest}" ]; then
      echo "Release run ${id}: a newer Release run exists; skipping."
      continue
    fi
    event="$(jq -r '.event' <<<"${row}")"
    status="$(jq -r '.status' <<<"${row}")"
    conclusion="$(jq -r '.conclusion // ""' <<<"${row}")"
    created="$(jq -r '.created_at' <<<"${row}")"
    updated="$(jq -r '.updated_at' <<<"${row}")"
    head_sha="$(jq -r '.head_sha // ""' <<<"${row}")"

    if [ "${event}" != workflow_dispatch ]; then
      echo "Release run ${id}: event ${event}, not a dispatch run; skipping."
      continue
    fi
    if [ "${status}" != completed ]; then
      echo "Release run ${id}: status ${status}, not completed; skipping."
      continue
    fi
    if [ "${conclusion}" = success ]; then
      echo "Release run ${id}: succeeded; skipping."
      continue
    fi
    if [ $((now - $(date -d "${created}" +%s))) -gt $((lookback * 3600)) ]; then
      echo "Release run ${id}: created more than ${lookback} h ago; skipping."
      continue
    fi
    if [ $((now - $(date -d "${updated}" +%s))) -lt $((minage * 60)) ]; then
      echo "Release run ${id}: updated less than ${minage} min ago, the retry may still come; skipping."
      continue
    fi
    if ! jobs="$(jobs_of_run "${id}")"; then
      echo "::warning::Release run ${id}: could not list its jobs; skipping."
      continue
    fi
    pub="$(jq -r '[.jobs[] | select(.name == "publish-release") | .conclusion][0] // ""' <<<"${jobs}")"
    if [ "${pub}" = success ]; then
      echo "Release run ${id}: publish-release succeeded on its latest attempt; skipping."
      continue
    fi
    tag="$(release_tag "${jobs}")"
    if [ -z "${tag}" ]; then
      echo "Release run ${id}: no kept tag (delete warning, no cleanup job or unreadable); skipping."
      continue
    fi
    irc=0
    info="$(tag_info "${tag}")" || irc=$?
    case "${irc}" in
      0) ;;
      2)
        echo "Release run ${id}: tag ${tag} already cleaned up; skipping."
        continue
        ;;
      3)
        echo "::warning::Release run ${id}: tag ${tag} is not the one run ${id} created; leaving it alone."
        continue
        ;;
      *)
        echo "::warning::Release run ${id}: could not look up tag ${tag}; leaving it alone."
        continue
        ;;
    esac
    IFS=$'\t' read -r isha itagger imsg <<<"${info}"
    if [ -z "${head_sha}" ] || [ "${isha}" != "${head_sha}" ] || [ "${itagger}" != "github-actions[bot]" ] || [ "${imsg}" != "Release ${tag}" ]; then
      echo "::warning::Release run ${id}: tag ${tag} is not the one run ${id} created; leaving it alone."
      continue
    fi
    irc=0
    published_release "${tag}" || irc=$?
    if [ "${irc}" = 0 ]; then
      echo "::warning::Release run ${id}: ${tag} has a published release; never deleting it. Check by hand."
      continue
    elif [ "${irc}" != 1 ]; then
      echo "::warning::Release run ${id}: could not check ${tag} for a published release; leaving it alone."
      continue
    fi
    if ! rels="$(releases_for_tag "${tag}")"; then
      echo "::warning::Release run ${id}: could not list releases for ${tag}; leaving it alone."
      continue
    fi
    published=0
    draft_ids=""
    while IFS=$'\t' read -r rid rdraft; do
      [ -n "${rid}" ] || continue
      if [ "${rdraft}" = true ]; then
        draft_ids+="${draft_ids:+ }${rid}"
      else
        published=1
      fi
    done <<<"${rels}"
    if [ "${published}" = 1 ]; then
      echo "::warning::Release run ${id}: ${tag} has a published release; never deleting it. Check by hand."
      continue
    fi

    rc=0
    for line in ${draft_ids}; do
      if ! delete_release "${line}"; then
        echo "::error::Release run ${id}: could not delete draft release ${line} for ${tag}."
        rc=1
      fi
    done
    if [ "${rc}" = 0 ] && ! delete_tag "${tag}"; then
      echo "::error::Release run ${id}: could not delete tag ${tag}."
      rc=1
    fi
    if [ "${rc}" = 1 ]; then
      failed=1
      continue
    fi
    echo "::warning::Release ${tag} (run ${id}) was kept for a runner retry that never came; deleted its tag and draft release; dispatch a fresh release."
  done <<<"${rows}"

  return "${failed}"
}

# Only run when executed directly, never when sourced (the test sources this).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
