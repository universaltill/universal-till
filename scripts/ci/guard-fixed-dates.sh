#!/usr/bin/env bash
#
# A fixed calendar date in a test fixture is a time bomb: it passes for
# months, then fails on an unrelated PR the day the real clock passes it
# (ut-docs#5467, #5456). This flags every YYYY-MM-DD literal in a *_test.go or
# *.spec.ts line that falls within the next 60 days (today..today+60,
# UTC, inclusive) so the fixture is fixed ~60 days before it detonates.
#
# Fix: derive the date from time.Now(), move it out of the window, or - if
# no real-clock comparison reads it - add a same-line marker WITH a reason:
#   // fixed-date:ok display-only expires_at, never compared to the clock
# A bare marker does not exempt.
#
# Matching is deliberately not limited to "..." literals: dates also sit in
# backtick raw strings and single-quoted SQL. Comment-only lines (//, /*, and
# a block comment's leading-* lines) are skipped;
# a trailing comment on a code line is still scanned (accepted false
# positive; the marker fixes it). time.Date(Y, M, D...) is out of scope.
# Scans tracked and untracked-not-ignored files, minus vendor/, .claude/ and
# node_modules/.
#
# Usage: guard-fixed-dates.sh [repo-root]
# UT_FIXED_DATE_TODAY=YYYY-MM-DD pins "today" (for the guard's own test).
set -euo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

TODAY="${UT_FIXED_DATE_TODAY:-$(date -u +%F)}"
if ! [[ "${TODAY}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  echo "usage: [UT_FIXED_DATE_TODAY=YYYY-MM-DD] guard-fixed-dates.sh [repo-root] (bad date: ${TODAY})" >&2
  exit 2
fi
if ! END="$(date -u -d "${TODAY} +60 days" +%F 2>/dev/null)" &&
  ! END="$(date -u -j -v+60d -f %F "${TODAY}" +%F 2>/dev/null)"; then
  echo "guard-fixed-dates: cannot compute ${TODAY} + 60 days with date(1)" >&2
  exit 2
fi

files=()
while IFS= read -r -d '' f; do
  case "/${f}" in
    */vendor/* | */.claude/* | */node_modules/*) continue ;;
  esac
  [ -f "${ROOT}/${f}" ] || continue # tracked but deleted in the worktree
  files+=("$f")
done < <(git -C "${ROOT}" ls-files -z --cached --others --exclude-standard -- '*_test.go' '*.spec.ts')

if [ "${#files[@]}" -eq 0 ]; then
  echo "::error::guard-fixed-dates: found no test files under ${ROOT} - the scan is broken"
  exit 1
fi

cd "${ROOT}"
set +e
awk -v today="${TODAY}" -v end="${END}" '
function report(msg) { print msg; bad = 1 }
FNR == 1 { fn = FILENAME }
/^[[:space:]]*(\/\/|\/\*|\*)/ { next }
{
  line = $0
  dates = ""
  re = "(^|[^0-9])(19|20)[0-9][0-9]-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])([^0-9]|$)"
  rest = line
  while (match(rest, re)) {
    s = substr(rest, RSTART, RLENGTH)
    # step past the date itself, keep the trailing context char for the next match
    rest = substr(rest, RSTART + RLENGTH - 1)
    if (match(s, /(19|20)[0-9][0-9]-[0-9][0-9]-[0-9][0-9]/)) {
      d = substr(s, RSTART, 10)
      if (d >= today && d <= end) dates = dates (dates == "" ? "" : ", ") d
    }
  }
  if (dates == "") next
  if (line ~ /fixed-date:ok[[:space:]]+[^[:space:]]/) next
  note = ""
  if (line ~ /fixed-date:ok/) note = " The fixed-date:ok marker needs a reason after it."
  report("::error file=" fn ",line=" FNR "::fixed date " dates " is within 60 days (" today ".." end "); a test fixture compared against the real clock starts failing once that date passes (ut-docs#5467). Make it relative to time.Now(), move it out of range, or, if no real-clock comparison reads it, add a same-line \47fixed-date:ok <reason>\47." note)
}
END { exit bad ? 1 : 0 }
' "${files[@]}"
rc=$?
set -e
if [ "$rc" -ne 0 ]; then
  exit 1
fi
echo "✓ guard-fixed-dates: ${#files[@]} files, no fixed date in ${TODAY}..${END}"
