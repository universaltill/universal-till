#!/usr/bin/env bash
#
# Weekly, non-blocking report of manual screenshots gone stale (ut-docs#4582).
#
# guard-docs-shots.sh deliberately leaves web/locales/**/*.json out of the
# surface hash (every key touches every locale file, so including it would
# force a full regen on almost any string change). The accepted cost: a copy
# change can go stale in a screenshot — e.g. the ar/fa manual still showing an
# old UI word — without tripping any guard. This script closes that gap after
# the fact instead of on every PR: docs-shots-locale-drift.yml runs
# `make docs-shots` on main every week (the output is byte-deterministic,
# ut-docs#2184), then calls this script, which compares the result with the
# committed web/help/img/** and keeps ONE tracking issue in step with it.
#
# Never fails the build on drift, never commits, never pushes: the report is
# an issue, and the regenerated files ride along as a workflow artifact.
# (A bot-authored PR needs the bot-identity decision ut-docs#3738 owns.)
#
#   docs-shots-locale-drift.sh collect <out-dir>
#       After `make docs-shots`. Writes <out-dir>/stale.txt (one
#       "<status> <path relative to web/help/img>" per line, sorted),
#       <out-dir>/body.md (the issue body) and copies every changed or new
#       file under <out-dir>/img/. Prints "count=<n>" (append it to
#       $GITHUB_OUTPUT). n=0 → stale.txt is empty and nothing else is written.
#   docs-shots-locale-drift.sh sync-issue <out-dir>
#       n>0 → update the open issue labelled $DRIFT_LABEL (or open one).
#       n=0 → close that issue if one is open (the drift was fixed); no
#       issue → do nothing. Needs `gh` with GH_TOKEN (issues: write).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMG_DIR="web/help/img"
DRIFT_LABEL="${DRIFT_LABEL:-docs-shots-drift}"
DRIFT_TITLE="Manual screenshots out of date (weekly docs-shots check)"

usage() {
  echo "usage: $0 collect <out-dir> | sync-issue <out-dir>" >&2
  exit 2
}

run_url() {
  if [ -n "${GITHUB_RUN_ID:-}" ]; then
    echo "${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-universaltill/universal-till}/actions/runs/${GITHUB_RUN_ID}"
  fi
}

collect() {
  local out="$1" line xy path status n porcelain
  mkdir -p "$out"
  : >"$out/stale.txt"
  # -z: paths are NUL-terminated and never quoted; --untracked-files=all lists
  # a new file individually instead of collapsing it into its directory.
  # Written to a file first (NULs don't survive $(...)) so a failing git
  # stops here under set -e — read straight from a process substitution, its
  # failure would look like "no drift" and sync-issue would close the issue.
  porcelain="$(mktemp)"
  git -C "$ROOT" status --porcelain=v1 -z --untracked-files=all -- "$IMG_DIR" >"$porcelain"
  while IFS= read -r -d '' line; do
    xy="${line:0:2}"
    path="${line:3}"
    case "$xy" in
      '??') status=new ;;
      *D*) status=deleted ;;
      *) status=changed ;;
    esac
    echo "$status ${path#"$IMG_DIR"/}" >>"$out/stale.txt"
    if [ "$status" != deleted ]; then
      mkdir -p "$out/img/$(dirname "${path#"$IMG_DIR"/}")"
      cp "$ROOT/$path" "$out/img/${path#"$IMG_DIR"/}"
    fi
  done <"$porcelain"
  rm -f "$porcelain"
  LC_ALL=C sort -k2 -o "$out/stale.txt" "$out/stale.txt"
  n="$(wc -l <"$out/stale.txt" | tr -d ' ')"
  if [ "$n" -gt 0 ]; then
    write_body "$out" "$n"
  fi
  echo "count=$n"
}

write_body() {
  local out="$1" n="$2" url
  url="$(run_url)"
  {
    echo "<!-- docs-shots-locale-drift (ut-docs#4582) -->"
    echo "The weekly \`make docs-shots\` run on \`main\` produced **$n** file(s) that differ from the committed \`$IMG_DIR/\`."
    echo
    echo "\`guard-docs-shots.sh\` does not hash \`web/locales/**\` (by design), so a translation change can leave a screenshot showing old wording without failing any PR. This issue is that report; it is updated in place each week and closed automatically once a run finds no difference."
    echo
    echo "**Stale files** (\`<status> <locale>/<id>.png\`, relative to \`$IMG_DIR/\`):"
    echo
    echo '```'
    cat "$out/stale.txt"
    echo '```'
    echo
    echo "**To fix:** run \`make docs-shots\` on an up-to-date \`main\` checkout and open a PR with the result"
    if [ -n "$url" ]; then
      echo "(or take the regenerated files from the \`docs-shots-locale-drift\` artifact of [this run]($url))."
    else
      echo "(or take the regenerated files from the run's \`docs-shots-locale-drift\` artifact)."
    fi
  } >"$out/body.md"
}

sync_issue() {
  local out="$1" n open
  [ -f "$out/stale.txt" ] || {
    echo "docs-shots-locale-drift: $out/stale.txt missing — run collect first" >&2
    exit 1
  }
  n="$(wc -l <"$out/stale.txt" | tr -d ' ')"
  open="$(gh issue list --label "$DRIFT_LABEL" --state open --limit 1 --json number --jq '.[0].number // empty')"
  if [ "$n" -gt 0 ]; then
    gh label create "$DRIFT_LABEL" --color FBCA04 \
      --description "Manual screenshots differ from a fresh make docs-shots (ut-docs#4582)" --force >/dev/null
    if [ -n "$open" ]; then
      gh issue edit "$open" --body-file "$out/body.md" >/dev/null
      echo "docs-shots-locale-drift: updated issue #$open ($n stale file(s))"
    else
      gh issue create --title "$DRIFT_TITLE" --label "$DRIFT_LABEL" --body-file "$out/body.md"
      echo "docs-shots-locale-drift: opened an issue ($n stale file(s))"
    fi
  elif [ -n "$open" ]; then
    gh issue close "$open" --comment "The weekly docs-shots run on \`main\` matches the committed screenshots again — closing. $(run_url)" >/dev/null
    echo "docs-shots-locale-drift: no drift — closed issue #$open"
  else
    echo "docs-shots-locale-drift: no drift, no open issue — nothing to do"
  fi
}

[ $# -eq 2 ] || usage
case "$1" in
  collect) collect "$2" ;;
  sync-issue) sync_issue "$2" ;;
  *) usage ;;
esac
