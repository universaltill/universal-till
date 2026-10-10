#!/usr/bin/env bash
#
# guard-pipefail-grep-q (ut-docs#2946, ut-docs#2983): under pipefail, never
# pipe into a reader that exits early — grep -q/-l/-m (or --quiet, --silent,
# --files-with-matches, --max-count) stops at its first match, head after N
# lines/bytes. A writer still producing output (a big file or template) then
# dies of SIGPIPE, the pipeline reports 141, and the check reads "not found"
# — a flaky false failure, or with `!` a false pass. #2941 turned main red
# this way.
# Capture first and read a here-string instead: grep -q … <<<"$text";
# for the first line, `sed -n 1p` reads all of its input and never SIGPIPEs.
# Not detected: other readers that stop early, e.g. awk '… {exit}' after a
# pipe. Same hazard, same fix — keep them off the read side of a pipe.
#
# Scans scripts/ci/*.sh that mention pipefail, and the GitHub Actions run:
# steps that execute under pipefail (shell: bash — Actions runs it with
# -eo pipefail — or a body that sets it; scripts/ci/pipefailruns decides).
#
# Reviewed exception: same-line `pipefail-reader:allow <reason>`.
#
# Usage: guard-pipefail-grep-q.sh [scripts-dir] [workflows-dir]
#        (defaults scripts/ci and .github/workflows)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR="${1:-scripts/ci}"
WF_DIR="${2:-.github/workflows}"
MSG="pipe into an early-exit reader (grep -q/-l/-m, head) under pipefail can SIGPIPE its writer; read a captured here-string, or sed -n 1p for the first line (ut-docs#2946, #2983)"

# A single | (not ||) into grep/egrep/fgrep whose flags include q, l or m
# (-q, -qE, -E -q, -m1, -m 1, -l, --quiet, --silent, --files-with-matches,
# --max-count), or into head in any form; also behind `command`.
READER='(^|[^|])\|[[:space:]]*(command[[:space:]]+)?(head([[:space:]]|$)|[ef]?grep([[:space:]]+-[-a-zA-Z0-9=]+)*[[:space:]]+(-[a-zA-Z]*[qlm][a-zA-Z0-9]*|--quiet|--silent|--files-with-matches|--max-count(=[^[:space:]]*)?)([[:space:]]|$))'

# hit_lines <file>: line numbers of non-comment lines that pipe into a reader.
hit_lines() {
  local hits
  hits="$(grep -nE "$READER" "$1" | grep -vE '^[0-9]+:[[:space:]]*#' | grep -v 'pipefail-reader:allow' || true)"
  [ -z "$hits" ] || cut -d: -f1 <<<"$hits"
}

fails=0
while IFS= read -r f; do
  grep -q 'pipefail' "$f" || continue
  lines="$(hit_lines "$f")"
  if [ -n "$lines" ]; then
    while IFS= read -r n; do
      echo "::error file=${f},line=${n}::${MSG}"
    done <<<"$lines"
    fails=$((fails + 1))
  fi
done < <(find "$DIR" -maxdepth 1 -name '*.sh' -type f | sort)

if [ -d "$WF_DIR" ]; then
  wf_abs="$(cd "$WF_DIR" && pwd)"
  bodies="$(mktemp -d)"
  trap 'rm -rf "$bodies"' EXIT
  if ! index="$(cd "$ROOT_DIR" && go run ./scripts/ci/pipefailruns "$wf_abs" "$bodies")"; then
    echo "guard-pipefail-grep-q: could not read the workflows in ${WF_DIR}" >&2
    exit 1
  fi
  while IFS=$'\t' read -r name file first; do
    [ -n "$name" ] || continue
    lines="$(hit_lines "${bodies}/${name}")"
    [ -n "$lines" ] || continue
    while IFS= read -r n; do
      echo "::error file=${WF_DIR%/}/$(basename "$file"),line=$((first + n - 1))::${MSG}"
    done <<<"$lines"
    fails=$((fails + 1))
  done <<<"$index"
fi

if [ "$fails" -ne 0 ]; then
  echo "guard-pipefail-grep-q: ${fails} script(s)/step(s) pipe into an early-exit reader under pipefail" >&2
  exit 1
fi
echo "guard-pipefail-grep-q: ok"
