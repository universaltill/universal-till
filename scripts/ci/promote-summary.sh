#!/usr/bin/env bash
#
# `summary` job of promote-release.yml (ut-docs#6407): ONE markdown table of what
# the promote did, written to $GITHUB_STEP_SUMMARY and posted as a comment on the
# standing issue "Store release log" in this (public) repo, created once if absent.
# Never prints seller id, app id, tenant id or a token: it only reads the values
# below. A comment failure is a warning, never a failed run.
#
# Env: COMMIT (real run: false = draft only), VERSION, REPO, RUN_URL, DRY_RUN, PROBE_STORE, PREFLIGHT_RESULT,
# MS_RESULT (needs.microsoft.result), MS_READY, MS_SHA256, STORES_JSON (the
# `stores` job output; empty if it did not run), GITHUB_STEP_SUMMARY, GH_TOKEN.
set -uo pipefail

REPO="${REPO:?REPO is required}"
version="${VERSION:-}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || version="(invalid)"
tag="v${version}"
dry="${DRY_RUN:-true}"
probe="${PROBE_STORE:-false}"
pre="${PREFLIGHT_RESULT:-skipped}"
ms="${MS_RESULT:-skipped}"
ready="${MS_READY:-}"
sha="${MS_SHA256:-}"
[[ "$sha" =~ ^[0-9a-f]{64}$ ]] || sha=""
run_url="${RUN_URL:-}"
[[ "$run_url" =~ ^https://github\.com/[A-Za-z0-9._/-]+$ ]] || run_url="n/a"
TITLE="Store release log"

if [ "$dry" = false ]; then mode="REAL PROMOTE"; else mode="DRY RUN"; fi
[ "$dry" = true ] && [ "$probe" = true ] && mode="DRY RUN (read-only Store probe)"

release_run="n/a"
if [ "$version" != "(invalid)" ]; then
  rr="$(gh run list --repo "$REPO" --workflow release.yml -L 30 --json databaseId,headBranch,url,createdAt 2>/dev/null \
    | jq -r --arg t "$tag" '[.[] | select(.headBranch==$t)] | sort_by(.createdAt) | last | .url // empty' 2>/dev/null || true)"
  [[ "$rr" =~ ^https://github\.com/[A-Za-z0-9._/-]+$ ]] && release_run="$rr"
fi

# Microsoft rows
if [ "$pre" != success ]; then
  msverify="not run (preflight refused)"; mssubmit="not run (preflight refused)"
else
  if [ "$ready" = true ]; then msverify="verify: passed"; else msverify="verify: failed (or did not run)"; fi
  if [ "$dry" = true ] && [ "$probe" = false ]; then
    mssubmit="verify only, Store not touched"
  elif [ "$ready" != true ]; then
    mssubmit="not reached (verify did not pass)"
  else
    case "$ms" in
      success) if [ "$dry" = true ]; then mssubmit="probe ok (read-only, nothing changed)"; else if [ "${COMMIT:-true}" = false ]; then mssubmit="package set (draft, not committed)"; else mssubmit="submitted"; fi; fi;;
      failure) mssubmit="rejected or failed";;
      cancelled) mssubmit="cancelled (approval rejected, expired, or run cancelled)";;
      *) mssubmit="skipped";;
    esac
  fi
fi
[ -z "$sha" ] || msverify="${msverify}; sha256 ${sha}"

# iOS / Play rows
row() { # row <key> -> "state: detail (url)"
  if [ -z "${STORES_JSON:-}" ] || ! jq -e . >/dev/null 2>&1 <<<"$STORES_JSON"; then echo "not run"; return; fi
  jq -r --arg k "$1" '.[$k] | "\(.state)" + (if (.detail // "") != "" then ": \(.detail)" else "" end) + (if (.url // "") != "" then " (\(.url))" else "" end)' <<<"$STORES_JSON"
}
ios="$(row ios)"
play="$(row play)"

body="$(
  echo "### Store promote ${tag}: ${mode}"
  echo
  echo "- Promote run: ${run_url}"
  echo "- Release run: ${release_run}"
  echo
  echo "| Store | Result |"
  echo "|---|---|"
  echo "| Microsoft (verify) | ${msverify} |"
  echo "| Microsoft (submit) | ${mssubmit} |"
  echo "| iOS TestFlight | ${ios} |"
  echo "| Google Play (internal) | ${play} |"
)"

printf '%s\n' "$body"
[ -z "${GITHUB_STEP_SUMMARY:-}" ] || printf '%s\n' "$body" >>"$GITHUB_STEP_SUMMARY"

tmp="$(mktemp -d)"
# shellcheck disable=SC2317,SC2329
cleanup() { rm -rf -- "${tmp:?}"; }
trap cleanup EXIT
printf '%s\n' "$body" >"$tmp/body.md"

issue="$(gh issue list --repo "$REPO" --state open --author "app/github-actions" --search "\"${TITLE}\" in:title" --json number,title 2>/dev/null \
  | jq -r --arg t "$TITLE" '[.[] | select(.title==$t)] | sort_by(.number) | first | .number // empty' 2>/dev/null || true)"
if [ -z "$issue" ]; then
  url="$(gh issue create --repo "$REPO" --title "$TITLE" --body "Standing log of promote-release runs (versions, run links, per-store state). Written by promote-release.yml; no ids or tokens." 2>/dev/null || true)"
  issue="${url##*/}"
fi
if [[ "$issue" =~ ^[0-9]+$ ]] && gh issue comment "$issue" --repo "$REPO" --body-file "$tmp/body.md" >/dev/null 2>&1; then
  echo "posted the summary on issue #${issue}"
else
  echo "::warning::could not post the promote summary to the '${TITLE}' issue; it is in the run summary"
fi
exit 0
