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
# A pre-publish re-run also needs the release tag to still exist (ut-docs#5209):
# cleanup-orphaned-tag runs seconds after the starved cancel and can decide
# `delete` before the starvation annotation is readable, while this retry
# reads it fine later and would re-run into a missing tag. So main() resolves
# the tag (`release_tag`: HEAD_BRANCH for a tag push, else the tag named in
# cleanup-orphaned-tag's keep warning) and re-runs only if `tag_exists`.
#
# Inputs (env): REPO, RUN_ID, RUN_ATTEMPT, GH_TOKEN (read by `gh`),
# EVENT and HEAD_BRANCH (the triggering run's event and head_branch; for a
# tag push head_branch is the tag name), optional MAX_ATTEMPTS (3) and
# PREPUBLISH_MAX_ATTEMPTS (2).
#
# list_jobs/annotations_for/rerun_failed/tag_exists are their own functions so
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

# tag_exists <tag>: succeeds only when the tag ref is still in the repo.
tag_exists() {
  gh api "repos/${REPO}/git/ref/tags/$1" >/dev/null
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
  local jobs
  jobs="$(list_jobs)" || return 1
  listing_from "${jobs}"
}

# listing_from <jobs json>: the build_listing rows for an already fetched
# jobs listing (main() keeps the JSON for release_tag: one listing call).
listing_from() {
  local jobs="$1" rows name id conclusion starved notes
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

# release_tag <jobs json>: prints the release tag, or nothing when it cannot be
# determined or looks unsafe. A tag push: HEAD_BRANCH is the tag name.
# workflow_dispatch: cleanup-orphaned-tag's keep warning names it (the phrase
# below is pinned against release.yml by release-runner-retry_test.sh); a
# delete message, no cleanup job or no match yields no tag.
release_tag() {
  local jobs="$1" tag="" id notes
  if [ "${EVENT:-}" = push ]; then
    tag="${HEAD_BRANCH:-}"
  else
    id="$(jq -r '[.jobs[] | select(.name == "cleanup-orphaned-tag") | .id][0] // empty' <<<"${jobs}")" || return 0
    if [ -n "${id}" ]; then
      notes="$(annotations_for "${id}")" || return 0
      if [[ "${notes}" =~ Release\ ([^[:space:]]+)\ did\ not\ publish\ because\ a\ job\ was\ never\ given\ a\ runner\;\ keeping\ the\ tag ]]; then
        tag="${BASH_REMATCH[1]}"
      fi
    fi
  fi
  if [[ "${tag}" =~ ^v[0-9A-Za-z._-]+$ ]]; then
    printf '%s\n' "${tag}"
  fi
  return 0
}

main() {
  local max="${MAX_ATTEMPTS:-3}" premax="${PREPUBLISH_MAX_ATTEMPTS:-2}" jobs listing verdict tag
  if [ "${RUN_ATTEMPT}" -ge "${max}" ]; then
    echo "::warning::Release run ${RUN_ID}: automatic runner retries are used up (attempt ${RUN_ATTEMPT} of ${max}). Use \"Re-run failed jobs\" by hand."
    return 0
  fi
  jobs="$(list_jobs)" || return 1
  listing="$(listing_from "${jobs}")" || return 1
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
        # A starved `prepare` never created a tag (and cleanup, which needs
        # prepare, never ran): nothing to check, the re-run makes the tag.
        if [[ ", ${verdict#PRERUN }, " != *", prepare, "* ]]; then
          tag="$(release_tag "${jobs}")"
          if [ -z "${tag}" ] || ! tag_exists "${tag}"; then
            if [ "${EVENT:-}" = push ]; then
              echo "::warning::Release run ${RUN_ID}: runner-starved jobs (${verdict#PRERUN }) stopped the release before it published, but its pushed tag ${tag:-(unknown)} was not found or could not be confirmed, so the run is not retried. If the tag exists, use \"Re-run failed jobs\" by hand."
            else
              echo "::warning::Release run ${RUN_ID}: runner-starved jobs (${verdict#PRERUN }) stopped the release before it published, but its tag ${tag:-(unknown)} is gone or could not be confirmed, so the run is not retried. Dispatch a fresh release; if the tag still exists, delete it by hand (release-orphan-tag-sweep.yml does this after ~2 h): git push origin :refs/tags/${tag:-<tag>} and gh release delete ${tag:-<tag>} --cleanup-tag."
            fi
            return 0
          fi
        fi
        echo "Release run ${RUN_ID}: release not published; re-running runner-starved jobs: ${verdict#PRERUN }"
        if ! rerun_failed; then
          echo "::error::Release run ${RUN_ID}: the re-run request failed, but cleanup-orphaned-tag kept the tag and draft release for this retry. Delete them by hand (release-orphan-tag-sweep.yml does this after ~2 h): git push origin :refs/tags/<tag> and gh release delete <tag> --cleanup-tag."
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
