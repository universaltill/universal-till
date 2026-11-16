#!/usr/bin/env bash
# Self-test for build-locale-overlay.sh (ut-docs#4371). Builds throwaway
# ut-translations-shaped trees in a fresh mktemp -d; never touches the repo.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${HERE}/build-locale-overlay.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

failures=0
n=0
OUT=""
RC=0
TREE=""
OUTDIR=""

# new_tree: fresh fixture tree; sets TREE and OUTDIR.
new_tree() {
  n=$((n + 1))
  TREE="${TMP}/t${n}"
  OUTDIR="${TMP}/o${n}"
  mkdir -p "${TREE}/meta" "${TREE}/sale" "${TREE}/staff" "${TREE}/website"
  echo '{"sale":{},"staff":{}}' > "${TREE}/meta/areas.json"
}

# gen N PREFIX: a JSON object with N string keys.
gen() { jq -n --argjson n "$1" --arg p "$2" '[range($n)] | map({key: ($p + (. | tostring)), value: ("Wert " + (. | tostring))}) | from_entries'; }

run() { # run <args...>; sets OUT and RC
  RC=0
  OUT="$(bash "${SCRIPT}" "$@" 2>&1)" || RC=$?
}

# Conditions (return status) used by t.
succeeded() { [[ "${RC}" -eq 0 ]]; }
failed() { [[ "${RC}" -ne 0 ]]; }
failed_mentioning() { [[ "${RC}" -ne 0 ]] && grep -q -- "$1" <<<"${OUT}"; }
overlay_is() { [[ "$(jq -r "$1" "${OUTDIR}/de.json")" == "$2" ]]; }

# t <name> <condition...>: pass if the condition command succeeds.
t() {
  local name="$1"
  shift
  if "$@"; then
    echo "ok   - ${name}"
  else
    echo "FAIL - ${name}"
    printf '%s\n' "${OUT}" | while IFS= read -r line; do echo "       ${line}"; done
    failures=$((failures + 1))
  fi
}

# 1. merge across two areas; a surface folder outside areas.json is ignored;
#    blank and non-string values are skipped.
new_tree
gen 400 a | jq '. + {"blank": "", "num": 3}' > "${TREE}/sale/de.json"
gen 400 b > "${TREE}/staff/de.json"
echo '{"web.only":"Nur Web"}' > "${TREE}/website/de.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "run succeeds" succeeded
t "merges two areas (800 keys)" overlay_is 'length' 800
t "surface folder outside areas.json ignored" overlay_is 'has("web.only")' false
t "blank and non-string values skipped" overlay_is 'has("blank") or has("num")' false

# 2. an identical duplicate across areas is fine.
new_tree
gen 600 a > "${TREE}/sale/de.json"
echo '{"a0":"Wert 0"}' > "${TREE}/staff/de.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "identical duplicate key tolerated" succeeded

# 3. a conflicting duplicate fails and names the key.
new_tree
gen 600 a > "${TREE}/sale/de.json"
echo '{"a0":"anders"}' > "${TREE}/staff/de.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "conflicting duplicate fails, names the key" failed_mentioning a0

# 4. a missing translations dir fails.
new_tree
run --translations "${TMP}/does-not-exist" --locale de --out "${OUTDIR}"
t "missing translations dir fails" failed

# 5. bad locale codes fail.
new_tree
gen 600 a > "${TREE}/sale/de.json"
for bad in "pt-BR" "../x" "DE" "d" ""; do
  run --translations "${TREE}" --locale "${bad}" --out "${OUTDIR}"
  t "bad locale '${bad}' refused" failed
done

# 6. an unsafe area id in areas.json fails.
new_tree
echo '{"../evil":{}}' > "${TREE}/meta/areas.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "unsafe area id refused" failed

# 7. the 500-key floor.
new_tree
gen 499 a > "${TREE}/sale/de.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "499 keys fails, mentions 500" failed_mentioning 500
new_tree
gen 500 a > "${TREE}/sale/de.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "exactly 500 keys passes" succeeded

# 8. no area has the locale.
new_tree
gen 600 a > "${TREE}/sale/de.json"
run --translations "${TREE}" --locale es --out "${OUTDIR}"
t "locale absent from every area fails" failed

# 9. output keys are sorted.
new_tree
echo '{"z":"1","a":"2"}' > "${TREE}/staff/de.json"
gen 600 k > "${TREE}/sale/de.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "output keys sorted" overlay_is 'keys_unsorted | first' a

# 10. a symlinked locale file or area dir is refused (it could point outside
#     the clone).
new_tree
gen 600 a > "${TMP}/outside.json"
ln -s "${TMP}/outside.json" "${TREE}/sale/de.json"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "symlinked locale file refused" failed_mentioning symlink
new_tree
mkdir -p "${TMP}/outside-area"
gen 600 a > "${TMP}/outside-area/de.json"
rmdir "${TREE}/sale"
ln -s "${TMP}/outside-area" "${TREE}/sale"
run --translations "${TREE}" --locale de --out "${OUTDIR}"
t "symlinked area dir refused" failed_mentioning symlink

if [[ "${failures}" -gt 0 ]]; then
  echo "build-locale-overlay_test.sh: ${failures} case(s) FAILED"
  exit 1
fi
echo "build-locale-overlay_test.sh: all cases passed"
