#!/usr/bin/env bash
# ut-docs#5175: re-runs the jobs of a Release run that GitHub never gave a
# runner. Release run 40889814547 (v0.35.3) published without its macOS .dmg
# because `macos-app` (macos-14) was cancelled after ~15 min with the
# annotation "The job was not acquired by Runner of type hosted even after
# multiple attempts" — hosted-capacity starvation, not our config.
#
# Called by .github/workflows/release-runner-retry.yml when a Release run
# did not succeed. It re-runs ("Re-run failed jobs") only when ALL of:
#   - fewer than MAX_ATTEMPTS (default 3) attempts have run;
#   - publish-release succeeded (an unpublished release needs a fresh
#     dispatch; cleanup-orphaned-tag already removed its tag);
#   - every failed/cancelled/timed-out job is runner-starved: conclusion
#     `cancelled` AND a check-run annotation saying "was not acquired by
#     Runner". A real build/notarize failure is never retried here.
#
# Inputs (env): REPO, RUN_ID, RUN_ATTEMPT, GH_TOKEN (read by `gh`).
#
# list_jobs/annotations_for/rerun_failed are their own functions so
# release-runner-retry_test.sh can redefine them and exercise main() with no
# network. Each `gh` result is captured with `x="$(...)"`, never read off a
# process substitution, so a failing call fails the script (see
# prune-nightly-assets.sh: a process substitution hides its exit status).
set -euo pipefail

list_jobs() {
  gh api "repos/${REPO}/actions/runs/${RUN_ID}/jobs?filter=latest&per_page=100"
}

# annotations_for <job id>: the check-run annotation messages, one per line.
annotations_for() {
  gh api "repos/${REPO}/check-runs/$1/annotations" --jq '.[].message'
}

rerun_failed() {
  gh api -X POST "repos/${REPO}/actions/runs/${RUN_ID}/rerun-failed-jobs"
}

# decide: reads "name<TAB>conclusion<TAB>starved(0|1)" lines on stdin and
# prints exactly one verdict line, pure (no network):
#   NOPUBLISH            publish-release is missing or not success
#   NONE                 nothing failed or was cancelled
#   REAL <names>         a failed job is not runner-starved (comma-separated)
#   RERUN <names>        every failed job is runner-starved
decide() {
  local name conclusion starved published=0 failed=0 real="" starving=""
  while IFS=$'\t' read -r name conclusion starved; do
    [ -n "${name}" ] || continue
    if [ "${name}" = "publish-release" ] && [ "${conclusion}" = "success" ]; then
      published=1
    fi
    case "${conclusion}" in
      failure | cancelled | timed_out)
        failed=1
        if [ "${conclusion}" = "cancelled" ] && [ "${starved}" = 1 ]; then
          starving+="${starving:+, }${name}"
        else
          real+="${real:+, }${name}"
        fi
        ;;
    esac
  done
  if [ "${published}" = 0 ]; then
    echo "NOPUBLISH"
  elif [ "${failed}" = 0 ]; then
    echo "NONE"
  elif [ -n "${real}" ]; then
    echo "REAL ${real}"
  else
    echo "RERUN ${starving}"
  fi
}

# build_listing: "name<TAB>conclusion<TAB>starved" per job of the latest
# attempt. Only cancelled jobs cost an annotations call.
build_listing() {
  local jobs rows name id conclusion starved notes
  jobs="$(list_jobs)" || return 1
  rows="$(jq -r '.jobs[] | [.name, (.id | tostring), (.conclusion // "")] | @tsv' <<<"${jobs}")"
  while IFS=$'\t' read -r name id conclusion; do
    [ -n "${name}" ] || continue
    starved=0
    if [ "${conclusion}" = "cancelled" ]; then
      notes="$(annotations_for "${id}")" || return 1
      if [[ "${notes}" == *"was not acquired by Runner"* ]]; then
        starved=1
      fi
    fi
    printf '%s\t%s\t%s\n' "${name}" "${conclusion}" "${starved}"
  done <<<"${rows}"
}

main() {
  local max="${MAX_ATTEMPTS:-3}" listing verdict
  if [ "${RUN_ATTEMPT}" -ge "${max}" ]; then
    echo "::warning::Release run ${RUN_ID}: automatic runner retries are used up (attempt ${RUN_ATTEMPT} of ${max}). Use \"Re-run failed jobs\" by hand."
    return 0
  fi
  listing="$(build_listing)" || return 1
  verdict="$(decide <<<"${listing}")"
  case "${verdict}" in
    NOPUBLISH)
      echo "::notice::Release run ${RUN_ID}: publish-release did not succeed, so the release never published (cleanup-orphaned-tag removed its tag). Not retrying; dispatch a fresh release."
      ;;
    NONE)
      echo "Release run ${RUN_ID}: no failed or cancelled jobs; nothing to retry."
      ;;
    REAL*)
      echo "::warning::Release run ${RUN_ID}: not retrying automatically, these jobs failed for a reason other than runner starvation: ${verdict#REAL }. Fix it, then \"Re-run failed jobs\" by hand."
      ;;
    RERUN*)
      echo "Release run ${RUN_ID}: re-running runner-starved jobs: ${verdict#RERUN }"
      rerun_failed || return 1
      ;;
  esac
}

# Only run when executed directly, never when sourced (the test sources this).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main
fi
