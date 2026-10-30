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
#   - every failed/cancelled/timed-out job is runner-starved: conclusion
#     `cancelled` AND a check-run annotation saying "was not acquired by
#     Runner". A real build/notarize failure is never retried here;
#   - either publish-release succeeded (the post-publish case: the macOS
#     .dmg), or it did not and this is attempt < PREPUBLISH_MAX_ATTEMPTS
#     (default 2, i.e. attempt 1 only; ut-docs#5205). Pre-publish, a starved
#     job (e.g. linux-shells on the hosted arm pool) skips goreleaser and
#     publish-release; a re-run started with GITHUB_TOKEN may not fire
#     another workflow_run, so only one automatic retry is promised.
#
# `keep-tag` mode (cleanup-orphaned-tag in release.yml) applies the same rule
# so the two cannot disagree: it prints `keep` only when this run will be
# re-run pre-publish (the tag and draft must survive for that), else `delete`
# (also on any API error: the old behaviour).
#
# Inputs (env): REPO, RUN_ID, RUN_ATTEMPT, GH_TOKEN (read by `gh`),
# optional MAX_ATTEMPTS (3) and PREPUBLISH_MAX_ATTEMPTS (2).
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
#   NOPUBLISH            publish-release is not success and nothing failed,
#                        or something failed for a real reason
#   PRERUN <names>       publish-release is not success and every failed job
#                        is runner-starved (ut-docs#5205)
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
    if [ "${failed}" = 1 ] && [ -z "${real}" ]; then
      echo "PRERUN ${starving}"
    else
      echo "NOPUBLISH"
    fi
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

# keep_tag: prints exactly `keep` or `delete` on stdout (diagnostics on
# stderr); always exits 0 so cleanup-orphaned-tag still falls back to deleting.
# cleanup's own job is in the listing with an empty conclusion: ignored.
keep_tag() {
  local max="${MAX_ATTEMPTS:-3}" premax="${PREPUBLISH_MAX_ATTEMPTS:-2}" listing verdict
  if [ "${RUN_ATTEMPT}" -ge "${max}" ] || [ "${RUN_ATTEMPT}" -ge "${premax}" ]; then
    echo delete
    return 0
  fi
  if ! listing="$(build_listing)"; then
    echo "::warning::Release run ${RUN_ID}: could not list jobs or annotations; deleting the tag as before." >&2
    echo delete
    return 0
  fi
  verdict="$(decide <<<"${listing}")"
  case "${verdict}" in
    PRERUN*) echo keep ;;
    *) echo delete ;;
  esac
}

main() {
  local max="${MAX_ATTEMPTS:-3}" premax="${PREPUBLISH_MAX_ATTEMPTS:-2}" listing verdict
  if [ "${RUN_ATTEMPT}" -ge "${max}" ]; then
    echo "::warning::Release run ${RUN_ID}: automatic runner retries are used up (attempt ${RUN_ATTEMPT} of ${max}). Use \"Re-run failed jobs\" by hand."
    return 0
  fi
  listing="$(build_listing)" || return 1
  verdict="$(decide <<<"${listing}")"
  case "${verdict}" in
    NOPUBLISH)
      echo "::notice::Release run ${RUN_ID}: publish-release did not succeed and not only for runner starvation (or nothing failed), so the release never published (cleanup-orphaned-tag removed its tag). Not retrying; dispatch a fresh release."
      ;;
    NONE)
      echo "Release run ${RUN_ID}: no failed or cancelled jobs; nothing to retry."
      ;;
    PRERUN*)
      if [ "${RUN_ATTEMPT}" -ge "${premax}" ]; then
        echo "::warning::Release run ${RUN_ID}: runner-starved jobs (${verdict#PRERUN }) stopped the release before it published, and no automatic retry remains (attempt ${RUN_ATTEMPT}); cleanup-orphaned-tag removed the tag. Dispatch a fresh release."
      else
        echo "Release run ${RUN_ID}: release not published; re-running runner-starved jobs: ${verdict#PRERUN }"
        if ! rerun_failed; then
          echo "::error::Release run ${RUN_ID}: the re-run request failed, but cleanup-orphaned-tag kept the tag and draft release for this retry. Delete them by hand: git push origin :refs/tags/<tag> and gh release delete <tag> --cleanup-tag."
          return 1
        fi
      fi
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
  case "${1:-}" in
    keep-tag) keep_tag ;;
    "") main ;;
    *) echo "usage: $0 [keep-tag]" >&2; exit 2 ;;
  esac
fi
