#!/usr/bin/env bash
# ut-docs#5490: re-runs the jobs of an ios-ci / desktop-darwin run that GitHub
# never gave a macOS runner. Intermittent hosted-capacity starvation: the job
# is cancelled after ~15 min with 0 steps and the annotation "The job was not
# acquired by Runner of type hosted even after multiple attempts". Same class
# as the Release case (ut-docs#5175, release-runner-retry.sh), which this
# script reuses: list_jobs / annotations_for / rerun_failed / listing_from are
# sourced from it, so there is one starvation detector.
#
# Called by .github/workflows/ci-runner-retry.yml when one of those runs did
# not succeed. It re-runs ("Re-run failed jobs") only when ALL of:
#   - the run's head repository is this repository (a fork PR's run is
#     skipped: workflow_run holds actions:write in the base repo);
#   - fewer than MAX_ATTEMPTS (default 3) attempts have run;
#   - every failed/cancelled/timed-out job is runner-starved: conclusion
#     `cancelled` AND an annotation saying "was not acquired by Runner". A
#     real failure is never retried here (a warning names the jobs).
#
# Honest limit: a re-run started with GITHUB_TOKEN may not fire another
# workflow_run, so in practice one automatic retry.
#
# Inputs (env): REPO, RUN_ID, RUN_ATTEMPT, GH_TOKEN (read by `gh`), HEAD_REPO
# (the triggering run's head_repository.full_name), optional MAX_ATTEMPTS (3).
set -euo pipefail

# shellcheck source=scripts/ci/release-runner-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/release-runner-retry.sh"

# decide_ci: reads "name<TAB>conclusion<TAB>starved(0|1)" lines on stdin and
# prints exactly one verdict line, pure (no network):
#   NONE            nothing failed or was cancelled
#   REAL <names>    a failed job is not runner-starved (comma-separated)
#   RERUN <names>   every failed job is runner-starved
decide_ci() {
  local name conclusion starved real="" starving=""
  while IFS=$'\t' read -r name conclusion starved; do
    [ -n "${name}" ] || continue
    case "${conclusion}" in
      failure | cancelled | timed_out)
        if [ "${conclusion}" = "cancelled" ] && [ "${starved}" = 1 ]; then
          starving+="${starving:+, }${name}"
        else
          real+="${real:+, }${name}"
        fi
        ;;
    esac
  done
  if [ -n "${real}" ]; then
    echo "REAL ${real}"
  elif [ -n "${starving}" ]; then
    echo "RERUN ${starving}"
  else
    echo "NONE"
  fi
}

main() {
  local max="${MAX_ATTEMPTS:-3}" jobs listing verdict
  if [ "${HEAD_REPO:-}" != "${REPO}" ]; then
    echo "::notice::Run ${RUN_ID}: head repository '${HEAD_REPO:-(unknown)}' is not ${REPO} (a fork PR?); not retrying automatically."
    return 0
  fi
  if [ "${RUN_ATTEMPT}" -ge "${max}" ]; then
    echo "::warning::Run ${RUN_ID}: automatic runner retries are used up (attempt ${RUN_ATTEMPT} of ${max}). Use \"Re-run failed jobs\" by hand."
    return 0
  fi
  jobs="$(list_jobs)" || return 1
  listing="$(listing_from "${jobs}")" || return 1
  verdict="$(decide_ci <<<"${listing}")"
  case "${verdict}" in
    NONE)
      echo "Run ${RUN_ID}: no failed or cancelled jobs; nothing to retry."
      ;;
    REAL*)
      echo "::warning::Run ${RUN_ID}: not retrying automatically, these jobs failed for a reason other than runner starvation: ${verdict#REAL }. Fix it, then \"Re-run failed jobs\" by hand."
      ;;
    RERUN*)
      echo "Run ${RUN_ID}: re-running runner-starved jobs: ${verdict#RERUN }"
      rerun_failed || return 1
      ;;
  esac
}

# Only run when executed directly, never when sourced (the test sources this).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main
fi
