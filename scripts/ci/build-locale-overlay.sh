#!/usr/bin/env bash
#
# Build a flat <code>.json locale overlay from a checkout of
# universaltill/ut-translations (ut-docs#4371, ADR-0160 section 9.3).
#
# Tills get languages as signed Weblate bundles (com.universaltill.language-<code>);
# e2e and the de render audit have no installed bundle, so they load German
# through the test-only UT_TEST_I18N_OVERLAY_DIR hook (internal/pages/init.go).
# This script produces that directory: every till area's <area>/<code>.json
# merged into one flat <out>/<code>.json.
#
# Usage:
#   build-locale-overlay.sh --translations <dir> --locale <code> --out <dir>
#   --translations defaults to ${UT_TRANSLATIONS_DIR:-<repo>/../ut-translations}
#
# Reads JSON only (nothing from ut-translations is executed; no token needed;
# CI uses an anonymous public clone). The overlay holds the strings in git,
# including ones Weblate has not approved yet -- fine for checking that German
# renders and long-locale layout holds, never a statement of translation
# quality. What tills get is the approved-only signed bundle.
#
# Rules: area ids come from meta/areas.json (never a glob, so surface folders
# such as website/ cannot leak in); empty / non-string values are skipped; a key
# with two different values across areas fails; fewer than MIN_KEYS keys fails
# (an empty overlay would make German specs "pass" in English); only 2-3 letter
# language codes are accepted (a regional code needs tools/locales.py mapping).
set -euo pipefail

MIN_KEYS=500
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TRANSLATIONS="${UT_TRANSLATIONS_DIR:-${ROOT_DIR}/../ut-translations}"
LOCALE=""
OUT=""

fail() { echo "build-locale-overlay: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --translations) TRANSLATIONS="${2-}"; shift 2 ;;
    --locale) LOCALE="${2-}"; shift 2 ;;
    --out) OUT="${2-}"; shift 2 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[[ "${LOCALE}" =~ ^[a-z]{2,3}$ ]] || fail "--locale '${LOCALE}' must match ^[a-z]{2,3}$ (regional codes are not supported)"
[[ -n "${OUT}" ]] || fail "--out is required"
command -v jq > /dev/null || fail "jq is required"
AREAS_FILE="${TRANSLATIONS}/meta/areas.json"
[[ -f "${AREAS_FILE}" ]] || fail "${AREAS_FILE} not found (is --translations a ut-translations checkout?)"

files=()
while IFS= read -r area; do
  [[ "${area}" =~ ^[a-z0-9-]+$ ]] || fail "unsafe area id in meta/areas.json: '${area}'"
  [[ -f "${TRANSLATIONS}/${area}/${LOCALE}.json" ]] && files+=("${TRANSLATIONS}/${area}/${LOCALE}.json")
done < <(jq -r 'keys[]' "${AREAS_FILE}")

[[ "${#files[@]}" -gt 0 ]] || fail "no <area>/${LOCALE}.json found under ${TRANSLATIONS}"

# Conflicting duplicates: same key, different non-empty string values.
conflicts="$(jq -n -r '
  [inputs | to_entries[] | select((.value | type) == "string" and .value != "")]
  | group_by(.key) | map(select((map(.value) | unique | length) > 1) | .[0].key) | .[]' "${files[@]}")"
[[ -z "${conflicts}" ]] || fail "key(s) with conflicting values across areas: $(sed -n 1,5p <<<"${conflicts}" | tr '\n' ' ')"

merged="$(jq -S -s 'add | with_entries(select((.value | type) == "string" and .value != ""))' "${files[@]}")"
count="$(jq 'length' <<<"${merged}")"
[[ "${count}" -ge "${MIN_KEYS}" ]] || fail "only ${count} ${LOCALE} keys (need at least ${MIN_KEYS}, i.e. 500) -- refusing an overlay that would silently render English"

mkdir -p "${OUT}"
printf '%s\n' "${merged}" > "${OUT}/${LOCALE}.json"
echo "build-locale-overlay: wrote ${count} ${LOCALE} keys to ${OUT}/${LOCALE}.json"
