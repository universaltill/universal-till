#!/usr/bin/env bash
#
# Help-topic terminology guard (ut-docs#5243): a translated help topic must
# call a thing what the till's own UI strings for that locale call it. The
# German manual once used both "Manager" and "Führungskraft" for the manager
# role while the UI only ever says "Manager"; guard-help-drift.sh compares
# structure only, so it could not see this.
#
# Reads scripts/ci/help-terms.txt (lines `<locale>|<forbidden regex>|<use
# instead>`; `#` comments and blank lines ignored) and fails with
# file:line for every match in web/help/<locale>/**/*.md. Reviewed
# exception: a same-line `help-term:allow <reason>` marker. An entry whose
# locale has no web/help/<locale> folder, or whose regex grep rejects, fails
# rather than silently checking nothing. Matching is on precomposed text (ü,
# not u + U+0308), which is how every topic is written today.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# Case-insensitive matching of non-ASCII letters (Ü/ü) needs a UTF-8 locale.
export LC_ALL=C.UTF-8

TERMS="scripts/ci/help-terms.txt"
fails=0

# The regex may itself contain '|', so split on the first and the last one.
while IFS= read -r line || [ -n "${line}" ]; do
  line="${line%$'\r'}"
  case "${line}" in '' | '#'*) continue ;; esac
  loc="${line%%|*}"
  instead="${line##*|}"
  pattern="${line#*|}"
  pattern="${pattern%|*}"
  if [ "${loc}" = "${line}" ] || [ -z "${pattern}" ] || [ -z "${instead}" ] || [ "${pattern}" = "${line#*|}" ]; then
    echo "${TERMS}: malformed entry '${line}'" >&2
    fails=$((fails + 1))
    continue
  fi
  dir="web/help/${loc}"
  if [ ! -d "${dir}" ]; then
    echo "${TERMS}: no help folder ${dir} for entry '${line}'" >&2
    fails=$((fails + 1))
    continue
  fi
  # grep exits 1 for no match (fine) and 2 for a bad regex (a failure).
  grc=0
  hits="$(grep -rniE --include='*.md' -- "${pattern}" "${dir}")" || grc=$?
  if [ "${grc}" -gt 1 ]; then
    echo "${TERMS}: grep rejected the regex in entry '${line}'" >&2
    fails=$((fails + 1))
    continue
  fi
  while IFS= read -r hit; do
    [ -n "${hit}" ] || continue
    # hit = file:line:text; report the matched word, not the whole line.
    prefix="${hit%%:*}"
    rest="${hit#*:}"
    lineno="${rest%%:*}"
    text="${rest#*:}"
    case "${text}" in *help-term:allow*) continue ;; esac
    match="$(grep -oiE -- "${pattern}" <<<"${text}" | sed -n 1p)"
    echo "${prefix}:${lineno}: \"${match}\" — use \"${instead}\" (as the till's UI does)" >&2
    fails=$((fails + 1))
  done <<<"${hits}"
done <"${TERMS}"

if [ "${fails}" -ne 0 ]; then
  echo "${fails} help-topic terminology violation(s); see ${TERMS}" >&2
  exit 1
fi
echo "✓ help-topic terminology guard passed"
