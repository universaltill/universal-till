#!/usr/bin/env bash
#
# Help-topic breadcrumb guard (ut-docs#4696): a help topic names a settings
# section as "Settings → <Section>". The translated topic must call that
# section what the till's own translated UI calls it (the web/locales
# settings.*.title / .label value, or one of the few other keys the Settings
# page shows as a section name -- extraSectionKeys), or the reader looks for a label that is
# not on screen. guard-help-drift.sh only compares structure, so it cannot
# see this.
#
# This calls the Go half (scripts/ci/checkhelpbreadcrumbs), which maps each
# English breadcrumb to its en.json key and checks the same numbered item of
# every translated topic for that key's value in the locale. A locale whose
# strings are not in web/locales (a language pack, e.g. de) is read from
# $UT_<LOC>_PACK_DIR or the sibling ut-plugin-language-<loc> checkout, and
# skipped with a notice when neither exists. Known mismatches live in
# scripts/ci/i18n-baseline/help-breadcrumb-baseline.txt (shrink-only: a stale
# entry fails). Quoted action/button labels are out of scope.
#
# Arrow direction (ut-docs#5644): a menu path in a right-to-left help topic
# reads with "←", not "→" (the arrow points the way the reader's eye travels).
# Any "→" under web/help/<rtl-locale>/ fails with file:line. The RTL language
# list is read from httpx.IsRTL's `case` line (internal/httpx/httpx.go), the
# one source, so a language added there is covered here too; a dir that does
# not exist is skipped. An unparsable list or a grep error fails closed.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

HTTPX_GO="internal/httpx/httpx.go"
rtl_case="$(awk '/^func IsRTL\(/{f=1} f && /^[[:space:]]*case "/{print; exit}' "${HTTPX_GO}")"
mapfile -t RTL_LOCALES < <(grep -oE '"[a-z]+"' <<<"${rtl_case}" | tr -d '"')
if [ "${#RTL_LOCALES[@]}" -eq 0 ]; then
  echo "guard-help-breadcrumbs: could not read the RTL languages from IsRTL in ${HTTPX_GO} (ut-docs#5644)" >&2
  exit 1
fi
rtl_dirs=()
for loc in "${RTL_LOCALES[@]}"; do
  [ -d "web/help/${loc}" ] && rtl_dirs+=("web/help/${loc}")
done
if [ "${#rtl_dirs[@]}" -gt 0 ]; then
  rc=0
  hits="$(grep -rnF --include='*.md' '→' "${rtl_dirs[@]}")" || rc=$?
  if [ "${rc}" -gt 1 ]; then
    echo "guard-help-breadcrumbs: grep failed (rc=${rc}) scanning RTL help" >&2
    exit "${rc}"
  fi
  if [ "${rc}" -eq 0 ]; then
    echo "${hits}" >&2
    echo "guard-help-breadcrumbs: '→' in an RTL help topic (above); use '←' (ut-docs#5644)" >&2
    exit 1
  fi
fi

go run ./scripts/ci/checkhelpbreadcrumbs -root "${ROOT_DIR}"
