#!/usr/bin/env bash
#
# guard-pipefail-grep-q (ut-docs#2946): in a script that sets pipefail, never
# pipe into `grep -q`. grep -q exits at its first match; a writer still
# producing output (a big file or template) then dies of SIGPIPE, the
# pipeline reports 141, and the check reads "not found" — a flaky false
# failure, or with `!` a false pass. #2941 turned main red this way.
# Capture first and grep a here-string instead: grep -q … <<<"$text".
#
# Usage: guard-pipefail-grep-q.sh [dir]   (default scripts/ci)
set -euo pipefail

DIR="${1:-scripts/ci}"
fails=0
while IFS= read -r f; do
  grep -q 'pipefail' "$f" || continue
  # A single | (not ||) into grep/egrep/fgrep whose flags include q (-q,
  # -qE, -Eq, -E -q, -i -q, --quiet), also behind `command`.
  hits="$(grep -nE '(^|[^|])\|[[:space:]]*(command[[:space:]]+)?[ef]?grep([[:space:]]+-[-a-zA-Z=]+)*[[:space:]]+(-[a-zA-Z]*q[a-zA-Z]*|--quiet)([[:space:]]|$)' "$f" | grep -vE '^[0-9]+:[[:space:]]*#' || true)"
  if [ -n "$hits" ]; then
    while IFS= read -r h; do
      echo "::error file=${f},line=${h%%:*}::pipe into grep -q under pipefail can SIGPIPE its writer; grep a captured here-string instead (ut-docs#2946)"
    done <<<"$hits"
    fails=$((fails + 1))
  fi
done < <(find "$DIR" -maxdepth 1 -name '*.sh' -type f | sort)

if [ "$fails" -ne 0 ]; then
  echo "guard-pipefail-grep-q: ${fails} script(s) pipe into grep -q under pipefail" >&2
  exit 1
fi
echo "guard-pipefail-grep-q: ok"
